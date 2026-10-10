package sema

import (
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// Associated types. A trait leaves a type for each impl to fill in, and
// `Self::Item` or `C::Item` names it. Over a type that is not known yet, a
// type parameter or another associated type, the name stays a projection
// (types.Assoc) until substitution gives the base a concrete type, and then
// it becomes what the base's impl wrote. A bound that pins the type,
// `C: Container<Item = i32>`, replaces the projection by the pin at once.

// opaque reports a type whose associated types are not known: a type
// parameter, or an associated type itself.
func opaque(t types.Type) bool {
	switch t.(type) {
	case *types.TypeParam, *types.Assoc:
		return true
	}
	return false
}

// boundsOf lists what an opaque type is declared to implement: the bounds of
// a type parameter, or those of an associated type with the trait's Self and
// arguments replaced by what the projection applies them to.
func (c *checker) boundsOf(t types.Type) []*types.TraitRef {
	switch t := t.(type) {
	case *types.TypeParam:
		return t.Bounds
	case *types.Assoc:
		declared := t.Ref.Trait.AssocNamed(t.Name)
		if declared == nil {
			return nil
		}
		s := bindParams(t.Ref.Trait.Params, t.Ref.Args)
		s[t.Ref.Trait.Self] = t.Base
		out := make([]*types.TraitRef, len(declared.Bounds))
		for i, bound := range declared.Bounds {
			out[i] = c.substRef(bound, s)
		}
		return out
	}
	return nil
}

// resolveAssocPath resolves `C::Item`: base is what the first segment names.
func (c *checker) resolveAssocPath(path *ast.Path, base types.Type) types.Type {
	if len(path.Segments) > 2 {
		c.errorf(path.Segments[2].Offset, "%s::%s cannot be reached into further", path.Segments[0].Name, path.Segments[1].Name)
		return types.Unknown
	}
	segment := path.Segments[1]
	if len(segment.Args) > 0 || len(segment.Pins) > 0 {
		c.errorf(segment.Offset, "the associated type %s takes no type arguments", segment.Name)
		return types.Unknown
	}
	if types.IsUnknown(base) {
		return types.Unknown
	}

	refs := c.assocCandidates(base, segment.Name)
	switch len(refs) {
	case 0:
		if opaque(base) {
			c.errorHintf(segment.Offset, c.noAssocHelp(base, segment.Name), "no bound of %s declares an associated type %s", base, segment.Name)
		} else {
			c.errorHintf(segment.Offset, nil, "%s implements no trait with an associated type %s", base, segment.Name)
		}
		return types.Unknown
	case 1:
		return c.projectAssoc(base, refs[0], segment.Name)
	}
	names := make([]string, len(refs))
	for i, ref := range refs {
		names[i] = ref.String()
	}
	c.errorHintf(segment.Offset, []string{
		"both " + strings.Join(names, " and ") + " declare it, and nothing says which is meant; bound the parameter by only one of them",
	}, "%s::%s is ambiguous", base, segment.Name)
	return types.Unknown
}

func (c *checker) noAssocHelp(base types.Type, name string) []string {
	var have []string
	for _, ref := range c.boundClosureOf(base) {
		for _, assoc := range ref.Trait.Assoc {
			have = append(have, assoc.Name)
		}
	}
	help := didYouMean(name, have)
	if opaque(base) {
		help = append(help, "bound "+base.String()+" by a trait that declares type "+name)
	}
	return help
}

// assocCandidates lists the trait references through which base has an
// associated type of that name: the traits an opaque base is bounded by
// and, for a concrete base, the traits it implements.
func (c *checker) assocCandidates(base types.Type, name string) []*types.TraitRef {
	var out []*types.TraitRef
	if opaque(base) {
		for _, ref := range c.boundClosureOf(base) {
			if ref.Trait.AssocNamed(name) != nil {
				out = append(out, ref)
			}
		}
		return out
	}
	for _, impl := range implsOf(base) {
		if impl.Trait.AssocNamed(name) == nil {
			continue
		}
		out = append(out, &types.TraitRef{Trait: impl.Trait, Args: c.implArgsFor(base, impl)})
	}
	return out
}

// projectAssoc is the associated type `name` of base through the trait ref.
func (c *checker) projectAssoc(base types.Type, ref *types.TraitRef, name string) types.Type {
	if types.IsUnknown(base) {
		return types.Unknown
	}
	if opaque(base) {
		for _, bound := range c.boundClosureOf(base) {
			if types.IdenticalRefs(bound, ref) {
				if pin := bound.PinNamed(name); pin != nil {
					return pin
				}
			}
		}
		return &types.Assoc{Base: base, Ref: ref, Name: name}
	}
	for _, impl := range implsOf(base) {
		if impl.Trait != ref.Trait || !argsIdentical(c.implArgsFor(base, impl), ref.Args) {
			continue
		}
		given := impl.AssocNamed(name)
		if given == nil {
			continue
		}
		return c.substType(given, c.implSubst(base))
	}
	return types.Unknown
}

// implSubst maps the parameters of a generic type's impls to the arguments of
// one instance, so what an impl wrote over T reads for Box<i32>.
func (c *checker) implSubst(t types.Type) subst {
	switch t := t.(type) {
	case *types.Struct:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		return bindParams(origin.TypeParams, t.Args)
	case *types.Enum:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		return bindParams(origin.TypeParams, t.Args)
	}
	return subst{}
}

// substAssoc rewrites a projection: with its base replaced, it is looked up
// again, which finds the impl's type once the base is concrete.
func (c *checker) substAssoc(t *types.Assoc, s subst) types.Type {
	base := c.substType(t.Base, s)
	ref := c.substRef(t.Ref, s)
	if base == t.Base && ref == t.Ref {
		return t
	}
	return c.projectAssoc(base, ref, t.Name)
}

// requirePins holds a type to the associated types a bound pins. It reports
// whether the type has each one.
func (c *checker) requirePins(offset int, t types.Type, ref *types.TraitRef) bool {
	ok := true
	for _, pin := range ref.Pins {
		have := c.projectAssoc(t, &types.TraitRef{Trait: ref.Trait, Args: ref.Args}, pin.Name)
		if types.IsUnknown(have) || flexible(have) || flexible(pin.Type) || types.Identical(have, pin.Type) {
			continue
		}
		c.errorHintf(offset, nil, "the associated type %s of %s is %s, but %s needs it to be %s",
			pin.Name, describeType(t), describeType(have), ref, describeType(pin.Type))
		ok = false
	}
	return ok
}

// pinsHold is requirePins without the report.
func (c *checker) pinsHold(t types.Type, ref *types.TraitRef) bool {
	for _, pin := range ref.Pins {
		have := c.projectAssoc(t, &types.TraitRef{Trait: ref.Trait, Args: ref.Args}, pin.Name)
		if types.IsUnknown(have) || flexible(have) || flexible(pin.Type) || types.Identical(have, pin.Type) {
			continue
		}
		return false
	}
	return true
}

// registerImplAssoc reads the `type Item = T` entries of an impl, in the
// scope that names the owner's parameters.
func (c *checker) registerImplAssoc(d *ast.ImplDecl, impl *types.Impl, owner types.Type) {
	for _, entry := range d.Assoc {
		if entry.Type == nil {
			continue
		}
		if impl.AssocNamed(entry.Name) != nil {
			c.errorf(entry.Offset, "the impl already defines the associated type %s", entry.Name)
			continue
		}
		given := c.noFormat(entry.Type.Pos(), c.resolveType(entry.Type))
		impl.Assoc = append(impl.Assoc, types.ImplAssoc{Name: entry.Name, Type: given})
	}
}

// checkImplAssoc holds an impl's associated types to its trait: every one the
// trait declares is given, no other is, and each satisfies its bounds.
func (c *checker) checkImplAssoc(d *ast.ImplDecl, impl *types.Impl, owner types.Type, s subst) {
	trait := impl.Trait
	for _, entry := range d.Assoc {
		if trait.AssocNamed(entry.Name) == nil {
			c.errorf(entry.Offset, "%s is not an associated type of %s", entry.Name, trait.Name)
		}
	}

	var missing []string
	for _, declared := range trait.Assoc {
		given := impl.AssocNamed(declared.Name)
		if given == nil {
			if !hasAssocEntry(d, declared.Name) {
				missing = append(missing, declared.Name)
			}
			continue
		}
		offset := d.Offset
		for _, entry := range d.Assoc {
			if entry.Name == declared.Name {
				offset = entry.Offset
			}
		}
		for _, bound := range declared.Bounds {
			wanted := c.substRef(bound, s)
			if flexible(given) || c.implementsRef(given, wanted) {
				continue
			}
			c.errorHintf(offset, c.implHelp(given, wanted),
				"the associated type %s of impl %s for %s is %s, which does not implement %s",
				declared.Name, trait.Name, owner, describeType(given), wanted)
		}
	}
	if len(missing) > 0 {
		c.errorHintf(d.Offset, []string{
			"write type " + missing[0] + " = ... in the block",
		}, "impl %s for %s is missing the associated type %s", trait.Name, owner, strings.Join(missing, ", "))
	}
}

func hasAssocEntry(d *ast.ImplDecl, name string) bool {
	for _, entry := range d.Assoc {
		if entry.Name == name {
			return true
		}
	}
	return false
}
