package sema

import "github.com/gyro-lang/gyro/internal/ast"

// Definite assignment. A `var x: i32` with no initializer holds nothing, and
// reading it before every path to the read has written it is an error.
//
// The analysis only ever adds to its set: once a name is assigned it stays
// assigned, and nothing takes that back. So a loop needs no second pass —
// whatever the body writes cannot be relied on afterwards anyway, because
// the body may not run at all. Branches are the whole difficulty, and they
// need the operation the narrowing facts never had: walk two paths
// separately and keep only the names both of them wrote.

// flow is the assignment state at one point: which tracked symbols hold a
// value for certain, and whether the point can be reached. An unreachable
// point contributes nothing to a merge, so `if c { return } else { x = 1 }`
// leaves x assigned below.
type flow struct {
	assigned map[*symbol]bool
	// settled holds the tracked allocations that something has freed or
	// carried out of the block. It merges the same way assigned does.
	settled   map[*symbol]bool
	reachable bool
}

func newFlow() flow {
	return flow{assigned: map[*symbol]bool{}, settled: map[*symbol]bool{}, reachable: true}
}

func copySet(from map[*symbol]bool) map[*symbol]bool {
	to := make(map[*symbol]bool, len(from))
	for sym := range from {
		to[sym] = true
	}
	return to
}

func intersectSets(a, b map[*symbol]bool) map[*symbol]bool {
	both := map[*symbol]bool{}
	for sym := range a {
		if b[sym] {
			both[sym] = true
		}
	}
	return both
}

func (f flow) clone() flow {
	return flow{
		assigned:  copySet(f.assigned),
		settled:   copySet(f.settled),
		reachable: f.reachable,
	}
}

// mergeFlow joins two paths that rejoin: a name is assigned afterwards only
// if both paths assigned it.
func mergeFlow(a, b flow) flow {
	if !a.reachable {
		return b
	}
	if !b.reachable {
		return a
	}
	return flow{
		assigned:  intersectSets(a.assigned, b.assigned),
		settled:   intersectSets(a.settled, b.settled),
		reachable: true,
	}
}

// branchFlow walks one path from a copy of the current state and returns
// where it ended, leaving the checker where it started.
func (c *checker) branchFlow(walk func()) flow {
	saved := c.flow
	c.flow = saved.clone()
	walk()
	result := c.flow
	c.flow = saved
	return result
}

// isolateFlow walks a body whose writes must not escape — a closure, which
// runs at a time this analysis cannot pin down. Reads inside it still check
// against the state at the point the closure is written, which is what makes
// capturing an unassigned local an error.
func (c *checker) isolateFlow(walk func()) {
	c.branchFlow(walk)
}

// loopFlow collects the states that leave a loop through `break`. They only
// matter for a loop that cannot fall out of its condition, where breaking is
// the sole exit.
type loopFlow struct {
	breaks []flow
}

func (c *checker) pushLoop(alwaysTrue bool, walk func()) {
	entry := c.flow.clone()
	loop := &loopFlow{}
	c.loops = append(c.loops, loop)
	walk()
	c.loops = c.loops[:len(c.loops)-1]

	if !alwaysTrue {
		// The loop may run zero times, so nothing its body wrote survives.
		c.flow = entry
		return
	}

	// `while true` is left only by breaking out of it.
	if len(loop.breaks) == 0 {
		c.flow = flow{assigned: map[*symbol]bool{}, settled: map[*symbol]bool{}, reachable: false}
		return
	}
	merged := loop.breaks[0]
	for _, state := range loop.breaks[1:] {
		merged = mergeFlow(merged, state)
	}
	c.flow = merged
}

// recordBreak hands the current state to the innermost loop and marks the
// rest of the block unreachable.
func (c *checker) recordBreak() {
	if len(c.loops) > 0 {
		loop := c.loops[len(c.loops)-1]
		loop.breaks = append(loop.breaks, c.flow.clone())
	}
	c.flow.reachable = false
}

// markAssigned records a write to a tracked name.
func (c *checker) markAssigned(sym *symbol) {
	if sym == nil || !sym.needsAssign {
		return
	}
	c.flow.assigned[sym] = true
}

// markAssignedTarget records a write through an assignment's left side.
func (c *checker) markAssignedTarget(lhs ast.Expr) {
	ident, ok := lhs.(*ast.Ident)
	if !ok {
		return
	}
	if sym := c.scope.lookup(ident.Name); sym != nil {
		c.markAssigned(sym)
	}
}

// checkAssignedTarget reports a read through an assignment's left side,
// which is how a compound assignment sees its target.
func (c *checker) checkAssignedTarget(lhs ast.Expr) {
	ident, ok := lhs.(*ast.Ident)
	if !ok {
		return
	}
	if sym := c.scope.lookup(ident.Name); sym != nil {
		c.checkAssigned(sym, ident.Offset, ident.Name)
	}
}

// checkAssigned reports a read that no path has written. A name bound by a
// two-name receive is reported against its `open` flag instead, since a
// closed channel yields no value and the flag is what says so.
func (c *checker) checkAssigned(sym *symbol, offset int, name string) {
	if sym == nil || !sym.needsAssign || c.flow.assigned[sym] {
		return
	}
	if !c.flow.reachable {
		return
	}
	// Treat it as assigned from here on. One mention of the real problem
	// beats a report at every later read of the same name.
	c.flow.assigned[sym] = true

	if sym.openGuard != nil {
		c.errorHintf(offset, []string{
			"a closed channel yields no value, so " + name + " is only there when " + sym.openGuardName + " is true",
			"guard the read: if " + sym.openGuardName + " { ... }",
		}, "%s holds a value only where %s is true", name, sym.openGuardName)
		return
	}

	c.errorHintf(offset, []string{
		"assign it where it is declared: var " + name + ": T = ...",
		"or write to it on every path that reaches this read",
	}, "%s is read before it is assigned", name)
}

// receiveGuard ties the value of a two-name receive to its flag, so that
// `if open { ... }` is what makes the value readable.
func (c *checker) receiveGuard(value, open *symbol, openName string) {
	if value == nil || open == nil {
		return
	}
	value.needsAssign = true
	value.openGuard = open
	value.openGuardName = openName
	c.guarded[open] = append(c.guarded[open], value)
}

// assumeAssigned takes the values a condition settled as readable on this
// branch.
func (c *checker) assumeAssigned(syms []*symbol) {
	for _, sym := range syms {
		c.flow.assigned[sym] = true
	}
}

// guardedByCondition returns the names a bare `open` condition makes
// readable on its true branch.
func (c *checker) guardedByCondition(e ast.Expr) []*symbol {
	ident, ok := e.(*ast.Ident)
	if !ok {
		return nil
	}
	sym := c.scope.lookup(ident.Name)
	if sym == nil {
		return nil
	}
	return c.guarded[sym]
}
