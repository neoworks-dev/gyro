package sema

import (
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// Traits. A trait is a list of method signatures, some with a default
// body; a type implements it in an `impl Trait for Type` block, and a
// generic states which traits its type parameters have to implement. The
// body of a generic may use only what those bounds provide, so a call that
// cannot work is reported at the call.
//
// The capabilities the language itself leans on — equality, hashing,
// printing, ordering, the arithmetic operators and iteration — are traits
// like any other, declared in the bundled `stdlib` module. The compiler
// knows them by where they are declared, which is what Kind records, and
// answers for the built-in types itself: a number is Eq and Ord without any
// impl being written for it.

// capabilityNames are the traits of the bundled `stdlib` the compiler
// gives a meaning.
var capabilityNames = map[string]bool{
	"Eq": true, "Hash": true, "Print": true, "Ord": true,
	"Add": true, "Sub": true, "Mul": true, "Div": true, "Rem": true, "Neg": true,
	"Iterator": true, "Index": true,
}

// derivable are the capabilities `impl Trait for Type` with no block asks
// the compiler to build from the type's fields.
var derivable = map[string]bool{"Eq": true, "Hash": true, "Print": true}

// operatorTraits maps the operators a user type can overload to the trait
// and the method that carries them.
var operatorTraits = map[token.Kind]struct{ trait, method string }{
	token.Plus:    {"Add", "add"},
	token.Minus:   {"Sub", "sub"},
	token.Star:    {"Mul", "mul"},
	token.Slash:   {"Div", "div"},
	token.Percent: {"Rem", "rem"},
}

// ---------------------------------------------------------------------------
// Declaring traits
// ---------------------------------------------------------------------------

// collectTrait makes the trait's shell so every module-level name can mention
// it before any signature is read.
func (c *checker) collectTrait(d *ast.TraitDecl) {
	shell := &types.Trait{
		Name:   d.Name,
		Module: c.modulePath,
		Params: makeTypeParams(d.TypeParams),
		Self:   &types.TypeParam{Name: "Self"},
		Decl:   d,
	}
	shell.Self.Bounds = []*types.TraitRef{{Trait: shell, Args: typeParamsAsArgs(shell.Params)}}
	if c.modulePath == stdlib.Path("stdlib") && capabilityNames[d.Name] {
		shell.Kind = d.Name
		c.program.caps[d.Name] = shell
	}
	c.traitTypes[d] = shell
	c.program.info.Traits[d] = shell
	c.declare(d.Name, d.Offset, symTrait, d).typ = shell
}

// buildTraitSupers reads which traits a trait requires.
func (c *checker) buildTraitSupers(d *ast.TraitDecl) {
	trait := c.traitTypes[d]
	c.pushScope(func() {
		c.bindTraitScope(trait)
		for _, super := range d.Supers {
			if ref := c.resolveTraitRef(super); ref != nil {
				trait.Supers = append(trait.Supers, ref)
			}
		}
	})
}

// buildTraitMethods reads a trait's method signatures, written over Self
// and the trait's own parameters.
func (c *checker) buildTraitMethods(d *ast.TraitDecl) {
	trait := c.traitTypes[d]
	c.pushScope(func() {
		c.bindTraitScope(trait)

		for _, decl := range d.Methods {
			if existing := trait.MethodNamed(decl.Name); existing != nil {
				c.errorf(decl.Offset, "the trait %s already has a method %s", trait.Name, decl.Name)
				continue
			}
			method := &types.Method{
				Name:  decl.Name,
				Recv:  recvKind(decl),
				Decl:  decl,
				Trait: trait,
			}
			c.program.envs[decl] = c
			c.funcSigs[decl] = method
			method.TypeParams = makeTypeParams(decl.TypeParams)

			c.pushScope(func() {
				c.bindTypeParams(decl.TypeParams, method.TypeParams)
				method.Sig = c.buildFuncSig(decl)
			})
			if decl.Recv == nil {
				c.errorf(decl.Offset, "a trait's method takes a receiver; write fn %s(self, ...)", decl.Name)
			}
			if decl.Body != nil && len(trait.Params) > 0 {
				c.errorf(decl.Offset, "a default body needs a trait without type parameters; %s takes some", trait.Name)
			}
			trait.Methods = append(trait.Methods, method)
		}
	})
}

// bindTraitScope declares Self and the trait's own parameters.
func (c *checker) bindTraitScope(trait *types.Trait) {
	self := c.declare("Self", trait.Decl.Offset, symTypeParam, nil)
	self.typ = trait.Self
	c.bindTypeParams(trait.Decl.TypeParams, trait.Params)
}

// checkTraitDecl checks the default bodies of a trait. A default is checked
// once with Self opaque and bounded by the trait, and again for every type
// that takes it, like any generic.
func (c *checker) checkTraitDecl(d *ast.TraitDecl) {
	c.checkDecorators(d.Decorators)
	for _, method := range d.Methods {
		if c.funcSigs[method] == nil {
			continue
		}
		c.checkFuncBody(method, nil)
	}
}

// ---------------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------------

// resolveBounds reads the bounds written on a declaration's type parameters
// onto their canonical objects. It runs with the parameters in scope, so a
// bound may mention a sibling: `<I: Iterator<T>, T>`.
func (c *checker) resolveBounds(params []ast.TypeParam, objects []*types.TypeParam) {
	for i, param := range params {
		if i >= len(objects) || len(param.Bounds) == 0 {
			continue
		}
		for _, bound := range param.Bounds {
			if ref := c.resolveTraitRef(bound); ref != nil {
				objects[i].Bounds = append(objects[i].Bounds, ref)
			}
		}
	}
}

// resolveTraitRef resolves the trait named in a bound, a supertrait list,
// an impl or a dyn type, with its arguments. It reports and answers nil
// when the name is not a trait.
func (c *checker) resolveTraitRef(t ast.Type) *types.TraitRef {
	named, ok := t.(*ast.NamedType)
	if !ok || len(named.Path.Segments) == 0 {
		return nil
	}
	head := named.Path.Segments[0]
	if head.Name == "<error>" {
		return nil
	}
	sym := c.scope.lookup(head.Name)
	if sym == nil {
		c.reportUndeclared(head.Offset, "type", head.Name)
		return nil
	}

	segment := head
	var entry *symbol
	switch sym.kind {
	case symTrait:
		entry = sym
		if len(named.Path.Segments) > 1 {
			c.errorf(named.Path.Segments[1].Offset, "%s::%s cannot be reached into further", head.Name, named.Path.Segments[1].Name)
			return nil
		}
	case symNamespace:
		if len(named.Path.Segments) < 2 || sym.exports == nil {
			return nil
		}
		segment = named.Path.Segments[1]
		found, ok := sym.exports.names[segment.Name]
		if !ok {
			c.errorf(segment.Offset, "%s does not export %s", head.Name, segment.Name)
			return nil
		}
		if found.kind != symTrait {
			c.errorf(segment.Offset, "%s is not a trait", segment.Name)
			return nil
		}
		entry = found
	case symImport:
		return nil
	default:
		c.errorf(head.Offset, "%s is not a trait", head.Name)
		return nil
	}

	trait, ok := entry.typ.(*types.Trait)
	if !ok {
		return nil
	}
	args := c.resolveTypeArgs(segment.Args)
	if len(args) != len(trait.Params) {
		c.typeArgArityError(segment.Offset, trait.Name, len(trait.Params), len(args))
		return nil
	}
	return &types.TraitRef{Trait: trait, Args: args}
}

// superRefs lists the traits a reference requires, with Self and the
// trait's parameters replaced by what this reference applies them to.
func (c *checker) superRefs(ref *types.TraitRef, self types.Type) []*types.TraitRef {
	s := bindParams(ref.Trait.Params, ref.Args)
	s[ref.Trait.Self] = self
	out := make([]*types.TraitRef, len(ref.Trait.Supers))
	for i, super := range ref.Trait.Supers {
		out[i] = c.substRef(super, s)
	}
	return out
}

// boundClosure lists every trait a type parameter is known to implement:
// what it is bounded by, and what those require, transitively.
func (c *checker) boundClosure(param *types.TypeParam) []*types.TraitRef {
	var out []*types.TraitRef
	seen := map[string]bool{}
	var walk func(ref *types.TraitRef)
	walk = func(ref *types.TraitRef) {
		key := ref.Trait.Name + "<" + instanceCacheKey(ref.Args) + ">@" + traitKey(ref.Trait)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ref)
		for _, super := range c.superRefs(ref, param) {
			walk(super)
		}
	}
	for _, bound := range param.Bounds {
		walk(bound)
	}
	return out
}

func traitKey(t *types.Trait) string { return t.Module + "." + t.Name }

// ---------------------------------------------------------------------------
// Implementing
// ---------------------------------------------------------------------------

// implsOf is the list of impls a named type carries, on its origin when it
// is an instance of a generic one.
func implsOf(t types.Type) []*types.Impl {
	switch t := t.(type) {
	case *types.Struct:
		if t.Origin != nil {
			return t.Origin.Impls
		}
		return t.Impls
	case *types.Enum:
		if t.Origin != nil {
			return t.Origin.Impls
		}
		return t.Impls
	case *types.Union:
		return t.Impls
	}
	return nil
}

// ownerOf returns the declared type an impl attaches to, the type
// parameters it is written over, and a pointer to its impl list.
func (c *checker) ownerOf(name string) (owner types.Type, params []*types.TypeParam, impls *[]*types.Impl) {
	sym := c.module.lookup(name)
	if sym == nil || sym.kind != symType {
		return nil, nil, nil
	}
	switch t := sym.typ.(type) {
	case *types.Struct:
		return t, t.TypeParams, &t.Impls
	case *types.Enum:
		return t, t.TypeParams, &t.Impls
	case *types.Union:
		return t, nil, &t.Impls
	}
	return nil, nil, nil
}

// registerImpl records that a type implements a trait, so that every use
// of the type in this module can find it before the impl's methods have
// been read.
func (c *checker) registerImpl(d *ast.ImplDecl) {
	owner, params, impls := c.implOwnerOf(d)
	if owner == nil {
		return
	}

	// The owner's parameters are named here, so the trait's arguments may
	// mention them: impl Iterator<T> for Walk<T>.
	var ref *types.TraitRef
	c.pushScope(func() {
		c.bindImplParams(d, params)
		ref = c.resolveTraitRef(d.Trait)
	})
	if ref == nil {
		return
	}

	for _, existing := range *impls {
		if existing.Trait == ref.Trait && argsIdentical(existing.Args, ref.Args) {
			c.errorf(d.Offset, "%s already implements %s", owner, ref)
			return
		}
	}

	impl := &types.Impl{Trait: ref.Trait, Args: ref.Args, Derived: !d.Body, Decl: d, Owner: owner}
	*impls = append(*impls, impl)
	c.implOf[d] = impl
	c.implTypes[d] = owner
}

// implOwnerOf finds the type an impl is for, which has to be declared in
// this module. The methods an impl brings are added to the type, and a
// module does not reach into a type that is not its own.
func (c *checker) implOwnerOf(d *ast.ImplDecl) (types.Type, []*types.TypeParam, *[]*types.Impl) {
	named, ok := d.For.(*ast.NamedType)
	if !ok || len(named.Path.Segments) != 1 {
		return nil, nil, nil
	}
	name := named.Path.Segments[0].Name
	if name == "<error>" {
		return nil, nil, nil
	}
	if builtinOwner(name) != nil || types.Builtin[name] != nil {
		c.errorf(named.Offset, "an impl is for a type this module declares; %s is built in", name)
		return nil, nil, nil
	}
	owner, params, impls := c.ownerOf(name)
	if owner == nil {
		c.errorf(named.Offset, "an impl is for a type this module declares; %s is not one", name)
		return nil, nil, nil
	}
	args := named.Path.Segments[0].Args
	if len(args) != len(params) {
		c.errorf(named.Offset, "%s has %d type parameters, the impl names %d", name, len(params), len(args))
		return nil, nil, nil
	}
	for _, arg := range args {
		if !c.isParameterName(arg) {
			c.errorf(arg.Pos(), "an impl names the type's own parameters, as in impl Eq for Pair<T>; %s is a type", typeText(arg))
			return nil, nil, nil
		}
	}
	return owner, params, impls
}

// isParameterName reports whether an impl's type argument is a fresh name,
// as opposed to a type that already exists.
func (c *checker) isParameterName(arg ast.Type) bool {
	named, ok := arg.(*ast.NamedType)
	if !ok || len(named.Path.Segments) != 1 || len(named.Path.Segments[0].Args) > 0 {
		return false
	}
	name := named.Path.Segments[0].Name
	if types.Builtin[name] != nil {
		return false
	}
	if sym := c.module.lookup(name); sym != nil && sym.kind != symTypeParam {
		return false
	}
	return true
}

func typeText(t ast.Type) string {
	if named, ok := t.(*ast.NamedType); ok {
		return named.Path.Segments[0].Name
	}
	return "this"
}

// bindImplParams declares the names an impl gives the owner's parameters.
func (c *checker) bindImplParams(d *ast.ImplDecl, params []*types.TypeParam) {
	named, ok := d.For.(*ast.NamedType)
	if !ok {
		return
	}
	for i, arg := range named.Path.Segments[0].Args {
		if i >= len(params) {
			break
		}
		if argNamed, ok := arg.(*ast.NamedType); ok && len(argNamed.Path.Segments) == 1 {
			sym := c.declare(argNamed.Path.Segments[0].Name, arg.Pos(), symTypeParam, nil)
			sym.typ = params[i]
		}
	}
}

func argsIdentical(a, b []types.Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !types.Identical(a[i], b[i]) {
			return false
		}
	}
	return true
}

// checkImpl holds a registered impl to its trait: a required method is
// there, an extra one is not, every signature is the trait's, and the
// traits it requires are implemented too. Defaults the impl leaves alone
// are installed on the type.
func (c *checker) checkImpl(d *ast.ImplDecl) {
	impl := c.implOf[d]
	owner := c.implTypes[d]
	if impl == nil {
		return
	}
	trait := impl.Trait
	_, params, _ := c.ownerOf(ownerName(owner))
	ownerSelf := c.ownerSelfType(owner)

	// Everything the trait requires has to be implemented as well.
	s := bindParams(trait.Params, impl.Args)
	s[trait.Self] = ownerSelf
	for _, super := range trait.Supers {
		wanted := c.substRef(super, s)
		if !c.implementsRef(ownerSelf, wanted) {
			c.errorHintf(d.Offset, []string{
				"write impl " + wanted.String() + " for " + owner.String() + " as well",
			}, "%s requires %s, which %s does not implement", trait.Name, wanted, owner)
		}
	}

	if !d.Body {
		if !derivable[trait.Kind] {
			c.errorHintf(d.Offset, []string{
				"the compiler derives Eq, Hash and Print; any other trait is implemented by writing its methods",
			}, "%s cannot be derived", trait.Name)
			return
		}
		c.checkDerivable(d, owner, ownerSelf, trait.Kind, params)
		return
	}

	before := len(c.bag.Items())
	provided := map[string]bool{}
	for _, method := range d.Methods {
		provided[method.Name] = true
		declared := trait.MethodNamed(method.Name)
		if declared == nil {
			c.errorf(method.Offset, "%s is not a method of %s", method.Name, trait.Name)
			continue
		}
		have := c.funcSigs[method]
		if have == nil || have.Sig == nil {
			continue
		}
		c.compareImplMethod(method, have, declared, s)
	}
	if len(c.bag.Items()) > before {
		impl.Broken = true
	}

	var missing []string
	for _, method := range trait.Methods {
		if provided[method.Name] {
			continue
		}
		if method.Decl != nil && method.Decl.Body != nil {
			c.installDefault(d, impl, owner, method)
			continue
		}
		missing = append(missing, method.Name)
	}
	if len(missing) > 0 {
		c.errorHintf(d.Offset, []string{
			"the trait declares " + strings.Join(missing, ", ") + " without a body",
		}, "impl %s for %s is missing %s", trait.Name, owner, strings.Join(missing, ", "))
	}
}

func ownerName(t types.Type) string {
	switch t := t.(type) {
	case *types.Struct:
		return t.Name
	case *types.Enum:
		return t.Name
	case *types.Union:
		return t.Name
	}
	return ""
}

// ownerSelfType is the type Self stands for inside an impl: the owner
// itself, which for a generic one is parameterized by its own parameters.
func (c *checker) ownerSelfType(owner types.Type) types.Type { return owner }

// compareImplMethod holds an impl's method to the trait's signature, with
// Self and the trait's arguments replaced by what this impl gives them.
func (c *checker) compareImplMethod(decl *ast.FuncDecl, have, declared *types.Method, s subst) {
	want, _ := c.substType(declared.Sig, s).(*types.Func)
	if want == nil {
		return
	}
	if have.Recv != declared.Recv {
		c.errorf(decl.Offset, "%s takes %s, but the trait declares it taking %s",
			decl.Name, receiverText(have.Recv), receiverText(declared.Recv))
		return
	}
	if len(have.TypeParams) != len(declared.TypeParams) {
		c.errorf(decl.Offset, "%s has %d type parameters, the trait declares %d",
			decl.Name, len(have.TypeParams), len(declared.TypeParams))
		return
	}

	// The method's own generics are different objects on each side; map
	// the trait's onto the impl's before comparing.
	own := subst{}
	for i, param := range declared.TypeParams {
		own[param] = have.TypeParams[i]
	}
	want, _ = c.substType(want, own).(*types.Func)
	if !types.Identical(have.Sig, want) {
		c.errorf(decl.Offset, "%s does not match the trait: expected %s, found %s", decl.Name, want, have.Sig)
	}
}

func receiverText(r types.RecvKind) string {
	switch r {
	case types.ValueRecv:
		return "self"
	case types.PointerRecv:
		return "*self"
	}
	return "no receiver"
}

// installDefault gives a type a trait's default method. The method is the
// trait's, so every implementer shares one body; it is checked again with
// Self bound to this type, and lowered once for it.
func (c *checker) installDefault(d *ast.ImplDecl, impl *types.Impl, owner types.Type, declared *types.Method) {
	methods := methodsOf(owner)
	if methods == nil {
		return
	}
	if _, exists := methods[declared.Name]; exists {
		c.errorf(d.Offset, "%s already has a method %s, which the trait %s would also give it", owner, declared.Name, impl.Trait.Name)
		return
	}
	s := bindParams(impl.Trait.Params, impl.Args)
	s[impl.Trait.Self] = owner
	sig, _ := c.substType(declared.Sig, s).(*types.Func)
	methods[declared.Name] = &types.Method{
		Name:       declared.Name,
		Recv:       declared.Recv,
		Sig:        sig,
		TypeParams: declared.TypeParams,
		Decl:       declared.Decl,
		Trait:      impl.Trait,
		Impl:       impl,
	}
}

func methodsOf(t types.Type) map[string]*types.Method {
	switch t := t.(type) {
	case *types.Struct:
		return t.Methods
	case *types.Enum:
		return t.Methods
	case *types.Union:
		return t.Methods
	}
	return nil
}

// checkDerivable holds `impl Eq for Point` to what the compiler can build:
// every field has to have the capability itself.
func (c *checker) checkDerivable(d *ast.ImplDecl, owner, self types.Type, kind string, params []*types.TypeParam) {
	if _, ok := owner.(*types.Union); ok {
		c.errorHintf(d.Offset, []string{
			"nothing records which member of a union holds the value, so there is no field-wise " + kind + " to derive",
		}, "%s cannot be derived for a union", kind)
		return
	}
	for _, part := range ownedTypes(owner) {
		// A part written over the type's own parameters has the capability
		// for the arguments that do, which is judged where the type is
		// used with them.
		if containsTypeParam(part.typ) {
			continue
		}
		if !c.hasCapability(part.typ, kind, map[string]bool{}) {
			c.errorHintf(d.Offset, []string{
				"every " + part.what + " has to implement " + kind + " too; write its impl, or write this one out with a block",
			}, "%s cannot be derived: %s %s is %s, which does not implement it", kind, part.what, part.name, part.typ)
			return
		}
	}
}

type ownedPart struct {
	what, name string
	typ        types.Type
}

// ownedTypes lists the types a derived implementation is built from.
func ownedTypes(owner types.Type) []ownedPart {
	var parts []ownedPart
	switch t := owner.(type) {
	case *types.Struct:
		for _, field := range t.Fields {
			parts = append(parts, ownedPart{"field", field.Name, field.Type})
		}
	case *types.Enum:
		for _, variant := range t.Variants {
			for i, payload := range variant.Payload {
				name := variant.Name
				if len(variant.Payload) > 1 {
					name += "#" + string(rune('0'+i))
				}
				parts = append(parts, ownedPart{"payload", name, payload})
			}
		}
	}
	return parts
}

// ---------------------------------------------------------------------------
// Does a type implement a trait?
// ---------------------------------------------------------------------------

// userNamed reports a type whose capabilities its author states, as
// opposed to one the language or the bundled library defines. A type a C
// file declared is the layout of someone else's data and has no impl to be
// written in, so it has the capabilities its fields give it.
func (c *checker) userNamed(t types.Type) bool {
	switch t := t.(type) {
	case *types.Struct:
		return !stdlib.IsBundled(t.Module) && !c.program.foreign[t.Module]
	case *types.Enum:
		return !stdlib.IsBundled(t.Module) && !c.program.foreign[t.Module]
	}
	return false
}

// implementsRef reports whether t implements the traits a reference names.
func (c *checker) implementsRef(t types.Type, ref *types.TraitRef) bool {
	if types.IsUnknown(t) {
		return true
	}
	t = types.Default(t)

	switch t := t.(type) {
	case *types.TypeParam:
		for _, bound := range c.boundClosure(t) {
			if types.IdenticalRefs(bound, ref) {
				return true
			}
		}
		return false
	case *types.Dyn:
		if types.IdenticalRefs(t.Ref, ref) {
			return true
		}
		for _, super := range c.allSupers(t.Ref, t) {
			if types.IdenticalRefs(super, ref) {
				return true
			}
		}
		return false
	}

	// The capability traits without parameters are answered by what the type
	// is; Iterator<T> and Index<T> are impls like any other, since which T
	// matters.
	if ref.Trait.Kind != "" && len(ref.Trait.Params) == 0 {
		return c.hasCapability(t, ref.Trait.Kind, map[string]bool{})
	}

	for _, impl := range implsOf(t) {
		if impl.Trait != ref.Trait {
			continue
		}
		if !argsIdentical(c.implArgsFor(t, impl), ref.Args) {
			continue
		}
		return true
	}
	return false
}

// allSupers lists every trait a reference transitively requires.
func (c *checker) allSupers(ref *types.TraitRef, self types.Type) []*types.TraitRef {
	var out []*types.TraitRef
	for _, super := range c.superRefs(ref, self) {
		out = append(out, super)
		out = append(out, c.allSupers(super, self)...)
	}
	return out
}

// implArgsFor gives an impl's trait arguments for one instance of a
// generic type.
func (c *checker) implArgsFor(t types.Type, impl *types.Impl) []types.Type {
	var params []*types.TypeParam
	var args []types.Type
	switch t := t.(type) {
	case *types.Struct:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		params, args = origin.TypeParams, t.Args
	case *types.Enum:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		params, args = origin.TypeParams, t.Args
	}
	if len(params) == 0 || len(args) == 0 {
		return impl.Args
	}
	s := bindParams(params, args)
	out := make([]types.Type, len(impl.Args))
	for i, arg := range impl.Args {
		out[i] = c.substType(arg, s)
	}
	return out
}

// hasCapability answers for the capability traits: whether t is Eq, Hash,
// Print, Ord or has an arithmetic operator. The built-in types answer by
// what they are, and a type its author declared by the impl it wrote, whose
// derived form also needs every part of the type to have the capability.
// seen breaks the cycle a recursive type would make.
func (c *checker) hasCapability(t types.Type, kind string, seen map[string]bool) bool {
	if types.IsUnknown(t) {
		return true
	}
	t = types.Default(t)

	switch t := t.(type) {
	case *types.TypeParam:
		for _, bound := range c.boundClosure(t) {
			if bound.Trait.Kind == kind {
				return true
			}
		}
		return false
	case *types.Dyn:
		return c.implementsRef(t, c.capabilityRef(kind))
	}

	if c.userNamed(t) {
		return c.userHasCapability(t, kind, seen)
	}

	switch kind {
	case "Eq":
		return c.builtinEq(t, seen)
	case "Hash":
		return c.builtinHash(t, seen)
	case "Print":
		return c.builtinPrint(t, seen)
	case "Ord":
		return types.IsOrdered(t) || types.IsString(t)
	case "Add", "Sub", "Mul", "Div", "Neg":
		return types.IsNumeric(t)
	case "Rem":
		return types.IsInteger(t)
	}
	return false
}

// capabilityRef is the reference to a capability trait, or one that no type
// implements when the bundled library that declares it was not loaded.
func (c *checker) capabilityRef(kind string) *types.TraitRef {
	if trait := c.program.caps[kind]; trait != nil {
		return &types.TraitRef{Trait: trait}
	}
	return &types.TraitRef{Trait: &types.Trait{Name: kind, Kind: kind}}
}

func (c *checker) userHasCapability(t types.Type, kind string, seen map[string]bool) bool {
	for _, impl := range implsOf(t) {
		if impl.Trait.Kind != kind {
			continue
		}
		if !impl.Derived {
			return true
		}
		// A derived implementation holds for an instance when every part
		// of that instance has the capability, so a generic type is Eq for
		// the arguments that are.
		key := kind + ":" + t.String()
		if seen[key] {
			return true
		}
		seen[key] = true
		for _, part := range ownedTypes(t) {
			if !c.hasCapability(part.typ, kind, seen) {
				return false
			}
		}
		return true
	}
	return false
}

func (c *checker) builtinEq(t types.Type, seen map[string]bool) bool {
	switch t := t.(type) {
	case *types.Basic:
		switch t.Kind {
		case types.VoidKind, types.NullKind:
			return false
		}
		return true
	case *types.Pointer, *types.Code:
		return true
	case *types.Optional:
		return c.hasCapability(t.Elem, "Eq", seen)
	case *types.Tuple:
		return c.everyHas(t.Elems, "Eq", seen)
	case *types.Array:
		return c.hasCapability(t.Elem, "Eq", seen)
	case *types.Struct:
		return c.bundledParts(t, "Eq", seen)
	case *types.Enum:
		return c.bundledParts(t, "Eq", seen)
	}
	return false
}

func (c *checker) builtinHash(t types.Type, seen map[string]bool) bool {
	switch t := t.(type) {
	case *types.Basic:
		return types.IsHashable(t)
	case *types.Tuple:
		return c.everyHas(t.Elems, "Hash", seen)
	case *types.Struct:
		return len(t.Fields) > 0 && c.bundledParts(t, "Hash", seen)
	case *types.Enum:
		return c.bundledParts(t, "Hash", seen)
	}
	return false
}

func (c *checker) builtinPrint(t types.Type, seen map[string]bool) bool {
	switch t := t.(type) {
	case *types.Basic:
		return t.Kind != types.VoidKind
	case *types.Pointer:
		return c.hasCapability(t.Elem, "Print", seen)
	case *types.Optional:
		return c.hasCapability(t.Elem, "Print", seen)
	case *types.Slice:
		return c.hasCapability(t.Elem, "Print", seen)
	case *types.Array:
		return c.hasCapability(t.Elem, "Print", seen)
	case *types.Tuple:
		return c.everyHas(t.Elems, "Print", seen)
	case *types.Struct:
		return c.bundledParts(t, "Print", seen)
	case *types.Enum:
		return c.bundledParts(t, "Print", seen)
	case *types.Code, *types.Chan, *types.Func, *types.Future, *types.Union:
		return true
	}
	return false
}

func (c *checker) everyHas(list []types.Type, kind string, seen map[string]bool) bool {
	for _, elem := range list {
		if !c.hasCapability(elem, kind, seen) {
			return false
		}
	}
	return true
}

// bundledParts is the rule for a type the bundled library declares: it has
// the capability when its parts do. Array<T> and Mutex<T> carry nothing
// to look at, so they answer for their arguments instead.
func (c *checker) bundledParts(t types.Type, kind string, seen map[string]bool) bool {
	key := kind + ":" + t.String()
	if seen[key] {
		return true
	}
	seen[key] = true

	var args []types.Type
	switch t := t.(type) {
	case *types.Struct:
		args = t.Args
		if len(t.Fields) == 0 {
			return c.everyHas(args, kind, seen)
		}
	case *types.Enum:
		args = t.Args
	}
	for _, part := range ownedTypes(t) {
		if !c.hasCapability(part.typ, kind, seen) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Requiring a trait
// ---------------------------------------------------------------------------

// requireImpl reports a type that does not implement a trait where one
// is needed. why finishes the sentence naming what asked for it.
func (c *checker) requireImpl(offset int, t types.Type, ref *types.TraitRef, why string) bool {
	if types.IsUnknown(t) || c.implementsRef(t, ref) {
		return true
	}
	c.errorHintf(offset, c.implHelp(t, ref), "%s does not implement %s, which %s", describeType(t), ref, why)
	return false
}

// implHelp is the note under a missing implementation: how to write it.
func (c *checker) implHelp(t types.Type, ref *types.TraitRef) []string {
	// A composite lacks a capability because of a part of it: say which.
	if part := c.lackingPart(t, ref.Trait.Kind); part != nil && part != t {
		return c.implHelp(part, ref)
	}
	switch t := t.(type) {
	case *types.TypeParam:
		return []string{"add the bound to the parameter: " + t.Name + ": " + ref.String()}
	case *types.Struct, *types.Enum:
		if c.userNamed(t) {
			if derivable[ref.Trait.Kind] {
				return []string{"add: impl " + ref.String() + " for " + t.String()}
			}
			return []string{"write the impl: impl " + ref.String() + " for " + t.String() + " { ... }"}
		}
	}
	return nil
}

// requireCapability is requireImpl for a capability trait the compiler
// asked for itself, such as the Eq behind ==.
func (c *checker) requireCapability(offset int, t types.Type, kind, why string) bool {
	if types.IsUnknown(t) || c.hasCapability(t, kind, map[string]bool{}) {
		return true
	}
	ref := c.capabilityRef(kind)
	c.errorHintf(offset, c.implHelp(t, ref), "%s does not implement %s, which %s", describeType(t), kind, why)
	return false
}

// checkBounds holds the type arguments of a call or a type application to
// the bounds their parameters declare. label names what declares them, as in
// "max" or "Map<K, V>".
func (c *checker) checkBounds(offset int, label string, params []*types.TypeParam, s subst) bool {
	ok := true
	for _, param := range params {
		arg, bound := s[param]
		if !bound || len(param.Bounds) == 0 {
			continue
		}
		for _, bound := range param.Bounds {
			wanted := c.substRef(bound, s)
			if !c.requireImpl(offset, arg, wanted, label+" requires of "+param.Name) {
				ok = false
			}
		}
	}
	return ok
}

// boundsOnApply checks the bounds of a type applied to arguments. Where the
// application is written before the impls of the module are known, as a
// field type may be, the check waits for them.
func (c *checker) boundsOnApply(offset int, name string, params []*types.TypeParam, args []types.Type) bool {
	bounded := false
	for _, param := range params {
		if len(param.Bounds) > 0 {
			bounded = true
		}
	}
	if !bounded {
		return true
	}
	s := bindParams(params, args)
	label := name + "<" + paramNames(params) + ">"
	if c.boundsReady {
		return c.checkBounds(offset, label, params, s)
	}
	c.pendingBounds = append(c.pendingBounds, func() { c.checkBounds(offset, label, params, s) })
	return true
}

func paramNames(params []*types.TypeParam) string {
	names := make([]string, len(params))
	for i, param := range params {
		names[i] = param.Name
	}
	return strings.Join(names, ", ")
}

// flushBounds runs the checks that waited for the module's impls.
func (c *checker) flushBounds() {
	c.boundsReady = true
	pending := c.pendingBounds
	c.pendingBounds = nil
	for _, check := range pending {
		check()
	}
}

// declareSelf declares Self inside a method of a declared type: the type
// itself, as it is written in its own methods.
func (c *checker) declareSelf(decl *ast.FuncDecl) {
	if decl.Owner == "" {
		return
	}
	var self types.Type
	if builtin := builtinOwner(decl.Owner); builtin != nil {
		self = builtin
	} else if owner := c.module.lookup(decl.Owner); owner != nil && owner.kind == symType {
		self = owner.typ
	} else {
		return
	}
	sym := c.declare("Self", decl.Offset, symTypeParam, nil)
	sym.typ = self
}

// ---------------------------------------------------------------------------
// Calling through a bound
// ---------------------------------------------------------------------------

// boundCallee resolves a method called on a value whose type is a type
// parameter: it has to come from one of the parameter's bounds, and the
// signature is the trait's with Self replaced by the parameter.
func (c *checker) boundCallee(fun *ast.SelectorExpr, param *types.TypeParam) callee {
	for _, ref := range c.boundClosure(param) {
		// Print is the hook that replaces the derived text, and a number's
		// text is made by the runtime, so there is no to_string to call on
		// an arbitrary Print.
		if ref.Trait.Kind == "Print" {
			continue
		}
		method := ref.Trait.MethodNamed(fun.Name)
		if method == nil {
			continue
		}
		if method.Recv == types.NoRecv {
			c.errorf(fun.NameOffset, "%s is an associated function; call it as %s::%s(...)", fun.Name, param, fun.Name)
			return callee{bad: true}
		}
		return c.traitCallee(fun.Name, method, ref, param, false)
	}

	var names []string
	for _, ref := range c.boundClosure(param) {
		for _, method := range ref.Trait.Methods {
			names = append(names, method.Name)
		}
	}
	help := didYouMean(fun.Name, names)
	if len(param.Bounds) == 0 {
		help = append(help, "a type parameter with no bounds can be stored and passed on, and nothing else; bound it: "+param.Name+": SomeTrait")
	} else {
		help = append(help, "the bounds of "+param.Name+" do not provide it; bound it by a trait that declares "+fun.Name)
	}
	c.errorHintf(fun.NameOffset, help, "%s has no method %s", param, fun.Name)
	return callee{bad: true}
}

// traitCallee builds the callee for a method declared by a trait, reached
// through a bound or a dyn value.
func (c *checker) traitCallee(name string, method *types.Method, ref *types.TraitRef, self types.Type, dyn bool) callee {
	ownerParams := append([]*types.TypeParam{ref.Trait.Self}, ref.Trait.Params...)
	ownerArgs := append([]types.Type{self}, ref.Args...)
	return callee{
		name:        name,
		method:      method,
		ownerParams: ownerParams,
		ownerArgs:   ownerArgs,
		dyn:         dyn,
	}
}

// dynCallee resolves a method called on a trait value.
func (c *checker) dynCallee(fun *ast.SelectorExpr, d *types.Dyn) callee {
	for _, entry := range d.Ref.AllMethods() {
		if entry.Method.Name != fun.Name {
			continue
		}
		ref := c.refFor(d.Ref, entry.Trait)
		return c.traitCallee(fun.Name, entry.Method, ref, d, true)
	}
	var names []string
	for _, entry := range d.Ref.AllMethods() {
		names = append(names, entry.Method.Name)
	}
	c.errorHintf(fun.NameOffset, didYouMean(fun.Name, names), "%s has no method %s", d, fun.Name)
	return callee{bad: true}
}

// refFor finds the reference to wanted among a trait and what it requires.
func (c *checker) refFor(ref *types.TraitRef, wanted *types.Trait) *types.TraitRef {
	if ref.Trait == wanted {
		return ref
	}
	for _, super := range c.allSupers(ref, ref.Trait.Self) {
		if super.Trait == wanted {
			return super
		}
	}
	return ref
}

// ---------------------------------------------------------------------------
// Trait values
// ---------------------------------------------------------------------------

// resolveDyn resolves `dyn Trait`, holding the trait to what a method
// table can carry.
func (c *checker) resolveDyn(t *ast.DynType) types.Type {
	ref := c.resolveTraitRef(t.Trait)
	if ref == nil {
		return types.Unknown
	}
	if problem := c.objectSafetyProblem(ref.Trait, map[*types.Trait]bool{}); problem != "" {
		c.errorHintf(t.Offset, []string{
			"a method table holds one entry per method, so every method has to be callable without knowing the type behind the value",
		}, "%s cannot be used as dyn: %s", ref.Trait.Name, problem)
		return types.Unknown
	}
	return &types.Dyn{Ref: ref}
}

// objectSafetyProblem says why a trait cannot be a trait value, or "" when
// it can. A method is callable through the table only if it takes a
// receiver, has no generics of its own and never mentions Self beyond its
// receiver: the caller of a dyn value does not know what Self is.
func (c *checker) objectSafetyProblem(trait *types.Trait, seen map[*types.Trait]bool) string {
	if seen[trait] || trait.Decl == nil {
		return ""
	}
	seen[trait] = true
	if trait.Kind != "" {
		return "the compiler answers " + trait.Name + " by the type, with no method table"
	}
	if len(trait.Params) > 0 {
		return "it takes type parameters, and a method table holds one signature per method"
	}
	for _, decl := range trait.Decl.Methods {
		switch {
		case len(decl.TypeParams) > 0:
			return "the method " + decl.Name + " is generic"
		case mentionsSelf(decl):
			return "the method " + decl.Name + " mentions Self"
		}
	}
	for _, super := range trait.Supers {
		if problem := c.objectSafetyProblem(super.Trait, seen); problem != "" {
			return problem
		}
	}
	return ""
}

func mentionsSelf(decl *ast.FuncDecl) bool {
	for _, param := range decl.Params {
		if typeMentionsSelf(param.Type) {
			return true
		}
	}
	for _, result := range decl.Results {
		if typeMentionsSelf(result) {
			return true
		}
	}
	return false
}

func typeMentionsSelf(t ast.Type) bool {
	switch t := t.(type) {
	case *ast.NamedType:
		for _, segment := range t.Path.Segments {
			if segment.Name == "Self" {
				return true
			}
			for _, arg := range segment.Args {
				if typeMentionsSelf(arg) {
					return true
				}
			}
		}
	case *ast.PointerType:
		return typeMentionsSelf(t.Elem)
	case *ast.OptionalType:
		return typeMentionsSelf(t.Elem)
	case *ast.ArrayType:
		return typeMentionsSelf(t.Elem)
	case *ast.ChanType:
		return typeMentionsSelf(t.Elem)
	case *ast.TupleType:
		for _, element := range t.Elements {
			if typeMentionsSelf(element) {
				return true
			}
		}
	case *ast.FuncType:
		for _, param := range t.Params {
			if typeMentionsSelf(param) {
				return true
			}
		}
		for _, result := range t.Results {
			if typeMentionsSelf(result) {
				return true
			}
		}
	}
	return false
}

// castToDyn checks `pointer as dyn Trait`: the pointer's target has to
// implement the trait. The result borrows the target, which is what the
// borrow analysis sees through the cast.
func (c *checker) castToDyn(e *ast.CastExpr, value types.Type, target *types.Dyn) types.Type {
	pointer, ok := value.(*types.Pointer)
	if !ok {
		c.errorHintf(e.Offset, []string{
			"a trait value borrows what it was made from: take its address, &value as " + target.String(),
		}, "%s as %s needs a pointer to the value, not %s", describeType(value), target, describeType(value))
		return target
	}
	c.requireImpl(e.Offset, pointer.Elem, target.Ref, "the dyn value needs to call")
	return target
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

// parameterArithmetic types + - * / % with a type parameter among the
// operands. They go through the traits the parameter is bounded by, and the
// operands are one type, because Add and its siblings take and give Self.
func (c *checker) parameterArithmetic(e *ast.BinaryExpr, left, right types.Type) types.Type {
	trait, ok := operatorTraits[e.Op]
	if !ok {
		c.errorHintf(e.Offset, []string{
			"a type parameter has + - * / % through the traits Add, Sub, Mul, Div and Rem; the other operators need a concrete type",
		}, "operator %s does not apply to %s", e.Op, describeType(left))
		return types.Unknown
	}
	if !types.Identical(left, right) {
		c.operandError(e, left, right)
		return types.Unknown
	}
	if !c.requireCapability(e.Offset, left, trait.trait, "the operator "+e.Op.String()+" needs") {
		return types.Unknown
	}
	return left
}

// declaredArithmetic types an arithmetic operator on a type its author
// declared. The operator is the trait's method, so both operands are the
// type and so is the result.
func (c *checker) declaredArithmetic(e *ast.BinaryExpr, left, right types.Type) types.Type {
	trait, ok := operatorTraits[e.Op]
	if !ok || !types.Identical(left, right) {
		c.operandError(e, left, right)
		return types.Unknown
	}
	if !c.requireCapability(e.Offset, left, trait.trait, "the operator "+e.Op.String()+" needs") {
		return types.Unknown
	}
	return left
}

// parameterComparison types == and the ordering operators when a type
// parameter is an operand, and reports whether it did. Two values of one
// parameter compare when its bounds make it Eq, or Ord for ordering.
func (c *checker) parameterComparison(e *ast.BinaryExpr, left, right types.Type, kind string) bool {
	_, leftIsParam := left.(*types.TypeParam)
	_, rightIsParam := right.(*types.TypeParam)
	if !leftIsParam && !rightIsParam {
		return false
	}
	if !types.Identical(left, right) {
		other, param := right, left
		if rightIsParam {
			other, param = left, right
		}
		if isNullType(other) {
			c.errorf(e.Offset, "%s can never be null", param)
			return true
		}
		c.errorf(e.Offset, "cannot compare %s with %s", describeType(left), describeType(right))
		return true
	}
	c.requireCapability(e.Offset, left, kind, "the operator "+e.Op.String()+" needs")
	return true
}

// ---------------------------------------------------------------------------
// Iteration
// ---------------------------------------------------------------------------

// iteratorElem is the element type a for..in loop over t yields, when t
// implements Iterator<T>, or nil when it does not.
func (c *checker) iteratorElem(t types.Type) types.Type { return c.singleArg(t, "Iterator") }

// indexElem is the element type `t[i]` reaches, when t implements Index<T>.
func (c *checker) indexElem(t types.Type) types.Type { return c.singleArg(t, "Index") }

// singleArg is the T of the impl or bound of a one-parameter capability
// trait such as Iterator<T>, or nil when t has none.
func (c *checker) singleArg(t types.Type, kind string) types.Type {
	if param, ok := t.(*types.TypeParam); ok {
		for _, bound := range c.boundClosure(param) {
			if bound.Trait.Kind == kind && len(bound.Args) == 1 {
				return bound.Args[0]
			}
		}
		return nil
	}
	for _, impl := range implsOf(t) {
		if impl.Trait.Kind != kind {
			continue
		}
		if args := c.implArgsFor(t, impl); len(args) == 1 {
			return args[0]
		}
	}
	return nil
}

// lackingPart finds the declared type inside a composite that is why the
// composite lacks a capability, or nil when there is none to blame.
func (c *checker) lackingPart(t types.Type, kind string) types.Type {
	if kind == "" {
		return nil
	}
	var elems []types.Type
	switch t := t.(type) {
	case *types.Optional:
		elems = []types.Type{t.Elem}
	case *types.Array:
		elems = []types.Type{t.Elem}
	case *types.Slice:
		elems = []types.Type{t.Elem}
	case *types.Pointer:
		elems = []types.Type{t.Elem}
	case *types.Tuple:
		elems = t.Elems
	default:
		return nil
	}
	for _, elem := range elems {
		if c.hasCapability(elem, kind, map[string]bool{}) {
			continue
		}
		if part := c.lackingPart(elem, kind); part != nil {
			return part
		}
		return elem
	}
	return nil
}

// ---------------------------------------------------------------------------
// Checking what an impl brings
// ---------------------------------------------------------------------------

// instantiateImpls checks the bodies an impl gives a concrete type again with
// the type's arguments, and the trait defaults the type took with Self bound
// to it. Nothing calls these by name when an operator, a for..in loop, an
// index or a trait value's method table reaches them, so without this they
// would never have been checked for the type, and lowering would find no
// record of what they do.
func (c *checker) instantiateImpls(t types.Type) {
	if types.IsUnknown(t) || containsTypeParam(t) {
		return
	}
	var methods map[string]*types.Method
	var args []types.Type
	switch t := t.(type) {
	case *types.Struct:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		methods, args = origin.Methods, t.Args
	case *types.Enum:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		methods, args = origin.Methods, t.Args
	case *types.Union:
		methods = t.Methods
	}

	names := make([]string, 0, len(methods))
	for name := range methods {
		names = append(names, name)
	}
	sortStrings(names)

	for _, name := range names {
		method := methods[name]
		decl := method.Decl
		if decl == nil || decl.Body == nil || (decl.Impl == nil && method.Impl == nil) {
			continue
		}
		if method.Impl != nil && method.Impl.Broken {
			continue
		}
		if len(decl.OwnerParams) == 0 && len(decl.TypeParams) == 0 {
			continue
		}
		bindings := map[string]types.Type{}
		if method.Impl != nil {
			bindings["Self"] = t
		} else {
			for i, param := range decl.OwnerParams {
				if i < len(args) {
					bindings[param.Name] = args[i]
				}
			}
		}
		if len(decl.TypeParams) > 0 {
			continue
		}
		c.instantiateQuietly(decl, bindings)
	}
}

// flushInstances checks the impls of every concrete instance created since
// the last flush. It runs between declarations, where nothing is half-checked.
func (c *checker) flushInstances() {
	for len(c.program.pendingInstances) > 0 {
		t := c.program.pendingInstances[0]
		c.program.pendingInstances = c.program.pendingInstances[1:]
		c.instantiateImpls(t)
	}
}

// checkCallBounds holds a call's type arguments to the bounds of the
// parameters they stand for: the function's own, and the owner's when the
// call is what fixes them.
func (c *checker) checkCallBounds(call *ast.CallExpr, target callee, method *types.Method, s subst) bool {
	ok := c.checkBounds(call.Offset, method.Name, method.TypeParams, s)
	if target.ownerArgs == nil && len(target.ownerParams) > 0 {
		label := method.Name
		if method.Decl != nil && method.Decl.Owner != "" {
			label = method.Decl.Owner
		}
		if !c.checkBounds(call.Offset, label, target.ownerParams, s) {
			ok = false
		}
	}
	return ok
}
