package sema

import (
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// Borrow checking. `T[]` and `string` are views over storage they do not
// own, so a function returning one has to say where the view came from. An
// effect row entry written `&xs` names a parameter; a row with no such entry
// says the result views nothing.
//
// What this tracks is provenance, not lifetime: every slice-typed local is
// either a view into a named parameter or backed by storage in this frame.
// Returning the second kind hands out a view of a frame that is about to go
// away, which is what gets reported.

// borrowOrigin is where a slice-typed value came from. A value can view more
// than one parameter, since a call whose row names several sources returns a
// view into whichever argument it picked.
type borrowOrigin struct {
	// params are the parameters of the enclosing function the value views.
	params []*symbol
	// local marks storage created here: an array literal, or the buffer of
	// an Array this frame owns.
	local bool
}

func (o borrowOrigin) known() bool { return len(o.params) > 0 || o.local }

// merge combines the origins a value could have come from. Local storage is
// contagious: a view that might sit on this frame does, for checking.
func (o borrowOrigin) merge(other borrowOrigin) borrowOrigin {
	merged := borrowOrigin{local: o.local || other.local}
	merged.params = append(merged.params, o.params...)
	for _, sym := range other.params {
		if !containsSymbol(merged.params, sym) {
			merged.params = append(merged.params, sym)
		}
	}
	return merged
}

func containsSymbol(syms []*symbol, want *symbol) bool {
	for _, sym := range syms {
		if sym == want {
			return true
		}
	}
	return false
}

// declareBorrows resolves a signature's `&name` entries against its
// parameters, leaving the set the body is checked against.
func (c *checker) declareBorrows(decl *ast.FuncDecl) map[*symbol]bool {
	row := decl.Effects
	if row == nil || len(row.Borrows) == 0 {
		return nil
	}

	sources := map[*symbol]bool{}
	for _, borrow := range row.Borrows {
		sym := c.scope.lookup(borrow.Name)
		if sym == nil || sym.kind != symValue {
			c.errorHintf(borrow.Offset, []string{
				"a borrow names a parameter of this function, as in fn f(xs: i32[]): i32[] !{ &xs }",
			}, "&%s names no parameter of this function", borrow.Name)
			continue
		}
		if !viewType(sym.typ) {
			c.errorHintf(borrow.Offset, []string{
				"only a slice or a string is a view; " + sym.typ.String() + " owns what it holds",
			}, "&%s borrows from %s, which is not a view", borrow.Name, sym.typ)
			continue
		}
		sources[sym] = true
	}

	if !viewType(c.fn.result) && len(sources) > 0 {
		c.errorHintf(row.Offset, []string{
			"a borrow says what the result is a view into; this function returns " + describeType(c.fn.result),
		}, "this row borrows, but the result is not a view")
	}
	return sources
}

// checkReturnedBorrow reports a returned view whose storage the row did not
// account for.
func (c *checker) checkReturnedBorrow(e ast.Expr, offset int) {
	if c.fn == nil || !c.fn.borrowChecked || !viewType(c.fn.result) {
		return
	}
	origin := c.originOf(e)
	if !origin.known() {
		return
	}

	// Frame storage is the worse problem, so it is the one reported.
	if origin.local {
		c.errorHintf(offset, []string{
			"the storage is this frame's, and the caller outlives it",
			"return an owning Array<T> or String, or take the storage as a parameter and name it in the row",
		}, "this returns a view into storage that ends with this function")
		return
	}

	for _, param := range origin.params {
		if c.fn.borrows[param] {
			continue
		}
		c.errorHintf(offset, []string{
			"say so in the row: !{ &" + param.name + " }",
			c.borrowRowHelp(),
		}, "this returns a view into %s, which the row does not name", param.name)
		return
	}
}

// borrowRowHelp restates what the row currently claims, since the row is
// usually a line away from the return.
func (c *checker) borrowRowHelp() string {
	if len(c.fn.borrows) == 0 {
		return "the row names no source, so the result must own what it holds"
	}
	names := make([]string, 0, len(c.fn.borrows))
	for sym := range c.fn.borrows {
		names = append(names, "&"+sym.name)
	}
	sortStrings(names)
	return "the row currently names " + strings.Join(names, ", ")
}

// originOf follows a value back to the storage behind it. Only the forms a
// view can actually travel through are walked; anything else is unknown, and
// unknown is silent.
func (c *checker) originOf(e ast.Expr) borrowOrigin {
	switch e := e.(type) {
	case *ast.Ident:
		sym := c.scope.lookup(e.Name)
		if sym == nil {
			return borrowOrigin{}
		}
		return sym.origin

	case *ast.ArrayLit:
		// The literal is storage in the enclosing scope.
		return borrowOrigin{local: true}

	case *ast.IndexExpr:
		// Slicing a view yields a view of the same storage.
		return c.originOf(e.X)

	case *ast.CallExpr:
		return c.callOrigin(e)
	}
	return borrowOrigin{}
}

// callOrigin works out what a call's result views. A method named on the
// built-in surface sits on its receiver; a call to a named function is
// answered by that function's own row, which is what carries the fact across
// a call and across a module.
func (c *checker) callOrigin(call *ast.CallExpr) borrowOrigin {
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
		switch selector.Name {
		case "slice", "runes", "view":
			return c.originOf(selector.X)
		}
	}
	return c.declaredCallOrigin(call)
}

// declaredCallOrigin maps a callee's `&param` entries onto the arguments
// this call passed for them. `pick(xs, ys)` with `!{ &a }` on pick views
// whatever xs views.
func (c *checker) declaredCallOrigin(call *ast.CallExpr) borrowOrigin {
	decl := c.calleeDecl(call)
	if decl == nil || decl.Effects == nil || len(decl.Effects.Borrows) == 0 {
		return borrowOrigin{}
	}
	// A generic callee's row is not checked at its definition, so it is not
	// trustworthy here either.
	if len(decl.TypeParams) > 0 || len(decl.OwnerParams) > 0 {
		return borrowOrigin{}
	}

	var origin borrowOrigin
	for _, borrow := range decl.Effects.Borrows {
		index := paramIndex(decl, borrow.Name)
		if index < 0 || index >= len(call.Args) {
			continue
		}
		origin = origin.merge(c.originOf(call.Args[index].Value))
	}
	return origin
}

// calleeDecl recovers the declaration a call resolved to, which sema
// recorded when it checked the call.
func (c *checker) calleeDecl(call *ast.CallExpr) *ast.FuncDecl {
	if c.rec == nil {
		return nil
	}
	resolved, ok := c.rec.calls[call]
	if !ok || resolved == nil || resolved.Method == nil {
		return nil
	}
	return resolved.Method.Decl
}

func paramIndex(decl *ast.FuncDecl, name string) int {
	for i, param := range decl.Params {
		if param.Name == name {
			return i
		}
	}
	return -1
}

// noteOrigin records where a newly declared local's storage lives.
func (c *checker) noteOrigin(sym *symbol, value ast.Expr, typ types.Type) {
	if sym == nil {
		return
	}
	// A fixed-size array is storage in this frame rather than a view, and a
	// slice of it points into the frame. `var xs = [1, 2, 3]` is the case
	// language.md calls out: the literal lives in the enclosing scope.
	if _, ok := typ.(*types.Array); ok {
		sym.origin = borrowOrigin{local: true}
		return
	}
	if !viewType(typ) {
		return
	}
	sym.origin = c.originOf(value)
}

// viewType reports whether a type is a view over storage it does not own.
func viewType(t types.Type) bool {
	switch t := t.(type) {
	case *types.Slice:
		return true
	case *types.Basic:
		return t.Kind == types.StringKind
	case *types.Optional:
		return viewType(t.Elem)
	}
	return false
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
