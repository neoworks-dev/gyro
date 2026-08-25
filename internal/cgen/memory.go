package cgen

import (
	"fmt"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Linear memory in the C backend.
//
// A memory is one file-scope gy_memory, allocated by gy_mem_init before
// main runs and initialized from its data segments there too — METALS
// carries no initializer for a global either, and a data segment is the
// same idea one level down.
//
// The access itself is inlined from the prelude rather than called in
// rt.c, because the width is a literal at every call site: cc constant-
// folds it through gy_mem_read's memcpy and emits a single load. That is
// the one place this backend's output differs from the assembler's in
// kind rather than in spelling, and it is the hot path of every program
// a C front end will produce.

// memorySymbol names a memory's storage, reserving it on first use.
func (g *generator) memorySymbol(name string) string {
	if symbol, ok := g.memorySyms[name]; ok {
		return symbol
	}
	decl, ok := g.memories[name]
	if !ok {
		g.fail("no memory @%s", name)
	}
	symbol := uniqueSymbol("M_"+sanitize(name), g.usedSyms)
	g.usedSyms[symbol] = true
	g.memorySyms[name] = symbol
	fmt.Fprintf(&g.data, "static gy_memory %s; /* @%s, %d page(s) */\n",
		symbol, comment(name), decl.Min)
	return symbol
}

// genMemoryInit emits the statements that bring every declared memory
// up before main: the allocation, then each data segment copied in.
//
// Segments are emitted as byte blobs rather than as initializers of the
// memory itself, because the memory is one flat allocation whose size is
// a page count, not an aggregate C can spell an initializer for.
func (g *generator) genMemoryInit() []string {
	var out []string
	for _, decl := range g.module.Memories {
		symbol := g.memorySymbol(decl.Name)
		out = append(out, fmt.Sprintf("gy_mem_init(&%s, %d, %d);", symbol, decl.Min, decl.Max))
	}
	for index, seg := range g.module.Data {
		if len(seg.Bytes) == 0 {
			continue
		}
		symbol := g.memorySymbol(seg.Memory)
		blob := fmt.Sprintf("D%d", index)
		fmt.Fprintf(&g.data, "static const uint8_t %s[%d] = %s;\n",
			blob, len(seg.Bytes), byteArrayLiteral(seg.Bytes))
		out = append(out, fmt.Sprintf("gy_mem_data(&%s, %d, %s, %d);",
			symbol, seg.Offset, blob, len(seg.Bytes)))
	}
	return out
}

// byteArrayLiteral renders a data segment as a braced list. A C string
// literal would be shorter, but it carries a NUL the array must not have
// and a length that has to be reasoned about; a list of numbers says
// exactly what is there.
func byteArrayLiteral(bytes []byte) string {
	var out strings.Builder
	out.WriteByte('{')
	for i, b := range bytes {
		if i > 0 {
			out.WriteByte(',')
		}
		// Twelve per line keeps a large segment readable in the generated
		// file, which is the backend's other job.
		if i%12 == 0 {
			out.WriteString("\n    ")
		}
		fmt.Fprintf(&out, "%d", b)
	}
	out.WriteString("\n}")
	return out.String()
}

// ---------------------------------------------------------------------------
// Instructions
// ---------------------------------------------------------------------------

// emitMemory generates the linear-memory instructions, and reports
// whether the instruction was one of them.
func (f *frame) emitMemory(instr metals.Instr) bool {
	g := f.g
	switch i := instr.(type) {
	case *metals.MemLoad:
		mem := g.memorySymbol(i.Memory)
		read := fmt.Sprintf("gy_mem_read(&%s, %s, %d)", mem, f.reg(i.Addr), metals.PrimWidth(i.Type))
		f.assign(i.Dst, fromBits(i.Type, read))

	case *metals.MemStore:
		mem := g.memorySymbol(i.Memory)
		f.line("gy_mem_write(&%s, %s, %d, %s);",
			mem, f.reg(i.Addr), metals.PrimWidth(i.Type), toBits(i.Type, f.reg(i.Val)))

	case *metals.MemSize:
		mem := g.memorySymbol(i.Memory)
		f.assign(i.Dst, fmt.Sprintf("(%s.size / 65536)", mem))

	case *metals.MemGrow:
		mem := g.memorySymbol(i.Memory)
		f.assign(i.Dst, fmt.Sprintf("gy_mem_grow(&%s, %s)", mem, f.reg(i.Pages)))

	case *metals.MemCopy:
		mem := g.memorySymbol(i.Memory)
		f.line("gy_mem_copy(&%s, %s, %s, %s);", mem, f.reg(i.To), f.reg(i.From), f.reg(i.Len))

	case *metals.MemFill:
		mem := g.memorySymbol(i.Memory)
		f.line("gy_mem_fill(&%s, %s, %s, %s);", mem, f.reg(i.To), f.reg(i.Byte), f.reg(i.Len))

	default:
		return false
	}
	return true
}

// fromBits reads a raw byte pattern back as a register of the given
// primitive. A register is one qword whatever its declared width, so a
// narrow signed type has to be sign-extended into it explicitly.
func fromBits(prim *metals.Prim, bits string) string {
	width := metals.PrimWidth(prim) * 8
	switch prim.Kind {
	case metals.F32:
		return "gy_float_bits(" + bits + ")"
	case metals.F64:
		return "gy_double_bits(" + bits + ")"
	}
	if intKinds[prim.Kind].signed {
		return fmt.Sprintf("gy_wrap_s(%s, %d)", bits, width)
	}
	return fmt.Sprintf("gy_wrap_u(%s, %d)", bits, width)
}

// toBits is the inverse: a register's byte pattern, ready to be written.
// gy_mem_write keeps only the low bytes of the width it is given, so an
// integer needs no masking here.
func toBits(prim *metals.Prim, reg string) string {
	switch prim.Kind {
	case metals.F32:
		return "gy_bits_of_float(" + reg + ")"
	case metals.F64:
		return "gy_bits_of_double(" + reg + ")"
	}
	return "(uint64_t)" + reg
}

// emitTrunc narrows an integer by keeping its low bits, and emitBitcast
// rereads the same bits as another type of the same width. Neither
// aborts, which is what separates both from conv.
func (f *frame) emitTrunc(i *metals.Trunc) {
	target, ok := i.To.(*metals.Prim)
	if !ok {
		f.g.fail("@%s: trunc to a %s", f.fn.Name, i.To)
	}
	f.assign(i.Dst, fromBits(target, "(uint64_t)"+f.reg(i.X)))
}

func (f *frame) emitBitcast(i *metals.Bitcast) {
	source, ok := f.typeOf(i.X).(*metals.Prim)
	if !ok {
		f.g.fail("@%s: bitcast from a %s", f.fn.Name, f.typeOf(i.X))
	}
	target, ok := i.To.(*metals.Prim)
	if !ok {
		f.g.fail("@%s: bitcast to a %s", f.fn.Name, i.To)
	}
	f.assign(i.Dst, fromBits(target, toBits(source, f.reg(i.X))))
}
