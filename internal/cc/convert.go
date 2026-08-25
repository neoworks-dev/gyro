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

// convert is an implicit conversion: what an assignment, an argument, a
// return and an initializer apply on their own.
func (f *funcLowerer) convert(v value, to *ctype, pos position) value {
	return f.convertTo(v, to, pos, false)
}

// cast is the conversion a program wrote, which is C's way of taking
// responsibility for one. The only place the two differ is pointers:
// assigning one object pointer to a pointer of another type is a
// constraint violation in C, and letting it through here would quietly
// undo the thing an opaque handle exists to keep — that a session and a
// ticket are two types. Through `void *` nothing is written in either
// direction, which is C's own exception and the reason the type is
// there.
func (f *funcLowerer) cast(v value, to *ctype, pos position) value {
	return f.convertTo(v, to, pos, true)
}

func (f *funcLowerer) convertTo(v value, to *ctype, pos position, written bool) value {
	if v.typ.equals(to) {
		return v
	}

	if to.isPointer() {
		if !v.typ.isPointer() {
			fail(pos, "cannot convert a %s to a %s; this front end has no integer-to-pointer conversion", v.typ, to)
		}
		if !written && !to.isVoidPointer() && !v.typ.isVoidPointer() {
			fail(pos, "cannot convert a %s to a %s without a cast; they point at different types", v.typ, to)
		}
		return f.convertPointer(v, to, pos)
	}
	if v.typ.isPointer() {
		if to.kind == kBool {
			return f.toBool(v)
		}
		fail(pos, "cannot convert a %s to a %s", v.typ, to)
	}

	if to.isVoid() {
		// `(void)x` is C's way of saying a value is deliberately
		// discarded. It is a conversion in the grammar and a statement
		// about intent in practice, and the value is already lowered —
		// what it does not have any more is a type anything can read.
		return value{typ: typeVoid}
	}
	if v.typ.isVoid() {
		fail(pos, "cannot convert a %s to a %s", v.typ, to)
	}
	if v.typ.isMembered() || to.isMembered() {
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
		return f.convertTo(widened, to, pos, written)
	}

	if v.typ.isFloat() || to.isFloat() {
		reg := f.temp()
		f.emit(&metals.Conv{Dst: reg, X: v.reg, To: to.metalsType()})
		return value{reg: reg, typ: to}
	}

	return f.convertInt(v, to)
}

// convertPointer is a cast between two pointer types, which in C changes
// nothing about the address and everything about what may be done with
// it. Here it is spelled in two halves: `ptr.erase` takes the target's
// type off and cannot fail, `ptr.cast` puts one back and is a claim.
//
// A cast between two object pointers goes through both, which is what
// makes it a claim rather than a coincidence — `(struct T *)p` says the
// object at that address is a T, the way C's own aliasing rules say it
// must be, and the evaluator is the tier that can tell when it is not.
//
// A function pointer is not an object pointer and does not join in.
// `void *` addresses storage; converting one to a code pointer is a
// POSIX guarantee about a machine, not a C one about a program, and
// METALS has two types here precisely because they are not the same
// thing.
func (f *funcLowerer) convertPointer(v value, to *ctype, pos position) value {
	if v.typ.isFuncPointer() || to.isFuncPointer() {
		fail(pos, "cannot convert a %s to a %s; a pointer to a function and a pointer to an object are different types here", v.typ, to)
	}

	erased := v.reg
	if !v.typ.isVoidPointer() {
		reg := f.temp()
		f.emit(&metals.PtrErase{Dst: reg, X: v.reg})
		erased = reg
	}
	if to.isVoidPointer() {
		return value{reg: erased, typ: to}
	}
	reg := f.temp()
	f.emit(&metals.PtrCast{Dst: reg, X: erased, To: to.metalsType()})
	return value{reg: reg, typ: to}
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
