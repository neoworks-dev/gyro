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
	// Named references a module-local record or enum by name.
	Named struct{ Name string }
	// Dyn is the dynamically typed escape hatch.
	Dyn struct{}
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
func (t *Dyn) String() string    { return "dyn" }
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
	case *Named:
		b, ok := b.(*Named)
		return ok && a.Name == b.Name
	case *Dyn:
		_, ok := b.(*Dyn)
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
	Name    string
	Imports []*Import
	Types   []*TypeDecl
	Globals []*Global
	Funcs   []*Func
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
// There is no initializer here: front ends lower one into main, where
// evaluation order is theirs to decide.
type Global struct {
	Name string
	Type Type
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
