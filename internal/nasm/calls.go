package nasm

import (
	"fmt"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// callArg is one argument of a System V call: a value loaded from
// memory, a pointer to memory (classMem), or an immediate.
type callArg struct {
	loc   memLoc
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
		g.ins("lea %s, %s", register, arg.loc)
	default:
		g.ins("mov %s, %s", register, arg.loc)
	}
}

// ---------------------------------------------------------------------------
// Direct calls
// ---------------------------------------------------------------------------

func (f *frame) emitCall(i *metals.Call) {
	g := f.g
	if callee := g.funcs[i.Callee]; callee != nil {
		sig := callee.Sig()
		if sig.Async {
			f.emitAsyncCall(i.Callee, sig, i.Args, i.Dst)
			return
		}
		args := f.userCallArgs(sig, i.Args, i.Dst)
		g.emitSysVCall(g.symbolFor(i.Callee), args)
		f.storeCallResult(sig.Result, i.Dst)
		return
	}
	if sig := g.imports[i.Callee]; sig != nil {
		if sig.Async {
			f.emitAsyncCall(i.Callee, sig, i.Args, i.Dst)
			return
		}
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
	sig, ok := f.typeOf(i.Fn).(*metals.FnType)
	if !ok {
		g.fail("@%s: call.indirect through a %s", f.fn.Name, f.typeOf(i.Fn))
	}
	if sig.Async {
		f.emitAsyncCallIndirect(i.Fn, sig, i.Args, i.Dst)
		return
	}

	var sret []callArg
	if sig.Result != nil && g.classOf(sig.Result) == classMem {
		if i.Dst == "" {
			g.fail("@%s: an aggregate result needs a destination", f.fn.Name)
		}
		sret = []callArg{{loc: f.slot(i.Dst), class: classMem}}
	}
	plain := make([]callArg, len(i.Args))
	for index, reg := range i.Args {
		plain[index] = callArg{loc: f.slot(reg), class: g.classOf(sig.Params[index])}
	}
	env := callArg{loc: f.slot(i.Fn).plus(8), class: classInt}

	bare := g.newLabel()
	done := g.newLabel()
	g.ins("mov r11, %s", f.slot(i.Fn))
	g.ins("mov rax, %s", f.slot(i.Fn).plus(8))
	g.ins("test rax, rax")
	g.ins("jz %s", bare)
	g.emitSysVCall("r11", append(append(append([]callArg{}, sret...), env), plain...))
	g.ins("jmp %s", done)
	g.label(bare)
	g.emitSysVCall("r11", append(append([]callArg{}, sret...), plain...))
	g.label(done)
	f.storeCallResult(sig.Result, i.Dst)
}

// ---------------------------------------------------------------------------
// Async calls
// ---------------------------------------------------------------------------

// emitAsyncCall starts the callee eagerly on a runtime thread: the
// arguments are packed into a heap buffer, and a generated thunk
// unpacks them on the other side. The future's result cell is sized
// here, where the concrete result type is known.
func (f *frame) emitAsyncCall(callee string, sig *metals.FnType, argRegs []string, dst string) {
	g := f.g
	scratch := memLoc{"rbp", scratchSlot}

	bufferSize := 8
	for _, param := range sig.Params {
		bufferSize += g.sizeOf(param)
	}
	g.ins("mov rdi, %d", bufferSize)
	g.ins("call %s", g.extern("gy_alloc"))
	g.ins("mov %s, rax", scratch)

	offset := 0
	for index, reg := range argRegs {
		g.ins("mov rdx, %s", scratch)
		f.copyMem(memLoc{base: "rdx", off: offset}, f.slot(reg), g.sizeOf(sig.Params[index]))
		offset += g.sizeOf(sig.Params[index])
	}

	resultSize := 0
	if sig.Result != nil {
		resultSize = g.sizeOf(sig.Result)
	}
	g.ins("lea rdi, [%s]", g.thunkFor(callee))
	g.ins("mov rsi, %s", scratch)
	g.ins("mov rdx, %d", resultSize)
	g.ins("call %s", g.extern("gy_future_start"))
	if dst != "" {
		g.ins("mov %s, rax", f.slot(dst))
	}
}

// emitAsyncCallIndirect starts a call through a function value. The value
// itself rides in the argument buffer ahead of the arguments, since there
// is no callee symbol to key a thunk on: one thunk per signature serves
// every function value of that shape.
func (f *frame) emitAsyncCallIndirect(fnReg string, sig *metals.FnType, argRegs []string, dst string) {
	g := f.g
	scratch := memLoc{"rbp", scratchSlot}

	bufferSize := 2 * wordSize
	for _, param := range sig.Params {
		bufferSize += g.sizeOf(param)
	}
	g.ins("mov rdi, %d", bufferSize)
	g.ins("call %s", g.extern("gy_alloc"))
	g.ins("mov %s, rax", scratch)

	g.ins("mov rdx, %s", scratch)
	f.copyMem(memLoc{base: "rdx"}, f.slot(fnReg), 2*wordSize)

	offset := 2 * wordSize
	for index, reg := range argRegs {
		g.ins("mov rdx, %s", scratch)
		f.copyMem(memLoc{base: "rdx", off: offset}, f.slot(reg), g.sizeOf(sig.Params[index]))
		offset += g.sizeOf(sig.Params[index])
	}

	resultSize := 0
	if sig.Result != nil {
		resultSize = g.sizeOf(sig.Result)
	}
	g.ins("lea rdi, [%s]", g.indirectThunkFor(sig))
	g.ins("mov rsi, %s", scratch)
	g.ins("mov rdx, %d", resultSize)
	g.ins("call %s", g.extern("gy_future_start"))
	if dst != "" {
		g.ins("mov %s, rax", f.slot(dst))
	}
}

// indirectThunkFor returns the thunk that unpacks a function value and
// the arguments behind it. Keyed on the signature, since that is all the
// thunk depends on.
func (g *generator) indirectThunkFor(sig *metals.FnType) string {
	key := sig.String()
	if symbol, ok := g.indirectThunks[key]; ok {
		return symbol
	}
	symbol := fmt.Sprintf("TI_%d", len(g.indirectThunks))
	g.indirectThunks[key] = symbol
	g.pending = append(g.pending, func() { g.emitIndirectThunk(symbol, sig) })
	return symbol
}

func (g *generator) emitIndirectThunk(symbol string, sig *metals.FnType) {
	g.text.WriteString("\n")
	g.comment("async thunk for a %s value", sig)
	g.label(symbol)
	g.ins("push rbp")
	g.ins("mov rbp, rsp")
	g.ins("sub rsp, 16")
	g.ins("mov [rbp-8], rdi")  // argument buffer
	g.ins("mov [rbp-16], rsi") // result cell
	g.ins("mov r10, [rbp-8]")

	plain := make([]callArg, len(sig.Params))
	offset := 2 * wordSize
	for index, param := range sig.Params {
		plain[index] = callArg{loc: memLoc{base: "r10", off: offset}, class: g.classOf(param)}
		offset += g.sizeOf(param)
	}

	var sret []callArg
	if sig.Result != nil && g.classOf(sig.Result) == classMem {
		sret = []callArg{{loc: memLoc{"rbp", -16}, class: classInt}}
	}

	// The environment pointer is the closure's; a bare function has none
	// and takes its arguments unshifted.
	env := callArg{loc: memLoc{base: "r10", off: 8}, class: classInt}
	bare := g.newLabel()
	done := g.newLabel()
	g.ins("mov r11, [r10]")
	g.ins("mov rax, [r10+8]")
	g.ins("test rax, rax")
	g.ins("jz %s", bare)
	g.emitSysVCall("r11", append(append(append([]callArg{}, sret...), env), plain...))
	g.ins("jmp %s", done)
	g.label(bare)
	g.emitSysVCall("r11", append(append([]callArg{}, sret...), plain...))
	g.label(done)

	if sig.Result != nil && g.classOf(sig.Result) != classMem {
		g.ins("mov rcx, [rbp-16]")
		if g.classOf(sig.Result) == classSSE {
			g.ins("movsd [rcx], xmm0")
		} else {
			g.ins("mov [rcx], rax")
		}
	}
	g.ins("leave")
	g.ins("ret")
}

// thunkFor returns the symbol of the callee's async thunk, emitting it
// once: fn(argbuf, out) unpacking the buffer and storing the result.
func (g *generator) thunkFor(callee string) string {
	if symbol, ok := g.thunks[callee]; ok {
		return symbol
	}
	symbol := fmt.Sprintf("T_%d", len(g.thunks))
	g.thunks[callee] = symbol
	g.pending = append(g.pending, func() { g.emitThunk(symbol, callee) })
	return symbol
}

func (g *generator) emitThunk(symbol, callee string) {
	var sig *metals.FnType
	if fn := g.funcs[callee]; fn != nil {
		sig = fn.Sig()
	} else {
		sig = g.imports[callee]
	}

	g.text.WriteString("\n")
	g.comment("async thunk for @%s", callee)
	g.label(symbol)
	g.ins("push rbp")
	g.ins("mov rbp, rsp")
	g.ins("sub rsp, 16")
	g.ins("mov [rbp-8], rdi")  // argument buffer
	g.ins("mov [rbp-16], rsi") // result cell
	g.ins("mov r10, [rbp-8]")

	bufferArgs := make([]callArg, len(sig.Params))
	offset := 0
	for index, param := range sig.Params {
		bufferArgs[index] = callArg{loc: memLoc{base: "r10", off: offset}, class: g.classOf(param)}
		offset += g.sizeOf(param)
	}
	outPointer := callArg{loc: memLoc{"rbp", -16}, class: classInt}

	if fn := g.funcs[callee]; fn != nil {
		args := bufferArgs
		if sig.Result != nil && g.classOf(sig.Result) == classMem {
			args = append([]callArg{outPointer}, args...)
		}
		g.emitSysVCall(g.symbolFor(callee), args)
	} else {
		plan := g.nativePlanFor(callee, sig)
		g.emitNativeInvoke(plan, sig, bufferArgs, &outPointer)
	}

	if sig.Result != nil && g.classOf(sig.Result) != classMem {
		g.ins("mov rcx, [rbp-16]")
		if g.classOf(sig.Result) == classSSE {
			g.ins("movsd [rcx], xmm0")
		} else {
			g.ins("mov [rcx], rax")
		}
	}
	g.ins("leave")
	g.ins("ret")
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
	owner, method := splitNativeName(name)
	switch owner {
	case "":
		return g.freePlan(name, method, sig)
	case "Array":
		return g.arrayPlan(name, method, sig)
	case "Mutex", "MutexGuard":
		return g.mutexPlan(name, method, sig)
	case "File":
		if method == "close" {
			return nativePlan{symbol: "gy_file_close", args: values(0)}
		}
	case "string":
		switch method {
		case "compare":
			return nativePlan{symbol: "gy_str_compare", args: values(0, 1)}
		case "runes":
			return nativePlan{symbol: "gy_str_runes", sret: true, args: values(0)}
		case "bytes":
			return nativePlan{symbol: "gy_str_bytes", sret: true, args: values(0)}
		}
	}
	g.fail("no native mapping for @%s", name)
	return nativePlan{}
}

func (g *generator) freePlan(name, method string, sig *metals.FnType) nativePlan {
	switch method {
	case "print", "println", "eprintln":
		suffix, ok := printSuffix(sig.Params[0])
		if !ok {
			g.fail("@%s: printing a %s natively needs a synthesized repr function (still open)", name, sig.Params[0])
		}
		return nativePlan{symbol: "gy_" + method + "_" + suffix, args: values(0)}
	case "str":
		suffix, ok := printSuffix(sig.Params[0])
		if !ok {
			g.fail("@%s: formatting a %s natively needs a synthesized repr function (still open)", name, sig.Params[0])
		}
		return nativePlan{symbol: "gy_str_" + suffix, args: values(0)}
	case "hash":
		return nativePlan{symbol: "gy_hash_" + g.hashSuffix(name, sig.Params[0]), args: values(0)}
	case "spawn":
		return nativePlan{symbol: "gy_spawn", args: values(0)}
	case "from_utf8":
		return nativePlan{symbol: "gy_from_utf8", args: []planArg{pointerTo(0)}}
	case "to_i32":
		return nativePlan{symbol: "gy_to_i32", sret: true, args: values(0)}
	case "to_f64":
		return nativePlan{symbol: "gy_to_f64", sret: true, args: values(0)}
	case "open":
		return nativePlan{symbol: "gy_open", sret: true, args: values(0)}
	case "read_all":
		return nativePlan{symbol: "gy_read_all", sret: true, args: values(0)}
	case "write_file":
		return nativePlan{symbol: "gy_write_file", sret: true, args: values(0, 1)}
	case "append_file":
		return nativePlan{symbol: "gy_append_file", sret: true, args: values(0, 1)}
	case "remove":
		return nativePlan{symbol: "gy_remove", sret: true, args: values(0)}
	case "read_dir":
		return nativePlan{symbol: "gy_read_dir", sret: true, args: values(0)}
	case "exists":
		return nativePlan{symbol: "gy_exists", args: values(0)}
	case "get":
		return nativePlan{symbol: "gy_get", sret: true, args: values(0)}
	case "now":
		return nativePlan{symbol: "gy_now"}
	case "monotonic":
		return nativePlan{symbol: "gy_monotonic"}
	case "sleep":
		return nativePlan{symbol: "gy_sleep", args: values(0)}
	}
	g.fail("no native mapping for @%s", name)
	return nativePlan{}
}

func (g *generator) arrayPlan(name, method string, sig *metals.FnType) nativePlan {
	switch method {
	case "new", "with_capacity":
		return nativePlan{symbol: "gy_array_new"}
	case "push":
		return nativePlan{
			symbol: "gy_array_push",
			args:   []planArg{{index: 0}, pointerTo(1)},
			extras: []int64{int64(g.sizeOf(sig.Params[1]))},
		}
	case "pop":
		tuple := sig.Result.(*metals.TupleOf)
		return nativePlan{
			symbol: "gy_array_pop", sret: true, args: values(0),
			extras: []int64{int64(g.sizeOf(tuple.Elems[0]))},
		}
	case "get":
		return nativePlan{
			symbol: "gy_array_get", sret: true, args: values(0, 1),
			extras: []int64{int64(g.sizeOf(sig.Result))},
		}
	case "set":
		return nativePlan{
			symbol: "gy_array_set",
			args:   []planArg{{index: 0}, {index: 1}, pointerTo(2)},
			extras: []int64{int64(g.sizeOf(sig.Params[2]))},
		}
	case "len":
		return nativePlan{symbol: "gy_array_len", args: values(0)}
	case "slice":
		return nativePlan{symbol: "gy_array_slice", sret: true, args: values(0)}
	case "free":
		return nativePlan{symbol: "gy_array_free", args: values(0)}
	}
	g.fail("no native mapping for @%s", name)
	return nativePlan{}
}

func (g *generator) mutexPlan(name, method string, sig *metals.FnType) nativePlan {
	switch method {
	case "new":
		return nativePlan{
			symbol: "gy_mutex_new", args: []planArg{pointerTo(0)},
			extras: []int64{int64(g.sizeOf(sig.Params[0]))},
		}
	case "lock":
		return nativePlan{symbol: "gy_mutex_lock", args: values(0)}
	case "get":
		return nativePlan{symbol: "gy_guard_get", sret: true, args: values(0)}
	case "set":
		return nativePlan{symbol: "gy_guard_set", args: []planArg{{index: 0}, pointerTo(1)}}
	case "unlock":
		return nativePlan{symbol: "gy_guard_unlock", args: values(0)}
	}
	g.fail("no native mapping for @%s", name)
	return nativePlan{}
}

// hashSuffix picks the entry point behind hash<T>. A rune hashes as the
// signed integer it is, and the two float widths share one, since an f32
// is already carried as a double.
func (g *generator) hashSuffix(name string, t metals.Type) string {
	prim, ok := t.(*metals.Prim)
	if ok {
		switch prim.Kind {
		case metals.I8, metals.I16, metals.I32, metals.I64, metals.Rune:
			return "int"
		case metals.U8, metals.U16, metals.U32, metals.U64, metals.Usize:
			return "uint"
		case metals.F32, metals.F64:
			return "float"
		case metals.Str:
			return "str"
		}
	}
	g.fail("@%s: no native hash for %s", name, t)
	return ""
}

func printSuffix(t metals.Type) (string, bool) {
	prim, ok := t.(*metals.Prim)
	if !ok {
		return "", false
	}
	switch prim.Kind {
	case metals.I8, metals.I16, metals.I32, metals.I64:
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
func (f *frame) emitNativeCall(name string, sig *metals.FnType, argRegs []string, dst string) {
	g := f.g
	plan := g.nativePlanFor(name, sig)

	sources := make([]callArg, len(argRegs))
	for index, reg := range argRegs {
		sources[index] = callArg{loc: f.slot(reg), class: g.classOf(sig.Params[index])}
	}

	var out *callArg
	if plan.sret {
		if dst == "" {
			g.fail("@%s: @%s needs a destination register", f.fn.Name, name)
		}
		f.zeroMem(f.slot(dst), g.sizeOf(sig.Result))
		out = &callArg{loc: f.slot(dst), class: classMem}
	}
	g.emitNativeInvoke(plan, sig, sources, out)
	if !plan.sret {
		f.storeCallResult(sig.Result, dst)
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
