package meval

import (
	"math"

	"github.com/gyro-lang/gyro/internal/metals"
)

// An f32 register holds its value in a float64, because every scalar is
// one qword and there is no separate single-precision cell. That is a
// representation choice, not a precision one: an f32 result still has to
// be the value single precision would have produced, or `a * b` can
// overflow to +Inf in single precision and stay finite here, and the two
// answers diverge from there on. mkFloat is where that rounding happens,
// and every operation that produces a float goes through it.
func mkFloat(v float64, kind metals.PrimKind) floatVal {
	if kind == metals.F32 {
		return floatVal{v: float64(float32(v)), kind: kind}
	}
	return floatVal{v: v, kind: kind}
}

// floatUnary is fabs, fsqrt and the four rounding intrinsics.
//
// The rounding four answer to IEEE-754's roundToIntegral: they produce a
// float, not an integer, and they leave a NaN, an infinity and a signed
// zero alone. fnearest is roundTiesToEven — C's `round` is ties-away and
// is the wrong function.
func (m *machine) floatUnary(op string, a floatVal) value {
	switch op {
	case "fabs":
		return mkFloat(math.Abs(a.v), a.kind)
	case "fsqrt":
		return mkFloat(math.Sqrt(a.v), a.kind)
	case "fceil":
		return mkFloat(math.Ceil(a.v), a.kind)
	case "ffloor":
		return mkFloat(math.Floor(a.v), a.kind)
	case "ftrunc":
		return mkFloat(math.Trunc(a.v), a.kind)
	case "fnearest":
		return mkFloat(math.RoundToEven(a.v), a.kind)
	}
	m.throw("unknown float operator %s", op)
	return nil
}

// fmin and fmax propagate NaN and distinguish the two zeros, which is
// what IEEE-754's minNum/maxNum do not do and what C's fmin/fmax do not
// do either — both of those answer the non-NaN operand. Go's math.Min
// and math.Max have the semantics wanted here.
func (m *machine) floatBinaryIntrinsic(op string, a, b floatVal) (value, bool) {
	switch op {
	case "fmin":
		return mkFloat(math.Min(a.v, b.v), a.kind), true
	case "fmax":
		return mkFloat(math.Max(a.v, b.v), a.kind), true
	case "fcopysign":
		return mkFloat(math.Copysign(a.v, b.v), a.kind), true
	}
	return nil, false
}
