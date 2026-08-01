package sema

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// callee is what a call expression resolved to: a function or method, a
// plain function value, or an enum variant constructor. bad means the
// error is already reported and the arguments should be typed leniently.
type callee struct {
	name   string
	method *types.Method
	// ownerParams and ownerArgs carry the method owner's generics. A nil
	// ownerArgs with non-empty ownerParams means the owner's arguments are
	// inferred from the call, as in Mutex::new(0).
	ownerParams []*types.TypeParam
	ownerArgs   []types.Type

	fnType *types.Func

	variantEnum *types.Enum
	variant     *types.Variant

	bad bool
}

func (c *checker) callExpr(call *ast.CallExpr) types.Type {
	target := c.resolveCallee(call)

	if target.variant != nil {
		return c.constructVariant(call, target)
	}
	if target.method != nil {
		return c.checkCall(call, target)
	}
	if target.fnType != nil {
		return c.checkPlainCall(call, target.fnType, target.name)
	}

	// Already reported; type the arguments so their names still resolve.
	for _, arg := range call.Args {
		c.expr(arg.Value)
	}
	for _, arg := range call.TypeArgs {
		if arg.Type != nil {
			c.resolveType(arg.Type)
		} else if arg.Expr != nil {
			c.expr(arg.Expr)
		}
	}
	return types.Unknown
}

// ---------------------------------------------------------------------------
// Callee resolution
// ---------------------------------------------------------------------------

func (c *checker) resolveCallee(call *ast.CallExpr) callee {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return c.calleeFromIdent(fun)
	case *ast.SelectorExpr:
		return c.calleeFromSelector(fun)
	case *ast.Path:
		return c.calleeFromPathee(fun.Offset, c.resolvePathee(fun))
	}

	t := c.expr(call.Fun)
	if fn, ok := t.(*types.Func); ok {
		return callee{fnType: fn, name: "the function value"}
	}
	if !flexible(t) {
		c.errorf(call.Fun.Pos(), "%s is not callable", describeType(t))
	}
	return callee{bad: true}
}

func (c *checker) calleeFromIdent(fun *ast.Ident) callee {
	sym := c.resolveIdent(fun)
	if sym == nil {
		return callee{bad: true}
	}

	switch sym.kind {
	case symType:
		c.errorf(fun.Offset, "%s is a type; a literal is written %s{ ... } and an associated function %s::name(...)",
			fun.Name, fun.Name, fun.Name)
		return callee{bad: true}
	case symNamespace:
		c.errorf(fun.Offset, "%s is a module, not a function", fun.Name)
		return callee{bad: true}
	case symTypeParam:
		c.errorf(fun.Offset, "%s is a type parameter, not a function", fun.Name)
		return callee{bad: true}
	}

	if sym.fn != nil {
		return callee{name: fun.Name, method: sym.fn}
	}
	if fn, ok := sym.typ.(*types.Func); ok {
		return callee{name: fun.Name, fnType: fn}
	}
	if sym.typ == nil || flexible(sym.typ) {
		return callee{bad: true}
	}
	c.errorf(fun.Offset, "%s is %s, which is not callable", fun.Name, sym.typ)
	return callee{bad: true}
}

func (c *checker) calleeFromSelector(fun *ast.SelectorExpr) callee {
	base := c.expr(fun.X)

	recv := base
	if pointer, ok := recv.(*types.Pointer); ok {
		recv = pointer.Elem
	}
	if optional, ok := recv.(*types.Optional); ok {
		c.errorf(fun.Offset, "this value is %s and may be null; check it against null before using it", optional)
		return callee{bad: true}
	}
	if flexible(recv) {
		return callee{bad: true}
	}

	// A field of function type wins over a method of the same name.
	if strct, ok := recv.(*types.Struct); ok {
		if field := strct.FieldNamed(fun.Name); field != nil {
			if fn, ok := field.Type.(*types.Func); ok {
				c.checkFieldVisibility(fun.Offset, strct, field)
				return callee{name: fun.Name, fnType: fn}
			}
		}
	}

	method, ownerParams, ownerArgs := c.lookupMethodFull(recv, fun.Name)
	if method == nil {
		c.errorf(fun.Offset, "%s has no method %s", recv, fun.Name)
		return callee{bad: true}
	}
	if method.Recv == types.NoRecv {
		c.errorf(fun.Offset, "%s is an associated function; call it as %s::%s(...)", fun.Name, recv, fun.Name)
		return callee{bad: true}
	}
	return callee{name: fun.Name, method: method, ownerParams: ownerParams, ownerArgs: ownerArgs}
}

func (c *checker) calleeFromPathee(offset int, p pathee) callee {
	if p.bad {
		return callee{bad: true}
	}
	if p.variant != nil {
		if len(p.variant.Payload) == 0 {
			c.errorf(offset, "%s::%s carries no payload; write it without parentheses", p.variantEnum.Name, p.variant.Name)
			return callee{bad: true}
		}
		return callee{variantEnum: p.variantEnum, variant: p.variant}
	}
	if p.method != nil {
		if p.method.Recv != types.NoRecv {
			c.errorf(offset, "%s takes a receiver; call it on a value: value.%s(...)", p.method.Name, p.method.Name)
			return callee{bad: true}
		}
		return callee{name: p.method.Name, method: p.method, ownerParams: p.ownerParams, ownerArgs: p.ownerArgs}
	}
	if fn, ok := p.valueType.(*types.Func); ok {
		return callee{name: "the function", fnType: fn}
	}
	if p.typ != nil {
		c.errorf(offset, "%s is a type, not a function", p.typ)
		return callee{bad: true}
	}
	if p.valueType != nil && !flexible(p.valueType) {
		c.errorf(offset, "%s is not callable", p.valueType)
	}
	return callee{bad: true}
}

// ---------------------------------------------------------------------------
// Call checking
// ---------------------------------------------------------------------------

func (c *checker) checkPlainCall(call *ast.CallExpr, sig *types.Func, name string) types.Type {
	if len(call.TypeArgs) > 0 {
		c.errorf(call.Offset, "%s is not generic; it takes no type arguments", name)
	}
	c.checkArity(call, sig, name)

	for i, arg := range call.Args {
		c.checkNamedArg(arg)
		if i < len(sig.Params) {
			c.assignExpr(arg.Value, sig.Params[i], argContext(i, name))
		} else {
			c.expr(arg.Value)
		}
	}
	c.recordCall(call, &CallInfo{Sig: sig})
	return futureWrap(sig)
}

func futureWrap(sig *types.Func) types.Type {
	if sig.Async {
		return &types.Future{Result: sig.Result}
	}
	return sig.Result
}

func argContext(index int, name string) string {
	return "argument " + strconv.Itoa(index+1) + " to " + name
}

func (c *checker) checkArity(call *ast.CallExpr, sig *types.Func, name string) {
	got := len(call.Args)
	if got >= sig.Required && got <= len(sig.Params) {
		return
	}
	if sig.Required == len(sig.Params) {
		c.errorf(call.Offset, "%s takes %d argument(s), got %d", name, len(sig.Params), got)
		return
	}
	c.errorf(call.Offset, "%s takes %d to %d arguments, got %d", name, sig.Required, len(sig.Params), got)
}

func (c *checker) checkNamedArg(arg ast.Arg) {
	if arg.Name != "" {
		c.errorf(arg.Offset, "arguments are positional; only decorator arguments take names")
	}
}

// checkCall handles a possibly generic function or method call: explicit
// type arguments or inference, argument checking against the substituted
// signature, and the duck-typed instantiation check of the body.
func (c *checker) checkCall(call *ast.CallExpr, target callee) types.Type {
	method := target.method
	sig := method.Sig
	c.checkArity(call, sig, target.name)

	s := subst{}
	inferOwner := target.ownerArgs == nil && len(target.ownerParams) > 0
	if !inferOwner {
		for i, param := range target.ownerParams {
			if i < len(target.ownerArgs) {
				s[param] = target.ownerArgs[i]
			}
		}
	}

	c.applyExplicitTypeArgs(call, method, s)

	// Type the arguments: concrete parameters check directly, parameters
	// mentioning free type parameters infer them from the argument.
	poisoned := false
	for i, arg := range call.Args {
		c.checkNamedArg(arg)
		if i >= len(sig.Params) {
			c.expr(arg.Value)
			continue
		}

		pattern := c.substType(sig.Params[i], s)
		if !containsTypeParam(pattern) {
			c.assignExpr(arg.Value, pattern, argContext(i, target.name))
			continue
		}

		argType := types.Default(c.expr(arg.Value))
		if flexible(argType) {
			poisoned = true
			continue
		}
		if !unify(pattern, argType, s) {
			c.errorf(arg.Value.Pos(), "cannot use %s as %s in %s",
				describeType(argType), c.substType(pattern, s), argContext(i, target.name))
			poisoned = true
		}
	}

	result := c.substType(sig.Result, s)

	// Inside a generic body the type parameters are still opaque, and a
	// poisoned argument already carries its own error; the inference
	// errors and the duck check belong to clean, concrete calls.
	var bindings map[string]types.Type
	if !c.lenient && !poisoned {
		bindings = c.collectBindings(call, method, target, s)
		if bindings != nil {
			c.instantiate(method.Decl, bindings, instantiationLabel(target.name, method, bindings), call.Offset)
		}
	}

	substituted, _ := c.substType(sig, s).(*types.Func)
	c.recordCall(call, &CallInfo{Method: method, Sig: substituted, Bindings: bindings})

	if sig.Async {
		return &types.Future{Result: result}
	}
	return result
}

func (c *checker) applyExplicitTypeArgs(call *ast.CallExpr, method *types.Method, s subst) {
	var explicit []types.Type
	for _, arg := range call.TypeArgs {
		if arg.Type != nil {
			explicit = append(explicit, c.resolveType(arg.Type))
		} else if arg.Expr != nil {
			// A constant launch configuration, as in kernel<1, 8>(...);
			// nothing to bind, but the names must resolve.
			c.expr(arg.Expr)
		}
	}
	if len(explicit) == 0 {
		return
	}

	if len(explicit) != len(method.TypeParams) {
		c.errorf(call.Offset, "%s takes %d type argument(s), got %d",
			method.Name, len(method.TypeParams), len(explicit))
	}
	for i, param := range method.TypeParams {
		if i < len(explicit) {
			s[param] = explicit[i]
		}
	}
}

// collectBindings turns the substitution into by-name bindings for the
// declaration's parameters, reporting what could not be inferred. It
// returns nil when there is nothing generic to check.
func (c *checker) collectBindings(call *ast.CallExpr, method *types.Method, target callee, s subst) map[string]types.Type {
	decl := method.Decl
	if decl == nil || (len(method.TypeParams) == 0 && len(target.ownerParams) == 0) {
		return nil
	}

	bindings := map[string]types.Type{}

	assign := func(name string, param *types.TypeParam) {
		bound, ok := s[param]
		if !ok {
			c.errorf(call.Offset, "cannot infer %s for %s from these arguments; write %s<...> explicitly",
				param.Name, method.Name, method.Name)
			bound = types.Unknown
		}
		bindings[name] = bound
	}

	for i, param := range decl.OwnerParams {
		if i < len(target.ownerParams) {
			assign(param.Name, target.ownerParams[i])
		}
	}
	for i, param := range decl.TypeParams {
		if i < len(method.TypeParams) {
			assign(param.Name, method.TypeParams[i])
		}
	}
	return bindings
}

func instantiationLabel(name string, method *types.Method, bindings map[string]types.Type) string {
	label := name + "<"
	first := true
	for _, param := range append(append([]ast.TypeParam{}, method.Decl.OwnerParams...), method.Decl.TypeParams...) {
		if !first {
			label += ", "
		}
		first = false
		if bound, ok := bindings[param.Name]; ok {
			label += bound.String()
		}
	}
	return label + ">"
}

// ---------------------------------------------------------------------------
// Variant construction
// ---------------------------------------------------------------------------

func (c *checker) constructVariant(call *ast.CallExpr, target callee) types.Type {
	variant := target.variant
	if len(call.Args) != len(variant.Payload) {
		c.errorf(call.Offset, "%s::%s carries %d value(s), got %d",
			target.variantEnum.Name, variant.Name, len(variant.Payload), len(call.Args))
	}
	for i, arg := range call.Args {
		c.checkNamedArg(arg)
		if i < len(variant.Payload) {
			c.assignExpr(arg.Value, variant.Payload[i], "the payload of "+variant.Name)
		} else {
			c.expr(arg.Value)
		}
	}
	c.recordCall(call, &CallInfo{Enum: target.variantEnum, Variant: variantIndex(target.variantEnum, variant)})
	return target.variantEnum
}

func variantIndex(enum *types.Enum, variant *types.Variant) int {
	for i := range enum.Variants {
		if &enum.Variants[i] == variant {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Method lookup
// ---------------------------------------------------------------------------

// lookupMethod finds a method on a type; the substitution carries the
// owner's type arguments for a generic instance.
func (c *checker) lookupMethod(t types.Type, name string) (*types.Method, subst) {
	method, params, args := c.lookupMethodFull(t, name)
	if method == nil {
		return nil, nil
	}
	s := subst{}
	for i, param := range params {
		if i < len(args) {
			s[param] = args[i]
		}
	}
	return method, s
}

func (c *checker) lookupMethodFull(t types.Type, name string) (*types.Method, []*types.TypeParam, []types.Type) {
	switch t := t.(type) {
	case *types.Struct:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		if method, ok := origin.Methods[name]; ok {
			return method, origin.TypeParams, t.Args
		}
	case *types.Enum:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		if method, ok := origin.Methods[name]; ok {
			return method, origin.TypeParams, t.Args
		}
	}

	if method := builtinMethod(t, name); method != nil {
		return method, nil, nil
	}
	return nil, nil, nil
}

// builtinMethod is the method surface of the built-in types, which needs no
// import: close on channels, len on the sliceable types, runes on strings,
// compare on the ordered primitives, message on error.
func builtinMethod(t types.Type, name string) *types.Method {
	valueMethod := func(sig *types.Func) *types.Method {
		return &types.Method{Name: name, Recv: types.ValueRecv, Sig: sig}
	}

	switch t := t.(type) {
	case *types.Chan:
		if name == "close" {
			return valueMethod(&types.Func{Result: types.Void})
		}
	case *types.Slice:
		if name == "len" {
			return valueMethod(&types.Func{Result: types.USize})
		}
	case *types.Array:
		if name == "len" {
			return valueMethod(&types.Func{Result: types.USize})
		}
	case *types.Basic:
		switch {
		case t.Kind == types.StringKind && name == "len":
			return valueMethod(&types.Func{Result: types.USize})
		case t.Kind == types.StringKind && name == "runes":
			return valueMethod(&types.Func{Result: &types.Slice{Elem: types.Rune}})
		case t.Kind == types.ErrorKind && name == "message":
			return valueMethod(&types.Func{Result: types.String})
		case name == "compare" && types.IsOrdered(t) && !types.IsUntyped(t):
			return valueMethod(&types.Func{Params: []types.Type{t}, Required: 1, Result: types.Int32})
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

// pathee is what a `::` path names.
type pathee struct {
	valueType types.Type
	typ       types.Type

	method      *types.Method
	ownerParams []*types.TypeParam
	// ownerArgs is nil when the owner is generic and its arguments must be
	// inferred from the call.
	ownerArgs []types.Type
	ownerName string

	variantEnum *types.Enum
	variant     *types.Variant

	bad bool
}

func (c *checker) resolvePathee(path *ast.Path) pathee {
	head := path.Segments[0]
	if head.Name == "<error>" {
		return pathee{bad: true}
	}

	sym := c.scope.lookup(head.Name)
	if sym == nil {
		c.errorf(head.Offset, "undeclared name %s; there is no universe scope, so it must be declared or imported", head.Name)
		return pathee{bad: true}
	}

	switch sym.kind {
	case symImport:
		// Opaque import: everything reached through it is unchecked.
		return pathee{valueType: types.Unknown}
	case symNamespace:
		return c.namespacePathee(path, sym)
	case symType:
		return c.typePathee(path, 0, sym.typ)
	case symTypeParam:
		c.errorf(head.Offset, "%s is a type parameter; it has no members", head.Name)
		return pathee{bad: true}
	}

	// A value: only a plain single-segment path makes sense.
	if len(path.Segments) > 1 {
		c.errorf(path.Segments[1].Offset, "%s is a value; :: reaches into modules and types", head.Name)
		return pathee{bad: true}
	}
	return pathee{valueType: c.symbolValueType(sym, head.Offset, head.Name)}
}

func (c *checker) namespacePathee(path *ast.Path, ns *symbol) pathee {
	if len(path.Segments) < 2 {
		c.errorf(path.Offset, "%s is a module, not a value", path.Segments[0].Name)
		return pathee{bad: true}
	}
	if ns.exports == nil {
		return pathee{valueType: types.Unknown}
	}

	segment := path.Segments[1]
	entry, ok := ns.exports.names[segment.Name]
	if !ok {
		c.errorf(segment.Offset, "%s does not export %s", path.Segments[0].Name, segment.Name)
		return pathee{bad: true}
	}

	switch entry.kind {
	case symType:
		return c.typePathee(path, 1, entry.typ)
	default:
		if len(path.Segments) > 2 {
			c.errorf(path.Segments[2].Offset, "%s::%s is a value; :: reaches into modules and types",
				path.Segments[0].Name, segment.Name)
			return pathee{bad: true}
		}
		c.recordRef(path, entry.decl)
		if entry.fn != nil {
			return pathee{method: entry.fn}
		}
		return pathee{valueType: entry.typ}
	}
}

// typePathee resolves the part of a path that starts at a type segment:
// Type, Type::assoc, Enum::Variant.
func (c *checker) typePathee(path *ast.Path, index int, named types.Type) pathee {
	segment := path.Segments[index]
	args := c.resolveTypeArgs(segment.Args)

	rest := path.Segments[index+1:]
	if len(rest) == 0 {
		if len(args) > 0 {
			named = c.applyTypeArgs(named, segment.Offset, args)
		}
		return pathee{typ: named}
	}
	member := rest[0]
	if len(rest) > 1 {
		c.errorf(rest[1].Offset, "%s::%s cannot be reached into further", segment.Name, member.Name)
		return pathee{bad: true}
	}

	switch t := named.(type) {
	case *types.Struct:
		return c.structMemberPathee(segment, member, t, args)
	case *types.Enum:
		return c.enumMemberPathee(segment, member, t, args)
	}
	if named == nil || flexible(named) {
		return pathee{valueType: types.Unknown}
	}
	c.errorf(member.Offset, "%s has no members; only structs and enums do", named)
	return pathee{bad: true}
}

func (c *checker) structMemberPathee(segment, member ast.PathSegment, strct *types.Struct, args []types.Type) pathee {
	if len(args) > 0 {
		instantiated := c.applyTypeArgs(strct, segment.Offset, args)
		instance, ok := instantiated.(*types.Struct)
		if !ok {
			return pathee{bad: true}
		}
		strct = instance
	}

	origin := strct
	if strct.Origin != nil {
		origin = strct.Origin
	}
	method, ok := origin.Methods[member.Name]
	if !ok {
		c.errorf(member.Offset, "%s has no associated function %s", origin.Name, member.Name)
		return pathee{bad: true}
	}

	p := pathee{method: method, ownerParams: origin.TypeParams, ownerName: origin.Name}
	if strct.Origin != nil {
		p.ownerArgs = strct.Args
	} else if len(origin.TypeParams) == 0 {
		p.ownerArgs = []types.Type{}
	}
	return p
}

func (c *checker) enumMemberPathee(segment, member ast.PathSegment, enum *types.Enum, args []types.Type) pathee {
	if len(args) > 0 {
		instantiated := c.applyTypeArgs(enum, segment.Offset, args)
		instance, ok := instantiated.(*types.Enum)
		if !ok {
			return pathee{bad: true}
		}
		enum = instance
	}

	if variant := enum.VariantNamed(member.Name); variant != nil {
		return pathee{variantEnum: enum, variant: variant}
	}

	origin := enum
	if enum.Origin != nil {
		origin = enum.Origin
	}
	if method, ok := origin.Methods[member.Name]; ok {
		p := pathee{method: method, ownerParams: origin.TypeParams, ownerName: origin.Name}
		if enum.Origin != nil {
			p.ownerArgs = enum.Args
		} else if len(origin.TypeParams) == 0 {
			p.ownerArgs = []types.Type{}
		}
		return p
	}

	c.errorf(member.Offset, "%s has no variant %s", origin.Name, member.Name)
	return pathee{bad: true}
}

// pathValue types a path in value position.
func (c *checker) pathValue(path *ast.Path) types.Type {
	p := c.resolvePathee(path)
	switch {
	case p.bad:
		return types.Unknown
	case p.valueType != nil:
		return p.valueType
	case p.variant != nil:
		if len(p.variant.Payload) > 0 {
			c.errorf(path.Offset, "%s::%s carries a payload; construct it with its values",
				p.variantEnum.Name, p.variant.Name)
			return types.Unknown
		}
		return p.variantEnum
	case p.method != nil:
		if len(p.method.TypeParams) > 0 || len(p.ownerParams) > 0 {
			c.errorf(path.Offset, "%s is generic; it must be called so its type parameters can be inferred", p.method.Name)
			return types.Unknown
		}
		if p.method.Recv != types.NoRecv {
			c.errorf(path.Offset, "%s takes a receiver; call it on a value", p.method.Name)
			return types.Unknown
		}
		c.recordRef(path, p.method.Decl)
		return p.method.Sig
	case p.typ != nil:
		c.errorf(path.Offset, "%s is a type, not a value", p.typ)
		return types.Unknown
	}
	return types.Unknown
}
