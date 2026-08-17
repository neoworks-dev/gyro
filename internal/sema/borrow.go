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

// borrowOrigin is where a slice-typed value came from.
type borrowOrigin struct {
	// param is the parameter the value views, or nil when the storage
	// belongs to this frame.
	param *symbol
	// local marks storage created here: an array literal, or the buffer of
	// an Array this frame owns.
	local bool
}

func (o borrowOrigin) known() bool { return o.param != nil || o.local }

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

	if origin.param != nil {
		if c.fn.borrows[origin.param] {
			return
		}
		c.errorHintf(offset, []string{
			"say so in the row: !{ &" + origin.param.name + " }",
			c.borrowRowHelp(),
		}, "this returns a view into %s, which the row does not name", origin.param.name)
		return
	}

	c.errorHintf(offset, []string{
		"the storage is this frame's, and the caller outlives it",
		"return an owning Array<T> or String, or take the storage as a parameter and name it in the row",
	}, "this returns a view into storage that ends with this function")
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

// callOrigin follows `buf.slice()` and `s.runes()` back to the receiver,
// which is the storage the returned view sits on. A call to a named function
// is not followed: what it borrows is its own row's business, and checking
// that across modules is a separate pass.
func (c *checker) callOrigin(call *ast.CallExpr) borrowOrigin {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return borrowOrigin{}
	}
	switch selector.Name {
	case "slice", "runes", "view":
		return c.originOf(selector.X)
	}
	return borrowOrigin{}
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
