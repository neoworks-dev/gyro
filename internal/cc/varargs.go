package cc

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Varargs: the two builtins <stdarg.h> is written over.
//
// METALS has no variadic call and does not need one. What varies at a C
// call site is how many arguments there are and what types they have;
// what the callee can do with them is walk them in order, asking for one
// type at a time. So the caller spills each argument after the last
// named one into a cell of its own and hands over an array of erased
// addresses plus a count, and `va_arg` walks that.
//
// The indirection is forced and is worth naming. One array cannot hold
// an `int`, a `double` and a `char *` directly, because METALS will not
// let a pointer and a number share a slot — which is the same rule that
// keeps linear memory and `ptr<T>` apart, and it is right. Addresses are
// all one type, so an array of them holds anything.
//
// What it buys back is that `va_arg` is checked. Its type reaches a
// `ptr.cast`, so the evaluator catches a format string that lies; and
// the count is carried, so reading one argument too many aborts with a
// message on every tier rather than reading whatever the stack held.
// C cannot say either of those things.
//
// See docs/decisions.md, "Varargs are an array a caller builds".

// vaListTag is the struct <stdarg.h> declares and this file knows the
// shape of: the cursor into the array of addresses, and how many are
// left. Its name is in the implementation's namespace, so a program that
// declares one differently has redefined a reserved name — which is why
// the shape is checked rather than assumed.
const vaListTag = "__va_list_tag"

const (
	vaCursorField = 0
	vaLeftField   = 1
)

// vaListPointer is what both builtins take: the `struct __va_list_tag *`
// a `va_list` decays to. C's `va_list` is an array of one, which is how
// glibc makes `vprintf(fmt, ap)` advance the cursor the caller declared
// rather than a copy of it.
func (f *funcLowerer) vaListPointer(e expr, pos position, what string) value {
	v := f.expr(e)
	if !v.typ.isPointer() || !v.typ.elem.isStruct() || v.typ.elem.tag != vaListTag {
		fail(pos, "%s wants a va_list, not a %s", what, v.typ)
	}
	shape := v.typ.elem
	if len(shape.fields) != 2 ||
		!shape.fields[vaCursorField].typ.equals(vaSlotsType()) ||
		!shape.fields[vaLeftField].typ.equals(vaCountType()) {
		fail(pos, "struct %s is <stdarg.h>'s, and this program declares it as something else", vaListTag)
	}
	return value{reg: f.deref(v), typ: shape}
}

// vaStart fills a va_list from the enclosing function's own hidden
// parameters. That is the whole reason it is a builtin: `va$slots` and
// `va$count` are not in C's identifier space and no C expression can
// name them.
//
// The `last` argument C writes is not used, and cannot be: this front
// end does not lay the named parameters out in memory next to the
// unnamed ones, so there is nothing to measure from. C only needs it
// because a register save area has to know where the named arguments
// stopped.
func (f *funcLowerer) vaStart(e *vaStartExpr) value {
	if !f.fn.variadic {
		fail(e.pos, "va_start is only meaningful in a function declared with `...`, and %s is not", f.fn.name)
	}
	list := f.vaListPointer(e.ap, e.pos, "va_start")

	slots := f.lookup(vaSlotsName)
	count := f.lookup(vaCountName)
	f.emit(&metals.Store{Ptr: f.field(list, vaCursorField), Val: f.load(value{reg: slots.ptr, typ: slots.typ}).reg})
	f.emit(&metals.Store{Ptr: f.field(list, vaLeftField), Val: f.load(value{reg: count.ptr, typ: count.typ}).reg})
	return value{typ: typeVoid}
}

// vaArg reads the next argument at the type the caller of the macro
// named, and moves the cursor past it.
//
// The type is a claim — C's is too, and C's is unchecked — so it becomes
// a `ptr.cast`, which the evaluator holds the program to. The count is
// checked before anything is read, which is the part C has no way to do:
// a format string that promises more arguments than were passed aborts
// here instead of walking off the end.
func (f *funcLowerer) vaArg(e *vaArgExpr) value {
	if !e.typ.isComplete() {
		fail(e.pos, "va_arg needs a type with a size, and %s has none", e.typ)
	}
	if promoted := defaultPromote(e.typ); e.typ.isArithmetic() && !promoted.equals(e.typ) {
		// C says this is undefined rather than a conversion, and it is
		// undefined for a reason worth repeating: the argument became an
		// `int` at the call, so nothing of the narrower type was ever
		// passed.
		fail(e.pos, "an argument after the `...` is promoted, so va_arg reads %s where you wrote %s", promoted, e.typ)
	}
	list := f.vaListPointer(e.ap, e.pos, "va_arg")

	left := f.load(value{reg: f.field(list, vaLeftField), typ: vaCountType()})
	empty := f.temp()
	f.emit(&metals.Bin{Dst: empty, Op: "le", A: left.reg, B: f.constant(vaCountType(), "0")})
	f.open()
	f.emit(&metals.Abort{Message: "va_arg read past the arguments this call was given"})
	f.emit(&metals.If{Cond: empty, Then: f.close()})

	// The cursor points at the address of the next argument, and the
	// argument is at that address.
	cursor := f.load(value{reg: f.field(list, vaCursorField), typ: vaSlotsType()})
	slot := f.load(value{reg: f.deref(cursor), typ: pointerTo(typeVoid)})
	read := f.rvalue(value{reg: f.deref(f.convert(slot, pointerTo(e.typ), e.pos)), typ: e.typ})

	stepped := f.step(cursor, value{reg: f.constant(typeLong, "1"), typ: typeLong}, false, e.pos)
	f.emit(&metals.Store{Ptr: f.field(list, vaCursorField), Val: stepped.reg})
	remaining := f.temp()
	f.emit(&metals.Bin{Dst: remaining, Op: "wsub", A: left.reg, B: f.constant(vaCountType(), "1")})
	f.emit(&metals.Store{Ptr: f.field(list, vaLeftField), Val: remaining})

	return read
}

// field is the address of one of a va_list's two members.
func (f *funcLowerer) field(list value, index int) string {
	reg := f.temp()
	f.emit(&metals.FieldPtr{Dst: reg, Ptr: list.reg, Index: index})
	return reg
}

// varargPack builds what a variadic callee is handed: an array of the
// addresses of the arguments after the last named one, and how many
// there are.
//
// The default argument promotions run first, because they are C's rule
// for what a `...` argument becomes and the callee's `va_arg` is written
// against their result: `va_arg(ap, int)` after passing a `char` has to
// find an `int`.
func (f *funcLowerer) varargPack(extra []expr, pos position) (slots, count string) {
	addresses := make([]string, len(extra))
	for i, arg := range extra {
		v := f.expr(arg)
		if v.typ.isVoid() {
			fail(pos, "an argument after the `...` has no value to pass")
		}
		if v.typ.isArithmetic() {
			v = f.convert(v, defaultPromote(v.typ), pos)
		}
		cell := f.temp()
		f.emit(&metals.Slot{Dst: cell, Type: v.typ.metalsType()})
		f.emit(&metals.Store{Ptr: cell, Val: v.reg})
		erased := f.temp()
		f.emit(&metals.PtrErase{Dst: erased, X: cell})
		wrapped := f.temp()
		f.emit(&metals.OptWrap{Dst: wrapped, X: erased})
		addresses[i] = wrapped
	}

	count = f.constant(vaCountType(), strconv.Itoa(len(extra)))
	if len(extra) == 0 {
		// No arguments and so no array. A `void **` is nullable, and
		// va_arg reaches the count before it reaches the cursor.
		return f.zero(vaSlotsType()), count
	}

	slotType := vaSlotsType().elem
	array := f.temp()
	f.emit(&metals.Slot{Dst: array, Type: arrayOf(slotType, int64(len(extra))).metalsType()})
	first := f.temp()
	f.emit(&metals.ElemPtr{Dst: first, Ptr: array})
	for i, address := range addresses {
		f.emit(&metals.Store{Ptr: f.elementAt(first, i), Val: address})
	}
	return f.wrapPointer(first, slotType).reg, count
}

// defaultPromote is C's rule for an argument with no parameter to
// convert to: everything narrower than `int` becomes one, and a `float`
// becomes a `double`. It is the integer promotion plus that second
// clause, which only the `...` case has.
func defaultPromote(t *ctype) *ctype {
	if t.kind == kFloat {
		return typeDouble
	}
	return promote(t)
}
