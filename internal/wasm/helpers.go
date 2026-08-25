package wasm

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// clz, ctz and popcnt have no METALS instruction, and should not: they
// are a hardware convenience, not a semantic primitive, and a producer
// that needs one can write the loop. So the loop is written here, once
// per width and only when a body reaches for it.
//
// A runtime import would have been the other option and is worse: it
// puts three functions into rt.c, two backend plan tables and two Go
// tiers, for something ordinary METALS expresses. Synthesizing in the
// producer is the same choice `lower` makes for derived printing.

const (
	helperClz    = 0
	helperCtz    = 1
	helperPopcnt = 2
)

var helperNames = [3]string{"clz", "ctz", "popcnt"}

// bitHelper returns the name of the counting function for a width,
// generating it on first use.
func (t *translator) bitHelper(which byte, v valType) string {
	key := helperNames[which] + strconv.Itoa(widthOf(v))
	if name, ok := t.helpers[key]; ok {
		return name
	}
	name := t.unique("wasm." + key)
	t.helpers[key] = name

	work := unsignedOf(v)
	width := widthOf(v)
	fn := &metals.Func{
		Name:   name,
		Params: []metals.Param{{Name: "x", Type: work}},
		Result: work,
	}
	switch which {
	case helperClz:
		fn.Body = countingLoop(work, width, true)
	case helperCtz:
		fn.Body = countingLoop(work, width, false)
	default:
		fn.Body = popcountLoop(work)
	}
	t.out.Funcs = append(t.out.Funcs, fn)
	return name
}

// countingLoop is clz and ctz: walk from one end until a set bit turns
// up, and answer the full width when none does.
//
// The zero case is handled before the loop rather than inside it,
// because the loop's test is "the bit at this end is clear", which is
// true forever for zero.
func countingLoop(work *metals.Prim, width int, leading bool) []metals.Instr {
	// The probe is the bit examined each pass: the top bit for clz, the
	// bottom one for ctz, with the value shifted the other way.
	probe := "1"
	shift := "shr"
	if leading {
		probe = strconv.FormatUint(uint64(1)<<(uint(width)-1), 10)
		shift = "shl"
	}

	body := []metals.Instr{
		&metals.Const{Dst: "zero", Type: work, Value: "0"},
		&metals.Const{Dst: "one", Type: work, Value: "1"},
		&metals.Const{Dst: "probe", Type: work, Value: probe},
		&metals.Const{Dst: "n", Type: work, Value: "0"},
		&metals.Bin{Dst: "empty", Op: "eq", A: "x", B: "zero"},
		&metals.If{
			Cond: "empty",
			Then: []metals.Instr{
				&metals.Const{Dst: "all", Type: work, Value: strconv.Itoa(width)},
				&metals.Return{Val: "all"},
			},
		},
		&metals.Block{
			Label: "done",
			Body: []metals.Instr{
				&metals.Loop{
					Label: "scan",
					Body: []metals.Instr{
						&metals.Bin{Dst: "bit", Op: "band", A: "x", B: "probe"},
						&metals.Bin{Dst: "found", Op: "ne", A: "bit", B: "zero"},
						&metals.BrIf{Cond: "found", Label: "done"},
						&metals.Bin{Dst: "x", Op: shift, A: "x", B: "one"},
						&metals.Bin{Dst: "n", Op: "wadd", A: "n", B: "one"},
						&metals.Br{Label: "scan"},
					},
				},
			},
		},
		&metals.Return{Val: "n"},
	}
	return body
}

// ---------------------------------------------------------------------------
// Saturating float-to-integer conversion
// ---------------------------------------------------------------------------

// The trunc_sat family clamps where METALS' conv aborts, and clang emits
// it by default because C's out-of-range float-to-integer conversion is
// undefined and saturating is the cheaper definition to give it.
//
// Neither behaviour is wrong and neither should be the other. conv stays
// the checked conversion Gyro's `as` needs; the clamping is written out
// here, once per shape, as the four comparisons it actually is. Every
// bound below is an exact power of two, so each test means the same
// thing in f32 as in f64 — a boundary that rounded would let a value one
// ulp outside the range through to a conv that aborts.
type satShape struct {
	from     valType
	to       valType
	unsigned bool
}

// satBounds is the low and high guard for a target width, as literals.
// The high guard is the first value that does not fit, so the test is
// `>=`; the low guard for a signed target is the minimum itself, so the
// test is `<`.
func satBounds(to valType, unsigned bool) (low, high, min, max string) {
	if to == valI64 {
		if unsigned {
			return "1.0", "18446744073709551616.0", "0", "18446744073709551615"
		}
		return "-9223372036854775808.0", "9223372036854775808.0",
			"-9223372036854775808", "9223372036854775807"
	}
	if unsigned {
		return "1.0", "4294967296.0", "0", "4294967295"
	}
	return "-2147483648.0", "2147483648.0", "-2147483648", "2147483647"
}

// satHelper returns the clamping conversion for a shape, generating it
// on first use. The result type is the target's unsigned type for a _u
// conversion, which the caller reads back onto the stack with a bitcast.
func (t *translator) satHelper(shape satShape) (string, metals.Type) {
	result := shape.to.metalsType()
	suffix := "s"
	if shape.unsigned {
		result = unsignedOf(shape.to)
		suffix = "u"
	}
	key := "trunc_sat." + shape.to.String() + "." + shape.from.String() + "." + suffix
	if name, ok := t.helpers[key]; ok {
		return name, result
	}
	name := t.unique("wasm." + key)
	t.helpers[key] = name

	source := shape.from.metalsType()
	low, high, min, max := satBounds(shape.to, shape.unsigned)

	body := []metals.Instr{
		&metals.Const{Dst: "zero", Type: result, Value: "0"},
		// NaN compares unequal to itself, which is the only test that
		// distinguishes it and the reason the operators are IEEE.
		&metals.Bin{Dst: "nan", Op: "ne", A: "v", B: "v"},
		&metals.If{Cond: "nan", Then: []metals.Instr{&metals.Return{Val: "zero"}}},

		&metals.Const{Dst: "low", Type: source, Value: low},
		&metals.Bin{Dst: "under", Op: "lt", A: "v", B: "low"},
		&metals.If{Cond: "under", Then: []metals.Instr{
			&metals.Const{Dst: "min", Type: result, Value: min},
			&metals.Return{Val: "min"},
		}},

		&metals.Const{Dst: "high", Type: source, Value: high},
		&metals.Bin{Dst: "over", Op: "ge", A: "v", B: "high"},
		&metals.If{Cond: "over", Then: []metals.Instr{
			&metals.Const{Dst: "max", Type: result, Value: max},
			&metals.Return{Val: "max"},
		}},

		&metals.Conv{Dst: "r", X: "v", To: result},
		&metals.Return{Val: "r"},
	}

	t.out.Funcs = append(t.out.Funcs, &metals.Func{
		Name:   name,
		Params: []metals.Param{{Name: "v", Type: source}},
		Result: result,
		Body:   body,
	})
	return name, result
}

// popcountLoop clears the lowest set bit each pass, so it runs once per
// set bit rather than once per bit of the width.
func popcountLoop(work *metals.Prim) []metals.Instr {
	return []metals.Instr{
		&metals.Const{Dst: "zero", Type: work, Value: "0"},
		&metals.Const{Dst: "one", Type: work, Value: "1"},
		&metals.Const{Dst: "n", Type: work, Value: "0"},
		&metals.Block{
			Label: "done",
			Body: []metals.Instr{
				&metals.Loop{
					Label: "scan",
					Body: []metals.Instr{
						&metals.Bin{Dst: "empty", Op: "eq", A: "x", B: "zero"},
						&metals.BrIf{Cond: "empty", Label: "done"},
						&metals.Bin{Dst: "lower", Op: "wsub", A: "x", B: "one"},
						&metals.Bin{Dst: "x", Op: "band", A: "x", B: "lower"},
						&metals.Bin{Dst: "n", Op: "wadd", A: "n", B: "one"},
						&metals.Br{Label: "scan"},
					},
				},
			},
		},
		&metals.Return{Val: "n"},
	}
}
