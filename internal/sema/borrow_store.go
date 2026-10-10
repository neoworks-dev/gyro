package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/types"
)

// Storing a view, and the arena a value sits in.
//
// A view returned from a function is checked at the return. A view put into
// something else, a struct field or a container, is checked where it goes in:
// the target takes on the origin of what it was handed, so returning it later
// is judged like returning the view, and a target that outlives this frame
// cannot take frame storage at all. Like the rest of the analysis this only
// ever adds, with one exception: assigning a plain name replaces the arenas
// it holds, because a name reassigned after its arena was reset holds
// something new, and the old value is not what a later read gets.

// isBundledStruct reports a struct declared by the named bundled module.
func isBundledStruct(t types.Type, module, name string) bool {
	t = stripPointer(t)
	strct, ok := t.(*types.Struct)
	if !ok {
		return false
	}
	origin := strct
	if strct.Origin != nil {
		origin = strct.Origin
	}
	mod, bundled := stdlib.NameOf(origin.Module)
	return bundled && mod == module && origin.Name == name
}

func stripPointer(t types.Type) types.Type {
	if pointer, ok := t.(*types.Pointer); ok {
		return pointer.Elem
	}
	return t
}

func isAllocator(t types.Type) bool { return isBundledStruct(t, "mem", "Allocator") }
func isArena(t types.Type) bool     { return isBundledStruct(t, "mem", "Arena") }

// isArenaMethod reports a call of the named method on mem's Arena.
func (c *checker) isArenaMethod(call *ast.CallExpr, selector *ast.SelectorExpr, name string) bool {
	return selector.Name == name && isArena(c.typeOf(selector.X))
}

// rootSymbol is the variable an expression is a place inside of: `a` for
// `a.items[i].name`, the receiver for `self.items`. Anything else, a call
// result among them, has none.
func (c *checker) rootSymbol(e ast.Expr) *symbol {
	switch e := e.(type) {
	case *ast.Ident:
		sym := c.scope.lookup(e.Name)
		if sym == nil || sym.kind != symValue {
			return nil
		}
		return sym
	case *ast.SelfExpr:
		if c.fn == nil {
			return nil
		}
		return c.fn.selfSymbol
	case *ast.SelectorExpr:
		return c.rootSymbol(e.X)
	case *ast.IndexExpr:
		return c.rootSymbol(e.X)
	case *ast.TupleIndexExpr:
		return c.rootSymbol(e.X)
	case *ast.UnaryExpr:
		return c.rootSymbol(e.X)
	}
	return nil
}

// outlivesFrame reports a variable whose storage is not this frame's: a
// parameter or receiver is the caller's, and a module-level variable is the
// process's.
func (c *checker) outlivesFrame(sym *symbol) bool {
	return sym.param || c.module.names[sym.name] == sym
}

// localArena reports an arena that ends with this function.
func (c *checker) localArena(ref arenaRef) bool {
	return !c.outlivesFrame(ref.sym) && !isAllocator(ref.sym.typ)
}

// firstLocalArena finds an arena in the origin that ends with this function.
func (c *checker) firstLocalArena(o borrowOrigin) *arenaRef {
	for i := range o.arenas {
		if c.localArena(o.arenas[i]) {
			return &o.arenas[i]
		}
	}
	return nil
}

// noteCall follows what a resolved call does to its receiver: a method that
// takes `*self` may keep what it is handed, and an arena's free ends the
// values in it.
func (c *checker) noteCall(call *ast.CallExpr, target callee) {
	if !c.borrowTracked() || target.method == nil {
		return
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	if c.isArenaMethod(call, selector, "free") || c.isArenaMethod(call, selector, "free_all") {
		c.noteArenaFree(selector)
		return
	}

	if target.method.Recv != types.PointerRecv {
		return
	}
	var origin borrowOrigin
	for _, arg := range call.Args {
		origin = origin.merge(restrictTo(c.originOf(arg.Value), c.typeOf(arg.Value), true))
	}
	origin = restrictTo(origin, stripPointer(c.typeOf(selector.X)), false)
	c.storeInto(selector.X, origin, call.Offset)
}

// noteArenaFree records that an arena's blocks are gone from here on. A
// deferred free runs at exit, after every use there is to check.
func (c *checker) noteArenaFree(selector *ast.SelectorExpr) {
	root := c.rootSymbol(selector.X)
	if root == nil || c.deferDepth > 0 {
		return
	}
	c.tick++
	if selector.Name == "free" {
		c.flow.freed[root] = c.tick
	} else {
		c.flow.reset[root] = c.tick
	}
}

// noteAssign follows `target = value`. A plain name takes the origin of the
// value; a field or an element is a store, like a push.
func (c *checker) noteAssign(lhs, rhs ast.Expr) {
	if !c.borrowTracked() {
		return
	}
	origin := c.originOf(rhs)
	if ident, ok := lhs.(*ast.Ident); ok {
		sym := c.scope.lookup(ident.Name)
		if sym == nil || sym.kind != symValue || scalarType(sym.typ) {
			return
		}
		origin = restrictTo(origin, sym.typ, true)
		sym.origin = borrowOrigin{params: sym.origin.params, local: sym.origin.local,
			closure: sym.origin.closure, depth: sym.origin.depth}.merge(origin)
		return
	}
	c.storeInto(lhs, restrictTo(origin, c.typeOf(lhs), false), lhs.Pos())
}

// storeInto records a view put into the place target is inside of, and
// rejects one that cannot stay there.
func (c *checker) storeInto(target ast.Expr, origin borrowOrigin, offset int) {
	if !origin.known() {
		return
	}
	root := c.rootSymbol(target)
	if root == nil {
		return
	}
	name := root.name
	outlives := c.outlivesFrame(root)

	if origin.local && outlives {
		if origin.closure {
			c.errorHintf(offset, []string{
				"the closure's environment is in this frame, and " + name + " outlives it",
				"copy it out first with keep(allocator, f) from mem, or write a closure that captures nothing",
			}, "this stores a closure that borrows this frame into %s, which outlives it", name)
		} else {
			c.errorHintf(offset, []string{
				"the storage is this frame's, and " + name + " outlives it",
				"store an owning Array<T> or String, or a view the caller handed in",
			}, "this stores a view into storage that ends with this function into %s, which outlives it", name)
		}
		return
	}
	if origin.local && origin.depth > root.loopDepth {
		if origin.closure {
			c.errorHintf(offset, []string{
				"a loop body writes the closure's environment in the same place every time, so each element would see the last iteration's captures",
				"copy each one out with keep(allocator, f) from mem",
			}, "this stores a closure written in a loop body into %s, which is declared outside the loop", name)
		} else {
			c.errorHintf(offset, []string{
				"a loop body writes the storage in the same place every time, so each element would see the last iteration's contents",
				"store an owning Array<T> or a copy",
			}, "this stores a view of storage written in a loop body into %s, which is declared outside the loop", name)
		}
		return
	}
	if ref := c.firstLocalArena(origin); ref != nil && outlives {
		c.errorHintf(offset, []string{
			"the arena is freed with this function, and " + name + " outlives it",
			"allocate from an allocator the caller handed in",
		}, "this stores a value allocated from %s into %s, which outlives it", ref.sym.name, name)
		return
	}

	root.origin = root.origin.merge(origin)
}

// checkReturnedArena reports a returned value that sits in an arena which
// ends with this function. It answers whether it did.
func (c *checker) checkReturnedArena(origin borrowOrigin, offset int, want types.Type) bool {
	if scalarType(want) {
		return false
	}
	ref := c.firstLocalArena(origin)
	if ref == nil {
		return false
	}
	c.errorHintf(offset, []string{
		"the arena is local to this function and ends with it, taking everything allocated from it",
		"take the allocator as a parameter, so the caller owns the arena",
	}, "this returns a value allocated from %s, which ends with this function", ref.sym.name)
	return true
}

// checkArenaLive reports a read of a value whose arena has been freed since
// the value was allocated. Reading an allocator after free_all is fine, since
// a reset arena hands out blocks again; reading one after free is not.
func (c *checker) checkArenaLive(sym *symbol, offset int) {
	if !c.borrowTracked() || len(sym.origin.arenas) == 0 {
		return
	}
	for _, ref := range sym.origin.arenas {
		if ref.sym == sym {
			continue
		}
		verb := ""
		switch {
		case c.flow.freed[ref.sym] > ref.tick:
			verb = "freed"
		case c.flow.reset[ref.sym] > ref.tick && !isAllocator(sym.typ):
			verb = "reset"
		default:
			continue
		}
		c.errorHintf(offset, []string{
			"everything allocated from an arena ends when it is freed or reset",
			"use " + sym.name + " before the " + verb + ", or allocate it again afterwards",
		}, "%s was allocated from %s, which has been %s", sym.name, ref.sym.name, verb)
		return
	}
}
