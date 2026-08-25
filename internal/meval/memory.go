package meval

import (
	"math"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Linear memory in the evaluator.
//
// A METALS linear memory is a flat byte slice, and every access into it
// is bounds-checked against the slice's current length — which is the
// property that makes compiling an unsafe language into METALS worth
// doing rather than linking it. A producer that gets its own pointer
// arithmetic wrong aborts here with a message, instead of reaching
// storage that belongs to something else.
//
// The representation is little-endian, unconditionally and everywhere.
// A byte-addressed memory whose meaning depended on the host would make
// a program's output a property of the machine that ran it, and the
// evaluator is the oracle the native backends are diffed against.
//
// The evaluator does not lock a memory against concurrent access, for
// the same reason it does not lock a global's cell: two threads racing
// on one cell is a program's bug, not something the oracle invents an
// answer for.

type linearMemory struct {
	bytes []byte
	// max is the ceiling mem.grow honours, in pages; 0 means the only
	// limit is what a u32 can address.
	max int64
}

// maxPages is what a u32 address space holds: growing past it could not
// be addressed, so mem.grow refuses rather than allocating.
const maxPages = 1 << 16

func (m *machine) memory(name string) *linearMemory {
	mem, ok := m.memories[name]
	if !ok {
		m.throw("no memory @%s", name)
	}
	return mem
}

// span bounds-checks an access and returns the byte range it covers.
// Address arithmetic is done in 64 bits so that an address near the top
// of the u32 range cannot wrap into a valid one.
func (m *machine) span(mem *linearMemory, addr uint64, width uint64) []byte {
	end := addr + width
	if end > uint64(len(mem.bytes)) {
		m.throw("linear memory access out of bounds: %d..%d of %d bytes", addr, end, len(mem.bytes))
	}
	return mem.bytes[addr:end]
}

// address reads an address, length or page count operand, all of which
// the verifier has already required to be a u32.
func (m *machine) address(regs map[string]value, name string) uint64 {
	v, ok := m.get(regs, name).(uintVal)
	if !ok {
		m.throw("linear memory wants a u32 operand")
	}
	return v.v
}

// ---------------------------------------------------------------------------
// Raw bits
// ---------------------------------------------------------------------------

// rawBits is a scalar's byte pattern, zero-extended into 64 bits. It is
// what trunc keeps the low end of, what bitcast reinterprets, and what a
// store writes.
func (m *machine) rawBits(v value) uint64 {
	switch v := v.(type) {
	case intVal:
		return uint64(v.v) & kindMask(v.kind)
	case uintVal:
		return v.v & kindMask(v.kind)
	case floatVal:
		if v.kind == metals.F32 {
			return uint64(math.Float32bits(float32(v.v)))
		}
		return math.Float64bits(v.v)
	}
	m.throw("this value has no byte pattern")
	return 0
}

// fromBits is rawBits' inverse: read a byte pattern back as a primitive,
// sign-extending when the target is signed.
func (m *machine) fromBits(bits uint64, kind metals.PrimKind) value {
	switch kind {
	case metals.F32:
		return floatVal{v: float64(math.Float32frombits(uint32(bits))), kind: kind}
	case metals.F64:
		return floatVal{v: math.Float64frombits(bits), kind: kind}
	case metals.I8, metals.I16, metals.I32, metals.I64, metals.Rune:
		return intVal{v: signExtend(bits&kindMask(kind), kindBits(kind)), kind: kind}
	case metals.U8, metals.U16, metals.U32, metals.U64, metals.Usize:
		return uintVal{v: bits & kindMask(kind), kind: kind}
	}
	m.throw("no byte pattern for this type")
	return nil
}

// kindBits and kindMask are a primitive's width in bits, and the bits it
// can hold. intWidth answers only for the signed kinds, and every one of
// these is byte-addressable.
func kindBits(kind metals.PrimKind) uint {
	return uint(metals.PrimWidth(&metals.Prim{Kind: kind})) * 8
}

func kindMask(kind metals.PrimKind) uint64 { return widthMask(kindBits(kind)) }

// primKind reads the kind out of a type the verifier has already
// established is a primitive.
func (m *machine) primKind(t metals.Type) metals.PrimKind {
	prim, ok := t.(*metals.Prim)
	if !ok {
		m.throw("expected a primitive type, found %s", t)
	}
	return prim.Kind
}

// truncate narrows an integer by keeping its low bits: C's cast, and
// conv's non-trapping sibling.
func (m *machine) truncate(v value, to metals.Type) value {
	kind := m.primKind(to)
	return m.fromBits(m.rawBits(v)&kindMask(kind), kind)
}

// reinterpret rereads the same bits as another type of the same width.
func (m *machine) reinterpret(v value, to metals.Type) value {
	return m.fromBits(m.rawBits(v), m.primKind(to))
}

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

// loadBytes reads a little-endian scalar of the given kind.
func (m *machine) loadBytes(mem *linearMemory, addr uint64, kind metals.PrimKind) value {
	width := metals.PrimWidth(&metals.Prim{Kind: kind})
	span := m.span(mem, addr, uint64(width))
	var bits uint64
	for i := width - 1; i >= 0; i-- {
		bits = bits<<8 | uint64(span[i])
	}
	return m.fromBits(bits, kind)
}

// storeBytes writes a little-endian scalar of the given kind.
func (m *machine) storeBytes(mem *linearMemory, addr uint64, kind metals.PrimKind, v value) {
	width := metals.PrimWidth(&metals.Prim{Kind: kind})
	span := m.span(mem, addr, uint64(width))
	bits := m.rawBits(v)
	for i := 0; i < width; i++ {
		span[i] = byte(bits)
		bits >>= 8
	}
}

// initMemories allocates every declared memory and applies its data
// segments, in declaration order, before any function runs.
func (m *machine) initMemories(module *metals.Module) error {
	m.memories = map[string]*linearMemory{}
	for _, decl := range module.Memories {
		m.memories[decl.Name] = &linearMemory{
			bytes: make([]byte, decl.Min*metals.PageSize),
			max:   decl.Max,
		}
	}
	for _, seg := range module.Data {
		mem, ok := m.memories[seg.Memory]
		if !ok {
			return errNoMemory(seg.Memory)
		}
		copy(mem.bytes[seg.Offset:], seg.Bytes)
	}
	return nil
}

type errNoMemory string

func (e errNoMemory) Error() string { return "no memory @" + string(e) }

// grow extends a memory by whole pages, zero-filled, and answers with
// the size it had before — or -1 when the request cannot be met, which
// is a value the program handles rather than an abort.
func (mem *linearMemory) grow(pages uint64) int32 {
	before := int64(len(mem.bytes)) / metals.PageSize
	after := before + int64(pages)
	limit := int64(maxPages)
	if mem.max > 0 && mem.max < limit {
		limit = mem.max
	}
	if after > limit {
		return -1
	}
	grown := make([]byte, after*metals.PageSize)
	copy(grown, mem.bytes)
	mem.bytes = grown
	return int32(before)
}

// stepMemory runs the linear-memory instructions. It returns false when
// the instruction is not one of them, so the main dispatch can go on.
func (m *machine) stepMemory(instr metals.Instr, regs map[string]value) bool {
	switch i := instr.(type) {
	case *metals.MemLoad:
		mem := m.memory(i.Memory)
		regs[i.Dst] = m.loadBytes(mem, m.address(regs, i.Addr), i.Type.Kind)

	case *metals.MemStore:
		mem := m.memory(i.Memory)
		m.storeBytes(mem, m.address(regs, i.Addr), i.Type.Kind, m.get(regs, i.Val))

	case *metals.MemSize:
		mem := m.memory(i.Memory)
		regs[i.Dst] = uintVal{v: uint64(len(mem.bytes) / metals.PageSize), kind: metals.U32}

	case *metals.MemGrow:
		mem := m.memory(i.Memory)
		regs[i.Dst] = intVal{v: int64(mem.grow(m.address(regs, i.Pages))), kind: metals.I32}

	case *metals.MemCopy:
		mem := m.memory(i.Memory)
		to, from := m.address(regs, i.To), m.address(regs, i.From)
		length := m.address(regs, i.Len)
		// Both ends are checked before either moves, so a copy that would
		// run off the end writes nothing at all.
		m.span(mem, to, length)
		m.span(mem, from, length)
		copy(mem.bytes[to:to+length], mem.bytes[from:from+length])

	case *metals.MemFill:
		mem := m.memory(i.Memory)
		to, length := m.address(regs, i.To), m.address(regs, i.Len)
		fill, ok := m.get(regs, i.Byte).(uintVal)
		if !ok {
			m.throw("mem.fill wants a u8")
		}
		span := m.span(mem, to, length)
		for index := range span {
			span[index] = byte(fill.v)
		}

	default:
		return false
	}
	return true
}
