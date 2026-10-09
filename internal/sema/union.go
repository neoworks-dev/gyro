package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// A union is one storage with several names for it, and nothing records
// which of them is live. Gyro does not declare one: it arrives through an
// import of a C file, as C declared it, so its members are C's types and
// there is nothing here to restrict.

// unionLit types `Value{ i: 42 }`. One storage holds one member, so a
// literal names exactly one — which is also C's rule for a braced
// initializer, and the reason it is not the struct literal's "every
// field exactly once".
func (c *checker) unionLit(e *ast.StructLit, union *types.Union) types.Type {
	// An empty literal is spelled positionally but says nothing about
	// position, so the count is the honest complaint about it.
	if e.Positional && len(e.Fields) > 0 {
		c.errorHintf(e.Offset, []string{
			"name the member: " + union.Name + "{ " + memberExample(union) + " }",
		}, "a union literal names the member it sets; position would not say which")
		c.structLitValuesOnly(e)
		return union
	}

	if union.Module != c.modulePath {
		for _, member := range union.Members {
			if !member.Exported {
				c.errorf(e.Offset, "a literal of %s is not allowed here: the member %s is private to its module; construct it with an associated function",
					union.Name, member.Name)
				break
			}
		}
	}

	if len(e.Fields) != 1 {
		c.errorHintf(e.Offset, []string{
			"the members share one storage, so writing one is writing all of it",
		}, "a union literal sets exactly one member, not %d", len(e.Fields))
		c.structLitValuesOnly(e)
		return union
	}

	field := e.Fields[0]
	declared := union.MemberNamed(field.Name)
	if declared == nil {
		c.errorHintf(field.Offset, didYouMean(field.Name, unionMemberNames(union)),
			"%s has no member %s", union.Name, field.Name)
		c.expr(field.Value)
		return union
	}
	c.assignExpr(field.Value, declared.Type, "the member "+field.Name)
	return union
}

func unionMemberNames(union *types.Union) []string {
	names := make([]string, len(union.Members))
	for i, member := range union.Members {
		names[i] = member.Name
	}
	return names
}

func memberExample(union *types.Union) string {
	if len(union.Members) == 0 {
		return ""
	}
	return union.Members[0].Name + ": ..."
}

// holdsUnion reports whether a type is a union or reaches one by value.
// Equality, hashing and printing are all derived field by field, and
// none of them has anything to derive from a member set with no live
// one recorded — so they refuse the whole aggregate rather than making
// up an answer for the part.
func holdsUnion(t types.Type, seen map[*types.Struct]bool) bool {
	switch t := t.(type) {
	case *types.Union:
		return true
	case *types.Struct:
		if seen[t] {
			return false
		}
		seen[t] = true
		for _, field := range t.Fields {
			if holdsUnion(field.Type, seen) {
				return true
			}
		}
	case *types.Array:
		return holdsUnion(t.Elem, seen)
	case *types.Tuple:
		for _, elem := range t.Elems {
			if holdsUnion(elem, seen) {
				return true
			}
		}
	case *types.Optional:
		return holdsUnion(t.Elem, seen)
	case *types.Enum:
		for _, variant := range t.Variants {
			for _, payload := range variant.Payload {
				if holdsUnion(payload, seen) {
					return true
				}
			}
		}
	}
	return false
}
