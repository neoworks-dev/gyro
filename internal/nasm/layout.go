package nasm

import (
	"github.com/gyro-lang/gyro/internal/metals"
)

// Layout rules, from docs/appendix-e-native-backend.md: storage is C's — natural widths,
// natural alignment, padding where C pads — and a register is one qword,
// with an f32 carried as the double it widens to. The arithmetic is
// metals.Layout's, so that this backend and the C one cannot disagree
// about where a field sits; what lives here is the move between the two
// representations, which is machine code and belongs to a backend.

const wordSize = metals.Word

// sizeOf is a type's storage size: what it occupies in memory the
// program addresses.
func (g *generator) sizeOf(t metals.Type) int { return g.layout.Size(t) }

// slotOf is what a register's own storage costs: a qword for a scalar,
// the storage size for an aggregate, which lives inline.
func (g *generator) slotOf(t metals.Type) int { return g.layout.Slot(t) }

func (g *generator) namedSize(name string) int {
	return g.layout.Size(&metals.Named{Name: name})
}

// fieldOffset returns a record field's offset inside the record.
func (g *generator) fieldOffset(record *metals.RecordShape, index int) int {
	return g.layout.FieldOffset(record, index)
}

// variantFieldOffset returns a payload field's offset inside the enum,
// tag included.
func (g *generator) variantFieldOffset(enum *metals.EnumShape, variant, index int) int {
	return g.layout.VariantFieldOffset(enum, variant, index)
}

// tupleOffset returns an element's offset inside the tuple.
func (g *generator) tupleOffset(tuple *metals.TupleOf, index int) int {
	return g.layout.TupleOffset(tuple, index)
}

// ---------------------------------------------------------------------------
// Between storage and a register
// ---------------------------------------------------------------------------

// narrowAccess is the instruction and operand-size keyword that move one
// narrow integer between memory and rax. A load extends by the type's own
// signedness; u32's plain 32-bit move is the extension, since writing eax
// clears the upper half.
var narrowAccess = map[metals.PrimKind]struct {
	load    string
	keyword string
	bits    int
}{
	metals.I8:   {"movsx", "byte", 8},
	metals.U8:   {"movzx", "byte", 8},
	metals.Bool: {"movzx", "byte", 8},
	metals.I16:  {"movsx", "word", 16},
	metals.U16:  {"movzx", "word", 16},
	metals.I32:  {"movsxd", "dword", 32},
	metals.Rune: {"movsxd", "dword", 32},
	metals.U32:  {"mov", "dword", 32},
}

// loadMember reads a value out of storage into a register's slot,
// widening a narrow scalar and rounding an f32 up to the double a float
// register carries. Clobbers rax and xmm0.
func (g *generator) loadMember(dst, src loc, t metals.Type) {
	prim, narrow := narrowPrim(t)
	if !narrow {
		g.copyMem(dst, src, g.slotOf(t))
		return
	}
	if prim.Kind == metals.F32 {
		g.ins("movss xmm0, %s", src.sized("dword"))
		g.ins("cvtss2sd xmm0, xmm0")
		g.ins("movsd %s, xmm0", dst)
		return
	}
	access := narrowAccess[prim.Kind]
	if access.load == "mov" {
		g.ins("mov eax, %s", src.sized(access.keyword))
	} else {
		g.ins("%s rax, %s", access.load, src.sized(access.keyword))
	}
	g.storeWork(dst, "rax")
}

// storeMember is the inverse: a register's value into storage, narrowed
// to the type's own width. Clobbers rax and xmm0.
func (g *generator) storeMember(dst, src loc, t metals.Type) {
	prim, narrow := narrowPrim(t)
	if !narrow {
		g.copyMem(dst, src, g.slotOf(t))
		return
	}
	if prim.Kind == metals.F32 {
		g.ins("movsd xmm0, %s", src)
		g.ins("cvtsd2ss xmm0, xmm0")
		g.ins("movss %s, xmm0", dst.sized("dword"))
		return
	}
	access := narrowAccess[prim.Kind]
	g.loadWork("rax", src)
	g.ins("mov %s, %s", dst.sized(access.keyword), narrow64(access.bits))
}

// narrow64 names the view of rax an access of this width writes.
func narrow64(bits int) string { return narrow("rax", bits) }

// narrowPrim reports the primitive whose storage differs from what a
// register holds, which is the only case either move has to think about.
func narrowPrim(t metals.Type) (*metals.Prim, bool) {
	prim, ok := t.(*metals.Prim)
	if !ok || !metals.Narrow(t) {
		return nil, false
	}
	return prim, true
}

// ---------------------------------------------------------------------------
// Staging
// ---------------------------------------------------------------------------

// The runtime stores what it holds — a channel's elements, an Array's,
// a mutex's value, a future's result — in storage representation, so
// that a slice of an Array is the same bytes a C function would read.
// A narrow scalar therefore cannot be handed to the runtime as a pointer
// to its register slot: the slot holds a widened value, and an f32's slot
// holds a double. It goes through one staging cell per frame instead,
// which is a word wide because no scalar's storage is wider.

func (f *frame) stage() loc { return memAt(memLoc{"rbp", f.stageAt}) }

// storeReturn writes a scalar a C call just returned — in rax, or in
// xmm0 for a float — into a cell that holds storage representation.
func (g *generator) storeReturn(cell memLoc, t metals.Type) {
	prim, ok := t.(*metals.Prim)
	switch {
	case ok && prim.Kind == metals.F32:
		g.ins("cvtsd2ss xmm0, xmm0")
		g.ins("movss dword %s, xmm0", cell)
	case ok && prim.Kind == metals.F64:
		g.ins("movsd %s, xmm0", cell)
	default:
		g.storeMember(memAt(cell), regAt("rax"), t)
	}
}

// elementOut addresses a register's value for a runtime call that reads
// it, narrowing it into the staging cell first when storage and register
// disagree.
func (f *frame) elementOut(reg string) memLoc {
	t := f.typeOf(reg)
	if !metals.Narrow(t) {
		return f.addr(reg)
	}
	cell := f.stage()
	f.storeMember(cell, f.slot(reg), t)
	return cell.address()
}

// elementIn addresses the cell a runtime call writes its answer into,
// which elementBack then reads into the register.
func (f *frame) elementIn(reg string) memLoc {
	if !metals.Narrow(f.typeOf(reg)) {
		return f.addr(reg)
	}
	return f.stage().address()
}

func (f *frame) elementBack(reg string) {
	t := f.typeOf(reg)
	if !metals.Narrow(t) {
		return
	}
	f.loadMember(f.slot(reg), f.stage(), t)
}

// ---------------------------------------------------------------------------
// Argument classes
// ---------------------------------------------------------------------------

// argClass says how a value travels in a call: integer register, SSE
// register, or by pointer to the caller's value.
type argClass int

const (
	classInt argClass = iota
	classSSE
	classMem
)

func (g *generator) classOf(t metals.Type) argClass {
	switch t := t.(type) {
	case *metals.Prim:
		if t.Kind == metals.F32 || t.Kind == metals.F64 {
			return classSSE
		}
		return classInt
	case *metals.Ptr, *metals.AnyPtr, *metals.ChanOf, *metals.FutureOf, *metals.Handle, *metals.CodeType:
		return classInt
	case *metals.Opt:
		// A niche optional is a pointer, and travels as one.
		if metals.NicheOpt(t) {
			return classInt
		}
	}
	return classMem
}

// intKindInfo describes an integer primitive for the arithmetic emitter.
type intKindInfo struct {
	width  int // bits
	signed bool
}

var intKinds = map[metals.PrimKind]intKindInfo{
	metals.I8:    {8, true},
	metals.I16:   {16, true},
	metals.I32:   {32, true},
	metals.I64:   {64, true},
	metals.Rune:  {32, true},
	metals.U8:    {8, false},
	metals.U16:   {16, false},
	metals.U32:   {32, false},
	metals.U64:   {64, false},
	metals.Usize: {64, false},
	metals.Bool:  {8, false},
}
