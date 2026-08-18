package lower

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/types"
)

// ---------------------------------------------------------------------------
// match
// ---------------------------------------------------------------------------

// evalMatch lowers a match to a test chain. Enum subjects test the
// variant tag, literal patterns test equality, a type test on an error
// subject tests the %error tag. A value match writes one result
// register in every arm; the trailing abort makes the register defined
// on all falling-through paths.
func (f *funcLowerer) evalMatch(m *ast.MatchExpr, want types.Type) string {
	matchType := f.typeOfVoidOK(m)
	isValue := matchType != nil && !isVoid(matchType)
	if isValue {
		matchType = types.Default(matchType)
	}

	subjType := f.typeOf(m.Subject)
	subj := f.lowerExpr(m.Subject, nil)

	tag := ""
	if _, ok := subjType.(*types.Enum); ok {
		tag = f.newReg("tag")
		f.emit(&metals.VariantTag{Dst: tag, X: subj})
	}
	if isErrorBasic(subjType) {
		tag = f.newReg("tag")
		f.emit(&metals.VariantTag{Dst: tag, X: subj})
	}

	result := ""
	if isValue {
		result = f.newReg("match")
	}
	f.lowerArms(m.Arms, subj, subjType, tag, result, matchType)
	return result
}

func (f *funcLowerer) lowerArms(arms []ast.MatchArm, subj string, subjType types.Type, tag, result string, matchType types.Type) {
	if len(arms) == 0 {
		f.emit(&metals.Abort{Message: "no match arm matched the value"})
		return
	}
	arm := arms[0]
	cond, bind := f.armTest(subj, subjType, tag, arm.Pattern)

	runBody := func() {
		f.pushScope()
		bind()
		f.armBody(arm, result, matchType)
		f.popScope()
	}
	if cond == "" {
		runBody()
		return
	}
	then := f.nested(runBody)
	rest := f.nested(func() {
		f.lowerArms(arms[1:], subj, subjType, tag, result, matchType)
	})
	f.emit(&metals.If{Cond: cond, Then: then, Else: rest})
}

func (f *funcLowerer) armBody(arm ast.MatchArm, result string, matchType types.Type) {
	if exprStmt, ok := arm.Body.(*ast.ExprStmt); ok && result != "" {
		armType := f.typeOfVoidOK(exprStmt.X)
		if armType != nil && !isVoid(armType) {
			value := f.lowerExpr(exprStmt.X, matchType)
			f.emit(&metals.Copy{Dst: result, X: value})
			return
		}
	}
	f.stmt(arm.Body)
}

// armTest returns the arm's test register ("" when the pattern always
// matches) and the binder that declares what it captures.
func (f *funcLowerer) armTest(subj string, subjType types.Type, tag string, pattern ast.Pattern) (string, func()) {
	nothing := func() {}
	switch p := pattern.(type) {
	case *ast.WildcardPattern:
		return "", nothing

	case *ast.BindingPattern:
		declared := f.localType(p, p.Name)
		return "", func() {
			if declared != nil {
				f.newLocal(p.Name, declared, subj)
			}
		}

	case *ast.LiteralPattern:
		lit := f.lowerExpr(p.Value, types.Default(subjType))
		cond := f.newReg("is")
		f.emit(&metals.Bin{Dst: cond, Op: "eq", A: subj, B: lit})
		return cond, nothing

	case *ast.VariantPattern:
		if enum, ok := subjType.(*types.Enum); ok {
			return f.enumArmTest(subj, enum, tag, p)
		}
		if isErrorBasic(subjType) {
			return f.errorArmTest(subj, tag, p)
		}
	}
	f.l.fail("@%s: cannot lower this match pattern", f.fn.Name)
	return "", nothing
}

func (f *funcLowerer) enumArmTest(subj string, enum *types.Enum, tag string, p *ast.VariantPattern) (string, func()) {
	name := p.Path.Segments[len(p.Path.Segments)-1].Name
	index := enumVariantIndex(enum, name)
	if index < 0 {
		f.l.fail("@%s: %s has no variant %s", f.fn.Name, enum.Name, name)
	}

	bind := func() {
		for i, payload := range p.Payload {
			f.bindPayload(payload, subj, index, i)
		}
	}
	cond := f.tagEquals(tag, index)
	if !refutablePayload(p.Payload) {
		return cond, bind
	}
	return f.payloadTest(subj, enum, index, p.Payload, cond), bind
}

// refutablePayload reports whether any payload pattern tests rather than
// binds, which is what puts it in the arm's condition.
func refutablePayload(patterns []ast.Pattern) bool {
	for _, pattern := range patterns {
		if _, ok := pattern.(*ast.LiteralPattern); ok {
			return true
		}
	}
	return false
}

// payloadTest folds the payload comparisons into the arm's condition. The
// comparisons nest inside the tag test rather than joining it with an and:
// reading a payload the value does not carry is only safe once the tag
// says it does.
func (f *funcLowerer) payloadTest(subj string, enum *types.Enum, index int, patterns []ast.Pattern, tagCond string) string {
	matched := f.newReg("armMatch")
	f.emit(&metals.Const{Dst: matched, Type: metals.TBool, Value: "false"})

	payloads := enum.Variants[index].Payload
	f.emit(&metals.If{Cond: tagCond, Then: f.nested(func() {
		f.emitPayloadTests(subj, index, 0, patterns, payloads, matched)
	})})
	return matched
}

// emitPayloadTests walks the payload patterns from `from` onwards, each
// test nested inside the last, and sets matched where they all held.
func (f *funcLowerer) emitPayloadTests(subj string, index, from int, patterns []ast.Pattern, payloads []types.Type, matched string) {
	for at := from; at < len(patterns); at++ {
		literal, ok := patterns[at].(*ast.LiteralPattern)
		if !ok {
			continue
		}
		if at >= len(payloads) {
			f.l.fail("@%s: variant %d carries no payload %d", f.fn.Name, index, at)
		}

		value := f.newReg("p")
		f.emit(&metals.VariantField{Dst: value, X: subj, Variant: index, Index: at})
		want := f.lowerExpr(literal.Value, types.Default(payloads[at]))
		same := f.newReg("is")
		f.emit(&metals.Bin{Dst: same, Op: "eq", A: value, B: want})

		f.emit(&metals.If{Cond: same, Then: f.nested(func() {
			f.emitPayloadTests(subj, index, at+1, patterns, payloads, matched)
		})})
		return
	}
	f.emit(&metals.Const{Dst: matched, Type: metals.TBool, Value: "true"})
}

// errorArmTest is the type test: does this error hold a RangeError?
func (f *funcLowerer) errorArmTest(subj, tag string, p *ast.VariantPattern) (string, func()) {
	name := p.Path.Segments[len(p.Path.Segments)-1].Name
	var strct *types.Struct
	for _, candidate := range f.l.errTypes {
		if candidate.Name == name {
			if strct != nil {
				f.l.fail("@%s: two error types are named %s; the type test is ambiguous", f.fn.Name, name)
			}
			strct = candidate
		}
	}
	if strct == nil {
		f.l.fail("@%s: no error type %s", f.fn.Name, name)
	}
	index := f.l.errorVariantOf(strct)
	cond := f.tagEquals(tag, index)
	return cond, func() {
		if len(p.Payload) == 1 {
			f.bindPayload(p.Payload[0], subj, index, 0)
		}
	}
}

func (f *funcLowerer) tagEquals(tag string, index int) string {
	want := f.newReg("k")
	f.emit(&metals.Const{Dst: want, Type: metals.TInt32, Value: itoa(index)})
	cond := f.newReg("is")
	f.emit(&metals.Bin{Dst: cond, Op: "eq", A: tag, B: want})
	return cond
}

func (f *funcLowerer) bindPayload(pattern ast.Pattern, subj string, variant, index int) {
	switch pattern := pattern.(type) {
	case *ast.WildcardPattern:
		return
	case *ast.LiteralPattern:
		// Already in the arm's condition; there is nothing to bind.
		return
	case *ast.BindingPattern:
		declared := f.localType(pattern, pattern.Name)
		if declared == nil {
			return
		}
		value := f.newReg(pattern.Name + ".v")
		f.emit(&metals.VariantField{Dst: value, X: subj, Variant: variant, Index: index})
		f.newLocal(pattern.Name, declared, value)
		return
	}
	f.l.fail("@%s: only names and _ can bind a payload", f.fn.Name)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	if negative {
		return "-" + digits
	}
	return digits
}

// ---------------------------------------------------------------------------
// for .. in
// ---------------------------------------------------------------------------

func (f *funcLowerer) forInStmt(s *ast.ForInStmt) {
	elemType := f.localType(s, s.Name)
	if elemType == nil {
		f.l.fail("@%s: no recorded element type for a for..in", f.fn.Name)
	}
	if rangeExpr, ok := s.Iterable.(*ast.RangeExpr); ok {
		f.rangeLoop(s, rangeExpr, elemType)
		return
	}
	switch iterType := f.typeOf(s.Iterable).(type) {
	case *types.Slice, *types.Array:
		f.indexLoop(s, elemType)
	case *types.Chan:
		f.chanLoop(s, elemType)
	case *types.Struct:
		f.iteratorLoop(s, iterType, elemType)
	default:
		f.l.fail("@%s: cannot iterate a %s", f.fn.Name, iterType)
	}
}

// loopShell emits the block/loop skeleton every for..in shares. test
// runs first each iteration (and jumps to break to stop), bindAndBody
// declares the element and runs the body, advance runs after continue's
// target.
func (f *funcLowerer) loopShell(test func(breakLabel string), body *ast.Block, bind func(), advance func()) {
	breakLabel := f.newLabel("break.")
	topLabel := f.newLabel("loop.")
	contLabel := f.newLabel("cont.")

	loopBody := f.nested(func() {
		test(breakLabel)
		f.loops = append(f.loops, loopContext{
			breakLabel: breakLabel, contLabel: contLabel, depth: len(f.scopes),
		})
		inner := f.nested(func() {
			f.pushScope()
			bind()
			f.blockInPlace(body)
			f.popScope()
		})
		f.loops = f.loops[:len(f.loops)-1]
		f.emit(&metals.Block{Label: contLabel, Body: inner})
		if advance != nil {
			advance()
		}
		f.emit(&metals.Br{Label: topLabel})
	})
	f.emit(&metals.Block{Label: breakLabel, Body: []metals.Instr{
		&metals.Loop{Label: topLabel, Body: loopBody},
	}})
}

func (f *funcLowerer) rangeLoop(s *ast.ForInStmt, rangeExpr *ast.RangeExpr, elemType types.Type) {
	low := f.lowerExpr(rangeExpr.Lo, elemType)
	high := f.lowerExpr(rangeExpr.Hi, elemType)
	induction := f.newReg("i")
	f.emit(&metals.Copy{Dst: induction, X: low})

	f.loopShell(func(breakLabel string) {
		stop := f.newReg("stop")
		f.emit(&metals.Bin{Dst: stop, Op: "ge", A: induction, B: high})
		f.emit(&metals.BrIf{Cond: stop, Label: breakLabel})
	}, s.Body, func() {
		f.newLocal(s.Name, elemType, induction)
	}, func() {
		one := f.newReg("one")
		f.emit(&metals.Const{Dst: one, Type: f.l.metalsType(elemType), Value: "1"})
		f.emit(&metals.Bin{Dst: induction, Op: "iadd", A: induction, B: one})
	})
}

func (f *funcLowerer) indexLoop(s *ast.ForInStmt, elemType types.Type) {
	sequence := f.lowerExpr(s.Iterable, nil)
	length := f.newReg("n")
	f.emit(&metals.Len{Dst: length, X: sequence})
	index := f.newReg("i")
	f.emit(&metals.Const{Dst: index, Type: metals.TUsize, Value: "0"})

	element := ""
	f.loopShell(func(breakLabel string) {
		stop := f.newReg("stop")
		f.emit(&metals.Bin{Dst: stop, Op: "ge", A: index, B: length})
		f.emit(&metals.BrIf{Cond: stop, Label: breakLabel})
		element = f.newReg("elem")
		f.emit(&metals.IndexGet{Dst: element, X: sequence, Index: index})
	}, s.Body, func() {
		f.newLocal(s.Name, elemType, element)
	}, func() {
		one := f.newReg("one")
		f.emit(&metals.Const{Dst: one, Type: metals.TUsize, Value: "1"})
		f.emit(&metals.Bin{Dst: index, Op: "iadd", A: index, B: one})
	})
}

func (f *funcLowerer) chanLoop(s *ast.ForInStmt, elemType types.Type) {
	channel := f.lowerExpr(s.Iterable, nil)
	element := ""
	f.loopShell(func(breakLabel string) {
		pair := f.newReg("recv")
		f.emit(&metals.Recv{Dst: pair, Ch: channel})
		open := f.newReg("open")
		f.emit(&metals.TupleGet{Dst: open, X: pair, Index: 1})
		closed := f.newReg("closed")
		f.emit(&metals.Un{Dst: closed, Op: "not", X: open})
		f.emit(&metals.BrIf{Cond: closed, Label: breakLabel})
		element = f.newReg("elem")
		f.emit(&metals.TupleGet{Dst: element, X: pair, Index: 0})
	}, s.Body, func() {
		f.newLocal(s.Name, elemType, element)
	}, nil)
}

// iteratorLoop drives the duck-typed contract: next(*self): (T, bool),
// reached directly or through iter().
func (f *funcLowerer) iteratorLoop(s *ast.ForInStmt, strct *types.Struct, elemType types.Type) {
	target := strct
	value := f.lowerExpr(s.Iterable, nil)

	next := methodOn(target, "next")
	if next == nil {
		iter := methodOn(target, "iter")
		if iter == nil || iter.Decl == nil {
			f.l.fail("@%s: %s has no next or iter method", f.fn.Name, strct)
		}
		iterName := f.l.ensureFunc(iter.Decl, ownerBindings(iter.Decl, target))
		out := f.newReg("iter")
		f.emit(&metals.Call{Dst: out, Callee: iterName, Args: []string{value}})
		value = out
		iterated, ok := iter.Sig.Result.(*types.Struct)
		if !ok {
			f.l.fail("@%s: iter() must return a struct iterator", f.fn.Name)
		}
		target = iterated
		next = methodOn(target, "next")
	}
	if next == nil || next.Decl == nil {
		f.l.fail("@%s: %s has no next method", f.fn.Name, target)
	}

	cell := f.newReg("it")
	f.emit(&metals.Slot{Dst: cell, Type: f.l.metalsType(target)})
	f.emit(&metals.Store{Ptr: cell, Val: value})
	nextName := f.l.ensureFunc(next.Decl, ownerBindings(next.Decl, target))

	element := ""
	f.loopShell(func(breakLabel string) {
		pair := f.newReg("step")
		f.emit(&metals.Call{Dst: pair, Callee: nextName, Args: []string{cell}})
		open := f.newReg("more")
		f.emit(&metals.TupleGet{Dst: open, X: pair, Index: 1})
		done := f.newReg("done")
		f.emit(&metals.Un{Dst: done, Op: "not", X: open})
		f.emit(&metals.BrIf{Cond: done, Label: breakLabel})
		element = f.newReg("elem")
		f.emit(&metals.TupleGet{Dst: element, X: pair, Index: 0})
	}, s.Body, func() {
		f.newLocal(s.Name, elemType, element)
	}, nil)
}

func methodOn(t *types.Struct, name string) *types.Method {
	origin := t
	if t.Origin != nil {
		origin = t.Origin
	}
	return origin.Methods[name]
}
