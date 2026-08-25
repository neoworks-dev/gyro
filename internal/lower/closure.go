package lower

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/types"
)

// evalClosure lifts a closure to a top-level function taking an
// environment record pointer, and partially applies it. The environment
// is heap-allocated and shared by every copy of the closure value, so a
// closure owns one set of captured copies and its mutations accumulate
// across calls — the recorded FnMut-style semantics.
func (f *funcLowerer) evalClosure(e *ast.ClosureExpr) string {
	sig, ok := f.typeOf(e).(*types.Func)
	if !ok {
		f.l.fail("@%s: a closure without a function type", f.fn.Name)
	}
	captured := f.freeVars(e)

	liftedName := f.l.syntheticName(f.fn.Name + "::closure")
	envName := liftedName + ".env"
	envDecl := &metals.TypeDecl{Name: envName, Record: &metals.RecordShape{}}
	for _, c := range captured {
		envDecl.Record.Fields = append(envDecl.Record.Fields, metals.FieldDecl{
			Name: c.name,
			Type: f.l.metalsType(c.typ),
		})
	}
	f.l.declared[envName] = envDecl
	f.l.module.Types = append(f.l.module.Types, envDecl)

	f.liftClosureBody(e, sig, liftedName, envName, captured)

	envPtr := f.newReg("env")
	f.emit(&metals.New{Dst: envPtr, Type: &metals.Named{Name: envName}})
	for i, c := range captured {
		entry := f.lookup(c.name)
		value := f.localPlace(entry).load()
		fieldPtr := f.newReg("cap")
		f.emit(&metals.FieldPtr{Dst: fieldPtr, Ptr: envPtr, Index: i})
		f.emit(&metals.Store{Ptr: fieldPtr, Val: value})
	}
	out := f.newReg("closure")
	f.emit(&metals.MakeClosure{Dst: out, Callee: liftedName, Env: envPtr})
	return out
}

func (f *funcLowerer) liftClosureBody(e *ast.ClosureExpr, sig *types.Func, liftedName, envName string, captured []capture) {
	child := &funcLowerer{
		l:        f.l,
		decl:     f.decl,
		inst:     f.inst,
		fn:       &metals.Func{Name: liftedName, Async: e.Async},
		regCount: map[string]int{},
		result:   sig.Result,
	}
	if !isVoid(sig.Result) {
		child.fn.Result = f.l.metalsType(sig.Result)
	}
	child.addrTaken = map[string]bool{}
	markAddressTaken(e.Body, child.addrTaken)
	markAddressTaken(e.BodyExpr, child.addrTaken)
	child.blocks = []*[]metals.Instr{&child.fn.Body}
	child.pushScope()

	child.fn.Params = append(child.fn.Params, metals.Param{
		Name: "env",
		Type: &metals.Ptr{Elem: &metals.Named{Name: envName}},
	})
	child.noteReg("env")
	for i, c := range captured {
		child.bind(c.name, &local{reg: "env", typ: c.typ, env: i})
	}
	for _, param := range e.Params {
		declared := child.localType(e, param.Name)
		if declared == nil {
			f.l.fail("@%s: a closure parameter has no recorded type", f.fn.Name)
		}
		child.declareParam(param.Name, declared)
	}

	if e.Body != nil {
		child.blockInPlace(e.Body)
		child.epilogue(blockTerminates(e.Body))
	} else if isVoid(sig.Result) {
		child.discard(e.BodyExpr)
		child.emit(&metals.Return{})
	} else {
		value := child.lowerExpr(e.BodyExpr, sig.Result)
		child.emit(&metals.Return{Val: value})
	}
	child.popScopeNoDefers()
	f.l.module.Funcs = append(f.l.module.Funcs, child.fn)
}

// ---------------------------------------------------------------------------
// Free variables
// ---------------------------------------------------------------------------

// freeVars finds the enclosing locals a closure uses, in first-use
// order. Implicit copies and explicit move captures lower identically.
func (f *funcLowerer) freeVars(e *ast.ClosureExpr) []capture {
	w := &fvWalker{f: f, seen: map[string]bool{}}
	w.push()
	for _, moved := range e.Captures {
		w.use(moved.Name)
	}
	for _, param := range e.Params {
		w.bind(param.Name)
	}
	if e.Body != nil {
		w.block(e.Body)
	}
	w.expr(e.BodyExpr)
	w.pop()
	return w.out
}

type fvWalker struct {
	f     *funcLowerer
	bound []map[string]bool
	seen  map[string]bool
	out   []capture
}

func (w *fvWalker) push() { w.bound = append(w.bound, map[string]bool{}) }
func (w *fvWalker) pop()  { w.bound = w.bound[:len(w.bound)-1] }

func (w *fvWalker) bind(name string) {
	w.bound[len(w.bound)-1][name] = true
}

func (w *fvWalker) isBound(name string) bool {
	for i := len(w.bound) - 1; i >= 0; i-- {
		if w.bound[i][name] {
			return true
		}
	}
	return false
}

func (w *fvWalker) use(name string) {
	if name == "" || name == "_" || w.isBound(name) || w.seen[name] {
		return
	}
	entry := w.f.lookup(name)
	if entry == nil {
		return
	}
	w.seen[name] = true
	w.out = append(w.out, capture{name: name, typ: entry.typ})
}

func (w *fvWalker) block(b *ast.Block) {
	if b == nil {
		return
	}
	w.push()
	for _, s := range b.Stmts {
		w.stmt(s)
	}
	w.pop()
}

func (w *fvWalker) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.VarDecl:
		w.expr(s.Value)
		for _, binding := range s.Names {
			w.bind(binding.Name)
		}
	case *ast.Block:
		w.block(s)
	case *ast.ExprStmt:
		w.expr(s.X)
	case *ast.AssignStmt:
		for _, lhs := range s.Lhs {
			w.expr(lhs)
		}
		w.expr(s.Rhs)
	case *ast.ReturnStmt:
		for _, result := range s.Results {
			w.expr(result)
		}
	case *ast.IfStmt:
		w.expr(s.Cond)
		w.block(s.Then)
		if s.Else != nil {
			w.push()
			w.stmt(s.Else)
			w.pop()
		}
	case *ast.WhileStmt:
		w.expr(s.Cond)
		w.block(s.Body)
	case *ast.ForStmt:
		w.push()
		if s.Init != nil {
			w.stmt(s.Init)
		}
		w.expr(s.Cond)
		if s.Post != nil {
			w.stmt(s.Post)
		}
		w.block(s.Body)
		w.pop()
	case *ast.ForInStmt:
		w.expr(s.Iterable)
		w.push()
		w.bind(s.Name)
		w.block(s.Body)
		w.pop()
	case *ast.DeferStmt:
		w.expr(s.Call)
	case *ast.SendStmt:
		w.expr(s.Chan)
		w.expr(s.Value)
	case *ast.SelectStmt:
		for _, arm := range s.Arms {
			w.push()
			w.stmt(arm.Comm)
			w.stmt(arm.Body)
			w.pop()
		}
		if s.Else != nil {
			w.push()
			w.stmt(s.Else)
			w.pop()
		}
	}
}

func (w *fvWalker) expr(e ast.Expr) {
	switch e := e.(type) {
	case nil:
		return
	case *ast.Ident:
		w.use(e.Name)
	case *ast.SelfExpr:
		w.use("self")
	case *ast.UnaryExpr:
		w.expr(e.X)
	case *ast.BinaryExpr:
		w.expr(e.X)
		w.expr(e.Y)
	case *ast.CallExpr:
		w.expr(e.Fun)
		for _, arg := range e.Args {
			w.expr(arg.Value)
		}
	case *ast.SelectorExpr:
		w.expr(e.X)
	case *ast.IndexExpr:
		w.expr(e.X)
		w.expr(e.Index)
	case *ast.TupleIndexExpr:
		w.expr(e.X)
	case *ast.RangeExpr:
		w.expr(e.Lo)
		w.expr(e.Hi)
	case *ast.ArrayLit:
		for _, element := range e.Elements {
			w.expr(element)
		}
	case *ast.TupleLit:
		for _, element := range e.Elements {
			w.expr(element)
		}
	case *ast.StructLit:
		for _, field := range e.Fields {
			w.expr(field.Value)
		}
	case *ast.StringLit:
		for _, part := range e.Parts {
			w.expr(part.Expr)
		}
	case *ast.MatchExpr:
		w.expr(e.Subject)
		for _, arm := range e.Arms {
			w.push()
			w.pattern(arm.Pattern)
			w.stmt(arm.Body)
			w.pop()
		}
	case *ast.BlockExpr:
		w.block(e.Block)
	case *ast.AwaitExpr:
		w.expr(e.X)
	case *ast.RecvExpr:
		w.expr(e.Chan)
	case *ast.ChanLit:
		w.expr(e.Cap)
	case *ast.ClosureExpr:
		w.push()
		for _, moved := range e.Captures {
			w.use(moved.Name)
		}
		for _, param := range e.Params {
			w.bind(param.Name)
		}
		if e.Body != nil {
			w.block(e.Body)
		}
		w.expr(e.BodyExpr)
		w.pop()
	case *ast.CastExpr:
		w.expr(e.X)
	}
}

func (w *fvWalker) pattern(p ast.Pattern) {
	switch p := p.(type) {
	case *ast.BindingPattern:
		w.bind(p.Name)
	case *ast.LiteralPattern:
		w.expr(p.Value)
	case *ast.VariantPattern:
		for _, payload := range p.Payload {
			w.pattern(payload)
		}
	case *ast.TypeTestPattern:
		for _, payload := range p.Payload {
			w.pattern(payload)
		}
	}
}
