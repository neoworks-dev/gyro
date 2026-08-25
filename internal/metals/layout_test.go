package metals

import "testing"

// The layout is the format's answer to "where does this sit", and both
// backends take it on trust — the C one asserts it against `cc`, the
// assembler cannot. So the padding cases are checked here directly:
// they are exactly the ones a hand-written "add up the field sizes"
// would get right until the first narrow field.

func testLayout(t *testing.T, decls ...*TypeDecl) *Layout {
	t.Helper()
	return NewLayout(&Module{Types: decls}, func(format string, args ...any) {
		t.Fatalf("layout failed: "+format, args...)
	})
}

func record(name string, fields ...Type) *TypeDecl {
	shape := &RecordShape{}
	for i, field := range fields {
		shape.Fields = append(shape.Fields, FieldDecl{Name: string(rune('a' + i)), Type: field})
	}
	return &TypeDecl{Name: name, Record: shape}
}

func TestLayoutScalarStorage(t *testing.T) {
	l := testLayout(t)
	cases := []struct {
		t          Type
		size, slot int
	}{
		{TInt8, 1, 8},
		{TBool, 1, 8},
		{TUint16, 2, 8},
		{TInt32, 4, 8},
		{TRune, 4, 8},
		{TF32, 4, 8},
		{TF64, 8, 8},
		{TUsize, 8, 8},
		{TStr, 8, 8},
		{&Ptr{Elem: TInt32}, 8, 8},
		{&SliceOf{Elem: TUint8}, 16, 16},
		{&Any{}, 16, 16},
	}
	for _, c := range cases {
		if got := l.Size(c.t); got != c.size {
			t.Errorf("Size(%s) = %d, want %d", c.t, got, c.size)
		}
		if got := l.Slot(c.t); got != c.slot {
			t.Errorf("Slot(%s) = %d, want %d", c.t, got, c.slot)
		}
	}
}

func TestLayoutRecordPadsLikeC(t *testing.T) {
	// struct { uint8_t a; uint32_t b; uint8_t c; } is 12 bytes: b is
	// pushed to 4 and the tail is rounded up to b's alignment.
	decl := record("Mixed", TUint8, TUint32, TUint8)
	l := testLayout(t, decl)
	named := &Named{Name: "Mixed"}

	if got := l.Size(named); got != 12 {
		t.Errorf("Size(%%Mixed) = %d, want 12", got)
	}
	if got := l.Align(named); got != 4 {
		t.Errorf("Align(%%Mixed) = %d, want 4", got)
	}
	for index, want := range []int{0, 4, 8} {
		if got := l.FieldOffset(decl.Record, index); got != want {
			t.Errorf("FieldOffset(%d) = %d, want %d", index, got, want)
		}
	}
}

func TestLayoutNestedRecordCarriesItsAlignment(t *testing.T) {
	inner := record("Inner", TUint8, TInt64) // 16 bytes, aligned to 8
	outer := record("Outer", TUint8, &Named{Name: "Inner"})
	l := testLayout(t, inner, outer)

	if got := l.Size(&Named{Name: "Inner"}); got != 16 {
		t.Errorf("Size(%%Inner) = %d, want 16", got)
	}
	if got := l.FieldOffset(outer.Record, 1); got != 8 {
		t.Errorf("Inner sits at %d, want 8", got)
	}
	if got := l.Size(&Named{Name: "Outer"}); got != 24 {
		t.Errorf("Size(%%Outer) = %d, want 24", got)
	}
}

func TestLayoutArrayHasNoPaddingBetweenElements(t *testing.T) {
	l := testLayout(t)
	if got := l.Size(&ArrayOf{Elem: TUint8, Len: 3}); got != 3 {
		t.Errorf("Size(array<u8, 3>) = %d, want 3", got)
	}
	if got := l.Size(&ArrayOf{Elem: TInt32, Len: 5}); got != 20 {
		t.Errorf("Size(array<i32, 5>) = %d, want 20", got)
	}
	if got := l.Align(&ArrayOf{Elem: TInt32, Len: 5}); got != 4 {
		t.Errorf("Align(array<i32, 5>) = %d, want 4", got)
	}
}

func TestLayoutTupleOfElementAndFlag(t *testing.T) {
	// What a receive and an Array pop answer with. The flag sits at the
	// element's own width, which is where a C struct would put it.
	l := testLayout(t)
	tuple := &TupleOf{Elems: []Type{TInt32, TBool}}
	if got := l.TupleOffset(tuple, 1); got != 4 {
		t.Errorf("flag at %d, want 4", got)
	}
	if got := l.Size(tuple); got != 8 {
		t.Errorf("Size(tuple<i32, bool>) = %d, want 8", got)
	}

	wide := &TupleOf{Elems: []Type{TStr, TBool}}
	if got := l.TupleOffset(wide, 1); got != 8 {
		t.Errorf("flag at %d, want 8", got)
	}
}

func TestLayoutOptAndEnumKeepTheirTagWord(t *testing.T) {
	l := testLayout(t, &TypeDecl{Name: "Shape", Enum: &EnumShape{Variants: []VariantDecl{
		{Name: "None"},
		{Name: "Circle", Payload: []Type{TUint8, TF64}},
	}}})

	// A payload is laid out like a C struct, one word in.
	if got := l.Size(&Named{Name: "Shape"}); got != 24 {
		t.Errorf("Size(%%Shape) = %d, want 24", got)
	}
	enum := &EnumShape{Variants: []VariantDecl{{Name: "Circle", Payload: []Type{TUint8, TF64}}}}
	if got := l.VariantFieldOffset(enum, 0, 1); got != 16 {
		t.Errorf("second payload field at %d, want 16", got)
	}

	// An optional's flag is a whole word, so its payload starts at 8.
	if got := l.Size(&Opt{Elem: TInt32}); got != 16 {
		t.Errorf("Size(opt<i32>) = %d, want 16", got)
	}
	if got := l.Size(&Opt{Elem: TUint8}); got != 16 {
		t.Errorf("Size(opt<u8>) = %d, want 16", got)
	}
}

// A callable is one word or two depending on whether it carries an
// environment, and C's is the one-word one. These are the numbers a C
// program's `sizeof` reads back, so they are checked as numbers.
func TestLayoutCallablesAreOneWordOrTwo(t *testing.T) {
	l := testLayout(t)
	code := &CodeType{Params: []Type{TInt32}, Result: TInt32}
	fn := &FnType{Params: []Type{TInt32}, Result: TInt32}

	if got := l.Size(code); got != 8 {
		t.Errorf("Size(%s) = %d, want 8", code, got)
	}
	if got := l.Size(fn); got != 16 {
		t.Errorf("Size(%s) = %d, want 16", fn, got)
	}

	// The niche covers a code pointer for the same reason it covers a
	// data pointer: every one of them is an address METALS handed out,
	// so no live value is null.
	nullable := &Opt{Elem: code}
	if !NicheOpt(nullable) {
		t.Fatalf("opt<code> is not a niche optional")
	}
	if got := l.Size(nullable); got != 8 {
		t.Errorf("Size(%s) = %d, want 8", nullable, got)
	}
	if NicheOpt(&Opt{Elem: fn}) {
		t.Errorf("opt<fn> has no niche to put a flag in")
	}

	// A struct of callbacks is C's struct: three addresses, no padding,
	// and a fourth member right behind them.
	decl := record("Table", code, code, code, TInt32)
	if got := testLayout(t, decl).Size(&Named{Name: "Table"}); got != 32 {
		t.Errorf("Size(%%Table) = %d, want 32", got)
	}
}

// An erased pointer costs what a pointer costs. That is the whole
// reason the type is a word and not a box: `sizeof(void *)` is 8 in C,
// and a struct with one in it has C's layout only if that holds here.
func TestLayoutAnyPtrIsAPointersWorth(t *testing.T) {
	l := testLayout(t)
	erased := &AnyPtr{}

	if got := l.Size(erased); got != 8 {
		t.Errorf("Size(anyptr) = %d, want 8", got)
	}
	if got := l.Align(erased); got != 8 {
		t.Errorf("Align(anyptr) = %d, want 8", got)
	}
	if !Scalar(erased) {
		t.Errorf("anyptr is not a scalar, and it is one word in a register")
	}

	// The niche covers it for the reason it covers the two it already
	// covered: an anyptr is one of those with its type taken away, not
	// an address someone invented, so the null pattern is still free.
	nullable := &Opt{Elem: erased}
	if !NicheOpt(nullable) {
		t.Fatalf("opt<anyptr> is not a niche optional")
	}
	if got := l.Size(nullable); got != 8 {
		t.Errorf("Size(%s) = %d, want 8", nullable, got)
	}

	// C's `struct { int kind; void *data; }`: four bytes, four of
	// padding, and the address on its own alignment.
	decl := record("Handle", TInt32, nullable)
	if got := testLayout(t, decl).Size(&Named{Name: "Handle"}); got != 16 {
		t.Errorf("Size(%%Handle) = %d, want 16", got)
	}
}

func TestLayoutNarrowNamesTheConvertingTypes(t *testing.T) {
	narrow := []Type{TInt8, TUint8, TBool, TInt16, TUint16, TInt32, TUint32, TRune, TF32}
	for _, t2 := range narrow {
		if !Narrow(t2) {
			t.Errorf("Narrow(%s) = false, want true", t2)
		}
	}
	wide := []Type{TInt64, TUint64, TUsize, TF64, TStr, &Ptr{Elem: TInt32}, &Any{}}
	for _, t2 := range wide {
		if Narrow(t2) {
			t.Errorf("Narrow(%s) = true, want false", t2)
		}
	}
}
