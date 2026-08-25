// Package metals defines METALS, the cross-language intermediate
// representation: a typed module IR with registers and structured control
// flow. Gyro lowers into it; other language front ends will too, which is
// why the package trusts no producer — see Verify.
//
// docs/metals.md is the format's specification; this package is its
// implementation. The text format (Print/Parse) is canonical for now, the
// binary encoding comes later.
package metals

import (
	"strconv"
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// Type is a METALS type. There are no generics: front ends monomorphize
// before lowering, so every type here is concrete.
type Type interface {
	String() string
}

type PrimKind int

const (
	I8 PrimKind = iota
	I16
	I32
	I64
	U8
	U16
	U32
	U64
	Usize
	F32
	F64
	Bool
	Rune
	Str
)

type Prim struct {
	Kind PrimKind
	name string
}

var (
	TInt8   = &Prim{I8, "i8"}
	TInt16  = &Prim{I16, "i16"}
	TInt32  = &Prim{I32, "i32"}
	TInt64  = &Prim{I64, "i64"}
	TUint8  = &Prim{U8, "u8"}
	TUint16 = &Prim{U16, "u16"}
	TUint32 = &Prim{U32, "u32"}
	TUint64 = &Prim{U64, "u64"}
	TUsize  = &Prim{Usize, "usize"}
	TF32    = &Prim{F32, "f32"}
	TF64    = &Prim{F64, "f64"}
	TBool   = &Prim{Bool, "bool"}
	TRune   = &Prim{Rune, "rune"}
	TStr    = &Prim{Str, "str"}
)

// Prims maps the spellable primitive names to their types.
var Prims = map[string]*Prim{
	"i8": TInt8, "i16": TInt16, "i32": TInt32, "i64": TInt64,
	"u8": TUint8, "u16": TUint16, "u32": TUint32, "u64": TUint64,
	"usize": TUsize, "f32": TF32, "f64": TF64,
	"bool": TBool, "rune": TRune, "str": TStr,
}

func (p *Prim) String() string { return p.name }

// PrimWidth is a primitive's size in bytes, and 0 for the ones that have
// no fixed byte width. It is what a linear-memory access reads and
// writes, and what bitcast requires both of its types to agree on.
func PrimWidth(t Type) int {
	prim, ok := t.(*Prim)
	if !ok {
		return 0
	}
	switch prim.Kind {
	case I8, U8, Bool:
		return 1
	case I16, U16:
		return 2
	case I32, U32, F32, Rune:
		return 4
	case I64, U64, Usize, F64:
		return 8
	}
	return 0
}

// MemAccess reports whether a primitive may be the width suffix of a
// linear-memory load or store. Bool, rune, str and usize are excluded:
// the first three are not byte patterns a producer should be inventing
// from raw memory, and usize's width is the target's business, not one a
// byte-addressed access may depend on.
func MemAccess(t Type) bool {
	prim, ok := t.(*Prim)
	if !ok {
		return false
	}
	switch prim.Kind {
	case I8, I16, I32, I64, U8, U16, U32, U64, F32, F64:
		return true
	}
	return false
}

type (
	// Ptr is ptr<T>: never null, never crosses a boundary.
	Ptr struct{ Elem Type }
	// Opt is opt<T>, the only nullable type.
	Opt struct{ Elem Type }
	// SliceOf is slice<T>, a borrowed view.
	SliceOf struct{ Elem Type }
	// ArrayOf is array<T, N>.
	ArrayOf struct {
		Elem Type
		Len  int64
	}
	TupleOf struct{ Elems []Type }
	ChanOf  struct{ Elem Type }
	// FutureOf is the un-awaited result of an async call.
	FutureOf struct{ Result Type }
	// FnType is fn(A, B) -> R. Result nil means the function returns
	// nothing.
	FnType struct {
		Params []Type
		Result Type
		Async  bool
	}
	// CodeType is code(A, B) -> R: the address of a function and nothing
	// else. It is one word where FnType is two, because it carries no
	// environment, and that is the whole reason it exists — C's function
	// pointer is one word and a struct full of them has C's layout. A
	// closure needs somewhere to put what it captured; a code pointer has
	// nothing to capture, so it is the address alone.
	CodeType struct {
		Params []Type
		Result Type
	}
	// AnyPtr is `anyptr`: the address of an object with the object's
	// type erased. It is one word, exactly what a `ptr<T>` is, and the
	// only things it supports are comparison and having the type put
	// back — there is nothing to load through a pointer that has
	// forgotten what it points at.
	//
	// That is C's `void *` and not a weaker `ptr<T>`. C already forbids
	// `*p`, `p + 1` and `sizeof *p` on one, so the erasure costs the
	// source language nothing it had. `ptr.erase` takes the type away
	// and `ptr.cast` asserts it back; the assertion is the producer's,
	// the way `ptr.offset` staying inside its object is.
	AnyPtr struct{}
	// Named references a module-local record or enum by name.
	Named struct{ Name string }
	// Any is the dynamically typed escape hatch.
	Any struct{}
	// Handle is an opaque foreign reference: identity, no structure.
	Handle struct{}
)

func (t *Ptr) String() string     { return "ptr<" + t.Elem.String() + ">" }
func (t *Opt) String() string     { return "opt<" + t.Elem.String() + ">" }
func (t *SliceOf) String() string { return "slice<" + t.Elem.String() + ">" }
func (t *ChanOf) String() string  { return "chan<" + t.Elem.String() + ">" }
func (t *FutureOf) String() string {
	return "future<" + t.Result.String() + ">"
}
func (t *Named) String() string  { return "%" + QuoteName(t.Name) }
func (t *Any) String() string    { return "any" }
func (t *AnyPtr) String() string { return "anyptr" }
func (t *Handle) String() string { return "handle" }

func (t *ArrayOf) String() string {
	return "array<" + t.Elem.String() + ", " + strconv.FormatInt(t.Len, 10) + ">"
}

func (t *TupleOf) String() string {
	parts := make([]string, len(t.Elems))
	for i, elem := range t.Elems {
		parts[i] = elem.String()
	}
	return "tuple<" + strings.Join(parts, ", ") + ">"
}

func (t *FnType) String() string {
	parts := make([]string, len(t.Params))
	for i, param := range t.Params {
		parts[i] = param.String()
	}
	out := "fn(" + strings.Join(parts, ", ") + ")"
	if t.Async {
		out = "async " + out
	}
	if t.Result != nil {
		out += " -> " + t.Result.String()
	}
	return out
}

func (t *CodeType) String() string {
	parts := make([]string, len(t.Params))
	for i, param := range t.Params {
		parts[i] = param.String()
	}
	out := "code(" + strings.Join(parts, ", ") + ")"
	if t.Result != nil {
		out += " -> " + t.Result.String()
	}
	return out
}

// TypesEqual is structural equality; Named compares by name, which is
// sound inside one module.
func TypesEqual(a, b Type) bool {
	switch a := a.(type) {
	case *Prim:
		b, ok := b.(*Prim)
		return ok && a.Kind == b.Kind
	case *Ptr:
		b, ok := b.(*Ptr)
		return ok && TypesEqual(a.Elem, b.Elem)
	case *Opt:
		b, ok := b.(*Opt)
		return ok && TypesEqual(a.Elem, b.Elem)
	case *SliceOf:
		b, ok := b.(*SliceOf)
		return ok && TypesEqual(a.Elem, b.Elem)
	case *ArrayOf:
		b, ok := b.(*ArrayOf)
		return ok && a.Len == b.Len && TypesEqual(a.Elem, b.Elem)
	case *TupleOf:
		b, ok := b.(*TupleOf)
		if !ok || len(a.Elems) != len(b.Elems) {
			return false
		}
		for i := range a.Elems {
			if !TypesEqual(a.Elems[i], b.Elems[i]) {
				return false
			}
		}
		return true
	case *ChanOf:
		b, ok := b.(*ChanOf)
		return ok && TypesEqual(a.Elem, b.Elem)
	case *FutureOf:
		b, ok := b.(*FutureOf)
		return ok && TypesEqual(a.Result, b.Result)
	case *FnType:
		b, ok := b.(*FnType)
		if !ok || a.Async != b.Async || len(a.Params) != len(b.Params) {
			return false
		}
		for i := range a.Params {
			if !TypesEqual(a.Params[i], b.Params[i]) {
				return false
			}
		}
		return resultsEqual(a.Result, b.Result)
	case *CodeType:
		b, ok := b.(*CodeType)
		if !ok || len(a.Params) != len(b.Params) {
			return false
		}
		for i := range a.Params {
			if !TypesEqual(a.Params[i], b.Params[i]) {
				return false
			}
		}
		return resultsEqual(a.Result, b.Result)
	case *Named:
		b, ok := b.(*Named)
		return ok && a.Name == b.Name
	case *Any:
		_, ok := b.(*Any)
		return ok
	case *AnyPtr:
		_, ok := b.(*AnyPtr)
		return ok
	case *Handle:
		_, ok := b.(*Handle)
		return ok
	}
	return false
}

func resultsEqual(a, b Type) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return TypesEqual(a, b)
}

// ---------------------------------------------------------------------------
// Module structure
// ---------------------------------------------------------------------------

// Module is one METALS compilation unit. Its name carries the producing
// language as a scheme prefix: "gyro:examples/structs", "js:./widgets".
type Module struct {
	Name     string
	Imports  []*Import
	Types    []*TypeDecl
	Memories []*Memory
	Data     []*DataSeg
	Globals  []*Global
	Funcs    []*Func

	// Init names a function this module needs run before the program
	// starts, or "" for a module that needs nothing. It is where a global
	// gets the initializer it does not carry: the front end decides
	// evaluation order by writing the stores in order, and the format
	// only has to say when the function runs.
	//
	// It exists because a producer cannot always arrange the call itself.
	// Gyro owns `main` and could put the call there by hand; a C
	// translation unit is imported by someone else's program and has no
	// entry point to patch. Link is the pass that sees the whole program,
	// so Link is where an initializer becomes a call — one per module, in
	// dependency order, so a module's own storage is written after
	// everything it imports and before anything reads it.
	Init string
}

// PageSize is the granularity a linear memory is declared and grown in,
// the same 64 KiB page WebAssembly uses.
const PageSize = 65536

// Memory is a linear memory: a flat, byte-addressed, bounds-checked
// region of storage private to the module that declares it.
//
// It exists for producers whose source language has a memory model
// METALS' typed pointers cannot express — C's, where a pointer is an
// integer, objects overlap, and `&local` outlives its type. Everything
// such a language needs (pointer arithmetic, unions, bitfields, varargs,
// packed structs, unaligned access) is one flat region and offsets into
// it, which is why this is one declaration rather than eight type rules.
//
// An address is a u32, deliberately not a ptr<T>. That is what keeps the
// format's promise intact: a linear address cannot appear in a
// boundary-crossing signature, because it is not a pointer and carries no
// claim about what it points at. A module exports the same copied values
// it always did. ptr<T> stays exactly what it was — typed, non-null,
// non-crossing — and the two memory models never mix.
//
// Every access is bounds-checked against the memory's current size, so a
// producer that is internally unsafe still cannot reach anything outside
// the region it declared. That check is the reason to compile C into
// METALS instead of linking it: the sandbox is structural.
type Memory struct {
	Name string
	// Min is the size the memory starts at, in pages.
	Min int64
	// Max caps mem.grow. Zero means the implementation's own limit.
	Max int64
}

// DataSeg initializes a span of a linear memory before any function
// runs. Segments are applied in declaration order; a later one wins where
// two overlap.
type DataSeg struct {
	Memory string
	Offset int64
	Bytes  []byte
}

// Import declares functions provided by another module.
type Import struct {
	Module string
	Funcs  []ImportedFunc
}

type ImportedFunc struct {
	Name string
	Sig  *FnType
}

// TypeDecl is a module-local record or enum. Exactly one shape is set.
type TypeDecl struct {
	Name   string
	Record *RecordShape
	Enum   *EnumShape
}

type RecordShape struct {
	Fields []FieldDecl
}

type FieldDecl struct {
	Name string
	Type Type
}

type EnumShape struct {
	Variants []VariantDecl
}

type VariantDecl struct {
	Name    string
	Payload []Type
}

// Global is module-level storage, zero-initialized before main runs.
type Global struct {
	Name string
	Type Type

	// Bytes is the storage this global starts with, in place of the zeros
	// it would otherwise hold. It is exactly Size(Type) bytes, in the
	// target's own representation — the same image a data segment writes
	// into a linear memory, for a global instead.
	//
	// This is not the initializer a global famously does not have. That
	// one is an expression, and the reason it is a front end's problem is
	// evaluation order; static bytes have none, which is the same reason
	// a data segment is allowed to exist. What they cannot express is a
	// pointer, because a pointer's value is not known until the program
	// is laid out — so `static int *p = &x;` still lowers into the
	// module's Init, and `static char msg[] = "hi";` does not.
	//
	// A producer that emits these is buying startup cost of nothing for a
	// program with many string literals, which is every real C program.
	Bytes []byte
}

// Func is one function. Result nil means it returns nothing.
type Func struct {
	Name   string
	Export bool
	Async  bool
	Params []Param
	Result Type
	Body   []Instr
}

type Param struct {
	Name string
	Type Type
}

// Sig is the function's type.
func (f *Func) Sig() *FnType {
	params := make([]Type, len(f.Params))
	for i, param := range f.Params {
		params[i] = param.Type
	}
	return &FnType{Params: params, Result: f.Result, Async: f.Async}
}

// Scheme returns the language prefix of a module name, "" if none.
func Scheme(moduleName string) string {
	if index := strings.IndexByte(moduleName, ':'); index > 0 {
		return moduleName[:index]
	}
	return ""
}

// producerSchemes are the schemes of the languages that compile *into*
// METALS. They are one runtime, because they literally are one: the
// linker makes them a single module, the verifier has checked all of it,
// and it runs on one heap with one allocator.
//
// A boundary is where that stops being true — a native library nothing
// verified, or another language's runtime with its own heap living in
// the process — and an unknown scheme is read as one, which is the
// conservative way round.
var producerSchemes = map[string]bool{
	"gyro":   true,
	"lisp":   true,
	"wasm":   true,
	"cc":     true,
	"metals": true,
}

// SameRuntime reports whether two modules share a runtime, and so whether
// a call between them is a boundary crossing. Two modules of one scheme
// always do; two producers of METALS do because the program they become
// is one module.
func SameRuntime(a, b string) bool {
	schemeA, schemeB := Scheme(a), Scheme(b)
	if schemeA == schemeB {
		return true
	}
	return producerSchemes[schemeA] && producerSchemes[schemeB]
}

// externScheme is the module scheme of a C library. A library is not a
// METALS producer, so nothing links against it and the import survives to
// the backends, which is exactly what a native symbol needs. It is
// deliberately not a producer scheme: a native library is the case the
// boundary rules exist for, since nothing has verified what it does with
// an address it is handed.
const externScheme = "c"

// ExternModule names the module a C library's symbols are imported from.
func ExternModule(library string) string { return externScheme + ":" + library }

// ExternLibrary is the inverse: the library an import names, or "" when
// the import is not a C library.
func ExternLibrary(moduleName string) string {
	if Scheme(moduleName) != externScheme {
		return ""
	}
	return moduleName[len(externScheme)+1:]
}

// QuoteName renders a name for printing after its @, % or $ sigil: bare
// when it is a plain word, quoted when monomorphization or module
// qualification put richer characters in it, as in @"Pair<i32>::sum".
// Both spellings name the same namespace.
func QuoteName(name string) string {
	if isPlainName(name) {
		return name
	}
	return strconv.Quote(name)
}

func isPlainName(name string) bool {
	if name == "" {
		return false
	}
	for _, ch := range name {
		if !unicode.IsLetter(ch) && !unicode.IsDigit(ch) && ch != '_' && ch != '.' {
			return false
		}
	}
	return true
}
