package nasm

import (
	"fmt"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Linear memory in the assembler backend.
//
// A memory is 24 bytes of .bss holding the gy_memory rt.c declares —
// { bytes, size, max } — brought up by gy_mem_init at the top of main.
// The allocation, growth, bulk operations and abort text are all the
// runtime's, so this file only has to know two things: the offsets of
// that struct, and how to bounds-check and then move a scalar.
//
// The access is emitted inline rather than called. A load is five
// instructions here and a call would be more than that on its own, and
// the whole reason this backend exists beside the C one is to be
// measured on exactly that kind of difference.

// The layout of rt.c's gy_memory. A change there is a change here, which
// is why both are one struct of three words and nothing more.
const (
	memBytesOffset = 0
	memSizeOffset  = 8
	memPageShift   = 16 // 65536 bytes to the page
	memStructSize  = 24
)

// memorySymbol names a memory's storage, reserving it on first use.
func (g *generator) memorySymbol(name string) string {
	if symbol, ok := g.memorySyms[name]; ok {
		return symbol
	}
	if _, ok := g.memories[name]; !ok {
		g.fail("no memory @%s", name)
	}
	symbol := uniqueSymbol("M_"+sanitize(name), g.usedSyms)
	g.usedSyms[symbol] = true
	g.memorySyms[name] = symbol

	g.bss.WriteString(symbol + ":\n")
	fmt.Fprintf(&g.bss, "\tresb %d\n", memStructSize)
	return symbol
}

// genMemoryInit emits the calls that bring every memory up. It runs at
// the top of main, which in this backend is the entry point itself.
func (g *generator) genMemoryInit() {
	if len(g.module.Memories) == 0 {
		return
	}
	g.comment("linear memory")
	for _, decl := range g.module.Memories {
		symbol := g.memorySymbol(decl.Name)
		g.ins("lea rdi, [%s]", symbol)
		g.ins("mov rsi, %d", decl.Min)
		g.ins("mov rdx, %d", decl.Max)
		g.ins("call %s", g.extern("gy_mem_init"))
	}
	for index, seg := range g.module.Data {
		if len(seg.Bytes) == 0 {
			continue
		}
		symbol := g.memorySymbol(seg.Memory)
		blob := g.dataSegment(index, seg.Bytes)
		g.ins("lea rdi, [%s]", symbol)
		g.ins("mov rsi, %d", seg.Offset)
		g.ins("lea rdx, [%s]", blob)
		g.ins("mov rcx, %d", len(seg.Bytes))
		g.ins("call %s", g.extern("gy_mem_data"))
	}
}

// dataSegment lays a segment's bytes into .rodata and names them.
func (g *generator) dataSegment(index int, bytes []byte) string {
	label := fmt.Sprintf("D%d", index)
	g.data.WriteString(label + ":\n")
	for start := 0; start < len(bytes); start += 24 {
		end := min(start+24, len(bytes))
		g.data.WriteString("\tdb ")
		for i := start; i < end; i++ {
			if i > start {
				g.data.WriteString(", ")
			}
			fmt.Fprintf(&g.data, "%d", bytes[i])
		}
		g.data.WriteString("\n")
	}
	return label
}

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

// accessBase bounds-checks an address and leaves rcx pointing at the
// byte it names. The address arrives in a u32 register, which is carried
// zero-extended in a whole word, so the 64-bit arithmetic below cannot
// wrap into a valid range.
func (f *frame) accessBase(memory, addr string, width int) {
	g := f.g
	symbol := g.memorySymbol(memory)
	g.ins("mov rax, %s", f.slot(addr))
	g.ins("lea rdx, [rax+%d]", width)
	g.ins("mov rcx, [%s+%d]", symbol, memSizeOffset)
	g.ins("cmp rdx, rcx")
	ok := g.newLabel()
	g.ins("jbe %s", ok)
	// gy_abort_mem_bounds(addr, end, size); it does not return, so
	// nothing here has to be preserved.
	g.ins("mov rdi, rax")
	g.ins("mov rsi, rdx")
	g.ins("mov rdx, rcx")
	g.ins("call %s", g.extern("gy_abort_mem_bounds"))
	g.label(ok)
	g.ins("mov rcx, [%s+%d]", symbol, memBytesOffset)
}

// loadForms is the instruction that reads each width into rax, with the
// extension the type's signedness calls for. u32 is a plain 32-bit move
// because writing eax clears the upper half.
var loadForms = map[metals.PrimKind]string{
	metals.I8:  "movsx rax, byte [rcx+rax]",
	metals.U8:  "movzx rax, byte [rcx+rax]",
	metals.I16: "movsx rax, word [rcx+rax]",
	metals.U16: "movzx rax, word [rcx+rax]",
	metals.I32: "movsxd rax, dword [rcx+rax]",
	metals.U32: "mov eax, dword [rcx+rax]",
	metals.I64: "mov rax, qword [rcx+rax]",
	metals.U64: "mov rax, qword [rcx+rax]",
}

// storeForms is the matching write, taking the value from rdx.
var storeForms = map[metals.PrimKind]string{
	metals.I8:  "mov byte [rcx+rax], dl",
	metals.U8:  "mov byte [rcx+rax], dl",
	metals.I16: "mov word [rcx+rax], dx",
	metals.U16: "mov word [rcx+rax], dx",
	metals.I32: "mov dword [rcx+rax], edx",
	metals.U32: "mov dword [rcx+rax], edx",
	metals.I64: "mov qword [rcx+rax], rdx",
	metals.U64: "mov qword [rcx+rax], rdx",
}

// emitMemory generates the linear-memory instructions and reports
// whether the instruction was one of them.
func (f *frame) emitMemory(instr metals.Instr) bool {
	g := f.g
	switch i := instr.(type) {
	case *metals.MemLoad:
		f.accessBase(i.Memory, i.Addr, metals.PrimWidth(i.Type))
		switch i.Type.Kind {
		case metals.F32:
			// A float register carries an f32 as the double it widens to,
			// the same as everywhere else in this backend.
			g.ins("movss xmm0, dword [rcx+rax]")
			g.ins("cvtss2sd xmm0, xmm0")
			g.ins("movsd %s, xmm0", f.slot(i.Dst))
		case metals.F64:
			g.ins("movsd xmm0, qword [rcx+rax]")
			g.ins("movsd %s, xmm0", f.slot(i.Dst))
		default:
			g.ins("%s", loadForms[i.Type.Kind])
			f.storeWork(f.slot(i.Dst), "rax")
		}

	case *metals.MemStore:
		// The value is read before the address check clobbers rdx, so a
		// value living in rdx is not lost.
		switch i.Type.Kind {
		case metals.F32:
			g.ins("movsd xmm0, %s", f.slot(i.Val))
			g.ins("cvtsd2ss xmm0, xmm0")
			f.accessBase(i.Memory, i.Addr, 4)
			g.ins("movss dword [rcx+rax], xmm0")
		case metals.F64:
			g.ins("movsd xmm0, %s", f.slot(i.Val))
			f.accessBase(i.Memory, i.Addr, 8)
			g.ins("movsd qword [rcx+rax], xmm0")
		default:
			g.ins("mov r8, %s", f.slot(i.Val))
			f.accessBase(i.Memory, i.Addr, metals.PrimWidth(i.Type))
			g.ins("mov rdx, r8")
			g.ins("%s", storeForms[i.Type.Kind])
		}

	case *metals.MemSize:
		symbol := g.memorySymbol(i.Memory)
		g.ins("mov rax, [%s+%d]", symbol, memSizeOffset)
		g.ins("shr rax, %d", memPageShift)
		f.storeWork(f.slot(i.Dst), "rax")

	case *metals.MemGrow:
		symbol := g.memorySymbol(i.Memory)
		g.ins("mov rsi, %s", f.slot(i.Pages))
		g.ins("lea rdi, [%s]", symbol)
		g.ins("call %s", g.extern("gy_mem_grow"))
		f.storeWork(f.slot(i.Dst), "rax")

	case *metals.MemCopy:
		f.bulk(i.Memory, "gy_mem_copy", i.To, i.From, i.Len)

	case *metals.MemFill:
		f.bulk(i.Memory, "gy_mem_fill", i.To, i.Byte, i.Len)

	default:
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// trunc and bitcast
// ---------------------------------------------------------------------------

// Both are the same two steps — get the source's byte pattern, then read
// it back as the target — which is what makes them one pair of helpers
// rather than a table of cases. trunc's own narrowing is the second
// step's, because keeping the low bits of a wider value and reading a
// narrower type's bits are the same instruction.

// narrowForms reads rax back as a narrower type, extending by the type's
// own signedness. The wide types are absent: there is nothing to do.
var narrowForms = map[metals.PrimKind]string{
	metals.I8:   "movsx rax, al",
	metals.U8:   "movzx rax, al",
	metals.I16:  "movsx rax, ax",
	metals.U16:  "movzx rax, ax",
	metals.I32:  "movsxd rax, eax",
	metals.Rune: "movsxd rax, eax",
	metals.U32:  "mov eax, eax",
}

// bitsIntoRax leaves a value's byte pattern in rax. A float register
// carries an f32 as a double, so its pattern is the single it narrows
// to, not the double's.
func (f *frame) bitsIntoRax(src loc, prim *metals.Prim) {
	g := f.g
	switch prim.Kind {
	case metals.F32:
		g.ins("movsd xmm0, %s", src)
		g.ins("cvtsd2ss xmm0, xmm0")
		g.ins("movd eax, xmm0")
	case metals.F64:
		g.ins("movsd xmm0, %s", src)
		g.ins("movq rax, xmm0")
	default:
		g.ins("mov rax, %s", src)
	}
}

// fromRaxInto is the inverse: write rax's pattern into dst as a value of
// the target type.
func (f *frame) fromRaxInto(dst loc, prim *metals.Prim) {
	g := f.g
	switch prim.Kind {
	case metals.F32:
		g.ins("movd xmm0, eax")
		g.ins("cvtss2sd xmm0, xmm0")
		g.ins("movsd %s, xmm0", dst)
		return
	case metals.F64:
		g.ins("movq xmm0, rax")
		g.ins("movsd %s, xmm0", dst)
		return
	}
	if form, ok := narrowForms[prim.Kind]; ok {
		g.ins("%s", form)
	}
	f.storeWork(dst, "rax")
}

func (f *frame) emitReinterpret(dst, x string, to metals.Type) {
	g := f.g
	if _, folded := f.consts[dst]; folded {
		return
	}
	source, ok := f.typeOf(x).(*metals.Prim)
	if !ok {
		g.fail("@%s: %%%s is a %s, which has no byte pattern", f.fn.Name, x, f.typeOf(x))
	}
	target, ok := to.(*metals.Prim)
	if !ok {
		g.fail("@%s: no byte pattern for a %s", f.fn.Name, to)
	}
	f.bitsIntoRax(f.slot(x), source)
	f.fromRaxInto(f.slot(dst), target)
}

// bulk calls one of the runtime's whole-span operations. Both take the
// memory and three words, so they share this.
func (f *frame) bulk(memory, symbol, a, b, c string) {
	g := f.g
	// The operands are read into their argument registers before rdi is
	// set, because one of them may live where rdi is about to go.
	g.ins("mov rsi, %s", f.slot(a))
	g.ins("mov rdx, %s", f.slot(b))
	g.ins("mov rcx, %s", f.slot(c))
	g.ins("lea rdi, [%s]", g.memorySymbol(memory))
	g.ins("call %s", g.extern(symbol))
}
