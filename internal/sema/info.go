package sema

import (
	"math/big"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// Info is what the checker records for the stages after it: the type of
// every expression, what names and calls resolved to, the folded
// constants, and every concrete instantiation of each generic function.
// The lowering pass consumes it instead of re-deriving types.
type Info struct {
	// Types holds the checked type of every expression outside generic
	// bodies. Untyped literal types may remain when no context typed
	// them; the reader applies types.Default. Expressions inside a
	// generic body are recorded per instantiation in Instances.
	Types map[ast.Expr]types.Type
	// Refs maps an identifier or path in value position to the
	// declaration it resolved to.
	Refs map[ast.Expr]ast.Node
	// Calls maps each call expression to what it invokes.
	Calls map[*ast.CallExpr]*CallInfo
	// Consts holds the folded values of untyped integer expressions.
	Consts map[ast.Expr]*big.Int
	// Locals holds the declared type of every value binding, keyed by
	// the declaring node and the bound name: parameters under their
	// FuncDecl or ClosureExpr, locals under their VarDecl, loop
	// variables under their ForInStmt, pattern binds under their
	// BindingPattern. Narrowing never changes these. Two reserved keys
	// piggyback on the mechanism, spelled as the keywords they are so
	// no parameter can collide: "self" is a method's receiver type and
	// "return" its result type.
	Locals map[ast.Node]map[string]types.Type
	// Structs and Enums map declarations to their built types.
	Structs map[*ast.StructDecl]*types.Struct
	Unions  map[*ast.UnionDecl]*types.Union
	Enums   map[*ast.EnumDecl]*types.Enum
	Traits  map[*ast.TraitDecl]*types.Trait
	// Instances lists every concrete instantiation of each generic
	// function, keyed by InstanceKey.
	Instances map[*ast.FuncDecl]map[string]*FuncInstance
	// Externs holds the library and symbol of every @extern function, so
	// lowering can emit the import without re-reading the decorator.
	Externs map[*ast.FuncDecl]types.Extern
}

// CallInfo is the resolution of one call expression.
type CallInfo struct {
	// Method is the named function or method invoked. Its Decl is nil
	// for the built-in method surface (len, close, runes, compare,
	// message). Method is nil when the call goes through a function
	// value instead.
	Method *types.Method
	// Sig is the concrete signature at this call site, type arguments
	// substituted.
	Sig *types.Func
	// Bindings maps the declaration's type parameter names — the
	// owner's and the function's own — to this call's concrete types.
	// nil when nothing is generic.
	Bindings map[string]types.Type
	// Enum and Variant are set for enum variant construction instead
	// of the fields above.
	Enum    *types.Enum
	Variant int
	// Dyn is set for a call on a trait value, which goes through the
	// value's method table: it names the trait value's type. Method is
	// the trait's declaration of the method, and has no body to call.
	Dyn *types.Dyn
}

// FuncInstance is one concrete instantiation of a generic function: the
// per-instantiation expression types and call resolutions of its body,
// recorded while the duck-typed instantiation check ran.
type FuncInstance struct {
	Decl     *ast.FuncDecl
	Bindings map[string]types.Type
	Types    map[ast.Expr]types.Type
	Calls    map[*ast.CallExpr]*CallInfo
	Locals   map[ast.Node]map[string]types.Type
}

// InstanceKey renders the canonical key of one instantiation: the bound
// types of the declaration's owner and own type parameters, in
// declaration order.
func InstanceKey(decl *ast.FuncDecl, bindings map[string]types.Type) string {
	var parts []string
	appendBinding := func(name string) {
		bound := bindings[name]
		if bound == nil {
			bound = types.Unknown
		}
		parts = append(parts, bound.String())
	}
	for _, param := range decl.OwnerParams {
		appendBinding(param.Name)
	}
	for _, param := range decl.TypeParams {
		appendBinding(param.Name)
	}
	return strings.Join(parts, ",")
}

func newInfo() *Info {
	return &Info{
		Types:     map[ast.Expr]types.Type{},
		Refs:      map[ast.Expr]ast.Node{},
		Calls:     map[*ast.CallExpr]*CallInfo{},
		Consts:    map[ast.Expr]*big.Int{},
		Locals:    map[ast.Node]map[string]types.Type{},
		Structs:   map[*ast.StructDecl]*types.Struct{},
		Unions:    map[*ast.UnionDecl]*types.Union{},
		Enums:     map[*ast.EnumDecl]*types.Enum{},
		Traits:    map[*ast.TraitDecl]*types.Trait{},
		Instances: map[*ast.FuncDecl]map[string]*FuncInstance{},
		Externs:   map[*ast.FuncDecl]types.Extern{},
	}
}

// instance returns the recording target for one instantiation, creating
// it on first use.
func (info *Info) instance(decl *ast.FuncDecl, bindings map[string]types.Type) *FuncInstance {
	key := InstanceKey(decl, bindings)
	byKey := info.Instances[decl]
	if byKey == nil {
		byKey = map[string]*FuncInstance{}
		info.Instances[decl] = byKey
	}
	if existing, ok := byKey[key]; ok {
		return existing
	}
	created := &FuncInstance{
		Decl:     decl,
		Bindings: bindings,
		Types:    map[ast.Expr]types.Type{},
		Calls:    map[*ast.CallExpr]*CallInfo{},
		Locals:   map[ast.Node]map[string]types.Type{},
	}
	byKey[key] = created
	return created
}

// recorder is where expression types and call resolutions currently go:
// the shared Info maps, or a FuncInstance's own maps during an
// instantiation check. nil while a generic body is checked leniently,
// whose poisoned types are worthless to later stages.
type recorder struct {
	types  map[ast.Expr]types.Type
	calls  map[*ast.CallExpr]*CallInfo
	locals map[ast.Node]map[string]types.Type
}

func (c *checker) record(e ast.Expr, t types.Type) {
	if c.rec == nil || e == nil || t == nil {
		return
	}
	c.rec.types[e] = t
}

// recordedType answers what the checker last recorded for e, or nil when
// nothing was.
func (c *checker) recordedType(e ast.Expr) types.Type {
	if c.rec == nil || e == nil {
		return nil
	}
	return c.rec.types[e]
}

func (c *checker) recordCall(call *ast.CallExpr, resolved *CallInfo) {
	if c.rec == nil {
		return
	}
	c.rec.calls[call] = resolved
}

func (c *checker) recordLocal(decl ast.Node, name string, typ types.Type) {
	if c.rec == nil || decl == nil || name == "" || name == "_" || typ == nil {
		return
	}
	byName := c.rec.locals[decl]
	if byName == nil {
		byName = map[string]types.Type{}
		c.rec.locals[decl] = byName
	}
	byName[name] = typ
}

// recordRef notes what a name resolved to. Resolution does not depend on
// type bindings, so refs are shared across instantiations.
func (c *checker) recordRef(e ast.Expr, decl ast.Node) {
	if e == nil || decl == nil {
		return
	}
	c.program.info.Refs[e] = decl
}
