// Package types defines the Gyro type representation used by sema and every
// later stage. Types are built once per declaration; generic instances are
// canonicalized by the checker, so two mentions of Pair<i32> share one
// object and identity is mostly pointer identity.
package types

import (
	"strconv"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
)

// Type is implemented by every Gyro type.
type Type interface {
	String() string
}

// ---------------------------------------------------------------------------
// Basic types
// ---------------------------------------------------------------------------

type BasicKind int

const (
	// Invalid marks a type that failed to resolve. It poisons silently: an
	// operation on an Invalid operand reports nothing, so one error does not
	// cascade into dozens.
	Invalid BasicKind = iota

	I8
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
	RuneKind
	BoolKind
	StringKind
	VoidKind
	// ErrorKind is the structural error type: any type with a
	// message(): string method satisfies it.
	ErrorKind

	// NullKind is the type of the null literal, assignable only to T?.
	NullKind
	// UntypedInt and UntypedFloat are the types of numeric literals before
	// context types them. They default to i32 and f64.
	UntypedInt
	UntypedFloat
)

type Basic struct {
	Kind BasicKind
	name string
}

func (b *Basic) String() string { return b.name }

var (
	Unknown = &Basic{Invalid, "<unknown>"}
	Int8    = &Basic{I8, "i8"}
	Int16   = &Basic{I16, "i16"}
	Int32   = &Basic{I32, "i32"}
	Int64   = &Basic{I64, "i64"}
	Uint8   = &Basic{U8, "u8"}
	Uint16  = &Basic{U16, "u16"}
	Uint32  = &Basic{U32, "u32"}
	Uint64  = &Basic{U64, "u64"}
	USize   = &Basic{Usize, "usize"}
	Float32 = &Basic{F32, "f32"}
	Float64 = &Basic{F64, "f64"}
	Rune    = &Basic{RuneKind, "rune"}
	Bool    = &Basic{BoolKind, "bool"}
	String  = &Basic{StringKind, "string"}
	Void    = &Basic{VoidKind, "void"}
	Error   = &Basic{ErrorKind, "error"}
	Null    = &Basic{NullKind, "null"}

	UntypedIntType   = &Basic{UntypedInt, "untyped int"}
	UntypedFloatType = &Basic{UntypedFloat, "untyped float"}
)

// Builtin maps the spellable primitive type names to their types.
var Builtin = map[string]*Basic{
	"i8": Int8, "i16": Int16, "i32": Int32, "i64": Int64,
	"u8": Uint8, "u16": Uint16, "u32": Uint32, "u64": Uint64,
	"usize": USize, "f32": Float32, "f64": Float64,
	"rune": Rune, "bool": Bool, "string": String, "void": Void,
	"error": Error,
}

// ---------------------------------------------------------------------------
// Composite types
// ---------------------------------------------------------------------------

// Pointer is *T, never null.
type Pointer struct{ Elem Type }

// Optional is T?, the only type that may hold null.
type Optional struct{ Elem Type }

// Slice is T[], a borrowed view.
type Slice struct{ Elem Type }

// Array is the fixed-size T[N]. Length is -1 when the size expression could
// not be evaluated.
type Array struct {
	Elem   Type
	Length int64
}

// Chan is chan<T>.
type Chan struct{ Elem Type }

// Tuple is (A, B). One-element tuples do not exist.
type Tuple struct{ Elems []Type }

// Func is fn(A, B): C. A method's receiver is not part of Params.
type Func struct {
	Params []Type
	// Required counts the leading parameters without defaults.
	Required int
	// Result is Void for a function that returns nothing; several declared
	// results are one Tuple.
	Result Type
	Async  bool
}

// Future is the value of an un-awaited async call; await unwraps it.
type Future struct{ Result Type }

// TypeParam is one generic parameter. Identity is the pointer.
type TypeParam struct{ Name string }

func (p *Pointer) String() string   { return "*" + p.Elem.String() }
func (o *Optional) String() string  { return o.Elem.String() + "?" }
func (s *Slice) String() string     { return s.Elem.String() + "[]" }
func (c *Chan) String() string      { return "chan<" + c.Elem.String() + ">" }
func (t *TypeParam) String() string { return t.Name }
func (f *Future) String() string    { return "awaitable<" + f.Result.String() + ">" }

func (a *Array) String() string {
	if a.Length < 0 {
		return a.Elem.String() + "[?]"
	}
	return a.Elem.String() + "[" + strconv.FormatInt(a.Length, 10) + "]"
}

func (t *Tuple) String() string {
	parts := make([]string, len(t.Elems))
	for i, elem := range t.Elems {
		parts[i] = elem.String()
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func (f *Func) String() string {
	parts := make([]string, len(f.Params))
	for i, param := range f.Params {
		parts[i] = param.String()
	}
	signature := "fn(" + strings.Join(parts, ", ") + ")"
	if f.Async {
		signature = "async " + signature
	}
	if f.Result != Void {
		signature += ": " + f.Result.String()
	}
	return signature
}

// ---------------------------------------------------------------------------
// Named types
// ---------------------------------------------------------------------------

// RecvKind says how a method takes its receiver.
type RecvKind int

const (
	// NoRecv marks an associated function, called as Type::name().
	NoRecv RecvKind = iota
	// ValueRecv is `self`: borrows immutably.
	ValueRecv
	// PointerRecv is `*self`: may mutate.
	PointerRecv
)

// Method is a free-standing fn attached to a type, or a free function when
// used standalone. Sig excludes the receiver.
type Method struct {
	Name string
	Recv RecvKind
	Sig  *Func
	// TypeParams are the function's own generics, the T of max<T>. A
	// non-empty list means calls infer or take explicit arguments, and the
	// body is checked per instantiation.
	TypeParams []*TypeParam
	// Decl carries the body for duck-typed instantiation checking.
	Decl *ast.FuncDecl
}

// Field is one struct field.
type Field struct {
	Name     string
	Type     Type
	Exported bool
}

// Struct is a declared aggregate. A generic instance shares the Origin's
// declaration and methods, with Args substituted on demand.
type Struct struct {
	Name string
	// Module identifies the defining module, for field visibility.
	Module     string
	TypeParams []*TypeParam
	Fields     []Field
	Methods    map[string]*Method
	// Origin is the generic declaration this instance came from, nil for
	// non-instances.
	Origin *Struct
	Args   []Type
}

// Variant is one enum case.
type Variant struct {
	Name    string
	Payload []Type
}

// Enum is a declared sum type.
type Enum struct {
	Name       string
	Module     string
	TypeParams []*TypeParam
	Variants   []Variant
	Methods    map[string]*Method
	Origin     *Enum
	Args       []Type
}

func (s *Struct) String() string { return namedString(s.Name, s.Args) }
func (e *Enum) String() string   { return namedString(e.Name, e.Args) }

func namedString(name string, args []Type) string {
	if len(args) == 0 {
		return name
	}
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = arg.String()
	}
	return name + "<" + strings.Join(parts, ", ") + ">"
}

// VariantNamed returns the variant with the given name, or nil.
func (e *Enum) VariantNamed(name string) *Variant {
	for i := range e.Variants {
		if e.Variants[i].Name == name {
			return &e.Variants[i]
		}
	}
	return nil
}

// FieldNamed returns the field with the given name, or nil.
func (s *Struct) FieldNamed(name string) *Field {
	for i := range s.Fields {
		if s.Fields[i].Name == name {
			return &s.Fields[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Predicates
// ---------------------------------------------------------------------------

func IsUnknown(t Type) bool {
	basic, ok := t.(*Basic)
	return ok && basic.Kind == Invalid
}

func IsInteger(t Type) bool {
	basic, ok := t.(*Basic)
	if !ok {
		return false
	}
	switch basic.Kind {
	case I8, I16, I32, I64, U8, U16, U32, U64, Usize, UntypedInt:
		return true
	}
	return false
}

func IsUnsigned(t Type) bool {
	basic, ok := t.(*Basic)
	if !ok {
		return false
	}
	switch basic.Kind {
	case U8, U16, U32, U64, Usize:
		return true
	}
	return false
}

func IsFloat(t Type) bool {
	basic, ok := t.(*Basic)
	if !ok {
		return false
	}
	switch basic.Kind {
	case F32, F64, UntypedFloat:
		return true
	}
	return false
}

func IsNumeric(t Type) bool { return IsInteger(t) || IsFloat(t) }

func IsString(t Type) bool {
	basic, ok := t.(*Basic)
	return ok && basic.Kind == StringKind
}

func IsUntyped(t Type) bool {
	basic, ok := t.(*Basic)
	if !ok {
		return false
	}
	return basic.Kind == UntypedInt || basic.Kind == UntypedFloat || basic.Kind == NullKind
}

// IsHashable reports whether hash() is derived for t. It follows == : the
// primitives that compare by value, and the composites that compare
// field-wise. A pointer is left out although == accepts one, because it
// compares by address and an address is not the same run to run.
func IsHashable(t Type) bool {
	switch t := t.(type) {
	case *Basic:
		switch {
		case IsUntyped(t):
			return false
		case IsNumeric(t):
			return true
		}
		return t.Kind == StringKind || t.Kind == BoolKind || t.Kind == RuneKind
	case *Tuple:
		return everyHashable(t.Elems)
	case *Struct:
		for _, field := range t.Fields {
			if !IsHashable(field.Type) {
				return false
			}
		}
		// A fieldless struct is an opaque runtime handle, which holds
		// nothing to hash.
		return len(t.Fields) > 0
	case *Enum:
		for _, variant := range t.Variants {
			if !everyHashable(variant.Payload) {
				return false
			}
		}
		return true
	}
	return false
}

func everyHashable(list []Type) bool {
	for _, elem := range list {
		if !IsHashable(elem) {
			return false
		}
	}
	return true
}

// IsOrdered reports whether < <= > >= apply: numeric primitives and rune.
func IsOrdered(t Type) bool {
	if IsNumeric(t) {
		return true
	}
	basic, ok := t.(*Basic)
	return ok && basic.Kind == RuneKind
}

// Default materializes an untyped literal type: integers become i32,
// floats f64. Typed types pass through.
func Default(t Type) Type {
	basic, ok := t.(*Basic)
	if !ok {
		return t
	}
	switch basic.Kind {
	case UntypedInt:
		return Int32
	case UntypedFloat:
		return Float64
	}
	return t
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

// Identical reports whether two types are the same type. Named types
// compare by pointer, which works because the checker canonicalizes
// generic instances.
func Identical(a, b Type) bool {
	if a == b {
		return true
	}
	switch x := a.(type) {
	case *Pointer:
		y, ok := b.(*Pointer)
		return ok && Identical(x.Elem, y.Elem)
	case *Optional:
		y, ok := b.(*Optional)
		return ok && Identical(x.Elem, y.Elem)
	case *Slice:
		y, ok := b.(*Slice)
		return ok && Identical(x.Elem, y.Elem)
	case *Array:
		y, ok := b.(*Array)
		return ok && x.Length == y.Length && Identical(x.Elem, y.Elem)
	case *Chan:
		y, ok := b.(*Chan)
		return ok && Identical(x.Elem, y.Elem)
	case *Tuple:
		y, ok := b.(*Tuple)
		if !ok || len(x.Elems) != len(y.Elems) {
			return false
		}
		for i := range x.Elems {
			if !Identical(x.Elems[i], y.Elems[i]) {
				return false
			}
		}
		return true
	case *Func:
		y, ok := b.(*Func)
		if !ok || x.Async != y.Async || len(x.Params) != len(y.Params) {
			return false
		}
		for i := range x.Params {
			if !Identical(x.Params[i], y.Params[i]) {
				return false
			}
		}
		return Identical(x.Result, y.Result)
	case *Future:
		y, ok := b.(*Future)
		return ok && Identical(x.Result, y.Result)
	}
	return false
}

// Key renders a stable cache key for a type list.
func Key(args []Type) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = arg.String()
	}
	return strings.Join(parts, ",")
}
