package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
)

// Leak analysis. An `alloc<T>()` bound to a local is tracked until something
// settles it: `free` releases it, and a return, a send, a call argument, a
// move capture or a field it is stored into all carry it out of reach. What
// reaches the end of its block settled by nothing is reported.
//
// A warning rather than an error, because an arena and a process-lifetime
// allocation are both deliberate and neither frees.
//
// Settling merges the same way definite assignment does: a name counts as
// settled below a branch only when every path settled it, so freeing in one
// arm of an if and not the other is still reported.

// allocSite is one tracked allocation: the local it was bound to, and where
// to point when nothing settles it.
type allocSite struct {
	sym    *symbol
	offset int
	name   string
}

// trackAllocation registers a local bound to an alloc call against the block
// it was declared in.
func (c *checker) trackAllocation(sym *symbol, offset int, name string) {
	if sym == nil || len(c.allocScopes) == 0 {
		return
	}
	top := len(c.allocScopes) - 1
	c.allocScopes[top] = append(c.allocScopes[top], allocSite{sym: sym, offset: offset, name: name})
}

// pushAllocScope runs a block's statements with their own set of tracked
// allocations, reporting the ones nothing settled when the block ends.
func (c *checker) pushAllocScope(walk func()) {
	c.allocScopes = append(c.allocScopes, nil)
	walk()

	sites := c.allocScopes[len(c.allocScopes)-1]
	c.allocScopes = c.allocScopes[:len(c.allocScopes)-1]
	c.reportUnsettled(sites)
}

// checkLeaksAtExit reports what a `return` leaves behind. Falling out of the
// bottom of a block is not the only way to reach its end, and an allocation
// the return does not carry with it is lost exactly here.
//
// Only the blocks belonging to the function being returned from are
// examined; a closure's return says nothing about the allocations of the
// function it was written in.
func (c *checker) checkLeaksAtExit() {
	for depth := len(c.allocScopes) - 1; depth >= c.allocFloor; depth-- {
		c.reportUnsettled(c.allocScopes[depth])
	}
}

func (c *checker) reportUnsettled(sites []allocSite) {
	for _, site := range sites {
		if c.flow.settled[site.sym] || c.reportedLeaks[site.sym] {
			continue
		}
		c.warnLeak(site)
	}
}

func (c *checker) warnLeak(site allocSite) {
	// One mention per allocation: a leak on an early-exit path and the same
	// leak at the end of the block are one problem.
	c.reportedLeaks[site.sym] = true
	c.bag.Add(diag.Diagnostic{
		Severity: diag.SeverityWarning,
		Code:     diag.WPossibleLeak,
		Pos:      c.file.Position(site.offset),
		Message:  "the allocation in " + site.name + " reaches the end of its block unfreed",
		Help: []string{
			"release it: defer free(" + site.name + ") beside the allocation",
			"or return it, so its caller owns it",
			"an arena or a process-lifetime allocation frees nothing on purpose; this is a warning, not an error",
		},
	})
}

// settleAllocation marks a tracked allocation as no longer this block's
// problem, whether it was freed or carried away.
func (c *checker) settleAllocation(sym *symbol) {
	if sym == nil || !sym.holdsAllocation {
		return
	}
	c.flow.settled[sym] = true
}

// settleEscape settles the allocation an expression hands somewhere else.
// Only a bare name counts: `p.field` reads through the pointer and leaves
// ownership where it was.
func (c *checker) settleEscape(e ast.Expr) {
	ident, ok := e.(*ast.Ident)
	if !ok {
		return
	}
	if sym := c.scope.lookup(ident.Name); sym != nil {
		c.settleAllocation(sym)
	}
}

// settleEscapes settles every name in a list of expressions, which is what a
// multi-value return and an argument list need.
func (c *checker) settleEscapes(exprs []ast.Expr) {
	for _, e := range exprs {
		c.settleEscape(e)
	}
}

// settleCallArgs settles every bare name handed to a call. A callee may keep
// what it is given, so passing a pointer is treated as handing it over —
// `free(p)` is only the most direct case of that.
func (c *checker) settleCallArgs(e ast.Expr) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return
	}
	for _, arg := range call.Args {
		c.settleEscape(arg.Value)
	}
}

// allocationCall reports whether an expression is a call to mem's alloc on
// the process heap, so that the local it initializes becomes this block's
// responsibility. A block from any other allocator — an arena, or one the
// function was handed — is that allocator's to reclaim, so it is not
// tracked.
func (c *checker) allocationCall(e ast.Expr) bool {
	if c.intrinsicCall(e) != "alloc" {
		return false
	}
	call := e.(*ast.CallExpr)
	return len(call.Args) == 1 && c.intrinsicCall(call.Args[0].Value) == "heap"
}

// intrinsicCall names the mem intrinsic a call invokes, or "".
func (c *checker) intrinsicCall(e ast.Expr) string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return ""
	}
	sym := c.calleeSymbol(call.Fun)
	if sym == nil {
		return ""
	}
	return sym.intrinsic
}

// calleeSymbol resolves the name a call goes through without reporting
// anything: the call itself has already been checked.
func (c *checker) calleeSymbol(fun ast.Expr) *symbol {
	switch fun := fun.(type) {
	case *ast.Ident:
		return c.scope.lookup(fun.Name)
	case *ast.Path:
		// alloc<Node>() carries its type argument on the segment, so this
		// is a path rather than a plain name.
		if len(fun.Segments) == 1 {
			return c.scope.lookup(fun.Segments[0].Name)
		}
	}
	return nil
}
