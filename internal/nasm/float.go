package nasm

import (
	"github.com/gyro-lang/gyro/internal/metals"
)

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

// The float intrinsics are emitted in line. An f32 register holds a double
// whose value is a single, and every one of these but sqrt returns an
// operand's value or an integral value of it, which a single represents
// exactly, so they compute at double width and only sqrt rounds back.
//
// min, max and nearest are not C's: NaN propagates, the two zeros are
// ordered, and halfway cases go to the even one (appendix D).

// roundModes are the roundsd immediates: the mode, with the precision
// exception suppressed.
var roundModes = map[string]int{
	"fnearest": 0 | 8,
	"ffloor":   1 | 8,
	"fceil":    2 | 8,
	"ftrunc":   3 | 8,
}

// emitFloatOp lowers the one- and two-operand float intrinsics, and
// reports whether op is one.
func (f *frame) emitFloatOp(dst, op, a, b string, single bool) bool {
	g := f.g
	switch op {
	case "fabs":
		g.ins("mov rax, %s", f.slot(a))
		g.ins("btr rax, 63")
		g.ins("mov %s, rax", f.slot(dst))

	case "fcopysign":
		g.ins("mov rax, %s", f.slot(a))
		g.ins("btr rax, 63")
		g.ins("mov rcx, %s", f.slot(b))
		g.ins("shr rcx, 63")
		g.ins("shl rcx, 63")
		g.ins("or rax, rcx")
		g.ins("mov %s, rax", f.slot(dst))

	case "fsqrt":
		g.ins("movsd xmm0, %s", f.slot(a))
		g.ins("sqrtsd xmm0, xmm0")
		if single {
			g.ins("cvtsd2ss xmm0, xmm0")
			g.ins("cvtss2sd xmm0, xmm0")
		}
		g.ins("movsd %s, xmm0", f.slot(dst))

	case "fnearest", "ffloor", "fceil", "ftrunc":
		g.ins("movsd xmm0, %s", f.slot(a))
		g.ins("roundsd xmm0, xmm0, %d", roundModes[op])
		g.ins("movsd %s, xmm0", f.slot(dst))

	case "fmin", "fmax":
		f.emitMinMax(dst, op, a, b)

	default:
		return false
	}
	return true
}

// emitMinMax is min or max with NaN propagating and -0.0 below +0.0.
// Unordered operands add, which leaves a NaN; operands that compare equal
// are either one value or the two zeros, and OR keeps a minus sign where
// AND drops it; anything else is the machine's own comparison.
func (f *frame) emitMinMax(dst, op, a, b string) {
	g := f.g
	unordered, apart, done := g.newLabel(), g.newLabel(), g.newLabel()
	g.ins("movsd xmm0, %s", f.slot(a))
	g.ins("movsd xmm1, %s", f.slot(b))
	g.ins("ucomisd xmm0, xmm1")
	g.ins("jp %s", unordered)
	g.ins("jne %s", apart)
	if op == "fmin" {
		g.ins("orpd xmm0, xmm1")
	} else {
		g.ins("andpd xmm0, xmm1")
	}
	g.ins("jmp %s", done)
	g.label(apart)
	if op == "fmin" {
		g.ins("minsd xmm0, xmm1")
	} else {
		g.ins("maxsd xmm0, xmm1")
	}
	g.ins("jmp %s", done)
	g.label(unordered)
	g.ins("addsd xmm0, xmm1")
	g.label(done)
	g.ins("movsd %s, xmm0", f.slot(dst))
}
