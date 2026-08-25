package cc

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// goto, and the labels it jumps to.
//
// METALS has structured control flow and no jump: `br $L` leaves an
// enclosing `block`, and `br $L` to a `loop` restarts it. That is
// WebAssembly's shape and it is deliberate — a format with a jump is a
// format whose consumers all need a control-flow graph. So a `goto` has
// to become a branch out of, or back to the top of, something that
// encloses it.
//
// The trick is that C's labels already nest, and the two directions want
// two different wrappers:
//
//   - A label that is jumped to from *earlier* gets a `block` around
//     everything in its statement list before it. `br` out of that block
//     lands exactly where the label is.
//   - A label that is jumped to from *later* gets a `loop` around
//     everything in its list from the label onwards. `br` to that loop
//     restarts it, which lands exactly where the label is, and running
//     off the end falls out the way the statements after it would.
//
// A label that is both gets both. Nothing else is needed, because a
// `goto` lexically before its label is inside that label's block and one
// lexically after it is inside that label's loop — and METALS lets a
// branch name any enclosing label, however deep the goto sits.
//
// What this does not do is the relooper. A `goto` that jumps *into* a
// block — a label in one nested statement list reached from another — is
// neither of the two cases and is refused by name. That is the shape a
// general control-flow graph would need, and it is vanishingly rare in
// real C: the three things `goto` is written for are a forward jump to a
// cleanup label, a backward jump that is a loop, and breaking out of
// nested loops, and all three are a label in the goto's own list or an
// enclosing one.

// labelUse is what one label needs, which follows from where it is
// jumped to rather than from how it is written.
type labelUse struct {
	name string
	pos  position
	// seq is the label's own place in the function's statement order,
	// and begin/end bracket the statement list it sits in. A goto lands
	// in one of the two halves or in neither, and that is the whole of
	// the analysis.
	seq, begin, end int
	// forward is set by a goto before the label, backward by one after
	// it. Both may be. lastGoto is the furthest of the backward ones,
	// which is how far the label's loop has to reach.
	forward, backward bool
	lastGoto          int
	// block and loop are the METALS labels the two wrappers get. They
	// are made when the wrapper is, so a label nothing jumps to costs
	// nothing.
	block, loop string
}

// labelPass numbers a function's statements and works out, for each
// label, which side each goto to it comes from.
type labelPass struct {
	next   int
	byName map[string]*labelUse
	// where records each goto's direction, keyed by the statement, so
	// that lowering does not have to work it out again.
	where map[*gotoStmt]bool // true for forward
	gotos map[*gotoStmt]int  // the goto's own place in the order
}

func scanLabels(fn *funcDecl) *labelPass {
	p := &labelPass{
		byName: map[string]*labelUse{},
		where:  map[*gotoStmt]bool{},
		gotos:  map[*gotoStmt]int{},
	}
	p.list(fn.body.stmts)
	p.resolve(fn)
	return p
}

// list walks one statement list, which is where labels live: a label is
// a point between two statements, and the two halves it divides the list
// into are what the block and the loop wrap.
func (p *labelPass) list(stmts []stmt) {
	begin := p.next
	var here []*labelUse
	for _, s := range stmts {
		if label, ok := s.(*labelStmt); ok {
			if _, dup := p.byName[label.name]; dup {
				fail(label.pos, "%s labels two statements in one function", label.name)
			}
			use := &labelUse{name: label.name, pos: label.pos, seq: p.next, begin: begin}
			p.byName[label.name] = use
			here = append(here, use)
			// A label has nothing nested in it; it is a point between
			// two statements, and the number is that point.
			p.next++
			continue
		}
		p.walk(s)
	}
	for _, use := range here {
		use.end = p.next
	}
}

// walk numbers one statement and everything inside it. The numbering is
// preorder, so everything nested inside a statement falls between that
// statement's number and the next sibling's — which is what makes "is
// this goto lexically inside that half of the list" a comparison.
func (p *labelPass) walk(s stmt) {
	at := p.next
	p.next++

	switch s := s.(type) {
	case *blockStmt:
		p.list(s.stmts)
	case *ifStmt:
		p.walk(s.then)
		if s.els != nil {
			p.walk(s.els)
		}
	case *whileStmt:
		p.walk(s.body)
	case *doStmt:
		p.walk(s.body)
	case *forStmt:
		if s.init != nil {
			p.walk(s.init)
		}
		p.walk(s.body)
	case *switchStmt:
		p.list(s.body)
	case *gotoStmt:
		p.gotos[s] = at
	case *labelStmt:
		// list handles every label that is in a statement list, so
		// reaching one here means it is the whole body of an `if` or a
		// loop written without braces. There is nothing to wrap around
		// it and nothing to branch out of.
		fail(s.pos, "label %s is not in a block of its own, and a goto needs one it can branch out of", s.name)
	}
}

func (p *labelPass) resolve(fn *funcDecl) {
	for s, at := range p.gotos {
		use, ok := p.byName[s.name]
		if !ok {
			fail(s.pos, "goto %s names no label in %s", s.name, fn.name)
		}
		switch {
		case at >= use.begin && at < use.seq:
			use.forward = true
			p.where[s] = true
		case at > use.seq && at < use.end:
			use.backward = true
			if at > use.lastGoto {
				use.lastGoto = at
			}
		default:
			// The label is in a statement list this goto is not inside,
			// which is a jump into a block. Expressing it needs the
			// relooper, and no shape here does.
			fail(s.pos, "goto %s jumps into a block, which this compiler has no shape for; a label reached from outside its own statement list needs a control-flow graph", s.name)
		}
	}
}

// ---------------------------------------------------------------------------
// Lowering
// ---------------------------------------------------------------------------

// segments lowers a statement list, wrapping it as the labels in it
// need. A list with no labels — which is nearly all of them — is the
// same loop over statements it was before.
//
// The wrappers are peeled off one label at a time, outermost first, and
// a forward one always goes outside a backward one in the same region.
// A block for the last forward label covers everything before it; what
// is left after it is a region of its own. A loop for the first backward
// label covers everything from it to the end of the region — the end of
// the *region*, not of the list, which is what lets `again: ... goto
// out; ... goto again; out:` work: the loop sits inside the block, and
// falling off the end of the loop leaves the block, which lands at
// `out`.
//
// The one shape this cannot express is a backward label whose jumps
// reach past a later forward label, because the loop would then have to
// cross out of the block. That is where a control-flow graph would be
// needed, and it is refused by name.
func (f *funcLowerer) segments(stmts []stmt) {
	var fwd, back []int
	for i, s := range stmts {
		label, ok := s.(*labelStmt)
		if !ok {
			continue
		}
		use := f.labels[label.name]
		if use.forward {
			fwd = append(fwd, i)
		}
		if use.backward {
			back = append(back, i)
		}
	}

	if len(fwd) > 0 {
		p := fwd[len(fwd)-1]
		use := f.labels[stmts[p].(*labelStmt).name]

		// Every loop this block will contain has to close inside it.
		for _, q := range back {
			if q >= p {
				continue
			}
			inner := f.labels[stmts[q].(*labelStmt).name]
			if inner.lastGoto > use.seq {
				fail(inner.pos,
					"%s is jumped back to from after %s, so its loop would have to leave %s's block; that takes a control-flow graph, which this compiler has no shape for",
					inner.name, use.name, use.name)
			}
		}

		use.block = f.gotoLabel(use.name, "ahead")
		f.open()
		f.segments(stmts[:p])
		f.emit(&metals.Block{Label: use.block, Body: f.close()})

		// A label jumped to from both sides gets both wrappers: the
		// block ends where it is, and the loop starts there.
		if use.backward {
			use.loop = f.gotoLabel(use.name, "back")
			f.open()
			f.segments(stmts[p+1:])
			f.emit(&metals.Loop{Label: use.loop, Body: f.close()})
			return
		}
		f.segments(stmts[p+1:])
		return
	}

	if len(back) > 0 {
		p := back[0]
		use := f.labels[stmts[p].(*labelStmt).name]
		f.plain(stmts[:p])
		use.loop = f.gotoLabel(use.name, "back")
		f.open()
		f.segments(stmts[p+1:])
		f.emit(&metals.Loop{Label: use.loop, Body: f.close()})
		return
	}

	f.plain(stmts)
}

// plain lowers statements with no wrapping. A label reaching here is one
// nothing jumps to, or one whose wrapper is already open around it.
func (f *funcLowerer) plain(stmts []stmt) {
	for _, s := range stmts {
		if _, ok := s.(*labelStmt); ok {
			continue
		}
		f.stmt(s)
	}
}

func (f *funcLowerer) gotoLabel(name, kind string) string {
	f.n++
	return "goto." + name + "." + kind + strconv.Itoa(f.n)
}

func (f *funcLowerer) gotoStmt(s *gotoStmt) {
	use := f.labels[s.name]
	target := use.loop
	if f.forward[s] {
		target = use.block
	}
	if target == "" {
		// The wrapper is opened where the label's list is lowered, so an
		// empty name means the label sits somewhere this pass does not
		// reach — a group inside a switch, where the labels are already
		// the case labels.
		fail(s.pos, "goto %s reaches a label in a switch body, which is a list of case labels rather than one a branch can leave", s.name)
	}
	f.emit(&metals.Br{Label: target})
}
