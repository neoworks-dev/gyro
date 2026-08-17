package sema

import (
	"fmt"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// block checks a block in a fresh scope.
func (c *checker) block(block *ast.Block) {
	c.pushScope(func() {
		c.blockInPlace(block)
	})
}

// blockInPlace checks a block's statements in the current scope. Function
// bodies use it so parameters and locals share the function scope.
func (c *checker) blockInPlace(block *ast.Block) {
	c.pushAllocScope(func() {
		for _, stmt := range block.Stmts {
			c.stmt(stmt)
		}
	})
}

func (c *checker) stmt(stmt ast.Stmt) {
	switch s := stmt.(type) {
	case *ast.VarDecl:
		c.localVar(s)

	case *ast.Block:
		c.block(s)

	case *ast.ExprStmt:
		c.exprStmt(s)

	case *ast.AssignStmt:
		c.assignStmt(s)

	case *ast.ReturnStmt:
		c.returnStmt(s)

	case *ast.IfStmt:
		c.ifStmt(s)

	case *ast.WhileStmt:
		facts := c.condition(s.Cond)
		c.pushLoop(isAlwaysTrue(s.Cond), func() {
			c.withFacts(facts.then, func() {
				c.block(s.Body)
			})
		})

	case *ast.ForStmt:
		// The init clause scopes over the condition, post and body.
		c.pushScope(func() {
			if s.Init != nil {
				c.stmt(s.Init)
			}
			var facts condFacts
			if s.Cond != nil {
				facts = c.condition(s.Cond)
			}
			// A header with no condition never falls out of the loop.
			c.pushLoop(s.Cond == nil || isAlwaysTrue(s.Cond), func() {
				c.withFacts(facts.then, func() {
					if s.Post != nil {
						c.stmt(s.Post)
					}
					c.blockInPlace(s.Body)
				})
			})
		})

	case *ast.ForInStmt:
		elem := c.iterableElem(s.Iterable)
		c.pushScope(func() {
			c.declareValue(s.Name, s.Offset, s, elem)
			c.pushLoop(false, func() {
				c.blockInPlace(s.Body)
			})
		})

	case *ast.DeferStmt:
		// The parser already guarantees this is a call, and checking it
		// settles the allocations it is handed: a deferred call runs at
		// block exit, which is exactly where the leak analysis looks.
		c.expr(s.Call)

	case *ast.SendStmt:
		c.sendStmt(s)

	case *ast.SelectStmt:
		c.selectStmt(s)

	case *ast.BranchStmt:
		if s.Kind == token.Break {
			c.recordBreak()
			return
		}
		// `continue` goes back to the loop head, so nothing below it runs.
		c.flow.reachable = false
	}
}

// selectStmt runs exactly one arm, so what survives is what every arm
// assigned. Without an `else` the statement blocks until an arm is ready,
// which is still exactly one arm.
func (c *checker) selectStmt(s *ast.SelectStmt) {
	var merged flow
	first := true

	for _, arm := range s.Arms {
		armFlow := c.branchFlow(func() {
			// The arm's binding is visible to its own body and nowhere else.
			c.pushScope(func() {
				c.stmt(arm.Comm)
				c.stmt(arm.Body)
			})
		})
		if first {
			merged, first = armFlow, false
			continue
		}
		merged = mergeFlow(merged, armFlow)
	}

	if s.Else != nil {
		elseFlow := c.branchFlow(func() { c.stmt(s.Else) })
		if first {
			merged, first = elseFlow, false
		} else {
			merged = mergeFlow(merged, elseFlow)
		}
	}

	if !first {
		c.flow = merged
	}
}

// isAlwaysTrue recognizes the loop header that cannot fall through, so that
// `while true { x = 1; break }` leaves x assigned.
func isAlwaysTrue(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.True
}

// exprStmt types a discarded expression and enforces the two values that
// must not be dropped silently: errors and un-awaited async results.
func (c *checker) exprStmt(s *ast.ExprStmt) {
	t := c.expr(s.X)
	if containsError(t) {
		c.errorf(s.X.Pos(), "the error result is dropped here; discarding one must be written out: var _, _ = ...")
		return
	}
	if _, ok := t.(*types.Future); ok {
		c.errorf(s.X.Pos(), "this async call does nothing until it is awaited")
	}
}

func containsError(t types.Type) bool {
	switch t := t.(type) {
	case *types.Basic:
		return t.Kind == types.ErrorKind
	case *types.Optional:
		return containsError(t.Elem)
	case *types.Tuple:
		for _, elem := range t.Elems {
			if containsError(elem) {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Declarations and assignment
// ---------------------------------------------------------------------------

// localVar checks a local declaration. The initializer is resolved before
// the names are bound, so `var x = x` refers to an outer x or fails.
func (c *checker) localVar(decl *ast.VarDecl) {
	var declared []*symbol
	c.varTypes(decl, func(binding ast.Binding, typ types.Type) {
		sym := c.declareValue(binding.Name, binding.Offset, decl, typ)
		c.noteOrigin(sym, decl.Value, typ)
		declared = append(declared, sym)
	})

	// A declaration with no value leaves its names holding nothing.
	if decl.Value == nil && !decl.Const {
		for _, sym := range declared {
			if sym != nil {
				sym.needsAssign = true
			}
		}
		return
	}

	// `var v, open = <-ch` binds a value that only exists when open is true.
	if _, ok := decl.Value.(*ast.RecvExpr); ok && len(decl.Names) == 2 && len(declared) == 2 {
		c.receiveGuard(declared[0], declared[1], decl.Names[1].Name)
	}

	// `var node = alloc<Node>()` makes this block responsible for it.
	if c.allocationCall(decl.Value) && len(declared) == 1 && declared[0] != nil {
		declared[0].holdsAllocation = true
		c.trackAllocation(declared[0], decl.Offset, decl.Names[0].Name)
		return
	}

	// Any other initializer that is a bare name hands the allocation over.
	c.settleEscape(decl.Value)
}

// varTypes computes the type of each bound name in a var or const
// declaration, checks the initializer against the annotation, and hands
// every binding to bind. Module scope and locals share this; they differ
// only in where the names go.
func (c *checker) varTypes(decl *ast.VarDecl, bind func(ast.Binding, types.Type)) {
	var annotation types.Type
	if decl.Type != nil {
		annotation = c.resolveType(decl.Type)
	}

	if decl.Value == nil {
		if annotation == nil {
			c.errorf(decl.Offset, "a declaration needs a type or a value")
			annotation = types.Unknown
		}
		if decl.Const {
			c.errorf(decl.Offset, "a const needs its value here")
		}
		for _, binding := range decl.Names {
			bind(binding, annotation)
		}
		return
	}

	if len(decl.Names) <= 1 {
		var t types.Type = types.Unknown
		if annotation != nil {
			c.assignExpr(decl.Value, annotation, "the declaration of "+bindingName(decl))
			t = annotation
		} else {
			t = types.Default(c.expr(decl.Value))
		}
		for _, binding := range decl.Names {
			bind(binding, t)
		}
		return
	}

	c.destructure(decl, annotation, bind)
}

func bindingName(decl *ast.VarDecl) string {
	if len(decl.Names) == 0 {
		return "_"
	}
	return decl.Names[0].Name
}

// destructure types the multi-name form: a two-name receive reporting
// whether the channel is open, or a tuple split across the names.
func (c *checker) destructure(decl *ast.VarDecl, annotation types.Type, bind func(ast.Binding, types.Type)) {
	if recv, ok := decl.Value.(*ast.RecvExpr); ok && len(decl.Names) == 2 {
		if annotation != nil {
			c.errorf(decl.Offset, "a receive binding needs no annotation; it is (element, open)")
		}
		elem := c.exprWith(recv, nil)
		bind(decl.Names[0], elem)
		bind(decl.Names[1], types.Bool)
		return
	}

	value := c.exprWith(decl.Value, annotation)
	if annotation != nil {
		c.assignValue(decl.Value.Pos(), decl.Value, value, annotation, "the declaration")
		value = annotation
	}

	tuple, ok := value.(*types.Tuple)
	if !ok {
		if !flexible(value) {
			c.errorf(decl.Offset, "destructuring %d names needs a tuple, not %s", len(decl.Names), describeType(value))
		}
		for _, binding := range decl.Names {
			bind(binding, types.Unknown)
		}
		return
	}

	if len(tuple.Elems) != len(decl.Names) {
		c.errorf(decl.Offset, "%s has %d elements, the declaration binds %d names", tuple, len(tuple.Elems), len(decl.Names))
	}
	for i, binding := range decl.Names {
		if i < len(tuple.Elems) {
			bind(binding, types.Default(tuple.Elems[i]))
		} else {
			bind(binding, types.Unknown)
		}
	}
}

func (c *checker) assignStmt(s *ast.AssignStmt) {
	if s.Op != token.Assign {
		c.compoundAssign(s)
		return
	}

	if len(s.Lhs) > 1 {
		c.destructuringAssign(s)
		return
	}

	target := c.assignTarget(s.Lhs[0])
	if target == nil {
		c.expr(s.Rhs)
		return
	}
	c.assignExpr(s.Rhs, target, "assignment")
	c.killFactsFor(s.Lhs[0])
	c.markAssignedTarget(s.Lhs[0])
}

// assignTarget types the left side of an assignment and checks that it is
// a place a value can go. nil means "discard": the blank identifier.
//
// A named target types as what it was declared, not as its narrowed view:
// assigning a fresh *Node? into a narrowed *Node? local is fine, and the
// write is what withdraws the narrowing afterwards.
func (c *checker) assignTarget(lhs ast.Expr) types.Type {
	switch lhs := lhs.(type) {
	case *ast.Ident:
		if lhs.Name == "_" {
			return nil
		}
		sym := c.resolveIdent(lhs)
		if sym == nil {
			return types.Unknown
		}
		if sym.kind != symValue {
			c.errorf(lhs.Offset, "%s is not a value; it cannot be assigned", lhs.Name)
			return types.Unknown
		}
		if sym.typ == nil {
			return types.Unknown
		}
		return sym.typ
	case *ast.SelectorExpr, *ast.IndexExpr, *ast.TupleIndexExpr:
		return c.expr(lhs)
	case *ast.UnaryExpr:
		if lhs.Op == token.Star {
			return c.expr(lhs)
		}
	}
	c.errorf(lhs.Pos(), "cannot assign to this expression")
	c.expr(lhs)
	return types.Unknown
}

func (c *checker) destructuringAssign(s *ast.AssignStmt) {
	targets := make([]types.Type, len(s.Lhs))
	for i, lhs := range s.Lhs {
		targets[i] = c.assignTarget(lhs)
	}

	if recv, ok := s.Rhs.(*ast.RecvExpr); ok && len(s.Lhs) == 2 {
		elem := c.exprWith(recv, nil)
		c.assignPart(s.Lhs[0], elem, targets[0])
		c.assignPart(s.Lhs[1], types.Bool, targets[1])
		return
	}

	value := c.expr(s.Rhs)
	tuple, ok := value.(*types.Tuple)
	if !ok {
		if !flexible(value) {
			c.errorf(s.Offset, "destructuring %d targets needs a tuple, not %s", len(s.Lhs), describeType(value))
		}
		return
	}
	if len(tuple.Elems) != len(s.Lhs) {
		c.errorf(s.Offset, "%s has %d elements, the assignment has %d targets", tuple, len(tuple.Elems), len(s.Lhs))
		return
	}
	for i, lhs := range s.Lhs {
		c.assignPart(lhs, tuple.Elems[i], targets[i])
	}
}

func (c *checker) assignPart(lhs ast.Expr, value, target types.Type) {
	if target == nil {
		return
	}
	c.markAssignedTarget(lhs)
	if !c.assignableType(value, target) {
		c.errorLabelf(lhs.Pos(), fmt.Sprintf("expected %s", target),
			"cannot use %s as %s in assignment", describeType(value), target)
	}
	c.killFactsFor(lhs)
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

func (c *checker) compoundAssign(s *ast.AssignStmt) {
	if len(s.Lhs) != 1 {
		c.errorf(s.Offset, "a compound assignment takes one target")
		return
	}
	// A compound assignment reads its target before it writes it, and
	// assignTarget only checks that the target is a place.
	c.checkAssignedTarget(s.Lhs[0])

	target := c.assignTarget(s.Lhs[0])
	if target == nil {
		c.errorf(s.Offset, "_ discards a value; there is nothing to update")
		c.expr(s.Rhs)
		return
	}
	c.markAssignedTarget(s.Lhs[0])

	op := compoundOps[s.Op]
	value := c.exprWith(s.Rhs, target)
	value = c.adaptUntyped(s.Rhs, value, target)

	if flexible(target) || flexible(value) {
		return
	}

	switch op {
	case token.Shl, token.Shr:
		if !types.IsInteger(target) {
			c.errorf(s.Offset, "operator %s shifts integers, not %s", s.Op, target)
		}
		if !types.IsInteger(value) {
			c.errorf(s.Rhs.Pos(), "a shift count is an integer, not %s", describeType(value))
		}
	default:
		valid := types.IsNumeric(target)
		if isIntegerOnlyOp(op) {
			valid = types.IsInteger(target)
		}
		if !valid || !types.Identical(target, value) {
			c.errorHintf(s.Offset, compoundHelp(isIntegerOnlyOp(op), target, value),
				"operator %s needs matching numeric operands, got %s and %s",
				s.Op, describeType(target), describeType(value))
		}
	}
	c.killFactsFor(s.Lhs[0])
}

func (c *checker) returnStmt(s *ast.ReturnStmt) {
	// The results are read before the function leaves; nothing below runs.
	// Returning an allocation hands it to the caller, and whatever the
	// return does not carry is lost right here.
	c.settleEscapes(s.Results)
	for _, result := range s.Results {
		c.checkReturnedBorrow(result, result.Pos())
	}
	defer func() {
		c.checkLeaksAtExit()
		c.flow.reachable = false
	}()

	if c.fn == nil {
		for _, result := range s.Results {
			c.expr(result)
		}
		return
	}
	want := c.fn.result

	switch len(s.Results) {
	case 0:
		if !flexible(want) && !types.Identical(want, types.Void) {
			c.errorf(s.Offset, "this function returns %s; the return gives nothing", want)
		}
	case 1:
		if types.Identical(want, types.Void) {
			t := c.expr(s.Results[0])
			if !flexible(t) && !types.Identical(t, types.Void) {
				c.errorf(s.Offset, "this function returns nothing; the value has no place to go")
			}
			return
		}
		c.assignExpr(s.Results[0], want, "return")
	default:
		tuple, ok := want.(*types.Tuple)
		if !ok || len(tuple.Elems) != len(s.Results) {
			for _, result := range s.Results {
				c.expr(result)
			}
			if !flexible(want) {
				c.errorf(s.Offset, "this function returns %s; the return gives %d values", want, len(s.Results))
			}
			return
		}
		for i, result := range s.Results {
			c.assignExpr(result, tuple.Elems[i], "return")
		}
	}
}

func (c *checker) sendStmt(s *ast.SendStmt) {
	// A sent allocation belongs to whoever receives it.
	c.settleEscape(s.Value)

	operand := c.expr(s.Chan)
	channel, ok := operand.(*types.Chan)
	if !ok {
		c.expr(s.Value)
		if !flexible(operand) {
			c.errorf(s.Offset, "send needs a channel, not %s", operand)
		}
		return
	}
	c.assignExpr(s.Value, channel.Elem, "the send")
}

// ---------------------------------------------------------------------------
// Conditions and narrowing
// ---------------------------------------------------------------------------

// condFacts is what a condition proves: the symbols known non-null when it
// is true, and when it is false, plus the two-name receive values each side
// makes readable. Both ride the same &&/||/! plumbing.
type condFacts struct {
	then []*symbol
	els  []*symbol
	// thenAssigned and elsAssigned hold the values of a two-name receive
	// whose `open` flag this condition settles.
	thenAssigned []*symbol
	elsAssigned  []*symbol
}

// condition types a boolean condition and extracts narrowing facts from
// null comparisons, following && || and !.
func (c *checker) condition(e ast.Expr) condFacts {
	switch e := e.(type) {
	case *ast.BinaryExpr:
		switch e.Op {
		case token.AndAnd:
			left := c.condition(e.X)
			var right condFacts
			c.withFacts(left.then, func() {
				right = c.condition(e.Y)
			})
			return condFacts{
				then:         append(left.then, right.then...),
				thenAssigned: append(left.thenAssigned, right.thenAssigned...),
			}
		case token.OrOr:
			left := c.condition(e.X)
			right := c.condition(e.Y)
			return condFacts{
				els:         append(left.els, right.els...),
				elsAssigned: append(left.elsAssigned, right.elsAssigned...),
			}
		case token.Eq, token.NotEq:
			c.equality(e)
			if sym := c.nullCheckedSymbol(e); sym != nil {
				if e.Op == token.NotEq {
					return condFacts{then: []*symbol{sym}}
				}
				return condFacts{els: []*symbol{sym}}
			}
			return condFacts{}
		}
	case *ast.UnaryExpr:
		if e.Op == token.Bang {
			inner := c.condition(e.X)
			return condFacts{
				then:         inner.els,
				els:          inner.then,
				thenAssigned: inner.elsAssigned,
				elsAssigned:  inner.thenAssigned,
			}
		}
	}

	// A bare `open` settles the value bound beside it.
	guarded := c.guardedByCondition(e)

	t := c.expr(e)
	if !flexible(t) && !types.Identical(t, types.Bool) {
		c.errorf(e.Pos(), "a condition is a bool, not %s", describeType(t))
	}
	return condFacts{thenAssigned: guarded}
}

// nullCheckedSymbol recognizes `x != null` and `x == null` where x is a
// nullable local, which is what narrowing tracks.
func (c *checker) nullCheckedSymbol(e *ast.BinaryExpr) *symbol {
	operand := e.X
	if isNullLiteral(e.X) {
		operand = e.Y
	} else if !isNullLiteral(e.Y) {
		return nil
	}

	ident, ok := operand.(*ast.Ident)
	if !ok {
		return nil
	}
	sym := c.scope.lookup(ident.Name)
	if sym == nil || sym.kind != symValue {
		return nil
	}
	if _, ok := sym.typ.(*types.Optional); !ok {
		return nil
	}
	return sym
}

func isNullLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.Null
}

// withFacts runs fn with extra non-null facts, then withdraws the ones
// that were new.
func (c *checker) withFacts(facts []*symbol, fn func()) {
	var added []*symbol
	for _, sym := range facts {
		if !c.facts[sym] {
			c.facts[sym] = true
			added = append(added, sym)
		}
	}
	fn()
	for _, sym := range added {
		delete(c.facts, sym)
	}
}

// persistFacts records facts that hold from here on: the branch that would
// contradict them cannot fall through.
func (c *checker) persistFacts(facts []*symbol) {
	for _, sym := range facts {
		c.facts[sym] = true
	}
}

// killFactsFor withdraws narrowing on an assigned name; the new value may
// be null again.
func (c *checker) killFactsFor(e ast.Expr) {
	ident, ok := e.(*ast.Ident)
	if !ok {
		return
	}
	if sym := c.scope.lookup(ident.Name); sym != nil {
		delete(c.facts, sym)
	}
}

func (c *checker) ifStmt(s *ast.IfStmt) {
	facts := c.condition(s.Cond)

	thenFlow := c.branchFlow(func() {
		c.assumeAssigned(facts.thenAssigned)
		c.withFacts(facts.then, func() {
			c.block(s.Then)
		})
	})

	elseFlow := c.branchFlow(func() {
		c.assumeAssigned(facts.elsAssigned)
		if s.Else == nil {
			return
		}
		c.withFacts(facts.els, func() {
			c.stmt(s.Else)
		})
	})

	c.flow = mergeFlow(thenFlow, elseFlow)

	// An early exit makes the surviving branch's facts permanent:
	// `if x == null { return }` leaves x narrowed below.
	thenExits := blockTerminates(s.Then)
	elseExits := s.Else != nil && stmtTerminates(s.Else)
	if thenExits && !elseExits {
		c.persistFacts(facts.els)
	}
	if elseExits && !thenExits {
		c.persistFacts(facts.then)
	}
}

func blockTerminates(b *ast.Block) bool {
	if len(b.Stmts) == 0 {
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

// ---------------------------------------------------------------------------
// Iteration
// ---------------------------------------------------------------------------

// iterableElem types a for..in iterable and returns the element the loop
// variable takes.
func (c *checker) iterableElem(e ast.Expr) types.Type {
	if rng, ok := e.(*ast.RangeExpr); ok {
		return c.rangeElem(rng)
	}

	t := c.expr(e)
	switch t := t.(type) {
	case *types.Slice:
		return t.Elem
	case *types.Array:
		return t.Elem
	case *types.Chan:
		return t.Elem
	case *types.Optional:
		c.errorf(e.Pos(), "this value is %s and may be null; check it against null before iterating", t)
		return types.Unknown
	}
	if flexible(t) {
		return types.Unknown
	}
	if basic, ok := t.(*types.Basic); ok && basic.Kind == types.StringKind {
		c.errorf(e.Pos(), "a string does not iterate directly; use .runes() for code points")
		return types.Unknown
	}

	if elem := c.duckIterableElem(t); elem != nil {
		return elem
	}
	c.errorf(e.Pos(), "%s is not iterable; for..in takes slices, arrays, ranges, channels, or a type with next(*self): (T, bool)", t)
	return types.Unknown
}

func (c *checker) rangeElem(rng *ast.RangeExpr) types.Type {
	lo := c.expr(rng.Lo)
	hi := c.expr(rng.Hi)

	if types.IsUntyped(lo) && !types.IsUntyped(hi) {
		lo = c.adaptUntyped(rng.Lo, lo, hi)
	} else if types.IsUntyped(hi) && !types.IsUntyped(lo) {
		hi = c.adaptUntyped(rng.Hi, hi, lo)
	}

	if flexible(lo) || flexible(hi) {
		return types.Unknown
	}
	lo = types.Default(lo)
	hi = types.Default(hi)
	if !types.IsInteger(lo) || !types.Identical(lo, hi) {
		c.errorf(rng.Offset, "a range needs matching integer endpoints, got %s and %s", describeType(lo), describeType(hi))
		return types.Unknown
	}
	return lo
}

// duckIterableElem implements the iteration contract: next(*self) returning
// (T, bool), or iter() returning something that has it.
func (c *checker) duckIterableElem(t types.Type) types.Type {
	if elem := c.nextMethodElem(t); elem != nil {
		return elem
	}

	iter, s := c.lookupMethod(t, "iter")
	if iter == nil || len(iter.Sig.Params) != 0 || len(iter.TypeParams) > 0 {
		return nil
	}
	return c.nextMethodElem(c.substType(iter.Sig.Result, s))
}

func (c *checker) nextMethodElem(t types.Type) types.Type {
	next, s := c.lookupMethod(t, "next")
	if next == nil || len(next.Sig.Params) != 0 || len(next.TypeParams) > 0 {
		return nil
	}
	result, ok := c.substType(next.Sig.Result, s).(*types.Tuple)
	if !ok || len(result.Elems) != 2 || !types.Identical(result.Elems[1], types.Bool) {
		return nil
	}
	return result.Elems[0]
}
