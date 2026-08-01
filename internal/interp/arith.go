package interp

import (
	"math"
	"strconv"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// Literal parsing. The lexer already stripped digit separators.

func parseIntLiteral(text string) Value {
	value, err := strconv.ParseInt(text, 0, 64)
	if err == nil {
		return IntVal{V: value, Kind: types.UntypedInt}
	}
	// Too big for int64: it can still be a u64-range literal.
	unsigned, uerr := strconv.ParseUint(text, 0, 64)
	if uerr == nil {
		return UintVal{V: unsigned, Kind: types.U64}
	}
	return IntVal{Kind: types.UntypedInt}
}

func parseFloatLiteral(text string) Value {
	value, _ := strconv.ParseFloat(text, 64)
	return FloatVal{V: value, Kind: types.UntypedFloat}
}

// ---------------------------------------------------------------------------
// Kinds, widths, adaptation
// ---------------------------------------------------------------------------

var intWidth = map[types.BasicKind]uint{
	types.I8: 8, types.I16: 16, types.I32: 32, types.I64: 64,
	types.U8: 8, types.U16: 16, types.U32: 32, types.U64: 64, types.Usize: 64,
}

func isSignedKind(kind types.BasicKind) bool {
	switch kind {
	case types.I8, types.I16, types.I32, types.I64, types.UntypedInt, types.RuneKind:
		return true
	}
	return false
}

func defaultIntKind(kind types.BasicKind) types.BasicKind {
	if kind == types.UntypedInt {
		return types.I32
	}
	return kind
}

// defaultUntyped materializes literal defaults: untyped ints become i32,
// untyped floats f64, recursively through the shapes literals build.
func defaultUntyped(v Value) Value {
	switch v := v.(type) {
	case IntVal:
		if v.Kind == types.UntypedInt {
			return IntVal{V: v.V, Kind: types.I32}
		}
	case FloatVal:
		if v.Kind == types.UntypedFloat {
			return FloatVal{V: v.V, Kind: types.F64}
		}
	case TupleVal:
		out := make(TupleVal, len(v))
		for i, e := range v {
			out[i] = defaultUntyped(e)
		}
		return out
	case ArrayVal:
		for i, e := range v.Elems {
			v.Elems[i] = defaultUntyped(e)
		}
		return v
	}
	return v
}

// adoptKinds makes two numeric operands agree: an untyped side takes the
// typed side's kind. The checker guaranteed the pairing is legal.
func adoptKinds(a, b Value) (Value, Value) {
	return adoptKindOf(b, a), adoptKindOf(a, b)
}

// adoptKindOf gives value the kind of reference when value is untyped.
func adoptKindOf(reference, value Value) Value {
	switch value := value.(type) {
	case IntVal:
		if value.Kind != types.UntypedInt {
			return value
		}
		switch reference := reference.(type) {
		case IntVal:
			if reference.Kind != types.UntypedInt {
				return IntVal{V: value.V, Kind: reference.Kind}
			}
		case UintVal:
			return UintVal{V: uint64(value.V), Kind: reference.Kind}
		case FloatVal:
			return FloatVal{V: float64(value.V), Kind: reference.Kind}
		}
	case FloatVal:
		if value.Kind != types.UntypedFloat {
			return value
		}
		if reference, ok := reference.(FloatVal); ok && reference.Kind != types.UntypedFloat {
			return FloatVal{V: value.V, Kind: reference.Kind}
		}
	}
	return value
}

// coerceToAst adapts a value to a declared type: untyped literals take
// the declared width, recursively through optionals, arrays and tuples.
// Only basics matter — everything else was checked statically.
func (f *frame) coerceToAst(v Value, t ast.Type) Value {
	switch t := t.(type) {
	case *ast.NamedType:
		if !t.Path.IsPlainName() {
			return defaultUntyped(v)
		}
		basic, ok := types.Builtin[t.Path.Segments[0].Name]
		if !ok {
			return defaultUntyped(v)
		}
		return convertUntyped(v, basic.Kind)
	case *ast.OptionalType:
		if isNull(v) {
			return v
		}
		return f.coerceToAst(v, t.Elem)
	case *ast.ArrayType:
		switch v := v.(type) {
		case ArrayVal:
			for i, e := range v.Elems {
				v.Elems[i] = f.coerceToAst(e, t.Elem)
			}
		case SliceVal:
			for i, e := range v.Elems {
				v.Elems[i] = f.coerceToAst(e, t.Elem)
			}
		}
		return v
	case *ast.TupleType:
		tuple, ok := v.(TupleVal)
		if !ok {
			return v
		}
		out := make(TupleVal, len(tuple))
		for i, e := range tuple {
			if i < len(t.Elements) {
				out[i] = f.coerceToAst(e, t.Elements[i])
			} else {
				out[i] = e
			}
		}
		return out
	}
	return defaultUntyped(v)
}

// convertUntyped types an untyped literal value; typed values pass
// through untouched, because context never converts them.
func convertUntyped(v Value, kind types.BasicKind) Value {
	switch v := v.(type) {
	case IntVal:
		if v.Kind != types.UntypedInt {
			return v
		}
		switch {
		case kind == types.F32 || kind == types.F64:
			return FloatVal{V: float64(v.V), Kind: kind}
		case isSignedKind(kind):
			return IntVal{V: v.V, Kind: kind}
		default:
			return UintVal{V: uint64(v.V), Kind: kind}
		}
	case FloatVal:
		if v.Kind == types.UntypedFloat && (kind == types.F32 || kind == types.F64) {
			return FloatVal{V: v.V, Kind: kind}
		}
	}
	return v
}

func toIndex(v Value) int64 {
	switch v := v.(type) {
	case IntVal:
		return v.V
	case UintVal:
		return int64(v.V)
	}
	return -1
}

// ---------------------------------------------------------------------------
// Checked arithmetic
// ---------------------------------------------------------------------------

func (f *frame) binaryValues(op token.Kind, left, right Value, offset int) Value {
	switch op {
	case token.Eq:
		return BoolVal(valuesEqual(left, right))
	case token.NotEq:
		return BoolVal(!valuesEqual(left, right))
	}

	left, right = adoptKinds(left, right)

	switch op {
	case token.Lt, token.LtEq, token.Gt, token.GtEq:
		return f.ordered(op, left, right, offset)
	}

	switch l := left.(type) {
	case IntVal:
		r, ok := right.(IntVal)
		if !ok {
			f.in.panicf(f.module, offset, "mismatched operands")
		}
		return f.intOp(op, l, r, offset)
	case UintVal:
		r, ok := right.(UintVal)
		if !ok {
			f.in.panicf(f.module, offset, "mismatched operands")
		}
		return f.uintOp(op, l, r, offset)
	case FloatVal:
		r, ok := right.(FloatVal)
		if !ok {
			f.in.panicf(f.module, offset, "mismatched operands")
		}
		return f.floatOp(op, l, r, offset)
	}
	f.in.panicf(f.module, offset, "unsupported operands")
	return nil
}

func (f *frame) ordered(op token.Kind, left, right Value, offset int) Value {
	c := f.compareNumeric(left, right, offset)
	switch op {
	case token.Lt:
		return BoolVal(c < 0)
	case token.LtEq:
		return BoolVal(c <= 0)
	case token.Gt:
		return BoolVal(c > 0)
	}
	return BoolVal(c >= 0)
}

func (f *frame) compareNumeric(left, right Value, offset int) int {
	switch l := left.(type) {
	case IntVal:
		r := right.(IntVal)
		switch {
		case l.V < r.V:
			return -1
		case l.V > r.V:
			return 1
		}
		return 0
	case UintVal:
		r := right.(UintVal)
		switch {
		case l.V < r.V:
			return -1
		case l.V > r.V:
			return 1
		}
		return 0
	case FloatVal:
		r := right.(FloatVal)
		switch {
		case l.V < r.V:
			return -1
		case l.V > r.V:
			return 1
		}
		return 0
	}
	f.in.panicf(f.module, offset, "values do not order")
	return 0
}

// intOp does signed arithmetic with overflow panics; the wrapping
// operators mask back into the width instead.
func (f *frame) intOp(op token.Kind, l, r IntVal, offset int) Value {
	kind := defaultIntKind(l.Kind)
	width := intWidth[kind]
	if width == 0 {
		width = 64 // rune arithmetic behaves as i32-ish; be permissive
	}

	wrap := func(v int64) Value {
		if width < 64 {
			shift := 64 - width
			v = v << shift >> shift
		}
		return IntVal{V: v, Kind: kind}
	}
	checked := func(v int64, overflowed bool) Value {
		if overflowed || !intFitsWidth(v, width) {
			f.in.panicf(f.module, offset, "integer overflow: %s", op)
		}
		return IntVal{V: v, Kind: kind}
	}

	switch op {
	case token.Plus:
		sum := l.V + r.V
		return checked(sum, (l.V > 0 && r.V > 0 && sum < 0) || (l.V < 0 && r.V < 0 && sum >= 0))
	case token.Minus:
		diff := l.V - r.V
		return checked(diff, (l.V >= 0 && r.V < 0 && diff < 0) || (l.V < 0 && r.V > 0 && diff >= 0))
	case token.Star:
		if l.V != 0 {
			product := l.V * r.V
			return checked(product, product/l.V != r.V)
		}
		return checked(0, false)
	case token.Slash:
		if r.V == 0 {
			f.in.panicf(f.module, offset, "division by zero")
		}
		if l.V == math.MinInt64 && r.V == -1 {
			f.in.panicf(f.module, offset, "integer overflow: /")
		}
		return checked(l.V/r.V, false)
	case token.Percent:
		if r.V == 0 {
			f.in.panicf(f.module, offset, "division by zero")
		}
		return IntVal{V: l.V % r.V, Kind: kind}
	case token.PlusMod:
		return wrap(l.V + r.V)
	case token.MinusMod:
		return wrap(l.V - r.V)
	case token.StarMod:
		return wrap(l.V * r.V)
	case token.Amp:
		return IntVal{V: l.V & r.V, Kind: kind}
	case token.Pipe:
		return IntVal{V: l.V | r.V, Kind: kind}
	case token.Caret:
		return IntVal{V: l.V ^ r.V, Kind: kind}
	case token.Shl:
		return checkedShift(l.V<<uint(r.V), kind, f, offset)
	case token.Shr:
		return IntVal{V: l.V >> uint(r.V), Kind: kind}
	}
	f.in.panicf(f.module, offset, "unsupported integer operator %s", op)
	return nil
}

func checkedShift(v int64, kind types.BasicKind, f *frame, offset int) Value {
	if !intFitsWidth(v, intWidth[kind]) {
		f.in.panicf(f.module, offset, "integer overflow: <<")
	}
	return IntVal{V: v, Kind: kind}
}

func intFitsWidth(v int64, width uint) bool {
	if width >= 64 {
		return true
	}
	min := int64(-1) << (width - 1)
	max := int64(1)<<(width-1) - 1
	return v >= min && v <= max
}

func uintFitsWidth(v uint64, width uint) bool {
	if width >= 64 {
		return true
	}
	return v <= uint64(1)<<width-1
}

func (f *frame) uintOp(op token.Kind, l, r UintVal, offset int) Value {
	kind := l.Kind
	width := intWidth[kind]

	wrap := func(v uint64) Value {
		if width < 64 {
			v &= uint64(1)<<width - 1
		}
		return UintVal{V: v, Kind: kind}
	}
	checked := func(v uint64, overflowed bool) Value {
		if overflowed || !uintFitsWidth(v, width) {
			f.in.panicf(f.module, offset, "integer overflow: %s", op)
		}
		return UintVal{V: v, Kind: kind}
	}

	switch op {
	case token.Plus:
		sum := l.V + r.V
		return checked(sum, sum < l.V)
	case token.Minus:
		if r.V > l.V {
			f.in.panicf(f.module, offset, "integer overflow: -")
		}
		return checked(l.V-r.V, false)
	case token.Star:
		if l.V != 0 {
			product := l.V * r.V
			return checked(product, product/l.V != r.V)
		}
		return checked(0, false)
	case token.Slash:
		if r.V == 0 {
			f.in.panicf(f.module, offset, "division by zero")
		}
		return UintVal{V: l.V / r.V, Kind: kind}
	case token.Percent:
		if r.V == 0 {
			f.in.panicf(f.module, offset, "division by zero")
		}
		return UintVal{V: l.V % r.V, Kind: kind}
	case token.PlusMod:
		return wrap(l.V + r.V)
	case token.MinusMod:
		return wrap(l.V - r.V)
	case token.StarMod:
		return wrap(l.V * r.V)
	case token.Amp:
		return UintVal{V: l.V & r.V, Kind: kind}
	case token.Pipe:
		return UintVal{V: l.V | r.V, Kind: kind}
	case token.Caret:
		return UintVal{V: l.V ^ r.V, Kind: kind}
	case token.Shl:
		shifted := l.V << uint(r.V)
		return checked(shifted, false)
	case token.Shr:
		return UintVal{V: l.V >> uint(r.V), Kind: kind}
	}
	f.in.panicf(f.module, offset, "unsupported integer operator %s", op)
	return nil
}

func (f *frame) floatOp(op token.Kind, l, r FloatVal, offset int) Value {
	kind := l.Kind
	if kind == types.UntypedFloat {
		kind = types.F64
	}
	switch op {
	case token.Plus:
		return FloatVal{V: l.V + r.V, Kind: kind}
	case token.Minus:
		return FloatVal{V: l.V - r.V, Kind: kind}
	case token.Star:
		return FloatVal{V: l.V * r.V, Kind: kind}
	case token.Slash:
		if r.V == 0 {
			f.in.panicf(f.module, offset, "division by zero")
		}
		return FloatVal{V: l.V / r.V, Kind: kind}
	}
	f.in.panicf(f.module, offset, "unsupported float operator %s", op)
	return nil
}

func (f *frame) negate(v Value, offset int) Value {
	switch v := v.(type) {
	case IntVal:
		if v.V == math.MinInt64 {
			f.in.panicf(f.module, offset, "integer overflow: -")
		}
		out := IntVal{V: -v.V, Kind: v.Kind}
		width := intWidth[defaultIntKind(v.Kind)]
		if width > 0 && !intFitsWidth(out.V, width) {
			f.in.panicf(f.module, offset, "integer overflow: -")
		}
		return out
	case UintVal:
		if v.V != 0 {
			f.in.panicf(f.module, offset, "integer overflow: -")
		}
		return v
	case FloatVal:
		return FloatVal{V: -v.V, Kind: v.Kind}
	}
	f.in.panicf(f.module, offset, "cannot negate this value")
	return nil
}

func (f *frame) bitNot(v Value, offset int) Value {
	switch v := v.(type) {
	case IntVal:
		kind := defaultIntKind(v.Kind)
		out := ^v.V
		if width := intWidth[kind]; width < 64 {
			shift := 64 - width
			out = out << shift >> shift
		}
		return IntVal{V: out, Kind: kind}
	case UintVal:
		out := ^v.V
		if width := intWidth[v.Kind]; width < 64 {
			out &= uint64(1)<<width - 1
		}
		return UintVal{V: out, Kind: v.Kind}
	}
	f.in.panicf(f.module, offset, "cannot invert this value")
	return nil
}

// ---------------------------------------------------------------------------
// Conversions: `as`
// ---------------------------------------------------------------------------

// convert implements `x as T`: widening always succeeds, a narrowing whose
// value does not fit panics, and a float converting to an integer
// truncates toward zero first.
func (f *frame) convert(v Value, kind types.BasicKind, offset int) Value {
	if kind == types.F32 || kind == types.F64 {
		switch v := v.(type) {
		case IntVal:
			return FloatVal{V: float64(v.V), Kind: kind}
		case UintVal:
			return FloatVal{V: float64(v.V), Kind: kind}
		case FloatVal:
			return FloatVal{V: v.V, Kind: kind}
		}
	}

	var raw int64
	var unsignedRaw uint64
	fromUnsigned := false

	switch v := v.(type) {
	case IntVal:
		raw = v.V
	case UintVal:
		unsignedRaw = v.V
		fromUnsigned = true
	case FloatVal:
		truncated := math.Trunc(v.V)
		if truncated > math.MaxInt64 || truncated < math.MinInt64 || truncated != truncated {
			f.in.panicf(f.module, offset, "value %s does not fit %s", strconv.FormatFloat(v.V, 'g', -1, 64), kindName(kind))
		}
		raw = int64(truncated)
	default:
		f.in.panicf(f.module, offset, "cannot convert this value")
	}

	if isSignedKind(kind) || kind == types.RuneKind {
		if fromUnsigned {
			if unsignedRaw > math.MaxInt64 {
				f.in.panicf(f.module, offset, "value %d does not fit %s", unsignedRaw, kindName(kind))
			}
			raw = int64(unsignedRaw)
		}
		width := intWidth[kind]
		if kind == types.RuneKind {
			width = 32
		}
		if !intFitsWidth(raw, width) {
			f.in.panicf(f.module, offset, "value %d does not fit %s", raw, kindName(kind))
		}
		return IntVal{V: raw, Kind: kind}
	}

	if !fromUnsigned {
		if raw < 0 {
			f.in.panicf(f.module, offset, "value %d does not fit %s", raw, kindName(kind))
		}
		unsignedRaw = uint64(raw)
	}
	if !uintFitsWidth(unsignedRaw, intWidth[kind]) {
		f.in.panicf(f.module, offset, "value %d does not fit %s", unsignedRaw, kindName(kind))
	}
	return UintVal{V: unsignedRaw, Kind: kind}
}

func kindName(kind types.BasicKind) string {
	for name, basic := range types.Builtin {
		if basic.Kind == kind {
			return name
		}
	}
	return "?"
}
