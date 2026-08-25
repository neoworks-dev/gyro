package wasm

import (
	"fmt"
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// The numeric opcodes.
//
// The table below is the whole correspondence, and the shape of each
// entry is the interesting part rather than any individual row: a wasm
// operation names a width and a signedness, and a METALS operation gets
// its signedness from its operands' type. So every unsigned row is the
// signed one with a bitcast on each side, and nothing else differs.

// numOp is one arithmetic or comparison opcode.
type numOp struct {
	// op is the METALS mnemonic.
	op string
	// operand is the wasm type the instruction works on.
	operand valType
	// result is what it leaves on the stack; i32 for every comparison.
	result valType
	// unsigned reads the operands through their unsigned type.
	unsigned bool
	// shift masks the count to the operand width first, which is what
	// wasm means by a shift and Go does not.
	shift bool
	// compare produces a bool that is widened to an i32.
	compare bool
	// guardRem tests for the -1 divisor wasm defines and METALS traps on.
	guardRem bool
}

var numericOps = map[byte]numOp{
	// --- i32 comparisons ---
	0x46: {op: "eq", operand: valI32, result: valI32, compare: true},
	0x47: {op: "ne", operand: valI32, result: valI32, compare: true},
	0x48: {op: "lt", operand: valI32, result: valI32, compare: true},
	0x49: {op: "lt", operand: valI32, result: valI32, compare: true, unsigned: true},
	0x4A: {op: "gt", operand: valI32, result: valI32, compare: true},
	0x4B: {op: "gt", operand: valI32, result: valI32, compare: true, unsigned: true},
	0x4C: {op: "le", operand: valI32, result: valI32, compare: true},
	0x4D: {op: "le", operand: valI32, result: valI32, compare: true, unsigned: true},
	0x4E: {op: "ge", operand: valI32, result: valI32, compare: true},
	0x4F: {op: "ge", operand: valI32, result: valI32, compare: true, unsigned: true},

	// --- i64 comparisons ---
	0x51: {op: "eq", operand: valI64, result: valI32, compare: true},
	0x52: {op: "ne", operand: valI64, result: valI32, compare: true},
	0x53: {op: "lt", operand: valI64, result: valI32, compare: true},
	0x54: {op: "lt", operand: valI64, result: valI32, compare: true, unsigned: true},
	0x55: {op: "gt", operand: valI64, result: valI32, compare: true},
	0x56: {op: "gt", operand: valI64, result: valI32, compare: true, unsigned: true},
	0x57: {op: "le", operand: valI64, result: valI32, compare: true},
	0x58: {op: "le", operand: valI64, result: valI32, compare: true, unsigned: true},
	0x59: {op: "ge", operand: valI64, result: valI32, compare: true},
	0x5A: {op: "ge", operand: valI64, result: valI32, compare: true, unsigned: true},

	// --- float comparisons ---
	0x5B: {op: "eq", operand: valF32, result: valI32, compare: true},
	0x5C: {op: "ne", operand: valF32, result: valI32, compare: true},
	0x5D: {op: "lt", operand: valF32, result: valI32, compare: true},
	0x5E: {op: "gt", operand: valF32, result: valI32, compare: true},
	0x5F: {op: "le", operand: valF32, result: valI32, compare: true},
	0x60: {op: "ge", operand: valF32, result: valI32, compare: true},
	0x61: {op: "eq", operand: valF64, result: valI32, compare: true},
	0x62: {op: "ne", operand: valF64, result: valI32, compare: true},
	0x63: {op: "lt", operand: valF64, result: valI32, compare: true},
	0x64: {op: "gt", operand: valF64, result: valI32, compare: true},
	0x65: {op: "le", operand: valF64, result: valI32, compare: true},
	0x66: {op: "ge", operand: valF64, result: valI32, compare: true},

	// --- i32 arithmetic. wasm wraps, so the wrapping mnemonics ---
	0x6A: {op: "wadd", operand: valI32, result: valI32},
	0x6B: {op: "wsub", operand: valI32, result: valI32},
	0x6C: {op: "wmul", operand: valI32, result: valI32},
	0x6D: {op: "idiv", operand: valI32, result: valI32},
	0x6E: {op: "idiv", operand: valI32, result: valI32, unsigned: true},
	0x6F: {op: "irem", operand: valI32, result: valI32, guardRem: true},
	0x70: {op: "irem", operand: valI32, result: valI32, unsigned: true},
	0x71: {op: "band", operand: valI32, result: valI32},
	0x72: {op: "bor", operand: valI32, result: valI32},
	0x73: {op: "bxor", operand: valI32, result: valI32},
	0x74: {op: "shl", operand: valI32, result: valI32, shift: true},
	0x75: {op: "shr", operand: valI32, result: valI32, shift: true},
	0x76: {op: "shr", operand: valI32, result: valI32, shift: true, unsigned: true},

	// --- i64 arithmetic ---
	0x7C: {op: "wadd", operand: valI64, result: valI64},
	0x7D: {op: "wsub", operand: valI64, result: valI64},
	0x7E: {op: "wmul", operand: valI64, result: valI64},
	0x7F: {op: "idiv", operand: valI64, result: valI64},
	0x80: {op: "idiv", operand: valI64, result: valI64, unsigned: true},
	0x81: {op: "irem", operand: valI64, result: valI64, guardRem: true},
	0x82: {op: "irem", operand: valI64, result: valI64, unsigned: true},
	0x83: {op: "band", operand: valI64, result: valI64},
	0x84: {op: "bor", operand: valI64, result: valI64},
	0x85: {op: "bxor", operand: valI64, result: valI64},
	0x86: {op: "shl", operand: valI64, result: valI64, shift: true},
	0x87: {op: "shr", operand: valI64, result: valI64, shift: true},
	0x88: {op: "shr", operand: valI64, result: valI64, shift: true, unsigned: true},

	// --- float arithmetic ---
	0x92: {op: "fadd", operand: valF32, result: valF32},
	0x93: {op: "fsub", operand: valF32, result: valF32},
	0x94: {op: "fmul", operand: valF32, result: valF32},
	0x95: {op: "fdiv", operand: valF32, result: valF32},
	0xA0: {op: "fadd", operand: valF64, result: valF64},
	0xA1: {op: "fsub", operand: valF64, result: valF64},
	0xA2: {op: "fmul", operand: valF64, result: valF64},
	0xA3: {op: "fdiv", operand: valF64, result: valF64},

	0x96: {op: "fmin", operand: valF32, result: valF32},
	0x97: {op: "fmax", operand: valF32, result: valF32},
	0x98: {op: "fcopysign", operand: valF32, result: valF32},
	0xA4: {op: "fmin", operand: valF64, result: valF64},
	0xA5: {op: "fmax", operand: valF64, result: valF64},
	0xA6: {op: "fcopysign", operand: valF64, result: valF64},
}

// floatUnaryOps are wasm's one-operand float opcodes. METALS spells them
// the same way, so each is a straight rename.
var floatUnaryOps = map[byte]struct {
	op      string
	operand valType
}{
	0x8B: {"fabs", valF32},
	0x8D: {"fceil", valF32},
	0x8E: {"ffloor", valF32},
	0x8F: {"ftrunc", valF32},
	0x90: {"fnearest", valF32},
	0x91: {"fsqrt", valF32},
	0x99: {"fabs", valF64},
	0x9B: {"fceil", valF64},
	0x9C: {"ffloor", valF64},
	0x9D: {"ftrunc", valF64},
	0x9E: {"fnearest", valF64},
	0x9F: {"fsqrt", valF64},
}

// conversion is one of the widening, narrowing and reinterpreting
// opcodes. Exactly one of the three fields is set.
type conversion struct {
	from valType
	to   valType
	// via is the METALS instruction: "conv" checks, "trunc" wraps,
	// "bitcast" reinterprets.
	via string
	// unsignedSource reads the operand through its unsigned type first,
	// which is what the _u conversions mean.
	unsignedSource bool
	// through is an intermediate narrow type, for the sign-extending
	// operations that keep a byte or a halfword and widen it back.
	through *metals.Prim
}

var conversions = map[byte]conversion{
	0xA7: {from: valI64, to: valI32, via: "trunc"},                      // i32.wrap_i64
	0xA8: {from: valF32, to: valI32, via: "conv"},                       // i32.trunc_f32_s
	0xA9: {from: valF32, to: valI32, via: "conv", unsignedSource: true}, // i32.trunc_f32_u
	0xAA: {from: valF64, to: valI32, via: "conv"},                       // i32.trunc_f64_s
	0xAB: {from: valF64, to: valI32, via: "conv", unsignedSource: true}, // i32.trunc_f64_u
	0xAC: {from: valI32, to: valI64, via: "conv"},                       // i64.extend_i32_s
	0xAD: {from: valI32, to: valI64, via: "conv", unsignedSource: true}, // i64.extend_i32_u
	0xAE: {from: valF32, to: valI64, via: "conv"},                       // i64.trunc_f32_s
	0xAF: {from: valF32, to: valI64, via: "conv", unsignedSource: true},
	0xB0: {from: valF64, to: valI64, via: "conv"},
	0xB1: {from: valF64, to: valI64, via: "conv", unsignedSource: true},
	0xB2: {from: valI32, to: valF32, via: "conv"}, // f32.convert_i32_s
	0xB3: {from: valI32, to: valF32, via: "conv", unsignedSource: true},
	0xB4: {from: valI64, to: valF32, via: "conv"},
	0xB5: {from: valI64, to: valF32, via: "conv", unsignedSource: true},
	0xB6: {from: valF64, to: valF32, via: "conv"}, // f32.demote_f64
	0xB7: {from: valI32, to: valF64, via: "conv"}, // f64.convert_i32_s
	0xB8: {from: valI32, to: valF64, via: "conv", unsignedSource: true},
	0xB9: {from: valI64, to: valF64, via: "conv"},
	0xBA: {from: valI64, to: valF64, via: "conv", unsignedSource: true},
	0xBB: {from: valF32, to: valF64, via: "conv"},    // f64.promote_f32
	0xBC: {from: valF32, to: valI32, via: "bitcast"}, // i32.reinterpret_f32
	0xBD: {from: valF64, to: valI64, via: "bitcast"},
	0xBE: {from: valI32, to: valF32, via: "bitcast"},
	0xBF: {from: valI64, to: valF64, via: "bitcast"},

	0xC0: {from: valI32, to: valI32, via: "extend", through: metals.TInt8},  // i32.extend8_s
	0xC1: {from: valI32, to: valI32, via: "extend", through: metals.TInt16}, // i32.extend16_s
	0xC2: {from: valI64, to: valI64, via: "extend", through: metals.TInt8},
	0xC3: {from: valI64, to: valI64, via: "extend", through: metals.TInt16},
	0xC4: {from: valI64, to: valI64, via: "extend", through: metals.TInt32},
}

// numeric dispatches everything that is neither control flow nor a
// memory access.
func (b *bodyTranslator) numeric(op byte) error {
	if entry, ok := numericOps[op]; ok {
		return b.applyNum(entry)
	}
	if entry, ok := conversions[op]; ok {
		return b.applyConv(entry)
	}

	switch op {
	case 0x45: // i32.eqz
		return b.eqz(valI32)
	case 0x50: // i64.eqz
		return b.eqz(valI64)
	case 0x67, 0x68, 0x69: // i32.clz, ctz, popcnt
		return b.bitCount(op-0x67, valI32)
	case 0x79, 0x7A, 0x7B: // i64.clz, ctz, popcnt
		return b.bitCount(op-0x79, valI64)
	case 0x77, 0x78: // i32.rotl, i32.rotr
		return b.rotate(valI32, op == 0x78)
	case 0x89, 0x8A: // i64.rotl, i64.rotr
		return b.rotate(valI64, op == 0x8A)
	case 0x8C: // f32.neg
		return b.unaryOp("neg", valF32)
	case 0x9A: // f64.neg
		return b.unaryOp("neg", valF64)
	}
	if entry, ok := floatUnaryOps[op]; ok {
		return b.unaryOp(entry.op, entry.operand)
	}
	return fmt.Errorf("opcode %#x is not translated", op)
}

// unsignedOf is the unsigned type of the same width, which is what every
// _u operation reads its operands through.
func unsignedOf(v valType) *metals.Prim {
	if v == valI64 {
		return metals.TUint64
	}
	return metals.TUint32
}

func widthOf(v valType) int {
	if v == valI64 {
		return 64
	}
	return 32
}

// reinterpret emits a bitcast, or returns the register unchanged when
// there is nothing to reinterpret.
func (b *bodyTranslator) reinterpret(reg string, to metals.Type) string {
	out := b.temp(to)
	b.emit(&metals.Bitcast{Dst: out, X: reg, To: to})
	return out
}

func (b *bodyTranslator) applyNum(entry numOp) error {
	left, right, err := b.pop2()
	if err != nil {
		return err
	}
	if left.typ != entry.operand || right.typ != entry.operand {
		return fmt.Errorf("%s wants two %ss and the stack holds a %s and a %s",
			entry.op, entry.operand, left.typ, right.typ)
	}

	a, c := left.reg, right.reg
	work := entry.operand.metalsType()
	if entry.unsigned {
		work = unsignedOf(entry.operand)
		a = b.reinterpret(a, work)
		c = b.reinterpret(c, work)
	}
	if entry.shift {
		mask := b.constant(work, strconv.Itoa(widthOf(entry.operand)-1))
		masked := b.temp(work)
		b.emit(&metals.Bin{Dst: masked, Op: "band", A: c, B: mask})
		c = masked
	}

	if entry.compare {
		flag := b.temp(metals.TBool)
		b.emit(&metals.Bin{Dst: flag, Op: entry.op, A: a, B: c})
		b.widen(flag)
		return nil
	}

	// The result is computed in the working type and read back as the
	// stack type, which for a signed operation are the same type.
	result := b.temp(work)
	if entry.guardRem {
		b.emitGuardedRem(result, entry, a, c)
	} else {
		b.emit(&metals.Bin{Dst: result, Op: entry.op, A: a, B: c})
	}

	dst := b.push(entry.result)
	if metals.TypesEqual(work, entry.result.metalsType()) {
		b.emit(&metals.Copy{Dst: dst, X: result})
		return nil
	}
	b.emit(&metals.Bitcast{Dst: dst, X: result, To: entry.result.metalsType()})
	return nil
}

// emitGuardedRem covers the one place wasm and METALS disagree about a
// defined answer rather than about a spelling: wasm says INT_MIN % -1 is
// 0, and METALS' irem traps on it because C's is undefined. The divisor
// is tested, so the trap on a zero divisor is still reached.
func (b *bodyTranslator) emitGuardedRem(dst string, entry numOp, a, c string) {
	work := entry.operand.metalsType()
	negative := b.constant(work, "-1")
	isNegOne := b.temp(metals.TBool)
	b.emit(&metals.Bin{Dst: isNegOne, Op: "eq", A: c, B: negative})
	b.emit(&metals.If{
		Cond: isNegOne,
		Then: []metals.Instr{&metals.Const{Dst: dst, Type: work, Value: "0"}},
		Else: []metals.Instr{&metals.Bin{Dst: dst, Op: entry.op, A: a, B: c}},
	})
}

// widen turns a bool into the 0 or 1 wasm expects on the stack, and
// remembers the pair so a branch reading it back can use the bool.
func (b *bodyTranslator) widen(flag string) {
	dst := b.push(valI32)
	b.emit(&metals.If{
		Cond: flag,
		Then: []metals.Instr{&metals.Const{Dst: dst, Type: metals.TInt32, Value: "1"}},
		Else: []metals.Instr{&metals.Const{Dst: dst, Type: metals.TInt32, Value: "0"}},
	})
	b.pendingCompare.value = dst
	b.pendingCompare.flag = flag
	b.pendingCompare.valid = true
}

func (b *bodyTranslator) eqz(v valType) error {
	value, err := b.pop()
	if err != nil {
		return err
	}
	if value.typ != v {
		return fmt.Errorf("eqz wants a %s and the stack holds a %s", v, value.typ)
	}
	zero := b.constant(v.metalsType(), "0")
	flag := b.temp(metals.TBool)
	b.emit(&metals.Bin{Dst: flag, Op: "eq", A: value.reg, B: zero})
	b.widen(flag)
	return nil
}

func (b *bodyTranslator) unaryOp(op string, v valType) error {
	value, err := b.pop()
	if err != nil {
		return err
	}
	if value.typ != v {
		return fmt.Errorf("%s wants a %s and the stack holds a %s", op, v, value.typ)
	}
	b.emit(&metals.Un{Dst: b.push(v), Op: op, X: value.reg})
	return nil
}

// rotate synthesizes a rotation from two shifts. The count is masked
// first, and the complementary shift is by the full width when the count
// is zero — which yields zero under Go's shift rule, leaving the value
// unchanged, exactly as a rotation by zero should.
func (b *bodyTranslator) rotate(v valType, right bool) error {
	value, amount, err := b.pop2()
	if err != nil {
		return err
	}
	if value.typ != v || amount.typ != v {
		return fmt.Errorf("a rotation wants two %ss", v)
	}
	work := unsignedOf(v)
	width := widthOf(v)

	x := b.reinterpret(value.reg, work)
	count := b.reinterpret(amount.reg, work)
	mask := b.constant(work, strconv.Itoa(width-1))
	k := b.temp(work)
	b.emit(&metals.Bin{Dst: k, Op: "band", A: count, B: mask})

	full := b.constant(work, strconv.Itoa(width))
	complement := b.temp(work)
	b.emit(&metals.Bin{Dst: complement, Op: "wsub", A: full, B: k})

	forward, backward := "shl", "shr"
	if right {
		forward, backward = "shr", "shl"
	}
	low := b.temp(work)
	high := b.temp(work)
	b.emit(&metals.Bin{Dst: low, Op: forward, A: x, B: k})
	b.emit(&metals.Bin{Dst: high, Op: backward, A: x, B: complement})
	joined := b.temp(work)
	b.emit(&metals.Bin{Dst: joined, Op: "bor", A: low, B: high})
	b.emit(&metals.Bitcast{Dst: b.push(v), X: joined, To: v.metalsType()})
	return nil
}

// bitCount calls one of the synthesized counting helpers: 0 is clz, 1 is
// ctz, 2 is popcnt.
func (b *bodyTranslator) bitCount(which byte, v valType) error {
	value, err := b.pop()
	if err != nil {
		return err
	}
	if value.typ != v {
		return fmt.Errorf("a bit count wants a %s and the stack holds a %s", v, value.typ)
	}
	name := b.t.bitHelper(which, v)
	work := unsignedOf(v)
	argument := b.reinterpret(value.reg, work)
	counted := b.temp(work)
	b.emit(&metals.Call{Dst: counted, Callee: name, Args: []string{argument}})
	b.emit(&metals.Bitcast{Dst: b.push(v), X: counted, To: v.metalsType()})
	return nil
}

func (b *bodyTranslator) applyConv(entry conversion) error {
	value, err := b.pop()
	if err != nil {
		return err
	}
	if value.typ != entry.from {
		return fmt.Errorf("a conversion wants a %s and the stack holds a %s", entry.from, value.typ)
	}

	source := value.reg
	if entry.unsignedSource {
		source = b.reinterpret(source, unsignedOf(entry.from))
	}
	dst := b.push(entry.to)
	target := entry.to.metalsType()

	switch entry.via {
	case "conv":
		b.emit(&metals.Conv{Dst: dst, X: source, To: target})
	case "trunc":
		b.emit(&metals.Trunc{Dst: dst, X: source, To: target})
	case "bitcast":
		b.emit(&metals.Bitcast{Dst: dst, X: source, To: target})
	case "extend":
		// Keep the low bytes, then widen them back with their sign: this
		// is what a sign-extending operation is, spelled out.
		narrow := b.temp(entry.through)
		b.emit(&metals.Trunc{Dst: narrow, X: source, To: entry.through})
		if metals.TypesEqual(entry.through, target) {
			b.emit(&metals.Copy{Dst: dst, X: narrow})
			return nil
		}
		b.emit(&metals.Conv{Dst: dst, X: narrow, To: target})
	}
	return nil
}
