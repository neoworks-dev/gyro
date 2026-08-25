package nasm

import (
	"fmt"

	"github.com/gyro-lang/gyro/internal/metals"
)

// An `any` is two words: a descriptor and a payload. The descriptors are
// rodata, one per distinct type the program wraps or tests, so a shape
// test is a pointer comparison against a known address and wrapping a
// scalar allocates nothing.
//
// The operations themselves live in rt.c, which both backends link; what
// has to be emitted here is the static data and the per-signature
// trampoline that applies a wrapped function, because only the code
// generator knows a signature's calling convention.

// anyType interns a type's descriptor and returns its label.
func (g *generator) anyType(t metals.Type) string {
	key := t.String()
	if label, ok := g.anyTypes[key]; ok {
		return label
	}
	if g.buildingAny[key] {
		g.fail("type %s contains itself, so it has no any descriptor", key)
	}
	g.buildingAny[key] = true

	g.anyN++
	label := fmt.Sprintf("TY_%d", g.anyN)

	// Members intern first, so every label a descriptor mentions is
	// already defined by the time the assembler reaches it.
	fields, count := g.anyFields(t, label)
	apply := g.anyApply(t, label)
	name := g.cstrLabel(key)

	delete(g.buildingAny, key)
	g.anyTypes[key] = label

	boxed := 0
	if g.anyBoxed(t) {
		boxed = 1
	}
	g.data.WriteString("\n" + label + ":\n")
	fmt.Fprintf(&g.data, "\tdq %s ; name\n", name)
	fmt.Fprintf(&g.data, "\tdq %d ; size\n", g.sizeOf(t))
	fmt.Fprintf(&g.data, "\tdq %d ; boxed\n", boxed)
	fmt.Fprintf(&g.data, "\tdq %s ; fields\n", fields)
	fmt.Fprintf(&g.data, "\tdq %d ; nfields\n", count)
	fmt.Fprintf(&g.data, "\tdq %s ; apply\n", apply)
	return label
}

// anyBoxed decides whether the payload holds the value or points at it.
// A record is always boxed however narrow it is, so that any.set through
// one reference is visible through the rest; the evaluator shares the
// same way, and the tiers have to agree.
func (g *generator) anyBoxed(t metals.Type) bool {
	if g.sizeOf(t) > wordSize {
		return true
	}
	named, ok := t.(*metals.Named)
	if !ok {
		return false
	}
	decl := g.types[named.Name]
	return decl != nil && decl.Record != nil
}

// anyFields emits the field table any.get and any.set walk. A field that
// is itself an any carries a null descriptor: it is handed out and taken
// back whole, with no boxing either way.
func (g *generator) anyFields(t metals.Type, owner string) (string, int) {
	named, ok := t.(*metals.Named)
	if !ok {
		return "0", 0
	}
	decl := g.types[named.Name]
	if decl == nil || decl.Record == nil {
		return "0", 0
	}

	type entry struct {
		name       string
		offset     int
		descriptor string
	}
	entries := make([]entry, len(decl.Record.Fields))
	for i, field := range decl.Record.Fields {
		descriptor := "0"
		if _, isAny := field.Type.(*metals.Any); !isAny {
			descriptor = g.anyType(field.Type)
		}
		entries[i] = entry{g.cstrLabel(field.Name), g.fieldOffset(decl.Record, i), descriptor}
	}

	label := owner + "_F"
	g.data.WriteString("\n" + label + ":\n")
	for _, e := range entries {
		fmt.Fprintf(&g.data, "\tdq %s, %d, %s\n", e.name, e.offset, e.descriptor)
	}
	return label, len(entries)
}

// cstrLabel interns a NUL-terminated C string. This is not a gy_str: the
// runtime's descriptor fields are plain char pointers, because they are
// compared and printed by C.
func (g *generator) cstrLabel(text string) string {
	if label, ok := g.cstrs[text]; ok {
		return label
	}
	label := fmt.Sprintf("CS_%d", len(g.cstrs))
	g.cstrs[text] = label

	g.data.WriteString(label + ":\n\tdb ")
	for i := 0; i < len(text); i++ {
		fmt.Fprintf(&g.data, "%d, ", text[i])
	}
	g.data.WriteString("0\n")
	return label
}

// ---------------------------------------------------------------------------
// The apply trampoline
// ---------------------------------------------------------------------------

// anyApply emits the trampoline the descriptor of a function type points
// at. It unwraps each argument to the type the callee declared, calls it
// through the ordinary convention, and boxes the result — which is why it
// has to be generated per signature rather than written once in C.
//
//	void apply(gy_any *out, gy_fn fn, const gy_any *args, int64_t n)
//	           rdi          rsi:rdx   rcx                 r8
func (g *generator) anyApply(t metals.Type, owner string) string {
	sig, ok := t.(*metals.FnType)
	if !ok {
		return "0"
	}
	if sig.Async {
		g.fail("any.call of %s: an async function has no result an any can hold yet", sig)
	}
	if sig.Result == nil {
		g.fail("any.call of %s, which returns nothing an any could hold", sig)
	}

	// The descriptors the trampoline names are interned now, before it is
	// queued, so the data is in place whenever the body is emitted.
	resultType := g.anyType(sig.Result)
	paramTypes := make([]string, len(sig.Params))
	for i, param := range sig.Params {
		paramTypes[i] = g.anyType(param)
	}

	symbol := fmt.Sprintf("%s_A", owner)
	g.pending = append(g.pending, func() {
		g.emitAnyApply(symbol, owner, sig, resultType, paramTypes)
	})
	return symbol
}

func (g *generator) emitAnyApply(symbol, owner string, sig *metals.FnType, resultType string, paramTypes []string) {
	// The four incoming words sit at the top of the frame, then the
	// unwrapped arguments, then the result. An argument gets two cells
	// when its storage is not what a register carries: gy_any_read writes
	// storage, and the call takes a register's worth.
	const fixed = 32
	offset := fixed
	readAt := make([]int, len(sig.Params))
	paramAt := make([]int, len(sig.Params))
	for i, param := range sig.Params {
		offset += words(g.sizeOf(param))
		readAt[i] = -offset
		paramAt[i] = readAt[i]
		if metals.Narrow(param) {
			offset += wordSize
			paramAt[i] = -offset
		}
	}
	offset += words(g.sizeOf(sig.Result))
	resultAt := -offset
	size := (offset + 15) / 16 * 16

	g.text.WriteString("\n")
	g.comment("any.call trampoline for a %s value", sig)
	g.label(symbol)
	g.ins("push rbp")
	g.ins("mov rbp, rsp")
	g.ins("sub rsp, %d", size)
	g.ins("mov [rbp-8], rdi")  // out
	g.ins("mov [rbp-16], rsi") // fn.code
	g.ins("mov [rbp-24], rdx") // fn.env
	g.ins("mov [rbp-32], rcx") // args

	// Arity is the callee's own business: the caller passed whatever it
	// had, and only the descriptor knows how many there should be.
	ok := g.newLabel()
	g.ins("cmp r8, %d", len(sig.Params))
	g.ins("je %s", ok)
	g.ins("lea rdi, [%s]", owner)
	g.ins("mov rsi, r8")
	g.ins("call %s", g.extern("gy_any_arity"))
	g.label(ok)

	for i, param := range sig.Params {
		g.ins("lea rdi, [rbp%+d]", readAt[i])
		g.ins("mov rax, [rbp-32]")
		g.ins("lea rsi, [rax+%d]", i*2*wordSize)
		g.ins("lea rdx, [%s]", paramTypes[i])
		g.ins("call %s", g.extern("gy_any_read"))
		if metals.Narrow(param) {
			g.loadMember(memAt(memLoc{"rbp", paramAt[i]}), memAt(memLoc{"rbp", readAt[i]}), param)
		}
	}

	plain := make([]callArg, len(sig.Params))
	for i, param := range sig.Params {
		plain[i] = callArg{loc: memAt(memLoc{"rbp", paramAt[i]}), class: g.classOf(param)}
	}
	var sret []callArg
	if g.classOf(sig.Result) == classMem {
		sret = []callArg{{loc: memAt(memLoc{"rbp", resultAt}), class: classInt}}
	}

	// A closure takes its environment as a leading parameter and a bare
	// fn.ref does not, so the call is emitted both ways and chosen on the
	// env word — the same branch an ordinary indirect call makes.
	bare := g.newLabel()
	done := g.newLabel()
	env := callArg{loc: memAt(memLoc{"rbp", -24}), class: classInt}
	g.ins("mov r11, [rbp-16]")
	g.ins("mov rax, [rbp-24]")
	g.ins("test rax, rax")
	g.ins("jz %s", bare)
	g.emitSysVCall("r11", append(append(append([]callArg{}, sret...), env), plain...))
	g.ins("jmp %s", done)
	g.label(bare)
	g.emitSysVCall("r11", append(append([]callArg{}, sret...), plain...))
	g.label(done)

	if g.classOf(sig.Result) != classMem {
		// The result cell is what gy_any_box copies, so it holds storage.
		g.storeReturn(memLoc{"rbp", resultAt}, sig.Result)
	}

	g.ins("lea rdi, [%s]", resultType)
	g.ins("lea rsi, [rbp%+d]", resultAt)
	g.ins("call %s", g.extern("gy_any_box"))
	g.ins("mov rcx, [rbp-8]")
	g.ins("mov [rcx], rax")
	g.ins("mov [rcx+8], rdx")
	g.ins("leave")
	g.ins("ret")
}

// ---------------------------------------------------------------------------
// The six instructions
// ---------------------------------------------------------------------------

func (f *frame) emitAnyWrap(i *metals.AnyWrap) {
	g := f.g
	held := f.typeOf(i.X)
	// Anys do not nest, the way optionals do not.
	if _, already := held.(*metals.Any); already {
		f.copyMem(f.slot(i.Dst), f.slot(i.X), 2*wordSize)
		return
	}
	// An any holds what it holds in storage representation, the same as a
	// record's field does — which is what lets any.get hand a field out by
	// copying it — so a narrow scalar narrows on the way in.
	element := f.elementOut(i.X)
	g.ins("lea rdi, [%s]", g.anyType(held))
	g.ins("lea rsi, %s", element)
	g.ins("call %s", g.extern("gy_any_box"))
	f.storeAny(i.Dst)
}

func (f *frame) emitAnyUnwrap(i *metals.AnyUnwrap) {
	g := f.g
	g.ins("lea rdi, %s", f.elementIn(i.Dst))
	g.ins("lea rsi, %s", f.slot(i.X).address())
	g.ins("lea rdx, [%s]", g.anyType(i.To))
	g.ins("call %s", g.extern("gy_any_read"))
	f.elementBack(i.Dst)
}

func (f *frame) emitAnyIs(i *metals.AnyIs) {
	g := f.g
	g.ins("lea rdi, %s", f.slot(i.X).address())
	g.ins("lea rsi, [%s]", g.anyType(i.To))
	g.ins("call %s", g.extern("gy_any_is"))
	g.ins("mov %s, rax", f.slot(i.Dst))
}

func (f *frame) emitAnyGet(i *metals.AnyGet) {
	g := f.g
	g.ins("lea rdi, %s", f.slot(i.X).address())
	g.ins("lea rsi, [%s]", g.cstrLabel(i.Name))
	g.ins("call %s", g.extern("gy_any_get"))
	f.storeAny(i.Dst)
}

func (f *frame) emitAnySet(i *metals.AnySet) {
	g := f.g
	g.ins("lea rdi, %s", f.slot(i.X).address())
	g.ins("lea rsi, [%s]", g.cstrLabel(i.Name))
	g.ins("lea rdx, %s", f.slot(i.Val).address())
	g.ins("call %s", g.extern("gy_any_set"))
}

func (f *frame) emitAnyCall(i *metals.AnyCall) {
	g := f.g
	area, ok := f.anyArgs[i]
	if !ok {
		g.fail("@%s: any.call has no argument area", f.fn.Name)
	}
	for n, arg := range i.Args {
		f.copyMem(memAt(memLoc{"rbp", area + n*2*wordSize}), f.slot(arg), 2*wordSize)
	}
	g.ins("lea rdi, %s", f.slot(i.Dst).address())
	g.ins("lea rsi, %s", f.slot(i.X).address())
	g.ins("lea rdx, [rbp%+d]", area)
	g.ins("mov rcx, %d", len(i.Args))
	g.ins("call %s", g.extern("gy_any_call"))
}

// storeAny lands a two-word any returned in rax:rdx.
func (f *frame) storeAny(dst string) {
	slot := f.slot(dst)
	f.g.ins("mov %s, rax", slot)
	f.g.ins("mov %s, rdx", slot.plus(wordSize))
}
