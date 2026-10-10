package nasm

import (
	"strings"

	"github.com/gyro-lang/gyro/internal/crt"
	"github.com/gyro-lang/gyro/internal/metals"
)

// callArg is one argument of a System V call: a value loaded from
// memory, a pointer to memory (classMem), or an immediate.
type callArg struct {
	loc   loc
	class argClass
	imm   int64
	isImm bool
}

// emitSysVCall marshals arguments and calls the target, a symbol or a
// register holding the code address. Uses rax while filling stack
// arguments; never touches r10 or r11.
func (g *generator) emitSysVCall(target string, args []callArg) {
	var ints, sses, stack []callArg
	for _, arg := range args {
		switch {
		case arg.class == classSSE && !arg.isImm:
			if len(sses) < len(sseArgRegs) {
				sses = append(sses, arg)
			} else {
				stack = append(stack, arg)
			}
		default:
			if len(ints) < len(intArgRegs) {
				ints = append(ints, arg)
			} else {
				stack = append(stack, arg)
			}
		}
	}

	stackBytes := (len(stack)*8 + 15) / 16 * 16
	if len(stack) > 0 {
		g.ins("sub rsp, %d", stackBytes)
		for index, arg := range stack {
			g.loadArg("rax", arg)
			g.ins("mov [rsp+%d], rax", index*8)
		}
	}
	for index, arg := range sses {
		g.ins("movsd %s, %s", sseArgRegs[index], arg.loc)
	}
	for index, arg := range ints {
		g.loadArg(intArgRegs[index], arg)
	}
	g.ins("call %s", target)
	if len(stack) > 0 {
		g.ins("add rsp, %d", stackBytes)
	}
}

func (g *generator) loadArg(register string, arg callArg) {
	switch {
	case arg.isImm:
		g.ins("mov %s, %d", register, arg.imm)
	case arg.class == classMem:
		g.ins("lea %s, %s", register, arg.loc.address())
	default:
		g.ins("mov %s, %s", register, arg.loc)
	}
}

// ---------------------------------------------------------------------------
// Direct calls
// ---------------------------------------------------------------------------

// syscallRegs are where a Linux system call reads its arguments, in order.
var syscallRegs = []string{"rdi", "rsi", "rdx", "r10", "r8", "r9"}

func (f *frame) emitCall(i *metals.Call) {
	g := f.g
	if callee := g.funcs[i.Callee]; callee != nil {
		sig := callee.Sig()
		args := f.userCallArgs(sig, i.Args, i.Dst)
		g.emitSysVCall(g.symbolFor(i.Callee), args)
		f.storeCallResult(sig.Result, i.Dst)
		return
	}
	if sig := g.imports[i.Callee]; sig != nil {
		f.emitNativeCall(i.Callee, sig, i.Args, i.Dst)
		return
	}
	g.fail("@%s: call of unknown function @%s", f.fn.Name, i.Callee)
}

// userCallArgs builds the argument list for a generated function:
// hidden sret pointer first when the result is an aggregate, then every
// argument by value or by pointer according to its class.
func (f *frame) userCallArgs(sig *metals.FnType, argRegs []string, dst string) []callArg {
	g := f.g
	var args []callArg
	if sig.Result != nil && g.classOf(sig.Result) == classMem {
		if dst == "" {
			g.fail("@%s: an aggregate result needs a destination", f.fn.Name)
		}
		args = append(args, callArg{loc: f.slot(dst), class: classMem})
	}
	for index, reg := range argRegs {
		args = append(args, callArg{loc: f.slot(reg), class: g.classOf(sig.Params[index])})
	}
	return args
}

func (f *frame) storeCallResult(result metals.Type, dst string) {
	if result == nil || dst == "" {
		return
	}
	switch f.g.classOf(result) {
	case classSSE:
		f.g.ins("movsd %s, xmm0", f.slot(dst))
	case classInt:
		f.g.ins("mov %s, rax", f.slot(dst))
	case classMem:
		// Already written through the sret pointer.
	}
}

func (f *frame) emitCallIndirect(i *metals.CallIndirect) {
	g := f.g
	switch typ := f.typeOf(i.Fn).(type) {
	case *metals.CodeType:
		// A code pointer is the address alone: no environment word to
		// test, so there is one call rather than two. This is what C's
		// `fp(x)` compiles to anywhere else.
		sig := &metals.FnType{Params: typ.Params, Result: typ.Result}
		sret, plain := f.indirectArgs(sig, i)
		g.ins("mov r11, %s", f.slot(i.Fn))
		g.emitSysVCall("r11", append(append([]callArg{}, sret...), plain...))
		f.storeCallResult(sig.Result, i.Dst)

	case *metals.FnType:
		sret, plain := f.indirectArgs(typ, i)
		env := callArg{loc: f.slot(i.Fn).plus(8), class: classInt}

		bare := g.newLabel()
		done := g.newLabel()
		g.ins("mov r11, %s", f.slot(i.Fn))
		f.testZero(f.slot(i.Fn).plus(8))
		g.ins("jz %s", bare)
		g.emitSysVCall("r11", append(append(append([]callArg{}, sret...), env), plain...))
		g.ins("jmp %s", done)
		g.label(bare)
		g.emitSysVCall("r11", append(append([]callArg{}, sret...), plain...))
		g.label(done)
		f.storeCallResult(typ.Result, i.Dst)

	default:
		g.fail("@%s: call.indirect through a %s", f.fn.Name, f.typeOf(i.Fn))
	}
}

// indirectArgs classifies what an indirect call passes: the hidden
// return-area pointer when the result is memory, and the arguments the
// signature names. The environment is not here, because only one of the
// two callable types has one.
func (f *frame) indirectArgs(sig *metals.FnType, i *metals.CallIndirect) (sret, plain []callArg) {
	if sig.Result != nil && f.g.classOf(sig.Result) == classMem {
		if i.Dst == "" {
			f.g.fail("@%s: an aggregate result needs a destination", f.fn.Name)
		}
		sret = []callArg{{loc: f.slot(i.Dst), class: classMem}}
	}
	plain = make([]callArg, len(i.Args))
	for index, reg := range i.Args {
		plain[index] = callArg{loc: f.slot(reg), class: f.g.classOf(sig.Params[index])}
	}
	return sret, plain
}

// ---------------------------------------------------------------------------
// Runtime imports
// ---------------------------------------------------------------------------

// nativePlan says how a monomorphized gyro:rt import maps onto its C
// implementation: which symbol, whether the result goes through an out
// pointer, which arguments pass by pointer, and trailing element sizes.
type nativePlan struct {
	symbol   string
	sret     bool
	args     []planArg
	extras   []int64
	noResult bool // result ignored even though the signature has one
}

type planArg struct {
	index   int
	pointer bool
}

func values(indexes ...int) []planArg {
	out := make([]planArg, len(indexes))
	for i, index := range indexes {
		out[i] = planArg{index: index}
	}
	return out
}

func pointerTo(index int) planArg { return planArg{index: index, pointer: true} }

// splitNativeName separates Array<i32>::push into (Array, push) and
// println<i32> into ("", println), the way the evaluator dispatches.
func splitNativeName(name string) (owner, method string) {
	method = name
	if i := strings.LastIndex(name, "::"); i >= 0 {
		owner, method = name[:i], name[i+2:]
	}
	if j := strings.IndexByte(owner, '<'); j >= 0 {
		owner = owner[:j]
	}
	if j := strings.IndexByte(method, '<'); j >= 0 {
		method = method[:j]
	}
	return owner, method
}

func (g *generator) nativePlanFor(name string, sig *metals.FnType) nativePlan {
	// A foreign symbol is reached through the generated wrapper, which is
	// where C's real argument and result widths are applied, and where a
	// struct becomes the by-value argument C's ABI wants.
	if g.externSyms[name] {
		indexes := make([]int, len(sig.Params))
		for i := range sig.Params {
			indexes[i] = i
		}
		return nativePlan{
			symbol: crt.ShimSymbol(name),
			sret:   sig.Result != nil && g.classOf(sig.Result) == classMem,
			args:   values(indexes...),
		}
	}

	owner, method := splitNativeName(name)
	switch owner {
	case "":
		return g.freePlan(name, method, sig)
	}
	g.fail("no native mapping for @%s", name)
	return nativePlan{}
}

func (g *generator) freePlan(name, method string, sig *metals.FnType) nativePlan {
	switch method {
	case "print", "println", "eprint", "eprintln":
		suffix, ok := printSuffix(sig.Params[0])
		if !ok {
			g.fail("@%s: printing a %s natively needs a synthesized repr function (still open)", name, sig.Params[0])
		}
		return nativePlan{symbol: "gy_" + method + "_" + suffix, args: values(0)}
	case "set_abort_hook":
		return nativePlan{symbol: "gy_set_abort_hook", args: values(0)}
	case "text_bytes":
		return nativePlan{symbol: "gy_str_view", sret: true, args: values(0)}

	// The floor of every allocator: the process heap, and the abort an
	// allocator reaches for when a request cannot be met.
	case "raw_alloc":
		return nativePlan{symbol: "gy_raw_alloc", args: values(0, 1)}
	case "raw_free":
		return nativePlan{symbol: "gy_raw_free", args: values(0)}
	case "raw_resize":
		return nativePlan{symbol: "gy_raw_resize", args: values(0, 1, 2, 3)}
	case "copy_bytes":
		return nativePlan{symbol: "gy_copy_bytes", args: values(0, 1, 2)}
	case "panic":
		return nativePlan{symbol: "gy_panic", args: values(0)}
	case "text_seal":
		return nativePlan{symbol: "gy_text_seal", args: values(0, 1)}
	case "set_flush_hook":
		return nativePlan{symbol: "gy_set_flush_hook", args: values(0)}
	case "lock_output":
		return nativePlan{symbol: "gy_lock_output"}
	case "unlock_output":
		return nativePlan{symbol: "gy_unlock_output"}

	case "write_byte":
		return nativePlan{symbol: "gy_write_byte", args: values(0, 1)}
	case "read_byte":
		return nativePlan{symbol: "gy_read_byte"}
	}
	g.fail("no native mapping for @%s", name)
	return nativePlan{}
}

func printSuffix(t metals.Type) (string, bool) {
	prim, ok := t.(*metals.Prim)
	if !ok {
		return "", false
	}
	switch prim.Kind {
	case metals.I8, metals.I16, metals.I32, metals.I64, metals.Isize:
		return "int", true
	case metals.U8, metals.U16, metals.U32, metals.U64, metals.Usize:
		return "uint", true
	case metals.F32, metals.F64:
		return "float", true
	case metals.Bool:
		return "bool", true
	case metals.Rune:
		return "rune", true
	case metals.Str:
		return "str", true
	}
	return "", false
}

// emitNativeCall invokes a runtime import from a frame: sources are
// register slots, the destination is the dst register's slot.
//
// What the runtime holds it holds in storage representation — an Array's
// elements are the bytes a slice of it borrows — so an argument it reads
// through a pointer, and a result it writes through one, go through the
// frame's staging cell when the type's storage is not what a register
// carries.
func (f *frame) emitNativeCall(name string, sig *metals.FnType, argRegs []string, dst string) {
	g := f.g
	plan := g.nativePlanFor(name, sig)

	byPointer := map[int]bool{}
	for _, pa := range plan.args {
		byPointer[pa.index] = pa.pointer
	}
	sources := make([]callArg, len(argRegs))
	staged := 0
	for index, reg := range argRegs {
		source := callArg{loc: f.slot(reg), class: g.classOf(sig.Params[index])}
		if byPointer[index] && metals.Narrow(sig.Params[index]) {
			staged++
			if staged > 1 {
				// One cell, so two would overwrite each other. No runtime
				// signature takes two, and a new one that did would land
				// here rather than in a wrong answer.
				g.fail("@%s: @%s stages more than one narrow argument", f.fn.Name, name)
			}
			source.loc = memAt(f.elementOut(reg))
		}
		sources[index] = source
	}

	var out *callArg
	staging := false
	if plan.sret {
		if dst == "" {
			g.fail("@%s: @%s needs a destination register", f.fn.Name, name)
		}
		staging = metals.Narrow(sig.Result)
		cell := f.slot(dst)
		if staging {
			cell = f.stage()
		}
		f.zeroMem(cell, g.sizeOf(sig.Result))
		out = &callArg{loc: cell, class: classMem}
	}
	g.emitNativeInvoke(plan, sig, sources, out)
	switch {
	case staging:
		f.elementBack(dst)
	case !plan.sret:
		f.storeCallResult(sig.Result, dst)
	}
}

// stageThunkArgs narrows the arguments a plan passes by pointer, since
// the argument buffer a thunk reads holds register representation and
// the runtime reads storage. The thunk's own third word is the cell,
// which is why no plan may pass two of them by pointer.
func (g *generator) stageThunkArgs(plan nativePlan, sig *metals.FnType, args []callArg) {
	cell := memLoc{"rbp", -24}
	staged := false
	for _, pa := range plan.args {
		if !pa.pointer || !metals.Narrow(sig.Params[pa.index]) {
			continue
		}
		if staged {
			g.fail("@%s stages more than one narrow argument", plan.symbol)
		}
		staged = true
		g.storeMember(memAt(cell), args[pa.index].loc, sig.Params[pa.index])
		args[pa.index].loc = memAt(cell)
	}
}

// emitNativeInvoke assembles the C call from a plan. sources[i] must
// address METALS argument i; out is the result cell for sret plans
// (classMem to lea a frame slot, classInt to load a stored pointer).
func (g *generator) emitNativeInvoke(plan nativePlan, sig *metals.FnType, sources []callArg, out *callArg) {
	var args []callArg
	if plan.sret {
		if out == nil {
			g.fail("@%s: missing result cell", plan.symbol)
		}
		args = append(args, *out)
	}
	for _, pa := range plan.args {
		source := sources[pa.index]
		if pa.pointer {
			source.class = classMem
		}
		args = append(args, source)
	}
	for _, extra := range plan.extras {
		args = append(args, callArg{imm: extra, isImm: true})
	}
	g.emitSysVCall(g.extern(plan.symbol), args)
}
