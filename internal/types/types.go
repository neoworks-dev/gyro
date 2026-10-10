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
	// AnyKind holds a value of one other type and remembers which. It is a
	// closed sum over the types the program wraps, not a runtime registry.
	AnyKind
	// AnyPtrKind is an address whose type has been forgotten: C's
	// `void *`, and the only thing in Gyro that is a pointer without
	// being a pointer *to* something. It has no operations of its own —
	// no dereference, no arithmetic, no ordering — because there is
	// nothing at the far end of it to have them. It is held, compared,
	// passed back, and cast to a `*T` by a program that knows.
	AnyPtrKind

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
	Any     = &Basic{AnyKind, "any"}
	AnyPtr  = &Basic{AnyPtrKind, "anyptr"}
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
	"error": Error, "any": Any, "anyptr": AnyPtr,
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
	// Variadic marks a signature whose last parameter collects the
	// remaining arguments. That parameter's type is already the T[] slice
	// the body sees, so Params needs no special reading.
	Variadic bool
}

// Code is `code(A, B): R`: the address of a function and nothing beside
// it, which is what C's function pointer is.
//
// It is not a Func with a flag. A Func is a closure — two words, code
// and the environment it captured — and the whole point of this type is
// that there is no environment, because the C on the other side has
// nowhere to put one. A type that said "a function value that promises
// not to capture" would be a claim the type does not make and the
// checker would have to keep making instead.
type Code struct {
	Params []Type
	// Result is Void for a code pointer that returns nothing.
	Result Type
}

// Future is the value of an un-awaited async call; await unwraps it.
type Future struct{ Result Type }

// TypeParam is one generic parameter. Identity is the pointer. Bounds are
// the traits a type argument has to implement, and all a generic body may
// rely on.
type TypeParam struct {
	Name   string
	Bounds []*TraitRef
}

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
	if f.Variadic && len(parts) > 0 {
		// The last parameter is stored as the T[] the body sees; the
		// declaration spelled it ...T.
		last := len(parts) - 1
		if slice, ok := f.Params[last].(*Slice); ok {
			parts[last] = "..." + slice.Elem.String()
		}
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

func (c *Code) String() string {
	parts := make([]string, len(c.Params))
	for i, param := range c.Params {
		parts[i] = param.String()
	}
	signature := "code(" + strings.Join(parts, ", ") + ")"
	if c.Result != Void {
		signature += ": " + c.Result.String()
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
	// Decl carries the body, which is checked again at every instantiation.
	Decl *ast.FuncDecl
	// Trait is the trait that declares the method, set for a method
	// listed in a trait. Its signature is written over the trait's Self.
	Trait *Trait
	// Impl is set on a default body installed on an implementing type:
	// the method is the trait's, shared by every implementer, and the
	// impl says what Self and the trait's arguments are for this one.
	Impl *Impl
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
	// Impls are the traits the type implements, in the order written.
	Impls []*Impl
	// Origin is the generic declaration this instance came from, nil for
	// non-instances.
	Origin *Struct
	Args   []Type
	// Extern names the C library a fieldless `@extern` struct stands in
	// for. Such a struct is an opaque pointer: no literal, no fields, and
	// only the library that produced one can do anything with it.
	Extern string
}

// Union is one storage with several names for it. Every member begins
// where the union does, so the size is the widest member's and writing
// one unwrites the others.
//
// It is its own type rather than a Struct carrying a flag, and the
// reason is what falls out of it: nothing that switches on *Struct
// treats a union as a record by accident. Equality, printing, hashing,
// `any` and a runtime boundary all refuse it, and they refuse it by not
// having a case rather than by remembering to ask — because nothing
// records which member the storage means, so none of them has a question
// to answer.
//
// Not generic: a union is one storage the widest member fits in, and C,
// which is what a union exists to be, has no generic ones.
type Union struct {
	Name string
	// Module identifies the defining module, for member visibility.
	Module  string
	Members []Field
	Methods map[string]*Method
	Impls   []*Impl
}

func (u *Union) String() string { return u.Name }

// MemberNamed finds a member by name, or nil.
func (u *Union) MemberNamed(name string) *Field {
	for i := range u.Members {
		if u.Members[i].Name == name {
			return &u.Members[i]
		}
	}
	return nil
}

// Extern is what `@extern("SDL2", symbol = "SDL_Delay")` says about a
// declaration: the library it is linked from, spelled as the linker wants
// it without the `lib` prefix, and the C symbol it names — the
// declaration's own name unless `symbol =` gave another.
type Extern struct {
	Library string
	Symbol  string
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
	Impls      []*Impl
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

// NewOptional builds T?, collapsing the nested case. Optionals do not
// nest: a generic result written T?, instantiated with a T that is
// already optional, is that same optional. It is what lets a Map hold
// optional values — get() answers null for a miss and for a stored null
// alike, and contains() separates them.
func NewOptional(elem Type) Type {
	if _, nested := elem.(*Optional); nested {
		return elem
	}
	return &Optional{Elem: elem}
}

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
		if !ok || x.Async != y.Async || x.Variadic != y.Variadic || len(x.Params) != len(y.Params) {
			return false
		}
		for i := range x.Params {
			if !Identical(x.Params[i], y.Params[i]) {
				return false
			}
		}
		return Identical(x.Result, y.Result)
	case *Code:
		y, ok := b.(*Code)
		if !ok || len(x.Params) != len(y.Params) {
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
	case *Dyn:
		y, ok := b.(*Dyn)
		return ok && IdenticalRefs(x.Ref, y.Ref)
	}
	return false
}

// IdenticalRefs reports whether two trait references name the same trait
// applied to identical arguments.
func IdenticalRefs(a, b *TraitRef) bool {
	if a.Trait != b.Trait || len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if !Identical(a.Args[i], b.Args[i]) {
			return false
		}
	}
	return true
}

// Key renders a stable cache key for a type list.
func Key(args []Type) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = arg.String()
	}
	return strings.Join(parts, ",")
}

// ---------------------------------------------------------------------------
// Traits
// ---------------------------------------------------------------------------

// Trait is a declared list of methods a type promises to provide. A trait
// is not a type: a value never has one, only `dyn Trait`, which is.
type Trait struct {
	Name   string
	Module string
	// Params are the trait's own type parameters, as in Iterator<T>.
	Params []*TypeParam
	// Self stands for the implementing type inside the trait. It is bound
	// by the trait itself, so a default body may call any method the trait
	// lists.
	Self *TypeParam
	// Supers are the traits every implementer also has to implement,
	// written over Self and Params.
	Supers []*TraitRef
	// Methods are the ones this trait declares, in order. Their
	// signatures are written over Self and Params.
	Methods []*Method
	Decl    *ast.TraitDecl
	// Kind names the capability the compiler knows the trait as — "Eq",
	// "Hash", "Print", "Ord", "Add", "Sub", "Mul", "Div", "Rem", "Neg" or
	// "Iterator" — and is empty for every other trait. Such a trait is
	// declared in the bundled standard library like any other; the
	// compiler recognizes it by where it is declared.
	Kind string
}

func (t *Trait) String() string { return t.Name }

// MethodNamed returns the method this trait itself declares, or nil.
func (t *Trait) MethodNamed(name string) *Method {
	for _, method := range t.Methods {
		if method.Name == name {
			return method
		}
	}
	return nil
}

// TraitRef is a trait applied to its arguments: Iterator<i32>.
type TraitRef struct {
	Trait *Trait
	Args  []Type
}

func (r *TraitRef) String() string { return namedString(r.Trait.Name, r.Args) }

// Impl records that a type implements a trait.
type Impl struct {
	Trait *Trait
	// Args are the trait's arguments, written over the implementing
	// type's own parameters.
	Args []Type
	// Derived marks `impl Eq for Point` with no block: the compiler's
	// implementation, built from the type's fields.
	Derived bool
	Decl    *ast.ImplDecl
	// Owner is the declared type the impl is for.
	Owner Type
	// Broken marks an impl that did not meet its trait. What it brings is
	// not checked further: the defaults would be checked against methods
	// that are not the trait's, and report the same mistake again.
	Broken bool
}

// Dyn is `dyn Trait`: a value of an unknown type known by the methods of
// the trait. It is two words, a pointer to the value and the table of its
// type's methods, and it borrows the value.
type Dyn struct{ Ref *TraitRef }

func (d *Dyn) String() string { return "dyn " + d.Ref.String() }

// Methods lists every method callable through the trait: those of the
// traits it requires, then its own. It is the order of a method table.
func (r *TraitRef) AllMethods() []*TraitMethod {
	var list []*TraitMethod
	seen := map[*Trait]bool{}
	var walk func(trait *Trait)
	walk = func(trait *Trait) {
		if seen[trait] {
			return
		}
		seen[trait] = true
		for _, super := range trait.Supers {
			walk(super.Trait)
		}
		for _, method := range trait.Methods {
			list = append(list, &TraitMethod{Trait: trait, Method: method})
		}
	}
	walk(r.Trait)
	return list
}

// TraitMethod is a method together with the trait that declares it.
type TraitMethod struct {
	Trait  *Trait
	Method *Method
}
