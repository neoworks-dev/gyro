package nasm

import (
	"math"

	"github.com/gyro-lang/gyro/internal/metals"
)

func floatBits(v float64) uint64 { return math.Float64bits(v) }

const overflowMessage = "integer overflow"
const divZeroMessage = "division by zero"
const convLossMessage = "conversion loses the value"

// ---------------------------------------------------------------------------
// Binary operators
// ---------------------------------------------------------------------------

func (f *frame) emitBin(i *metals.Bin) {
	operand := f.typeOf(i.A)
	switch i.Op {
	case "eq", "ne":
		f.emitEquality(i.Dst, i.Op, i.A, i.B, operand)
		return
	case "lt", "le", "gt", "ge":
		f.emitOrdered(i, operand)
		return
	case "fadd", "fsub", "fmul", "fdiv":
		f.emitFloatBin(i)
		return
	case "fmin", "fmax", "fcopysign":
		f.emitFloatOp(i.Dst, i.Op, i.A, i.B, isSingle(operand))
		return
	}

	prim, ok := operand.(*metals.Prim)
	if !ok {
		f.g.fail("@%s: %s of a %s", f.fn.Name, i.Op, operand)
	}
	kind, ok := intKinds[prim.Kind]
	if !ok {
		f.g.fail("@%s: %s of a %s", f.fn.Name, i.Op, operand)
	}

	switch i.Op {
	case "iadd", "isub", "imul":
		f.emitCheckedArith(i, kind)
	case "wadd", "wsub", "wmul":
		f.emitWrappingArith(i, kind)
	case "idiv", "irem":
		f.emitDivRem(i, kind)
	case "band", "bor", "bxor":
		f.emitBitwise(i)
	case "shl", "shr":
		f.emitShift(i, kind)
	default:
		f.g.fail("@%s: unknown operator %s", f.fn.Name, i.Op)
	}
}

func (f *frame) emitFloatBin(i *metals.Bin) {
	g := f.g
	mnemonic := map[string]string{"fadd": "addsd", "fsub": "subsd", "fmul": "mulsd", "fdiv": "divsd"}[i.Op]
	g.ins("movsd xmm0, %s", f.slot(i.A))
	g.ins("%s xmm0, %s", mnemonic, f.slot(i.B))
	f.narrowFloat(f.typeOf(i.A))
	g.ins("movsd %s, xmm0", f.slot(i.Dst))
}

// emitCheckedArith is iadd/isub/imul: overflow aborts. 64-bit widths
// trap on the flags; narrower widths compute exactly in 64 bits and
// check that the result still fits.
func (f *frame) emitCheckedArith(i *metals.Bin, kind intKindInfo) {
	g := f.g
	overflow := f.panicLabel(overflowMessage)
	dst, a, b := f.slot(i.Dst), f.slot(i.A), f.slot(i.B)

	if i.Op == "imul" && !kind.signed && kind.width == 64 {
		// The widening multiply names rax and rdx itself, and takes no
		// immediate.
		if b.isImm {
			b = f.inReg(b, "rcx")
		}
		f.loadWork("rax", a)
		g.ins("mul %s", b.sized("qword"))
		g.ins("test rdx, rdx")
		g.ins("jnz %s", overflow)
		f.storeWork(dst, "rax")
		return
	}

	work := f.work(dst, b)
	f.loadWork(work, a)
	switch {
	case i.Op == "imul":
		f.multiply(work, b)
		if kind.signed && kind.width == 64 {
			g.ins("jo %s", overflow)
		} else {
			f.checkFits(work, kind, overflow)
		}
	case i.Op == "isub" && !kind.signed:
		g.ins("sub %s, %s", work, b)
		g.ins("jc %s", overflow)
	default:
		mnemonic := "add"
		if i.Op == "isub" {
			mnemonic = "sub"
		}
		g.ins("%s %s, %s", mnemonic, work, b)
		switch {
		case kind.signed && kind.width == 64:
			g.ins("jo %s", overflow)
		case !kind.signed && kind.width == 64:
			g.ins("jc %s", overflow)
		default:
			f.checkFits(work, kind, overflow)
		}
	}
	f.storeWork(dst, work)
}

// checkFits aborts unless the working register fits the width. Clobbers
// rcx, which is never a home and never a working register.
func (f *frame) checkFits(work string, kind intKindInfo, target string) {
	g := f.g
	if kind.width == 64 {
		return
	}
	if kind.signed {
		g.ins("%s", signExtend("rcx", work, kind.width))
		g.ins("cmp %s, rcx", work)
		g.ins("jne %s", target)
		return
	}
	g.ins("mov rcx, %d", uintMaxOf(kind.width))
	g.ins("cmp %s, rcx", work)
	g.ins("ja %s", target)
}

// signExtend widens the low bits of src into the full dst register.
func signExtend(dst, src string, width int) string {
	if width >= 32 {
		return "movsxd " + dst + ", " + narrow(src, 32)
	}
	return "movsx " + dst + ", " + narrow(src, width)
}

func uintMaxOf(width int) uint64 {
	if width >= 64 {
		return math.MaxUint64
	}
	return (uint64(1) << width) - 1
}

func (f *frame) emitWrappingArith(i *metals.Bin, kind intKindInfo) {
	g := f.g
	mnemonic := map[string]string{"wadd": "add", "wsub": "sub", "wmul": "imul"}[i.Op]
	dst, b := f.slot(i.Dst), f.slot(i.B)
	work := f.work(dst, b)
	f.loadWork(work, f.slot(i.A))
	if mnemonic == "imul" {
		f.multiply(work, b)
	} else {
		g.ins("%s %s, %s", mnemonic, work, b)
	}
	f.wrapToWidth(work, kind)
	f.storeWork(dst, work)
}

// multiply is the two-operand signed multiply. Its immediate form is
// three-operand, so an immediate spells the destination out twice.
func (f *frame) multiply(work string, b loc) {
	if b.isImm {
		f.g.ins("imul %s, %s, %s", work, work, b)
		return
	}
	f.g.ins("imul %s, %s", work, b)
}

// wrapToWidth truncates the working register to the width and re-extends
// it, keeping the full-qword invariant every value holds.
func (f *frame) wrapToWidth(work string, kind intKindInfo) {
	g := f.g
	if kind.width == 64 {
		return
	}
	if kind.signed {
		g.ins("%s", signExtend(work, work, kind.width))
		return
	}
	switch kind.width {
	case 8, 16:
		g.ins("movzx %s, %s", work, narrow(work, kind.width))
	default:
		g.ins("mov %s, %s", narrow(work, 32), narrow(work, 32))
	}
}

func (f *frame) emitDivRem(i *metals.Bin, kind intKindInfo) {
	g := f.g
	// The dividend and both results are rax and rdx, so only the divisor
	// can stay where it already is.
	divisor := f.inReg(f.slot(i.B), "rcx")
	f.testZero(divisor)
	g.ins("jz %s", f.panicLabel(divZeroMessage))
	f.loadWork("rax", f.slot(i.A))

	if !kind.signed {
		g.ins("xor edx, edx")
		g.ins("div %s", divisor)
		f.storeDivResult(i)
		return
	}

	done := g.newLabel()
	if i.Op == "irem" {
		// x % -1 is 0 for every x; skipping the divide also avoids the
		// MIN/-1 hardware fault.
		skip := g.newLabel()
		g.ins("cmp %s, -1", divisor)
		g.ins("jne %s", skip)
		g.ins("xor edx, edx")
		g.ins("jmp %s", done)
		g.label(skip)
	} else if kind.width == 64 {
		safe := g.newLabel()
		g.ins("mov rdx, -9223372036854775808")
		g.ins("cmp rax, rdx")
		g.ins("jne %s", safe)
		g.ins("cmp %s, -1", divisor)
		g.ins("je %s", f.panicLabel(overflowMessage))
		g.label(safe)
	}
	g.ins("cqo")
	g.ins("idiv %s", divisor)
	if i.Op == "idiv" && kind.width < 64 {
		f.checkFits("rax", kind, f.panicLabel(overflowMessage))
	}
	g.label(done)
	f.storeDivResult(i)
}

func (f *frame) storeDivResult(i *metals.Bin) {
	source := "rax"
	if i.Op == "irem" {
		source = "rdx"
	}
	f.storeWork(f.slot(i.Dst), source)
}

func (f *frame) emitBitwise(i *metals.Bin) {
	mnemonic := map[string]string{"band": "and", "bor": "or", "bxor": "xor"}[i.Op]
	dst, b := f.slot(i.Dst), f.slot(i.B)
	work := f.work(dst, b)
	f.loadWork(work, f.slot(i.A))
	f.g.ins("%s %s, %s", mnemonic, work, b)
	f.storeWork(dst, work)
}

// emitShift keeps Go semantics: a count of 64 or more (or a negative
// one) is not masked the x86 way — left shifts and logical right shifts
// produce 0, arithmetic right shifts produce the sign fill.
func (f *frame) emitShift(i *metals.Bin, kind intKindInfo) {
	g := f.g
	dst := f.slot(i.Dst)
	if count := f.slot(i.B); count.isImm {
		f.emitConstShift(i, kind, dst, count.imm)
		return
	}
	// The count has to be cl, so it moves first: the working register may
	// well be the count's own home.
	g.ins("mov rcx, %s", f.slot(i.B))
	work := f.work(dst)
	f.loadWork(work, f.slot(i.A))

	big := g.newLabel()
	done := g.newLabel()
	if kind.signed {
		g.ins("test rcx, rcx")
		g.ins("js %s", big)
	}
	g.ins("cmp rcx, 64")
	g.ins("jae %s", big)

	if i.Op == "shl" {
		g.ins("shl %s, cl", work)
		f.wrapToWidth(work, kind)
	} else if kind.signed {
		g.ins("sar %s, cl", work)
	} else {
		g.ins("shr %s, cl", work)
	}
	g.ins("jmp %s", done)

	g.label(big)
	if i.Op == "shr" && kind.signed {
		g.ins("sar %s, 63", work)
	} else {
		g.ins("xor %s, %s", narrow(work, 32), narrow(work, 32))
	}
	g.label(done)
	f.storeWork(dst, work)
}

// emitConstShift takes the branch a known count decides here instead of
// at run time.
func (f *frame) emitConstShift(i *metals.Bin, kind intKindInfo, dst loc, count int64) {
	g := f.g
	work := f.work(dst)
	f.loadWork(work, f.slot(i.A))
	switch {
	case count < 0 || count >= 64:
		if i.Op == "shr" && kind.signed {
			g.ins("sar %s, 63", work)
		} else {
			g.ins("xor %s, %s", narrow(work, 32), narrow(work, 32))
		}
	case count == 0:
		// The value is already the whole word it has to be.
	case i.Op == "shl":
		g.ins("shl %s, %d", work, count)
		f.wrapToWidth(work, kind)
	case kind.signed:
		g.ins("sar %s, %d", work, count)
	default:
		g.ins("shr %s, %d", work, count)
	}
	f.storeWork(dst, work)
}

// ---------------------------------------------------------------------------
// Comparisons
// ---------------------------------------------------------------------------

func (f *frame) emitOrdered(i *metals.Bin, operand metals.Type) {
	g := f.g
	prim, ok := operand.(*metals.Prim)
	if !ok {
		g.fail("@%s: ordering a %s", f.fn.Name, operand)
	}

	if prim.Kind == metals.F32 || prim.Kind == metals.F64 {
		// a<b is computed as b>a so that NaN (which sets CF) reads false.
		swapped := i.Op == "lt" || i.Op == "le"
		first, second := i.A, i.B
		if swapped {
			first, second = i.B, i.A
		}
		g.ins("movsd xmm0, %s", f.slot(first))
		g.ins("ucomisd xmm0, %s", f.slot(second))
		set := "seta"
		if i.Op == "le" || i.Op == "ge" {
			set = "setae"
		}
		f.setFlag(f.slot(i.Dst), set)
		return
	}

	kind, ok := intKinds[prim.Kind]
	if !ok {
		g.fail("@%s: ordering a %s", f.fn.Name, operand)
	}
	sets := map[string][2]string{
		"lt": {"setl", "setb"}, "le": {"setle", "setbe"},
		"gt": {"setg", "seta"}, "ge": {"setge", "setae"},
	}[i.Op]
	set := sets[1]
	if kind.signed {
		set = sets[0]
	}
	f.compare(f.slot(i.A), f.slot(i.B))
	f.setFlag(f.slot(i.Dst), set)
}

// compare emits a cmp of two whole words. Only the second operand may
// be an immediate, and at most one may be memory, so anything else goes
// through rax first.
func (f *frame) compare(a, b loc) {
	if a.isImm || !a.isReg() && !b.isReg() {
		a = f.inReg(a, "rax")
	}
	f.g.ins("cmp %s, %s", a, b)
}

// setFlag widens a condition code into a whole word, in the
// destination's own home where it has one.
func (f *frame) setFlag(dst loc, set string) {
	out := "rax"
	if dst.isReg() {
		out = dst.reg
	}
	f.g.ins("%s %s", set, narrow(out, 8))
	f.g.ins("movzx %s, %s", narrow(out, 32), narrow(out, 8))
	f.storeWork(dst, out)
}

func (f *frame) emitEquality(dst, op, a, b string, operand metals.Type) {
	g := f.g
	negate := op == "ne"

	switch t := operand.(type) {
	case *metals.Prim:
		switch t.Kind {
		case metals.F32, metals.F64:
			f.emitFloatEquality(dst, negate, a, b)
			return
		case metals.Str:
			g.ins("mov rdi, %s", f.slot(a))
			g.ins("mov rsi, %s", f.slot(b))
			g.ins("call %s", g.extern("gy_str_eq"))
			f.finishEquality(dst, negate)
			return
		}
		f.emitWordEquality(dst, negate, a, b)
		return
	case *metals.Ptr, *metals.AnyPtr, *metals.Handle, *metals.CodeType:
		f.emitWordEquality(dst, negate, a, b)
		return
	case *metals.Opt:
		// A niche optional is a pointer, null included, so the comparison
		// that answers a pointer answers this too — and it has to be done
		// here, because a scalar may be homed in a machine register and a
		// helper takes addresses.
		if metals.NicheOpt(t) {
			f.emitWordEquality(dst, negate, a, b)
			return
		}
	case *metals.SliceOf, *metals.FnType:
		// The evaluator treats these as never equal.
		value := int64(0)
		if negate {
			value = 1
		}
		f.storeWord(f.slot(dst), value)
		return
	}

	helper := g.eqHelperFor(operand)
	g.ins("lea rdi, %s", f.addr(a))
	g.ins("lea rsi, %s", f.addr(b))
	g.ins("call %s", helper)
	f.finishEquality(dst, negate)
}

func (f *frame) emitWordEquality(dst string, negate bool, a, b string) {
	set := "sete"
	if negate {
		set = "setne"
	}
	f.compare(f.slot(a), f.slot(b))
	f.setFlag(f.slot(dst), set)
}

func (f *frame) emitFloatEquality(dst string, negate bool, a, b string) {
	g := f.g
	g.ins("movsd xmm0, %s", f.slot(a))
	g.ins("ucomisd xmm0, %s", f.slot(b))
	if negate {
		g.ins("setne al")
		g.ins("setp cl")
		g.ins("or al, cl")
	} else {
		g.ins("sete al")
		g.ins("setnp cl")
		g.ins("and al, cl")
	}
	g.ins("movzx eax, al")
	f.storeWork(f.slot(dst), "rax")
}

func (f *frame) finishEquality(dst string, negate bool) {
	if negate {
		f.g.ins("xor rax, 1")
	}
	f.storeWork(f.slot(dst), "rax")
}

// ---------------------------------------------------------------------------
// Unary operators
// ---------------------------------------------------------------------------

func (f *frame) emitUn(i *metals.Un) {
	g := f.g
	dst, x := f.slot(i.Dst), f.slot(i.X)
	switch i.Op {
	case "not":
		work := f.work(dst)
		f.loadWork(work, x)
		g.ins("xor %s, 1", work)
		f.storeWork(dst, work)

	case "neg":
		prim, ok := f.typeOf(i.X).(*metals.Prim)
		if !ok {
			g.fail("@%s: neg of a %s", f.fn.Name, f.typeOf(i.X))
		}
		if prim.Kind == metals.F32 || prim.Kind == metals.F64 {
			g.ins("movsd xmm0, %s", x)
			g.ins("mov rax, -9223372036854775808") // the sign bit
			g.ins("movq xmm1, rax")
			g.ins("xorpd xmm0, xmm1")
			g.ins("movsd %s, xmm0", dst)
			return
		}
		kind := intKinds[prim.Kind]
		overflow := f.panicLabel(overflowMessage)
		work := f.work(dst, x)
		g.ins("xor %s, %s", narrow(work, 32), narrow(work, 32))
		g.ins("sub %s, %s", work, x)
		if kind.width == 64 {
			g.ins("jo %s", overflow)
		} else {
			f.checkFits(work, kind, overflow)
		}
		f.storeWork(dst, work)

	case "bnot":
		prim := f.typeOf(i.X).(*metals.Prim)
		kind := intKinds[prim.Kind]
		work := f.work(dst)
		f.loadWork(work, x)
		g.ins("not %s", work)
		if !kind.signed {
			f.wrapToWidth(work, kind)
		}
		f.storeWork(dst, work)

	default:
		if !f.emitFloatOp(i.Dst, i.Op, i.X, "", isSingle(f.typeOf(i.X))) {
			g.fail("@%s: unknown unary operator %s", f.fn.Name, i.Op)
		}
	}
}

// ---------------------------------------------------------------------------
// Conversions
// ---------------------------------------------------------------------------

func (f *frame) emitConv(i *metals.Conv) {
	g := f.g
	if _, folded := f.consts[i.Dst]; folded {
		return
	}
	source, ok := f.typeOf(i.X).(*metals.Prim)
	if !ok {
		g.fail("@%s: conv from a %s", f.fn.Name, f.typeOf(i.X))
	}
	target, ok := i.To.(*metals.Prim)
	if !ok {
		g.fail("@%s: conv to a %s", f.fn.Name, i.To)
	}

	if target.Kind == metals.F32 || target.Kind == metals.F64 {
		f.emitConvToFloat(i, source, target)
		return
	}
	if source.Kind == metals.F32 || source.Kind == metals.F64 {
		f.emitConvFloatToInt(i, target)
		return
	}
	f.emitConvIntToInt(i, intKinds[source.Kind], intKinds[target.Kind])
}

func (f *frame) emitConvToFloat(i *metals.Conv, source, target *metals.Prim) {
	g := f.g
	switch {
	case source.Kind == metals.F32 || source.Kind == metals.F64:
		g.ins("movsd xmm0, %s", f.slot(i.X))
	case intKinds[source.Kind].signed:
		// cvtsi2sd takes no immediate.
		g.ins("cvtsi2sd xmm0, %s", f.inReg(f.slot(i.X), "rax").sized("qword"))
	default:
		// cvtsi2sd reads its source as signed, so a value with the top bit
		// set is halved with its low bit kept (to round correctly), converted
		// and doubled.
		big, done := g.newLabel(), g.newLabel()
		g.ins("mov rax, %s", f.slot(i.X))
		g.ins("test rax, rax")
		g.ins("js %s", big)
		g.ins("cvtsi2sd xmm0, rax")
		g.ins("jmp %s", done)
		g.label(big)
		g.ins("mov rcx, rax")
		g.ins("shr rcx, 1")
		g.ins("and eax, 1")
		g.ins("or rcx, rax")
		g.ins("cvtsi2sd xmm0, rcx")
		g.ins("addsd xmm0, xmm0")
		g.label(done)
	}
	if target.Kind == metals.F32 {
		g.ins("cvtsd2ss xmm0, xmm0")
		g.ins("cvtss2sd xmm0, xmm0")
	}
	g.ins("movsd %s, xmm0", f.slot(i.Dst))
}

// emitConvFloatToInt truncates toward zero and aborts when the result does
// not fit. cvttsd2si answers 0x8000000000000000 for what a signed 64-bit
// number cannot hold and for NaN, which is out of range for every narrower
// width too, so only the 64-bit signed case has to ask whether the input
// was exactly that value.
func (f *frame) emitConvFloatToInt(i *metals.Conv, target *metals.Prim) {
	g := f.g
	kind := intKinds[target.Kind]
	loss := f.panicLabel(convLossMessage)
	g.ins("movsd xmm0, %s", f.slot(i.X))

	switch {
	case kind.signed:
		g.ins("cvttsd2si rax, xmm0")
		if kind.width == 64 {
			fits := g.newLabel()
			g.ins("mov rcx, 0x8000000000000000")
			g.ins("cmp rax, rcx")
			g.ins("jne %s", fits)
			g.ins("movq rdx, xmm0")
			g.ins("mov rcx, 0xC3E0000000000000") // -2^63
			g.ins("cmp rdx, rcx")
			g.ins("jne %s", loss)
			g.label(fits)
		} else {
			f.checkFits("rax", kind, loss)
		}

	case kind.width < 64:
		g.ins("cvttsd2si rax, xmm0")
		g.ins("test rax, rax")
		g.ins("js %s", loss)
		g.ins("mov rcx, %d", uintMaxOf(kind.width))
		g.ins("cmp rax, rcx")
		g.ins("ja %s", loss)

	default:
		// u64: below 2^63 it is the signed conversion; from there up the
		// 2^63 is taken off first and put back as the top bit.
		big, done := g.newLabel(), g.newLabel()
		g.ins("mov rax, 0x43E0000000000000") // 2^63
		g.ins("movq xmm1, rax")
		g.ins("ucomisd xmm0, xmm1")
		g.ins("jae %s", big)
		g.ins("cvttsd2si rax, xmm0")
		g.ins("test rax, rax")
		g.ins("js %s", loss)
		g.ins("jmp %s", done)
		g.label(big)
		g.ins("subsd xmm0, xmm1")
		g.ins("cvttsd2si rax, xmm0")
		g.ins("test rax, rax")
		g.ins("js %s", loss)
		g.ins("mov rcx, 0x8000000000000000")
		g.ins("xor rax, rcx")
		g.label(done)
	}
	f.storeWork(f.slot(i.Dst), "rax")
}

func (f *frame) emitConvIntToInt(i *metals.Conv, source, target intKindInfo) {
	g := f.g
	dst := f.slot(i.Dst)
	// Widening cannot lose anything when the sign agrees, or when an
	// unsigned source lands in a strictly wider signed target, and every
	// value is already carried as a whole word.
	widens := target.width > source.width || target.width == source.width && source.signed == target.signed
	if widens && (source.signed == target.signed || !source.signed && target.width > source.width) {
		f.copyMem(dst, f.slot(i.X), wordSize)
		return
	}

	loss := f.panicLabel(convLossMessage)
	work := f.work(dst)
	f.loadWork(work, f.slot(i.X))

	switch {
	case source.signed && target.signed:
		if target.width < source.width {
			f.checkFits(work, target, loss)
		}
	case source.signed && !target.signed:
		g.ins("test %s, %s", work, work)
		g.ins("js %s", loss)
		if target.width < 64 {
			g.ins("mov rcx, %d", uintMaxOf(target.width))
			g.ins("cmp %s, rcx", work)
			g.ins("ja %s", loss)
		}
	case !source.signed && target.signed:
		g.ins("mov rcx, %d", int64(1)<<(target.width-1)-1)
		g.ins("cmp %s, rcx", work)
		g.ins("ja %s", loss)
	default:
		if target.width < source.width {
			g.ins("mov rcx, %d", uintMaxOf(target.width))
			g.ins("cmp %s, rcx", work)
			g.ins("ja %s", loss)
		}
	}
	f.storeWork(dst, work)
}
