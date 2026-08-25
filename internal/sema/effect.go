package sema

import (
	"strconv"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
)

// Concrete-effect checking. A declared effect row is a complete statement
// of what a function does: a row without `io` promises no io, a row without
// `alloc` promises no allocation, and a row holding only borrows promises
// both. A function with no row is unconstrained in v1.
//
// A call contributes its callee's declared row when it states one. When it
// does not, the callee's effects are inferred from its body, in whatever
// module that body lives: Gyro compiles whole-program and the loader holds
// every source, so a row is a way to state an interface, not the only way
// to know what a function does. The one body that cannot be read is the
// one that does not exist — an intrinsic — and a rowless intrinsic
// therefore contributes every concrete effect. A call through a function
// value still contributes nothing. String interpolation builds new text,
// which allocates.
//
// The check runs after every module, so a row is judged with every body
// resolved and every generic instantiation recorded.

// effectSet is the concrete-effect lattice: two bits.
type effectSet struct {
	io, alloc bool
}

func (e effectSet) union(other effectSet) effectSet {
	return effectSet{io: e.io || other.io, alloc: e.alloc || other.alloc}
}

// minus is what e performs beyond what other allows.
func (e effectSet) minus(other effectSet) effectSet {
	return effectSet{io: e.io && !other.io, alloc: e.alloc && !other.alloc}
}

func (e effectSet) empty() bool { return !e.io && !e.alloc }

// everyEffect is the top of the lattice: what a callee that promises
// nothing may do.
var everyEffect = effectSet{io: true, alloc: true}

func (e effectSet) String() string {
	switch {
	case e.io && e.alloc:
		return "io and alloc"
	case e.io:
		return "io"
	case e.alloc:
		return "alloc"
	}
	return "nothing"
}

// declaredEffects reads the concrete effects a row states. closed is false
// when there is no row, or the row carries a row variable and is open:
// neither promises anything about effects.
func declaredEffects(row *ast.EffectRow) (effectSet, bool) {
	if row == nil || row.Var != "" {
		return effectSet{}, false
	}
	var declared effectSet
	for _, name := range row.Names {
		switch name {
		case "io":
			declared.io = true
		case "alloc":
			declared.alloc = true
		}
	}
	return declared, true
}

// ---------------------------------------------------------------------------
// Checking a declared row against a body
// ---------------------------------------------------------------------------

// checkModuleEffects enforces every declared row of the module's functions,
// and every row a closure inside them states for itself.
func (c *checker) checkModuleEffects(tree *ast.File) {
	for _, decl := range tree.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		c.checkEffectNames(d.Effects)
		c.checkDeclaredEffects(d)
		c.checkClosureEffects(d)
	}
}

// checkEffectNames rejects a row entry that names no effect. The
// vocabulary is closed — `io` and `alloc` — so a misspelling is an error
// rather than a row that silently promises less than it looks like it does.
func (c *checker) checkEffectNames(row *ast.EffectRow) {
	if row == nil {
		return
	}
	for _, name := range row.Names {
		switch name {
		case "io", "alloc":
		default:
			c.errorHintf(row.Offset, []string{
				"the effects are io and alloc; a &name entry names a borrow source",
			}, "%s is not an effect", name)
		}
	}
}

// checkClosureEffects holds every closure in the body to the row it states
// for itself. A closure without a row is unconstrained, and calling one
// still contributes nothing to its caller: both wait for effect
// polymorphism, which is what would let a row travel with a function value.
func (c *checker) checkClosureEffects(decl *ast.FuncDecl) {
	for _, closure := range closuresIn(decl.Body) {
		c.checkEffectNames(closure.Effects)
		allowed, closed := declaredEffects(closure.Effects)
		if !closed || closure.Body == nil {
			continue
		}
		reported := map[string]bool{}
		c.checkBody(closure.Body, closure.Effects, c.program.info.Calls, allowed, reported)
	}
}

func (c *checker) checkDeclaredEffects(decl *ast.FuncDecl) {
	allowed, closed := declaredEffects(decl.Effects)
	if !closed || decl.Body == nil {
		return
	}

	if len(decl.TypeParams) == 0 && len(decl.OwnerParams) == 0 {
		c.checkBody(decl.Body, decl.Effects, c.program.info.Calls, allowed, map[string]bool{})
		return
	}

	// A generic body is judged per instantiation, like its borrows: what a
	// call on a type parameter reaches is only known once T is concrete.
	// The same site can misbehave in every instantiation, so each finding
	// is reported once.
	reported := map[string]bool{}
	instances := c.program.info.Instances[decl]
	keys := make([]string, 0, len(instances))
	for key := range instances {
		keys = append(keys, key)
	}
	sortStrings(keys)
	for _, key := range keys {
		c.checkBody(decl.Body, decl.Effects, instances[key].Calls, allowed, reported)
	}
}

// checkBody reports every effect the body performs that the row does not
// name, resolving calls through the given map: the shared one normally, an
// instance's own during a generic's per-instantiation check.
func (c *checker) checkBody(body ast.Stmt, row *ast.EffectRow, calls map[*ast.CallExpr]*CallInfo, allowed effectSet, reported map[string]bool) {
	walkEffectStmt(body, func(source ast.Expr) {
		switch source := source.(type) {
		case *ast.StringLit:
			if allowed.alloc {
				return
			}
			c.reportEffect(source.Offset, reported, row, effectSet{alloc: true},
				"this string interpolation allocates, which the row does not name")
		case *ast.CallExpr:
			resolved := calls[source]
			if resolved == nil || resolved.Method == nil {
				return
			}
			performed, opaque := c.callEffects(resolved, nil)
			missing := performed.minus(allowed)
			if missing.empty() {
				return
			}
			if opaque {
				c.reportEffect(source.Offset, reported, row, missing,
					"%s is an intrinsic with no row, so it may do %s", resolved.Method.Name, everyEffect)
				return
			}
			c.reportEffect(source.Offset, reported, row, missing,
				"%s does %s, which the row does not name", resolved.Method.Name, missing)
		}
	})
}

func (c *checker) reportEffect(offset int, reported map[string]bool, row *ast.EffectRow, missing effectSet, format string, args ...any) {
	key := strconv.Itoa(offset) + "|" + missing.String()
	if reported[key] {
		return
	}
	reported[key] = true
	c.errorHintf(offset, []string{
		"name it in the row: " + suggestedRow(row, missing),
	}, format, args...)
}

// suggestedRow renders the declared row with the missing effects added, so
// the help shows the row to write rather than the entry alone.
func suggestedRow(row *ast.EffectRow, missing effectSet) string {
	declared, _ := declaredEffects(row)
	full := declared.union(missing)

	var entries []string
	if full.io {
		entries = append(entries, "io")
	}
	if full.alloc {
		entries = append(entries, "alloc")
	}
	for _, borrow := range row.Borrows {
		entries = append(entries, "&"+borrow.Name)
	}
	return "!{ " + strings.Join(entries, ", ") + " }"
}

// ---------------------------------------------------------------------------
// Inference
// ---------------------------------------------------------------------------

// effectInference is the state of one bottom-up traversal. The lattice is
// two bits, so a single pass with a cycle guard is exact for its root: a
// call back into a function still on the stack contributes nothing there,
// and that function's own effects reach the root through its own frame.
type effectInference struct {
	active map[*ast.FuncDecl]bool
	done   map[*ast.FuncDecl]effectSet
}

// effectsOf answers what a same-module function's body does. Only the root
// of a traversal is memoized program-wide: a set computed below a cut cycle
// is exact for the root but may be partial for the functions under it.
func (c *checker) effectsOf(decl *ast.FuncDecl) effectSet {
	if memoized, ok := c.program.inferredEffects[decl]; ok {
		return memoized
	}
	state := &effectInference{
		active: map[*ast.FuncDecl]bool{},
		done:   map[*ast.FuncDecl]effectSet{},
	}
	inferred := c.inferEffects(decl, state)
	c.program.inferredEffects[decl] = inferred
	return inferred
}

func (c *checker) inferEffects(decl *ast.FuncDecl, state *effectInference) effectSet {
	if memoized, ok := c.program.inferredEffects[decl]; ok {
		return memoized
	}
	if done, ok := state.done[decl]; ok {
		return done
	}
	if state.active[decl] {
		return effectSet{}
	}
	state.active[decl] = true

	var inferred effectSet
	for _, calls := range c.bodyResolutions(decl) {
		walkEffectStmt(decl.Body, func(source ast.Expr) {
			switch source := source.(type) {
			case *ast.StringLit:
				inferred.alloc = true
			case *ast.CallExpr:
				resolved := calls[source]
				if resolved == nil || resolved.Method == nil {
					return
				}
				performed, _ := c.callEffects(resolved, state)
				inferred = inferred.union(performed)
			}
		})
	}

	delete(state.active, decl)
	state.done[decl] = inferred
	return inferred
}

// bodyResolutions are the call maps a body is read through. A plain body
// resolves against the module's shared map; a generic one resolves against
// each instantiation, because a call on a type parameter has no callee
// until T is concrete. Effects are unioned over the instantiations: the
// row a caller is held to has to cover every one of them.
func (c *checker) bodyResolutions(decl *ast.FuncDecl) []map[*ast.CallExpr]*CallInfo {
	if len(decl.TypeParams) == 0 && len(decl.OwnerParams) == 0 {
		return []map[*ast.CallExpr]*CallInfo{c.program.info.Calls}
	}
	instances := c.program.info.Instances[decl]
	if len(instances) == 0 {
		return []map[*ast.CallExpr]*CallInfo{c.program.info.Calls}
	}
	keys := make([]string, 0, len(instances))
	for key := range instances {
		keys = append(keys, key)
	}
	sortStrings(keys)
	maps := make([]map[*ast.CallExpr]*CallInfo, 0, len(keys))
	for _, key := range keys {
		maps = append(maps, instances[key].Calls)
	}
	return maps
}

// callEffects is what one resolved call contributes to its caller, and
// whether the callee is opaque — promising nothing rather than promising
// what it names. A declared row answers for itself. Without one the body
// is read, wherever it lives; an intrinsic has no body to read, so a
// rowless one contributes every concrete effect. A built-in method answers
// from the fixed table.
func (c *checker) callEffects(resolved *CallInfo, state *effectInference) (effectSet, bool) {
	decl := resolved.Method.Decl
	if decl == nil {
		return builtinEffects(resolved.Method.Name), false
	}
	if declared, closed := declaredEffects(decl.Effects); closed {
		return declared, false
	}
	if decl.Body == nil {
		return everyEffect, true
	}
	if state == nil {
		return c.effectsOf(decl), false
	}
	return c.inferEffects(decl, state), false
}

// builtinEffects answers for the built-in method surface, which has no
// declaration to carry a row. runes() decodes and bytes() copies, so both
// allocate; len, close, hash, compare and message read what is there.
func builtinEffects(name string) effectSet {
	switch name {
	case "runes", "bytes":
		return effectSet{alloc: true}
	}
	return effectSet{}
}

// ---------------------------------------------------------------------------
// Walking a body for effect sources
// ---------------------------------------------------------------------------

// closuresIn collects every closure written inside a body, including the
// ones nested in another closure. Each is checked against its own row;
// none contributes to the row of the function it sits in, because a
// closure's effects happen where it is called.
func closuresIn(body ast.Stmt) []*ast.ClosureExpr {
	var found []*ast.ClosureExpr
	var collect func(stmt ast.Stmt)
	collect = func(stmt ast.Stmt) {
		walkEffectStmt(stmt, func(source ast.Expr) {
			closure, ok := source.(*ast.ClosureExpr)
			if !ok {
				return
			}
			found = append(found, closure)
			if closure.Body != nil {
				collect(closure.Body)
				return
			}
			collect(&ast.ExprStmt{X: closure.BodyExpr})
		})
	}
	collect(body)
	return found
}

// walkEffectStmt hands every effect source in a statement to visit: each
// call expression, each interpolated string and each closure. Closure
// bodies are not walked through — a closure's effects happen when it is
// called, and calls through function values contribute nothing in v1.
func walkEffectStmt(stmt ast.Stmt, visit func(source ast.Expr)) {
	switch s := stmt.(type) {
	case *ast.Block:
		if s == nil {
			return
		}
		for _, inner := range s.Stmts {
			walkEffectStmt(inner, visit)
		}
	case *ast.ExprStmt:
		walkEffectExpr(s.X, visit)
	case *ast.VarDecl:
		walkEffectExpr(s.Value, visit)
	case *ast.AssignStmt:
		for _, target := range s.Lhs {
			walkEffectExpr(target, visit)
		}
		walkEffectExpr(s.Rhs, visit)
	case *ast.ReturnStmt:
		for _, result := range s.Results {
			walkEffectExpr(result, visit)
		}
	case *ast.IfStmt:
		walkEffectExpr(s.Cond, visit)
		walkEffectStmt(s.Then, visit)
		walkEffectStmt(s.Else, visit)
	case *ast.WhileStmt:
		walkEffectExpr(s.Cond, visit)
		walkEffectStmt(s.Body, visit)
	case *ast.ForStmt:
		walkEffectStmt(s.Init, visit)
		walkEffectExpr(s.Cond, visit)
		walkEffectStmt(s.Post, visit)
		walkEffectStmt(s.Body, visit)
	case *ast.ForInStmt:
		walkEffectExpr(s.Iterable, visit)
		walkEffectStmt(s.Body, visit)
	case *ast.DeferStmt:
		walkEffectExpr(s.Call, visit)
	case *ast.SendStmt:
		walkEffectExpr(s.Chan, visit)
		walkEffectExpr(s.Value, visit)
	case *ast.SelectStmt:
		for _, arm := range s.Arms {
			walkEffectStmt(arm.Comm, visit)
			walkEffectStmt(arm.Body, visit)
		}
		walkEffectStmt(s.Else, visit)
	}
}

func walkEffectExpr(e ast.Expr, visit func(source ast.Expr)) {
	switch e := e.(type) {
	case *ast.StringLit:
		visit(e)
		for _, part := range e.Parts {
			walkEffectExpr(part.Expr, visit)
		}
	case *ast.CallExpr:
		visit(e)
		walkEffectExpr(e.Fun, visit)
		for _, arg := range e.TypeArgs {
			walkEffectExpr(arg.Expr, visit)
		}
		for _, arg := range e.Args {
			walkEffectExpr(arg.Value, visit)
		}
	case *ast.UnaryExpr:
		walkEffectExpr(e.X, visit)
	case *ast.BinaryExpr:
		walkEffectExpr(e.X, visit)
		walkEffectExpr(e.Y, visit)
	case *ast.SelectorExpr:
		walkEffectExpr(e.X, visit)
	case *ast.IndexExpr:
		walkEffectExpr(e.X, visit)
		walkEffectExpr(e.Index, visit)
	case *ast.RangeExpr:
		walkEffectExpr(e.Lo, visit)
		walkEffectExpr(e.Hi, visit)
	case *ast.TupleLit:
		for _, element := range e.Elements {
			walkEffectExpr(element, visit)
		}
	case *ast.TupleIndexExpr:
		walkEffectExpr(e.X, visit)
	case *ast.ArrayLit:
		for _, element := range e.Elements {
			walkEffectExpr(element, visit)
		}
	case *ast.StructLit:
		for _, field := range e.Fields {
			walkEffectExpr(field.Value, visit)
		}
	case *ast.MatchExpr:
		walkEffectExpr(e.Subject, visit)
		for _, arm := range e.Arms {
			walkEffectStmt(arm.Body, visit)
		}
	case *ast.BlockExpr:
		walkEffectStmt(e.Block, visit)
	case *ast.AwaitExpr:
		walkEffectExpr(e.X, visit)
	case *ast.RecvExpr:
		walkEffectExpr(e.Chan, visit)
	case *ast.ChanLit:
		walkEffectExpr(e.Cap, visit)
	case *ast.CastExpr:
		walkEffectExpr(e.X, visit)
	case *ast.ClosureExpr:
		visit(e)
	}
}
