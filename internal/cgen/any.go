package cgen

import (
	"fmt"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// An `any` is two words: a descriptor and a payload. The descriptors are
// static data, one per distinct type the program wraps or tests, so a
// shape test is a pointer comparison against a known address rather than
// anything structural at run time. That is what keeps the open dynamic
// value from costing what an interpreter's value costs — a scalar wrap
// allocates nothing, and `any.is` is one compare.
//
// The runtime shapes are rt.c's; this file only emits the data and the
// per-signature trampolines that rt.c cannot write for itself.

// anyType interns the descriptor for a type and returns its C
// identifier, emitting the descriptor the first time it is asked for.
func (g *generator) anyType(t metals.Type) string {
	key := t.String()
	if name, ok := g.anyTypes[key]; ok {
		return name
	}
	if g.buildingAny[key] {
		g.fail("type %s contains itself, so it has no any descriptor", key)
	}
	g.buildingAny[key] = true

	g.anyN++
	name := fmt.Sprintf("TY%d", g.anyN)

	// Members are interned first, so every descriptor a definition
	// mentions is already above it in the translation unit.
	fields, count := g.anyFields(t, name)
	apply := g.anyApply(t, name)

	delete(g.buildingAny, key)
	g.anyTypes[key] = name

	fmt.Fprintf(&g.anyData, "static const gy_type %s = { %s, %d, %d, %s, %d, %s };\n",
		name, cStringLiteral(key), g.sizeOf(t), boolBit(g.anyBoxed(t)), fields, count, apply)
	return name
}

// anyBoxed decides whether the payload holds the value or points at it.
// A record is always boxed however narrow it is, so that any.set through
// one reference is visible through the rest; the evaluator shares the
// same way, and the two tiers have to agree.
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

// anyFields emits the field table for a record, which is what any.get and
// any.set walk. Everything else has none.
func (g *generator) anyFields(t metals.Type, owner string) (string, int) {
	named, ok := t.(*metals.Named)
	if !ok {
		return "0", 0
	}
	decl := g.types[named.Name]
	if decl == nil || decl.Record == nil {
		return "0", 0
	}

	entries := make([]string, len(decl.Record.Fields))
	for i, field := range decl.Record.Fields {
		// A field that is itself an any needs no descriptor: it is
		// handed out and taken back whole, with no boxing either way.
		descriptor := "0"
		if _, isAny := field.Type.(*metals.Any); !isAny {
			descriptor = "&" + g.anyType(field.Type)
		}
		entries[i] = fmt.Sprintf("{ %s, %d, %s }",
			cStringLiteral(field.Name), g.layout.FieldOffset(decl.Record, i), descriptor)
	}

	name := owner + "F"
	fmt.Fprintf(&g.anyData, "static const gy_field %s[] = { %s };\n", name, strings.Join(entries, ", "))
	return name, len(entries)
}

// anyApply emits the trampoline that applies a wrapped function: it
// unwraps each argument to the type the callee declared, calls it, and
// boxes the result. A caller that knows nothing about the callee still
// hands it values of the right types, because the descriptor carries the
// signature's own conversion code.
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

	resultType := g.anyType(sig.Result)
	paramTypes := make([]string, len(sig.Params))
	for i, param := range sig.Params {
		paramTypes[i] = g.anyType(param)
	}

	name := owner + "A"
	var out strings.Builder
	fmt.Fprintf(&out, "\nstatic void %s(gy_any *out, gy_fn fn, const gy_any *args, int64_t n) {\n", name)
	fmt.Fprintf(&out, "    if (n != %d) gy_any_arity(&%s, n);\n", len(sig.Params), owner)

	// gy_any_read writes storage, and the call takes what a register
	// carries, so a narrow parameter is read into its own storage cell and
	// widened on the way into the call.
	plain := make([]string, len(sig.Params))
	for i, param := range sig.Params {
		fmt.Fprintf(&out, "    %s a%d;\n", g.memberType(param), i)
		fmt.Fprintf(&out, "    gy_any_read(&a%d, &args[%d], &%s);\n", i, i, paramTypes[i])
		plain[i] = fmt.Sprintf("a%d", i)
	}

	call := strings.Join(plain, ", ")
	withEnv := "fn.env"
	if call != "" {
		withEnv += ", " + call
	}
	fmt.Fprintf(&out, "    %s r;\n", g.memberType(sig.Result))
	// The same runtime branch an ordinary indirect call makes: a closure
	// takes its environment as a leading parameter, a bare fn.ref does
	// not.
	fmt.Fprintf(&out, "    if (fn.env != 0) {\n")
	fmt.Fprintf(&out, "        r = ((%s)fn.code)(%s);\n", g.fnPointerType(sig, true), withEnv)
	fmt.Fprintf(&out, "    } else {\n")
	fmt.Fprintf(&out, "        r = ((%s)fn.code)(%s);\n", g.fnPointerType(sig, false), call)
	fmt.Fprintf(&out, "    }\n")
	fmt.Fprintf(&out, "    *out = gy_any_box(&%s, &r);\n}\n", resultType)

	// The trampoline names its own descriptor, which is still being
	// built, so it is forward-declared and emitted after it.
	fmt.Fprintf(&g.anyData, "static void %s(gy_any *out, gy_fn fn, const gy_any *args, int64_t n);\n", name)
	g.anyText.WriteString(out.String())
	return name
}

func boolBit(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// The six instructions
// ---------------------------------------------------------------------------

func (f *frame) emitAnyWrap(i *metals.AnyWrap) {
	g := f.g
	held := f.typeOf(i.X)
	// Anys do not nest, the way optionals do not.
	if _, already := held.(*metals.Any); already {
		f.assign(i.Dst, f.reg(i.X))
		return
	}
	// An any holds what it holds in storage representation, the same as a
	// record's field does — which is what lets any.get hand a field out by
	// copying it — so a narrow scalar narrows on the way in.
	f.stagedOut(i.X, func(address string) {
		f.assign(i.Dst, fmt.Sprintf("gy_any_box(&%s, %s)", g.anyType(held), address))
	})
}

func (f *frame) emitAnyUnwrap(i *metals.AnyUnwrap) {
	g := f.g
	f.stagedIn(i.Dst, func(address string) {
		f.line("gy_any_read(%s, &%s, &%s);", address, f.reg(i.X), g.anyType(i.To))
	})
}

func (f *frame) emitAnyIs(i *metals.AnyIs) {
	g := f.g
	f.assign(i.Dst, fmt.Sprintf("gy_any_is(&%s, &%s)", f.reg(i.X), g.anyType(i.To)))
}

func (f *frame) emitAnyGet(i *metals.AnyGet) {
	f.assign(i.Dst, fmt.Sprintf("gy_any_get(&%s, %s)", f.reg(i.X), cStringLiteral(i.Name)))
}

func (f *frame) emitAnySet(i *metals.AnySet) {
	f.line("gy_any_set(&%s, %s, &%s);", f.reg(i.X), cStringLiteral(i.Name), f.reg(i.Val))
}

func (f *frame) emitAnyCall(i *metals.AnyCall) {
	// The arguments are gathered into an array so the trampoline can walk
	// them; it is the callee's descriptor that knows how many there
	// should be and what each becomes.
	buffer := "argv" + f.g.newLabel()
	f.line("gy_any %s[%d];", buffer, max(len(i.Args), 1))
	for n, arg := range i.Args {
		f.line("%s[%d] = %s;", buffer, n, f.reg(arg))
	}
	f.line("gy_any_call(&%s, &%s, %s, %d);", f.reg(i.Dst), f.reg(i.X), buffer, len(i.Args))
}
