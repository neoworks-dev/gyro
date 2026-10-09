package sema

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/stdlib"
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
	t = c.checkFormatPosition(e, t)
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
		return c.formatLiteral(e)

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
		c.checkKeep(e)
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
	if sym.kind == symValue && sym.closureDepth < c.closureDepth && c.isFormat(sym.typ) {
		c.errorf(e.Offset, "a closure cannot capture a format value, because it cannot outlive the statement it is written in")
		return types.Unknown
	}
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
	c.pushUntyped(e, basic)
	return basic
}

// pushUntyped drives a context type into the untyped operands of an
// untyped compound expression. Recording the expression alone is not
// enough: lowering asks for the type of every operand it emits, and one
// still carrying UntypedInt is emitted at the i32 default. That is how
// `a & (0xff >> b)` with a u32 `a` reached METALS as `shr i32, u32`, and
// how `var x: i64 = 1 << 40` came out an i32 shift in the interpreter.
//
// A shift takes the type of its left operand only — the count keeps its
// own, as `shift` decided.
func (c *checker) pushUntyped(e ast.Expr, target *types.Basic) {
	// A folded expression emits its value and never its operands, and
	// only the folded value has to fit: `var x: i8 = -128` is legal even
	// though 128 alone is not an i8.
	if _, folded := c.consts[e]; folded {
		return
	}
	switch e := e.(type) {
	case *ast.BinaryExpr:
		c.adaptUntyped(e.X, c.recordedType(e.X), target)
		if e.Op != token.Shl && e.Op != token.Shr {
			c.adaptUntyped(e.Y, c.recordedType(e.Y), target)
		}
	case *ast.UnaryExpr:
		switch e.Op {
		case token.Minus, token.Tilde:
			c.adaptUntyped(e.X, c.recordedType(e.X), target)
		}
	}
}

// defaultExpr settles an untyped literal that no context type decided:
// integers become i32 and floats f64. Defaulting is a type decision like
// any other, so the value still has to fit — without this a declaration
// binds a name whose type cannot hold its own initializer, and every tier
// agrees on a number the type says is impossible.
func (c *checker) defaultExpr(e ast.Expr, t types.Type) types.Type {
	if e == nil || (!isUntypedInt(t) && !isUntypedFloat(t)) {
		return types.Default(t)
	}
	return c.adaptUntyped(e, t, types.Default(t))
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
	if c.isFormat(value) && types.Identical(target, types.String) {
		c.errorHintf(offset, []string{
			"a Format is not text; build the string into an allocator: format(heap(), ...) from mem",
		}, "cannot use %s as %s in %s", describeType(value), target, context)
		return
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
		// The address of a declared function is a code pointer: one
		// word, no environment, which is what a C callback is. A `fn`
		// value is two words because it may have captured something,
		// and `&f` on a local holding one is still a pointer to that
		// local.
		if decl := c.namedFunc(e.X); decl != nil {
			return c.codePointer(e.Offset, decl, operand)
		}
		if index, ok := e.X.(*ast.IndexExpr); ok {
			if _, narrowing := index.Index.(*ast.RangeExpr); narrowing {
				c.errorf(e.Offset, "a narrowing has no address; it is a view built here, not storage")
				return types.Unknown
			}
		}
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

// namedFunc is the declaration an expression names, when it names one
// directly. A local holding a function value is not one: it is a
// binding, and its address is a pointer to the binding.
func (c *checker) namedFunc(e ast.Expr) *ast.FuncDecl {
	switch e.(type) {
	case *ast.Ident, *ast.Path:
	default:
		return nil
	}
	decl, _ := c.program.info.Refs[e].(*ast.FuncDecl)
	return decl
}

// codePointer is the type of `&f`.
//
// A generic function and a method never reach here: neither is a value
// on its own, so typing the operand has already said so — the first
// needs its parameters inferred from a call and the second needs a
// receiver, and both messages are about the same missing thing.
func (c *checker) codePointer(offset int, decl *ast.FuncDecl, signature types.Type) types.Type {
	sig, ok := signature.(*types.Func)
	if !ok {
		return types.Unknown
	}
	switch {
	case sig.Async:
		c.errorf(offset, "%s is async, and what runs it is this program's scheduler rather than a caller",
			decl.Name)
		return types.Unknown
	case sig.Variadic:
		c.errorf(offset, "%s is variadic, and a code pointer carries no argument pack", decl.Name)
		return types.Unknown
	}
	return &types.Code{Params: sig.Params, Result: sig.Result}
}

// codeSignature is a code pointer read as a callable, which is how the
// call checker asks about arity and argument types.
func codeSignature(code *types.Code) *types.Func {
	return &types.Func{
		Params:   code.Params,
		Required: len(code.Params),
		Result:   code.Result,
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
		// A union has no equality to derive: nothing records which member
		// the storage means, so there is no question for `==` to answer.
		// A struct holding one inherits the refusal.
		return !isFunc(left) && !isChanType(left) && !holdsUnion(left, map[*types.Struct]bool{})
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
		t = c.defaultExpr(element, t)
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
		t := c.exprWith(element, elemWant)
		if elemWant != nil {
			// Adapt rather than default: `-1` is a literal behind a
			// negation, and only adapting carries the element's type
			// through it. Defaulting here typed `(-1, 0)` as `(i32,
			// i64)` where `(i64, i64)` was asked for.
			c.assignValue(element.Pos(), element, t, elemWant, "the tuple literal")
			elems[i] = elemWant
			continue
		}
		elems[i] = c.defaultExpr(element, t)
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
	if narrowing, ok := e.Index.(*ast.RangeExpr); ok {
		return c.sliceNarrowing(e, narrowing)
	}

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
	case *types.Struct:
		// `xs[i]` on an Array<T> is `xs.at(i)` read or written through,
		// so an element is a place: assigning to one assigns into the
		// array, where `get` hands back a copy of it.
		if elem := arrayElem(base); elem != nil {
			return elem
		}
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

// arrayElem is the element type of an `Array<T>`, or nil for anything
// else. The Array is the runtime's own handle, so it is recognized by
// being that declaration rather than by shape.
func arrayElem(t *types.Struct) types.Type {
	origin := t
	if t.Origin != nil {
		origin = t.Origin
	}
	if origin.Name != "Array" || !stdlib.IsBundled(origin.Module) || len(t.Args) != 1 {
		return nil
	}
	return t.Args[0]
}

// sliceNarrowing types `xs[lo..hi]`. The result is always a slice, even
// of a fixed-size array, because a window onto storage is a borrow and
// `T[]` is the type that says so — the length is no longer static, which
// is the whole point.
//
// It is not a copy and it does not allocate: the same elements, seen
// through a shorter view. That is why `string` is left out. A `string`
// is UTF-8 and its length is in bytes, so a byte range could cut a code
// point in half and hand back a `string` that is not text; narrowing
// text needs to say which unit it counts in, and that is a separate
// decision.
func (c *checker) sliceNarrowing(e *ast.IndexExpr, r *ast.RangeExpr) types.Type {
	base := c.expr(e.X)

	for _, bound := range []ast.Expr{r.Lo, r.Hi} {
		if bound == nil {
			// The container's own bound: zero, or its length.
			continue
		}
		got := c.expr(bound)
		if flexible(got) {
			continue
		}
		if isUntypedInt(got) {
			c.adaptUntyped(bound, got, types.USize)
			continue
		}
		if !types.Identical(got, types.USize) {
			c.errorf(bound.Pos(), "range bounds are usize, not %s; lengths and indexes never mix with signed arithmetic implicitly", got)
		}
	}

	switch base := base.(type) {
	case *types.Slice:
		return base
	case *types.Array:
		return &types.Slice{Elem: base.Elem}
	case *types.Struct:
		// An Array<T> narrows through the view it already hands out, so
		// `xs[a..b]` is `xs.slice()[a..b]` and borrows the same buffer.
		if elem := arrayElem(base); elem != nil {
			return &types.Slice{Elem: elem}
		}
	case *types.Optional:
		c.errorf(e.Offset, "this value is %s and may be null; check it against null before narrowing it", base)
		return types.Unknown
	case *types.Basic:
		if base.Kind == types.StringKind {
			c.errorHintf(e.Offset, []string{
				"a string is UTF-8 and its length is in bytes, so a byte range can cut a code point in half",
			}, "a string cannot be narrowed")
			return types.Unknown
		}
	}
	if flexible(base) {
		return types.Unknown
	}
	c.errorf(e.Offset, "%s cannot be narrowed; a slice, a fixed-size array and an Array<T> are what have a window to take", base)
	return types.Unknown
}

// selectorExpr types `x.name`: a struct field, or nothing here — method
// calls are handled by callExpr, so a bare method reference is an error.
func (c *checker) selectorExpr(e *ast.SelectorExpr) types.Type {
	c.formatOK[e.X] = true
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

	if union, ok := t.(*types.Union); ok {
		if member := union.MemberNamed(name); member != nil {
			if !member.Exported && union.Module != c.modulePath {
				c.errorf(offset, "%s.%s is private to its module", union.Name, member.Name)
			}
			return member.Type
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

	if union, ok := resolved.(*types.Union); ok {
		return c.unionLit(e, union)
	}

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

	// `as` is also how a value enters and leaves an any. Wrapping takes
	// anything with a value; unwrapping names the type it must hold and
	// panics at runtime when it holds another.
	if isAnyBasic(target) {
		payload := c.defaultExpr(e.X, value)
		if !anyPayload(payload) {
			c.errorf(e.Offset, "an any cannot hold %s", describeType(value))
		}
		c.record(e.X, payload)
		return target
	}
	if isAnyBasic(value) {
		if !anyPayload(target) {
			c.errorf(e.Offset, "an any cannot hold %s, so it cannot be unwrapped to one", target)
		}
		return target
	}

	// `as` is also how an address forgets its type and gets it back.
	// Forgetting is free; the other way is the program's claim about
	// what is at the address, which is the same claim C's cast makes and
	// the same one the evaluator checks at runtime.
	if isAnyPtr(target) && isAnyPtrCastable(value) {
		return target
	}
	if isAnyPtr(value) && isAnyPtrCastable(target) {
		return target
	}

	if !castable(value) || !castable(target) {
		c.errorf(e.Offset, "as converts between numeric types and rune; %s as %s does not",
			describeType(value), target)
		return target
	}
	return target
}

func isAnyPtr(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind == types.AnyPtrKind
}

// isAnyPtrCastable reports the other side of an anyptr cast: a pointer,
// or an anyptr again. Nothing else is an address — an integer is not one
// here, because a pointer that came from arithmetic is the thing this
// language does not have.
func isAnyPtrCastable(t types.Type) bool {
	if _, ok := t.(*types.Pointer); ok {
		return true
	}
	return isAnyPtr(t)
}

// anyPayload reports whether a type can be wrapped in an any. What it
// excludes is what cannot be asked for again on the way out. void and null
// carry no value; an any is one already; `error` and `T?` hide the very
// concrete type an any is meant to be sure of; a channel, a future, a
// function value and a runtime handle are references whose type the
// interpreter cannot recover from the value at hand.
//
// Everything else can, including the types no path can spell: a tuple, a
// slice, an array and a pointer are tested by walking their shape, and
// written in a match arm as `(i32, string)(pair)`.
func anyPayload(t types.Type) bool {
	switch t := t.(type) {
	case *types.Basic:
		switch t.Kind {
		case types.VoidKind, types.NullKind, types.AnyKind, types.ErrorKind:
			return false
		case types.AnyPtrKind:
			// An any promises the concrete type comes back out, and an
			// anyptr is an address that already gave one up.
			return false
		}
		return true
	case *types.Struct:
		// A Format borrows its statement's frame, and an any can be
		// carried anywhere.
		if t.Name == "Format" && t.Module == stdlib.Path("mem") {
			return false
		}
		return !isRuntimeHandle(t) && !holdsUnion(t, map[*types.Struct]bool{})
	case *types.Union:
		// An any promises the concrete value comes back out, and a union
		// does not record which member it holds — so what came out would
		// be storage rather than a value.
		return false
	case *types.Optional, *types.Chan, *types.Future, *types.Func, *types.Code, *types.TypeParam:
		return false
	case *types.Pointer:
		return anyPayload(t.Elem)
	case *types.Slice:
		return anyPayload(t.Elem)
	case *types.Array:
		return anyPayload(t.Elem)
	case *types.Tuple:
		for _, elem := range t.Elems {
			if !anyPayload(elem) {
				return false
			}
		}
		return true
	}
	return true
}

// isRuntimeHandle reports a bundled struct the runtime owns — Array,
// Mutex, File. It has no fields to carry, so it is a reference the runtime
// hands out, and putting one in an any would copy a handle rather than a
// value.
func isRuntimeHandle(t *types.Struct) bool {
	return stdlib.IsBundled(t.Module) && len(t.Fields) == 0
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

	armType := c.defaultExpr(exprStmt.X, c.exprWith(exprStmt.X, want))
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
				bodyType := c.defaultExpr(e.BodyExpr, c.exprWith(e.BodyExpr, result))
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
