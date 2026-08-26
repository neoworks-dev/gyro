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
		sym := c.borrowSource(borrow)
		if sym == nil {
			continue
		}
		sources[sym] = true
	}

	if !c.fn.generic && !viewType(c.fn.result) && len(sources) > 0 {
		c.errorHintf(row.Offset, []string{
			"a borrow says what the result is a view into; this function returns " + describeType(c.fn.result),
		}, "this row borrows, but the result is not a view")
	}
	return sources
}

// borrowSource resolves one `&name` entry. `&self` names a method's
// receiver, which is not a scope symbol but is every bit as much a place a
// view can sit on.
func (c *checker) borrowSource(borrow ast.Borrow) *symbol {
	if borrow.Name == "self" {
		if c.fn.selfSymbol == nil {
			c.errorHintf(borrow.Offset, []string{
				"&self borrows a method's receiver; this function has none",
			}, "&%s names no parameter of this function", borrow.Name)
			return nil
		}
		return c.fn.selfSymbol
	}

	sym := c.scope.lookup(borrow.Name)
	if sym == nil || sym.kind != symValue {
		c.errorHintf(borrow.Offset, []string{
			"a borrow names a parameter of this function, as in fn f(xs: i32[]): i32[] !{ &xs }",
			"a method's receiver is named &self",
		}, "&%s names no parameter of this function", borrow.Name)
		return nil
	}
	// A source is anything a view can sit on: a slice or string directly, a
	// struct or array that owns storage, a pointer to one. A number holds
	// nothing a view could point into — except on a generic, where the same
	// row has to serve `T = string` and `T = i32`, and being vacuous at one
	// of them is the arrangement rather than a mistake.
	if !c.fn.generic && scalarType(sym.typ) {
		c.errorHintf(borrow.Offset, []string{
			"a view sits on storage; " + sym.typ.String() + " is a single value with none",
		}, "&%s borrows from %s, which is not a view", borrow.Name, sym.typ)
		return nil
	}
	return sym
}

// scalarType reports a type holding no storage a view could point into.
// `string` is excluded: it is itself a view.
func scalarType(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	if !ok {
		return false
	}
	return basic.Kind != types.StringKind
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

	case *ast.StructLit:
		// A struct carries whatever its fields carry. `View{ data: xs }`
		// with a frame-local xs holds frame storage; `Stack{ items:
		// Array<i32>::new() }` holds a heap buffer and is not this frame's
		// to lose.
		var origin borrowOrigin
		for _, field := range e.Fields {
			origin = origin.merge(c.originOf(field.Value))
		}
		return origin

	case *ast.SelfExpr:
		if c.fn == nil || c.fn.selfSymbol == nil {
			return borrowOrigin{}
		}
		return c.fn.selfSymbol.origin

	case *ast.SelectorExpr:
		// A field belongs to whatever holds it, so a chain roots at its
		// base: `self.items` is the receiver's storage, `s.items` is s's.
		return c.originOf(e.X)

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
	// A declared row answers for itself, including a user method that
	// happens to be named `slice` and does not lend anything out.
	if c.calleeDecl(call) != nil {
		return c.declaredCallOrigin(call)
	}

	// Nothing on the built-in method surface hands back a view of its
	// receiver: `runes()` and `bytes()` both decode into storage of their
	// own, since a slice element is wider than a UTF-8 byte.
	return borrowOrigin{}
}

// declaredCallOrigin maps a callee's `&param` entries onto the arguments
// this call passed for them. `pick(xs, ys)` with `!{ &a }` on pick views
// whatever xs views.
func (c *checker) declaredCallOrigin(call *ast.CallExpr) borrowOrigin {
	decl := c.calleeDecl(call)
	if decl == nil || decl.Effects == nil || len(decl.Effects.Borrows) == 0 {
		return borrowOrigin{}
	}

	var origin borrowOrigin
	for _, borrow := range decl.Effects.Borrows {
		// `&self` on a method points at the receiver, which is the value the
		// call was made on rather than one of the arguments.
		if borrow.Name == "self" {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				origin = origin.merge(c.originOf(selector.X))
			}
			continue
		}
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
	// the book calls out: the literal lives in the enclosing scope.
	if _, ok := typ.(*types.Array); ok {
		sym.origin = borrowOrigin{local: true}
		return
	}
	// Anything holding storage can be the root of a view, a struct included,
	// so a local carries wherever its value came from.
	if scalarType(typ) {
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
