package cc

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// C's type system, as far as this front end reads it, and the mapping
// onto METALS' types.
//
// The mapping is a table and not a translation, which is the whole point
// of a direct front end: an `int` is an `i32` and a `struct Point *` is a
// `ptr<%Point>`, and what those occupy in memory is C's answer already,
// because METALS' layout rule *is* C's (docs/native.md). Nothing is
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
)

type ctype struct {
	kind typeKind
	// elem is a pointer's target or an array's element type.
	elem *ctype
	// length is an array's element count, or -1 for the unspecified
	// length a parameter may be written with.
	length int64
	// tag and fields describe a struct. The tag is also the METALS type
	// name, so a C program's `struct Point` is a `%Point`.
	tag    string
	fields []field
}

type field struct {
	name string
	typ  *ctype
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

func (t *ctype) isVoid() bool    { return t.kind == kVoid }
func (t *ctype) isPointer() bool { return t.kind == kPtr }
func (t *ctype) isArray() bool   { return t.kind == kArray }
func (t *ctype) isStruct() bool  { return t.kind == kStruct }
func (t *ctype) isFloat() bool   { return t.kind == kFloat || t.kind == kDouble }

// isCharacter is what a string literal may initialize an array of, which
// is a char of either signedness and nothing else.
func (t *ctype) isCharacter() bool { return t.kind == kChar || t.kind == kUChar }

// isComplete reports whether a type has a size. An array of an
// incomplete type is incomplete, and a declaration of one has nothing to
// reserve.
func (t *ctype) isComplete() bool {
	switch t.kind {
	case kVoid:
		return false
	case kStruct:
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
	case kStruct:
		return "struct " + t.tag
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
	case kStruct:
		return a.tag == b.tag
	}
	return true
}

// metalsType is the IR type a C type lowers to. A void pointer has no
// answer — METALS has no untyped pointer and should not grow one to hold
// C's — so it is refused where it is written rather than mapped to
// something that would silently lose the target's type.
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
		// A C pointer is nullable and METALS' is not, so the IR type is
		// the optional one. Every dereference is an opt.unwrap, which is
		// where a null pointer stops being undefined and starts being an
		// abort with a message.
		return &metals.Opt{Elem: &metals.Ptr{Elem: t.elem.metalsType()}}
	case kArray:
		// An array is storage and not a value: it never reaches a
		// register, because C decays it to a pointer everywhere but a
		// declaration and a sizeof.
		return &metals.ArrayOf{Elem: t.elem.metalsType(), Len: t.length}
	case kStruct:
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
