package cc

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Expressions. An rvalue is a register and its C type; an lvalue is a
// register holding a ptr<T> to the object, with the object's type
// alongside. Everything that can be assigned to goes through lvalue, and
// everything else reads one and loads.

func (f *funcLowerer) expr(e expr) value {
	switch e := e.(type) {
	case *intLit:
		text := strconv.FormatInt(int64(e.val), 10)
		if e.typ.isUnsigned() {
			text = strconv.FormatUint(e.val, 10)
		}
		return value{reg: f.constant(e.typ, text), typ: e.typ}

	case *floatLit:
		return value{reg: f.constant(e.typ, strconv.FormatFloat(e.val, 'g', -1, 64)), typ: e.typ}

	case *identExpr:
		// An enumeration constant is looked up here rather than during
		// the parse, because a local of the same name shadows one and
		// only this scope chain knows that.
		if f.lookup(e.name) == nil {
			if enumerator, ok := f.l.scope.enums[e.name]; ok {
				text := strconv.FormatInt(enumerator, 10)
				return value{reg: f.constant(typeInt, text), typ: typeInt}
			}
		}
		return f.rvalue(f.lvalue(e))

	case *memberExpr:
		return f.rvalue(f.lvalue(e))

	case *strLit:
		return f.rvalue(f.lvalue(e))

	case *sizeofExpr:
		return f.sizeOf(e)

	case *unaryExpr:
		if e.op == "*" {
			return f.rvalue(f.lvalue(e))
		}
		return f.unary(e)

	case *incExpr:
		return f.increment(e)

	case *castExpr:
		return f.convert(f.exprAs(e.x, e.typ), e.typ, e.pos)

	case *callExpr:
		return f.call(e)

	case *condExpr:
		return f.conditional(e)

	case *assignExpr:
		return f.assign(e)

	case *binaryExpr:
		if e.op == "&&" || e.op == "||" {
			return f.shortCircuit(e)
		}
		return f.binaryPair(e)
	}
	panic("cc: an expression kind with no lowering")
}

// exprAs lowers an expression that is about to become a value of a known
// type. It exists for the one case the type has to be known first: a
// null pointer constant is the integer 0 written where a pointer goes,
// and its lowering is `const.null` rather than a zero of any width.
func (f *funcLowerer) exprAs(e expr, want *ctype) value {
	if want.isPointer() && isNullConstant(e) {
		reg := f.temp()
		f.emit(&metals.Const{Dst: reg, Type: want.metalsType(), Null: true})
		return value{reg: reg, typ: want}
	}
	return f.expr(e)
}

// isNullConstant is C's null pointer constant: an integer constant
// expression with value 0, optionally cast — which is what makes
// `#define NULL ((void *)0)` mean what every header expects it to.
//
// The cast has to be seen through rather than lowered: `void *` has no
// METALS type, and a null pointer of the type it is going to be does not
// need one.
func isNullConstant(e expr) bool {
	switch e := e.(type) {
	case *intLit:
		return e.val == 0
	case *castExpr:
		if e.typ.isInteger() || (e.typ.isPointer() && e.typ.elem.isVoid()) {
			return isNullConstant(e.x)
		}
	}
	return false
}

func (f *funcLowerer) load(lv value) value {
	reg := f.temp()
	f.emit(&metals.Load{Dst: reg, Ptr: lv.reg})
	return value{reg: reg, typ: lv.typ}
}

// rvalue turns an lvalue into the value C reads out of it. Everything
// loads except an array, which has no value: naming one anywhere but in
// a sizeof or a declaration produces a pointer to its first element.
func (f *funcLowerer) rvalue(lv value) value {
	if lv.typ.isArray() {
		return f.decay(lv)
	}
	if lv.typ.isFunc() {
		// A function designator decays to a pointer to itself, and the
		// register that reached it already is that pointer: a function
		// is not storage, so there is nothing at the other end to load.
		return f.wrapCode(lv.reg, lv.typ)
	}
	return f.load(lv)
}

// decay is C's array-to-pointer conversion. The METALS side is elem.ptr,
// which reaches the array's first element without moving: an array's
// storage begins where its first element does.
func (f *funcLowerer) decay(lv value) value {
	if lv.typ.length < 0 {
		fail(position{}, "an array of unknown length has no first element")
	}
	first := f.temp()
	f.emit(&metals.ElemPtr{Dst: first, Ptr: lv.reg})
	return f.wrapPointer(first, lv.typ.elem)
}

// wrapPointer is how a raw ptr<T> becomes the C pointer type, which is
// the nullable one. See the note at the top of lower.go.
func (f *funcLowerer) wrapPointer(raw string, elem *ctype) value {
	wrapped := f.temp()
	f.emit(&metals.OptWrap{Dst: wrapped, X: raw})
	return value{reg: wrapped, typ: pointerTo(elem)}
}

// wrapCode is the same for a code pointer, which is nullable for the
// same reason and unwrapped at the same place: a call through it.
func (f *funcLowerer) wrapCode(raw string, fn *ctype) value {
	wrapped := f.temp()
	f.emit(&metals.OptWrap{Dst: wrapped, X: raw})
	return value{reg: wrapped, typ: pointerTo(fn)}
}

// designator is a function's name used as a value. The address is the
// whole value — there is no object to load — so this is the "lvalue" of
// a function, and rvalue decays it to a pointer the way C does.
func (f *funcLowerer) designator(fn *funcDecl, pos position) value {
	if fn.body == nil {
		fail(pos, "%s is declared but not defined here, and taking the address of a function another module defines is not wired up yet", fn.name)
	}
	reg := f.temp()
	f.emit(&metals.CodeRef{Dst: reg, Callee: fn.symbol})
	return value{reg: reg, typ: funcOf(fn.result, fn.params)}
}

// sizeOf answers `sizeof`. The operand of the expression form is lowered
// to learn its type and then thrown away, which is also C's rule: what
// `sizeof f()` measures is the result type, and the call does not
// happen.
func (f *funcLowerer) sizeOf(e *sizeofExpr) value {
	typ := e.typ
	if typ == nil {
		f.open()
		// The operand of sizeof is the one place an array does not decay:
		// `sizeof a` is the whole array and not the pointer naming it
		// would produce anywhere else. So the type comes from the lvalue
		// where there is one, and only otherwise from the value.
		if f.isLvalue(e.x) {
			typ = f.lvalue(e.x).typ
		} else {
			typ = f.expr(e.x).typ
		}
		f.close()
	}
	if typ.isVoid() {
		fail(e.pos, "sizeof(void) has no answer")
	}
	if !typ.isComplete() {
		fail(e.pos, "%s is incomplete, so it has no size", typ)
	}

	size := f.l.layout.Size(typ.metalsType())
	// C's sizeof has type size_t, which is unsigned long on this target.
	return value{reg: f.constant(typeULong, strconv.Itoa(size)), typ: typeULong}
}

// ---------------------------------------------------------------------------
// Lvalues
// ---------------------------------------------------------------------------

// isLvalue reports whether an expression names storage. It exists for
// sizeof, which is the one operator that wants an operand's type without
// the conversions reading it would apply.
func (f *funcLowerer) isLvalue(e expr) bool {
	switch e := e.(type) {
	case *identExpr:
		return f.lookup(e.name) != nil
	case *memberExpr:
		return true
	case *unaryExpr:
		return e.op == "*"
	case *strLit:
		// An array with static storage, which is why sizeof answers its
		// length rather than a pointer's.
		return true
	}
	return false
}

func (f *funcLowerer) lvalue(e expr) value {
	switch e := e.(type) {
	case *identExpr:
		b := f.lookup(e.name)
		if b == nil {
			if fn, ok := f.l.scope.funcs[e.name]; ok {
				return f.designator(fn, e.pos)
			}
			if _, ok := f.l.scope.enums[e.name]; ok {
				fail(e.pos, "%s is an enumeration constant, which has no storage to assign to", e.name)
			}
			fail(e.pos, "%s is not declared", e.name)
		}
		return value{reg: f.address(b), typ: b.typ}

	case *unaryExpr:
		if e.op != "*" {
			break
		}
		operand := f.expr(e.x)
		if !operand.typ.isPointer() {
			fail(e.pos, "cannot dereference a %s", operand.typ)
		}
		return value{reg: f.deref(operand), typ: operand.typ.elem}

	case *memberExpr:
		return f.member(e)

	case *strLit:
		// The literal's bytes are module storage, and the expression is
		// that array — so it decays like any other, and `"abc"[1]` and
		// `sizeof "abc"` fall out of the same rule.
		ptr := f.temp()
		f.emit(&metals.GlobalPtr{Dst: ptr, Name: f.l.stringData(e.text)})
		return value{reg: ptr, typ: stringType(e.text)}
	}
	fail(e.at(), "this expression cannot be assigned to")
	return value{}
}

// deref opens a C pointer: the opt<ptr<T>> a pointer variable holds
// becomes the ptr<T> an access needs, and a null one aborts there rather
// than being undefined the way C leaves it.
func (f *funcLowerer) deref(v value) string {
	reg := f.temp()
	f.emit(&metals.OptUnwrap{Dst: reg, X: v.reg})
	return reg
}

func (f *funcLowerer) member(e *memberExpr) value {
	var base value
	var record *ctype

	if e.arrow {
		pointer := f.expr(e.x)
		if !pointer.typ.isPointer() || !pointer.typ.elem.isStruct() {
			fail(e.pos, "-> needs a pointer to a struct, not a %s", pointer.typ)
		}
		record = pointer.typ.elem
		base = value{reg: f.deref(pointer), typ: record}
	} else {
		base = f.lvalue(e.x)
		if !base.typ.isStruct() {
			fail(e.pos, ". needs a struct, not a %s", base.typ)
		}
		record = base.typ
	}

	index := -1
	for i, fld := range record.fields {
		if fld.name == e.name {
			index = i
			break
		}
	}
	if index < 0 {
		fail(e.pos, "struct %s has no field %s", record.tag, e.name)
	}

	reg := f.temp()
	f.emit(&metals.FieldPtr{Dst: reg, Ptr: base.reg, Index: index})
	return value{reg: reg, typ: record.fields[index].typ}
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

func (f *funcLowerer) unary(e *unaryExpr) value {
	if e.op == "&" {
		object := f.lvalue(e.x)
		wrapped := f.temp()
		f.emit(&metals.OptWrap{Dst: wrapped, X: object.reg})
		return value{reg: wrapped, typ: pointerTo(object.typ)}
	}

	if e.op == "!" {
		flag := f.condition(e.x)
		inverted := f.temp()
		f.emit(&metals.Un{Dst: inverted, Op: "not", X: flag})
		return f.fromBool(inverted)
	}

	operand := f.expr(e.x)
	if !operand.typ.isArithmetic() {
		fail(e.pos, "unary %s needs a number, not a %s", e.op, operand.typ)
	}
	operand = f.convert(operand, promote(operand.typ), e.pos)

	switch e.op {
	case "+":
		return operand

	case "-":
		if operand.typ.isFloat() {
			reg := f.temp()
			f.emit(&metals.Un{Dst: reg, Op: "neg", X: operand.reg})
			return value{reg: reg, typ: operand.typ}
		}
		// Negation of an unsigned value is modular in C, so it is a
		// subtraction from zero with the wrapping mnemonic rather than
		// `neg`, which asks a signed question.
		reg := f.temp()
		f.emit(&metals.Bin{Dst: reg, Op: "wsub", A: f.zero(operand.typ), B: operand.reg})
		return value{reg: reg, typ: operand.typ}

	case "~":
		if !operand.typ.isInteger() {
			fail(e.pos, "~ needs an integer, not a %s", operand.typ)
		}
		reg := f.temp()
		f.emit(&metals.Un{Dst: reg, Op: "bnot", X: operand.reg})
		return value{reg: reg, typ: operand.typ}
	}
	fail(e.pos, "unary %s is not lowered", e.op)
	return value{}
}

func (f *funcLowerer) increment(e *incExpr) value {
	target := f.lvalue(e.x)
	if target.typ.isPointer() {
		old := f.load(target)
		one := value{reg: f.constant(typeLong, "1"), typ: typeLong}
		updated := f.step(old, one, e.op == "--", e.pos)
		f.emit(&metals.Store{Ptr: target.reg, Val: updated.reg})
		if e.postfix {
			return old
		}
		return updated
	}
	if !target.typ.isArithmetic() {
		fail(e.pos, "%s needs a number, not a %s", e.op, target.typ)
	}

	old := f.load(target)
	op := "wadd"
	if e.op == "--" {
		op = "wsub"
	}
	if target.typ.isFloat() {
		op = "fadd"
		if e.op == "--" {
			op = "fsub"
		}
	}

	updated := f.temp()
	f.emit(&metals.Bin{Dst: updated, Op: op, A: old.reg, B: f.constant(target.typ, "1")})
	f.emit(&metals.Store{Ptr: target.reg, Val: updated})

	if e.postfix {
		return old
	}
	return value{reg: updated, typ: target.typ}
}

// binaryPair lowers a binary operator's two operands. A null pointer
// constant takes its type from the other side — `walk != 0` is a pointer
// comparison and not an integer one — so whichever operand is not the
// literal is lowered first and lends its type to the one that is.
func (f *funcLowerer) binaryPair(e *binaryExpr) value {
	// Only the equality operators take a null pointer constant. `p + 0`
	// steps a pointer by nothing and is not a comparison against null,
	// so the literal keeps its own type there.
	nullable := e.op == "==" || e.op == "!="
	if nullable && isNullConstant(e.a) && !isNullConstant(e.b) {
		b := f.expr(e.b)
		return f.binaryOp(e.op, f.exprAs(e.a, b.typ), b, e.pos)
	}
	a := f.expr(e.a)
	if nullable {
		return f.binaryOp(e.op, a, f.exprAs(e.b, a.typ), e.pos)
	}
	return f.binaryOp(e.op, a, f.expr(e.b), e.pos)
}

// binaryOp applies one operator to two already-lowered operands. It is
// shared with compound assignment, which is the same operator over a
// value the target already holds.
func (f *funcLowerer) binaryOp(op string, a, b value, pos position) value {
	if a.typ.isPointer() || b.typ.isPointer() {
		return f.pointerOp(op, a, b, pos)
	}
	if !a.typ.isArithmetic() || !b.typ.isArithmetic() {
		fail(pos, "%s does not apply to a %s and a %s", op, a.typ, b.typ)
	}

	// A shift is the exception to the usual arithmetic conversions: each
	// operand promotes on its own and the result is the left one's type.
	// METALS wants one type for both, so the count is converted to it.
	if op == "<<" || op == ">>" {
		left := f.convert(a, promote(a.typ), pos)
		if !left.typ.isInteger() {
			fail(pos, "%s needs an integer, not a %s", op, a.typ)
		}
		count := f.convert(b, left.typ, pos)
		mnemonic := "shl"
		if op == ">>" {
			mnemonic = "shr"
		}
		reg := f.temp()
		f.emit(&metals.Bin{Dst: reg, Op: mnemonic, A: left.reg, B: count.reg})
		return value{reg: reg, typ: left.typ}
	}

	common := usualArithmetic(a.typ, b.typ)
	left := f.convert(a, common, pos)
	right := f.convert(b, common, pos)

	if mnemonic, ok := comparisons[op]; ok {
		flag := f.temp()
		f.emit(&metals.Bin{Dst: flag, Op: mnemonic, A: left.reg, B: right.reg})
		return f.fromBool(flag)
	}

	mnemonic := arithmetic[op]
	if common.isFloat() {
		mnemonic = floatArithmetic[op]
	}
	if mnemonic == "" {
		fail(pos, "%s does not apply to a %s", op, common)
	}
	if !common.isFloat() && (op == "&" || op == "|" || op == "^" || op == "%") && !common.isInteger() {
		fail(pos, "%s needs integers", op)
	}

	reg := f.temp()
	f.emit(&metals.Bin{Dst: reg, Op: mnemonic, A: left.reg, B: right.reg})
	return value{reg: reg, typ: common}
}

var comparisons = map[string]string{
	"==": "eq", "!=": "ne", "<": "lt", "<=": "le", ">": "gt", ">=": "ge",
}

// C's integer arithmetic wraps here rather than trapping; see the note
// at the top of lower.go.
var arithmetic = map[string]string{
	"+": "wadd", "-": "wsub", "*": "wmul", "/": "idiv", "%": "irem",
	"&": "band", "|": "bor", "^": "bxor",
}

var floatArithmetic = map[string]string{
	"+": "fadd", "-": "fsub", "*": "fmul", "/": "fdiv",
}

// pointerOp is the operators C defines over pointers: stepping one by a
// count, subtracting two, and comparing two.
func (f *funcLowerer) pointerOp(op string, a, b value, pos position) value {
	switch op {
	case "+":
		// `n + p` is `p + n`; addition is the one operator where either
		// side may be the pointer.
		if !a.typ.isPointer() {
			a, b = b, a
		}
		if !b.typ.isInteger() {
			fail(pos, "a pointer takes an integer offset, not a %s", b.typ)
		}
		return f.step(a, b, false, pos)

	case "-":
		if !a.typ.isPointer() {
			fail(pos, "%s does not apply to a %s and a %s", op, a.typ, b.typ)
		}
		if b.typ.isInteger() {
			return f.step(a, b, true, pos)
		}
		if !b.typ.isPointer() || !a.typ.equals(b.typ) {
			fail(pos, "subtracting pointers needs two of one type, not a %s and a %s", a.typ, b.typ)
		}
		// The difference is in elements, which is what ptr.diff counts,
		// and its type is ptrdiff_t — long on this target.
		reg := f.temp()
		f.emit(&metals.PtrDiff{Dst: reg, A: f.deref(a), B: f.deref(b)})
		return value{reg: reg, typ: typeLong}

	case "==", "!=":
		if !a.typ.isPointer() || !b.typ.isPointer() || !a.typ.equals(b.typ) {
			fail(pos, "%s compares two pointers of one type, not a %s and a %s", op, a.typ, b.typ)
		}
		flag := f.temp()
		f.emit(&metals.Bin{Dst: flag, Op: comparisons[op], A: a.reg, B: b.reg})
		return f.fromBool(flag)

	case "<", "<=", ">", ">=":
		if !a.typ.isPointer() || !b.typ.isPointer() || !a.typ.equals(b.typ) {
			fail(pos, "%s orders two pointers of one type, not a %s and a %s", op, a.typ, b.typ)
		}
		// Ordering pointers is only defined inside one object, and inside
		// one object the order is the order of the element counts — so
		// the comparison is on the difference, and METALS needs no
		// ordering over ptr<T> to answer it.
		gap := f.temp()
		f.emit(&metals.PtrDiff{Dst: gap, A: f.deref(a), B: f.deref(b)})
		flag := f.temp()
		f.emit(&metals.Bin{Dst: flag, Op: comparisons[op], A: gap, B: f.constant(typeLong, "0")})
		return f.fromBool(flag)
	}
	fail(pos, "%s does not apply to a %s and a %s", op, a.typ, b.typ)
	return value{}
}

// step is `p + n` and `p - n`: the pointer n elements along, or n back.
func (f *funcLowerer) step(p, n value, back bool, pos position) value {
	count := f.convert(n, typeLong, pos)
	if back {
		negated := f.temp()
		f.emit(&metals.Bin{Dst: negated, Op: "wsub", A: f.zero(typeLong), B: count.reg})
		count = value{reg: negated, typ: typeLong}
	}
	moved := f.temp()
	f.emit(&metals.PtrOffset{Dst: moved, Ptr: f.deref(p), N: count.reg})
	return f.wrapPointer(moved, p.typ.elem)
}

// shortCircuit lowers && and ||. The second operand is evaluated inside
// the branch that needs it, which is the whole meaning of the operator.
func (f *funcLowerer) shortCircuit(e *binaryExpr) value {
	result := f.temp()
	left := f.condition(e.a)

	f.open()
	f.emit(&metals.Const{Dst: result, Type: metals.TInt32, Value: boolLiteral(e.op == "||")})
	shortcut := f.close()

	f.open()
	right := f.condition(e.b)
	f.emit(&metals.If{
		Cond: right,
		Then: []metals.Instr{&metals.Const{Dst: result, Type: metals.TInt32, Value: "1"}},
		Else: []metals.Instr{&metals.Const{Dst: result, Type: metals.TInt32, Value: "0"}},
	})
	full := f.close()

	// `a || b` shortcuts when a is true, `a && b` when a is false.
	branch := &metals.If{Cond: left, Then: shortcut, Else: full}
	if e.op == "&&" {
		branch.Then, branch.Else = full, shortcut
	}
	f.emit(branch)
	return value{reg: result, typ: typeInt}
}

func boolLiteral(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func (f *funcLowerer) conditional(e *condExpr) value {
	cond := f.condition(e.cond)

	// Both arms have to land in one register of one type, so the arms are
	// lowered first to learn what that type is, and the conversion to it
	// is emitted inside each arm where the value exists.
	f.open()
	then := f.expr(e.then)
	thenBody := f.close()

	f.open()
	els := f.expr(e.els)
	elseBody := f.close()

	common := then.typ
	switch {
	case then.typ.equals(els.typ):
	case then.typ.isArithmetic() && els.typ.isArithmetic():
		common = usualArithmetic(then.typ, els.typ)
	default:
		fail(e.pos, "the two arms of ?: are a %s and a %s", then.typ, els.typ)
	}

	result := f.temp()

	f.blocks = append(f.blocks, thenBody)
	converted := f.convert(then, common, e.pos)
	f.emit(&metals.Copy{Dst: result, X: converted.reg})
	thenBody = f.close()

	f.blocks = append(f.blocks, elseBody)
	converted = f.convert(els, common, e.pos)
	f.emit(&metals.Copy{Dst: result, X: converted.reg})
	elseBody = f.close()

	f.emit(&metals.If{Cond: cond, Then: thenBody, Else: elseBody})
	return value{reg: result, typ: common}
}

func (f *funcLowerer) assign(e *assignExpr) value {
	target := f.lvalue(e.target)
	if target.typ.isArray() {
		fail(e.pos, "an array is storage, not a value, so it cannot be assigned to")
	}
	if target.typ.isFunc() {
		fail(e.pos, "a function is not storage, so it cannot be assigned to")
	}

	var result value
	if e.op == "=" {
		result = f.convert(f.exprAs(e.value, target.typ), target.typ, e.pos)
	} else {
		current := f.load(target)
		combined := f.binaryOp(e.op[:len(e.op)-1], current, f.expr(e.value), e.pos)
		result = f.convert(combined, target.typ, e.pos)
	}

	f.emit(&metals.Store{Ptr: target.reg, Val: result.reg})
	return result
}

// call lowers a call. A name that is a function is called directly; a
// name that is a variable, or any other expression, is called through
// whatever function pointer it turns out to hold. C draws the line in
// the same place, which is why the callee is an expression here.
func (f *funcLowerer) call(e *callExpr) value {
	if named, ok := e.callee.(*identExpr); ok && f.lookup(named.name) == nil {
		if callee, known := f.l.scope.funcs[named.name]; known {
			return f.directCall(e, callee)
		}
		fail(named.pos, "%s is not declared", named.name)
	}
	return f.indirectCall(e)
}

func (f *funcLowerer) directCall(e *callExpr, callee *funcDecl) value {
	args := f.callArgs(e, callee.name, callee.params)
	if callee.body == nil {
		fail(e.pos, "%s is declared but not defined here, and calling into another module is not wired up yet", callee.name)
	}

	if callee.result.isVoid() {
		f.emit(&metals.Call{Callee: callee.symbol, Args: args})
		return value{typ: typeVoid}
	}
	reg := f.temp()
	f.emit(&metals.Call{Dst: reg, Callee: callee.symbol, Args: args})
	return value{reg: reg, typ: callee.result}
}

func (f *funcLowerer) indirectCall(e *callExpr) value {
	target := f.expr(e.callee)
	if !target.typ.isFuncPointer() {
		fail(e.pos, "a %s is not something that can be called", target.typ)
	}
	sig := target.typ.elem
	args := f.callArgs(e, "this call", sig.params)

	// The pointer is opened here, so calling through a null one aborts
	// with a message rather than jumping to address zero.
	code := f.deref(target)
	if sig.result.isVoid() {
		f.emit(&metals.CallIndirect{Fn: code, Args: args})
		return value{typ: typeVoid}
	}
	reg := f.temp()
	f.emit(&metals.CallIndirect{Dst: reg, Fn: code, Args: args})
	return value{reg: reg, typ: sig.result}
}

// callArgs converts a call's arguments to the parameter types, which is
// the same work whether the callee is a name or a pointer.
func (f *funcLowerer) callArgs(e *callExpr, what string, params []param) []string {
	if len(e.args) != len(params) {
		fail(e.pos, "%s takes %d argument(s), and this call passes %d",
			what, len(params), len(e.args))
	}
	args := make([]string, len(e.args))
	for i, arg := range e.args {
		want := params[i].typ
		args[i] = f.convert(f.exprAs(arg, want), want, e.pos).reg
	}
	return args
}

// ---------------------------------------------------------------------------
// Truth
// ---------------------------------------------------------------------------

// condition lowers an expression to the METALS bool a branch takes. C
// spells truth as "not equal to zero" for every scalar, and a pointer's
// zero is null.
func (f *funcLowerer) condition(e expr) string {
	v := f.expr(e)
	if v.typ.kind == kBool {
		return v.reg
	}
	if !v.typ.isScalar() {
		fail(e.at(), "a %s is not a condition", v.typ)
	}
	flag := f.temp()
	f.emit(&metals.Bin{Dst: flag, Op: "ne", A: v.reg, B: f.zero(v.typ)})
	return flag
}

// fromBool is the other direction: a comparison in C has type int and
// the value 0 or 1, and METALS' bool is not an integer.
func (f *funcLowerer) fromBool(flag string) value {
	reg := f.temp()
	f.emit(&metals.If{
		Cond: flag,
		Then: []metals.Instr{&metals.Const{Dst: reg, Type: metals.TInt32, Value: "1"}},
		Else: []metals.Instr{&metals.Const{Dst: reg, Type: metals.TInt32, Value: "0"}},
	})
	return value{reg: reg, typ: typeInt}
}
