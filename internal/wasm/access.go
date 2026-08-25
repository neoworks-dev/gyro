package wasm

import (
	"fmt"
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Loads, stores and the bulk-memory operations.
//
// A wasm access is a base address on the stack plus a static offset, and
// the sum is what METALS' u32 addressing wants — computed with the
// checked iadd rather than the wrapping wadd, so an address that runs
// past what a u32 can hold traps instead of folding back into range.

// access describes one load or store opcode: the width it touches, and
// the wasm value type it produces or consumes.
type access struct {
	// prim is the METALS primitive the memory instruction names, which
	// fixes both the width and whether a narrow load sign-extends.
	prim *metals.Prim
	// value is the wasm stack type on the other side of the access.
	value valType
	store bool
}

var memoryOps = map[byte]access{
	0x28: {metals.TInt32, valI32, false},  // i32.load
	0x29: {metals.TInt64, valI64, false},  // i64.load
	0x2A: {metals.TF32, valF32, false},    // f32.load
	0x2B: {metals.TF64, valF64, false},    // f64.load
	0x2C: {metals.TInt8, valI32, false},   // i32.load8_s
	0x2D: {metals.TUint8, valI32, false},  // i32.load8_u
	0x2E: {metals.TInt16, valI32, false},  // i32.load16_s
	0x2F: {metals.TUint16, valI32, false}, // i32.load16_u
	0x30: {metals.TInt8, valI64, false},   // i64.load8_s
	0x31: {metals.TUint8, valI64, false},  // i64.load8_u
	0x32: {metals.TInt16, valI64, false},  // i64.load16_s
	0x33: {metals.TUint16, valI64, false}, // i64.load16_u
	0x34: {metals.TInt32, valI64, false},  // i64.load32_s
	0x35: {metals.TUint32, valI64, false}, // i64.load32_u

	0x36: {metals.TInt32, valI32, true},  // i32.store
	0x37: {metals.TInt64, valI64, true},  // i64.store
	0x38: {metals.TF32, valF32, true},    // f32.store
	0x39: {metals.TF64, valF64, true},    // f64.store
	0x3A: {metals.TUint8, valI32, true},  // i32.store8
	0x3B: {metals.TUint16, valI32, true}, // i32.store16
	0x3C: {metals.TUint8, valI64, true},  // i64.store8
	0x3D: {metals.TUint16, valI64, true}, // i64.store16
	0x3E: {metals.TUint32, valI64, true}, // i64.store32
}

func (b *bodyTranslator) memoryAccess(r *reader, a access) error {
	if _, err := r.u32(); err != nil { // alignment hint, which changes nothing
		return err
	}
	offset, err := r.u32()
	if err != nil {
		return err
	}
	if err := b.wantMemory("a memory access"); err != nil {
		return err
	}

	// A store pops its value first: the address was pushed before it.
	var value stackEntry
	if a.store {
		if value, err = b.pop(); err != nil {
			return err
		}
	}
	base, err := b.pop()
	if err != nil {
		return err
	}
	addr := b.address(base, offset)

	if !a.store {
		raw := b.temp(a.prim)
		b.emit(&metals.MemLoad{Dst: raw, Memory: memoryName, Addr: addr, Type: a.prim})
		dst := b.push(a.value)
		if metals.TypesEqual(a.prim, a.value.metalsType()) {
			b.emit(&metals.Copy{Dst: dst, X: raw})
			return nil
		}
		// A narrow load widens into its stack type. Every one of these is
		// a widening, so conv cannot lose anything and cannot abort.
		b.emit(&metals.Conv{Dst: dst, X: raw, To: a.value.metalsType()})
		return nil
	}

	if value.typ != a.value {
		return fmt.Errorf("a store wants a %s and the stack holds a %s", a.value, value.typ)
	}
	source := value.reg
	if !metals.TypesEqual(a.prim, a.value.metalsType()) {
		// A narrowing store keeps the low bytes, which is trunc's whole
		// job; mem.store refuses to make that decision implicitly.
		narrowed := b.temp(a.prim)
		b.emit(&metals.Trunc{Dst: narrowed, X: value.reg, To: a.prim})
		source = narrowed
	}
	b.emit(&metals.MemStore{Memory: memoryName, Addr: addr, Val: source, Type: a.prim})
	return nil
}

// address turns a wasm base address and static offset into the u32
// METALS wants. The bitcast is free: a wasm i32 address is unsigned, and
// only the register's declared type said otherwise.
func (b *bodyTranslator) address(base stackEntry, offset uint32) string {
	unsigned := b.temp(metals.TUint32)
	b.emit(&metals.Bitcast{Dst: unsigned, X: base.reg, To: metals.TUint32})
	if offset == 0 {
		return unsigned
	}
	delta := b.constant(metals.TUint32, strconv.FormatUint(uint64(offset), 10))
	sum := b.temp(metals.TUint32)
	b.emit(&metals.Bin{Dst: sum, Op: "iadd", A: unsigned, B: delta})
	return sum
}

// ---------------------------------------------------------------------------
// The 0xFC prefix
// ---------------------------------------------------------------------------

func (b *bodyTranslator) prefixed(r *reader) error {
	sub, err := r.u32()
	if err != nil {
		return err
	}
	switch sub {
	case 10: // memory.copy
		if _, err := r.u32(); err != nil { // destination memory
			return err
		}
		if _, err := r.u32(); err != nil { // source memory
			return err
		}
		if err := b.wantMemory("memory.copy"); err != nil {
			return err
		}
		length, source, target, err := b.popThree()
		if err != nil {
			return err
		}
		b.emit(&metals.MemCopy{Memory: memoryName, To: target, From: source, Len: length})
		return nil

	case 11: // memory.fill
		if _, err := r.u32(); err != nil { // memory index
			return err
		}
		if err := b.wantMemory("memory.fill"); err != nil {
			return err
		}
		lengthEntry, err := b.pop()
		if err != nil {
			return err
		}
		valueEntry, err := b.pop()
		if err != nil {
			return err
		}
		targetEntry, err := b.pop()
		if err != nil {
			return err
		}
		length := b.unsigned32(lengthEntry.reg)
		target := b.unsigned32(targetEntry.reg)
		fill := b.temp(metals.TUint8)
		b.emit(&metals.Trunc{Dst: fill, X: valueEntry.reg, To: metals.TUint8})
		b.emit(&metals.MemFill{Memory: memoryName, To: target, Byte: fill, Len: length})
		return nil

	case 0, 1, 2, 3, 4, 5, 6, 7:
		return b.truncSat(byte(sub))
	case 8, 9:
		return fmt.Errorf("passive data segments are not translated")
	}
	return fmt.Errorf("prefixed opcode 0xFC %d is not translated", sub)
}

// truncSat is the 0xFC 0..7 block, ordered target-major then source
// then signedness: i32.trunc_sat_f32_s, _u, then f64, then the i64 four.
func (b *bodyTranslator) truncSat(sub byte) error {
	shape := satShape{
		to:       valI32,
		from:     valF32,
		unsigned: sub&1 == 1,
	}
	if sub >= 4 {
		shape.to = valI64
	}
	if sub&2 == 2 {
		shape.from = valF64
	}

	value, err := b.pop()
	if err != nil {
		return err
	}
	if value.typ != shape.from {
		return fmt.Errorf("a saturating conversion wants a %s and the stack holds a %s",
			shape.from, value.typ)
	}
	name, result := b.t.satHelper(shape)
	clamped := b.temp(result)
	b.emit(&metals.Call{Dst: clamped, Callee: name, Args: []string{value.reg}})

	dst := b.push(shape.to)
	if metals.TypesEqual(result, shape.to.metalsType()) {
		b.emit(&metals.Copy{Dst: dst, X: clamped})
		return nil
	}
	b.emit(&metals.Bitcast{Dst: dst, X: clamped, To: shape.to.metalsType()})
	return nil
}

// popThree pops three i32 operands and reads them as u32, which is what
// the bulk-memory operations take. The order is the stack's: the last
// operand pushed comes off first.
func (b *bodyTranslator) popThree() (top, middle, bottom string, err error) {
	topEntry, err := b.pop()
	if err != nil {
		return
	}
	middleEntry, err := b.pop()
	if err != nil {
		return
	}
	bottomEntry, err := b.pop()
	if err != nil {
		return
	}
	return b.unsigned32(topEntry.reg), b.unsigned32(middleEntry.reg), b.unsigned32(bottomEntry.reg), nil
}

// unsigned32 reads an i32 register's bits as a u32.
func (b *bodyTranslator) unsigned32(reg string) string {
	out := b.temp(metals.TUint32)
	b.emit(&metals.Bitcast{Dst: out, X: reg, To: metals.TUint32})
	return out
}
