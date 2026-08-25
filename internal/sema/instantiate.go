package sema

import (
	"fmt"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// subst maps type parameters to the types standing in for them.
type subst map[*types.TypeParam]types.Type

// substType rewrites a type with the substitution applied. Generic
// instances are re-canonicalized so identity stays pointer identity.
func (c *checker) substType(t types.Type, s subst) types.Type {
	if len(s) == 0 {
		return t
	}
	switch t := t.(type) {
	case *types.TypeParam:
		if bound, ok := s[t]; ok {
			return bound
		}
		return t
	case *types.Pointer:
		return &types.Pointer{Elem: c.substType(t.Elem, s)}
	case *types.Optional:
		return types.NewOptional(c.substType(t.Elem, s))
	case *types.Slice:
		return &types.Slice{Elem: c.substType(t.Elem, s)}
	case *types.Array:
		return &types.Array{Elem: c.substType(t.Elem, s), Length: t.Length}
	case *types.Chan:
		return &types.Chan{Elem: c.substType(t.Elem, s)}
	case *types.Future:
		return &types.Future{Result: c.substType(t.Result, s)}
	case *types.Tuple:
		elems := make([]types.Type, len(t.Elems))
		for i, elem := range t.Elems {
			elems[i] = c.substType(elem, s)
		}
		return &types.Tuple{Elems: elems}
	case *types.Func:
		params := make([]types.Type, len(t.Params))
		for i, param := range t.Params {
			params[i] = c.substType(param, s)
		}
		return &types.Func{Params: params, Required: t.Required, Result: c.substType(t.Result, s), Async: t.Async, Variadic: t.Variadic}
	case *types.Struct:
		return c.substStruct(t, s)
	case *types.Enum:
		return c.substEnum(t, s)
	}
	return t
}

func (c *checker) substStruct(t *types.Struct, s subst) types.Type {
	origin, args := t, t.Args
	if t.Origin != nil {
		origin = t.Origin
	} else if len(t.TypeParams) > 0 {
		// A bare generic struct inside its own methods stands for itself
		// parameterized by its own type parameters.
		args = typeParamsAsArgs(t.TypeParams)
	} else {
		return t
	}

	newArgs := make([]types.Type, len(args))
	changed := false
	for i, arg := range args {
		newArgs[i] = c.substType(arg, s)
		if newArgs[i] != arg {
			changed = true
		}
	}
	if !changed && t.Origin != nil {
		return t
	}
	return c.instantiateStruct(origin, newArgs)
}

func (c *checker) substEnum(t *types.Enum, s subst) types.Type {
	origin, args := t, t.Args
	if t.Origin != nil {
		origin = t.Origin
	} else if len(t.TypeParams) > 0 {
		args = typeParamsAsArgs(t.TypeParams)
	} else {
		return t
	}

	newArgs := make([]types.Type, len(args))
	changed := false
	for i, arg := range args {
		newArgs[i] = c.substType(arg, s)
		if newArgs[i] != arg {
			changed = true
		}
	}
	if !changed && t.Origin != nil {
		return t
	}
	return c.instantiateEnum(origin, newArgs)
}

func typeParamsAsArgs(params []*types.TypeParam) []types.Type {
	args := make([]types.Type, len(params))
	for i, param := range params {
		args[i] = param
	}
	return args
}

// instantiateStruct returns the canonical instance of a generic struct.
// The shell is cached before the fields are substituted, so self-referential
// types like Node<T> terminate.
func (c *checker) instantiateStruct(origin *types.Struct, args []types.Type) *types.Struct {
	if identityArgs(origin.TypeParams, args) {
		return origin
	}

	key := instanceCacheKey(args)
	cache := c.program.structInstances[origin]
	if cache == nil {
		cache = map[string]*types.Struct{}
		c.program.structInstances[origin] = cache
	}
	if instance, ok := cache[key]; ok {
		return instance
	}

	instance := &types.Struct{
		Name:   origin.Name,
		Module: origin.Module,
		Origin: origin,
		Args:   args,
	}
	cache[key] = instance

	s := bindParams(origin.TypeParams, args)
	for _, field := range origin.Fields {
		instance.Fields = append(instance.Fields, types.Field{
			Name:     field.Name,
			Type:     c.substType(field.Type, s),
			Exported: field.Exported,
		})
	}
	return instance
}

func (c *checker) instantiateEnum(origin *types.Enum, args []types.Type) *types.Enum {
	if identityArgs(origin.TypeParams, args) {
		return origin
	}

	key := instanceCacheKey(args)
	cache := c.program.enumInstances[origin]
	if cache == nil {
		cache = map[string]*types.Enum{}
		c.program.enumInstances[origin] = cache
	}
	if instance, ok := cache[key]; ok {
		return instance
	}

	instance := &types.Enum{
		Name:   origin.Name,
		Module: origin.Module,
		Origin: origin,
		Args:   args,
	}
	cache[key] = instance

	s := bindParams(origin.TypeParams, args)
	for _, variant := range origin.Variants {
		payload := make([]types.Type, len(variant.Payload))
		for i, typ := range variant.Payload {
			payload[i] = c.substType(typ, s)
		}
		instance.Variants = append(instance.Variants, types.Variant{Name: variant.Name, Payload: payload})
	}
	return instance
}

// instanceCacheKey renders the cache key of one argument list. It is
// types.Key with one refinement: a type parameter is keyed by identity,
// not by name. Two declarations may both name a parameter K, and keying
// by name would hand the second declaration the first one's Array<K> —
// an instance whose Args mention a foreign parameter that pointer-keyed
// substitution can never rewrite.
func instanceCacheKey(args []types.Type) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = cacheString(arg)
	}
	return strings.Join(parts, ",")
}

func cacheString(t types.Type) string {
	switch t := t.(type) {
	case *types.TypeParam:
		return fmt.Sprintf("%s@%p", t.Name, t)
	case *types.Pointer:
		return "*" + cacheString(t.Elem)
	case *types.Optional:
		return cacheString(t.Elem) + "?"
	case *types.Slice:
		return cacheString(t.Elem) + "[]"
	case *types.Array:
		return fmt.Sprintf("%s[%d]", cacheString(t.Elem), t.Length)
	case *types.Chan:
		return "chan<" + cacheString(t.Elem) + ">"
	case *types.Future:
		return "future<" + cacheString(t.Result) + ">"
	case *types.Tuple:
		parts := make([]string, len(t.Elems))
		for i, elem := range t.Elems {
			parts[i] = cacheString(elem)
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case *types.Func:
		parts := make([]string, len(t.Params))
		for i, param := range t.Params {
			parts[i] = cacheString(param)
		}
		return "fn(" + strings.Join(parts, ", ") + "): " + cacheString(t.Result)
	case *types.Struct:
		if len(t.Args) > 0 {
			return t.Name + "<" + instanceCacheKey(t.Args) + ">"
		}
		return t.Name
	case *types.Enum:
		if len(t.Args) > 0 {
			return t.Name + "<" + instanceCacheKey(t.Args) + ">"
		}
		return t.Name
	}
	return t.String()
}

// identityArgs reports whether the arguments are the origin's own type
// parameters, in which case the origin already is the instance.
func identityArgs(params []*types.TypeParam, args []types.Type) bool {
	if len(params) != len(args) {
		return false
	}
	for i, param := range params {
		if args[i] != types.Type(param) {
			return false
		}
	}
	return true
}

func bindParams(params []*types.TypeParam, args []types.Type) subst {
	s := subst{}
	for i, param := range params {
		if i < len(args) {
			s[param] = args[i]
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// Inference
// ---------------------------------------------------------------------------

// unify binds the type parameters occurring in pattern so that pattern
// matches actual, reporting a conflict through the returned flag.
func unify(pattern, actual types.Type, s subst) bool {
	if types.IsUnknown(actual) {
		return true
	}
	switch p := pattern.(type) {
	case *types.TypeParam:
		if bound, ok := s[p]; ok {
			return types.Identical(bound, actual)
		}
		s[p] = actual
		return true
	case *types.Pointer:
		a, ok := actual.(*types.Pointer)
		return ok && unify(p.Elem, a.Elem, s)
	case *types.Optional:
		a, ok := actual.(*types.Optional)
		return ok && unify(p.Elem, a.Elem, s)
	case *types.Slice:
		// A fixed-size array lends a view of itself wherever a slice is
		// wanted, so unification has to see through that too — otherwise
		// sort(xs) takes an Array<T> and refuses [1, 2, 3].
		if a, ok := actual.(*types.Array); ok {
			return unify(p.Elem, a.Elem, s)
		}
		a, ok := actual.(*types.Slice)
		return ok && unify(p.Elem, a.Elem, s)
	case *types.Array:
		a, ok := actual.(*types.Array)
		return ok && p.Length == a.Length && unify(p.Elem, a.Elem, s)
	case *types.Chan:
		a, ok := actual.(*types.Chan)
		return ok && unify(p.Elem, a.Elem, s)
	case *types.Tuple:
		a, ok := actual.(*types.Tuple)
		if !ok || len(p.Elems) != len(a.Elems) {
			return false
		}
		for i := range p.Elems {
			if !unify(p.Elems[i], a.Elems[i], s) {
				return false
			}
		}
		return true
	case *types.Func:
		a, ok := actual.(*types.Func)
		if !ok || len(p.Params) != len(a.Params) || p.Async != a.Async {
			return false
		}
		for i := range p.Params {
			if !unify(p.Params[i], a.Params[i], s) {
				return false
			}
		}
		return unify(p.Result, a.Result, s)
	case *types.Struct:
		a, ok := actual.(*types.Struct)
		if !ok || a.Origin == nil || p.Origin == nil || a.Origin != p.Origin {
			return types.Identical(pattern, actual)
		}
		for i := range p.Args {
			if !unify(p.Args[i], a.Args[i], s) {
				return false
			}
		}
		return true
	case *types.Enum:
		a, ok := actual.(*types.Enum)
		if !ok || a.Origin == nil || p.Origin == nil || a.Origin != p.Origin {
			return types.Identical(pattern, actual)
		}
		for i := range p.Args {
			if !unify(p.Args[i], a.Args[i], s) {
				return false
			}
		}
		return true
	}
	return types.Identical(pattern, actual)
}

// containsTypeParam reports whether a type still mentions a generic
// parameter, meaning it is not concrete yet.
func containsTypeParam(t types.Type) bool {
	switch t := t.(type) {
	case *types.TypeParam:
		return true
	case *types.Pointer:
		return containsTypeParam(t.Elem)
	case *types.Optional:
		return containsTypeParam(t.Elem)
	case *types.Slice:
		return containsTypeParam(t.Elem)
	case *types.Array:
		return containsTypeParam(t.Elem)
	case *types.Chan:
		return containsTypeParam(t.Elem)
	case *types.Future:
		return containsTypeParam(t.Result)
	case *types.Tuple:
		for _, elem := range t.Elems {
			if containsTypeParam(elem) {
				return true
			}
		}
	case *types.Func:
		for _, param := range t.Params {
			if containsTypeParam(param) {
				return true
			}
		}
		return containsTypeParam(t.Result)
	case *types.Struct:
		for _, arg := range t.Args {
			if containsTypeParam(arg) {
				return true
			}
		}
		if t.Origin == nil {
			return len(t.TypeParams) > 0
		}
	case *types.Enum:
		for _, arg := range t.Args {
			if containsTypeParam(arg) {
				return true
			}
		}
		if t.Origin == nil {
			return len(t.TypeParams) > 0
		}
	}
	return false
}

// flexible reports whether a type should poison instead of erroring: it is
// Unknown from an earlier error, or it mentions a type parameter whose
// requirements are checked at instantiation instead.
func flexible(t types.Type) bool {
	return types.IsUnknown(t) || containsTypeParam(t)
}

// ---------------------------------------------------------------------------
// Duck-typed instantiation checking
// ---------------------------------------------------------------------------

const maxInstantiationDepth = 32

// instantiate re-checks a generic function's body with its type parameters
// bound to concrete types. This is where duck typing bites: whatever the
// body calls on a parameter, the concrete type must have. Results are
// cached per (declaration, arguments), and recursive instantiations of the
// same shape converge through the cache.
func (c *checker) instantiate(decl *ast.FuncDecl, bindings map[string]types.Type, label string, callOffset int) {
	if decl.Body == nil {
		return
	}
	for _, bound := range bindings {
		if flexible(bound) {
			return
		}
	}

	key := fmt.Sprintf("%p", decl)
	for _, param := range append(append([]ast.TypeParam{}, decl.OwnerParams...), decl.TypeParams...) {
		key += "|" + bindings[param.Name].String()
	}
	if c.program.instantiated[key] {
		return
	}
	c.program.instantiated[key] = true

	if c.program.depth >= maxInstantiationDepth {
		c.errorf(callOffset, "generic instantiation of %s is too deep; the type keeps growing", label)
		return
	}
	c.program.depth++
	defer func() { c.program.depth-- }()

	env := c.program.envs[decl]
	if env == nil {
		return
	}

	before := len(c.bag.Items())
	env.checkFuncBody(decl, bindings)
	if len(c.bag.Items()) > before {
		c.errorf(callOffset, "in the instantiation %s required here", label)
	}
}
