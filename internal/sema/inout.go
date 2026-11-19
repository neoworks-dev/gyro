package sema

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// Changing a value. A plain parameter borrows its argument: the function may
// read it and may not change it, and that includes `self`. `inout` borrows
// exclusively, so the function may change the argument in place, and nothing
// else may see it for as long as the call runs. A `const` never changes.
//
// What counts as a change is anything that writes the value or a part of it:
// an assignment to it, to a field or an element of it, or a call that takes
// it as `inout`, a method's `inout self` among them. A generic body checked
// before instantiation has no types to tell a view from a value, so there a
// part of a variable is given the benefit of the doubt, and the
// instantiation judges it.

// faultKind is why a place cannot be changed.
type faultKind int

const (
	faultNone faultKind = iota
	// faultConst is a place inside a `const`.
	faultConst
	// faultBorrowed is a place inside a plain parameter.
	faultBorrowed
	// faultReceiver is a place inside a method's plain `self`.
	faultReceiver
	// faultTemporary is a value no variable holds, so a change to it would
	// be lost.
	faultTemporary
)

// fault is the reason a place cannot be changed, and the name it is rooted
// at.
type fault struct {
	kind faultKind
	name string
}

// changeFault says why e cannot be changed, or faultNone when it can. A
// chain of fields and elements is judged by what it is rooted at, unless it
// passes through a pointer, a slice or a trait value: those view storage
// somebody else owns, and writing through one changes that storage rather
// than the binding that holds the view.
func (c *checker) changeFault(e ast.Expr) fault {
	switch e := e.(type) {
	case *ast.Ident:
		sym := c.scope.lookup(e.Name)
		if sym == nil || sym.kind != symValue {
			return fault{}
		}
		return symbolFault(sym, e.Name)
	case *ast.SelfExpr:
		if c.fn != nil && c.fn.selfBorrowed {
			return fault{faultReceiver, "self"}
		}
		return fault{}
	case *ast.SelectorExpr:
		return c.projectionFault(e.X)
	case *ast.IndexExpr:
		return c.projectionFault(e.X)
	case *ast.TupleIndexExpr:
		return c.projectionFault(e.X)
	case *ast.UnaryExpr:
		if e.Op == token.Star {
			return fault{}
		}
	}
	return fault{kind: faultTemporary}
}

// projectionFault judges a part of base: through a view it is the view's
// storage that changes, otherwise it is base itself.
func (c *checker) projectionFault(base ast.Expr) fault {
	t := c.typeOf(base)
	if t == nil || viewsStorage(t) {
		return fault{}
	}
	return c.changeFault(base)
}

func symbolFault(sym *symbol, name string) fault {
	if decl, ok := sym.decl.(*ast.VarDecl); ok && decl.Const {
		return fault{faultConst, name}
	}
	if sym.param && !sym.inout && !sym.take {
		return fault{faultBorrowed, name}
	}
	return fault{}
}

// viewsStorage reports a type whose values look at storage they do not own,
// so that a write through one is not a write to the value holding it. A type
// nobody recorded, which is every one in a generic body before it is
// instantiated, is given the benefit of the doubt.
func viewsStorage(t types.Type) bool {
	if t == nil {
		return true
	}
	if optional, ok := t.(*types.Optional); ok {
		t = optional.Elem
	}
	switch t.(type) {
	case *types.Pointer, *types.Slice, *types.Dyn:
		return true
	}
	return types.IsUnknown(t)
}

// checkChangeable reports a change to a place that cannot take one.
func (c *checker) checkChangeable(e ast.Expr) {
	c.reportFault(e.Pos(), c.changeFault(e))
}

func (c *checker) reportFault(offset int, f fault) {
	switch f.kind {
	case faultConst:
		c.errorHintf(offset, []string{
			"declare " + f.name + " with let if it has to change",
		}, "%s cannot be changed: %s", f.name, "it is a const")
	case faultBorrowed:
		c.errorHintf(offset, []string{
			"write " + f.name + ": inout T to change the caller's value, or copy it into a let first",
		}, "%s cannot be changed: %s", f.name, "a plain parameter only borrows its argument")
	case faultReceiver:
		c.errorHintf(offset, []string{
			"a method that changes its receiver takes inout self",
		}, "%s cannot be changed: %s", f.name, "a plain self only borrows the receiver")
	case faultTemporary:
		c.errorHintf(offset, []string{
			"keep the value in a let, and change that",
		}, "%s cannot be changed: %s", "a temporary value", "nothing holds it once the statement ends")
	}
}

// ---------------------------------------------------------------------------
// Inout arguments
// ---------------------------------------------------------------------------

// pass is one value a call hands over: the receiver or an argument.
type pass struct {
	expr     ast.Expr
	inout    bool
	receiver bool
	// what names it in a diagnostic: "argument 2 to sort", "the receiver
	// of bump".
	what string
}

// passesOf lists the receiver and arguments of a call to a declared
// function, in the order they are evaluated.
func passesOf(call *ast.CallExpr, target callee) []pass {
	method := target.method
	var passes []pass
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok && method.Recv != types.NoRecv {
		passes = append(passes, pass{
			expr:     selector.X,
			inout:    method.Recv == types.InoutRecv,
			receiver: true,
			what:     "the receiver of " + target.name,
		})
	}
	for i, arg := range call.Args {
		passes = append(passes, pass{
			expr:  arg.Value,
			inout: method.InoutAt(i),
			what:  "argument " + strconv.Itoa(i+1) + " to " + target.name,
		})
	}
	return passes
}

// checkInoutCall holds a call to what `inout` asks: each inout pass is a
// place that can change, and no value is passed twice when one of the
// passes is inout.
func (c *checker) checkInoutCall(call *ast.CallExpr, target callee) {
	if target.method == nil {
		return
	}
	passes := passesOf(call, target)
	for _, p := range passes {
		if p.inout {
			c.checkInoutPlace(p)
		}
	}
	// Whether a pass is a copy depends on its type.
	if !c.lenient {
		c.checkExclusive(passes, target.name)
	}
}

// checkInoutPlace reports an inout pass that is not a place that can
// change. A receiver that is a pointer is already the place: the method
// changes what it points at.
func (c *checker) checkInoutPlace(p pass) {
	if p.receiver && viewsStorage(c.typeOf(p.expr)) {
		return
	}
	f := c.changeFault(p.expr)
	if f.kind == faultTemporary {
		c.errorHintf(p.expr.Pos(), []string{
			"an inout parameter changes the caller's variable, so the argument has to name one",
		}, "%s is inout, so it must be a variable that can change, not a temporary value", p.what)
		return
	}
	c.reportFault(p.expr.Pos(), f)
}

// place names storage for the overlap check: the variable it sits in, and
// the fields below it. An element or a deref ends the path with "", since
// which element it is cannot be known here, so the whole container counts.
type place struct {
	root any
	path []string
}

// selfRoot stands for the receiver, which is no scope symbol.
const selfRoot = "self"

func (c *checker) placeOf(e ast.Expr) (place, bool) {
	switch e := e.(type) {
	case *ast.Ident:
		sym := c.scope.lookup(e.Name)
		if sym == nil || sym.kind != symValue {
			return place{}, false
		}
		return place{root: sym}, true
	case *ast.SelfExpr:
		return place{root: selfRoot}, true
	case *ast.SelectorExpr:
		return c.partOf(e.X, e.Name)
	case *ast.TupleIndexExpr:
		return c.partOf(e.X, "."+strconv.Itoa(e.Index))
	case *ast.IndexExpr:
		return c.partOf(e.X, "")
	case *ast.UnaryExpr:
		if e.Op == token.Star {
			return c.partOf(e.X, "")
		}
	}
	return place{}, false
}

// partOf is the place of a part of base. Nothing below an element is told
// apart from the element.
func (c *checker) partOf(base ast.Expr, part string) (place, bool) {
	p, ok := c.placeOf(base)
	if !ok {
		return place{}, false
	}
	if n := len(p.path); n > 0 && p.path[n-1] == "" {
		return p, true
	}
	p.path = append(p.path[:len(p.path):len(p.path)], part)
	return p, true
}

// overlaps reports whether two places can be the same storage: one is the
// other, or a part of it.
func (a place) overlaps(b place) bool {
	if a.root != b.root {
		return false
	}
	for i := range min(len(a.path), len(b.path)) {
		if a.path[i] != b.path[i] && a.path[i] != "" && b.path[i] != "" {
			return false
		}
	}
	return true
}

// checkExclusive reports a value passed twice to one call when one of the
// passes is inout: the callee could then change it through one name while
// reading it through the other.
func (c *checker) checkExclusive(passes []pass, name string) {
	for j := 1; j < len(passes); j++ {
		for i := range j {
			if !passes[i].inout && !passes[j].inout || c.copied(passes[i]) || c.copied(passes[j]) {
				continue
			}
			a, okA := c.placeOf(passes[i].expr)
			b, okB := c.placeOf(passes[j].expr)
			if okA && okB && a.overlaps(b) {
				c.errorHintf(passes[j].expr.Pos(), []string{
					"an inout parameter is exclusive for the length of the call; pass a copy for the other",
				}, "%s and %s overlap, and one of them is inout in this call to %s", passes[i].what, passes[j].what, name)
				return
			}
		}
	}
}

// copied reports a plain pass of a number: the callee gets its own copy, so
// nothing it does to another pass can reach it.
func (c *checker) copied(p pass) bool {
	t := c.typeOf(p.expr)
	return !p.inout && t != nil && scalarType(types.Default(t))
}

// refuseInoutValue reports a function with an inout parameter written as a
// value. The argument is passed by address, which a function value's
// signature cannot say, so such a function is only ever called by name.
func (c *checker) refuseInoutValue(offset int, method *types.Method) bool {
	if method.HasTakeParam() {
		c.errorHintf(offset, []string{
			"call it directly, or wrap the call in a closure",
		}, "%s takes ownership of a parameter, so it can only be called, not used as a value", method.Name)
		return true
	}
	if !method.HasInoutParam() {
		return false
	}
	c.errorHintf(offset, []string{
		"call it directly, or wrap the call in a closure",
	}, "%s takes an inout parameter, so it can only be called, not used as a value", method.Name)
	return true
}
