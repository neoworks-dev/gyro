package metals

// Data layout. This is the format's, not a backend's: two backends that
// computed padding separately would agree until the first struct with a
// u8 in front of a u32, and the corpus would then be measuring which one
// guessed. Both ask this file instead, and the C backend asserts every
// answer against `cc`'s own with a _Static_assert per type.
//
// There are two representations, and the difference between them is the
// whole of what a boundary conversion is:
//
//   - Storage. What a value looks like in memory the program addresses:
//     a record field, a tuple element, an array element, a `slot` cell, a
//     heap cell, a global. This is C's: natural widths, natural
//     alignment, padding where C pads. It is what makes a Gyro struct and
//     a C struct the same bytes.
//
//   - Register. What a METALS register holds: one qword per scalar, an
//     f32 carried as the double it widens to. Aggregates are the same in
//     both, since an aggregate register is its storage.
//
// Loading a narrow scalar out of storage extends it; storing one back
// narrows it; an f32 rounds through single precision in both directions.
// Everything that copies a register — Copy, a call argument, an async
// argument buffer — uses Slot, and everything that touches program
// memory uses Size.

// Word is the size of a machine word: a pointer, and a register slot.
const Word = 8

// Layout answers size, alignment and offset questions for one module's
// types. Build it once per module: the named types are resolved through
// the module's declarations, and the answers are cached.
type Layout struct {
	types map[string]*TypeDecl
	size  map[string]int
	align map[string]int
	// building catches a record that contains itself, which has no
	// finite layout; the backends report it as a program error.
	building map[string]bool
	fail     func(format string, args ...any)
}

// NewLayout indexes the types a module may mention, its own and the ones
// it imports — an imported type is laid out here because a module that
// holds one has to reserve the storage. onError is called with
// a message a type that has no layout — an unknown name, or a cycle —
// and must not return; a backend passes its own failure path.
func NewLayout(m *Module, onError func(format string, args ...any)) *Layout {
	return &Layout{
		types:    m.TypeEnv(),
		size:     map[string]int{},
		align:    map[string]int{},
		building: map[string]bool{},
		fail:     onError,
	}
}

// Size is a type's storage size in bytes: what sizeof would say in C,
// padding included.
func (l *Layout) Size(t Type) int {
	switch t := t.(type) {
	case *Prim:
		return primSize(t.Kind)
	case *Ptr, *AnyPtr, *ChanOf, *Handle, *CodeType:
		return Word
	case *FnType:
		return 2 * Word // { code, env }
	case *SliceOf:
		return 2 * Word // { data, len }
	case *Any:
		return 2 * Word // { descriptor, payload }
	case *Opt:
		if NicheOpt(t) {
			return Word
		}
		return roundUp(Word+l.Size(t.Elem), l.Align(t)) // { flag, payload }
	case *TupleOf:
		size, _ := l.members(t.Elems)
		return size
	case *ArrayOf:
		return int(t.Len) * l.Size(t.Elem)
	case *Named:
		return l.namedSize(t.Name)
	}
	l.fail("no layout for type %s", t)
	return 0
}

// Align is a type's alignment in bytes. Nothing is ever more aligned
// than a word, which is what keeps an enum's payload at a fixed offset
// and a slot's area at a fixed one too.
func (l *Layout) Align(t Type) int {
	switch t := t.(type) {
	case *Prim:
		return primSize(t.Kind)
	case *Ptr, *AnyPtr, *ChanOf, *Handle, *CodeType, *FnType, *SliceOf, *Any:
		return Word
	case *Opt:
		return max(Word, l.Align(t.Elem))
	case *TupleOf:
		_, align := l.members(t.Elems)
		return align
	case *ArrayOf:
		return l.Align(t.Elem)
	case *Named:
		return l.namedAlign(t.Name)
	}
	l.fail("no layout for type %s", t)
	return 1
}

// Slot is the size of a register's own storage: a qword for every
// scalar, and the storage size for an aggregate, which lives inline.
func (l *Layout) Slot(t Type) int {
	if Scalar(t) {
		return Word
	}
	return l.Size(t)
}

// Scalar reports whether a type is one machine word in a register. The
// distinction that matters is not "primitive" — it is whether a register
// and its storage can be the same bytes.
func Scalar(t Type) bool {
	switch t := t.(type) {
	case *Prim, *Ptr, *AnyPtr, *ChanOf, *Handle, *CodeType:
		return true
	case *Opt:
		return NicheOpt(t)
	}
	return false
}

// NicheOpt reports whether an optional is laid out inside its own
// payload, with the absent case spelled as a bit pattern the payload
// cannot otherwise hold, rather than as a flag beside it.
//
// Three types qualify, for the same reason: `ptr<T>`, `code(...)` and
// `anyptr`. Every `ptr<T>` in a verified module addresses an object
// METALS produced, every `code(...)` is the address of a function METALS
// emitted, and an `anyptr` is one of those two with its type taken away
// rather than a number someone made up — so in each case the null
// address is a pattern no live value has and the format may reserve it.
// That is not true of a `handle` or a `chan`: those are the runtime's
// values, and what they may hold is the runtime's business, not the
// format's.
//
// The reason to bother is C. A C pointer is `opt<ptr<T>>` here — METALS'
// pointer is never null and C's is, so every dereference is an unwrap —
// and with a flag beside the payload `sizeof(char *)` answers 16. That is
// self-consistent and it is not C's ABI, and C's ABI is the whole point
// of reading C in. With the niche it answers 8, a struct of pointers is
// the bytes C lays out, and a zeroed page reads back as a page of nulls,
// which is what a C program compiled anywhere else already assumes.
//
// It costs nothing elsewhere: `const.null` is a zeroed store either way,
// a wrap is a move, an unwrap is a null test, and equality was already a
// word compare. See docs/decisions.md, "A C pointer is an optional
// pointer".
func NicheOpt(t *Opt) bool {
	switch t.Elem.(type) {
	case *Ptr, *AnyPtr, *CodeType:
		return true
	}
	return false
}

// Narrow reports whether a scalar's storage differs from what its
// register holds, which is what a boundary conversion has to answer for:
// a type under a word wide, and an f32, which a register carries as a
// double and memory holds as a single.
func Narrow(t Type) bool {
	prim, ok := t.(*Prim)
	if !ok {
		return false
	}
	return primSize(prim.Kind) < Word || prim.Kind == F32
}

func primSize(kind PrimKind) int {
	switch kind {
	case I8, U8, Bool:
		return 1
	case I16, U16:
		return 2
	case I32, U32, F32, Rune:
		return 4
	}
	return Word // i64, u64, usize, f64, and str's pointer
}

// ---------------------------------------------------------------------------
// Aggregates
// ---------------------------------------------------------------------------

// members lays a sequence out the way a C struct is laid out: each
// member at the next offset its own alignment allows, the whole rounded
// up to the widest member's alignment.
func (l *Layout) members(types []Type) (size, align int) {
	align = 1
	for _, t := range types {
		memberAlign := l.Align(t)
		if memberAlign > align {
			align = memberAlign
		}
		size = roundUp(size, memberAlign) + l.Size(t)
	}
	return roundUp(size, align), align
}

// overlap lays a union out the way C lays one out: every member at
// offset zero, the size the widest member's, the alignment the
// strictest, and the size rounded up to it so that an array of them
// keeps every element aligned.
func (l *Layout) overlap(union *UnionShape) (size, align int) {
	align = 1
	for _, member := range union.Members {
		if memberAlign := l.Align(member.Type); memberAlign > align {
			align = memberAlign
		}
		if memberSize := l.Size(member.Type); memberSize > size {
			size = memberSize
		}
	}
	return roundUp(size, align), align
}

// MemberOffset is a member's offset inside a record or a union. A
// union's members all begin at zero — that is the whole of what a union
// is — so the two questions share an entry point and differ only in the
// answer.
func (l *Layout) MemberOffset(decl *TypeDecl, index int) int {
	if decl.Union != nil {
		return 0
	}
	return l.FieldOffset(decl.Record, index)
}

// Offsets gives every member's byte offset, which is what a record, a
// tuple and an enum payload all need.
func (l *Layout) Offsets(types []Type) []int {
	offsets := make([]int, len(types))
	at := 0
	for i, t := range types {
		at = roundUp(at, l.Align(t))
		offsets[i] = at
		at += l.Size(t)
	}
	return offsets
}

// FieldOffset is a record field's offset inside the record.
func (l *Layout) FieldOffset(record *RecordShape, index int) int {
	at := 0
	for i := 0; i < index; i++ {
		at = roundUp(at, l.Align(record.Fields[i].Type)) + l.Size(record.Fields[i].Type)
	}
	return roundUp(at, l.Align(record.Fields[index].Type))
}

// TupleOffset is an element's offset inside a tuple.
func (l *Layout) TupleOffset(tuple *TupleOf, index int) int {
	return l.Offsets(tuple.Elems)[index]
}

// VariantFieldOffset is a payload field's offset inside the enum, the
// tag included. Every payload starts one word in, because nothing is
// aligned more than a word.
func (l *Layout) VariantFieldOffset(enum *EnumShape, variant, index int) int {
	return Word + l.Offsets(enum.Variants[variant].Payload)[index]
}

// PayloadSize is the size of an enum's payload union: the widest
// variant, or 0 for an enum whose variants all carry nothing.
func (l *Layout) PayloadSize(enum *EnumShape) int {
	widest := 0
	for _, variant := range enum.Variants {
		if size, _ := l.members(variant.Payload); size > widest {
			widest = size
		}
	}
	return widest
}

func (l *Layout) namedSize(name string) int {
	if size, ok := l.size[name]; ok {
		return size
	}
	decl := l.declOf(name)
	if decl.Opaque {
		l.fail("type %%%s is opaque, so it has no size", name)
		return 0
	}
	if l.building[name] {
		l.fail("type %%%s contains itself", name)
	}
	l.building[name] = true

	size := 0
	switch {
	case decl.Record != nil:
		size, _ = l.members(fieldTypes(decl.Record))
	case decl.Union != nil:
		size, _ = l.overlap(decl.Union)
	default:
		size = roundUp(Word+l.PayloadSize(decl.Enum), Word) // { tag, payload }
	}
	// A declared alignment beyond what the members ask for rounds the
	// whole up to it, the way C rounds a struct up to its strictest
	// member. Nothing else changes: every offset is still the members'.
	size = roundUp(size, l.namedAlign(name))

	delete(l.building, name)
	l.size[name] = size
	return size
}

func (l *Layout) namedAlign(name string) int {
	if align, ok := l.align[name]; ok {
		return align
	}
	decl := l.declOf(name)
	if decl.Opaque {
		l.fail("type %%%s is opaque, so it has no alignment", name)
		return 1
	}
	align := Word
	switch {
	case decl.Record != nil:
		_, align = l.members(fieldTypes(decl.Record))
	case decl.Union != nil:
		_, align = l.overlap(decl.Union)
	}
	if decl.Align > align {
		align = decl.Align
	}
	l.align[name] = align
	return align
}

func (l *Layout) declOf(name string) *TypeDecl {
	decl := l.types[name]
	if decl == nil {
		l.fail("unknown type %%%s", name)
		return &TypeDecl{Name: name, Record: &RecordShape{}}
	}
	return decl
}

func fieldTypes(record *RecordShape) []Type {
	types := make([]Type, len(record.Fields))
	for i, field := range record.Fields {
		types[i] = field.Type
	}
	return types
}

func roundUp(value, align int) int {
	if align <= 1 {
		return value
	}
	return (value + align - 1) / align * align
}
