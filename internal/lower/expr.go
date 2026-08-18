package lower

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// lowerExpr evaluates an expression into a register, coerced to want
// when a target type is given.
func (f *funcLowerer) lowerExpr(e ast.Expr, want types.Type) string {
	if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.Null {
		return f.nullConst(want)
	}
	reg := f.eval(e, want)
	// A condition-shaped node is bool by construction, and sema's
	// condition checker does not record the node itself.
	if isCondShaped(e) {
		return f.snapshot(reg)
	}
	return f.snapshot(f.coerce(reg, f.typeOf(e), want))
}

// snapshot pins a value that a later instruction will read. An assignment
// writes a local's register in place, so a deferred call naming one would
// otherwise run against whatever the local holds at block exit rather
// than what it held where the defer was written.
//
// Only a local's own register needs it: every other register is written
// once, and a local that lives in a slot was already loaded into a fresh
// one.
func (f *funcLowerer) snapshot(reg string) string {
	if f.deferring == 0 || !f.namesLocal(reg) {
		return reg
	}
	held := f.newReg("held")
	f.emit(&metals.Copy{Dst: held, X: reg})
	return held
}

func (f *funcLowerer) namesLocal(reg string) bool {
	for _, scope := range f.scopes {
		for _, entry := range scope.locals {
			if entry.reg == reg && !entry.slot {
				return true
			}
		}
	}
	return false
}

func isCondShaped(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.BinaryExpr:
		switch e.Op {
		case token.AndAnd, token.OrOr, token.Eq, token.NotEq,
			token.Lt, token.LtEq, token.Gt, token.GtEq:
			return true
		}
	case *ast.UnaryExpr:
		return e.Op == token.Bang
	}
	return false
}

func (f *funcLowerer) nullConst(want types.Type) string {
	optional, ok := want.(*types.Optional)
	if !ok {
		f.l.fail("@%s: null needs an optional context", f.fn.Name)
	}
	out := f.newReg("null")
	f.emit(&metals.Const{Dst: out, Type: f.l.metalsType(optional), Null: true})
	return out
}

func (f *funcLowerer) eval(e ast.Expr, want types.Type) string {
	// A folded constant expression emits directly, whatever shape it had.
	if value, ok := f.l.info.Consts[e]; ok {
		if t := f.typeOf(e); types.IsInteger(t) {
			out := f.newReg("c")
			f.emit(&metals.Const{Dst: out, Type: f.l.metalsType(t), Value: value.String()})
			return out
		}
	}

	switch e := e.(type) {
	case *ast.Ident:
		return f.evalIdent(e)
	case *ast.SelfExpr:
		if entry := f.lookup("self"); entry != nil {
			return f.localPlace(entry).load()
		}
		f.l.fail("@%s: self outside a method", f.fn.Name)
	case *ast.Path:
		return f.evalPath(e)
	case *ast.BasicLit:
		return f.evalBasicLit(e)
	case *ast.StringLit:
		return f.evalStringLit(e)
	case *ast.ArrayLit:
		return f.evalArrayLit(e)
	case *ast.TupleLit:
		return f.evalTupleLit(e, want)
	case *ast.StructLit:
		return f.evalStructLit(e)
	case *ast.UnaryExpr:
		return f.evalUnary(e)
	case *ast.BinaryExpr:
		return f.evalBinary(e)
	case *ast.CallExpr:
		result := f.prepareCall(e)()
		if result == "" {
			f.l.fail("@%s: a void call has no value", f.fn.Name)
		}
		return result
	case *ast.SelectorExpr:
		return f.selectorPlace(e).load()
	case *ast.IndexExpr:
		return f.indexPlace(e).load()
	case *ast.TupleIndexExpr:
		return f.tupleElemPlace(e).load()
	case *ast.MatchExpr:
		return f.evalMatch(e, want)
	case *ast.AwaitExpr:
		future := f.lowerExpr(e.X, nil)
		out := f.newReg("awaited")
		f.emit(&metals.Await{Dst: out, X: future})
		return out
	case *ast.RecvExpr:
		pair := f.recvPair(e)
		out := f.newReg("recvd")
		f.emit(&metals.TupleGet{Dst: out, X: pair, Index: 0})
		return out
	case *ast.ChanLit:
		return f.evalChanLit(e)
	case *ast.ClosureExpr:
		return f.evalClosure(e)
	case *ast.CastExpr:
		value := f.lowerExpr(e.X, nil)
		out := f.newReg("cast")
		f.emit(&metals.Conv{Dst: out, X: value, To: f.l.metalsType(f.typeOf(e))})
		return out
	}
	f.l.fail("@%s: cannot lower a %T expression", f.fn.Name, e)
	return ""
}

// ---------------------------------------------------------------------------
// Names
// ---------------------------------------------------------------------------

func (f *funcLowerer) evalIdent(e *ast.Ident) string {
	if entry := f.lookup(e.Name); entry != nil {
		value := f.localPlace(entry).load()
		// Only an optional local can need the narrowing unwrap; callee
		// idents are never recorded, so ask for the use type lazily.
		if _, optional := entry.typ.(*types.Optional); optional {
			return f.narrowed(value, entry.typ, f.typeOf(e))
		}
		return value
	}
	switch decl := f.refOf(e).(type) {
	case *ast.FuncDecl:
		return f.fnValue(decl)
	case *ast.VarDecl:
		return f.moduleConst(decl, e.Name)
	}
	f.l.fail("@%s: %s resolves to nothing lowerable", f.fn.Name, e.Name)
	return ""
}

// narrowed makes the checker's null-narrowing explicit: a use recorded at
// the element type of an optional local unwraps, and the abort inside
// opt.unwrap is the guarantee the checker already proved.
func (f *funcLowerer) narrowed(reg string, declared, used types.Type) string {
	if _, ok := declared.(*types.Optional); !ok {
		return reg
	}
	if _, stillOptional := used.(*types.Optional); stillOptional {
		return reg
	}
	out := f.newReg("nn")
	f.emit(&metals.OptUnwrap{Dst: out, X: reg})
	return out
}

func (f *funcLowerer) evalPath(e *ast.Path) string {
	switch decl := f.refOf(e).(type) {
	case *ast.FuncDecl:
		return f.fnValue(decl)
	case *ast.VarDecl:
		return f.moduleConst(decl, e.Segments[len(e.Segments)-1].Name)
	}
	// A payload-less enum variant used as a value.
	if enum, ok := f.typeOf(e).(*types.Enum); ok {
		name := e.Segments[len(e.Segments)-1].Name
		if variant := enum.VariantNamed(name); variant != nil {
			index := enumVariantIndex(enum, name)
			out := f.newReg("variant")
			typeName := f.l.metalsEnum(enum).(*metals.Named).Name
			f.emit(&metals.MakeVariant{Dst: out, TypeName: typeName, Variant: index})
			return out
		}
	}
	f.l.fail("@%s: cannot lower this path as a value", f.fn.Name)
	return ""
}

func enumVariantIndex(enum *types.Enum, name string) int {
	for i := range enum.Variants {
		if enum.Variants[i].Name == name {
			return i
		}
	}
	return -1
}

// fnValue is a named non-generic function used as a value.
func (f *funcLowerer) fnValue(decl *ast.FuncDecl) string {
	if decl.Body == nil {
		f.l.fail("@%s: a runtime function cannot be a bare value yet", f.fn.Name)
	}
	name := f.l.ensureFunc(decl, nil)
	out := f.newReg("fn")
	f.emit(&metals.FnRef{Dst: out, Callee: name})
	return out
}

// moduleConst reads a module-level constant by calling its synthesized
// getter. There are no mutable METALS globals yet, so a const's
// initializer re-evaluates per use; @comptime folding will subsume this.
func (f *funcLowerer) moduleConst(decl *ast.VarDecl, name string) string {
	if !decl.Const {
		f.l.fail("@%s: module-level var %s is not lowerable yet; only const is", f.fn.Name, name)
	}
	getter := f.l.ensureConstGetter(decl, name)
	out := f.newReg(name)
	f.emit(&metals.Call{Dst: out, Callee: getter, Args: nil})
	return out
}

func (l *lowerer) ensureConstGetter(decl *ast.VarDecl, name string) string {
	fnName := name
	if module := l.moduleOf[decl]; module != nil {
		if tag := l.tags[module.Path]; tag != "" {
			fnName = tag + "::" + name
		}
	}
	if l.lowered[fnName] {
		return fnName
	}
	l.lowered[fnName] = true

	declared := l.info.Locals[decl][name]
	if declared == nil || decl.Value == nil {
		l.fail("const %s has no recorded type or no value", name)
	}
	child := &funcLowerer{
		l:        l,
		decl:     nil,
		fn:       &metals.Func{Name: fnName, Result: l.metalsType(declared)},
		regCount: map[string]int{},
		result:   declared,
	}
	child.addrTaken = map[string]bool{}
	child.blocks = []*[]metals.Instr{&child.fn.Body}
	child.pushScope()
	value := child.lowerExpr(decl.Value, declared)
	child.emit(&metals.Return{Val: value})
	child.popScopeNoDefers()
	l.module.Funcs = append(l.module.Funcs, child.fn)
	return fnName
}

// ---------------------------------------------------------------------------
// Literals
// ---------------------------------------------------------------------------

func (f *funcLowerer) evalBasicLit(e *ast.BasicLit) string {
	t := f.typeOf(e)
	out := f.newReg("c")
	switch e.Kind {
	case token.Int:
		value := e.Value
		if folded, ok := f.l.info.Consts[e]; ok {
			value = folded.String()
		}
		f.emit(&metals.Const{Dst: out, Type: f.l.metalsType(t), Value: value})
	case token.Float:
		f.emit(&metals.Const{Dst: out, Type: f.l.metalsType(t), Value: e.Value})
	case token.String:
		f.emit(&metals.Const{Dst: out, Type: metals.TStr, Value: strconv.Quote(e.Value)})
	case token.Char:
		runes := []rune(e.Value)
		var r rune
		if len(runes) > 0 {
			r = runes[0]
		}
		f.emit(&metals.Const{Dst: out, Type: metals.TRune, Value: strconv.QuoteRune(r)})
	case token.True:
		f.emit(&metals.Const{Dst: out, Type: metals.TBool, Value: "true"})
	case token.False:
		f.emit(&metals.Const{Dst: out, Type: metals.TBool, Value: "false"})
	default:
		f.l.fail("@%s: cannot lower this literal", f.fn.Name)
	}
	return out
}

func (f *funcLowerer) evalArrayLit(e *ast.ArrayLit) string {
	array, ok := f.typeOf(e).(*types.Array)
	if !ok {
		f.l.fail("@%s: an array literal without an array type", f.fn.Name)
	}
	args := make([]string, len(e.Elements))
	for i, element := range e.Elements {
		args[i] = f.lowerExpr(element, array.Elem)
	}
	out := f.newReg("arr")
	f.emit(&metals.MakeArray{Dst: out, Elem: f.l.metalsType(array.Elem), Args: args})
	return out
}

func (f *funcLowerer) evalTupleLit(e *ast.TupleLit, want types.Type) string {
	wantTuple, _ := want.(*types.Tuple)
	if optional, ok := want.(*types.Optional); ok {
		wantTuple, _ = optional.Elem.(*types.Tuple)
	}
	tuple, _ := f.typeOf(e).(*types.Tuple)

	args := make([]string, len(e.Elements))
	for i, element := range e.Elements {
		var elemWant types.Type
		if wantTuple != nil && i < len(wantTuple.Elems) {
			elemWant = wantTuple.Elems[i]
		} else if tuple != nil && i < len(tuple.Elems) {
			elemWant = types.Default(tuple.Elems[i])
		}
		args[i] = f.lowerExpr(element, elemWant)
	}
	out := f.newReg("tup")
	f.emit(&metals.MakeTuple{Dst: out, Args: args})
	return out
}

func (f *funcLowerer) evalStructLit(e *ast.StructLit) string {
	strct, ok := f.typeOf(e).(*types.Struct)
	if !ok {
		f.l.fail("@%s: a struct literal without a struct type", f.fn.Name)
	}
	typeName := f.l.metalsStruct(strct)
	named, ok := typeName.(*metals.Named)
	if !ok {
		f.l.fail("@%s: cannot construct the runtime type %s", f.fn.Name, strct)
	}

	args := make([]string, len(strct.Fields))
	for i, field := range e.Fields {
		var declared *types.Field
		index := i
		if e.Positional {
			if i < len(strct.Fields) {
				declared = &strct.Fields[i]
			}
		} else {
			declared = strct.FieldNamed(field.Name)
			index = fieldIndex(strct, field.Name)
		}
		if declared == nil || index < 0 {
			f.l.fail("@%s: the literal field %s does not exist", f.fn.Name, field.Name)
		}
		args[index] = f.lowerExpr(field.Value, declared.Type)
	}
	out := f.newReg("lit")
	f.emit(&metals.MakeRecord{Dst: out, TypeName: named.Name, Args: args})
	return out
}

func (f *funcLowerer) evalChanLit(e *ast.ChanLit) string {
	chanType, ok := f.typeOf(e).(*types.Chan)
	if !ok {
		f.l.fail("@%s: a channel literal without a channel type", f.fn.Name)
	}
	out := f.newReg("ch")
	made := &metals.ChanNew{Dst: out, Elem: f.l.metalsType(chanType.Elem)}
	if e.Cap != nil {
		capReg := f.lowerExpr(e.Cap, types.USize)
		if !types.Identical(types.Default(f.typeOf(e.Cap)), types.USize) {
			converted := f.newReg("cap")
			f.emit(&metals.Conv{Dst: converted, X: capReg, To: metals.TUsize})
			capReg = converted
		}
		made.Cap = capReg
	}
	f.emit(made)
	return out
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

func (f *funcLowerer) evalUnary(e *ast.UnaryExpr) string {
	switch e.Op {
	case token.Minus:
		value := f.lowerExpr(e.X, nil)
		out := f.newReg("neg")
		f.emit(&metals.Un{Dst: out, Op: "neg", X: value})
		return out
	case token.Bang:
		value := f.lowerExpr(e.X, types.Bool)
		out := f.newReg("not")
		f.emit(&metals.Un{Dst: out, Op: "not", X: value})
		return out
	case token.Tilde:
		value := f.lowerExpr(e.X, nil)
		out := f.newReg("inv")
		f.emit(&metals.Un{Dst: out, Op: "bnot", X: value})
		return out
	case token.Amp:
		target := f.placeOfAddressable(e.X)
		if target == nil {
			f.l.fail("@%s: cannot take the address of this expression", f.fn.Name)
		}
		addr := target.addr()
		if addr == "" {
			f.l.fail("@%s: this value has no address", f.fn.Name)
		}
		return addr
	case token.Star:
		pointer, ok := f.typeOf(e.X).(*types.Pointer)
		if !ok {
			f.l.fail("@%s: dereferencing a non-pointer", f.fn.Name)
		}
		ptr := f.lowerExpr(e.X, nil)
		return f.pointerPlace(ptr, pointer.Elem).load()
	}
	f.l.fail("@%s: cannot lower the unary operator %s", f.fn.Name, e.Op)
	return ""
}

func (f *funcLowerer) evalBinary(e *ast.BinaryExpr) string {
	switch e.Op {
	case token.AndAnd:
		return f.shortCircuit(e, true)
	case token.OrOr:
		return f.shortCircuit(e, false)
	case token.Eq, token.NotEq:
		return f.equality(e)
	case token.Lt, token.LtEq, token.Gt, token.GtEq:
		left := f.lowerExpr(e.X, orderedContext(f.typeOf(e.X), f.typeOf(e.Y)))
		right := f.lowerExpr(e.Y, orderedContext(f.typeOf(e.Y), f.typeOf(e.X)))
		out := f.newReg("cmp")
		f.emit(&metals.Bin{Dst: out, Op: orderOps[e.Op], A: left, B: right})
		return out
	}

	resultType := f.typeOf(e)
	left := f.lowerExpr(e.X, resultType)
	right := f.lowerExpr(e.Y, resultType)
	// A shift count keeps its own integer type in Gyro; METALS wants
	// matching operands, so the count converts.
	if e.Op == token.Shl || e.Op == token.Shr {
		countType := types.Default(f.typeOf(e.Y))
		if !types.Identical(countType, types.Default(resultType)) {
			converted := f.newReg("count")
			f.emit(&metals.Conv{Dst: converted, X: right, To: f.l.metalsType(resultType)})
			right = converted
		}
	}
	out := f.newReg("bin")
	f.emit(&metals.Bin{Dst: out, Op: binOpFor(e.Op, resultType, f), A: left, B: right})
	return out
}

// orderedContext picks the typed side as the context for an untyped one.
func orderedContext(own, other types.Type) types.Type {
	if types.IsUntyped(own) && !types.IsUntyped(other) {
		return types.Default(other)
	}
	return types.Default(own)
}

var orderOps = map[token.Kind]string{
	token.Lt: "lt", token.LtEq: "le", token.Gt: "gt", token.GtEq: "ge",
}

func binOpFor(op token.Kind, t types.Type, f *funcLowerer) string {
	float := types.IsFloat(t)
	switch op {
	case token.Plus:
		if float {
			return "fadd"
		}
		return "iadd"
	case token.Minus:
		if float {
			return "fsub"
		}
		return "isub"
	case token.Star:
		if float {
			return "fmul"
		}
		return "imul"
	case token.Slash:
		if float {
			return "fdiv"
		}
		return "idiv"
	case token.Percent:
		return "irem"
	case token.PlusMod:
		return "wadd"
	case token.MinusMod:
		return "wsub"
	case token.StarMod:
		return "wmul"
	case token.Amp:
		return "band"
	case token.Pipe:
		return "bor"
	case token.Caret:
		return "bxor"
	case token.Shl:
		return "shl"
	case token.Shr:
		return "shr"
	}
	f.l.fail("@%s: cannot lower the operator %s", f.fn.Name, op)
	return ""
}

func (f *funcLowerer) shortCircuit(e *ast.BinaryExpr, isAnd bool) string {
	out := f.newReg("bool")
	left := f.lowerExpr(e.X, types.Bool)
	if isAnd {
		f.emit(&metals.Const{Dst: out, Type: metals.TBool, Value: "false"})
		then := f.nested(func() {
			right := f.lowerExpr(e.Y, types.Bool)
			f.emit(&metals.Copy{Dst: out, X: right})
		})
		f.emit(&metals.If{Cond: left, Then: then})
		return out
	}
	f.emit(&metals.Const{Dst: out, Type: metals.TBool, Value: "true"})
	negated := f.newReg("nor")
	f.emit(&metals.Un{Dst: negated, Op: "not", X: left})
	then := f.nested(func() {
		right := f.lowerExpr(e.Y, types.Bool)
		f.emit(&metals.Copy{Dst: out, X: right})
	})
	f.emit(&metals.If{Cond: negated, Then: then})
	return out
}

// equality lowers == and !=, including null tests and the coercions the
// checker's comparability rules imply.
func (f *funcLowerer) equality(e *ast.BinaryExpr) string {
	if isNullLiteral(e.X) || isNullLiteral(e.Y) {
		operand := e.X
		if isNullLiteral(e.X) {
			operand = e.Y
		}
		return f.nullTest(operand, e.Op)
	}

	leftType, rightType := f.typeOf(e.X), f.typeOf(e.Y)
	left := f.lowerExpr(e.X, orderedContext(leftType, rightType))
	right := f.lowerExpr(e.Y, orderedContext(rightType, leftType))
	left, right = f.balanceForCompare(left, types.Default(leftType), right, types.Default(rightType))

	out := f.newReg("eq")
	op := "eq"
	if e.Op == token.NotEq {
		op = "ne"
	}
	f.emit(&metals.Bin{Dst: out, Op: op, A: left, B: right})
	return out
}

func isNullLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.Null
}

// nullTest compares a value against null without applying narrowing to
// the operand: the raw register is the optional one.
func (f *funcLowerer) nullTest(operand ast.Expr, op token.Kind) string {
	var reg string
	var declared types.Type
	if ident, ok := operand.(*ast.Ident); ok {
		if entry := f.lookup(ident.Name); entry != nil {
			reg = f.localPlace(entry).load()
			declared = entry.typ
		}
	}
	if reg == "" {
		reg = f.eval(operand, nil)
		declared = f.typeOf(operand)
	}
	optional, ok := declared.(*types.Optional)
	if !ok {
		f.l.fail("@%s: a null test on a non-optional", f.fn.Name)
	}
	null := f.nullConst(optional)
	out := f.newReg("isnull")
	metalsOp := "eq"
	if op == token.NotEq {
		metalsOp = "ne"
	}
	f.emit(&metals.Bin{Dst: out, Op: metalsOp, A: reg, B: null})
	return out
}

// balanceForCompare reconciles the operand types == may mix: T? with T,
// and error with a concrete error type.
func (f *funcLowerer) balanceForCompare(left string, leftType types.Type, right string, rightType types.Type) (string, string) {
	if types.Identical(leftType, rightType) {
		return left, right
	}
	if _, ok := leftType.(*types.Optional); ok {
		return left, f.coerce(right, rightType, leftType)
	}
	if _, ok := rightType.(*types.Optional); ok {
		return f.coerce(left, leftType, rightType), right
	}
	if isErrorBasic(leftType) {
		return left, f.coerce(right, rightType, types.Error)
	}
	if isErrorBasic(rightType) {
		return f.coerce(left, leftType, types.Error), right
	}
	return left, right
}

// ---------------------------------------------------------------------------
// Coercions
// ---------------------------------------------------------------------------

// coerce adapts a value register between the assignable-but-distinct
// types the checker allows: T into T?, a concrete error into %error, an
// array borrowed as a slice, and tuples elementwise.
func (f *funcLowerer) coerce(reg string, from, to types.Type) string {
	if to == nil || from == nil || types.Identical(from, to) {
		return reg
	}
	// A null reached here as a tuple element or through re-coercion; the
	// register already holds the right optional's null.
	if isNullBasic(from) {
		return reg
	}
	from = types.Default(from)
	to = derefWant(to)
	if types.Identical(from, to) {
		return reg
	}

	switch to := to.(type) {
	case *types.Optional:
		if fromOpt, ok := from.(*types.Optional); ok {
			return f.rewrapOptional(reg, fromOpt, to)
		}
		inner := f.coerce(reg, from, to.Elem)
		out := f.newReg("some")
		f.emit(&metals.OptWrap{Dst: out, X: inner})
		return out

	case *types.Basic:
		if to.Kind == types.ErrorKind {
			return f.wrapError(reg, from)
		}

	case *types.Slice:
		if _, ok := from.(*types.Array); ok {
			out := f.newReg("view")
			f.emit(&metals.Slice{Dst: out, X: reg})
			return out
		}

	case *types.Tuple:
		fromTuple, ok := from.(*types.Tuple)
		if !ok || len(fromTuple.Elems) != len(to.Elems) {
			return reg
		}
		same := true
		for i := range to.Elems {
			if !types.Identical(types.Default(fromTuple.Elems[i]), to.Elems[i]) {
				same = false
			}
		}
		if same {
			return reg
		}
		parts := make([]string, len(to.Elems))
		for i := range to.Elems {
			part := f.newReg("part")
			f.emit(&metals.TupleGet{Dst: part, X: reg, Index: i})
			parts[i] = f.coerce(part, types.Default(fromTuple.Elems[i]), to.Elems[i])
		}
		out := f.newReg("tup")
		f.emit(&metals.MakeTuple{Dst: out, Args: parts})
		return out
	}
	return reg
}

// derefWant strips nothing today but keeps the call sites honest about
// what they pass: the target is used as-is.
func derefWant(t types.Type) types.Type { return t }

// wrapError boxes a concrete error struct into the %error enum.
func (f *funcLowerer) wrapError(reg string, from types.Type) string {
	if isErrorBasic(from) {
		return reg
	}
	strct, ok := from.(*types.Struct)
	if !ok {
		f.l.fail("@%s: %s cannot become an error", f.fn.Name, from)
	}
	f.l.usesError = true
	f.l.metalsStruct(strct)
	out := f.newReg("err")
	f.emit(&metals.MakeVariant{
		Dst:      out,
		TypeName: "error",
		Variant:  f.l.errorVariantOf(strct),
		Args:     []string{reg},
	})
	return out
}

// rewrapOptional converts between optionals of different element types
// at runtime: null stays null, a present value converts.
func (f *funcLowerer) rewrapOptional(reg string, from, to *types.Optional) string {
	if types.Identical(from.Elem, to.Elem) {
		return reg
	}
	out := f.newReg("opt")
	null := f.newReg("null")
	f.emit(&metals.Const{Dst: null, Type: f.l.metalsType(from), Null: true})
	missing := f.newReg("isnull")
	f.emit(&metals.Bin{Dst: missing, Op: "eq", A: reg, B: null})
	then := f.nested(func() {
		empty := f.newReg("none")
		f.emit(&metals.Const{Dst: empty, Type: f.l.metalsType(to), Null: true})
		f.emit(&metals.Copy{Dst: out, X: empty})
	})
	elseBody := f.nested(func() {
		inner := f.newReg("v")
		f.emit(&metals.OptUnwrap{Dst: inner, X: reg})
		converted := f.coerce(inner, from.Elem, to.Elem)
		wrapped := f.newReg("some")
		f.emit(&metals.OptWrap{Dst: wrapped, X: converted})
		f.emit(&metals.Copy{Dst: out, X: wrapped})
	})
	f.emit(&metals.If{Cond: missing, Then: then, Else: elseBody})
	return out
}

// ---------------------------------------------------------------------------
// String interpolation and display
// ---------------------------------------------------------------------------

func (f *funcLowerer) evalStringLit(e *ast.StringLit) string {
	var acc string
	appendPart := func(part string) {
		if acc == "" {
			acc = part
			return
		}
		joined := f.newReg("s")
		f.emit(&metals.StrConcat{Dst: joined, A: acc, B: part})
		acc = joined
	}
	for _, part := range e.Parts {
		if part.Expr == nil {
			if part.Text == "" {
				continue
			}
			text := f.newReg("s")
			f.emit(&metals.Const{Dst: text, Type: metals.TStr, Value: strconv.Quote(part.Text)})
			appendPart(text)
			continue
		}
		appendPart(f.display(part.Expr))
	}
	if acc == "" {
		empty := f.newReg("s")
		f.emit(&metals.Const{Dst: empty, Type: metals.TStr, Value: `""`})
		return empty
	}
	return acc
}

// display renders a value the way #{} and println show it: strings pass
// through, a to_string method wins, everything else goes to the runtime.
func (f *funcLowerer) display(e ast.Expr) string {
	t := f.typeOf(e)
	if isStringType(t) {
		return f.lowerExpr(e, nil)
	}
	if toString := f.toStringMethod(t); toString != nil {
		return f.callToString(e, t, toString)
	}
	value := f.lowerExpr(e, nil)
	return f.displayReg(value, t)
}

// displayReg converts an already-evaluated value to its display string. A
// composite has a synthesized repr; the runtime answers for the leaves.
func (f *funcLowerer) displayReg(value string, t types.Type) string {
	name := f.l.reprFor(t)
	if name == "" {
		name = "str<" + t.String() + ">"
		f.l.ensureImport(name, &metals.FnType{
			Params: []metals.Type{f.l.metalsType(t)},
			Result: metals.TStr,
		})
	}
	out := f.newReg("shown")
	f.emit(&metals.Call{Dst: out, Callee: name, Args: []string{value}})
	return out
}

func (f *funcLowerer) toStringMethod(t types.Type) *types.Method {
	return toStringMethodOf(t)
}

// toStringMethodOf finds a user-defined to_string(): string on a named
// type.
func toStringMethodOf(t types.Type) *types.Method {
	var method *types.Method
	switch t := t.(type) {
	case *types.Struct:
		if isOpaqueHandle(t) {
			return nil
		}
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		method = origin.Methods["to_string"]
	case *types.Enum:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		method = origin.Methods["to_string"]
	}
	if method == nil || method.Decl == nil || method.Recv == types.NoRecv {
		return nil
	}
	if len(method.Sig.Params) != 0 || len(method.TypeParams) > 0 {
		return nil
	}
	if !types.Identical(method.Sig.Result, types.String) {
		return nil
	}
	return method
}

func (f *funcLowerer) callToString(e ast.Expr, t types.Type, method *types.Method) string {
	bindings := ownerBindings(method.Decl, t)
	name := f.l.ensureFunc(method.Decl, bindings)
	receiver := f.methodReceiver(e, method)
	out := f.newReg("shown")
	f.emit(&metals.Call{Dst: out, Callee: name, Args: []string{receiver}})
	return out
}

// ownerBindings rebuilds the by-name bindings of a method's owner from a
// concrete instance, for methods reached without a recorded call (display,
// iteration).
func ownerBindings(decl *ast.FuncDecl, t types.Type) map[string]types.Type {
	var args []types.Type
	switch t := t.(type) {
	case *types.Struct:
		args = t.Args
	case *types.Enum:
		args = t.Args
	}
	if len(decl.OwnerParams) == 0 || len(args) == 0 {
		return nil
	}
	bindings := map[string]types.Type{}
	for i, param := range decl.OwnerParams {
		if i < len(args) {
			bindings[param.Name] = args[i]
		}
	}
	return bindings
}
