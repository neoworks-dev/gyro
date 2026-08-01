package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// pattern checks a match pattern against the subject's type, declaring
// whatever the pattern binds into the current (per-arm) scope.
func (c *checker) pattern(p ast.Pattern, subject types.Type) {
	switch p := p.(type) {
	case *ast.WildcardPattern:
		// Matches anything, binds nothing.

	case *ast.LiteralPattern:
		value := c.exprWith(p.Value, subject)
		value = c.adaptUntyped(p.Value, value, subject)
		if flexible(value) || flexible(subject) {
			return
		}
		if !c.comparable(value, subject) {
			c.errorf(p.Value.Pos(), "this pattern is %s; it can never match %s", describeType(value), subject)
		}

	case *ast.BindingPattern:
		c.declareValue(p.Name, p.Offset, p, subject)

	case *ast.VariantPattern:
		c.variantPattern(p, subject)
	}
}

func (c *checker) variantPattern(p *ast.VariantPattern, subject types.Type) {
	resolved := c.resolvePathee(p.Path)
	if resolved.bad {
		c.bindPayloadLeniently(p)
		return
	}

	if resolved.variant != nil {
		c.enumVariantPattern(p, resolved, subject)
		return
	}

	if strct, ok := resolved.typ.(*types.Struct); ok {
		c.typeTestPattern(p, strct, subject)
		return
	}

	if resolved.valueType != nil && flexible(resolved.valueType) {
		c.bindPayloadLeniently(p)
		return
	}
	c.errorf(p.Offset, "a pattern names an enum variant or a struct type test; this is neither")
	c.bindPayloadLeniently(p)
}

func (c *checker) bindPayloadLeniently(p *ast.VariantPattern) {
	for _, payload := range p.Payload {
		c.pattern(payload, types.Unknown)
	}
}

func (c *checker) enumVariantPattern(p *ast.VariantPattern, resolved pathee, subject types.Type) {
	enum := resolved.variantEnum
	variant := resolved.variant

	// Prefer the subject's instance, so the payload comes out with the
	// subject's type arguments.
	if subjectEnum, ok := subject.(*types.Enum); ok && sameEnumOrigin(subjectEnum, enum) {
		enum = subjectEnum
		if v := subjectEnum.VariantNamed(variant.Name); v != nil {
			variant = v
		}
	} else if !flexible(subject) {
		c.errorf(p.Offset, "a %s pattern can never match %s", enum.Name, subject)
	}

	if len(p.Payload) != len(variant.Payload) {
		c.errorf(p.Offset, "%s::%s carries %d value(s), the pattern binds %d",
			enum.Name, variant.Name, len(variant.Payload), len(p.Payload))
	}
	for i, payload := range p.Payload {
		if i < len(variant.Payload) {
			c.pattern(payload, variant.Payload[i])
		} else {
			c.pattern(payload, types.Unknown)
		}
	}
}

func sameEnumOrigin(a, b *types.Enum) bool {
	originOf := func(e *types.Enum) *types.Enum {
		if e.Origin != nil {
			return e.Origin
		}
		return e
	}
	return originOf(a) == originOf(b)
}

// typeTestPattern is `RangeError(e)` on an error value: it matches when
// the concrete type is RangeError and binds the whole struct.
func (c *checker) typeTestPattern(p *ast.VariantPattern, strct *types.Struct, subject types.Type) {
	if !flexible(subject) && !isErrorBasic(subject) {
		c.errorf(p.Offset, "a type test needs an error subject whose concrete type is unknown; this subject is %s", subject)
	}
	if !c.satisfiesError(strct) && !flexible(subject) {
		c.errorf(p.Offset, "%s has no message(): string method, so an error can never hold one", strct.Name)
	}

	if len(p.Payload) != 1 {
		c.errorf(p.Offset, "a type test binds the whole value: %s(name)", strct.Name)
		c.bindPayloadLeniently(p)
		return
	}
	c.pattern(p.Payload[0], strct)
}
