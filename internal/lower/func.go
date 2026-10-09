package lower

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// funcLowerer lowers one function body. Gyro locals become METALS
// registers; locals that need an address — user-struct values, and
// anything &-taken — live in stack slots instead, with the register
// holding the slot pointer.
type funcLowerer struct {
	l    *lowerer
	decl *ast.FuncDecl
	inst *sema.FuncInstance
	fn   *metals.Func

	blocks   []*[]metals.Instr
	scopes   []*scopeInfo
	loops    []loopContext
	regCount map[string]int
	labelN   int

	result    types.Type
	addrTaken map[string]bool

	// captures is set when this is a lifted closure body: the names the
	// closure copied into its environment record, in field order. The
	// env pointer arrives as the first parameter, named env.
	captures []capture

	// deferring counts the deferred calls being prepared. A deferred
	// call's arguments are evaluated where it is written, so while this
	// is non-zero a local's register is snapshotted rather than named.
	deferring int
}

type scopeInfo struct {
	locals map[string]*local
	defers []func()
}

type local struct {
	reg  string
	typ  types.Type
	slot bool
	// env is the field index in the closure environment record, or -1.
	env int
}

type capture struct {
	name string
	typ  types.Type
}

type loopContext struct {
	breakLabel string
	contLabel  string
	depth      int
}

// ---------------------------------------------------------------------------
// Function assembly
// ---------------------------------------------------------------------------

func (l *lowerer) lowerFunc(item workItem) {
	decl := item.decl
	var inst *sema.FuncInstance
	if item.bindings != nil {
		inst = l.info.Instances[decl][sema.InstanceKey(decl, item.bindings)]
		if inst == nil {
			l.fail("no recorded instantiation for @%s", item.name)
		}
	}

	f := &funcLowerer{
		l:        l,
		decl:     decl,
		inst:     inst,
		fn:       &metals.Func{Name: item.name, Export: item.name == "main", Async: decl.Async},
		regCount: map[string]int{},
	}
	f.result = f.localType(decl, "return")
	if !isVoid(f.result) {
		f.fn.Result = l.metalsType(f.result)
	}
	f.addrTaken = map[string]bool{}
	markAddressTaken(decl.Body, f.addrTaken)

	f.blocks = []*[]metals.Instr{&f.fn.Body}
	f.pushScope()

	if decl.Recv != nil {
		f.declareReceiver()
	}
	for _, param := range decl.Params {
		f.declareParam(param.Name, f.localType(decl, param.Name))
	}
	// A function whose row names alloc receives the allocator last. main
	// is called by the runtime, which has none to pass, so it starts on
	// the heap instead.
	if l.allocates(decl) {
		if f.fn.Export {
			f.bindAllocator(f.heapAllocator())
		} else {
			f.declareParam(allocatorLocal, l.allocatorStruct())
		}
	}

	f.blockInPlace(decl.Body)
	f.epilogue(blockTerminates(decl.Body))
	f.popScopeNoDefers()

	l.module.Funcs = append(l.module.Funcs, f.fn)
}

// allocatorLocal is the name the current allocator is bound under. It is
// the keyword that chooses one, so no local of the program can collide
// with it — the same trick sema's Locals play with "self" and "return".
const allocatorLocal = "with"

// currentAllocator is the allocator every allocating call from here is
// handed. A function that receives one has it bound as a parameter and a
// `with` rebinds it; where nothing is bound — a closure written inside a
// function that does not allocate, or a module initializer — the heap is
// what there is, and it is bound so later uses share it.
func (f *funcLowerer) currentAllocator() string {
	if entry := f.lookup(allocatorLocal); entry != nil {
		return f.localPlace(entry).load()
	}
	f.bindAllocator(f.heapAllocator())
	return f.localPlace(f.lookup(allocatorLocal)).load()
}

// heapAllocator calls mem's heap(), which builds the value and allocates
// nothing.
func (f *funcLowerer) heapAllocator() string {
	name := f.l.ensureFunc(f.l.memDecl("heap"), nil)
	out := f.newReg("heap")
	f.emit(&metals.Call{Dst: out, Callee: name, Args: nil})
	return out
}

// bindAllocator makes value the current allocator for the rest of the
// current scope. An Allocator is a record, so it lives in a slot the way
// every other struct local does.
func (f *funcLowerer) bindAllocator(value string) {
	typ := f.l.allocatorStruct()
	slot := f.newReg(allocatorLocal)
	f.emit(&metals.Slot{Dst: slot, Type: f.l.metalsType(typ)})
	f.emit(&metals.Store{Ptr: slot, Val: value})
	f.bind(allocatorLocal, &local{reg: slot, typ: typ, slot: true, env: -1})
}

// withStmt is `with expr`: the allocator for the rest of the block.
func (f *funcLowerer) withStmt(s *ast.WithStmt) {
	f.bindAllocator(f.lowerExpr(s.Allocator, f.l.allocatorStruct()))
}

func (f *funcLowerer) declareReceiver() {
	selfType := f.localType(f.decl, "self")
	if selfType == nil {
		f.l.fail("@%s has a receiver but no recorded self type", f.fn.Name)
	}
	if f.decl.Recv.Pointer {
		f.fn.Params = append(f.fn.Params, metals.Param{
			Name: "self",
			Type: &metals.Ptr{Elem: f.l.metalsType(selfType)},
		})
		f.bind("self", &local{reg: "self", typ: selfType, slot: true, env: -1})
		f.noteReg("self")
		return
	}
	f.declareParam("self", selfType)
}

// declareParam takes one incoming value parameter, spilling it to a slot
// when the local model requires one.
func (f *funcLowerer) declareParam(name string, typ types.Type) {
	if typ == nil {
		f.l.fail("@%s: parameter %s has no recorded type", f.fn.Name, name)
	}
	if !f.needsSlot(name, typ) {
		f.fn.Params = append(f.fn.Params, metals.Param{Name: name, Type: f.l.metalsType(typ)})
		f.bind(name, &local{reg: name, typ: typ, env: -1})
		f.noteReg(name)
		return
	}
	incoming := name + ".arg"
	f.fn.Params = append(f.fn.Params, metals.Param{Name: incoming, Type: f.l.metalsType(typ)})
	f.noteReg(incoming)
	slot := f.newReg(name)
	f.emit(&metals.Slot{Dst: slot, Type: f.l.metalsType(typ)})
	f.emit(&metals.Store{Ptr: slot, Val: incoming})
	f.bind(name, &local{reg: slot, typ: typ, slot: true, env: -1})
}

// epilogue closes the function when its body can fall off the end.
func (f *funcLowerer) epilogue(terminated bool) {
	if terminated {
		return
	}
	f.flushDefersDownTo(0)
	if isVoid(f.result) {
		f.emit(&metals.Return{})
		return
	}
	f.emit(&metals.Abort{Message: "fn " + f.fn.Name + " fell off its end without a return"})
}

// blockTerminates mirrors sema's fall-through analysis.
func blockTerminates(b *ast.Block) bool {
	if b == nil || len(b.Stmts) == 0 {
		return false
	}
	return stmtTerminates(b.Stmts[len(b.Stmts)-1])
}

func stmtTerminates(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt, *ast.BranchStmt:
		return true
	case *ast.Block:
		return blockTerminates(s)
	case *ast.IfStmt:
		if s.Else == nil {
			return false
		}
		return blockTerminates(s.Then) && stmtTerminates(s.Else)
	}
	return false
}

// markAddressTaken collects every name whose address the body takes.
func markAddressTaken(node ast.Node, out map[string]bool) {
	walk(node, func(n ast.Node) {
		unary, ok := n.(*ast.UnaryExpr)
		if !ok || unary.Op != token.Amp {
			return
		}
		if name := placeRoot(unary.X); name != "" {
			out[name] = true
		}
	})
}

// placeRoot names the local a place expression is rooted at, so that
// `&xs[i]` and `&s.field.inner` spill `xs` and `s` the way `&xs` does.
// A chain that reaches a pointer stops there: the address is the
// pointer's own and no local has to move.
func placeRoot(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.TupleIndexExpr:
			e = x.X
		default:
			return ""
		}
	}
}

// ---------------------------------------------------------------------------
// Recorded-fact lookups
// ---------------------------------------------------------------------------

// typeOf returns the recorded type of an expression, defaulted.
func (f *funcLowerer) typeOf(e ast.Expr) types.Type {
	if f.inst != nil {
		if t, ok := f.inst.Types[e]; ok {
			return types.Default(t)
		}
	}
	if t, ok := f.l.info.Types[e]; ok {
		return types.Default(t)
	}
	f.l.fail("@%s: no recorded type at offset %d", f.fn.Name, e.Pos())
	return nil
}

func (f *funcLowerer) callInfo(call *ast.CallExpr) *sema.CallInfo {
	if f.inst != nil {
		if ci, ok := f.inst.Calls[call]; ok {
			return ci
		}
	}
	if ci, ok := f.l.info.Calls[call]; ok {
		return ci
	}
	f.l.fail("@%s: no recorded resolution for a call at offset %d", f.fn.Name, call.Pos())
	return nil
}

func (f *funcLowerer) localType(decl ast.Node, name string) types.Type {
	if f.inst != nil {
		if byName, ok := f.inst.Locals[decl]; ok {
			if t, ok := byName[name]; ok {
				return t
			}
		}
	}
	if byName, ok := f.l.info.Locals[decl]; ok {
		if t, ok := byName[name]; ok {
			return t
		}
	}
	return nil
}

func (f *funcLowerer) refOf(e ast.Expr) ast.Node {
	return f.l.info.Refs[e]
}

// ---------------------------------------------------------------------------
// Emission plumbing
// ---------------------------------------------------------------------------

func (f *funcLowerer) emit(instr metals.Instr) {
	top := f.blocks[len(f.blocks)-1]
	*top = append(*top, instr)
}

// nested lowers fn's emissions into a fresh instruction list and returns it.
func (f *funcLowerer) nested(fill func()) []metals.Instr {
	var body []metals.Instr
	f.blocks = append(f.blocks, &body)
	fill()
	f.blocks = f.blocks[:len(f.blocks)-1]
	return body
}

func (f *funcLowerer) noteReg(name string) {
	f.regCount[name]++
}

func (f *funcLowerer) newReg(hint string) string {
	if hint == "" {
		hint = "t"
	}
	n := f.regCount[hint]
	f.regCount[hint] = n + 1
	if n == 0 {
		return hint
	}
	return hint + "." + strconv.Itoa(n)
}

func (f *funcLowerer) newLabel(hint string) string {
	f.labelN++
	return hint + strconv.Itoa(f.labelN)
}

// ---------------------------------------------------------------------------
// Scopes, locals and defers
// ---------------------------------------------------------------------------

func (f *funcLowerer) pushScope() {
	f.scopes = append(f.scopes, &scopeInfo{locals: map[string]*local{}})
}

// popScope runs the scope's defers (reverse order) and drops it.
func (f *funcLowerer) popScope() {
	top := f.scopes[len(f.scopes)-1]
	for i := len(top.defers) - 1; i >= 0; i-- {
		top.defers[i]()
	}
	f.scopes = f.scopes[:len(f.scopes)-1]
}

func (f *funcLowerer) popScopeNoDefers() {
	f.scopes = f.scopes[:len(f.scopes)-1]
}

func (f *funcLowerer) bind(name string, entry *local) {
	if name == "" || name == "_" {
		return
	}
	f.scopes[len(f.scopes)-1].locals[name] = entry
}

func (f *funcLowerer) lookup(name string) *local {
	for i := len(f.scopes) - 1; i >= 0; i-- {
		if entry, ok := f.scopes[i].locals[name]; ok {
			return entry
		}
	}
	return nil
}

// flushDefersDownTo emits the pending defers of every scope at depth or
// deeper, innermost first, without popping anything: an early exit runs
// them but does not end the scopes.
func (f *funcLowerer) flushDefersDownTo(depth int) {
	for i := len(f.scopes) - 1; i >= depth; i-- {
		defers := f.scopes[i].defers
		for j := len(defers) - 1; j >= 0; j-- {
			defers[j]()
		}
	}
}

func (f *funcLowerer) needsSlot(name string, typ types.Type) bool {
	if f.addrTaken[name] {
		return true
	}
	return isUserRecord(typ)
}

func isUserRecord(t types.Type) bool {
	// A union is always storage: reaching a member is `field.ptr`, which
	// needs an address, and there is no register form to fall back to.
	if _, isUnion := t.(*types.Union); isUnion {
		return true
	}
	strct, ok := t.(*types.Struct)
	if !ok {
		return false
	}
	return !isOpaqueHandle(strct)
}

// isOpaqueHandle reports whether a struct is a value the backend passes
// around without ever looking inside: fieldless, so there is nothing to
// lay out, and owned by something other than this program — the runtime,
// for a bundled struct, or a C library for an `@extern` one. A struct
// that declares fields is ordinary Gyro data and lowers to a record
// either way.
func isOpaqueHandle(t *types.Struct) bool {
	if len(t.Fields) > 0 {
		return false
	}
	return stdlib.IsBundled(t.Module) || t.Extern != ""
}

// newLocal declares a local holding an already-coerced value register.
func (f *funcLowerer) newLocal(name string, declared types.Type, value string) {
	if name == "" || name == "_" {
		return
	}
	if f.needsSlot(name, declared) {
		slot := f.newReg(name)
		f.emit(&metals.Slot{Dst: slot, Type: f.l.metalsType(declared)})
		f.emit(&metals.Store{Ptr: slot, Val: value})
		f.bind(name, &local{reg: slot, typ: declared, slot: true, env: -1})
		return
	}
	reg := f.newReg(name)
	f.emit(&metals.Copy{Dst: reg, X: value})
	f.bind(name, &local{reg: reg, typ: declared, env: -1})
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

func (f *funcLowerer) block(b *ast.Block) {
	f.pushScope()
	f.blockInPlace(b)
	f.popScope()
}

func (f *funcLowerer) blockInPlace(b *ast.Block) {
	if b == nil {
		return
	}
	terminated := false
	for _, s := range b.Stmts {
		if terminated {
			// Nothing after a return or branch runs; skip it rather than
			// emit unreachable instructions.
			return
		}
		f.stmt(s)
		terminated = stmtIsJump(s)
	}
}

func stmtIsJump(s ast.Stmt) bool {
	switch s.(type) {
	case *ast.ReturnStmt, *ast.BranchStmt:
		return true
	}
	return false
}

func (f *funcLowerer) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.VarDecl:
		f.localVar(s)
	case *ast.Block:
		f.block(s)
	case *ast.ExprStmt:
		f.discard(s.X)
	case *ast.AssignStmt:
		f.assign(s)
	case *ast.ReturnStmt:
		f.returnStmt(s)
	case *ast.IfStmt:
		f.ifStmt(s)
	case *ast.WhileStmt:
		f.whileStmt(s)
	case *ast.ForStmt:
		f.forStmt(s)
	case *ast.ForInStmt:
		f.forInStmt(s)
	case *ast.BranchStmt:
		f.branchStmt(s)
	case *ast.DeferStmt:
		f.deferStmt(s)
	case *ast.WithStmt:
		f.withStmt(s)
	case *ast.SendStmt:
		f.sendStmt(s)
	case *ast.SelectStmt:
		f.selectStmt(s)
	default:
		f.l.fail("@%s: cannot lower a %T statement", f.fn.Name, s)
	}
}

// discard evaluates an expression for its effect.
func (f *funcLowerer) discard(e ast.Expr) {
	if call, ok := e.(*ast.CallExpr); ok {
		f.prepareCall(call)()
		return
	}
	if isVoid(f.typeOfVoidOK(e)) {
		// A void non-call expression (a statement match, a block) lowers
		// through eval, which handles it.
		f.evalVoid(e)
		return
	}
	f.lowerExpr(e, nil)
}

// typeOfVoidOK is typeOf without the Default() that would trip on Void.
func (f *funcLowerer) typeOfVoidOK(e ast.Expr) types.Type {
	if f.inst != nil {
		if t, ok := f.inst.Types[e]; ok {
			return t
		}
	}
	if t, ok := f.l.info.Types[e]; ok {
		return t
	}
	return nil
}

// evalVoid lowers a void-typed expression used as a statement.
func (f *funcLowerer) evalVoid(e ast.Expr) {
	switch e := e.(type) {
	case *ast.MatchExpr:
		f.evalMatch(e, nil)
	case *ast.BlockExpr:
		f.block(e.Block)
	case *ast.AwaitExpr:
		f.lowerExpr(e.X, nil)
	default:
		f.lowerExpr(e, nil)
	}
}

func (f *funcLowerer) localVar(decl *ast.VarDecl) {
	if len(decl.Names) >= 2 {
		f.destructuringVar(decl)
		return
	}
	name := ""
	if len(decl.Names) == 1 {
		name = decl.Names[0].Name
	}
	declared := f.localType(decl, name)
	if decl.Value == nil {
		// No zero values: reserve the slot or name; the first assignment
		// defines the register.
		if declared != nil && f.needsSlot(name, declared) {
			slot := f.newReg(name)
			f.emit(&metals.Slot{Dst: slot, Type: f.l.metalsType(declared)})
			f.bind(name, &local{reg: slot, typ: declared, slot: true, env: -1})
			return
		}
		if declared != nil {
			f.bind(name, &local{reg: f.newReg(name), typ: declared, env: -1})
		}
		return
	}
	if declared == nil {
		// The value is bound to _; evaluate for effect.
		f.discard(decl.Value)
		return
	}
	value := f.lowerExpr(decl.Value, declared)
	f.newLocal(name, declared, value)
}

func (f *funcLowerer) destructuringVar(decl *ast.VarDecl) {
	if recv, ok := decl.Value.(*ast.RecvExpr); ok && len(decl.Names) == 2 {
		pair := f.recvPair(recv)
		elemType := f.localType(decl, decl.Names[0].Name)
		elem := f.newReg("elem")
		f.emit(&metals.TupleGet{Dst: elem, X: pair, Index: 0})
		if elemType != nil {
			f.newLocal(decl.Names[0].Name, elemType, elem)
		}
		open := f.newReg("open")
		f.emit(&metals.TupleGet{Dst: open, X: pair, Index: 1})
		if openType := f.localType(decl, decl.Names[1].Name); openType != nil {
			f.newLocal(decl.Names[1].Name, openType, open)
		}
		return
	}

	tuple, ok := f.typeOf(decl.Value).(*types.Tuple)
	if !ok {
		f.l.fail("@%s: destructuring a non-tuple", f.fn.Name)
	}
	value := f.lowerExpr(decl.Value, nil)
	for i, binding := range decl.Names {
		if binding.Blank || binding.Name == "_" {
			continue
		}
		declared := f.localType(decl, binding.Name)
		if declared == nil || i >= len(tuple.Elems) {
			continue
		}
		elem := f.newReg(binding.Name + ".v")
		f.emit(&metals.TupleGet{Dst: elem, X: value, Index: i})
		elem = f.coerce(elem, types.Default(tuple.Elems[i]), declared)
		f.newLocal(binding.Name, declared, elem)
	}
}

// recvPair lowers a receive into its (element, open) tuple register.
func (f *funcLowerer) recvPair(recv *ast.RecvExpr) string {
	ch := f.lowerExpr(recv.Chan, nil)
	pair := f.newReg("recv")
	f.emit(&metals.Recv{Dst: pair, Ch: ch})
	return pair
}

func (f *funcLowerer) assign(s *ast.AssignStmt) {
	if s.Op != token.Assign {
		f.compoundAssign(s)
		return
	}
	if len(s.Lhs) >= 2 {
		f.destructuringAssign(s)
		return
	}
	target := f.placeOf(s.Lhs[0])
	if target == nil {
		f.discard(s.Rhs)
		return
	}
	value := f.lowerExpr(s.Rhs, target.typ)
	target.store(value)
}

func (f *funcLowerer) destructuringAssign(s *ast.AssignStmt) {
	places := make([]*place, len(s.Lhs))
	for i, lhs := range s.Lhs {
		places[i] = f.placeOf(lhs)
	}

	if recv, ok := s.Rhs.(*ast.RecvExpr); ok && len(s.Lhs) == 2 {
		pair := f.recvPair(recv)
		for i, target := range places {
			if target == nil {
				continue
			}
			part := f.newReg("part")
			f.emit(&metals.TupleGet{Dst: part, X: pair, Index: i})
			target.store(part)
		}
		return
	}

	tuple, ok := f.typeOf(s.Rhs).(*types.Tuple)
	if !ok {
		f.l.fail("@%s: destructuring assignment from a non-tuple", f.fn.Name)
	}
	value := f.lowerExpr(s.Rhs, nil)
	for i, target := range places {
		if target == nil || i >= len(tuple.Elems) {
			continue
		}
		part := f.newReg("part")
		f.emit(&metals.TupleGet{Dst: part, X: value, Index: i})
		target.store(f.coerce(part, types.Default(tuple.Elems[i]), target.typ))
	}
}

var compoundOps = map[token.Kind]token.Kind{
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

func (f *funcLowerer) compoundAssign(s *ast.AssignStmt) {
	target := f.placeOf(s.Lhs[0])
	if target == nil {
		f.l.fail("@%s: compound assignment to _", f.fn.Name)
	}
	current := target.load()
	value := f.lowerExpr(s.Rhs, target.typ)
	op := binOpFor(compoundOps[s.Op], target.typ, f)
	updated := f.newReg("upd")
	f.emit(&metals.Bin{Dst: updated, Op: op, A: current, B: value})
	target.store(updated)
}

func (f *funcLowerer) returnStmt(s *ast.ReturnStmt) {
	switch len(s.Results) {
	case 0:
		f.flushDefersDownTo(0)
		f.emit(&metals.Return{})
	case 1:
		if isVoid(f.result) {
			f.discard(s.Results[0])
			f.flushDefersDownTo(0)
			f.emit(&metals.Return{})
			return
		}
		value := f.lowerExpr(s.Results[0], f.result)
		f.flushDefersDownTo(0)
		f.emit(&metals.Return{Val: value})
	default:
		tuple, ok := f.result.(*types.Tuple)
		if !ok || len(tuple.Elems) != len(s.Results) {
			f.l.fail("@%s: return arity does not match the result", f.fn.Name)
		}
		parts := make([]string, len(s.Results))
		for i, result := range s.Results {
			parts[i] = f.lowerExpr(result, tuple.Elems[i])
		}
		value := f.newReg("ret")
		f.emit(&metals.MakeTuple{Dst: value, Args: parts})
		f.flushDefersDownTo(0)
		f.emit(&metals.Return{Val: value})
	}
}

func (f *funcLowerer) ifStmt(s *ast.IfStmt) {
	cond := f.lowerExpr(s.Cond, types.Bool)
	then := f.nested(func() { f.block(s.Then) })
	var elseBody []metals.Instr
	if s.Else != nil {
		elseBody = f.nested(func() {
			f.pushScope()
			f.stmt(s.Else)
			f.popScope()
		})
	}
	f.emit(&metals.If{Cond: cond, Then: then, Else: elseBody})
}

func (f *funcLowerer) whileStmt(s *ast.WhileStmt) {
	breakLabel := f.newLabel("break.")
	topLabel := f.newLabel("loop.")
	body := f.nested(func() {
		cond := f.lowerExpr(s.Cond, types.Bool)
		stop := f.newReg("stop")
		f.emit(&metals.Un{Dst: stop, Op: "not", X: cond})
		f.emit(&metals.BrIf{Cond: stop, Label: breakLabel})

		f.loops = append(f.loops, loopContext{
			breakLabel: breakLabel, contLabel: topLabel, depth: len(f.scopes),
		})
		f.block(s.Body)
		f.loops = f.loops[:len(f.loops)-1]
		f.emit(&metals.Br{Label: topLabel})
	})
	f.emit(&metals.Block{Label: breakLabel, Body: []metals.Instr{
		&metals.Loop{Label: topLabel, Body: body},
	}})
}

func (f *funcLowerer) forStmt(s *ast.ForStmt) {
	f.pushScope()
	if s.Init != nil {
		f.stmt(s.Init)
	}
	breakLabel := f.newLabel("break.")
	topLabel := f.newLabel("loop.")
	contLabel := f.newLabel("cont.")

	body := f.nested(func() {
		if s.Cond != nil {
			cond := f.lowerExpr(s.Cond, types.Bool)
			stop := f.newReg("stop")
			f.emit(&metals.Un{Dst: stop, Op: "not", X: cond})
			f.emit(&metals.BrIf{Cond: stop, Label: breakLabel})
		}
		f.loops = append(f.loops, loopContext{
			breakLabel: breakLabel, contLabel: contLabel, depth: len(f.scopes),
		})
		inner := f.nested(func() { f.block(s.Body) })
		f.loops = f.loops[:len(f.loops)-1]
		f.emit(&metals.Block{Label: contLabel, Body: inner})
		if s.Post != nil {
			f.stmt(s.Post)
		}
		f.emit(&metals.Br{Label: topLabel})
	})
	f.emit(&metals.Block{Label: breakLabel, Body: []metals.Instr{
		&metals.Loop{Label: topLabel, Body: body},
	}})
	f.popScope()
}

func (f *funcLowerer) branchStmt(s *ast.BranchStmt) {
	if len(f.loops) == 0 {
		f.l.fail("@%s: %s outside a loop", f.fn.Name, s.Kind)
	}
	loop := f.loops[len(f.loops)-1]
	f.flushDefersDownTo(loop.depth)
	if s.Kind == token.Break {
		f.emit(&metals.Br{Label: loop.breakLabel})
		return
	}
	f.emit(&metals.Br{Label: loop.contLabel})
}

func (f *funcLowerer) deferStmt(s *ast.DeferStmt) {
	call, ok := s.Call.(*ast.CallExpr)
	if !ok {
		f.l.fail("@%s: defer takes a call", f.fn.Name)
	}
	f.deferring++
	run := f.prepareCall(call)
	f.deferring--

	top := f.scopes[len(f.scopes)-1]
	top.defers = append(top.defers, func() { run() })
}

func (f *funcLowerer) sendStmt(s *ast.SendStmt) {
	chanType, ok := f.typeOf(s.Chan).(*types.Chan)
	if !ok {
		f.l.fail("@%s: send on a non-channel", f.fn.Name)
	}
	ch := f.lowerExpr(s.Chan, nil)
	value := f.lowerExpr(s.Value, chanType.Elem)
	f.emit(&metals.Send{Ch: ch, Val: value})
}

func (f *funcLowerer) selectStmt(s *ast.SelectStmt) {
	out := &metals.Select{}
	for _, arm := range s.Arms {
		out.Arms = append(out.Arms, f.selectArm(arm))
	}
	if s.Else != nil {
		out.Default = f.nested(func() {
			f.pushScope()
			f.stmt(s.Else)
			f.popScope()
		})
		if len(out.Default) == 0 {
			// An empty default still means "do not block".
			out.Default = []metals.Instr{&metals.MakeTuple{Dst: f.newReg("nop"), Args: nil}}
		}
	}
	f.emit(out)
}

func (f *funcLowerer) selectArm(arm ast.SelectArm) metals.SelectArm {
	switch comm := arm.Comm.(type) {
	case *ast.SendStmt:
		chanType := f.typeOf(comm.Chan).(*types.Chan)
		ch := f.lowerExpr(comm.Chan, nil)
		value := f.lowerExpr(comm.Value, chanType.Elem)
		body := f.nested(func() {
			f.pushScope()
			f.stmt(arm.Body)
			f.popScope()
		})
		return metals.SelectArm{Send: true, Ch: ch, Val: value, Body: body}

	case *ast.VarDecl:
		recv, ok := comm.Value.(*ast.RecvExpr)
		if !ok || len(comm.Names) != 1 {
			f.l.fail("@%s: a select binding is one name receiving", f.fn.Name)
		}
		ch := f.lowerExpr(recv.Chan, nil)
		declared := f.localType(comm, comm.Names[0].Name)
		dst := f.newReg(comm.Names[0].Name)
		body := f.nested(func() {
			f.pushScope()
			if declared != nil {
				f.bind(comm.Names[0].Name, &local{reg: dst, typ: declared, env: -1})
			}
			f.stmt(arm.Body)
			f.popScope()
		})
		return metals.SelectArm{Dst: dst, Ch: ch, Body: body}

	case *ast.AssignStmt:
		recv, ok := comm.Rhs.(*ast.RecvExpr)
		if !ok || len(comm.Lhs) != 1 {
			f.l.fail("@%s: a select assignment is one target receiving", f.fn.Name)
		}
		ch := f.lowerExpr(recv.Chan, nil)
		dst := f.newReg("got")
		body := f.nested(func() {
			f.pushScope()
			if target := f.placeOf(comm.Lhs[0]); target != nil {
				target.store(dst)
			}
			f.stmt(arm.Body)
			f.popScope()
		})
		return metals.SelectArm{Dst: dst, Ch: ch, Body: body}

	case *ast.ExprStmt:
		recv, ok := comm.X.(*ast.RecvExpr)
		if !ok {
			f.l.fail("@%s: a select arm waits on a channel operation", f.fn.Name)
		}
		ch := f.lowerExpr(recv.Chan, nil)
		dst := f.newReg("got")
		body := f.nested(func() {
			f.pushScope()
			f.stmt(arm.Body)
			f.popScope()
		})
		return metals.SelectArm{Dst: dst, Ch: ch, Body: body}
	}
	f.l.fail("@%s: cannot lower this select arm", f.fn.Name)
	return metals.SelectArm{}
}

// ---------------------------------------------------------------------------
// Places
// ---------------------------------------------------------------------------

// place is a resolved storage location: how to read it, write it, and —
// when it lives in memory — take its address.
type place struct {
	typ   types.Type
	load  func() string
	store func(value string)
	addr  func() string // "" when the location has no address
}

func noAddr() string { return "" }

// placeOf resolves an assignable expression. nil means the blank discard.
func (f *funcLowerer) placeOf(e ast.Expr) *place {
	switch e := e.(type) {
	case *ast.Ident:
		if e.Name == "_" {
			return nil
		}
		if entry := f.lookup(e.Name); entry != nil {
			return f.localPlace(entry)
		}
		if decl, ok := f.refOf(e).(*ast.VarDecl); ok && !decl.Const {
			return f.globalPlace(decl, e.Name)
		}
		f.l.fail("@%s: %s does not name an assignable place", f.fn.Name, e.Name)

	case *ast.SelfExpr:
		if entry := f.lookup("self"); entry != nil {
			return f.localPlace(entry)
		}
		f.l.fail("@%s: self outside a method", f.fn.Name)

	case *ast.SelectorExpr:
		return f.selectorPlace(e)

	case *ast.IndexExpr:
		return f.indexPlace(e)

	case *ast.TupleIndexExpr:
		return f.tupleElemPlace(e)

	case *ast.UnaryExpr:
		if e.Op == token.Star {
			pointer, ok := f.typeOf(e.X).(*types.Pointer)
			if !ok {
				f.l.fail("@%s: dereferencing a non-pointer", f.fn.Name)
			}
			ptr := f.lowerExpr(e.X, nil)
			return f.pointerPlace(ptr, pointer.Elem)
		}
	}
	f.l.fail("@%s: cannot assign to this expression", f.fn.Name)
	return nil
}

func (f *funcLowerer) localPlace(entry *local) *place {
	if entry.env >= 0 {
		envIndex := entry.env
		fieldPtr := func() string {
			ptr := f.newReg("cap")
			f.emit(&metals.FieldPtr{Dst: ptr, Ptr: entry.reg, Index: envIndex})
			return ptr
		}
		return &place{
			typ:   entry.typ,
			load:  func() string { out := f.newReg("v"); f.emit(&metals.Load{Dst: out, Ptr: fieldPtr()}); return out },
			store: func(value string) { f.emit(&metals.Store{Ptr: fieldPtr(), Val: value}) },
			addr:  fieldPtr,
		}
	}
	if entry.slot {
		return f.pointerPlace(entry.reg, entry.typ)
	}
	return &place{
		typ:  entry.typ,
		load: func() string { return entry.reg },
		store: func(value string) {
			// Writing a register back onto itself is what an array
			// mutated in its own register asks for, and it is a copy of
			// every element to change nothing.
			if value == entry.reg {
				return
			}
			f.emit(&metals.Copy{Dst: entry.reg, X: value})
		},
		addr: noAddr,
	}
}

// pointerPlace is a value behind a pointer register.
func (f *funcLowerer) pointerPlace(ptr string, typ types.Type) *place {
	return &place{
		typ:   typ,
		load:  func() string { out := f.newReg("v"); f.emit(&metals.Load{Dst: out, Ptr: ptr}); return out },
		store: func(value string) { f.emit(&metals.Store{Ptr: ptr, Val: value}) },
		addr:  func() string { return ptr },
	}
}

func (f *funcLowerer) selectorPlace(e *ast.SelectorExpr) *place {
	baseType := f.typeOf(e.X)

	// Through a pointer the field has a stable address.
	if pointer, ok := baseType.(*types.Pointer); ok {
		if union, isUnion := pointer.Elem.(*types.Union); isUnion {
			base := f.lowerExpr(e.X, nil)
			return f.unionMemberPlace(base, union, e.Name)
		}
		strct := f.structOf(pointer.Elem)
		index := fieldIndex(strct, e.Name)
		base := f.lowerExpr(e.X, nil)
		ptr := f.newReg(e.Name + ".p")
		f.emit(&metals.FieldPtr{Dst: ptr, Ptr: base, Index: index})
		return f.pointerPlace(ptr, strct.Fields[index].Type)
	}

	// A union is only ever reached through its storage: writing one
	// member is writing all of it, so there is no register form of the
	// operation to fall back to.
	if union, isUnion := baseType.(*types.Union); isUnion {
		basePlace := f.placeOfAddressable(e.X)
		if basePlace == nil {
			f.l.fail("@%s: a union member of a value with no storage", f.fn.Name)
		}
		base := basePlace.addr()
		if base == "" {
			f.l.fail("@%s: a union member of a value with no address", f.fn.Name)
		}
		return f.unionMemberPlace(base, union, e.Name)
	}

	strct := f.structOf(baseType)
	index := fieldIndex(strct, e.Name)
	fieldType := strct.Fields[index].Type

	// An addressable struct value: field.ptr through its slot.
	if basePlace := f.placeOfAddressable(e.X); basePlace != nil {
		if base := basePlace.addr(); base != "" {
			ptr := f.newReg(e.Name + ".p")
			f.emit(&metals.FieldPtr{Dst: ptr, Ptr: base, Index: index})
			return f.pointerPlace(ptr, fieldType)
		}
	}

	// A struct rvalue: readable, not writable.
	return &place{
		typ: fieldType,
		load: func() string {
			base := f.lowerExpr(e.X, nil)
			out := f.newReg(e.Name)
			f.emit(&metals.FieldGet{Dst: out, X: base, Index: index})
			return out
		},
		store: func(string) { f.l.fail("@%s: cannot assign into this value", f.fn.Name) },
		addr:  noAddr,
	}
}

// unionMemberPlace reaches one member through the union's address. Every
// member begins where the union does, so this is the same `field.ptr` a
// record's field uses and the offset it answers is zero.
func (f *funcLowerer) unionMemberPlace(base string, union *types.Union, name string) *place {
	index := unionMemberIndex(union, name)
	if index < 0 {
		f.l.fail("@%s: %s has no member %s", f.fn.Name, union.Name, name)
	}
	ptr := f.newReg(name + ".p")
	f.emit(&metals.FieldPtr{Dst: ptr, Ptr: base, Index: index})
	return f.pointerPlace(ptr, union.Members[index].Type)
}

func unionMemberIndex(union *types.Union, name string) int {
	for i := range union.Members {
		if union.Members[i].Name == name {
			return i
		}
	}
	return -1
}

// placeOfAddressable is placeOf for bases that may not be assignable
// expressions at all; it returns nil instead of failing.
func (f *funcLowerer) placeOfAddressable(e ast.Expr) *place {
	switch e := e.(type) {
	case *ast.Ident:
		if entry := f.lookup(e.Name); entry != nil {
			return f.localPlace(entry)
		}
		if decl, ok := f.refOf(e).(*ast.VarDecl); ok && !decl.Const {
			return f.globalPlace(decl, e.Name)
		}
	case *ast.SelfExpr:
		if entry := f.lookup("self"); entry != nil {
			return f.localPlace(entry)
		}
	case *ast.SelectorExpr:
		return f.selectorPlace(e)
	case *ast.IndexExpr:
		// An element of an addressable fixed-size array has an address of
		// its own; indexPlace answers noAddr for every other container.
		return f.indexPlace(e)
	case *ast.UnaryExpr:
		if e.Op == token.Star {
			if pointer, ok := f.typeOf(e.X).(*types.Pointer); ok {
				ptr := f.lowerExpr(e.X, nil)
				return f.pointerPlace(ptr, pointer.Elem)
			}
		}
	}
	return nil
}

func (f *funcLowerer) indexPlace(e *ast.IndexExpr) *place {
	baseType := f.typeOf(e.X)

	// An Array<T> is a runtime handle, so its elements are reached by
	// asking the runtime for one's address. `at` rather than `get` is
	// what makes an index a place: `get` answers with a copy, and
	// assigning into a copy assigns into nothing.
	if strct, isStruct := baseType.(*types.Struct); isStruct {
		if elemType := arrayElemOf(strct); elemType != nil {
			return f.arrayElementPlace(e, elemType)
		}
	}

	elem := elemOf(baseType, f)
	basePlace := f.placeOfAddressable(e.X)

	// A fixed-size array with an address is reached through it. Loading
	// the array first would copy every element to read or write one, and
	// writing one back would copy them all again; `self.data[j]` on a
	// `u8[64]` field is 64 bytes moved per byte examined.
	if _, isArray := baseType.(*types.Array); isArray && basePlace != nil {
		if base := basePlace.addr(); base != "" {
			elementPtr := func() string {
				index := f.lowerExpr(e.Index, types.USize)
				ptr := f.newReg("elem.p")
				f.emit(&metals.IndexPtr{Dst: ptr, Ptr: base, Index: index})
				return ptr
			}
			return &place{
				typ: elem,
				load: func() string {
					out := f.newReg("elem")
					f.emit(&metals.Load{Dst: out, Ptr: elementPtr()})
					return out
				},
				store: func(value string) {
					f.emit(&metals.Store{Ptr: elementPtr(), Val: value})
				},
				addr: elementPtr,
			}
		}
	}

	loadBase := func() string {
		if basePlace != nil {
			return basePlace.load()
		}
		return f.lowerExpr(e.X, nil)
	}

	// A slice already carries a data pointer and a length, so an element
	// of one has an address without anything having to be loaded. It is
	// the same checked instruction a fixed-size array uses; only where
	// the extent comes from differs.
	elementAddr := noAddr
	if _, isSlice := baseType.(*types.Slice); isSlice {
		elementAddr = func() string {
			base := loadBase()
			index := f.lowerExpr(e.Index, types.USize)
			ptr := f.newReg("elem.p")
			f.emit(&metals.IndexPtr{Dst: ptr, Ptr: base, Index: index})
			return ptr
		}
	}

	return &place{
		typ: elem,
		load: func() string {
			base := loadBase()
			index := f.lowerExpr(e.Index, types.USize)
			out := f.newReg("elem")
			f.emit(&metals.IndexGet{Dst: out, X: base, Index: index})
			return out
		},
		store: func(value string) {
			base := loadBase()
			index := f.lowerExpr(e.Index, types.USize)
			f.emit(&metals.IndexSet{X: base, Index: index, Val: value})
			// An array loaded out of memory is a copy; write it back.
			if _, isArray := baseType.(*types.Array); isArray && basePlace != nil {
				basePlace.store(base)
			}
		},
		addr: elementAddr,
	}
}

// arrayElementPlace reaches one element of an Array<T> through the
// address the runtime hands back. The name it calls is the one an
// explicit `xs.at(i)` lowers to, so the two share an import.
func (f *funcLowerer) arrayElementPlace(e *ast.IndexExpr, elemType types.Type) *place {
	name := "Array<" + elemType.String() + ">::at"
	f.l.ensureImport(name, &metals.FnType{
		Params: []metals.Type{&metals.Handle{}, metals.TUsize},
		Result: &metals.Ptr{Elem: f.l.metalsType(elemType)},
	})

	elementPtr := func() string {
		handle := f.nativeReceiver(e.X)
		index := f.lowerExpr(e.Index, types.USize)
		ptr := f.newReg("elem.p")
		f.emit(&metals.Call{Dst: ptr, Callee: name, Args: []string{handle, index}})
		return ptr
	}
	return f.pointerPlace(elementPtr(), elemType)
}

// arrayView calls Array<T>::slice, which is the borrow the runtime
// already offers. The name is the one an explicit `xs.slice()` lowers
// to, so the two share an import.
func (f *funcLowerer) arrayView(x ast.Expr, elemType types.Type) string {
	name := "Array<" + elemType.String() + ">::slice"
	f.l.ensureImport(name, &metals.FnType{
		Params: []metals.Type{&metals.Handle{}},
		Result: &metals.SliceOf{Elem: f.l.metalsType(elemType)},
	})
	view := f.newReg("view")
	f.emit(&metals.Call{Dst: view, Callee: name, Args: []string{f.nativeReceiver(x)}})
	return view
}

// arrayElemOf is the element type of an `Array<T>`, or nil. The Array is
// the runtime's own handle, so it is recognized by being that
// declaration rather than by shape.
func arrayElemOf(t *types.Struct) types.Type {
	origin := t
	if t.Origin != nil {
		origin = t.Origin
	}
	if origin.Name != "Array" || !stdlib.IsBundled(origin.Module) || len(t.Args) != 1 {
		return nil
	}
	return t.Args[0]
}

// sliceNarrowing lowers `xs[lo..hi]` to a window onto the storage xs
// already views. Nothing is copied and nothing is allocated.
//
// A fixed-size array is viewed through its *address* where it has one.
// Loading it first would put a copy in a register and borrow that, so a
// write through the window would reach storage nothing else can see —
// the same reason `index.ptr` exists.
func (f *funcLowerer) sliceNarrowing(e *ast.IndexExpr, r *ast.RangeExpr) string {
	baseType := f.typeOf(e.X)
	base := f.narrowingBase(e.X, baseType)

	lo := ""
	if r.Lo == nil {
		lo = f.newReg("from")
		f.emit(&metals.Const{Dst: lo, Type: metals.TUsize, Value: "0"})
	} else {
		lo = f.lowerExpr(r.Lo, types.USize)
	}

	hi := ""
	if r.Hi == nil {
		hi = f.newReg("to")
		f.emit(&metals.Len{Dst: hi, X: base})
	} else {
		hi = f.lowerExpr(r.Hi, types.USize)
	}

	out := f.newReg("window")
	f.emit(&metals.SliceSub{Dst: out, X: base, Lo: lo, Hi: hi})
	return out
}

// narrowingBase produces the slice a narrowing starts from.
func (f *funcLowerer) narrowingBase(x ast.Expr, baseType types.Type) string {
	// An Array<T> narrows through the view it already hands out, which
	// borrows its buffer rather than copying it.
	if strct, isStruct := baseType.(*types.Struct); isStruct {
		if elemType := arrayElemOf(strct); elemType != nil {
			return f.arrayView(x, elemType)
		}
	}
	if _, isArray := baseType.(*types.Array); !isArray {
		return f.lowerExpr(x, nil)
	}
	if place := f.placeOfAddressable(x); place != nil {
		if addr := place.addr(); addr != "" {
			view := f.newReg("view")
			f.emit(&metals.Slice{Dst: view, X: addr})
			return view
		}
	}
	// An array with no address — a literal, or a call's result — is its
	// own register, and slice.of borrows that register's storage.
	value := f.lowerExpr(x, nil)
	view := f.newReg("view")
	f.emit(&metals.Slice{Dst: view, X: value})
	return view
}

func (f *funcLowerer) tupleElemPlace(e *ast.TupleIndexExpr) *place {
	tuple, ok := f.typeOf(e.X).(*types.Tuple)
	if !ok {
		f.l.fail("@%s: positional access on a non-tuple", f.fn.Name)
	}
	elemType := tuple.Elems[e.Index]
	basePlace := f.placeOfAddressable(e.X)
	return &place{
		typ: elemType,
		load: func() string {
			base := f.lowerExpr(e.X, nil)
			out := f.newReg("elem")
			f.emit(&metals.TupleGet{Dst: out, X: base, Index: e.Index})
			return out
		},
		store: func(value string) {
			if basePlace == nil {
				f.l.fail("@%s: cannot assign into this tuple", f.fn.Name)
			}
			base := basePlace.load()
			parts := make([]string, len(tuple.Elems))
			for i := range tuple.Elems {
				if i == e.Index {
					parts[i] = value
					continue
				}
				part := f.newReg("keep")
				f.emit(&metals.TupleGet{Dst: part, X: base, Index: i})
				parts[i] = part
			}
			rebuilt := f.newReg("tup")
			f.emit(&metals.MakeTuple{Dst: rebuilt, Args: parts})
			basePlace.store(rebuilt)
		},
		addr: noAddr,
	}
}

func (f *funcLowerer) structOf(t types.Type) *types.Struct {
	strct, ok := t.(*types.Struct)
	if !ok {
		f.l.fail("@%s: %s is not a struct", f.fn.Name, t)
	}
	return strct
}

func fieldIndex(strct *types.Struct, name string) int {
	for i := range strct.Fields {
		if strct.Fields[i].Name == name {
			return i
		}
	}
	return -1
}

func elemOf(t types.Type, f *funcLowerer) types.Type {
	switch t := t.(type) {
	case *types.Slice:
		return t.Elem
	case *types.Array:
		return t.Elem
	}
	f.l.fail("@%s: %s cannot be indexed", f.fn.Name, t)
	return nil
}

// ---------------------------------------------------------------------------
// AST walking
// ---------------------------------------------------------------------------

// walk visits every node reachable from n, statements and expressions
// alike. It exists for the small prepasses (address-taken, free
// variables); the lowering itself dispatches explicitly.
func walk(node ast.Node, visit func(ast.Node)) {
	if node == nil {
		return
	}
	visit(node)
	switch n := node.(type) {
	case *ast.Block:
		if n == nil {
			return
		}
		for _, s := range n.Stmts {
			walk(s, visit)
		}
	case *ast.VarDecl:
		walk(n.Value, visit)
	case *ast.ExprStmt:
		walk(n.X, visit)
	case *ast.AssignStmt:
		for _, lhs := range n.Lhs {
			walk(lhs, visit)
		}
		walk(n.Rhs, visit)
	case *ast.ReturnStmt:
		for _, r := range n.Results {
			walk(r, visit)
		}
	case *ast.IfStmt:
		walk(n.Cond, visit)
		walk(n.Then, visit)
		walk(n.Else, visit)
	case *ast.WhileStmt:
		walk(n.Cond, visit)
		walk(n.Body, visit)
	case *ast.ForStmt:
		walk(n.Init, visit)
		walk(n.Cond, visit)
		walk(n.Post, visit)
		walk(n.Body, visit)
	case *ast.ForInStmt:
		walk(n.Iterable, visit)
		walk(n.Body, visit)
	case *ast.DeferStmt:
		walk(n.Call, visit)
	case *ast.WithStmt:
		walk(n.Allocator, visit)
	case *ast.SendStmt:
		walk(n.Chan, visit)
		walk(n.Value, visit)
	case *ast.SelectStmt:
		for _, arm := range n.Arms {
			walk(arm.Comm, visit)
			walk(arm.Body, visit)
		}
		walk(n.Else, visit)
	case *ast.UnaryExpr:
		walk(n.X, visit)
	case *ast.BinaryExpr:
		walk(n.X, visit)
		walk(n.Y, visit)
	case *ast.CallExpr:
		walk(n.Fun, visit)
		for _, arg := range n.Args {
			walk(arg.Value, visit)
		}
	case *ast.SelectorExpr:
		walk(n.X, visit)
	case *ast.IndexExpr:
		walk(n.X, visit)
		walk(n.Index, visit)
	case *ast.TupleIndexExpr:
		walk(n.X, visit)
	case *ast.RangeExpr:
		walk(n.Lo, visit)
		walk(n.Hi, visit)
	case *ast.ArrayLit:
		for _, elem := range n.Elements {
			walk(elem, visit)
		}
	case *ast.TupleLit:
		for _, elem := range n.Elements {
			walk(elem, visit)
		}
	case *ast.StructLit:
		for _, field := range n.Fields {
			walk(field.Value, visit)
		}
	case *ast.StringLit:
		for _, part := range n.Parts {
			walk(part.Expr, visit)
		}
	case *ast.MatchExpr:
		walk(n.Subject, visit)
		for _, arm := range n.Arms {
			walkPattern(arm.Pattern, visit)
			walk(arm.Body, visit)
		}
	case *ast.BlockExpr:
		walk(n.Block, visit)
	case *ast.AwaitExpr:
		walk(n.X, visit)
	case *ast.RecvExpr:
		walk(n.Chan, visit)
	case *ast.ChanLit:
		walk(n.Cap, visit)
	case *ast.ClosureExpr:
		for _, param := range n.Params {
			walk(param.Default, visit)
		}
		walk(n.Body, visit)
		walk(n.BodyExpr, visit)
	case *ast.CastExpr:
		walk(n.X, visit)
	}
}

func walkPattern(p ast.Pattern, visit func(ast.Node)) {
	switch p := p.(type) {
	case *ast.LiteralPattern:
		walk(p.Value, visit)
	case *ast.VariantPattern:
		for _, payload := range p.Payload {
			walkPattern(payload, visit)
		}
	case *ast.TypeTestPattern:
		for _, payload := range p.Payload {
			walkPattern(payload, visit)
		}
	}
}
