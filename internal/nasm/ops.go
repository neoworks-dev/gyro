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
	g.ins("movsd %s, xmm0", f.slot(i.Dst))
}

// emitCheckedArith is iadd/isub/imul: overflow aborts. 64-bit widths
// trap on the flags; narrower widths compute exactly in 64 bits and
// check that the result still fits.
func (f *frame) emitCheckedArith(i *metals.Bin, kind intKindInfo) {
	g := f.g
	overflow := f.panicLabel(overflowMessage)
	g.ins("mov rax, %s", f.slot(i.A))

	switch {
	case i.Op == "imul" && !kind.signed && kind.width == 64:
		g.ins("mul qword %s", f.slot(i.B))
		g.ins("test rdx, rdx")
		g.ins("jnz %s", overflow)
	case i.Op == "imul":
		g.ins("imul rax, %s", f.slot(i.B))
		if kind.signed && kind.width == 64 {
			g.ins("jo %s", overflow)
		} else {
			f.checkFits(kind, overflow)
		}
	case i.Op == "isub" && !kind.signed:
		g.ins("sub rax, %s", f.slot(i.B))
		g.ins("jc %s", overflow)
	default:
		mnemonic := "add"
		if i.Op == "isub" {
			mnemonic = "sub"
		}
		g.ins("%s rax, %s", mnemonic, f.slot(i.B))
		switch {
		case kind.signed && kind.width == 64:
			g.ins("jo %s", overflow)
		case !kind.signed && kind.width == 64:
			g.ins("jc %s", overflow)
		default:
			f.checkFits(kind, overflow)
		}
	}
	g.ins("mov %s, rax", f.slot(i.Dst))
}

// checkFits aborts unless rax fits the width. Clobbers rcx.
func (f *frame) checkFits(kind intKindInfo, target string) {
	g := f.g
	if kind.width == 64 {
		return
	}
	if kind.signed {
		g.ins("%s", signExtendInstr(kind.width))
		g.ins("cmp rax, rcx")
		g.ins("jne %s", target)
		return
	}
	g.ins("mov rcx, %d", uintMaxOf(kind.width))
	g.ins("cmp rax, rcx")
	g.ins("ja %s", target)
}

func signExtendInstr(width int) string {
	switch width {
	case 8:
		return "movsx rcx, al"
	case 16:
		return "movsx rcx, ax"
	default:
		return "movsxd rcx, eax"
	}
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
	g.ins("mov rax, %s", f.slot(i.A))
	if mnemonic == "imul" {
		g.ins("imul rax, %s", f.slot(i.B))
	} else {
		g.ins("%s rax, %s", mnemonic, f.slot(i.B))
	}
	f.wrapToWidth(kind)
	g.ins("mov %s, rax", f.slot(i.Dst))
}

// wrapToWidth truncates rax to the width and re-extends it, keeping the
// full-qword invariant every slot holds.
func (f *frame) wrapToWidth(kind intKindInfo) {
	g := f.g
	if kind.width == 64 {
		return
	}
	if kind.signed {
		switch kind.width {
		case 8:
			g.ins("movsx rax, al")
		case 16:
			g.ins("movsx rax, ax")
		default:
			g.ins("movsxd rax, eax")
		}
		return
	}
	switch kind.width {
	case 8:
		g.ins("movzx rax, al")
	case 16:
		g.ins("movzx rax, ax")
	default:
		g.ins("mov eax, eax")
	}
}

func (f *frame) emitDivRem(i *metals.Bin, kind intKindInfo) {
	g := f.g
	g.ins("mov rcx, %s", f.slot(i.B))
	g.ins("test rcx, rcx")
	g.ins("jz %s", f.panicLabel(divZeroMessage))
	g.ins("mov rax, %s", f.slot(i.A))

	if !kind.signed {
		g.ins("xor edx, edx")
		g.ins("div rcx")
		f.storeDivResult(i)
		return
	}

	done := g.newLabel()
	if i.Op == "irem" {
		// x % -1 is 0 for every x; skipping the divide also avoids the
		// MIN/-1 hardware fault.
		skip := g.newLabel()
		g.ins("cmp rcx, -1")
		g.ins("jne %s", skip)
		g.ins("xor edx, edx")
		g.ins("jmp %s", done)
		g.label(skip)
	} else if kind.width == 64 {
		safe := g.newLabel()
		g.ins("mov rdx, -9223372036854775808")
		g.ins("cmp rax, rdx")
		g.ins("jne %s", safe)
		g.ins("cmp rcx, -1")
		g.ins("je %s", f.panicLabel(overflowMessage))
		g.label(safe)
	}
	g.ins("cqo")
	g.ins("idiv rcx")
	if i.Op == "idiv" && kind.width < 64 {
		f.checkFits(kind, f.panicLabel(overflowMessage))
	}
	g.label(done)
	f.storeDivResult(i)
}

func (f *frame) storeDivResult(i *metals.Bin) {
	source := "rax"
	if i.Op == "irem" {
		source = "rdx"
	}
	f.g.ins("mov %s, %s", f.slot(i.Dst), source)
}

func (f *frame) emitBitwise(i *metals.Bin) {
	g := f.g
	mnemonic := map[string]string{"band": "and", "bor": "or", "bxor": "xor"}[i.Op]
	g.ins("mov rax, %s", f.slot(i.A))
	g.ins("%s rax, %s", mnemonic, f.slot(i.B))
	g.ins("mov %s, rax", f.slot(i.Dst))
}

// emitShift keeps Go semantics: a count of 64 or more (or a negative
// one) is not masked the x86 way — left shifts and logical right shifts
// produce 0, arithmetic right shifts produce the sign fill.
func (f *frame) emitShift(i *metals.Bin, kind intKindInfo) {
	g := f.g
	g.ins("mov rax, %s", f.slot(i.A))
	g.ins("mov rcx, %s", f.slot(i.B))
	big := g.newLabel()
	done := g.newLabel()
	if kind.signed {
		g.ins("test rcx, rcx")
		g.ins("js %s", big)
	}
	g.ins("cmp rcx, 64")
	g.ins("jae %s", big)

	if i.Op == "shl" {
		g.ins("shl rax, cl")
		f.wrapToWidth(kind)
	} else if kind.signed {
		g.ins("sar rax, cl")
	} else {
		g.ins("shr rax, cl")
	}
	g.ins("jmp %s", done)

	g.label(big)
	if i.Op == "shr" && kind.signed {
		g.ins("sar rax, 63")
	} else {
		g.ins("xor eax, eax")
	}
	g.label(done)
	g.ins("mov %s, rax", f.slot(i.Dst))
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
		g.ins("%s al", set)
		g.ins("movzx eax, al")
		g.ins("mov %s, rax", f.slot(i.Dst))
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
	g.ins("mov rax, %s", f.slot(i.A))
	g.ins("cmp rax, %s", f.slot(i.B))
	g.ins("%s al", set)
	g.ins("movzx eax, al")
	g.ins("mov %s, rax", f.slot(i.Dst))
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
	case *metals.Ptr, *metals.ChanOf, *metals.FutureOf, *metals.Handle:
		f.emitWordEquality(dst, negate, a, b)
		return
	case *metals.SliceOf, *metals.FnType:
		// The evaluator treats these as never equal.
		value := 0
		if negate {
			value = 1
		}
		g.ins("mov qword %s, %d", f.slot(dst), value)
		return
	}

	helper := g.eqHelperFor(operand)
	g.ins("lea rdi, %s", f.slot(a))
	g.ins("lea rsi, %s", f.slot(b))
	g.ins("call %s", helper)
	f.finishEquality(dst, negate)
}

func (f *frame) emitWordEquality(dst string, negate bool, a, b string) {
	g := f.g
	set := "sete"
	if negate {
		set = "setne"
	}
	g.ins("mov rax, %s", f.slot(a))
	g.ins("cmp rax, %s", f.slot(b))
	g.ins("%s al", set)
	g.ins("movzx eax, al")
	g.ins("mov %s, rax", f.slot(dst))
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
	g.ins("mov %s, rax", f.slot(dst))
}

func (f *frame) finishEquality(dst string, negate bool) {
	if negate {
		f.g.ins("xor rax, 1")
	}
	f.g.ins("mov %s, rax", f.slot(dst))
}

// ---------------------------------------------------------------------------
// Unary operators
// ---------------------------------------------------------------------------

func (f *frame) emitUn(i *metals.Un) {
	g := f.g
	switch i.Op {
	case "not":
		g.ins("mov rax, %s", f.slot(i.X))
		g.ins("xor rax, 1")
		g.ins("mov %s, rax", f.slot(i.Dst))

	case "neg":
		prim, ok := f.typeOf(i.X).(*metals.Prim)
		if !ok {
			g.fail("@%s: neg of a %s", f.fn.Name, f.typeOf(i.X))
		}
		if prim.Kind == metals.F32 || prim.Kind == metals.F64 {
			g.ins("movsd xmm0, %s", f.slot(i.X))
			g.ins("mov rax, -9223372036854775808") // the sign bit
			g.ins("movq xmm1, rax")
			g.ins("xorpd xmm0, xmm1")
			g.ins("movsd %s, xmm0", f.slot(i.Dst))
			return
		}
		kind := intKinds[prim.Kind]
		overflow := f.panicLabel(overflowMessage)
		g.ins("xor eax, eax")
		g.ins("sub rax, %s", f.slot(i.X))
		if kind.width == 64 {
			g.ins("jo %s", overflow)
		} else {
			f.checkFits(kind, overflow)
		}
		g.ins("mov %s, rax", f.slot(i.Dst))

	case "bnot":
		prim := f.typeOf(i.X).(*metals.Prim)
		kind := intKinds[prim.Kind]
		g.ins("mov rax, %s", f.slot(i.X))
		g.ins("not rax")
		if !kind.signed {
			f.wrapToWidth(kind)
		}
		g.ins("mov %s, rax", f.slot(i.Dst))

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
		g.ins("cvtsi2sd xmm0, qword %s", f.slot(i.X))
	default:
		g.ins("mov rdi, %s", f.slot(i.X))
		g.ins("call %s", g.extern("gy_uint_to_double"))
	}
	if target.Kind == metals.F32 {
		g.ins("cvtsd2ss xmm0, xmm0")
		g.ins("cvtss2sd xmm0, xmm0")
	}
	g.ins("movsd %s, xmm0", f.slot(i.Dst))
}

func (f *frame) emitConvFloatToInt(i *metals.Conv, target *metals.Prim) {
	g := f.g
	kind := intKinds[target.Kind]
	g.ins("movsd xmm0, %s", f.slot(i.X))
	if kind.signed {
		bound := int64(1) << (kind.width - 1)
		g.ins("mov rdi, %d", -bound)
		g.ins("mov rsi, %d", bound-1)
		g.ins("call %s", g.extern("gy_double_to_int"))
	} else {
		g.ins("mov rdi, %d", int64(uintMaxOf(kind.width)))
		g.ins("call %s", g.extern("gy_double_to_uint"))
	}
	g.ins("mov %s, rax", f.slot(i.Dst))
}

func (f *frame) emitConvIntToInt(i *metals.Conv, source, target intKindInfo) {
	g := f.g
	loss := f.panicLabel(convLossMessage)
	g.ins("mov rax, %s", f.slot(i.X))

	switch {
	case source.signed && target.signed:
		if target.width < source.width {
			f.checkFits(target, loss)
		}
	case source.signed && !target.signed:
		g.ins("test rax, rax")
		g.ins("js %s", loss)
		if target.width < 64 {
			g.ins("mov rcx, %d", uintMaxOf(target.width))
			g.ins("cmp rax, rcx")
			g.ins("ja %s", loss)
		}
	case !source.signed && target.signed:
		g.ins("mov rcx, %d", int64(1)<<(target.width-1)-1)
		g.ins("cmp rax, rcx")
		g.ins("ja %s", loss)
	default:
		if target.width < source.width {
			g.ins("mov rcx, %d", uintMaxOf(target.width))
			g.ins("cmp rax, rcx")
			g.ins("ja %s", loss)
		}
	}
	g.ins("mov %s, rax", f.slot(i.Dst))
}
