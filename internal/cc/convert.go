package cc

import "github.com/gyro-lang/gyro/internal/metals"

// C's conversions, spelled with METALS' three.
//
// The three exist because they answer three different questions, and C
// needs all of them: `conv` is value-preserving and aborts on a loss,
// `trunc` keeps the low bits, and `bitcast` reinterprets. A C cast is
// `trunc` when it narrows, and a C widening is `conv` — except from a
// signed type to a wider unsigned one, where C sign-extends and *then*
// reinterprets, which is the two of them in that order.
//
// The floating conversions are all `conv`: METALS defines float to
// integer as truncation toward zero, which is C's rule, and aborts only
// when the value is outside the target's range, which is where C stops
// defining anything at all.

func (f *funcLowerer) convert(v value, to *ctype, pos position) value {
	if v.typ.equals(to) {
		return v
	}

	if to.isPointer() {
		if !v.typ.isPointer() {
			fail(pos, "cannot convert a %s to a %s; this front end has no integer-to-pointer conversion", v.typ, to)
		}
		// Two pointer types with different targets have the same
		// representation in C and different ones here, and there is no
		// void* to launder one through. Refusing is the honest answer
		// until METALS grows something for it.
		fail(pos, "cannot convert a %s to a %s; a pointer keeps its target's type here", v.typ, to)
	}
	if v.typ.isPointer() {
		if to.kind == kBool {
			return f.toBool(v)
		}
		fail(pos, "cannot convert a %s to a %s", v.typ, to)
	}

	if v.typ.isVoid() || to.isVoid() {
		fail(pos, "cannot convert a %s to a %s", v.typ, to)
	}
	if v.typ.isStruct() || to.isStruct() {
		fail(pos, "cannot convert a %s to a %s", v.typ, to)
	}

	if to.kind == kBool {
		return f.toBool(v)
	}
	if v.typ.kind == kBool {
		// A _Bool is a METALS bool, which is not a number in this IR the
		// way it is in C, so it becomes one the same way a comparison
		// does.
		widened := f.fromBool(v.reg)
		return f.convert(widened, to, pos)
	}

	if v.typ.isFloat() || to.isFloat() {
		reg := f.temp()
		f.emit(&metals.Conv{Dst: reg, X: v.reg, To: to.metalsType()})
		return value{reg: reg, typ: to}
	}

	return f.convertInt(v, to)
}

func (f *funcLowerer) convertInt(v value, to *ctype) value {
	from := v.typ

	// Narrowing, and same-width sign changes, both keep the low bits.
	if width(to) <= width(from) {
		reg := f.temp()
		f.emit(&metals.Trunc{Dst: reg, X: v.reg, To: to.metalsType()})
		return value{reg: reg, typ: to}
	}

	// Widening a signed value into a wider unsigned type is C's one
	// two-step: the sign extends first and the result is read unsigned.
	if !from.isUnsigned() && to.isUnsigned() {
		wide := signedOf(to)
		extended := f.temp()
		f.emit(&metals.Conv{Dst: extended, X: v.reg, To: wide.metalsType()})
		reg := f.temp()
		f.emit(&metals.Trunc{Dst: reg, X: extended, To: to.metalsType()})
		return value{reg: reg, typ: to}
	}

	reg := f.temp()
	f.emit(&metals.Conv{Dst: reg, X: v.reg, To: to.metalsType()})
	return value{reg: reg, typ: to}
}

// toBool is the conversion to _Bool, which in C is a comparison and not
// a narrowing: every non-zero value becomes 1, including one whose low
// bits are all zero.
func (f *funcLowerer) toBool(v value) value {
	flag := f.temp()
	f.emit(&metals.Bin{Dst: flag, Op: "ne", A: v.reg, B: f.zero(v.typ)})
	return value{reg: flag, typ: typeBool}
}
