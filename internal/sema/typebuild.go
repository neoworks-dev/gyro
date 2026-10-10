package sema

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// buildTypes fills the struct and enum shells created by collectModule,
// builds every function signature and attaches methods to their owners,
// then types the module-level values. Field types can point forward at any
// module type because the shells already exist.
func (c *checker) buildTypes(tree *ast.File) {
	for _, decl := range tree.Decls {
		switch d := decl.(type) {
		case *ast.StructDecl:
			c.buildStruct(d)
		case *ast.UnionDecl:
			c.buildUnion(d)
		case *ast.EnumDecl:
			c.buildEnum(d)
		}
	}

	// A trait's methods mention types, and a type may mention a trait as a
	// bound or a dyn, so the supertraits come first: a dyn needs to know
	// what its trait requires.
	for _, decl := range tree.Decls {
		if d, ok := decl.(*ast.TraitDecl); ok {
			c.buildTraitSupers(d)
		}
	}
	for _, decl := range tree.Decls {
		if d, ok := decl.(*ast.TraitDecl); ok {
			c.buildTraitAssoc(d)
		}
	}
	for _, decl := range tree.Decls {
		if d, ok := decl.(*ast.TraitDecl); ok {
			c.buildTraitMethods(d)
		}
	}

	// Which traits a type implements is known before any signature or body
	// mentions a bound, so a bound can be checked wherever it is written.
	for _, decl := range tree.Decls {
		if d, ok := decl.(*ast.ImplDecl); ok {
			c.registerImpl(d)
		}
	}

	for _, decl := range tree.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok {
			c.buildSignature(d)
		}
	}

	for _, decl := range tree.Decls {
		if d, ok := decl.(*ast.ImplDecl); ok {
			c.checkImpl(d)
		}
	}

	c.flushBounds()

	for _, decl := range tree.Decls {
		if d, ok := decl.(*ast.VarDecl); ok {
			c.moduleVar(d)
		}
	}
}

func (c *checker) buildStruct(decl *ast.StructDecl) {
	shell := c.structTypes[decl]
	c.pushScope(func() {
		c.bindTypeParams(decl.TypeParams, shell.TypeParams)
		for _, field := range decl.Fields {
			shell.Fields = append(shell.Fields, types.Field{
				Name:     field.Name,
				Type:     c.noFormat(field.Type.Pos(), c.resolveType(field.Type)),
				Exported: field.Exported,
			})
		}
	})
}

func (c *checker) buildUnion(decl *ast.UnionDecl) {
	shell := c.unionTypes[decl]
	for _, member := range decl.Members {
		shell.Members = append(shell.Members, types.Field{
			Name:     member.Name,
			Type:     c.resolveType(member.Type),
			Exported: member.Exported,
		})
	}
}

func (c *checker) buildEnum(decl *ast.EnumDecl) {
	shell := c.enumTypes[decl]
	c.pushScope(func() {
		c.bindTypeParams(decl.TypeParams, shell.TypeParams)
		for _, variant := range decl.Variants {
			payload := make([]types.Type, len(variant.Payload))
			for i, typ := range variant.Payload {
				payload[i] = c.resolveType(typ)
			}
			shell.Variants = append(shell.Variants, types.Variant{
				Name:    variant.Name,
				Payload: payload,
			})
		}
	})
}

// bindTypeParams declares generic parameter names, each bound to its
// canonical TypeParam object.
func (c *checker) bindTypeParams(params []ast.TypeParam, objects []*types.TypeParam) {
	for i, param := range params {
		sym := c.declare(param.Name, param.Offset, symTypeParam, nil)
		sym.typ = objects[i]
	}
	c.resolveBounds(params, objects)
}

// buildSignature builds a function's *types.Method, attaches it to its
// owner when it is a method, and registers the declaration's environment
// for instantiation checking.
func (c *checker) buildSignature(decl *ast.FuncDecl) {
	method := &types.Method{
		Name: decl.Name,
		Recv: recvKind(decl),
		Decl: decl,
	}
	c.program.envs[decl] = c
	c.funcSigs[decl] = method

	ownerParams := c.ownerTypeParams(decl)
	method.TypeParams = makeTypeParams(decl.TypeParams)

	c.pushScope(func() {
		c.bindTypeParams(decl.OwnerParams, ownerParams)
		c.bindTypeParams(decl.TypeParams, method.TypeParams)
		c.declareSelf(decl)
		method.Sig = c.buildFuncSig(decl)
	})

	c.attachMethod(decl, method)

	if decl.Owner == "" {
		sym := c.module.lookup(decl.Name)
		if sym != nil && sym.decl == decl {
			sym.fn = method
			if len(method.TypeParams) == 0 {
				sym.typ = method.Sig
			}
		}
	}
}

func recvKind(decl *ast.FuncDecl) types.RecvKind {
	if decl.Recv == nil {
		return types.NoRecv
	}
	if decl.Recv.Pointer {
		return types.PointerRecv
	}
	return types.ValueRecv
}

func (c *checker) buildFuncSig(decl *ast.FuncDecl) *types.Func {
	sig := &types.Func{Async: decl.Async, Required: len(decl.Params)}
	for i, param := range decl.Params {
		typ := c.resolveType(param.Type)
		if param.Variadic {
			// The body sees the pack as the slice it is, so the signature
			// carries T[] and only the Variadic flag records the form.
			typ = &types.Slice{Elem: typ}
			c.checkVariadicParam(decl, i, param)
			sig.Variadic = true
			sig.Required = min(sig.Required, i)
		}
		sig.Params = append(sig.Params, typ)
		if param.Default != nil && sig.Required == len(decl.Params) {
			sig.Required = i
		}
	}
	sig.Result = c.resultType(decl.Results)
	return sig
}

// checkVariadicParam enforces the two structural rules a pack has: it is
// the last parameter, and it carries no default — a call that supplies
// nothing already gives it an empty slice.
func (c *checker) checkVariadicParam(decl *ast.FuncDecl, i int, param ast.Param) {
	if i != len(decl.Params)-1 {
		c.errorf(param.Offset, "only the last parameter may be variadic")
	}
	if param.Default != nil {
		c.errorf(param.Offset, "a variadic parameter takes no default; it is empty when nothing is passed")
	}
}

func (c *checker) resultType(results []ast.Type) types.Type {
	switch len(results) {
	case 0:
		return types.Void
	case 1:
		return c.noFormat(results[0].Pos(), c.resolveType(results[0]))
	}
	tuple := &types.Tuple{Elems: make([]types.Type, len(results))}
	for i, result := range results {
		tuple.Elems[i] = c.noFormat(result.Pos(), c.resolveType(result))
	}
	return tuple
}

// ownerTypeParams validates a method's owner and returns the owner's
// canonical type parameters, which the method's OwnerParams alias
// positionally.
func (c *checker) ownerTypeParams(decl *ast.FuncDecl) []*types.TypeParam {
	if decl.Owner == "" {
		if decl.Recv != nil {
			c.errorf(decl.Recv.Offset, "a receiver needs an owning type: write fn Type::%s(self, ...)", decl.Name)
		}
		return nil
	}

	// A built-in type has no declaration to look up. It takes no owner
	// parameters either: an extension applies to one concrete type.
	if builtinOwner(decl.Owner) != nil {
		if decl.Recv == nil {
			c.errorf(decl.Offset, "an extension of %s needs a receiver: write fn %s::%s(self, ...)",
				decl.Owner, decl.Owner, decl.Name)
		}
		if len(decl.OwnerParams) > 0 {
			c.errorf(decl.Offset, "%s takes no type parameters", decl.Owner)
			// The names still have to bind, or the body checks against a
			// parameter list the declaration does not have.
			return makeTypeParams(decl.OwnerParams)
		}
		return nil
	}

	owner := c.module.lookup(decl.Owner)
	if owner == nil {
		c.errorf(decl.Offset, "cannot attach a method to %s: no such type in this module", decl.Owner)
		return makeTypeParams(decl.OwnerParams)
	}
	if owner.kind != symType {
		c.errorf(decl.Offset, "cannot attach a method to %s: it is not a type", decl.Owner)
		return makeTypeParams(decl.OwnerParams)
	}

	var params []*types.TypeParam
	switch owner := owner.typ.(type) {
	case *types.Struct:
		params = owner.TypeParams
	case *types.Enum:
		params = owner.TypeParams
	}

	if len(decl.OwnerParams) != len(params) {
		c.errorf(decl.Offset, "%s has %d type parameters, the method names %d",
			decl.Owner, len(params), len(decl.OwnerParams))
		return makeTypeParams(decl.OwnerParams)
	}
	return params
}

func (c *checker) attachMethod(decl *ast.FuncDecl, method *types.Method) {
	if decl.Owner == "" {
		return
	}
	owner := c.module.lookup(decl.Owner)
	if owner == nil || owner.kind != symType {
		return
	}

	var methods map[string]*types.Method
	switch owner := owner.typ.(type) {
	case *types.Struct:
		methods = owner.Methods
	case *types.Enum:
		methods = owner.Methods
	case *types.Union:
		methods = owner.Methods
	default:
		return
	}

	if _, exists := methods[decl.Name]; exists {
		c.errorf(decl.Offset, "%s already has a method %s", decl.Owner, decl.Name)
		return
	}
	methods[decl.Name] = method
}

// moduleVar types a module-level var or const: the annotation when there is
// one, the initializer's type otherwise. The full value check happens here
// too, so checkDecls does not revisit it.
func (c *checker) moduleVar(decl *ast.VarDecl) {
	c.varTypes(decl, func(binding ast.Binding, typ types.Type) {
		if sym := c.module.lookup(binding.Name); sym != nil && sym.decl == decl {
			sym.typ = typ
			c.recordLocal(decl, binding.Name, typ)
		}
	})
}

// ---------------------------------------------------------------------------
// AST type expressions to types
// ---------------------------------------------------------------------------

func (c *checker) resolveType(typ ast.Type) types.Type {
	switch t := typ.(type) {
	case nil:
		return types.Unknown
	case *ast.NamedType:
		return c.resolveNamedType(t)
	case *ast.PointerType:
		return &types.Pointer{Elem: c.noFormat(t.Elem.Pos(), c.resolveType(t.Elem))}
	case *ast.OptionalType:
		return types.NewOptional(c.noFormat(t.Elem.Pos(), c.resolveType(t.Elem)))
	case *ast.ArrayType:
		return c.resolveArrayType(t)
	case *ast.TupleType:
		tuple := &types.Tuple{Elems: make([]types.Type, len(t.Elements))}
		for i, element := range t.Elements {
			tuple.Elems[i] = c.noFormat(element.Pos(), c.resolveType(element))
		}
		return tuple
	case *ast.FuncType:
		sig := &types.Func{Async: t.Async, Required: len(t.Params)}
		for _, param := range t.Params {
			sig.Params = append(sig.Params, c.resolveType(param))
		}
		sig.Result = c.resultType(t.Results)
		return sig
	case *ast.CodeType:
		code := &types.Code{}
		for _, param := range t.Params {
			code.Params = append(code.Params, c.resolveType(param))
		}
		code.Result = c.resultType(t.Results)
		return code
	case *ast.ChanType:
		return &types.Chan{Elem: c.noFormat(t.Elem.Pos(), c.resolveType(t.Elem))}
	case *ast.DynType:
		return c.resolveDyn(t)
	}
	return types.Unknown
}

func (c *checker) resolveArrayType(t *ast.ArrayType) types.Type {
	elem := c.noFormat(t.Elem.Pos(), c.resolveType(t.Elem))
	if t.Length == nil {
		return &types.Slice{Elem: elem}
	}

	length := int64(-1)
	if lit, ok := t.Length.(*ast.BasicLit); ok && lit.Kind == token.Int {
		if value, err := strconv.ParseInt(lit.Value, 0, 64); err == nil {
			length = value
		}
	} else {
		// Non-literal lengths need constant evaluation, which does not
		// exist yet; resolve names so they are at least checked.
		c.expr(t.Length)
	}
	return &types.Array{Elem: elem, Length: length}
}

func (c *checker) resolveNamedType(t *ast.NamedType) types.Type {
	head := t.Path.Segments[0]
	if head.Name == "<error>" {
		return types.Unknown
	}

	if len(t.Path.Segments) == 1 && types.Builtin[head.Name] != nil {
		if sym := c.scope.lookup(head.Name); sym == nil || sym.kind != symType {
			if len(head.Args) > 0 {
				c.errorf(head.Offset, "%s takes no type arguments", head.Name)
				return types.Unknown
			}
			return types.Builtin[head.Name]
		}
	}

	sym := c.scope.lookup(head.Name)
	if sym == nil {
		c.reportUndeclared(head.Offset, "type", head.Name)
		return types.Unknown
	}

	switch sym.kind {
	case symValue:
		c.errorf(head.Offset, "%s is a value, not a type", head.Name)
		return types.Unknown
	case symImport:
		return types.Unknown
	case symTypeParam:
		if len(head.Args) > 0 {
			c.errorf(head.Offset, "%s is a type parameter and takes no type arguments", head.Name)
		}
		if sym.typ == nil {
			return types.Unknown
		}
		if len(t.Path.Segments) > 1 {
			return c.resolveAssocPath(t.Path, sym.typ)
		}
		return sym.typ
	case symNamespace:
		return c.resolveNamespaceType(t.Path, sym)
	case symTrait:
		c.errorHintf(head.Offset, []string{
			"a trait is not a type; a value of some type implementing it is written dyn " + head.Name,
		}, "%s is a trait, not a type", head.Name)
		return types.Unknown
	}

	c.recordRef(t.Path, sym.decl)
	if len(t.Path.Segments) > 1 {
		c.errorHintf(t.Path.Segments[1].Offset, []string{
			"an associated type is reached through Self in an impl, or through a type parameter bounded by its trait",
		}, "%s::%s cannot be reached into further", head.Name, t.Path.Segments[1].Name)
		return types.Unknown
	}
	return c.applyTypeArgs(sym.typ, head.Offset, c.resolveTypeArgs(head.Args))
}

func (c *checker) resolveTypeArgs(args []ast.Type) []types.Type {
	out := make([]types.Type, len(args))
	for i, arg := range args {
		out[i] = c.noFormat(arg.Pos(), c.resolveType(arg))
	}
	return out
}

// applyTypeArgs instantiates a named type with the given arguments,
// reporting arity mismatches.
func (c *checker) applyTypeArgs(named types.Type, offset int, args []types.Type) types.Type {
	switch t := named.(type) {
	case *types.Struct:
		if len(args) != len(t.TypeParams) {
			c.typeArgArityError(offset, t.Name, len(t.TypeParams), len(args))
			return types.Unknown
		}
		if len(args) == 0 {
			return t
		}
		if !c.boundsOnApply(offset, t.Name, t.TypeParams, args) {
			return types.Unknown
		}
		return c.instantiateStruct(t, args)
	case *types.Enum:
		if len(args) != len(t.TypeParams) {
			c.typeArgArityError(offset, t.Name, len(t.TypeParams), len(args))
			return types.Unknown
		}
		if len(args) == 0 {
			return t
		}
		if !c.boundsOnApply(offset, t.Name, t.TypeParams, args) {
			return types.Unknown
		}
		return c.instantiateEnum(t, args)
	case *types.Union:
		// A union is one storage the widest member fits in, so it takes
		// no parameters: a size that depends on an argument is not what
		// a union is for.
		if len(args) > 0 {
			c.typeArgArityError(offset, t.Name, 0, len(args))
			return types.Unknown
		}
		return t
	case nil:
		return types.Unknown
	}
	return types.Unknown
}

func (c *checker) typeArgArityError(offset int, name string, want, got int) {
	if want == 0 {
		c.errorf(offset, "%s takes no type arguments", name)
		return
	}
	c.errorf(offset, "%s takes %d type argument(s), got %d", name, want, got)
}

// resolveNamespaceType handles `ns::Name` in type position when ns is a
// wildcard import whose module the loader resolved.
func (c *checker) resolveNamespaceType(path *ast.Path, head *symbol) types.Type {
	if len(path.Segments) < 2 {
		c.errorf(path.Offset, "%s is a module, not a type", path.Segments[0].Name)
		return types.Unknown
	}
	if head.exports == nil {
		return types.Unknown
	}

	segment := path.Segments[1]
	entry, ok := head.exports.names[segment.Name]
	if !ok {
		c.errorf(segment.Offset, "%s does not export %s", path.Segments[0].Name, segment.Name)
		return types.Unknown
	}
	if entry.kind == symValue {
		c.errorf(segment.Offset, "%s::%s is a value, not a type", path.Segments[0].Name, segment.Name)
		return types.Unknown
	}
	if entry.kind == symTrait {
		c.errorHintf(segment.Offset, []string{
			"a trait is not a type; a value of some type implementing it is written dyn " + path.Segments[0].Name + "::" + segment.Name,
		}, "%s is a trait, not a type", segment.Name)
		return types.Unknown
	}
	return c.applyTypeArgs(entry.typ, segment.Offset, c.resolveTypeArgs(segment.Args))
}
