package wasm

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Module-level translation: index spaces become names, the memory and
// globals become declarations, and each body is handed to bodies.go.
//
// Two things here are not a one-to-one mapping and are worth saying out
// loud. Wasm globals carry initializers and METALS globals do not — a
// front end is expected to lower module initialization into a function
// it arranges to run first — so the initializers become @wasm.init,
// which the synthesized entry calls before anything else. And a wasm
// table is translated away rather than represented: the element section
// says statically which functions the table holds, so call_indirect
// becomes a switch over those indices, and no table exists at run time.

const memoryName = "memory"

type translator struct {
	src *wmodule
	out *metals.Module

	// names is the METALS name of each wasm function, indexed by the wasm
	// function index (imports first, then definitions).
	names []string
	used  map[string]bool

	// hasMemory records whether the module declared one, so that an
	// access in a body can say so rather than naming a memory that is not
	// there.
	hasMemory bool

	// dispatchers maps a type index to the generated function that turns
	// a table index into a direct call.
	dispatchers map[uint32]string
	// helpers are the synthesized bit-counting functions, generated once
	// each and only when a body reaches for one.
	helpers map[string]string
}

func translate(name string, src *wmodule) (*metals.Module, error) {
	t := &translator{
		src:         src,
		out:         &metals.Module{Name: name},
		used:        map[string]bool{},
		dispatchers: map[uint32]string{},
		helpers:     map[string]string{},
	}

	// @main is the entry every consumer looks for, so it is reserved
	// before anything else can take it: a wasm function called main is
	// the program's, not the module's, and the two are different things.
	t.used["main"] = true

	if err := t.declareFuncs(); err != nil {
		return nil, err
	}
	if err := t.declareImports(); err != nil {
		return nil, err
	}
	if err := t.declareMemory(); err != nil {
		return nil, err
	}
	t.declareGlobals()

	for index, body := range src.Code {
		fnIndex := uint32(src.importedFuncs() + index)
		if err := t.function(fnIndex, &body); err != nil {
			return nil, fmt.Errorf("@%s: %w", t.names[fnIndex], err)
		}
	}
	if err := t.entry(); err != nil {
		return nil, err
	}
	return t.out, nil
}

// ---------------------------------------------------------------------------
// Names and declarations
// ---------------------------------------------------------------------------

// declareFuncs assigns a METALS name to every function index. The name
// section supplies most of them; anything unnamed is its index, which is
// still what a disassembler would have shown.
func (t *translator) declareFuncs() error {
	total := t.src.importedFuncs() + len(t.src.Funcs)
	t.names = make([]string, total)
	imported := 0
	for _, imp := range t.src.Imports {
		if imp.Kind != kindFunc {
			continue
		}
		// An import keeps the name the other side knows it by: that name
		// is the contract, not a label.
		t.names[imported] = imp.Name
		t.used[imp.Name] = true
		imported++
	}
	// wasm-ld emits no name section unless it is asked for one, so the
	// export table is usually the only place a function's own name
	// survives the link. Between the two, and the index as a last resort,
	// most of a translated module reads as the C that produced it.
	exported := map[uint32]string{}
	for _, exp := range t.src.Exports {
		if exp.Kind == kindFunc {
			exported[exp.Index] = exp.Name
		}
	}
	for i := imported; i < total; i++ {
		desired := t.src.Names[uint32(i)]
		if desired == "" {
			desired = exported[uint32(i)]
		}
		if desired == "" {
			desired = "f" + strconv.Itoa(i)
		}
		t.names[i] = t.unique(desired)
	}
	return nil
}

// unique reserves a name, suffixing it when it is already taken. The
// suffix is visible on purpose: a reader seeing @"main.1" should be able
// to tell that the module wanted `main` and could not have it.
func (t *translator) unique(desired string) string {
	if !t.used[desired] {
		t.used[desired] = true
		return desired
	}
	for n := 1; ; n++ {
		candidate := desired + "." + strconv.Itoa(n)
		if !t.used[candidate] {
			t.used[candidate] = true
			return candidate
		}
	}
}

// declareImports turns each wasm function import into a METALS import.
//
// The import's wasm module string is used verbatim as the METALS module
// name, which is why it has to carry a scheme: `gyro:rt` is a runtime
// module and `env` is nothing at all. C says which it means with
// __attribute__((import_module(...))), so the requirement lands on the
// source that made the claim rather than on a table here that guesses.
func (t *translator) declareImports() error {
	for _, imp := range t.src.Imports {
		switch imp.Kind {
		case kindFunc:
		case kindMemory, kindTable, kindGlobal:
			return fmt.Errorf("importing a %s is not translated; link the module first",
				importKindName(imp.Kind))
		default:
			return fmt.Errorf("unknown import kind %#x", imp.Kind)
		}
		if metals.Scheme(imp.Module) == "" {
			return fmt.Errorf("import %q.%q names no module scheme: declare it with "+
				"__attribute__((import_module(\"gyro:rt\"), import_name(%q)))",
				imp.Module, imp.Name, imp.Name)
		}
		if int(imp.TypeIndex) >= len(t.src.Types) {
			return fmt.Errorf("import %q.%q names type %d, of %d",
				imp.Module, imp.Name, imp.TypeIndex, len(t.src.Types))
		}
		sig, err := signature(&t.src.Types[imp.TypeIndex])
		if err != nil {
			return fmt.Errorf("import %q.%q: %w", imp.Module, imp.Name, err)
		}
		t.addImport(imp.Module, metals.ImportedFunc{Name: imp.Name, Sig: sig})
	}
	return nil
}

// addImport files one imported function under its module, reusing the
// group when the module is already there.
func (t *translator) addImport(module string, fn metals.ImportedFunc) {
	for _, imp := range t.out.Imports {
		if imp.Module == module {
			imp.Funcs = append(imp.Funcs, fn)
			return
		}
	}
	t.out.Imports = append(t.out.Imports, &metals.Import{
		Module: module,
		Funcs:  []metals.ImportedFunc{fn},
	})
}

func importKindName(kind byte) string {
	switch kind {
	case kindTable:
		return "table"
	case kindMemory:
		return "memory"
	case kindGlobal:
		return "global"
	}
	return "thing"
}

// signature converts a wasm function type. Wasm's multi-value results
// would be a METALS tuple, but nothing that reaches this bridge produces
// one: clang emits single-result functions unless multi-value is turned
// on, and a wrong guess here would be silent.
func signature(ft *funcType) (*metals.FnType, error) {
	sig := &metals.FnType{}
	for _, param := range ft.Params {
		sig.Params = append(sig.Params, param.metalsType())
	}
	switch len(ft.Results) {
	case 0:
	case 1:
		sig.Result = ft.Results[0].metalsType()
	default:
		return nil, fmt.Errorf("a multi-value result %s is not translated", ft)
	}
	return sig, nil
}

func (t *translator) declareMemory() error {
	if len(t.src.Memories) == 0 {
		if len(t.src.Data) > 0 {
			return fmt.Errorf("there are data segments but no memory to put them in")
		}
		return nil
	}
	if len(t.src.Memories) > 1 {
		return fmt.Errorf("%d memories: METALS allows several, but wasm-ld emits one and "+
			"an access carries no index to tell them apart", len(t.src.Memories))
	}
	mem := t.src.Memories[0]
	t.hasMemory = true
	t.out.Memories = append(t.out.Memories, &metals.Memory{
		Name: memoryName,
		Min:  int64(mem.Min),
		Max:  int64(mem.Max),
	})
	for _, seg := range t.src.Data {
		if len(seg.Bytes) == 0 {
			continue
		}
		t.out.Data = append(t.out.Data, &metals.DataSeg{
			Memory: memoryName,
			Offset: int64(seg.Offset),
			Bytes:  seg.Bytes,
		})
	}
	return nil
}

// globalName is the METALS global for a wasm global index.
func globalName(index int) string { return "g" + strconv.Itoa(index) }

func (t *translator) declareGlobals() {
	for index, g := range t.src.Globals {
		t.out.Globals = append(t.out.Globals, &metals.Global{
			Name: globalName(index),
			Type: g.Type.metalsType(),
		})
	}
}

// ---------------------------------------------------------------------------
// The entry point
// ---------------------------------------------------------------------------

// entry synthesizes @main: the global initializers, then the module's
// constructors, then the C main. A wasm module has no such function —
// what it has is a set of exports and a convention — so this is the one
// place the bridge decides something rather than translating it.
func (t *translator) entry() error {
	fn := &metals.Func{Name: "main", Export: true}
	b := &bodyBuilder{}

	if len(t.src.Globals) > 0 {
		t.out.Funcs = append(t.out.Funcs, t.globalInit())
		b.emit(&metals.Call{Callee: "wasm.init"})
	}
	if ctors, ok := t.exported("__wasm_call_ctors"); ok {
		b.emit(&metals.Call{Callee: ctors})
	}

	main, ok := t.exported("main")
	if !ok {
		return fmt.Errorf("the module exports no main; link with -Wl,--export=main")
	}
	sig, err := t.sigOfName(main)
	if err != nil {
		return err
	}
	// A freestanding C main is `int main(void)`; the hosted spelling
	// `int main(int, char **)` reaches wasm as two i32s. Both are called
	// with zeroes, because there is no argv in a linear memory nobody
	// filled in.
	var args []string
	for i, param := range sig.Params {
		reg := "a" + strconv.Itoa(i)
		b.emit(&metals.Const{Dst: reg, Type: param, Value: zeroLiteral(param)})
		args = append(args, reg)
	}
	if len(sig.Params) > 2 {
		return fmt.Errorf("main takes %d parameters, which is not a C main", len(sig.Params))
	}
	call := &metals.Call{Callee: main, Args: args}
	if sig.Result != nil {
		call.Dst = "status"
	}
	b.emit(call)

	// Returning from main is exit(status) in C, which a libc has to see:
	// its stdout buffer is still full and its exit code is still in a
	// register. A module that exports __gy_finish gets called with the
	// status; one that does not — a freestanding program with no libc —
	// is unaffected, and its status is dropped as before.
	if finish, ok := t.exported("__gy_finish"); ok {
		status := call.Dst
		if status == "" {
			status = "status"
			b.emit(&metals.Const{Dst: status, Type: metals.TInt32, Value: "0"})
		}
		b.emit(&metals.Call{Callee: finish, Args: []string{status}})
	}
	b.emit(&metals.Return{})

	fn.Body = b.instrs
	t.out.Funcs = append(t.out.Funcs, fn)
	return nil
}

// globalInit writes each global's declared initial value. __stack_pointer
// is one of these, so skipping it would put every C frame at address 0.
func (t *translator) globalInit() *metals.Func {
	b := &bodyBuilder{}
	for index, g := range t.src.Globals {
		typ := g.Type.metalsType()
		value := "v" + strconv.Itoa(index)
		pointer := "p" + strconv.Itoa(index)
		b.emit(&metals.Const{Dst: value, Type: typ, Value: initLiteral(g)})
		b.emit(&metals.GlobalPtr{Dst: pointer, Name: globalName(index)})
		b.emit(&metals.Store{Ptr: pointer, Val: value})
	}
	b.emit(&metals.Return{})
	return &metals.Func{Name: "wasm.init", Body: b.instrs}
}

// initLiteral renders a global's initial value. A float global's
// initializer is not read by the decoder — nothing wasm-ld emits has
// one — so it starts at zero, which is what the constant expression
// would have said anyway for the bss-shaped globals a C module has.
func initLiteral(g globalDef) string {
	switch g.Type {
	case valF32, valF64:
		return "0.0"
	}
	return strconv.FormatInt(g.Init, 10)
}

func zeroLiteral(t metals.Type) string {
	if prim, ok := t.(*metals.Prim); ok {
		switch prim.Kind {
		case metals.F32, metals.F64:
			return "0.0"
		case metals.Bool:
			return "false"
		}
	}
	return "0"
}

// exported resolves an exported function name to its METALS name.
func (t *translator) exported(name string) (string, bool) {
	for _, exp := range t.src.Exports {
		if exp.Kind == kindFunc && exp.Name == name {
			if int(exp.Index) < len(t.names) {
				return t.names[exp.Index], true
			}
		}
	}
	return "", false
}

func (t *translator) sigOfName(name string) (*metals.FnType, error) {
	for _, fn := range t.out.Funcs {
		if fn.Name == name {
			return fn.Sig(), nil
		}
	}
	return nil, fmt.Errorf("@%s is exported but was not translated", name)
}

// ---------------------------------------------------------------------------
// Functions
// ---------------------------------------------------------------------------

// function translates one body. Its parameters are METALS registers
// named for their wasm local index, and the locals that follow are
// registers too — wasm locals are already single-assignment-free named
// slots, which is exactly what a METALS register is.
func (t *translator) function(index uint32, c *code) error {
	ft, err := t.src.typeOfFunc(index)
	if err != nil {
		return err
	}
	sig, err := signature(ft)
	if err != nil {
		return err
	}

	fn := &metals.Func{Name: t.names[index], Result: sig.Result}
	for i, param := range ft.Params {
		fn.Params = append(fn.Params, metals.Param{Name: localName(i), Type: param.metalsType()})
	}
	// Everything a wasm module exports is exported here too. The boundary
	// rules apply to those signatures and every wasm value type passes
	// them, so nothing is lost by being generous.
	fn.Export = t.isExported(index)

	body := &bodyTranslator{
		t:      t,
		fn:     fn,
		src:    t.src,
		result: sig.Result,
		locals: make([]valType, 0, len(ft.Params)+len(c.Locals)),
		types:  map[string]metals.Type{},
	}
	body.locals = append(body.locals, ft.Params...)
	body.locals = append(body.locals, c.Locals...)

	if err := body.run(c.Body, len(ft.Params)); err != nil {
		return err
	}
	t.out.Funcs = append(t.out.Funcs, fn)
	return nil
}

func (t *translator) isExported(index uint32) bool {
	for _, exp := range t.src.Exports {
		if exp.Kind == kindFunc && exp.Index == index {
			return true
		}
	}
	return false
}

func localName(index int) string { return "l" + strconv.Itoa(index) }

// ---------------------------------------------------------------------------
// Dispatchers for call_indirect
// ---------------------------------------------------------------------------

// dispatcher generates, once per signature, the function that turns a
// table index into a direct call. The element section says statically
// which functions the table holds, so the switch is complete and there
// is no table at run time — which also means an index the table never
// held aborts rather than jumping somewhere.
func (t *translator) dispatcher(typeIndex uint32) (string, error) {
	if name, ok := t.dispatchers[typeIndex]; ok {
		return name, nil
	}
	if int(typeIndex) >= len(t.src.Types) {
		return "", fmt.Errorf("call_indirect names type %d, of %d", typeIndex, len(t.src.Types))
	}
	want := &t.src.Types[typeIndex]
	sig, err := signature(want)
	if err != nil {
		return "", err
	}

	name := t.unique("wasm.indirect." + strconv.Itoa(int(typeIndex)))
	t.dispatchers[typeIndex] = name

	fn := &metals.Func{Name: name, Result: sig.Result}
	fn.Params = append(fn.Params, metals.Param{Name: "index", Type: metals.TInt32})
	args := make([]string, len(want.Params))
	for i, param := range want.Params {
		args[i] = "a" + strconv.Itoa(i)
		fn.Params = append(fn.Params, metals.Param{Name: args[i], Type: param.metalsType()})
	}

	sw := &metals.Switch{X: "index"}
	for _, seg := range t.src.Elements {
		for offset, target := range seg.Funcs {
			slot := int(seg.Offset) + offset
			targetType, err := t.src.typeOfFunc(target)
			if err != nil {
				return "", err
			}
			if !sameType(targetType, want) {
				continue
			}
			arm := &bodyBuilder{}
			call := &metals.Call{Callee: t.names[target], Args: args}
			if sig.Result != nil {
				call.Dst = "r"
			}
			arm.emit(call)
			arm.emit(&metals.Return{Val: call.Dst})
			sw.Cases = append(sw.Cases, metals.SwitchCase{Value: slot, Body: arm.instrs})
		}
	}
	sort.Slice(sw.Cases, func(i, j int) bool { return sw.Cases[i].Value < sw.Cases[j].Value })

	body := &bodyBuilder{}
	body.emit(sw)
	body.emit(&metals.Abort{Message: "indirect call through an index the table does not hold"})
	fn.Body = body.instrs

	t.out.Funcs = append(t.out.Funcs, fn)
	return name, nil
}

func sameType(a, b *funcType) bool {
	if len(a.Params) != len(b.Params) || len(a.Results) != len(b.Results) {
		return false
	}
	for i := range a.Params {
		if a.Params[i] != b.Params[i] {
			return false
		}
	}
	for i := range a.Results {
		if a.Results[i] != b.Results[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Emission
// ---------------------------------------------------------------------------

// bodyBuilder accumulates instructions for one nesting level.
type bodyBuilder struct{ instrs []metals.Instr }

func (b *bodyBuilder) emit(instr metals.Instr) { b.instrs = append(b.instrs, instr) }
