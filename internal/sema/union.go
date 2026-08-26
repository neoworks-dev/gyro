package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// A union is one storage with several names for it, and nothing records
// which of them is live. That single fact decides everything this file
// checks.
//
// What may be a member is bytes and nothing else. Reading a member that
// was never written reinterprets whatever is there, which is C's rule
// and is undefined in C too — but a Gyro value whose meaning the runtime
// keeps *beside* the bytes turns "undefined" into "the runtime follows a
// number as if it were its own object". A `string` is a pointer to
// Gyro's own header; a slice is a pointer and a length the runtime
// trusts; an `Array<T>` is a runtime handle; an enum is a tag that must
// name a real variant. None of those survives being reinterpreted, so
// none of them is a member.
//
// A raw pointer is a member, and deliberately: `union { i: i64, p: *Node }`
// is what half of C's tagged unions are, Gyro's pointers are unmanaged,
// and nothing about a `*T` is kept anywhere but in the bytes.

func (c *checker) checkUnionDecl(decl *ast.UnionDecl) {
	built := c.unionTypes[decl]
	if built == nil {
		return
	}

	if ast.FindDecorator(decl.Decorators, "extern") != nil {
		// @extern marks the fieldless struct that stands for an opaque C
		// pointer. A union is the opposite of opaque: it is declared
		// precisely so its members can be read.
		c.errorf(decl.Offset, "@extern union %s: @extern marks an opaque pointer, and a union is declared to be read into", decl.Name)
		return
	}

	if len(decl.Members) == 0 {
		c.errorf(decl.Offset, "union %s declares no members; a union is storage, and there would be none", decl.Name)
		return
	}

	seen := map[string]bool{}
	for i, member := range decl.Members {
		if seen[member.Name] {
			c.errorf(member.Offset, "the member %s is declared twice", member.Name)
			continue
		}
		seen[member.Name] = true

		if i >= len(built.Members) {
			continue
		}
		if bad := unionMemberOffender(built.Members[i].Type, map[*types.Struct]bool{}); bad != "" {
			c.errorHintf(member.Offset, []string{
				"a union member is bytes: nothing records which member is live, so a value the runtime tracks beside its bytes cannot be one",
			}, "%s cannot be a union member: %s", built.Members[i].Type, bad)
		}
	}
}

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

// unionMemberOffender names the part of a type that cannot share
// storage, or "".
func unionMemberOffender(t types.Type, seen map[*types.Struct]bool) string {
	switch t := t.(type) {
	case *types.Basic:
		switch t.Kind {
		case types.Invalid:
			// Already reported where it failed to resolve.
			return ""
		case types.StringKind:
			return "a string is a pointer to Gyro's own header"
		case types.ErrorKind:
			return "an error is a value with a method table behind it"
		case types.AnyKind:
			return "an any is a type descriptor beside a payload"
		}
		return ""
	case *types.Union:
		return ""
	case *types.Struct:
		if t.Extern != "" {
			return "" // an opaque address, which is what it is made of
		}
		if isRuntimeHandle(t) {
			// One word, like a pointer, but the far end is an object the
			// runtime owns and checks. A handle read out of a member
			// nothing wrote is a number the runtime would follow.
			return t.Name + " is a handle to an object the runtime owns"
		}
		if seen[t] {
			return ""
		}
		seen[t] = true
		for _, field := range t.Fields {
			if bad := unionMemberOffender(field.Type, seen); bad != "" {
				return bad
			}
		}
		return ""
	case *types.Array:
		return unionMemberOffender(t.Elem, seen)
	case *types.Pointer, *types.Code:
		// An address is bytes. Reading one out of a member nothing wrote
		// is C's own hazard and Gyro's pointers are C's.
		return ""
	case *types.Optional:
		return "a nullable is a flag beside a payload, and the flag would be bytes from another member"
	case *types.Slice:
		return "a slice is a pointer and a length the runtime trusts"
	case *types.Enum:
		return t.Name + " is an enum, and its tag would be bytes from another member"
	case *types.Tuple:
		return "a tuple is a Gyro layout rather than storage C would lay out"
	case *types.Chan:
		return "a channel is a Gyro runtime object"
	case *types.Future:
		return "a future is a Gyro runtime object"
	case *types.Func:
		return "a fn is code and a captured environment; a code pointer is the one that is bytes"
	}
	return ""
}
