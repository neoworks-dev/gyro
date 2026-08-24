package cgen

import (
	"fmt"
	"math"

	"github.com/gyro-lang/gyro/internal/metals"
)

func floatBits(v float64) uint64 { return math.Float64bits(v) }

// ---------------------------------------------------------------------------
// Binary operators
// ---------------------------------------------------------------------------

var floatOps = map[string]string{"fadd": "+", "fsub": "-", "fmul": "*", "fdiv": "/"}
var bitOps = map[string]string{"band": "&", "bor": "|", "bxor": "^"}
var orderOps = map[string]string{"lt": "<", "le": "<=", "gt": ">", "ge": ">="}

func (f *frame) emitBin(i *metals.Bin) {
	operand := f.typeOf(i.A)

	switch i.Op {
	case "eq", "ne":
		f.emitEquality(i.Dst, i.Op, i.A, i.B, operand)
		return
	case "lt", "le", "gt", "ge":
		// Signedness rides on the register's C type, and a NaN operand
		// makes every comparison false the way the evaluator has it.
		f.assign(i.Dst, fmt.Sprintf("(%s %s %s)", f.reg(i.A), orderOps[i.Op], f.reg(i.B)))
		return
	}
	if operator, ok := floatOps[i.Op]; ok {
		f.assign(i.Dst, fmt.Sprintf("%s %s %s", f.reg(i.A), operator, f.reg(i.B)))
		return
	}
	if operator, ok := bitOps[i.Op]; ok {
		f.assign(i.Dst, fmt.Sprintf("%s %s %s", f.reg(i.A), operator, f.reg(i.B)))
		return
	}

	kind, ok := f.intKindOf(operand)
	if !ok {
		f.g.fail("@%s: %s of a %s", f.fn.Name, i.Op, operand)
	}
	helper, ok := arithHelpers[i.Op]
	if !ok {
		f.g.fail("@%s: unknown operator %s", f.fn.Name, i.Op)
	}
	f.assign(i.Dst, fmt.Sprintf("%s(%s, %s, %d)", helper+suffixOf(kind), f.reg(i.A), f.reg(i.B), kind.width))
}

var arithHelpers = map[string]string{
	"iadd": "gy_iadd", "isub": "gy_isub", "imul": "gy_imul",
	"idiv": "gy_idiv", "irem": "gy_irem",
	"wadd": "gy_wadd", "wsub": "gy_wsub", "wmul": "gy_wmul",
	"shl": "gy_shl", "shr": "gy_shr",
}

func suffixOf(kind intKindInfo) string {
	if kind.signed {
		return "_s"
	}
	return "_u"
}

func (f *frame) intKindOf(t metals.Type) (intKindInfo, bool) {
	prim, ok := t.(*metals.Prim)
	if !ok {
		return intKindInfo{}, false
	}
	kind, ok := intKinds[prim.Kind]
	return kind, ok
}

// ---------------------------------------------------------------------------
// Unary operators
// ---------------------------------------------------------------------------

func (f *frame) emitUn(i *metals.Un) {
	g := f.g
	switch i.Op {
	case "not":
		f.assign(i.Dst, f.reg(i.X)+" ^ 1")

	case "neg":
		prim, ok := f.typeOf(i.X).(*metals.Prim)
		if !ok {
			g.fail("@%s: neg of a %s", f.fn.Name, f.typeOf(i.X))
		}
		if prim.Kind == metals.F32 || prim.Kind == metals.F64 {
			f.assign(i.Dst, "-"+f.reg(i.X))
			return
		}
		kind := intKinds[prim.Kind]
		f.assign(i.Dst, fmt.Sprintf("gy_isub%s(0, %s, %d)", suffixOf(kind), f.reg(i.X), kind.width))

	case "bnot":
		kind, ok := f.intKindOf(f.typeOf(i.X))
		if !ok {
			g.fail("@%s: bnot of a %s", f.fn.Name, f.typeOf(i.X))
		}
		if kind.signed {
			f.assign(i.Dst, "~"+f.reg(i.X))
			return
		}
		f.assign(i.Dst, fmt.Sprintf("gy_wrap_u(~%s, %d)", f.reg(i.X), kind.width))

	default:
		g.fail("@%s: unknown unary operator %s", f.fn.Name, i.Op)
	}
}

// ---------------------------------------------------------------------------
// Conversions
// ---------------------------------------------------------------------------

func (f *frame) emitConv(i *metals.Conv) {
	g := f.g
	source, ok := f.typeOf(i.X).(*metals.Prim)
	if !ok {
		g.fail("@%s: conv from a %s", f.fn.Name, f.typeOf(i.X))
	}
	target, ok := i.To.(*metals.Prim)
	if !ok {
		g.fail("@%s: conv to a %s", f.fn.Name, i.To)
	}

	switch {
	case isFloat(target):
		f.assign(i.Dst, f.convToFloat(source, target, f.reg(i.X)))
	case isFloat(source):
		f.assign(i.Dst, f.convFromFloat(target, f.reg(i.X)))
	default:
		from, to := intKinds[source.Kind], intKinds[target.Kind]
		f.assign(i.Dst, convIntToInt(from, to, f.reg(i.X)))
	}
}

func isFloat(prim *metals.Prim) bool {
	return prim.Kind == metals.F32 || prim.Kind == metals.F64
}

func (f *frame) convToFloat(source, target *metals.Prim, x string) string {
	var value string
	switch {
	case isFloat(source):
		value = x
	case intKinds[source.Kind].signed:
		value = "(double)" + x
	default:
		// u64 past 2^63 needs the runtime's conversion, not C's, to land
		// on the same double the evaluator picks.
		value = "gy_uint_to_double(" + x + ")"
	}
	if target.Kind == metals.F32 {
		return "gy_narrow_f32(" + value + ")"
	}
	return value
}

// convFromFloat truncates toward zero and aborts when the value does not
// fit, in the runtime rather than inline: the u64 and range cases are
// where C's own conversions go undefined.
func (f *frame) convFromFloat(target *metals.Prim, x string) string {
	kind := intKinds[target.Kind]
	if kind.signed {
		bound := int64(1) << (kind.width - 1)
		return fmt.Sprintf("gy_double_to_int(%s, %s, %s)", x, signedLiteral(-bound), signedLiteral(bound-1))
	}
	return fmt.Sprintf("gy_double_to_uint(%s, UINT64_C(%d))", x, uintMaxOf(kind.width))
}

func convIntToInt(from, to intKindInfo, x string) string {
	switch {
	case from.signed && to.signed:
		return fmt.Sprintf("gy_conv_s2s(%s, %d, %d)", x, from.width, to.width)
	case from.signed && !to.signed:
		return fmt.Sprintf("gy_conv_s2u(%s, %d)", x, to.width)
	case !from.signed && to.signed:
		return fmt.Sprintf("gy_conv_u2s(%s, %d)", x, to.width)
	default:
		return fmt.Sprintf("gy_conv_u2u(%s, %d, %d)", x, from.width, to.width)
	}
}

func uintMaxOf(width int) uint64 {
	if width >= 64 {
		return math.MaxUint64
	}
	return (uint64(1) << width) - 1
}

// ---------------------------------------------------------------------------
// Equality
// ---------------------------------------------------------------------------

func (f *frame) emitEquality(dst, op, a, b string, operand metals.Type) {
	g := f.g
	negate := op == "ne"

	switch t := operand.(type) {
	case *metals.Prim:
		if t.Kind == metals.Str {
			f.assign(dst, fmt.Sprintf("(gy_str_eq(%s, %s) %s 0)", f.reg(a), f.reg(b), zeroTest(negate)))
			return
		}
		// Floats included: C's == is already NaN-aware the same way.
		f.assign(dst, fmt.Sprintf("(%s %s %s)", f.reg(a), comparison(negate), f.reg(b)))

	case *metals.Ptr, *metals.ChanOf, *metals.FutureOf, *metals.Handle:
		f.assign(dst, fmt.Sprintf("(%s %s %s)", f.reg(a), comparison(negate), f.reg(b)))

	case *metals.SliceOf, *metals.FnType:
		// The evaluator holds these to be never equal.
		if negate {
			f.assign(dst, "1")
		} else {
			f.assign(dst, "0")
		}

	default:
		helper := g.eqHelperFor(operand)
		call := fmt.Sprintf("%s(&%s, &%s)", helper, f.reg(a), f.reg(b))
		if negate {
			call = "!" + call
		}
		f.assign(dst, call)
	}
}

func comparison(negate bool) string {
	if negate {
		return "!="
	}
	return "=="
}

func zeroTest(negate bool) string {
	if negate {
		return "=="
	}
	return "!="
}
