package interp

import (
	"reflect"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// frame is the execution context of one function activation: the module
// whose file positions and scope apply.
type frame struct {
	in     *interp
	module *moduleEnv
}

type flowKind int

const (
	flowNormal flowKind = iota
	flowReturn
	flowBreak
	flowContinue
)

type flow struct {
	kind  flowKind
	value Value
}

// flowEscape carries control flow out of an expression position: a match
// arm that returns, breaks or continues. Blocks on the way out run their
// defers and re-panic; the enclosing loop or call converts it back.
type flowEscape struct {
	f flow
}

// VoidVal is what a call with no result produces.
type VoidVal struct{}

// ---------------------------------------------------------------------------
// Calls
// ---------------------------------------------------------------------------

// callFunction invokes a declared function. self is nil for free and
// associated functions, the receiver value or pointer otherwise.
func (in *interp) callFunction(def *funcDef, self Value, args []Value) Value {
	if def.native != nil {
		panic("native function called without call context")
	}

	f := &frame{in: in, module: def.module}
	env := newEnv(nil)

	if self != nil {
		env.bind("self", self)
	}
	f.bindParams(def.decl.Params, args, env)

	if def.decl.Async {
		return f.spawnAsync(def, env)
	}
	return f.invokeBody(def, env)
}

func (f *frame) spawnAsync(def *funcDef, env *env) Value {
	future := &FutureVal{ch: make(chan Value, 1)}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				f.in.abortFromGoroutine(r)
			}
		}()
		future.ch <- f.invokeBody(def, env)
	}()
	return future
}

func (f *frame) invokeBody(def *funcDef, env *env) (result Value) {
	result = VoidVal{}
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		escape, ok := r.(flowEscape)
		if !ok || escape.f.kind != flowReturn {
			panic(r)
		}
		result = f.coerceResult(def.decl.Results, escape.f.value)
	}()

	out := f.execBlock(def.decl.Body, env)
	if out.kind == flowReturn {
		result = f.coerceResult(def.decl.Results, out.value)
	}
	return result
}

func (f *frame) bindParams(params []ast.Param, args []Value, env *env) {
	for i, param := range params {
		var value Value
		if i < len(args) {
			value = f.coerceToAst(args[i], param.Type)
		} else if param.Default != nil {
			value = f.coerceToAst(f.eval(param.Default, env), param.Type)
		} else {
			value = UninitVal{}
		}
		env.bind(param.Name, copyValue(value))
	}
}

// coerceResult adapts a return value to the declared results: untyped
// literals take their declared widths, tuple elements elementwise.
func (f *frame) coerceResult(results []ast.Type, value Value) Value {
	switch len(results) {
	case 0:
		return VoidVal{}
	case 1:
		return f.coerceToAst(value, results[0])
	}
	tuple, ok := value.(TupleVal)
	if !ok || len(tuple) != len(results) {
		return value
	}
	out := make(TupleVal, len(tuple))
	for i, elem := range tuple {
		out[i] = f.coerceToAst(elem, results[i])
	}
	return out
}

// callClosure invokes a closure value.
func (in *interp) callClosure(cl *ClosureVal, args []Value) Value {
	if cl.Def != nil {
		return in.callFunction(cl.Def, nil, args)
	}

	f := &frame{in: in, module: cl.Module}
	env := newEnv(cl.Env)
	f.bindParams(cl.Decl.Params, args, env)

	run := func() Value {
		if cl.Decl.BodyExpr != nil {
			return f.eval(cl.Decl.BodyExpr, env)
		}
		return f.invokeClosureBody(cl.Decl.Body, env)
	}

	if cl.Async {
		future := &FutureVal{ch: make(chan Value, 1)}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					in.abortFromGoroutine(r)
				}
			}()
			future.ch <- run()
		}()
		return future
	}
	return run()
}

func (f *frame) invokeClosureBody(body *ast.Block, env *env) (result Value) {
	result = VoidVal{}
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		escape, ok := r.(flowEscape)
		if !ok || escape.f.kind != flowReturn {
			panic(r)
		}
		result = escape.f.value
	}()

	out := f.execBlock(body, env)
	if out.kind == flowReturn {
		result = out.value
	}
	return result
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

// execBlock runs a block in a fresh scope. Deferred calls run at block
// exit in reverse order — on normal exit and on control flow leaving the
// block, but not on a panic, which aborts without unwinding.
func (f *frame) execBlock(block *ast.Block, parent *env) flow {
	env := newEnv(parent)
	var deferred []func()

	runDeferred := func() {
		for i := len(deferred) - 1; i >= 0; i-- {
			deferred[i]()
		}
	}

	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if _, ok := r.(flowEscape); ok {
			runDeferred()
		}
		panic(r)
	}()

	for _, stmt := range block.Stmts {
		if deferStmt, ok := stmt.(*ast.DeferStmt); ok {
			call := deferStmt.Call.(*ast.CallExpr)
			thunk := f.prepareCall(call, env)
			deferred = append(deferred, func() { thunk() })
			continue
		}

		out := f.exec(stmt, env)
		if out.kind != flowNormal {
			runDeferred()
			return out
		}
	}
	runDeferred()
	return flow{}
}

func (f *frame) exec(stmt ast.Stmt, env *env) flow {
	switch s := stmt.(type) {
	case *ast.VarDecl:
		f.execVar(s, env)

	case *ast.Block:
		return f.execBlock(s, env)

	case *ast.ExprStmt:
		f.eval(s.X, env)

	case *ast.AssignStmt:
		f.execAssign(s, env)

	case *ast.ReturnStmt:
		return flow{kind: flowReturn, value: f.returnValue(s, env)}

	case *ast.BranchStmt:
		if s.Kind == token.Break {
			return flow{kind: flowBreak}
		}
		return flow{kind: flowContinue}

	case *ast.IfStmt:
		if truthy(f.eval(s.Cond, env)) {
			return f.execBlock(s.Then, env)
		}
		if s.Else != nil {
			return f.exec(s.Else, env)
		}

	case *ast.WhileStmt:
		for truthy(f.eval(s.Cond, env)) {
			out := f.loopBody(s.Body, env)
			if out.kind == flowBreak {
				break
			}
			if out.kind == flowReturn {
				return out
			}
		}

	case *ast.ForStmt:
		return f.execFor(s, env)

	case *ast.ForInStmt:
		return f.execForIn(s, env)

	case *ast.SendStmt:
		f.execSend(s, env)

	case *ast.SelectStmt:
		return f.execSelect(s, env)

	case *ast.DeferStmt:
		// Handled by execBlock; a defer outside a block cannot happen.
	}
	return flow{}
}

// loopBody runs one iteration, converting a break or continue that
// escaped from an expression position back into flow.
func (f *frame) loopBody(body *ast.Block, env *env) (out flow) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		escape, ok := r.(flowEscape)
		if !ok || escape.f.kind == flowReturn {
			panic(r)
		}
		out = escape.f
	}()
	return f.execBlock(body, env)
}

func (f *frame) execFor(s *ast.ForStmt, env *env) flow {
	scope := newEnv(env)
	if s.Init != nil {
		f.exec(s.Init, scope)
	}
	for s.Cond == nil || truthy(f.eval(s.Cond, scope)) {
		out := f.loopBody(s.Body, scope)
		if out.kind == flowBreak {
			break
		}
		if out.kind == flowReturn {
			return out
		}
		if s.Post != nil {
			f.exec(s.Post, scope)
		}
	}
	return flow{}
}

func (f *frame) execForIn(s *ast.ForInStmt, env *env) flow {
	iterate := f.iterator(s.Iterable, env)
	for {
		value, ok := iterate()
		if !ok {
			return flow{}
		}
		scope := newEnv(env)
		scope.bind(s.Name, value)
		out := f.loopBody(s.Body, scope)
		if out.kind == flowBreak {
			return flow{}
		}
		if out.kind == flowReturn {
			return out
		}
	}
}

// iterator turns the iterable into a pull function.
func (f *frame) iterator(e ast.Expr, env *env) func() (Value, bool) {
	if rng, ok := e.(*ast.RangeExpr); ok {
		return f.rangeIterator(rng, env)
	}

	switch v := f.eval(e, env).(type) {
	case SliceVal:
		return sliceIterator(v.Elems)
	case ArrayVal:
		return sliceIterator(v.Elems)
	case ChanVal:
		return func() (Value, bool) {
			value, open := <-v
			if !open {
				return nil, false
			}
			return value, true
		}
	default:
		return f.duckIterator(v, e.Pos())
	}
}

func sliceIterator(elems []Value) func() (Value, bool) {
	index := 0
	return func() (Value, bool) {
		if index >= len(elems) {
			return nil, false
		}
		value := elems[index]
		index++
		return copyValue(value), true
	}
}

func (f *frame) rangeIterator(rng *ast.RangeExpr, env *env) func() (Value, bool) {
	lo := f.eval(rng.Lo, env)
	hi := f.eval(rng.Hi, env)
	lo, hi = adoptKinds(lo, hi)

	switch lo := lo.(type) {
	case IntVal:
		current, limit := lo.V, hi.(IntVal).V
		kind := defaultIntKind(lo.Kind)
		return func() (Value, bool) {
			if current >= limit {
				return nil, false
			}
			value := IntVal{V: current, Kind: kind}
			current++
			return value, true
		}
	case UintVal:
		current, limit := lo.V, hi.(UintVal).V
		return func() (Value, bool) {
			if current >= limit {
				return nil, false
			}
			value := UintVal{V: current, Kind: lo.Kind}
			current++
			return value, true
		}
	}
	f.in.panicf(f.module, rng.Offset, "a range needs integer endpoints")
	return nil
}

// duckIterator drives the next(*self): (T, bool) contract, going through
// iter() when the value itself has no next.
func (f *frame) duckIterator(v Value, offset int) func() (Value, bool) {
	def := f.userMethodOf(v, "next")
	if def == nil {
		if iterDef := f.userMethodOf(v, "iter"); iterDef != nil {
			v = f.in.callFunction(iterDef, f.receiverFor(iterDef, cellOf(v)), nil)
			def = f.userMethodOf(v, "next")
		}
	}
	if def == nil {
		f.in.panicf(f.module, offset, "value is not iterable")
	}

	cell := cellOf(v)
	return func() (Value, bool) {
		result := f.in.callFunction(def, f.receiverFor(def, cell), nil)
		tuple, ok := result.(TupleVal)
		if !ok || len(tuple) != 2 {
			return nil, false
		}
		if more, ok := tuple[1].(BoolVal); !ok || !bool(more) {
			return nil, false
		}
		return tuple[0], true
	}
}

func (f *frame) userMethodOf(v Value, name string) *funcDef {
	switch v := v.(type) {
	case StructVal:
		return v.Def.module.methodOf(v.Def.name, name)
	case EnumVal:
		return v.Def.module.methodOf(v.Def.name, name)
	}
	if def := f.in.runtimeMethodOf(v, name); def != nil {
		return def
	}
	return f.extensionMethodOf(v, name)
}

// runtimeMethodOf finds a method written in Gyro on a type whose values
// the runtime owns. Array<T> and friends are declared like any other
// struct — the runtime only supplies the bodies that have none — so a
// method beside that declaration is found where a struct's own methods
// are: in the module that declares the type.
func (in *interp) runtimeMethodOf(v Value, name string) *funcDef {
	owner := runtimeTypeName(v)
	if owner == "" {
		return nil
	}
	def := in.runtimeTypes[owner]
	if def == nil {
		return nil
	}
	return def.module.methodOf(owner, name)
}

// runtimeTypeName spells the declared type a runtime value inhabits.
func runtimeTypeName(v Value) string {
	switch v.(type) {
	case *ArrayObj:
		return "Array"
	case *MutexObj:
		return "Mutex"
	case *GuardObj:
		return "MutexGuard"
	case *FileObj:
		return "File"
	}
	return ""
}

// extensionMethodOf finds a method declared on a built-in type. Sema
// resolved it through the name in scope, so this looks it up the same
// way: the calling module's scope, not the receiver's, which is what
// makes an extension travel by import.
func (f *frame) extensionMethodOf(v Value, name string) *funcDef {
	owner := builtinTypeName(v)
	if owner == "" {
		return nil
	}
	binding, ok := f.module.scope[name]
	if !ok || binding.fn == nil || binding.fn.decl == nil {
		return nil
	}
	if binding.fn.decl.Owner != owner || binding.fn.decl.Recv == nil {
		return nil
	}
	return binding.fn
}

// builtinTypeName spells the built-in type a value inhabits, or "" when
// it is not one an extension can name.
func builtinTypeName(v Value) string {
	switch v := v.(type) {
	case StringVal:
		return "string"
	case BoolVal:
		return "bool"
	case IntVal:
		return basicKindName(v.Kind)
	case UintVal:
		return basicKindName(v.Kind)
	case FloatVal:
		return basicKindName(v.Kind)
	}
	return ""
}

// basicKindName spells a value's type. An untyped literal takes its
// default, the way it would anywhere else: 4.double() is i32's.
func basicKindName(kind types.BasicKind) string {
	switch kind {
	case types.UntypedInt:
		return "i32"
	case types.UntypedFloat:
		return "f64"
	}
	for name, basic := range types.Builtin {
		if basic.Kind == kind {
			return name
		}
	}
	return ""
}

// receiverFor shapes the receiver the way the method wants it: a pointer
// for *self, a copy for self.
func (f *frame) receiverFor(def *funcDef, cell *Value) Value {
	if def.decl.Recv != nil && def.decl.Recv.Pointer {
		return PointerVal{Cell: cell}
	}
	return copyValue(*cell)
}

// ---------------------------------------------------------------------------
// Declarations and assignment
// ---------------------------------------------------------------------------

func (f *frame) execVar(decl *ast.VarDecl, env *env) {
	if decl.Value == nil {
		for _, name := range decl.Names {
			env.bind(name.Name, UninitVal{})
		}
		return
	}

	// The two-name receive binds (element, open); everywhere else the
	// receive expression is just the element.
	if recv, ok := decl.Value.(*ast.RecvExpr); ok && len(decl.Names) == 2 {
		value, open := f.recvPair(recv, env)
		env.bind(decl.Names[0].Name, copyValue(value))
		env.bind(decl.Names[1].Name, BoolVal(open))
		return
	}

	value := f.eval(decl.Value, env)
	f.bindDeclared(decl, value, func(name string, v Value) {
		env.bind(name, v)
	})
}

// bindDeclared distributes an initializer over the declared names: the
// two-name receive form, tuple destructuring, or a single binding, with
// the annotation applied.
func (f *frame) bindDeclared(decl *ast.VarDecl, value Value, bind func(string, Value)) {
	if len(decl.Names) == 2 {
		// A select arm hands the (element, open) pair in pre-built.
		if _, isRecv := decl.Value.(*ast.RecvExpr); isRecv {
			pair := value.(TupleVal)
			bind(decl.Names[0].Name, copyValue(pair[0]))
			bind(decl.Names[1].Name, pair[1])
			return
		}
	}

	if len(decl.Names) > 1 {
		tuple, ok := value.(TupleVal)
		if !ok {
			f.in.panicf(f.module, decl.Offset, "destructuring needs a tuple")
		}
		for i, name := range decl.Names {
			if i < len(tuple) {
				bind(name.Name, copyValue(defaultUntyped(tuple[i])))
			}
		}
		return
	}

	if decl.Type != nil {
		value = f.coerceToAst(value, decl.Type)
	} else {
		value = defaultUntyped(value)
	}
	for _, name := range decl.Names {
		bind(name.Name, copyValue(value))
	}
}

func (f *frame) execAssign(s *ast.AssignStmt, env *env) {
	if s.Op != token.Assign {
		f.execCompound(s, env)
		return
	}

	if len(s.Lhs) > 1 {
		f.execDestructuringAssign(s, env)
		return
	}

	value := f.eval(s.Rhs, env)
	f.assignInto(s.Lhs[0], value, env)
}

func (f *frame) assignInto(lhs ast.Expr, value Value, env *env) {
	if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "_" {
		return
	}
	cell := f.evalCell(lhs, env)
	*cell = copyValue(f.adaptToCell(*cell, value))
}

// adaptToCell gives an untyped literal the width of the value it replaces,
// so `hash = hash + 1` style writes stay in kind.
func (f *frame) adaptToCell(previous, value Value) Value {
	switch previous.(type) {
	case IntVal, UintVal, FloatVal:
		return adoptKindOf(previous, value)
	}
	return defaultUntyped(value)
}

func (f *frame) execDestructuringAssign(s *ast.AssignStmt, env *env) {
	if recv, isRecv := s.Rhs.(*ast.RecvExpr); isRecv && len(s.Lhs) == 2 {
		value, open := f.recvPair(recv, env)
		f.assignInto(s.Lhs[0], value, env)
		f.assignInto(s.Lhs[1], BoolVal(open), env)
		return
	}

	value := f.eval(s.Rhs, env)
	tuple, ok := value.(TupleVal)
	if !ok {
		f.in.panicf(f.module, s.Offset, "destructuring needs a tuple")
	}
	for i, lhs := range s.Lhs {
		if i < len(tuple) {
			f.assignInto(lhs, tuple[i], env)
		}
	}
}

func (f *frame) execCompound(s *ast.AssignStmt, env *env) {
	cell := f.evalCell(s.Lhs[0], env)
	right := f.eval(s.Rhs, env)
	op := compoundToBinary[s.Op]
	*cell = f.binaryValues(op, *cell, right, s.Offset)
}

var compoundToBinary = map[token.Kind]token.Kind{
	token.PlusEq:    token.Plus,
	token.MinusEq:   token.Minus,
	token.StarEq:    token.Star,
	token.SlashEq:   token.Slash,
	token.PercentEq: token.Percent,
	token.AmpEq:     token.Amp,
	token.PipeEq:    token.Pipe,
	token.CaretEq:   token.Caret,
	token.ShlEq:     token.Shl,
	token.ShrEq:     token.Shr,
}

func (f *frame) returnValue(s *ast.ReturnStmt, env *env) Value {
	switch len(s.Results) {
	case 0:
		return VoidVal{}
	case 1:
		return f.eval(s.Results[0], env)
	}
	tuple := make(TupleVal, len(s.Results))
	for i, result := range s.Results {
		tuple[i] = f.eval(result, env)
	}
	return tuple
}

// ---------------------------------------------------------------------------
// Channels and select
// ---------------------------------------------------------------------------

func (f *frame) execSend(s *ast.SendStmt, env *env) {
	channel := f.channelOperand(s.Chan, s.Offset, env)
	value := copyValue(defaultUntyped(f.eval(s.Value, env)))
	f.sendOn(channel, value, s.Offset)
}

func (f *frame) sendOn(channel ChanVal, value Value, offset int) {
	defer func() {
		if recover() != nil {
			f.in.panicf(f.module, offset, "send on a closed channel")
		}
	}()
	channel <- value
}

func (f *frame) channelOperand(e ast.Expr, offset int, env *env) ChanVal {
	channel, ok := f.eval(e, env).(ChanVal)
	if !ok {
		f.in.panicf(f.module, offset, "not a channel")
	}
	return channel
}

// execSelect maps the arms onto reflect.Select.
func (f *frame) execSelect(s *ast.SelectStmt, env *env) flow {
	cases := make([]reflect.SelectCase, 0, len(s.Arms)+1)

	for _, arm := range s.Arms {
		switch comm := arm.Comm.(type) {
		case *ast.SendStmt:
			channel := f.channelOperand(comm.Chan, comm.Offset, env)
			value := copyValue(defaultUntyped(f.eval(comm.Value, env)))
			cases = append(cases, reflect.SelectCase{
				Dir:  reflect.SelectSend,
				Chan: reflect.ValueOf((chan Value)(channel)),
				Send: reflect.ValueOf(value),
			})
		default:
			recv := recvOfComm(comm)
			channel := f.channelOperand(recv.Chan, recv.Offset, env)
			cases = append(cases, reflect.SelectCase{
				Dir:  reflect.SelectRecv,
				Chan: reflect.ValueOf((chan Value)(channel)),
			})
		}
	}
	if s.Else != nil {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectDefault})
	}

	chosen, received, open := reflect.Select(cases)
	if s.Else != nil && chosen == len(s.Arms) {
		return f.exec(s.Else, env)
	}

	arm := s.Arms[chosen]
	scope := newEnv(env)
	f.bindSelectComm(arm.Comm, received, open, scope)
	return f.exec(arm.Body, scope)
}

// recvOfComm digs the receive out of the comm statement shapes the parser
// allows: a var binding, an assignment, or a bare receive.
func recvOfComm(comm ast.Stmt) *ast.RecvExpr {
	switch comm := comm.(type) {
	case *ast.VarDecl:
		return comm.Value.(*ast.RecvExpr)
	case *ast.AssignStmt:
		return comm.Rhs.(*ast.RecvExpr)
	case *ast.ExprStmt:
		return comm.X.(*ast.RecvExpr)
	}
	panic("unreachable: the parser only builds these comm forms")
}

func (f *frame) bindSelectComm(comm ast.Stmt, received reflect.Value, open bool, env *env) {
	switch comm := comm.(type) {
	case *ast.SendStmt:
		// The send already happened inside reflect.Select. reflect
		// requires a pointer-shaped send value; nothing to bind.
	case *ast.VarDecl:
		value := receivedValue(received, open)
		f.bindDeclared(comm, recvShape(comm.Names, value, open), func(name string, v Value) {
			env.bind(name, v)
		})
	case *ast.AssignStmt:
		value := receivedValue(received, open)
		if len(comm.Lhs) == 2 {
			f.assignInto(comm.Lhs[0], value, env)
			f.assignInto(comm.Lhs[1], BoolVal(open), env)
			return
		}
		f.assignInto(comm.Lhs[0], value, env)
	case *ast.ExprStmt:
		// A bare receive discards the value.
	}
}

func receivedValue(received reflect.Value, open bool) Value {
	if !open {
		return UninitVal{}
	}
	return received.Interface()
}

// recvShape rebuilds the (value, open) pair for a two-name binding.
func recvShape(names []ast.Binding, value Value, open bool) Value {
	if len(names) == 2 {
		return TupleVal{value, BoolVal(open)}
	}
	return value
}

func truthy(v Value) bool {
	b, ok := v.(BoolVal)
	return ok && bool(b)
}
