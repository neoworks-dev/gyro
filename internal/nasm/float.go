package nasm

import (
	"github.com/gyro-lang/gyro/internal/metals"
)

// The float intrinsics, by the C function each width calls. These are
// the same names cgen emits: sqrt, fabs, ceil, floor, trunc and
// copysign mean in C what they mean in METALS, and the three that do
// not — min, max and nearest — are defined once in rt.c so the two
// backends cannot drift apart on a NaN or a signed zero.
var floatCalls = map[string][2]string{
	"fabs":      {"fabsf", "fabs"},
	"fsqrt":     {"sqrtf", "sqrt"},
	"fceil":     {"ceilf", "ceil"},
	"ffloor":    {"floorf", "floor"},
	"ftrunc":    {"truncf", "trunc"},
	"fnearest":  {"gy_f32_nearest", "gy_f64_nearest"},
	"fmin":      {"gy_f32_min", "gy_f64_min"},
	"fmax":      {"gy_f32_max", "gy_f64_max"},
	"fcopysign": {"copysignf", "copysign"},
}

func floatCall(op string, single bool) (string, bool) {
	entry, ok := floatCalls[op]
	if !ok {
		return "", false
	}
	if single {
		return entry[0], true
	}
	return entry[1], true
}

func isSingle(t metals.Type) bool {
	prim, ok := t.(*metals.Prim)
	return ok && prim.Kind == metals.F32
}

// narrowFloat rounds xmm0 through single precision. An f32 register
// holds a double, so every operation producing one has to round or the
// value keeps precision that single precision would have lost — and an
// f32 multiply that should overflow to +Inf stays finite instead.
//
// Rounding a double result of +, -, *, / or sqrt down to float gives
// the correctly-rounded float answer: double carries 53 bits where the
// two-step bound needs 2*24+2, so there is no double-rounding error to
// worry about at this width.
func (f *frame) narrowFloat(t metals.Type) {
	if isSingle(t) {
		f.g.ins("cvtsd2ss xmm0, xmm0")
		f.g.ins("cvtss2sd xmm0, xmm0")
	}
}

// emitFloatCall is the one- and two-operand intrinsics. The f-suffixed
// C functions take and return a float, so an f32 narrows on the way in
// and widens on the way back into its qword.
func (f *frame) emitFloatCall(dst, op, a, b string, single bool) bool {
	name, ok := floatCall(op, single)
	if !ok {
		return false
	}
	g := f.g
	g.ins("movsd xmm0, %s", f.slot(a))
	if single {
		g.ins("cvtsd2ss xmm0, xmm0")
	}
	if b != "" {
		g.ins("movsd xmm1, %s", f.slot(b))
		if single {
			g.ins("cvtsd2ss xmm1, xmm1")
		}
	}
	g.ins("call %s", g.extern(name))
	if single {
		g.ins("cvtss2sd xmm0, xmm0")
	}
	g.ins("movsd %s, xmm0", f.slot(dst))
	return true
}
