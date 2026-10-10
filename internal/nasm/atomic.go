package nasm

import "github.com/gyro-lang/gyro/internal/metals"

// Atomics. METALS has one ordering, sequentially consistent, and x86-64
// gives it nearly for free: a load is a plain move, every read-modify-write
// is lock-prefixed, and only a store pays, as an xchg (which locks without
// being asked to) instead of a mov. A fence is mfence.
//
// An operand is the width of the cell it goes through. A register holds
// the value widened to a qword, so the old value an instruction hands back
// is widened again by the cell type's own signedness.

// atomicBits is the access width of a cell holding t, and whether the
// value widens by sign.
func atomicBits(t metals.Type) (bits int, signed bool) {
	prim, ok := t.(*metals.Prim)
	if !ok {
		return 64, false
	}
	switch prim.Kind {
	case metals.I8:
		return 8, true
	case metals.U8, metals.Bool:
		return 8, false
	case metals.I16:
		return 16, true
	case metals.U16:
		return 16, false
	case metals.I32:
		return 32, true
	case metals.U32:
		return 32, false
	}
	return 64, false
}

// view names the part of a register an access of this width uses.
func view(machine string, bits int) string {
	if bits == 64 {
		return machine
	}
	return narrow(machine, bits)
}

var atomicKeyword = map[int]string{8: "byte", 16: "word", 32: "dword", 64: "qword"}

// widenRax makes rax, whose low bits hold a value of the given width, the
// qword a register carries.
func (g *generator) widenRax(bits int, signed bool) {
	switch {
	case bits == 64:
	case bits == 32 && signed:
		g.ins("movsxd rax, eax")
	case bits == 32:
		g.ins("mov eax, eax")
	case signed:
		g.ins("movsx rax, %s", view("rax", bits))
	default:
		g.ins("movzx eax, %s", view("rax", bits))
	}
}

// loadRax reads the cell at [rdx] into rax, zero-extended.
func (g *generator) loadRaxZero(bits int) {
	switch bits {
	case 64:
		g.ins("mov rax, [rdx]")
	case 32:
		g.ins("mov eax, [rdx]")
	default:
		g.ins("movzx eax, %s [rdx]", atomicKeyword[bits])
	}
}

func (f *frame) emitAtomic(instr metals.Instr) {
	g := f.g
	switch i := instr.(type) {
	case *metals.AtomicLoad:
		g.ins("mov rdx, %s", f.slot(i.Ptr))
		f.loadMember(f.slot(i.Dst), memAt(memLoc{base: "rdx"}), f.typeOf(i.Dst))

	case *metals.AtomicStore:
		bits, _ := atomicBits(f.typeOf(i.Val))
		g.ins("mov rax, %s", f.slot(i.Val))
		g.ins("mov rdx, %s", f.slot(i.Ptr))
		g.ins("xchg %s [rdx], %s", atomicKeyword[bits], view("rax", bits))

	case *metals.AtomicRMW:
		bits, signed := atomicBits(f.typeOf(i.Dst))
		keyword := atomicKeyword[bits]
		switch i.Op {
		case "swap":
			g.ins("mov rax, %s", f.slot(i.Val))
			g.ins("mov rdx, %s", f.slot(i.Ptr))
			g.ins("xchg %s [rdx], %s", keyword, view("rax", bits))
		case "add", "sub":
			g.ins("mov rax, %s", f.slot(i.Val))
			if i.Op == "sub" {
				g.ins("neg rax")
			}
			g.ins("mov rdx, %s", f.slot(i.Ptr))
			g.ins("lock xadd %s [rdx], %s", keyword, view("rax", bits))
		default:
			// No instruction hands back the old value of an and, an or
			// or an xor, so the update retries until the cell still held
			// what it was computed from. cmpxchg leaves the current
			// value in rax when it fails.
			g.ins("mov rcx, %s", f.slot(i.Val))
			g.ins("mov rdx, %s", f.slot(i.Ptr))
			g.loadRaxZero(bits)
			retry := g.newLabel()
			g.label(retry)
			g.ins("mov r8, rax")
			g.ins("%s r8, rcx", i.Op)
			g.ins("lock cmpxchg %s [rdx], %s", keyword, view("r8", bits))
			g.ins("jne %s", retry)
		}
		g.widenRax(bits, signed)
		g.storeWork(f.slot(i.Dst), "rax")

	case *metals.AtomicCas:
		bits, signed := atomicBits(f.typeOf(i.Dst))
		g.ins("mov rax, %s", f.slot(i.Expected))
		g.ins("mov rcx, %s", f.slot(i.New))
		g.ins("mov rdx, %s", f.slot(i.Ptr))
		g.ins("lock cmpxchg %s [rdx], %s", atomicKeyword[bits], view("rcx", bits))
		g.widenRax(bits, signed)
		g.storeWork(f.slot(i.Dst), "rax")

	case *metals.Fence:
		g.ins("mfence")
	}
}
