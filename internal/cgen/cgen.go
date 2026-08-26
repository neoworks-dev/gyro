// Package cgen compiles a verified METALS module to one self-contained C
// translation unit, which links against the same runtime the nasm backend
// uses (internal/crt).
//
// It is a sibling of internal/nasm, not a replacement. Two jobs: it
// checks that the METALS lowering says what it means, without the
// assembler-level bug surface of hand-written codegen, and it produces
// binaries to benchmark the nasm backend's output against. The layout,
// the runtime contract and every abort message are docs/appendix-e-native-backends.md's,
// unchanged — only the way instructions are spelled differs.
package cgen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gyro-lang/gyro/internal/crt"
	"github.com/gyro-lang/gyro/internal/metals"
)

// Generate compiles the module to one C source file.
func Generate(module *metals.Module) (source string, err error) {
	registerTypes, err := metals.RegisterTypes(module)
	if err != nil {
		return "", fmt.Errorf("cgen: %w", err)
	}

	g := &generator{
		module:         module,
		regTypes:       registerTypes,
		types:          map[string]*metals.TypeDecl{},
		imports:        map[string]*metals.FnType{},
		externSyms:     map[string]bool{},
		funcs:          map[string]*metals.Func{},
		typeNames:      map[string]string{},
		building:       map[string]bool{},
		symbols:        map[string]string{},
		usedSyms:       map[string]bool{},
		strLits:        map[string]string{},
		anyTypes:       map[string]string{},
		buildingAny:    map[string]bool{},
		eqHelpers:      map[string]string{},
		thunks:         map[string]string{},
		indirectThunks: map[string]string{},
		globals:        map[string]*metals.Global{},
		globalSyms:     map[string]string{},
		memories:       map[string]*metals.Memory{},
		memorySyms:     map[string]string{},
	}
	for _, global := range module.Globals {
		g.globals[global.Name] = global
	}
	for _, mem := range module.Memories {
		g.memories[mem.Name] = mem
	}
	for _, decl := range module.Types {
		g.types[decl.Name] = decl
	}
	g.layout = metals.NewLayout(module, g.fail)
	for _, imp := range module.Imports {
		library := metals.ExternLibrary(imp.Module)
		for _, fn := range imp.Funcs {
			g.imports[fn.Name] = fn.Sig
			if library != "" {
				g.externSyms[fn.Name] = true
			}
		}
	}
	for _, fn := range module.Funcs {
		g.funcs[fn.Name] = fn
	}

	defer func() {
		if failure := recover(); failure != nil {
			genErr, ok := failure.(*genError)
			if !ok {
				panic(failure)
			}
			err = genErr.err
		}
	}()

	if g.funcs["main"] == nil {
		g.fail("the module has no main")
	}

	// Every signature is declared before any body, so the order the
	// functions happen to be in never matters. The foreign wrappers are
	// compiled as their own translation unit, so they are declared here
	// rather than defined.
	g.decls.WriteString(crt.ShimDeclarations(module))
	for _, fn := range module.Funcs {
		fmt.Fprintf(&g.decls, "%s;\n", g.signature(fn))
	}
	for _, fn := range module.Funcs {
		g.genFunc(fn)
	}
	g.drainPending()
	g.genEntry()
	return g.assemble(), nil
}

type generator struct {
	module   *metals.Module
	regTypes map[string]map[string]metals.Type
	types    map[string]*metals.TypeDecl
	// layout answers every size, alignment and offset question, so that
	// the two backends cannot pad a struct differently.
	layout  *metals.Layout
	imports map[string]*metals.FnType
	funcs   map[string]*metals.Func
	// externSyms holds the imports that name a C library's symbol rather
	// than the runtime's, which are called through a generated wrapper.
	externSyms map[string]bool

	typedefs strings.Builder // generated struct typedefs
	data     strings.Builder // string literals and module storage
	decls    strings.Builder // forward declarations
	text     strings.Builder // function bodies

	typeNames map[string]string // METALS type string -> C typedef name
	building  map[string]bool   // cycle guard while a typedef is emitted
	// stageN names the temporaries a narrow scalar crosses the runtime
	// boundary through.
	stageN int

	globals    map[string]*metals.Global
	globalSyms map[string]string
	memories   map[string]*metals.Memory
	memorySyms map[string]string

	symbols  map[string]string // METALS function name -> C identifier
	usedSyms map[string]bool
	strLits  map[string]string // decoded literal -> C identifier

	// anyTypes interns one descriptor per type the program wraps or
	// tests, so that identity is structural equality; anyData holds the
	// descriptors and field tables, anyText the apply trampolines.
	anyTypes    map[string]string
	buildingAny map[string]bool
	anyData     strings.Builder
	anyText     strings.Builder
	anyN        int

	eqHelpers map[string]string // type string -> helper name
	thunks    map[string]string // async callee -> thunk name
	// indirectThunks key on the signature instead: a call through a
	// function value has no callee symbol to name.
	indirectThunks map[string]string
	pending        []func() // helpers and thunks queued during emission

	labelN int
	typeN  int
}

type genError struct{ err error }

func (g *generator) fail(format string, args ...any) {
	panic(&genError{fmt.Errorf("cgen: "+format, args...)})
}

// drainPending emits queued helper functions; emitting one may queue
// more, so the loop runs until the list is empty.
func (g *generator) drainPending() {
	for len(g.pending) > 0 {
		next := g.pending[0]
		g.pending = g.pending[1:]
		next()
	}
}

func (g *generator) assemble() string {
	var out strings.Builder
	out.WriteString(prelude)
	if g.typedefs.Len() > 0 {
		out.WriteString("\n/* ---------------- generated types ---------------- */\n")
		out.WriteString(g.typedefs.String())
	}
	if g.data.Len() > 0 {
		out.WriteString("\n/* ---------------- constants and module storage ---------------- */\n\n")
		out.WriteString(g.data.String())
	}
	if g.anyData.Len() > 0 {
		out.WriteString("\n/* ---------------- any descriptors ---------------- */\n\n")
		out.WriteString(g.anyData.String())
		out.WriteString(g.anyText.String())
	}
	out.WriteString("\n/* ---------------- declarations ---------------- */\n\n")
	out.WriteString(g.decls.String())
	out.WriteString(g.text.String())
	return out.String()
}

// ---------------------------------------------------------------------------
// Emission primitives
// ---------------------------------------------------------------------------

func (g *generator) line(format string, args ...any) {
	g.text.WriteString("\t")
	fmt.Fprintf(&g.text, format, args...)
	g.text.WriteString("\n")
}

func (g *generator) raw(format string, args ...any) {
	fmt.Fprintf(&g.text, format, args...)
	g.text.WriteString("\n")
}

func (g *generator) newLabel() string {
	g.labelN++
	return fmt.Sprintf("L%d", g.labelN)
}

// ---------------------------------------------------------------------------
// Names
// ---------------------------------------------------------------------------

// symbolFor maps a METALS function name to a unique C identifier. The
// entry point rt.c calls is a separate wrapper (see genEntry), so main
// needs no special name here.
func (g *generator) symbolFor(name string) string {
	if symbol, ok := g.symbols[name]; ok {
		return symbol
	}
	symbol := uniqueSymbol("F_"+sanitize(name), g.usedSyms)
	g.usedSyms[symbol] = true
	g.symbols[name] = symbol
	return symbol
}

func sanitize(name string) string {
	var out strings.Builder
	for _, ch := range name {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' {
			out.WriteRune(ch)
		} else {
			out.WriteByte('_')
		}
	}
	return out.String()
}

func uniqueSymbol(base string, used map[string]bool) string {
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := base + "_" + strconv.Itoa(i)
		if !used[candidate] {
			return candidate
		}
	}
}

// globalSymbol names module storage, reserving it on first use. A global
// with no static image is zero-initialized, which C gives a file-scope
// object for free.
func (g *generator) globalSymbol(name string) string {
	if symbol, ok := g.globalSyms[name]; ok {
		return symbol
	}
	global, ok := g.globals[name]
	if !ok {
		g.fail("no global @%s", name)
	}
	symbol := uniqueSymbol("G_"+sanitize(name), g.usedSyms)
	g.usedSyms[symbol] = true

	// Module storage is addressed through global.ptr, so it holds storage
	// the way a slot does.
	spelling := g.memberType(global.Type)
	if global.Bytes == nil {
		g.globalSyms[name] = symbol
		fmt.Fprintf(&g.data, "static %s %s; /* @%s */\n", spelling, symbol, comment(name))
		return symbol
	}

	// A static image is bytes, and C has no way to spell an object of an
	// arbitrary type as bytes — except through the first member of a
	// union, which is initialized statically and aliases the rest. The
	// object is that union's second member, so every reference to it
	// stays typed and the image stays a compile-time constant.
	storage := symbol + "_store"
	g.usedSyms[storage] = true
	fmt.Fprintf(&g.data, "static union { unsigned char image[%d]; %s value; } %s = { { ",
		len(global.Bytes), spelling, storage)
	for i, b := range global.Bytes {
		if i > 0 {
			g.data.WriteString(", ")
		}
		fmt.Fprintf(&g.data, "%d", b)
	}
	fmt.Fprintf(&g.data, " } }; /* @%s */\n", comment(name))
	fmt.Fprintf(&g.data, "_Static_assert(sizeof(%s) == %d, %s);\n",
		spelling, len(global.Bytes), cStringLiteral("storage image for @"+name))

	// The name the rest of the file uses is the typed member, so
	// global.ptr takes its address the way it takes any other global's.
	g.globalSyms[name] = storage + ".value"
	return g.globalSyms[name]
}

// strLiteral interns a string constant as a { len, bytes } blob shaped
// like the runtime's gy_str, so a pointer to it is an ordinary str value.
func (g *generator) strLiteral(text string) string {
	if name, ok := g.strLits[text]; ok {
		return name
	}
	name := fmt.Sprintf("S%d", len(g.strLits))
	g.strLits[text] = name
	fmt.Fprintf(&g.data, "static const struct { int64_t len; char bytes[%d]; } %s = { %d, %s };\n",
		len(text)+1, name, len(text), cStringLiteral(text))
	return name
}

// strConst is the expression that reads an interned literal as a str.
func (g *generator) strConst(text string) string {
	return "(const gy_str *)&" + g.strLiteral(text)
}

// cStringLiteral escapes bytes for a C string literal. Everything
// outside printable ASCII goes out as an octal escape, which keeps the
// generated file plain ASCII and never depends on the compiler's source
// encoding.
func cStringLiteral(text string) string {
	var out strings.Builder
	out.WriteByte('"')
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case ch == '"' || ch == '\\':
			out.WriteByte('\\')
			out.WriteByte(ch)
		case ch == '\n':
			out.WriteString("\\n")
		case ch == '\t':
			out.WriteString("\\t")
		case ch == '\r':
			out.WriteString("\\r")
		case ch >= 0x20 && ch < 0x7f:
			out.WriteByte(ch)
		default:
			// Exactly three octal digits: C reads at most three, so a
			// following digit cannot be swallowed into the escape, and a
			// fourth would silently start a new character.
			fmt.Fprintf(&out, "\\%03o", ch)
		}
	}
	out.WriteByte('"')
	return out.String()
}

// comment makes text safe to drop inside a C block comment. METALS names
// are quoted words that may hold anything, monomorphization included, so
// the one sequence that would end the comment early is broken up.
func comment(text string) string {
	text = strings.ReplaceAll(text, "*/", "* /")
	return strings.ReplaceAll(text, "\n", " ")
}

// ---------------------------------------------------------------------------
// Signatures
// ---------------------------------------------------------------------------

// signature spells a generated function's C prototype. Aggregates travel
// by value in both directions: C's own struct passing already does what
// the nasm backend hand-rolls with sret and argument pointers, and the
// runtime boundary is untouched because rt.c only ever sees scalars and
// pointers.
func (g *generator) signature(fn *metals.Func) string {
	var out strings.Builder
	fmt.Fprintf(&out, "static %s %s(", g.resultType(fn.Result), g.symbolFor(fn.Name))
	if len(fn.Params) == 0 {
		out.WriteString("void")
	}
	for index, param := range fn.Params {
		if index > 0 {
			out.WriteString(", ")
		}
		fmt.Fprintf(&out, "%s %s", g.cType(param.Type), registerName(param.Name))
	}
	out.WriteString(")")
	return out.String()
}

func (g *generator) resultType(result metals.Type) string {
	if result == nil {
		return "void"
	}
	return g.cType(result)
}

// fnPointerType spells the type a function value's code word is cast to
// before an indirect call. withEnv prepends the environment parameter a
// closure's lifted body takes.
func (g *generator) fnPointerType(sig *metals.FnType, withEnv bool) string {
	var params []string
	if withEnv {
		params = append(params, "void *")
	}
	for _, param := range sig.Params {
		params = append(params, g.cType(param))
	}
	if len(params) == 0 {
		params = append(params, "void")
	}
	return fmt.Sprintf("%s (*)(%s)", g.resultType(sig.Result), strings.Join(params, ", "))
}

// registerName spells a METALS register as a C local. The escape is
// injective — every character a C identifier cannot hold, underscore
// included, becomes a two-digit hex escape — so two registers can never
// collide on one local, and no per-function renaming table is needed.
func registerName(name string) string {
	var out strings.Builder
	out.WriteString("r_")
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' {
			out.WriteByte(ch)
			continue
		}
		fmt.Fprintf(&out, "_%02x", ch)
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// The entry point
// ---------------------------------------------------------------------------

// genEntry emits gy_user_main, the symbol rt.c calls. The runtime always
// hands over a pointer to the command line as a { data, len } slice; a
// main that declares no parameter simply ignores it.
func (g *generator) genEntry() {
	main := g.funcs["main"]
	argument := ""
	if len(main.Params) == 1 {
		if _, ok := main.Params[0].Type.(*metals.SliceOf); !ok {
			g.fail("main takes a %s, which is not the command line", main.Params[0].Type)
		}
		argument = "*(const gy_slice *)args"
	} else if len(main.Params) > 1 {
		g.fail("main takes %d parameters", len(main.Params))
	}

	// Linear memory comes up here rather than in a constructor: this is
	// the one point the runtime hands control to generated code, so it is
	// where "before anything runs" is a thing that can be said.
	setup := g.genMemoryInit()

	g.raw("\nvoid gy_user_main(void *args);")
	g.raw("void gy_user_main(void *args) {")
	g.line("(void)args;")
	for _, statement := range setup {
		g.line("%s", statement)
	}
	g.line("%s(%s);", g.symbolFor("main"), argument)
	g.raw("}")
}

// ---------------------------------------------------------------------------
// Sorted iteration
// ---------------------------------------------------------------------------

func sortedNames(types map[string]metals.Type) []string {
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
