package cc

import "github.com/gyro-lang/gyro/internal/metals"

// Statements. Every loop is the same shape, and it is the shape METALS'
// structured control flow gives it:
//
//	block $break { loop $top { <test>  block $continue { <body> }  <post>  br $top } }
//
// `break` leaves the outer block and `continue` leaves the inner one,
// which lands on the post-expression — that is why the body is wrapped
// rather than the whole loop, and it is what makes `continue` in a `for`
// still run the increment.

func (f *funcLowerer) stmt(s stmt) {
	switch s := s.(type) {
	case *blockStmt:
		f.pushScope()
		for _, inner := range s.stmts {
			f.stmt(inner)
		}
		f.popScope()

	case *emptyStmt:

	case *declStmt:
		f.declare(s)

	case *exprStmt:
		f.expr(s.x)

	case *returnStmt:
		f.returnStmt(s)

	case *ifStmt:
		f.ifStmt(s)

	case *whileStmt:
		f.loop(nil, s.cond, nil, s.body)

	case *forStmt:
		// The init clause may declare, and its name is scoped to the loop.
		f.pushScope()
		f.loop(s.init, s.cond, s.post, s.body)
		f.popScope()

	case *doStmt:
		f.doStmt(s)

	case *switchStmt:
		f.switchStmt(s)

	case *caseStmt:
		fail(s.pos, "a case label is not inside a switch")

	case *defaultStmt:
		fail(s.pos, "a default label is not inside a switch")

	case *breakStmt:
		if len(f.loops) == 0 {
			fail(s.pos, "break is not inside a loop")
		}
		f.emit(&metals.Br{Label: f.loops[len(f.loops)-1].brk})

	case *continueStmt:
		if len(f.loops) == 0 {
			fail(s.pos, "continue is not inside a loop")
		}
		f.emit(&metals.Br{Label: f.loops[len(f.loops)-1].cont})
	}
}

func (f *funcLowerer) declare(s *declStmt) {
	for _, v := range s.vars {
		if v.static {
			f.declareStatic(v)
			continue
		}
		if !v.typ.isComplete() {
			v.typ = f.l.sizeFromInit(&globalDecl{name: v.name, typ: v.typ, init: v.init, pos: v.pos})
		}

		cell := f.reserve(v.name)
		f.emit(&metals.Slot{Dst: cell, Type: v.typ.metalsType()})

		// The name enters scope after its own initializer, which is C's
		// rule: `int x = x;` reads the outer x, not this one. It is also
		// why the declarators of one statement enter one at a time.
		if v.init != nil {
			// A local's storage is a fresh slot, which the native
			// backends zero and the evaluator deliberately leaves
			// unwritten. What an initializer leaves out is zero in C, so
			// an aggregate says so rather than depending on the tier —
			// `char buf[6] = "ab"` has four of them at the end.
			if v.typ.isArray() || v.typ.isMembered() {
				f.zeroObject(cell, v.typ, v.pos)
			}
			f.initialize(cell, v.typ, v.init)
		}
		f.scopes[len(f.scopes)-1][v.name] = &binding{ptr: cell, typ: v.typ}
	}
}

// declareStatic gives a local module storage. The name is block-scoped
// and the storage is not — it is written once before main and keeps its
// value across every call — so the initializer is a global's, not a
// statement, which is also why C only lets it be a constant expression.
//
// The symbol is qualified by the function, because two functions may
// each declare a `static int count` and they are two objects.
func (f *funcLowerer) declareStatic(v *varDecl) {
	g := &globalDecl{
		name:   v.name,
		symbol: f.l.staticSymbol(f.fn.symbol, v.name),
		typ:    v.typ,
		init:   v.init,
		static: true,
		pos:    v.pos,
	}
	f.l.declareGlobal(g)
	f.scopes[len(f.scopes)-1][v.name] = &binding{typ: g.typ, symbol: g.symbol}
}

func (f *funcLowerer) returnStmt(s *returnStmt) {
	if s.x == nil {
		if !f.fn.result.isVoid() {
			fail(s.pos, "%s returns %s, and this return carries nothing", f.fn.name, f.fn.result)
		}
		f.emit(&metals.Return{})
		return
	}
	if f.fn.result.isVoid() {
		fail(s.pos, "%s returns void, and this return carries a value", f.fn.name)
	}
	v := f.convert(f.exprAs(s.x, f.fn.result), f.fn.result, s.pos)
	f.emit(&metals.Return{Val: v.reg})
}

func (f *funcLowerer) ifStmt(s *ifStmt) {
	cond := f.condition(s.cond)

	f.open()
	f.stmt(s.then)
	then := f.close()

	var els []metals.Instr
	if s.els != nil {
		f.open()
		f.stmt(s.els)
		els = f.close()
	}
	f.emit(&metals.If{Cond: cond, Then: then, Else: els})
}

// loop is while and for together: a while is a for with no init and no
// post, and writing it once keeps the two from drifting.
func (f *funcLowerer) loop(init stmt, cond expr, post expr, body stmt) {
	if init != nil {
		f.stmt(init)
	}

	brk := f.label("break")
	cont := f.label("continue")

	f.open() // the loop body
	if cond != nil {
		// `while (c)` leaves when c is false, and the only branch METALS
		// has is br_if, so the test is inverted rather than the branch.
		flag := f.condition(cond)
		leave := f.temp()
		f.emit(&metals.Un{Dst: leave, Op: "not", X: flag})
		f.emit(&metals.BrIf{Cond: leave, Label: brk})
	}

	f.open() // the continue block
	f.loops = append(f.loops, loop{brk: brk, cont: cont})
	f.stmt(body)
	f.loops = f.loops[:len(f.loops)-1]
	f.emit(&metals.Block{Label: cont, Body: f.close()})

	if post != nil {
		f.expr(post)
	}
	top := f.label("top")
	f.emit(&metals.Br{Label: top})

	f.emit(&metals.Block{
		Label: brk,
		Body:  []metals.Instr{&metals.Loop{Label: top, Body: f.close()}},
	})
}

// A switch is nested blocks, one per group of case labels, in source
// order and innermost first:
//
//	block $break {
//	  block $g2 {
//	    block $g1 {
//	      block $g0 { switch %x { 1: br $g0; 2: br $g1; default: br $g2 } }
//	      <group 0>
//	    }
//	    <group 1>
//	  }
//	  <group 2>
//	}
//
// `br $g1` lands where group 1 begins, and running off the end of group
// 1 lands where group 2 begins — so fallthrough is what happens when a
// group does not branch, which is exactly C's rule. `break` leaves the
// outermost block, and a `default` anywhere in the body is just the
// group the switch's default arm branches to.
//
// This is the encoding that needs no relooper, and it is the reason
// `switch` is here while `goto` is not: C's switch is a multiway branch
// into a linear body, and C's goto is not.
type switchGroup struct {
	values     []int64
	hasDefault bool
	label      string
	stmts      []stmt
	body       []metals.Instr
	// closed says a statement has followed the labels, so the next label
	// starts a group of its own rather than joining this one.
	closed bool
}

func (f *funcLowerer) switchStmt(s *switchStmt) {
	control := f.expr(s.x)
	if !control.typ.isInteger() {
		fail(s.pos, "a switch runs on an integer, not a %s", control.typ)
	}
	// The control expression promotes, which is what makes `switch (c)`
	// on a char dispatch on the int the case labels already are.
	control = f.convert(control, promote(control.typ), s.pos)

	groups := f.switchGroups(s)
	if len(groups) == 0 {
		// A switch with no labels runs nothing, and its control
		// expression has already been evaluated for its side effects.
		return
	}

	brk := f.label("break")
	f.pushScope()
	// The whole body is one C scope, and `break` inside it leaves the
	// switch while `continue` still belongs to the enclosing loop.
	enclosing := ""
	if len(f.loops) > 0 {
		enclosing = f.loops[len(f.loops)-1].cont
	}
	f.loops = append(f.loops, loop{brk: brk, cont: enclosing})

	for i := range groups {
		f.open()
		for _, inner := range groups[i].stmts {
			f.stmt(inner)
		}
		groups[i].body = f.close()
	}

	f.loops = f.loops[:len(f.loops)-1]
	f.popScope()

	// The dispatch, then the blocks around it from the inside out.
	dispatch := &metals.Switch{X: control.reg, Default: []metals.Instr{&metals.Br{Label: brk}}}
	for _, group := range groups {
		if group.hasDefault {
			dispatch.Default = []metals.Instr{&metals.Br{Label: group.label}}
		}
		for _, value := range group.values {
			dispatch.Cases = append(dispatch.Cases, metals.SwitchCase{
				Value: int(value),
				Body:  []metals.Instr{&metals.Br{Label: group.label}},
			})
		}
	}

	level := []metals.Instr{dispatch}
	for _, group := range groups {
		level = append([]metals.Instr{&metals.Block{Label: group.label, Body: level}}, group.body...)
	}
	f.emit(&metals.Block{Label: brk, Body: level})
}

// switchGroups splits a switch body into the runs a label reaches:
// consecutive labels with nothing between them name one group, which is
// what `case 1: case 2:` means.
func (f *funcLowerer) switchGroups(s *switchStmt) []*switchGroup {
	var groups []*switchGroup
	seen := map[int64]bool{}
	sawDefault := false

	for _, inner := range s.body {
		switch label := inner.(type) {
		case *caseStmt:
			if seen[label.value] {
				fail(label.pos, "case %d appears twice in this switch", label.value)
			}
			seen[label.value] = true
			if int64(int(label.value)) != label.value {
				fail(label.pos, "case %d does not fit the switch's dispatch", label.value)
			}
			groups = f.openGroup(groups)
			last := groups[len(groups)-1]
			last.values = append(last.values, label.value)

		case *defaultStmt:
			if sawDefault {
				fail(label.pos, "this switch has two defaults")
			}
			sawDefault = true
			groups = f.openGroup(groups)
			groups[len(groups)-1].hasDefault = true

		default:
			if len(groups) == 0 {
				fail(s.pos, "a statement before the first case label of a switch is unreachable")
			}
			group := groups[len(groups)-1]
			group.stmts = append(group.stmts, inner)
			group.closed = true
		}
	}
	return groups
}

// openGroup starts a group, or keeps the one already open when the
// previous label had no statements after it.
func (f *funcLowerer) openGroup(groups []*switchGroup) []*switchGroup {
	if len(groups) > 0 && !groups[len(groups)-1].closed {
		return groups
	}
	return append(groups, &switchGroup{label: f.label("case")})
}

func (f *funcLowerer) doStmt(s *doStmt) {
	brk := f.label("break")
	cont := f.label("continue")
	top := f.label("top")

	f.open() // the loop body

	f.open() // the continue block
	f.loops = append(f.loops, loop{brk: brk, cont: cont})
	f.stmt(s.body)
	f.loops = f.loops[:len(f.loops)-1]
	f.emit(&metals.Block{Label: cont, Body: f.close()})

	// The test is at the bottom, which is the one thing that makes a
	// do-while a different shape rather than a flag.
	f.emit(&metals.BrIf{Cond: f.condition(s.cond), Label: top})

	f.emit(&metals.Block{
		Label: brk,
		Body:  []metals.Instr{&metals.Loop{Label: top, Body: f.close()}},
	})
}
