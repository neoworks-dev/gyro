package cgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// byPointer reports whether a value reaches the C runtime as a pointer
// to its storage rather than as a machine word. It is the same split the
// nasm backend calls classMem: runtime signatures only ever take scalars
// and pointers, so everything wider than a word travels by address.
func byPointer(t metals.Type) bool {
	switch t.(type) {
	case *metals.Prim, *metals.Ptr, *metals.ChanOf, *metals.FutureOf, *metals.Handle:
		return false
	}
	return true
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
		f.finish(sig.Result, i.Dst, g.symbolFor(i.Callee)+"("+f.argList(i.Args)+")")
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

func (f *frame) argList(regs []string) string {
	parts := make([]string, len(regs))
	for index, reg := range regs {
		parts[index] = f.reg(reg)
	}
	return strings.Join(parts, ", ")
}

// finish stores a call's result, or discards it when the METALS call has
// no destination.
func (f *frame) finish(result metals.Type, dst, call string) {
	switch {
	case result == nil:
		f.line("%s;", call)
	case dst == "":
		f.line("(void)%s;", call)
	default:
		f.assign(dst, call)
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

	value := f.reg(i.Fn)
	plain := f.argList(i.Args)
	withEnv := value + ".env"
	if plain != "" {
		withEnv += ", " + plain
	}

	// A closure's environment is an ordinary first parameter; a bare
	// fn.ref has none, so the call is emitted both ways and chosen at
	// runtime on the env word.
	f.line("if (%s.env != 0) {", value)
	f.indent++
	f.finish(sig.Result, i.Dst, fmt.Sprintf("((%s)%s.code)(%s)", g.fnPointerType(sig, true), value, withEnv))
	f.indent--
	f.line("} else {")
	f.indent++
	f.finish(sig.Result, i.Dst, fmt.Sprintf("((%s)%s.code)(%s)", g.fnPointerType(sig, false), value, plain))
	f.indent--
	f.line("}")
}

// ---------------------------------------------------------------------------
// Async calls
// ---------------------------------------------------------------------------

// emitAsyncCall starts the callee eagerly on a runtime thread: the
// arguments are packed into a heap buffer shaped by a generated struct,
// and a thunk unpacks them on the other side. The future's result cell
// is sized here, where the concrete result type is known.
func (f *frame) emitAsyncCall(callee string, sig *metals.FnType, argRegs []string, dst string) {
	g := f.g
	thunk, argsType := g.thunkFor(callee, sig)
	f.packAndStart(thunk, argsType, "", argRegs, sig, dst)
}

// emitAsyncCallIndirect starts a call through a function value. The value
// rides in the argument buffer ahead of the arguments, since there is no
// callee symbol to key a thunk on: one thunk per signature serves every
// function value of that shape.
func (f *frame) emitAsyncCallIndirect(fnReg string, sig *metals.FnType, argRegs []string, dst string) {
	g := f.g
	thunk, argsType := g.indirectThunkFor(sig)
	f.packAndStart(thunk, argsType, fnReg, argRegs, sig, dst)
}

func (f *frame) packAndStart(thunk, argsType, fnReg string, argRegs []string, sig *metals.FnType, dst string) {
	g := f.g
	resultSize := 0
	if sig.Result != nil {
		resultSize = g.sizeOf(sig.Result)
	}
	f.line("{")
	f.indent++
	f.line("%s *packed = gy_alloc(sizeof(%s));", argsType, argsType)
	if fnReg != "" {
		f.line("packed->fn = %s;", f.reg(fnReg))
	}
	for index, reg := range argRegs {
		f.line("packed->a%d = %s;", index, f.reg(reg))
	}
	start := fmt.Sprintf("gy_future_start(%s, packed, %d)", thunk, resultSize)
	if dst == "" {
		f.line("(void)%s;", start)
	} else {
		f.assign(dst, start)
	}
	f.indent--
	f.line("}")
}

// argsStruct interns the buffer layout an async call packs into. A
// signature with no parameters still needs a member: C has no empty
// struct.
func (g *generator) argsStruct(name string, sig *metals.FnType, withFn bool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "\n/* argument buffer for %s */\ntypedef struct {", comment(sig.String()))
	if withFn {
		out.WriteString(" gy_fn fn;")
	}
	for index, param := range sig.Params {
		fmt.Fprintf(&out, " %s a%d;", g.cType(param), index)
	}
	if !withFn && len(sig.Params) == 0 {
		out.WriteString(" int64_t unused;")
	}
	fmt.Fprintf(&out, " } %s;\n", name)
	g.typedefs.WriteString(out.String())
	return name
}

// thunkFor returns the thunk that runs @callee on the worker thread, and
// the type of the buffer it unpacks.
func (g *generator) thunkFor(callee string, sig *metals.FnType) (string, string) {
	if name, ok := g.thunks[callee]; ok {
		return name, name + "_args"
	}
	name := fmt.Sprintf("TH%d", len(g.thunks))
	g.thunks[callee] = name
	argsType := g.argsStruct(name+"_args", sig, false)
	fmt.Fprintf(&g.decls, "static void %s(void *argbuf, void *out);\n", name)
	g.pending = append(g.pending, func() { g.emitThunk(name, argsType, callee, sig) })
	return name, argsType
}

func (g *generator) emitThunk(name, argsType, callee string, sig *metals.FnType) {
	g.raw("\n/* async thunk for @%s */", comment(callee))
	g.raw("static void %s(void *argbuf, void *out) {", name)
	g.line("%s *packed = (%s *)argbuf;", argsType, argsType)
	g.line("(void)packed; (void)out;")

	sources := make([]string, len(sig.Params))
	for index := range sig.Params {
		sources[index] = fmt.Sprintf("packed->a%d", index)
	}

	if fn := g.funcs[callee]; fn != nil {
		g.storeThunkResult(sig, g.symbolFor(callee)+"("+strings.Join(sources, ", ")+")")
	} else {
		plan := g.nativePlanFor(callee, sig)
		call := g.nativeCall(plan, sig, sources, "out")
		if plan.sret {
			g.line("%s;", call)
		} else {
			g.storeThunkResult(sig, call)
		}
	}
	g.raw("}")
}

func (g *generator) storeThunkResult(sig *metals.FnType, call string) {
	if sig.Result == nil {
		g.line("%s;", call)
		return
	}
	g.line("*(%s *)out = %s;", g.cType(sig.Result), call)
}

// indirectThunkFor is keyed on the signature, since that is all the thunk
// depends on.
func (g *generator) indirectThunkFor(sig *metals.FnType) (string, string) {
	key := sig.String()
	if name, ok := g.indirectThunks[key]; ok {
		return name, name + "_args"
	}
	name := fmt.Sprintf("TI%d", len(g.indirectThunks))
	g.indirectThunks[key] = name
	argsType := g.argsStruct(name+"_args", sig, true)
	fmt.Fprintf(&g.decls, "static void %s(void *argbuf, void *out);\n", name)
	g.pending = append(g.pending, func() { g.emitIndirectThunk(name, argsType, sig) })
	return name, argsType
}

func (g *generator) emitIndirectThunk(name, argsType string, sig *metals.FnType) {
	g.raw("\n/* async thunk for a %s value */", comment(sig.String()))
	g.raw("static void %s(void *argbuf, void *out) {", name)
	g.line("%s *packed = (%s *)argbuf;", argsType, argsType)
	g.line("(void)out;")

	plain := make([]string, len(sig.Params))
	for index := range sig.Params {
		plain[index] = fmt.Sprintf("packed->a%d", index)
	}
	withEnv := append([]string{"packed->fn.env"}, plain...)

	g.line("if (packed->fn.env != 0) {")
	g.storeThunkResult(sig, fmt.Sprintf("((%s)packed->fn.code)(%s)",
		g.fnPointerType(sig, true), strings.Join(withEnv, ", ")))
	g.line("} else {")
	g.storeThunkResult(sig, fmt.Sprintf("((%s)packed->fn.code)(%s)",
		g.fnPointerType(sig, false), strings.Join(plain, ", ")))
	g.line("}")
	g.raw("}")
}

// ---------------------------------------------------------------------------
// Runtime imports
// ---------------------------------------------------------------------------

// nativePlan says how a monomorphized gyro:rt import maps onto its C
// implementation: which symbol, whether the result goes through an out
// pointer, which arguments pass by pointer, and trailing element sizes.
// It is the nasm backend's table, re-read against C's call syntax rather
// than System V's registers.
type nativePlan struct {
	symbol string
	sret   bool
	args   []planArg
	extras []int64
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

// nativeCall assembles the C call from a plan. sources[i] is an lvalue
// naming METALS argument i; out is the result cell for sret plans.
func (g *generator) nativeCall(plan nativePlan, sig *metals.FnType, sources []string, out string) string {
	var args []string
	if plan.sret {
		if out == "" {
			g.fail("@%s needs a result cell", plan.symbol)
		}
		args = append(args, out)
	}
	for _, pa := range plan.args {
		source := sources[pa.index]
		if pa.pointer || byPointer(sig.Params[pa.index]) {
			source = "(void *)&" + source
		}
		args = append(args, source)
	}
	for _, extra := range plan.extras {
		args = append(args, strconv.FormatInt(extra, 10))
	}
	return plan.symbol + "(" + strings.Join(args, ", ") + ")"
}

// emitNativeCall invokes a runtime import from a frame: the sources are
// register locals, the destination is the dst register.
func (f *frame) emitNativeCall(name string, sig *metals.FnType, argRegs []string, dst string) {
	g := f.g
	plan := g.nativePlanFor(name, sig)

	sources := make([]string, len(argRegs))
	for index, reg := range argRegs {
		sources[index] = f.reg(reg)
	}

	out := ""
	if plan.sret {
		if dst == "" {
			g.fail("@%s: @%s needs a destination register", f.fn.Name, name)
		}
		f.line("memset(&%s, 0, %d);", f.reg(dst), g.sizeOf(sig.Result))
		out = "(void *)&" + f.reg(dst)
	}
	call := g.nativeCall(plan, sig, sources, out)
	if plan.sret {
		f.line("%s;", call)
		return
	}
	f.finish(sig.Result, dst, call)
}

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
	case "get":
		return nativePlan{symbol: "gy_get", sret: true, args: values(0)}
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
	if prim, ok := t.(*metals.Prim); ok {
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
