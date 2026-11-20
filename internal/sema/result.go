package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// Errors travel in the result of the function that raises them. A function
// that can fail returns `T | Error`; one with no value to return writes
// `Error?`, where null is success. `x?` takes the value of the first form
// and sends the error of either back to the caller.

// isErrorOptional reports `Error?`, the result of a function that has
// nothing to return when it succeeds.
func isErrorOptional(t types.Type) bool {
	optional, ok := t.(*types.Optional)
	return ok && isErrorBasic(optional.Elem)
}

// tryExpr checks `x?`. It is the value of a `T | Error`, and nothing for an
// `Error?`, and in the other case it is a return, so the function around it
// has to be able to return an error and its unfreed allocations are
// reported as at any other exit.
func (c *checker) tryExpr(e *ast.TryExpr) types.Type {
	operand := c.expr(e.X)

	var value types.Type
	switch t := operand.(type) {
	case *types.Result:
		value = t.Value
	case *types.Optional:
		if isErrorBasic(t.Elem) {
			value = types.Void
		}
	}
	if value == nil {
		if !flexible(operand) {
			c.errorHintf(e.Offset, []string{"? takes a call that can fail, one returning T | Error or Error?"},
				"? needs a T | Error or an Error?, not %s", describeType(operand))
		}
		return types.Unknown
	}

	c.requireFallible(e.Offset)
	c.checkLeaksAtExit()
	return value
}

// requireFallible reports a `?` in a function that has nowhere to send the
// error.
func (c *checker) requireFallible(offset int) {
	if c.fn == nil {
		c.errorf(offset, "? returns from a function, and this is not inside one")
		return
	}
	want := c.fn.result
	if want == nil || types.IsUnknown(want) || isErrorOptional(want) {
		return
	}
	if _, ok := want.(*types.Result); ok {
		return
	}
	c.errorHintf(offset, []string{"make this function return T | Error, or Error? when it has no value"},
		"? sends an error back to the caller, but this function returns %s", describeType(want))
}

// resultPattern is `Error(e)` or `i32(n)` on a `T | Error`: it matches the
// error or the value and binds it, narrowed to that type. The two arms
// together are exhaustive.
func (c *checker) resultPattern(node ast.Node, typ ast.Type, payload []ast.Pattern, result *types.Result) coverage {
	held := c.resolveType(typ)
	if flexible(held) {
		c.bindPayloadPatterns(payload, types.Unknown)
		return coverage{}
	}

	side := ""
	switch {
	case isErrorBasic(held):
		side = "error"
	case types.Identical(held, result.Value):
		side = "value"
	default:
		c.errorf(node.Pos(), "%s holds an Error or a %s, so a test for %s can never match", result, result.Value, held)
		c.bindPayloadPatterns(payload, types.Unknown)
		return coverage{}
	}

	if len(payload) != 1 {
		c.errorf(node.Pos(), "a type test binds the whole value: %s(name)", held)
		c.bindPayloadPatterns(payload, held)
		return coverage{}
	}
	c.recordLocal(node, TypeTestLocal, held)
	if !c.pattern(payload[0], held).irrefutable {
		return coverage{}
	}
	return coverage{variant: side}
}

func (c *checker) bindPayloadPatterns(payload []ast.Pattern, subject types.Type) {
	for _, p := range payload {
		c.pattern(p, subject)
	}
}
