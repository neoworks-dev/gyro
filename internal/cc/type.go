package cc

import (
	"strconv"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// C's type system, as far as this front end reads it, and the mapping
// onto METALS' types.
//
// The mapping is a table and not a translation, which is the whole point
// of a direct front end: an `int` is an `i32` and a `struct Point *` is a
// `ptr<%Point>`, and what those occupy in memory is C's answer already,
// because METALS' layout rule *is* C's (docs/appendix-e-native-backends.md). Nothing is
// flattened into a byte region on the way in and nothing has to be
// reconstructed on the way out.
//
// The widths are LP64's, which is what Linux x86-64 means by C and what
// the runtime under all three tiers is compiled as.

type typeKind int

const (
	kVoid typeKind = iota
	kBool
	kChar
	kUChar
	kShort
	kUShort
	kInt
	kUInt
	kLong
	kULong
	kFloat
	kDouble
	kPtr
	kArray
	kStruct
	kUnion
	kFunc
)

type ctype struct {
	kind typeKind
	// elem is a pointer's target or an array's element type.
	elem *ctype
	// length is an array's element count, or -1 for the unspecified
	// length a parameter may be written with.
	length int64
	// tag and fields describe a struct or a union. The tag is also the
	// METALS type name, so a C program's `struct Point` is a `%Point`.
	// C keeps one namespace for both, so `struct P` and `union P` in one
	// program are an error rather than two types, which is also what
	// keeps one METALS name from meaning two things.
	tag    string
	fields []field
	// storage is what the METALS record or union actually declares. It
	// is the same list as fields until a bit-field appears: a run of
	// those is packed into one member of bytes, so several of C's
	// members become one of the record's and each remembers which bits
	// of it are theirs. align is what those bit-fields' declared types
	// ask of the whole, which their bytes no longer say.
	storage []field
	align   int
	// params and result describe a function. A function type has no
	// size and no storage — it is not an object — so it reaches METALS
	// only through a pointer to it, which is a one-word `code(...)`.
	//
	// variadic is the `...` at the end of the list. It is part of the
	// type and not a property of one declaration, because a call through
	// a function pointer has to know it: what the arguments after the
	// named ones become is decided at the call, and only the type says
	// whether there may be any.
	params   []param
	variadic bool
	result   *ctype
}

type field struct {
	name string
	typ  *ctype
	// unit is the index in ctype.storage of the member this one's
	// storage is: itself for an ordinary member, and for a bit-field
	// the run of bytes it was packed into.
	unit int
	// bits is set on a bit-field and says which bits of that run are
	// this member's. C gives a bit-field no address, which is why it is
	// not a member of the record in its own right.
	bits *bitfield
}

// bitfield is one C bit-field's place inside the run of bytes it shares
// with its neighbours: the first bit, counted from that run's first
// byte, and how many. Bit zero of a run is the least significant bit of
// its first byte, which is what x86-64 means by a bit-field.
//
// The declared type is not here because storage does not keep it — it
// decided where the bits went (a bit-field may not cross a boundary of
// its own type's size, which is what makes the packing predictable) and
// what the struct's alignment is, and those are already recorded.
type bitfield struct {
	offset int
	width  int
	signed bool
}

var (
	typeVoid   = &ctype{kind: kVoid}
	typeBool   = &ctype{kind: kBool}
	typeChar   = &ctype{kind: kChar}
	typeUChar  = &ctype{kind: kUChar}
	typeShort  = &ctype{kind: kShort}
	typeUShort = &ctype{kind: kUShort}
	typeInt    = &ctype{kind: kInt}
	typeUInt   = &ctype{kind: kUInt}
	typeLong   = &ctype{kind: kLong}
	typeULong  = &ctype{kind: kULong}
	typeFloat  = &ctype{kind: kFloat}
	typeDouble = &ctype{kind: kDouble}
)

func pointerTo(elem *ctype) *ctype { return &ctype{kind: kPtr, elem: elem} }

func arrayOf(elem *ctype, length int64) *ctype {
	return &ctype{kind: kArray, elem: elem, length: length}
}

func funcOf(result *ctype, params []param, variadic bool) *ctype {
	return &ctype{kind: kFunc, result: result, params: params, variadic: variadic}
}

func (t *ctype) isVoid() bool    { return t.kind == kVoid }
func (t *ctype) isPointer() bool { return t.kind == kPtr }
func (t *ctype) isArray() bool   { return t.kind == kArray }
func (t *ctype) isStruct() bool  { return t.kind == kStruct }
func (t *ctype) isUnion() bool   { return t.kind == kUnion }
func (t *ctype) isFunc() bool    { return t.kind == kFunc }

// isMembered is a struct or a union: an aggregate whose parts have
// names. The two are reached the same way and differ in where the parts
// sit, which is the layout table's answer and not this one's.
func (t *ctype) isMembered() bool { return t.kind == kStruct || t.kind == kUnion }

// keyword is how a declaration of this type begins, which is what an
// error message has to say to point at the right line.
func (t *ctype) keyword() string {
	if t.kind == kUnion {
		return "union"
	}
	return "struct"
}

// isFuncPointer is what a call through a variable needs, and what a
// callback table's fields are made of.
func (t *ctype) isFuncPointer() bool { return t.kind == kPtr && t.elem.isFunc() }

// isVoidPointer is `void *`: an address with nothing said about what is
// at it. Everything that needs the target's type — a dereference, an
// index, arithmetic, a sizeof — asks this first, because C says all four
// are errors on one.
func (t *ctype) isVoidPointer() bool { return t.kind == kPtr && t.elem.isVoid() }

// isOpaquePointer is a pointer to a struct tag no file completes, which
// C calls a pointer to an incomplete type. A program may hold one, pass
// it, compare it and hand it back to whoever made it, and may do nothing
// that measures the object — which is the same list `void *` refuses,
// for the same reason and with a name still attached.
//
// It is what a library header is written in: `struct SDL_Window;` and
// then `SDL_Window *` everywhere, so that the caller keeps the type and
// never learns the shape.
func (t *ctype) isOpaquePointer() bool {
	return t.kind == kPtr && t.elem.isMembered() && t.elem.fields == nil
}

// The two parameters a variadic function is given beyond the ones it
// declares: the arguments after the last named one, spilled and reached
// by address, and how many of them there are.
//
// METALS has no variadic call and does not need one. What varies at a C
// call site is the number and types of the arguments; what a callee can
// actually do with them is walk them in order, asking for one type at a
// time — so an array of erased addresses says everything a callee may
// know, and the machinery is a front end's rather than a format's. See
// docs/decisions.md, "Varargs are an array a caller builds".
//
// Both are ordinary C types, which is what makes `va_list` an ordinary C
// struct in <stdarg.h> rather than something the front end has to
// synthesize.
func vaSlotsType() *ctype { return pointerTo(pointerTo(typeVoid)) }
func vaCountType() *ctype { return typeLong }

// VaSlotsName and VaCountName are the parameters' names. Neither is in
// C's identifier space, so nothing a translation unit declares can
// collide with one, and no C expression can name them: `va_start` is how
// a program reaches them.
//
// They are exported because they are how a module says a function is
// variadic. METALS has no variadic call and should not grow a flag for
// one — the machinery is a front end's — so what is left is the
// convention, and an importer reading the two names off the end of a
// parameter list is reading the convention rather than guessing at a
// shape two ordinary parameters could also have.
const (
	VaSlotsName = "va$slots"
	VaCountName = "va$count"
)

// signature is the parameter list as METALS sees it, which for a
// variadic function is the declared parameters plus the two that carry
// what came after them.
func (t *ctype) signature() []param {
	if !t.variadic {
		return t.params
	}
	out := make([]param, 0, len(t.params)+2)
	out = append(out, t.params...)
	return append(out,
		param{name: VaSlotsName, typ: vaSlotsType()},
		param{name: VaCountName, typ: vaCountType()})
}

func (t *ctype) isFloat() bool { return t.kind == kFloat || t.kind == kDouble }

// isCharacter is what a string literal may initialize an array of, which
// is a char of either signedness and nothing else.
func (t *ctype) isCharacter() bool { return t.kind == kChar || t.kind == kUChar }

// isComplete reports whether a type has a size. An array of an
// incomplete type is incomplete, and a declaration of one has nothing to
// reserve.
func (t *ctype) isComplete() bool {
	switch t.kind {
	case kVoid, kFunc:
		return false
	case kStruct, kUnion:
		return t.fields != nil
	case kArray:
		return t.length >= 0 && t.elem.isComplete()
	}
	return true
}

func (t *ctype) isInteger() bool {
	switch t.kind {
	case kBool, kChar, kUChar, kShort, kUShort, kInt, kUInt, kLong, kULong:
		return true
	}
	return false
}

func (t *ctype) isArithmetic() bool { return t.isInteger() || t.isFloat() }

// isScalar is what a condition, a `!` and a `&&` accept: something that
// compares against zero.
func (t *ctype) isScalar() bool { return t.isArithmetic() || t.isPointer() }

func (t *ctype) isUnsigned() bool {
	switch t.kind {
	case kBool, kUChar, kUShort, kUInt, kULong:
		return true
	}
	return false
}

// rank orders the integer types for the usual arithmetic conversions.
// Everything below int promotes to int first, so only int and long carry
// distinct ranks here.
func (t *ctype) rank() int {
	switch t.kind {
	case kLong, kULong:
		return 2
	default:
		return 1
	}
}

func (t *ctype) String() string {
	switch t.kind {
	case kVoid:
		return "void"
	case kBool:
		return "_Bool"
	case kChar:
		return "char"
	case kUChar:
		return "unsigned char"
	case kShort:
		return "short"
	case kUShort:
		return "unsigned short"
	case kInt:
		return "int"
	case kUInt:
		return "unsigned int"
	case kLong:
		return "long"
	case kULong:
		return "unsigned long"
	case kFloat:
		return "float"
	case kDouble:
		return "double"
	case kPtr:
		return t.elem.String() + " *"
	case kArray:
		if t.length < 0 {
			return t.elem.String() + "[]"
		}
		return t.elem.String() + "[" + strconv.FormatInt(t.length, 10) + "]"
	case kStruct, kUnion:
		return t.keyword() + " " + t.tag
	case kFunc:
		parts := make([]string, len(t.params))
		for i, p := range t.params {
			parts[i] = p.typ.String()
		}
		if t.variadic {
			parts = append(parts, "...")
		} else if len(parts) == 0 {
			parts = append(parts, "void")
		}
		return t.result.String() + "(" + strings.Join(parts, ", ") + ")"
	}
	return "?"
}

// equals is C's type identity, which for this subset is structural all
// the way down except at a struct, where the tag decides.
func (a *ctype) equals(b *ctype) bool {
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case kPtr:
		return a.elem.equals(b.elem)
	case kArray:
		return a.length == b.length && a.elem.equals(b.elem)
	case kStruct, kUnion:
		return a.tag == b.tag
	case kFunc:
		if len(a.params) != len(b.params) || a.variadic != b.variadic || !a.result.equals(b.result) {
			return false
		}
		for i := range a.params {
			if !a.params[i].typ.equals(b.params[i].typ) {
				return false
			}
		}
		return true
	}
	return true
}

// metalsType is the IR type a C type lowers to.
func (t *ctype) metalsType() metals.Type {
	switch t.kind {
	case kBool:
		return metals.TBool
	case kChar:
		return metals.TInt8
	case kUChar:
		return metals.TUint8
	case kShort:
		return metals.TInt16
	case kUShort:
		return metals.TUint16
	case kInt:
		return metals.TInt32
	case kUInt:
		return metals.TUint32
	case kLong:
		return metals.TInt64
	case kULong:
		return metals.TUint64
	case kFloat:
		return metals.TF32
	case kDouble:
		return metals.TF64
	case kPtr:
		// A pointer to a function is a code pointer, not a pointer to
		// storage: there is no object at the other end to load from, and
		// METALS' `ptr<T>` addresses an object. It is nullable for the
		// same reason every C pointer is.
		if t.elem.isFunc() {
			return &metals.Opt{Elem: t.elem.metalsType()}
		}
		// A `void *` is an address with the target's type erased, which
		// is what `anyptr` is. C already forbids everything erasure
		// costs — `*p`, `p + 1` and `sizeof *p` are all errors on one —
		// so nothing is lost by saying so in the IR, and `sizeof(void *)`
		// still answers 8.
		if t.elem.isVoid() {
			return &metals.Opt{Elem: &metals.AnyPtr{}}
		}
		// A C pointer is nullable and METALS' is not, so the IR type is
		// the optional one. Every dereference is an opt.unwrap, which is
		// where a null pointer stops being undefined and starts being an
		// abort with a message.
		return &metals.Opt{Elem: &metals.Ptr{Elem: t.elem.metalsType()}}
	case kFunc:
		var params []metals.Type
		for _, p := range t.signature() {
			params = append(params, p.typ.metalsType())
		}
		var result metals.Type
		if !t.result.isVoid() {
			result = t.result.metalsType()
		}
		return &metals.CodeType{Params: params, Result: result}
	case kArray:
		// An array is storage and not a value: it never reaches a
		// register, because C decays it to a pointer everywhere but a
		// declaration and a sizeof.
		return &metals.ArrayOf{Elem: t.elem.metalsType(), Len: t.length}
	case kStruct, kUnion:
		return &metals.Named{Name: t.tag}
	}
	return nil
}

// promote is C's integer promotion: everything narrower than int becomes
// int, because that is the type C computes in. A `char + char` is int
// arithmetic in C and has to be i32 arithmetic here too, or the result
// wraps at a width the source language never wraps at.
func promote(t *ctype) *ctype {
	switch t.kind {
	case kBool, kChar, kUChar, kShort, kUShort:
		return typeInt
	}
	return t
}

// usualArithmetic is the common type two operands of a binary operator
// are converted to before it applies.
func usualArithmetic(a, b *ctype) *ctype {
	if a.kind == kDouble || b.kind == kDouble {
		return typeDouble
	}
	if a.kind == kFloat || b.kind == kFloat {
		return typeFloat
	}

	a, b = promote(a), promote(b)
	if a.kind == b.kind {
		return a
	}
	if a.isUnsigned() == b.isUnsigned() {
		if a.rank() >= b.rank() {
			return a
		}
		return b
	}

	// One of each sign. The unsigned one wins at equal or greater rank;
	// otherwise the signed type is wide enough to hold every value of the
	// unsigned one, which at these two ranks it always is.
	unsigned, signed := a, b
	if b.isUnsigned() {
		unsigned, signed = b, a
	}
	if unsigned.rank() >= signed.rank() {
		return unsigned
	}
	return signed
}

// unsignedOf is the same width read without a sign, which is what a
// bitcast between the two needs to name.
func unsignedOf(t *ctype) *ctype {
	switch t.kind {
	case kChar:
		return typeUChar
	case kShort:
		return typeUShort
	case kInt:
		return typeUInt
	case kLong:
		return typeULong
	}
	return t
}

// signedOf is unsignedOf's inverse.
func signedOf(t *ctype) *ctype {
	switch t.kind {
	case kUChar:
		return typeChar
	case kUShort:
		return typeShort
	case kUInt:
		return typeInt
	case kULong:
		return typeLong
	}
	return t
}

// width is a scalar's size in bytes, and 0 for anything else. Only the
// conversion rules use it, and only to tell a widening from a narrowing.
func width(t *ctype) int {
	switch t.kind {
	case kBool, kChar, kUChar:
		return 1
	case kShort, kUShort:
		return 2
	case kInt, kUInt, kFloat:
		return 4
	case kLong, kULong, kDouble, kPtr:
		return 8
	}
	return 0
}
