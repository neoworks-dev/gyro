package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// Unsafe code is marked on functions. A function is one of three kinds:
//
//   - unsafe fn: unsafe to call, because it relies on something its caller
//     has to uphold. Its body is an unsafe context.
//   - safe fn: its body is an unsafe context too, but the author vouches
//     that no caller can misuse it, so anyone may call it.
//   - fn: neither. It may not use an unchecked operation or call an unsafe
//     function.
//
// The unchecked operations are the calls to unsafe functions — the
// primitives of the bundled `unsafe` module, an @extern declaration not
// written `safe`, and a function imported from a C file by an import that
// does not vouch — and reading a union member. Unsafe is not an effect and
// does not appear in a row: it propagates only through calls to an unsafe
// fn, and a safe fn is where it stops.
//
// A closure is part of the function it is written in, so it uses whatever
// context that function has. The author of a safe fn vouches for the
// closures inside it as for the rest of the body.

// isUnsafeDecl reports whether calling the declaration needs an unsafe
// context. A foreign function is unsafe by default, since nothing the
// compiler can see says what the C behind it does, and `safe` on the
// declaration is the author saying it does nothing a caller can misuse.
func isUnsafeDecl(decl *ast.FuncDecl) bool {
	if decl == nil {
		return false
	}
	if decl.Unsafe {
		return true
	}
	return ast.FindDecorator(decl.Decorators, "extern") != nil && !decl.Safe
}

// trusted reports whether the function being checked may do unchecked
// things.
func (c *checker) trusted() bool {
	return c.fn != nil && c.fn.trusted
}

// requireTrusted reports an unchecked operation in a plain fn.
func (c *checker) requireTrusted(offset int, format string, subject string) {
	if c.trusted() {
		return
	}
	c.errorHintf(offset, []string{
		"mark this function `safe fn` if it upholds what the operation needs, so that callers need not",
		"mark it `unsafe fn` to leave that to its callers instead",
	}, format, subject)
}

// checkUnsafeCall holds a call to an unsafe function to an unsafe context.
func (c *checker) checkUnsafeCall(offset int, target callee) {
	if target.method == nil || target.vouched || !isUnsafeDecl(target.method.Decl) {
		return
	}
	c.requireTrusted(offset, "%s is unsafe to call; this function is a plain fn", target.name)
}

// markUnionWrites records the selectors on an assignment's left side that
// store into a union member rather than read it. Storing says nothing about
// which member is live, so it is safe; the same selector as an operand is a
// read of bytes another member may have written.
//
// The target itself is a store whatever it holds. A selector under it is
// only a way to say where, and is a store too when the member is storage
// in the union: `v.bytes[2] = 1` and `n.inner.i = 3` write the union's own
// bytes. When the member holds a pointer, the pointer is read out of the
// union to be written through, which is a read.
func (c *checker) markUnionWrites(lhs ast.Expr) {
	direct := true
	for {
		var next ast.Expr
		switch e := lhs.(type) {
		case *ast.SelectorExpr:
			if direct {
				c.unionWrites[e] = unionStore
			} else {
				c.unionWrites[e] = unionPlace
			}
			next = e.X
		case *ast.IndexExpr:
			next = e.X
		case *ast.TupleIndexExpr:
			next = e.X
		default:
			return
		}
		lhs = next
		direct = false
	}
}

// unionStores values for checker.unionWrites.
const (
	unionStore = iota + 1 // the member is the target of the assignment
	unionPlace            // the member is on the way to the target
)

// isUnionStore reports whether the selector writes the member it names.
func (c *checker) isUnionStore(e *ast.SelectorExpr, member types.Type) bool {
	switch c.unionWrites[e] {
	case unionStore:
		return true
	case unionPlace:
		switch member.(type) {
		case *types.Array, *types.Struct, *types.Union:
			return true
		}
	}
	return false
}
