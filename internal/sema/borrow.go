package sema

import (
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// Borrow checking. `T[]` and `string` are views over storage they do not
// own, and so is a closure, whose environment sits in the frame that wrote
// it. A function returning one has to say where the view came from. An
// effect row entry written `&xs` names a parameter; a row with no such entry
// says the result views nothing.
//
// A view does not have to be the result itself. A struct, a tuple, an `any`
// or a container can hold one, and then the aggregate carries whatever its
// parts carry: the same row, the same error.
//
// What this tracks is provenance, not lifetime: every local that can hold a
// view is either a view into a named parameter or backed by storage in this
// frame. Returning the second kind hands out a view of a frame that is about
// to go away, which is what gets reported.
//
// An allocator is a source too. A value allocated from `arena.allocator()`
// sits in that arena and goes with it, which is tracked beside the views.

// borrowOrigin is where a value came from. A value can view more than one
// parameter, since a call whose row names several sources returns a view
// into whichever argument it picked.
type borrowOrigin struct {
	// params are the parameters of the enclosing function the value views.
	params []*symbol
	// local marks storage created here: an array literal, a closure's
	// environment, or the buffer of an Array this frame owns.
	local bool
	// closure marks that some of that frame storage is a closure's
	// environment, which has a fix (keep) the others do not.
	closure bool
	// depth is how many loops deep the frame storage was written. A loop
	// body writes it again every iteration, in the same place.
	depth int
	// arenas are the arenas the value was allocated from.
	arenas []arenaRef
}

// arenaRef names an arena, or the allocator parameter standing for one, and
// the tick at which the value was allocated from it. An arena freed at a
// later tick has taken the value with it.
type arenaRef struct {
	sym  *symbol
	tick int
}

func (o borrowOrigin) known() bool {
	return len(o.params) > 0 || o.local || len(o.arenas) > 0
}

// merge combines the origins a value could have come from. Local storage is
// contagious: a view that might sit on this frame does, for checking. Of two
// allocations from one arena the older counts, since it dies first.
func (o borrowOrigin) merge(other borrowOrigin) borrowOrigin {
	merged := borrowOrigin{
		local:   o.local || other.local,
		closure: o.closure || other.closure,
		depth:   max(o.depth, other.depth),
	}
	merged.params = append(merged.params, o.params...)
	for _, sym := range other.params {
		if !containsSymbol(merged.params, sym) {
			merged.params = append(merged.params, sym)
		}
	}
	merged.arenas = append(merged.arenas, o.arenas...)
	for _, ref := range other.arenas {
		found := false
		for i := range merged.arenas {
			if merged.arenas[i].sym == ref.sym {
				merged.arenas[i].tick = min(merged.arenas[i].tick, ref.tick)
				found = true
			}
		}
		if !found {
			merged.arenas = append(merged.arenas, ref)
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

	if !c.fn.generic && len(sources) > 0 && !c.rowFitsResult(sources) {
		c.errorHintf(row.Offset, []string{
			"a borrow says what the result is a view into; this function returns " + describeType(c.fn.result),
		}, "this row borrows, but the result is not a view")
	}
	return sources
}

// rowFitsResult reports whether a row's sources can be borrowed by the
// result. A result that holds a view can borrow anything. An allocator or an
// arena is the other kind of source: whatever is allocated from it, of any
// type but a plain number, sits in it.
func (c *checker) rowFitsResult(sources map[*symbol]bool) bool {
	if holdsView(c.fn.result) {
		return true
	}
	if scalarType(c.fn.result) {
		return false
	}
	for sym := range sources {
		if !isAllocator(sym.typ) && !isArena(sym.typ) {
			return false
		}
	}
	return true
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
// `string` is excluded: it is itself a view, and `any` may hold one.
func scalarType(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	if !ok {
		return false
	}
	return basic.Kind != types.StringKind && basic.Kind != types.AnyKind
}

// checkReturnedBorrow reports a returned value whose storage the row did not
// account for. want is the type the function declared for this value, since
// a struct, a tuple or an `any` can hold the view just as a slice is one.
func (c *checker) checkReturnedBorrow(e ast.Expr, offset int, want types.Type) {
	if !c.borrowTracked() || want == nil {
		return
	}
	origin := c.originOf(e)
	if !origin.known() {
		return
	}
	if c.checkReturnedArena(origin, offset, want) {
		return
	}
	if !holdsView(want) {
		return
	}

	// Frame storage is the worse problem, so it is the one reported.
	if origin.local && origin.closure {
		c.errorHintf(offset, []string{
			"the closure's environment is this frame's, and the caller outlives it",
			"copy it out with keep(allocator, f) from mem, or write a closure that captures nothing",
		}, "this returns a closure that borrows this frame")
		return
	}
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

// borrowTracked reports whether the function being checked has borrows that
// can be judged. A closure body and a generic body before instantiation
// cannot be.
func (c *checker) borrowTracked() bool {
	return c.fn != nil && c.fn.borrowChecked
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
		return borrowOrigin{local: true, depth: len(c.loops)}

	case *ast.ClosureExpr:
		// A closure that captures something keeps it in a record in this
		// frame, so it borrows the frame like an array literal does. One
		// that captures nothing has no record.
		if len(c.closureCaptures[e]) > 0 || e.Async {
			return borrowOrigin{local: true, closure: true, depth: len(c.loops)}
		}
		return borrowOrigin{}

	case *ast.StructLit:
		// A struct carries whatever its fields carry. `View{ data: xs }`
		// with a frame-local xs holds frame storage; `Stack{ items:
		// Array<i32>::new() }` holds a heap buffer and is not this frame's
		// to lose. A field that cannot hold a view carries only the arenas
		// its value came from.
		strct, _ := c.typeOf(e).(*types.Struct)
		var origin borrowOrigin
		for i, field := range e.Fields {
			value := c.originOf(field.Value)
			if declared := structLitField(strct, i, field); declared != nil {
				value = restrictTo(value, declared.Type, true)
			}
			origin = origin.merge(value)
		}
		return origin

	case *ast.TupleLit:
		var origin borrowOrigin
		for _, elem := range e.Elements {
			origin = origin.merge(restrictTo(c.originOf(elem), c.typeOf(elem), false))
		}
		return origin

	case *ast.CastExpr:
		// `xs as any` is the same view, in a wrapper.
		return restrictTo(c.originOf(e.X), c.typeOf(e), false)

	case *ast.TupleIndexExpr:
		return c.originOf(e.X)

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

// typeOf is the type sema recorded for an expression, nil where it recorded
// none: inside a generic body checked before instantiation.
func (c *checker) typeOf(e ast.Expr) types.Type {
	if c.rec == nil {
		return nil
	}
	return c.rec.types[e]
}

// structLitField finds the declared field a literal's i-th entry fills.
func structLitField(strct *types.Struct, i int, field ast.FieldValue) *types.Field {
	if strct == nil {
		return nil
	}
	if field.Name != "" {
		return strct.FieldNamed(field.Name)
	}
	if i < len(strct.Fields) {
		return &strct.Fields[i]
	}
	return nil
}

// restrictTo keeps of an origin what a value of type t can hold. A type that
// holds no view cannot hold a frame's storage or a parameter's, but a value
// of any type except a number can sit in an arena. A fixed array held by
// value is itself frame storage, which composing a struct preserves and
// storing into one does not.
func restrictTo(o borrowOrigin, t types.Type, byValue bool) borrowOrigin {
	if t == nil {
		return o
	}
	if scalarType(t) {
		return borrowOrigin{}
	}
	if holdsView(t) {
		return o
	}
	if _, ok := t.(*types.Array); ok && byValue {
		return o
	}
	return borrowOrigin{arenas: o.arenas}
}

// callOrigin works out what a call's result views. A method named on the
// built-in surface sits on its receiver; a call to a named function is
// answered by that function's own row, which is what carries the fact across
// a call and across a module.
func (c *checker) callOrigin(call *ast.CallExpr) borrowOrigin {
	// An arena's allocator hands out blocks of that arena: what it
	// allocates goes with the arena, and with the arena it was cut from.
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok && c.isArenaMethod(call, selector, "allocator") {
		origin := borrowOrigin{}.merge(borrowOrigin{arenas: c.originOf(selector.X).arenas})
		if root := c.rootSymbol(selector.X); root != nil {
			origin = origin.merge(borrowOrigin{arenas: []arenaRef{{sym: root}}})
		}
		return c.restamp(origin)
	}

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
	return c.restamp(origin)
}

// restamp dates the arenas of a freshly computed origin now. A call that
// allocates from an allocator allocates at this moment, however long ago the
// allocator itself was made.
func (c *checker) restamp(o borrowOrigin) borrowOrigin {
	if len(o.arenas) == 0 {
		return o
	}
	c.tick++
	for i := range o.arenas {
		o.arenas[i].tick = c.tick
	}
	return o
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
		sym.origin = borrowOrigin{local: true, depth: len(c.loops)}
		return
	}
	// Anything holding storage can be the root of a view, a struct included,
	// so a local carries wherever its value came from.
	if scalarType(typ) {
		return
	}
	sym.origin = c.originOf(value)
}

// holdsView reports whether a value of the type can be, or can hold, a view
// over storage it does not own: a slice, a string, a closure, an `any`, and
// any struct, tuple, enum or optional with one inside, type arguments
// included, so an `Array<fn(): i32>` does.
func holdsView(t types.Type) bool {
	return holdsViewIn(t, map[types.Type]bool{})
}

func holdsViewIn(t types.Type, seen map[types.Type]bool) bool {
	switch t := t.(type) {
	case *types.Slice, *types.Func:
		return true
	case *types.Basic:
		return t.Kind == types.StringKind || t.Kind == types.AnyKind
	case *types.Optional:
		return holdsViewIn(t.Elem, seen)
	case *types.Array:
		return holdsViewIn(t.Elem, seen)
	case *types.Future:
		return holdsViewIn(t.Result, seen)
	case *types.Tuple:
		for _, elem := range t.Elems {
			if holdsViewIn(elem, seen) {
				return true
			}
		}
	case *types.Struct:
		if seen[t] {
			return false
		}
		seen[t] = true
		for _, field := range t.Fields {
			if holdsViewIn(field.Type, seen) {
				return true
			}
		}
		for _, arg := range t.Args {
			if holdsViewIn(arg, seen) {
				return true
			}
		}
	case *types.Enum:
		if seen[t] {
			return false
		}
		seen[t] = true
		for _, variant := range t.Variants {
			for _, payload := range variant.Payload {
				if holdsViewIn(payload, seen) {
					return true
				}
			}
		}
		for _, arg := range t.Args {
			if holdsViewIn(arg, seen) {
				return true
			}
		}
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

// checkKeep rejects keep and release of something that is not a function
// value: only a fn has an environment to copy.
func (c *checker) checkKeep(call *ast.CallExpr) {
	name := c.intrinsicCall(call)
	if (name != "keep" && name != "release") || len(call.Args) < 2 {
		return
	}
	t := c.rec.types[call.Args[1].Value]
	if t == nil || flexible(t) {
		return
	}
	if _, ok := t.(*types.Func); ok {
		return
	}
	if _, ok := t.(*types.TypeParam); ok {
		return
	}
	c.errorHintf(call.Args[1].Value.Pos(), []string{
		"keep copies a closure's environment; a code pointer, struct or number has none",
	}, "%s takes a function value; %s is not one", name, describeType(t))
}
