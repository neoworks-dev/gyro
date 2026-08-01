package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

func (c *checker) checkFuncDecl(decl *ast.FuncDecl) {
	c.checkDecorators(decl.Decorators)
	c.checkFuncBody(decl, nil)
}

// checkFuncBody checks a function's parameters, defaults and body.
//
// bindings is nil for the ordinary pass. For a generic function that means
// lenient mode: type parameters stay opaque and operations involving them
// poison to Unknown, because the real requirements are duck-typed and
// checked per instantiation — which is the other way here, with bindings
// mapping each type parameter name to a concrete type.
//
// The checker's traversal state is saved and restored, so an instantiation
// can re-enter while another body is mid-check.
func (c *checker) checkFuncBody(decl *ast.FuncDecl, bindings map[string]types.Type) {
	savedScope, savedFn, savedFacts, savedLenient := c.scope, c.fn, c.facts, c.lenient
	defer func() {
		c.scope, c.fn, c.facts, c.lenient = savedScope, savedFn, savedFacts, savedLenient
	}()

	c.scope = newScope(c.module)
	c.facts = map[*symbol]bool{}

	generic := len(decl.TypeParams) > 0 || len(decl.OwnerParams) > 0
	c.lenient = bindings == nil && generic

	c.bindFuncTypeParams(decl, bindings)

	selfType := c.selfTypeFor(decl)
	c.fn = &funcContext{
		result:   c.resultType(decl.Results),
		async:    decl.Async,
		inMethod: decl.Recv != nil,
		selfType: selfType,
	}

	for _, param := range decl.Params {
		paramType := c.resolveType(param.Type)
		if param.Default != nil {
			c.assignExpr(param.Default, paramType, "the default value")
		}
		c.declareValue(param.Name, param.Offset, decl, paramType)
	}

	if decl.Body != nil {
		c.blockInPlace(decl.Body)
	}
}

// bindFuncTypeParams declares the function's type parameter names: bound to
// concrete types during instantiation, to their canonical objects otherwise.
func (c *checker) bindFuncTypeParams(decl *ast.FuncDecl, bindings map[string]types.Type) {
	ownerObjects := c.ownerParamObjects(decl)
	for i, param := range decl.OwnerParams {
		sym := c.declare(param.Name, param.Offset, symTypeParam, nil)
		sym.typ = c.typeParamBinding(param.Name, bindings, ownerObjects, i)
	}

	var ownObjects []*types.TypeParam
	if method := c.funcSigs[decl]; method != nil {
		ownObjects = method.TypeParams
	}
	for i, param := range decl.TypeParams {
		sym := c.declare(param.Name, param.Offset, symTypeParam, nil)
		sym.typ = c.typeParamBinding(param.Name, bindings, ownObjects, i)
	}
}

func (c *checker) typeParamBinding(name string, bindings map[string]types.Type, objects []*types.TypeParam, index int) types.Type {
	if bindings != nil {
		if bound, ok := bindings[name]; ok {
			return bound
		}
		return types.Unknown
	}
	if index < len(objects) {
		return objects[index]
	}
	return types.Unknown
}

// ownerParamObjects returns the canonical type parameters of the method's
// owner, which the OwnerParams alias positionally.
func (c *checker) ownerParamObjects(decl *ast.FuncDecl) []*types.TypeParam {
	if decl.Owner == "" {
		return nil
	}
	owner := c.module.lookup(decl.Owner)
	if owner == nil {
		return nil
	}
	switch t := owner.typ.(type) {
	case *types.Struct:
		return t.TypeParams
	case *types.Enum:
		return t.TypeParams
	}
	return nil
}

// selfTypeFor computes the type `self` has inside a method: the owner, or
// the owner instantiated with whatever the type parameter names are
// currently bound to.
func (c *checker) selfTypeFor(decl *ast.FuncDecl) types.Type {
	if decl.Owner == "" || decl.Recv == nil {
		return nil
	}
	owner := c.module.lookup(decl.Owner)
	if owner == nil || owner.kind != symType {
		return types.Unknown
	}

	args := make([]types.Type, len(decl.OwnerParams))
	for i, param := range decl.OwnerParams {
		if sym := c.scope.lookup(param.Name); sym != nil && sym.typ != nil {
			args[i] = sym.typ
		} else {
			args[i] = types.Unknown
		}
	}

	switch t := owner.typ.(type) {
	case *types.Struct:
		if len(args) == 0 {
			return t
		}
		return c.instantiateStruct(t, args)
	case *types.Enum:
		if len(args) == 0 {
			return t
		}
		return c.instantiateEnum(t, args)
	}
	return types.Unknown
}
