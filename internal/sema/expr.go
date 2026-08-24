package sema

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// expr types an expression with no expected type.
func (c *checker) expr(e ast.Expr) types.Type {
	return c.exprWith(e, nil)
}

// exprWith types an expression and records the result. want, when non-nil,
// is the type the context requires; it types untyped literals and infers
// closure parameters, and nothing else — typed values never convert.
func (c *checker) exprWith(e ast.Expr, want types.Type) types.Type {
	t := c.exprValue(e, want)
	c.record(e, t)
	return t
}

func (c *checker) exprValue(e ast.Expr, want types.Type) types.Type {
	switch e := e.(type) {
	case *ast.Ident:
		return c.identExpr(e)

	case *ast.Path:
		return c.pathValue(e)

	case *ast.SelfExpr:
		if c.fn == nil || !c.fn.inMethod {
			c.errorf(e.Offset, "self is only available inside a method")
			return types.Unknown
		}
		return c.fn.selfType

	case *ast.BasicLit:
		return c.basicLit(e, want)

	case *ast.StringLit:
		for _, part := range e.Parts {
			if part.Expr != nil {
				c.expr(part.Expr)
			}
		}
		return types.String

	case *ast.ArrayLit:
		return c.arrayLit(e, want)

	case *ast.TupleLit:
		return c.tupleLit(e, want)

	case *ast.StructLit:
		return c.structLit(e)

	case *ast.UnaryExpr:
		return c.unaryExpr(e)

	case *ast.BinaryExpr:
		return c.binaryExpr(e)

	case *ast.CallExpr:
		result := c.callExpr(e)
		c.settleCallArgs(e)
		c.checkSpawnRace(e)
		return result

	case *ast.SelectorExpr:
		return c.selectorExpr(e)

	case *ast.TupleIndexExpr:
		return c.tupleIndexExpr(e)

	case *ast.IndexExpr:
		return c.indexExpr(e)

	case *ast.RangeExpr:
		c.expr(e.Lo)
		c.expr(e.Hi)
		c.errorf(e.Offset, "a range is not a value; `..` only appears in for..in")
		return types.Unknown

	case *ast.MatchExpr:
		return c.matchExpr(e, want)

	case *ast.BlockExpr:
		c.block(e.Block)
		return types.Void

	case *ast.AwaitExpr:
		return c.awaitExpr(e)

	case *ast.RecvExpr:
		return c.recvExpr(e)

	case *ast.ChanLit:
		elem := c.resolveType(e.Elem)
		if e.Cap != nil {
			capType := c.expr(e.Cap)
			if !types.IsInteger(capType) && !flexible(capType) {
				c.errorf(e.Cap.Pos(), "a channel capacity is an integer, not %s", capType)
			}
		}
		return &types.Chan{Elem: elem}

	case *ast.ClosureExpr:
		return c.closureExpr(e, want)

	case *ast.CastExpr:
		return c.castExpr(e)
	}
	return types.Unknown
}

// ---------------------------------------------------------------------------
// Names
// ---------------------------------------------------------------------------

func (c *checker) identExpr(e *ast.Ident) types.Type {
	sym := c.resolveIdent(e)
	if sym == nil {
		return types.Unknown
	}
	c.checkAssigned(sym, e.Offset, e.Name)
	c.noteCapture(sym)
	return c.symbolValueType(sym, e.Offset, e.Name)
}

// resolveIdent looks a bare name up, reporting it when it resolves to
// nothing. `_` discards and `<error>` marks parser recovery; neither is a
// name.
func (c *checker) resolveIdent(e *ast.Ident) *symbol {
	if e.Name == "_" || e.Name == "<error>" {
		return nil
	}
	sym := c.scope.lookup(e.Name)
	if sym == nil {
		c.reportUndeclared(e.Offset, "name", e.Name)
		return nil
	}
	c.recordRef(e, sym.decl)
	return sym
}

// symbolValueType is the type a symbol has when it is used as a value,
// with narrowing applied.
func (c *checker) symbolValueType(sym *symbol, offset int, name string) types.Type {
	switch sym.kind {
	case symType:
		c.errorf(offset, "%s is a type, not a value", name)
		return types.Unknown
	case symNamespace:
		c.errorf(offset, "%s is a module, not a value", name)
		return types.Unknown
	case symTypeParam:
		c.errorf(offset, "%s is a type parameter, not a value", name)
		return types.Unknown
	}

	if sym.fn != nil && len(sym.fn.TypeParams) > 0 && sym.typ == nil {
		c.errorf(offset, "%s is generic; it must be called so its type parameters can be inferred", name)
		return types.Unknown
	}
	if sym.typ == nil {
		return types.Unknown
	}
	if c.facts[sym] {
		if optional, ok := sym.typ.(*types.Optional); ok {
			return optional.Elem
		}
	}
	return sym.typ
}

// ---------------------------------------------------------------------------
// Literals and constants
// ---------------------------------------------------------------------------

func (c *checker) basicLit(e *ast.BasicLit, want types.Type) types.Type {
	switch e.Kind {
	case token.Int:
		if value, ok := new(big.Int).SetString(e.Value, 0); ok {
			c.consts[e] = value
		}
		return c.adaptUntyped(e, types.UntypedIntType, want)
	case token.Float:
		return c.adaptUntyped(e, types.UntypedFloatType, want)
	case token.String:
		return types.String
	case token.Char:
		return types.Rune
	case token.True, token.False:
		return types.Bool
	case token.Null:
		return types.Null
	}
	return types.Unknown
}

// adaptUntyped types an untyped literal against the type its context
// requires, checking that the value fits. Without a usable context the
// literal keeps its untyped type, to be defaulted where it is stored.
func (c *checker) adaptUntyped(e ast.Expr, t types.Type, want types.Type) types.Type {
	if want == nil || (!isUntypedInt(t) && !isUntypedFloat(t)) {
		return t
	}

	target := want
	for {
		if optional, ok := target.(*types.Optional); ok {
			target = optional.Elem
			continue
		}
		break
	}

	basic, ok := target.(*types.Basic)
	if !ok || !types.IsNumeric(basic) {
		return t
	}

	if isUntypedFloat(t) && types.IsInteger(basic) {
		c.errorf(e.Pos(), "a float literal cannot become %s", basic)
		return types.Unknown
	}
	if isUntypedInt(t) && types.IsInteger(basic) {
		if value, ok := c.consts[e]; ok && !intFits(value, basic.Kind) {
			c.errorf(e.Pos(), "the literal %s does not fit %s", value.String(), basic)
			return types.Unknown
		}
	}
	c.record(e, basic)
	return basic
}

func isUntypedInt(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind == types.UntypedInt
}

func isUntypedFloat(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind == types.UntypedFloat
}

var intBounds = map[types.BasicKind][2]*big.Int{}

func init() {
	bound := func(bits uint, signed bool) [2]*big.Int {
		if signed {
			max := new(big.Int).Lsh(big.NewInt(1), bits-1)
			min := new(big.Int).Neg(max)
			return [2]*big.Int{min, new(big.Int).Sub(max, big.NewInt(1))}
		}
		max := new(big.Int).Lsh(big.NewInt(1), bits)
		return [2]*big.Int{big.NewInt(0), new(big.Int).Sub(max, big.NewInt(1))}
	}
	intBounds[types.I8] = bound(8, true)
	intBounds[types.I16] = bound(16, true)
	intBounds[types.I32] = bound(32, true)
	intBounds[types.I64] = bound(64, true)
	intBounds[types.U8] = bound(8, false)
	intBounds[types.U16] = bound(16, false)
	intBounds[types.U32] = bound(32, false)
	intBounds[types.U64] = bound(64, false)
	intBounds[types.Usize] = bound(64, false)
}

func intFits(value *big.Int, kind types.BasicKind) bool {
	bounds, ok := intBounds[kind]
	if !ok {
		return true
	}
	return value.Cmp(bounds[0]) >= 0 && value.Cmp(bounds[1]) <= 0
}

// ---------------------------------------------------------------------------
// Assignability
// ---------------------------------------------------------------------------

// assignExpr types an expression against a target type and reports a
// mismatch. context finishes the sentence "cannot use X as Y in <context>".
func (c *checker) assignExpr(e ast.Expr, target types.Type, context string) {
	value := c.exprWith(e, target)
	c.assignValue(e.Pos(), e, value, target, context)
}

func (c *checker) assignValue(offset int, e ast.Expr, value, target types.Type, context string) {
	if e != nil {
		value = c.adaptUntyped(e, value, target)
	}
	if !c.assignableType(value, target) {
		c.errorLabelf(offset, fmt.Sprintf("expected %s", target),
			"cannot use %s as %s in %s", describeType(value), target, context)
	}
}

func describeType(t types.Type) string {
	if isUntypedInt(t) {
		return "an integer literal"
	}
	if isUntypedFloat(t) {
		return "a float literal"
	}
	if basic, ok := t.(*types.Basic); ok && basic.Kind == types.NullKind {
		return "null"
	}
	return t.String()
}

// assignableType reports whether a value of one type can be stored where
// the other is required. There are no implicit conversions between typed
// values; what this allows beyond identity is exactly: null into T?, T into
// T?, a fixed-size array borrowed as a slice, and any type with
// message(): string used as an error.
func (c *checker) assignableType(value, target types.Type) bool {
	if flexible(value) || flexible(target) {
		return true
	}
	if types.Identical(value, target) {
		return true
	}

	if basic, ok := value.(*types.Basic); ok {
		switch basic.Kind {
		case types.NullKind:
			_, isOptional := target.(*types.Optional)
			return isOptional
		case types.UntypedInt:
			return types.IsNumeric(target)
		case types.UntypedFloat:
			return types.IsFloat(target)
		}
	}

	switch target := target.(type) {
	case *types.Optional:
		return c.assignableType(value, target.Elem)
	case *types.Basic:
		if target.Kind == types.ErrorKind {
			return c.satisfiesError(value)
		}
	case *types.Slice:
		if array, ok := value.(*types.Array); ok {
			return types.Identical(array.Elem, target.Elem)
		}
	case *types.Tuple:
		// Tuples are structural, so a tuple value fits elementwise:
		// (string, null) goes where (string, error?) is required.
		tuple, ok := value.(*types.Tuple)
		if !ok || len(tuple.Elems) != len(target.Elems) {
			return false
		}
		for i, elem := range tuple.Elems {
			if !c.assignableType(elem, target.Elems[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// satisfiesError implements the structural error contract: any type with a
// message(): string method is an error.
func (c *checker) satisfiesError(t types.Type) bool {
	if basic, ok := t.(*types.Basic); ok && basic.Kind == types.ErrorKind {
		return true
	}
	method, _ := c.lookupMethod(t, "message")
	if method == nil || method.Recv == types.NoRecv || len(method.TypeParams) > 0 {
		return false
	}
	return len(method.Sig.Params) == 0 && types.Identical(method.Sig.Result, types.String)
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

func (c *checker) unaryExpr(e *ast.UnaryExpr) types.Type {
	// &x names a place rather than reading one. The storage is there and
	// sized whether or not anything has been written to it yet, so taking
	// the address is what puts the local beyond this analysis: writes
	// through the pointer are the point, and they cannot be followed here.
	if e.Op == token.Amp {
		c.markAssignedTarget(e.X)
	}

	operand := c.expr(e.X)

	switch e.Op {
	case token.Minus:
		if flexible(operand) || types.IsNumeric(operand) {
			c.foldUnary(e, operand)
			return operand
		}
		c.errorf(e.Offset, "operator - negates numeric values, not %s", operand)

	case token.Bang:
		if flexible(operand) || types.Identical(operand, types.Bool) {
			return types.Bool
		}
		c.errorf(e.Offset, "operator ! needs a bool, not %s", operand)
		return types.Bool

	case token.Tilde:
		if flexible(operand) || types.IsInteger(operand) {
			return operand
		}
		c.errorf(e.Offset, "operator ~ inverts integers, not %s", operand)

	case token.Amp:
		c.killFactsFor(e.X)
		return &types.Pointer{Elem: types.Default(operand)}

	case token.Star:
		return c.derefType(e.Offset, operand)
	}
	return types.Unknown
}

func (c *checker) foldUnary(e *ast.UnaryExpr, operand types.Type) {
	if !isUntypedInt(operand) {
		return
	}
	if value, ok := c.consts[e.X]; ok {
		c.consts[e] = new(big.Int).Neg(value)
	}
}

func (c *checker) derefType(offset int, operand types.Type) types.Type {
	switch t := operand.(type) {
	case *types.Pointer:
		return t.Elem
	case *types.Optional:
		c.errorf(offset, "this value is %s and may be null; check it against null before dereferencing", t)
		return types.Unknown
	}
	if flexible(operand) {
		return types.Unknown
	}
	c.errorf(offset, "cannot dereference %s; it is not a pointer", operand)
	return types.Unknown
}

func (c *checker) binaryExpr(e *ast.BinaryExpr) types.Type {
	switch e.Op {
	case token.AndAnd, token.OrOr:
		c.condition(e)
		return types.Bool
	case token.Eq, token.NotEq:
		c.equality(e)
		return types.Bool
	case token.Lt, token.LtEq, token.Gt, token.GtEq:
		c.ordering(e)
		return types.Bool
	case token.Shl, token.Shr:
		return c.shift(e)
	}
	return c.arithmetic(e)
}

// arithmetic covers + - * / %, the wrapping +% -% *%, and the bitwise
// & | ^. Both operands must be the same numeric type; only literals adapt.
// Operands are typed without the outer context, so an all-literal
// expression folds first and the folded value is what must fit.
func (c *checker) arithmetic(e *ast.BinaryExpr) types.Type {
	left := c.expr(e.X)
	right := c.expr(e.Y)

	if flexible(left) || flexible(right) {
		return c.pickTyped(left, right)
	}

	// An untyped side adapts to the typed side; two untyped sides fold.
	if types.IsUntyped(left) && !types.IsUntyped(right) {
		left = c.adaptUntyped(e.X, left, right)
	} else if types.IsUntyped(right) && !types.IsUntyped(left) {
		right = c.adaptUntyped(e.Y, right, left)
	} else if types.IsUntyped(left) && types.IsUntyped(right) {
		return c.foldBinary(e, left, right)
	}

	// Adapting reports its own error when a literal does not fit, and the
	// poisoned type it hands back would only produce a second one here.
	if flexible(left) || flexible(right) {
		return c.pickTyped(left, right)
	}

	integerOnly := isIntegerOnlyOp(e.Op)
	valid := types.IsNumeric(left)
	if integerOnly {
		valid = types.IsInteger(left)
	}
	if !valid || !types.Identical(left, right) {
		c.operandError(e, left, right)
		return types.Unknown
	}
	return left
}

func isIntegerOnlyOp(op token.Kind) bool {
	switch op {
	case token.PlusMod, token.MinusMod, token.StarMod,
		token.Amp, token.Pipe, token.Caret, token.Percent:
		return true
	}
	return false
}

func (c *checker) operandError(e *ast.BinaryExpr, left, right types.Type) {
	if types.Identical(left, types.String) || types.Identical(right, types.String) {
		if e.Op == token.Plus {
			c.errorf(e.Offset, "strings do not concatenate with +; interpolate instead: \"#{a}#{b}\"")
			return
		}
	}
	integerOnly := isIntegerOnlyOp(e.Op)
	kind := "matching numeric operands"
	if integerOnly {
		kind = "matching integer operands"
	}
	c.errorHintf(e.Offset, operandHelp(integerOnly, left, right),
		"operator %s needs %s, got %s and %s",
		e.Op, kind, describeType(left), describeType(right))
}

// foldBinary evaluates an operation on two untyped literals, so the result
// stays a literal that later context can still type.
func (c *checker) foldBinary(e *ast.BinaryExpr, left, right types.Type) types.Type {
	if isUntypedFloat(left) || isUntypedFloat(right) {
		if isIntegerOnlyOp(e.Op) {
			c.errorf(e.Offset, "operator %s needs integers, got float literals", e.Op)
			return types.Unknown
		}
		return types.UntypedFloatType
	}

	x, okX := c.consts[e.X]
	y, okY := c.consts[e.Y]
	if okX && okY {
		if value := foldInts(e.Op, x, y); value != nil {
			c.consts[e] = value
		} else if e.Op == token.Slash || e.Op == token.Percent {
			c.errorf(e.Offset, "division by zero in a constant expression")
			return types.Unknown
		}
	}
	return types.UntypedIntType
}

func foldInts(op token.Kind, x, y *big.Int) *big.Int {
	switch op {
	case token.Plus, token.PlusMod:
		return new(big.Int).Add(x, y)
	case token.Minus, token.MinusMod:
		return new(big.Int).Sub(x, y)
	case token.Star, token.StarMod:
		return new(big.Int).Mul(x, y)
	case token.Slash:
		if y.Sign() == 0 {
			return nil
		}
		return new(big.Int).Quo(x, y)
	case token.Percent:
		if y.Sign() == 0 {
			return nil
		}
		return new(big.Int).Rem(x, y)
	case token.Amp:
		return new(big.Int).And(x, y)
	case token.Pipe:
		return new(big.Int).Or(x, y)
	case token.Caret:
		return new(big.Int).Xor(x, y)
	}
	return nil
}

// pickTyped resolves an operation with a flexible side: the concretely
// typed side wins. An untyped literal is not a winner — defaulting it
// against a generic operand would fix the type before instantiation gets
// to see the real one, turning `xs.len() * 2` into i32 inside a generic
// body. Flexible-versus-literal stays flexible instead.
func (c *checker) pickTyped(left, right types.Type) types.Type {
	if !flexible(left) && !types.IsUntyped(left) {
		return left
	}
	if !flexible(right) && !types.IsUntyped(right) {
		return right
	}
	return types.Unknown
}

func (c *checker) shift(e *ast.BinaryExpr) types.Type {
	left := c.expr(e.X)
	right := c.expr(e.Y)

	if !flexible(right) && !types.IsInteger(right) {
		c.errorf(e.Y.Pos(), "a shift count is an integer, not %s", describeType(right))
	}
	if flexible(left) {
		return types.Unknown
	}
	if !types.IsInteger(left) {
		c.errorf(e.Offset, "operator %s shifts integers, not %s", e.Op, describeType(left))
		return types.Unknown
	}

	if isUntypedInt(left) {
		if x, okX := c.consts[e.X]; okX {
			if y, okY := c.consts[e.Y]; okY && y.IsUint64() && y.Uint64() < 1024 {
				if e.Op == token.Shl {
					c.consts[e] = new(big.Int).Lsh(x, uint(y.Uint64()))
				} else {
					c.consts[e] = new(big.Int).Rsh(x, uint(y.Uint64()))
				}
			}
		}
	}
	return left
}

func (c *checker) equality(e *ast.BinaryExpr) {
	left := c.expr(e.X)
	right := c.expr(e.Y)

	if flexible(left) || flexible(right) {
		return
	}

	// Comparisons against null require a type that can actually be null.
	if isNullType(left) || isNullType(right) {
		other := left
		if isNullType(left) {
			other = right
		}
		if isNullType(other) {
			return
		}
		if _, ok := other.(*types.Optional); !ok {
			c.errorf(e.Offset, "%s can never be null", other)
		}
		return
	}

	if types.IsUntyped(left) && !types.IsUntyped(right) {
		left = c.adaptUntyped(e.X, left, right)
	} else if types.IsUntyped(right) && !types.IsUntyped(left) {
		right = c.adaptUntyped(e.Y, right, left)
	} else if types.IsUntyped(left) && types.IsUntyped(right) {
		return
	}

	if _, ok := left.(*types.Slice); ok {
		c.errorf(e.Offset, "slices do not compare with ==; compare the contents explicitly")
		return
	}
	if !c.comparable(left, right) {
		c.errorf(e.Offset, "cannot compare %s with %s", describeType(left), describeType(right))
	}
}

func isNullType(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind == types.NullKind
}

// comparable allows == between identical primitives, pointers, tuples,
// enums and structs, and between T? and its element.
func (c *checker) comparable(left, right types.Type) bool {
	if types.Identical(left, right) {
		return !isFunc(left) && !isChanType(left)
	}
	if optional, ok := left.(*types.Optional); ok {
		return c.comparable(optional.Elem, right)
	}
	if optional, ok := right.(*types.Optional); ok {
		return c.comparable(left, optional.Elem)
	}
	// An error compares against any concrete error type.
	if isErrorBasic(left) {
		return c.satisfiesError(right)
	}
	if isErrorBasic(right) {
		return c.satisfiesError(left)
	}
	return false
}

func isFunc(t types.Type) bool {
	_, ok := t.(*types.Func)
	return ok
}

func isChanType(t types.Type) bool {
	_, ok := t.(*types.Chan)
	return ok
}

func isErrorBasic(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind == types.ErrorKind
}

func (c *checker) ordering(e *ast.BinaryExpr) {
	left := c.expr(e.X)
	right := c.expr(e.Y)

	if flexible(left) || flexible(right) {
		return
	}

	if types.IsUntyped(left) && !types.IsUntyped(right) {
		left = c.adaptUntyped(e.X, left, right)
	} else if types.IsUntyped(right) && !types.IsUntyped(left) {
		right = c.adaptUntyped(e.Y, right, left)
	} else if types.IsUntyped(left) && types.IsUntyped(right) {
		return
	}

	if !types.IsOrdered(left) || !types.Identical(left, right) {
		c.errorf(e.Offset, "operator %s orders numeric types and rune; ordering %s is spelled a.compare(b)",
			e.Op, describeType(left))
	}
}

// ---------------------------------------------------------------------------
// Composite literals and access
// ---------------------------------------------------------------------------

func (c *checker) arrayLit(e *ast.ArrayLit, want types.Type) types.Type {
	var elemWant types.Type
	switch want := want.(type) {
	case *types.Slice:
		elemWant = want.Elem
	case *types.Array:
		elemWant = want.Elem
	}

	elem := elemWant
	for _, element := range e.Elements {
		// Storing an allocation in a literal puts it beyond this block.
		c.settleEscape(element)
		t := c.exprWith(element, elemWant)
		if elemWant != nil {
			c.assignValue(element.Pos(), element, t, elemWant, "the array literal")
			continue
		}
		t = types.Default(t)
		if elem == nil {
			elem = t
			continue
		}
		if !flexible(t) && !flexible(elem) && !types.Identical(elem, t) {
			c.errorf(element.Pos(), "the array literal mixes %s and %s", elem, t)
			elem = types.Unknown
		}
	}

	if elem == nil {
		elem = types.Unknown
	}
	return &types.Array{Elem: elem, Length: int64(len(e.Elements))}
}

func (c *checker) tupleLit(e *ast.TupleLit, want types.Type) types.Type {
	wantTuple, _ := want.(*types.Tuple)
	if wantTuple == nil {
		if optional, ok := want.(*types.Optional); ok {
			wantTuple, _ = optional.Elem.(*types.Tuple)
		}
	}

	elems := make([]types.Type, len(e.Elements))
	for i, element := range e.Elements {
		var elemWant types.Type
		if wantTuple != nil && i < len(wantTuple.Elems) {
			elemWant = wantTuple.Elems[i]
		}
		elems[i] = types.Default(c.exprWith(element, elemWant))
	}
	return &types.Tuple{Elems: elems}
}

func (c *checker) tupleIndexExpr(e *ast.TupleIndexExpr) types.Type {
	base := c.expr(e.X)
	tuple, ok := base.(*types.Tuple)
	if !ok {
		if !flexible(base) {
			c.errorf(e.Offset, "positional access needs a tuple, not %s", base)
		}
		return types.Unknown
	}
	if e.Index >= len(tuple.Elems) {
		c.errorf(e.Offset, "%s has no element %d", tuple, e.Index)
		return types.Unknown
	}
	return tuple.Elems[e.Index]
}

func (c *checker) indexExpr(e *ast.IndexExpr) types.Type {
	base := c.expr(e.X)
	index := c.expr(e.Index)

	if !flexible(index) {
		if isUntypedInt(index) {
			c.adaptUntyped(e.Index, index, types.USize)
		} else if !types.Identical(index, types.USize) {
			c.errorf(e.Index.Pos(), "indexes are usize, not %s; lengths and indexes never mix with signed arithmetic implicitly", index)
		}
	}

	switch base := base.(type) {
	case *types.Slice:
		return base.Elem
	case *types.Array:
		return base.Elem
	case *types.Optional:
		c.errorf(e.Offset, "this value is %s and may be null; check it against null before indexing", base)
		return types.Unknown
	}
	if flexible(base) {
		return types.Unknown
	}
	c.errorf(e.Offset, "%s cannot be indexed", base)
	return types.Unknown
}

// selectorExpr types `x.name`: a struct field, or nothing here — method
// calls are handled by callExpr, so a bare method reference is an error.
func (c *checker) selectorExpr(e *ast.SelectorExpr) types.Type {
	base := c.expr(e.X)
	return c.selectField(e.NameOffset, base, e.Name, true)
}

// selectField resolves field access through pointers, reporting nullable
// access. bareMethod controls the message when the name is a method rather
// than a field.
func (c *checker) selectField(offset int, base types.Type, name string, bareMethod bool) types.Type {
	t := base
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem
	}

	if optional, ok := t.(*types.Optional); ok {
		c.errorf(offset, "this value is %s and may be null; check it against null before using it", optional)
		return types.Unknown
	}
	if flexible(t) {
		return types.Unknown
	}

	if strct, ok := t.(*types.Struct); ok {
		if field := strct.FieldNamed(name); field != nil {
			c.checkFieldVisibility(offset, strct, field)
			return field.Type
		}
	}

	if method, _ := c.lookupMethod(t, name); method != nil {
		if bareMethod {
			c.errorf(offset, "%s is a method; call it: .%s(...)", name, name)
		}
		return types.Unknown
	}

	c.errorHintf(offset, didYouMean(name, memberNames(t)), "%s has no field or method %s", t, name)
	return types.Unknown
}

func (c *checker) checkFieldVisibility(offset int, strct *types.Struct, field *types.Field) {
	if !field.Exported && strct.Module != c.modulePath {
		c.errorf(offset, "%s.%s is private to its module", strct.Name, field.Name)
	}
}

func (c *checker) structLit(e *ast.StructLit) types.Type {
	// A field holding an allocation outlives this block along with the
	// struct it went into.
	for _, field := range e.Fields {
		c.settleEscape(field.Value)
	}

	resolved := c.structLitType(e.Type)

	strct, ok := resolved.(*types.Struct)
	if !ok {
		if !flexible(resolved) {
			c.errorf(e.Offset, "%s is not a struct; only structs have literals", resolved)
		}
		c.structLitValuesOnly(e)
		return resolved
	}

	// A generic struct without explicit arguments infers them from the
	// field values.
	if strct.Origin == nil && len(strct.TypeParams) > 0 && !c.lenient {
		strct = c.inferStructLit(e, strct)
	}

	if hidden := c.hiddenFields(strct); hidden != "" {
		c.errorf(e.Offset, "a literal of %s is not allowed here: the field %s is private to its module; construct it with an associated function",
			strct.Name, hidden)
	}

	if e.Positional {
		c.positionalStructLit(e, strct)
	} else {
		c.namedStructLit(e, strct)
	}
	return strct
}

// structLitType resolves the literal's type name, but leaves a generic
// struct written without arguments — Pair{ 1, 2 } — uninstantiated so the
// literal can infer them, instead of tripping the arity check.
func (c *checker) structLitType(t ast.Type) types.Type {
	named, ok := t.(*ast.NamedType)
	if !ok {
		return c.resolveType(t)
	}
	segments := named.Path.Segments
	last := segments[len(segments)-1]
	if len(last.Args) > 0 {
		return c.resolveType(t)
	}

	var entry *symbol
	switch len(segments) {
	case 1:
		entry = c.scope.lookup(last.Name)
	case 2:
		ns := c.scope.lookup(segments[0].Name)
		if ns != nil && ns.kind == symNamespace && ns.exports != nil {
			entry = ns.exports.names[last.Name]
		}
	}
	if entry != nil && entry.kind == symType {
		if strct, ok := entry.typ.(*types.Struct); ok && len(strct.TypeParams) > 0 {
			return strct
		}
	}
	return c.resolveType(t)
}

func (c *checker) structLitValuesOnly(e *ast.StructLit) {
	for _, field := range e.Fields {
		c.expr(field.Value)
	}
}

func (c *checker) hiddenFields(strct *types.Struct) string {
	if strct.Module == c.modulePath {
		return ""
	}
	for _, field := range strct.Fields {
		if !field.Exported {
			return field.Name
		}
	}
	return ""
}

func (c *checker) inferStructLit(e *ast.StructLit, origin *types.Struct) *types.Struct {
	s := subst{}
	for i, field := range e.Fields {
		var declared *types.Field
		if e.Positional {
			if i < len(origin.Fields) {
				declared = &origin.Fields[i]
			}
		} else {
			declared = origin.FieldNamed(field.Name)
		}
		if declared == nil {
			continue
		}
		value := types.Default(c.expr(field.Value))
		unify(declared.Type, value, s)
	}

	args := make([]types.Type, len(origin.TypeParams))
	for i, param := range origin.TypeParams {
		bound, ok := s[param]
		if !ok {
			c.errorf(e.Offset, "cannot infer %s for %s; write %s<...> explicitly", param.Name, origin.Name, origin.Name)
			bound = types.Unknown
		}
		args[i] = bound
	}
	return c.instantiateStruct(origin, args)
}

func (c *checker) positionalStructLit(e *ast.StructLit, strct *types.Struct) {
	if len(e.Fields) != len(strct.Fields) {
		c.errorf(e.Offset, "%s has %d fields, the literal provides %d; a literal sets every field",
			strct.Name, len(strct.Fields), len(e.Fields))
	}
	for i, field := range e.Fields {
		if i < len(strct.Fields) {
			c.assignExpr(field.Value, strct.Fields[i].Type, "the field "+strct.Fields[i].Name)
		} else {
			c.expr(field.Value)
		}
	}
}

func (c *checker) namedStructLit(e *ast.StructLit, strct *types.Struct) {
	seen := map[string]bool{}
	for _, field := range e.Fields {
		if seen[field.Name] {
			c.errorf(field.Offset, "the field %s is set twice", field.Name)
			continue
		}
		seen[field.Name] = true

		declared := strct.FieldNamed(field.Name)
		if declared == nil {
			c.errorHintf(field.Offset, didYouMean(field.Name, fieldNames(strct)),
				"%s has no field %s", strct.Name, field.Name)
			c.expr(field.Value)
			continue
		}
		c.assignExpr(field.Value, declared.Type, "the field "+field.Name)
	}

	for _, declared := range strct.Fields {
		if !seen[declared.Name] {
			c.errorf(e.Offset, "the literal misses the field %s; there are no zero values, so a literal sets every field", declared.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Channels, async, casts
// ---------------------------------------------------------------------------

func (c *checker) recvExpr(e *ast.RecvExpr) types.Type {
	operand := c.expr(e.Chan)
	if channel, ok := operand.(*types.Chan); ok {
		return channel.Elem
	}
	if !flexible(operand) {
		c.errorf(e.Offset, "receive needs a channel, not %s", operand)
	}
	return types.Unknown
}

func (c *checker) awaitExpr(e *ast.AwaitExpr) types.Type {
	if c.fn != nil && !c.fn.async {
		c.errorf(e.Offset, "await only works inside an async fn")
	}
	operand := c.expr(e.X)
	if future, ok := operand.(*types.Future); ok {
		return future.Result
	}
	if !flexible(operand) {
		c.errorf(e.Offset, "await needs a call to an async fn, not %s", describeType(operand))
	}
	return types.Unknown
}

func (c *checker) castExpr(e *ast.CastExpr) types.Type {
	value := c.expr(e.X)
	target := c.resolveType(e.Type)

	if flexible(value) || flexible(target) {
		return target
	}
	if !castable(value) || !castable(target) {
		c.errorf(e.Offset, "as converts between numeric types and rune; %s as %s does not",
			describeType(value), target)
		return target
	}
	return target
}

func castable(t types.Type) bool {
	if types.IsNumeric(t) {
		return true
	}
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind == types.RuneKind
}

// ---------------------------------------------------------------------------
// Match
// ---------------------------------------------------------------------------

func (c *checker) matchExpr(e *ast.MatchExpr, want types.Type) types.Type {
	subject := c.expr(e.Subject)

	var result types.Type
	covered := map[string]bool{}
	irrefutable := false

	// One arm runs, so what survives the match is what every arm assigned.
	// Falling off the end aborts, so it contributes nothing to the merge.
	var merged flow
	firstArm := true

	for _, arm := range e.Arms {
		var armCoverage coverage
		armFlow := c.branchFlow(func() {
			c.pushScope(func() {
				armCoverage, result = c.matchArm(arm, subject, want, result)
			})
		})

		if armCoverage.irrefutable {
			irrefutable = true
		}
		if armCoverage.variant != "" {
			covered[armCoverage.variant] = true
		}

		if firstArm {
			merged, firstArm = armFlow, false
			continue
		}
		merged = mergeFlow(merged, armFlow)
	}

	if !firstArm {
		c.flow = merged
	}

	c.checkExhaustive(e, subject, covered, irrefutable)

	if result == nil {
		return types.Void
	}
	return result
}

// matchArm checks one arm against the subject, returning what its pattern
// covers and the match's result type with this arm folded in.
func (c *checker) matchArm(arm ast.MatchArm, subject, want, result types.Type) (coverage, types.Type) {
	armCoverage := c.pattern(arm.Pattern, subject)

	exprStmt, ok := arm.Body.(*ast.ExprStmt)
	if !ok {
		c.stmt(arm.Body)
		return armCoverage, result
	}

	armType := types.Default(c.exprWith(exprStmt.X, want))
	if types.Identical(armType, types.Void) || flexible(armType) {
		return armCoverage, result
	}
	if result == nil {
		return armCoverage, armType
	}
	if !flexible(result) && !types.Identical(result, armType) && !c.assignableType(armType, result) {
		c.errorf(arm.Offset, "the match arms disagree: %s here, %s before", armType, result)
	}
	return armCoverage, result
}

// checkExhaustive demands that a match over an enum name every variant or
// end in a `_`. Adding a variant then breaks every match that enumerated
// the old set, which is the point of the check.
func (c *checker) checkExhaustive(e *ast.MatchExpr, subject types.Type, covered map[string]bool, irrefutable bool) {
	if irrefutable {
		return
	}
	enum, ok := subject.(*types.Enum)
	if !ok {
		return
	}

	var missing []string
	for _, variant := range enum.Variants {
		if !covered[variant.Name] {
			missing = append(missing, enum.Name+"::"+variant.Name)
		}
	}
	if len(missing) == 0 {
		return
	}

	c.errorHintf(e.Offset, missingVariantHelp(missing), "this match leaves %s uncovered", strings.Join(missing, ", "))
}

// missingVariantHelp offers the two ways out, with the arms spelled so the
// first is a paste.
func missingVariantHelp(missing []string) []string {
	arms := make([]string, len(missing))
	for i, name := range missing {
		arms[i] = name + ": ..."
	}
	return []string{
		"add the missing arm(s): " + strings.Join(arms, ", "),
		"or add `_: ...` to accept everything else, which stops a new variant from breaking this match",
	}
}

// ---------------------------------------------------------------------------
// Closures
// ---------------------------------------------------------------------------

func (c *checker) closureExpr(e *ast.ClosureExpr, want types.Type) types.Type {
	wantFunc, _ := want.(*types.Func)
	if wantFunc == nil {
		if optional, ok := want.(*types.Optional); ok {
			wantFunc, _ = optional.Elem.(*types.Func)
		}
	}

	// A move capture takes an existing binding with it, so the name must
	// already resolve outside the closure. Moving is also what carries an
	// allocation into the closure, which is the case the leak analysis
	// cannot see any other way.
	for _, capture := range e.Captures {
		captured := c.scope.lookup(capture.Name)
		if captured == nil {
			c.errorf(capture.Offset, "cannot move %s: no such name in scope", capture.Name)
			continue
		}
		c.settleAllocation(captured)
	}

	sig := &types.Func{Async: e.Async, Required: len(e.Params)}

	savedFn, savedFacts, savedFloor := c.fn, c.facts, c.allocFloor
	defer func() { c.fn, c.facts, c.allocFloor = savedFn, savedFacts, savedFloor }()

	// A return inside the closure leaves the closure, not the function this
	// is written in.
	c.allocFloor = len(c.allocScopes)

	// Captures copy, so a narrowed value stays narrowed inside the body.
	inherited := map[*symbol]bool{}
	for sym := range c.facts {
		inherited[sym] = true
	}
	c.facts = inherited

	c.closureCaptures[e] = c.pushCaptures(func() {
		c.pushScope(func() {
			for i, param := range e.Params {
				var paramType types.Type
				switch {
				case param.Type != nil:
					paramType = c.resolveType(param.Type)
				case wantFunc != nil && i < len(wantFunc.Params):
					paramType = wantFunc.Params[i]
				default:
					c.errorf(param.Offset, "the parameter %s needs a type; nothing here says what the closure is", param.Name)
					paramType = types.Unknown
				}
				sig.Params = append(sig.Params, paramType)
				if param.Default != nil {
					c.assignExpr(param.Default, paramType, "the default value")
				}
				c.declareValue(param.Name, param.Offset, e, paramType)
			}

			result := c.closureResult(e, wantFunc)

			inMethod := savedFn != nil && savedFn.inMethod
			var selfType types.Type
			if savedFn != nil {
				selfType = savedFn.selfType
			}
			c.fn = &funcContext{result: result, async: e.Async, inMethod: inMethod, selfType: selfType}

			if e.Body != nil {
				// A closure runs at a time this analysis cannot pin down, so its
				// writes do not count outside it. Reads inside still check
				// against the state here, which is what makes capturing a local
				// that holds nothing an error.
				c.isolateFlow(func() { c.blockInPlace(e.Body) })
				sig.Result = result
			}
			if e.BodyExpr != nil {
				bodyType := types.Default(c.exprWith(e.BodyExpr, result))
				if result == nil {
					sig.Result = bodyType
				} else {
					c.assignValue(e.BodyExpr.Pos(), e.BodyExpr, bodyType, result, "the closure body")
					sig.Result = result
				}
			}
			if sig.Result == nil {
				sig.Result = types.Void
			}
		})
	})

	if wantFunc != nil && len(e.Params) != len(wantFunc.Params) {
		c.errorf(e.Offset, "this closure takes %d parameter(s), the context wants %s", len(e.Params), wantFunc)
	}
	return sig
}

// closureResult picks the closure's declared result, or the context's, or
// leaves inference to the body.
func (c *checker) closureResult(e *ast.ClosureExpr, wantFunc *types.Func) types.Type {
	if len(e.Results) > 0 {
		return c.resultType(e.Results)
	}
	if e.Body != nil {
		// A braced body with no annotation returns nothing; only an
		// expression body infers its result.
		if wantFunc != nil {
			return wantFunc.Result
		}
		return types.Void
	}
	if wantFunc != nil {
		return wantFunc.Result
	}
	return nil
}
