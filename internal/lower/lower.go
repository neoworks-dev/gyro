// Package lower translates a checked Gyro program into one METALS module.
//
// The whole loader graph collapses into a single self-contained module:
// only main is exported, every other function is internal, and the only
// imports are runtime intrinsics from "gyro:rt". Generics are
// monomorphized — one METALS function per recorded instantiation — and
// expanded names keep their source spelling: @"Pair<i32>::sum",
// @"util::helper". Lowering trusts sema: it consumes the recorded Info
// instead of re-deriving types, and must only run on an error-free
// program.
package lower

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/types"
)

// Lower translates the program into one verified-shape METALS module.
func Lower(prog *loader.Program, info *sema.Info) (module *metals.Module, err error) {
	l := &lowerer{
		prog:          prog,
		info:          info,
		moduleOf:      map[ast.Node]*loader.Module{},
		tags:          map[string]string{},
		imports:       map[string]*metals.FnType{},
		importFrom:    map[string]string{},
		declared:      map[string]*metals.TypeDecl{},
		foreignOf:     map[string]*metals.Module{},
		foreignName:   map[string]bool{},
		importedTypes: map[string]*metals.TypeDecl{},
		importedFrom:  map[string]string{},
		reprs:         map[string]string{},
		hashes:        map[string]bool{},
		globalOf:      map[*ast.VarDecl][]int{},
		lowered:       map[string]bool{},
		errVariant:    map[*types.Struct]int{},
		anyVariant:    map[string]int{},
	}
	defer func() {
		switch failure := recover().(type) {
		case nil:
		case *lowerFailure:
			module, err = nil, fmt.Errorf("lower: %s", failure.message)
		default:
			panic(failure)
		}
	}()

	l.indexModules()
	l.collectErrorTypes()

	main := l.findMain()
	if main == nil {
		return nil, fmt.Errorf("lower: the program has no fn main in %s", prog.Root.File.Name)
	}

	l.module = &metals.Module{Name: l.moduleName()}
	l.collectGlobals()
	l.ensureFunc(main, nil)
	l.drain()
	l.settleAnyTypes()
	l.emitGlobalInit()
	l.finishErrorType()
	l.finishAnyType()
	l.attachImports()

	// A program that imports another language is several METALS modules,
	// and every consumer takes one. Linking belongs here rather than in
	// the driver, so that the corpus, the language server and `gyro
	// build` all see the same whole program. It runs even for a program
	// of one module, because that is also where a module's initializer
	// becomes the call at the top of main.
	var foreign []*metals.Module
	for _, loaded := range prog.Modules {
		if loaded.Foreign != nil {
			foreign = append(foreign, loaded.Foreign)
		}
	}
	linked, linkErr := metals.Link(append(foreign, l.module))
	if linkErr != nil {
		return nil, linkErr
	}
	return linked, nil
}

// lowerFailure aborts lowering with one message; Lower catches it.
type lowerFailure struct{ message string }

func (l *lowerer) fail(format string, args ...any) {
	panic(&lowerFailure{message: fmt.Sprintf(format, args...)})
}

type lowerer struct {
	prog *loader.Program
	info *sema.Info

	module *metals.Module

	// moduleOf maps every top-level declaration to its module, and tags
	// maps a module path to the name prefix its declarations carry ("" for
	// the root module).
	moduleOf map[ast.Node]*loader.Module
	tags     map[string]string

	// imports collects the intrinsics and foreign functions the program
	// actually uses; importFrom names the METALS module each comes from,
	// which is "gyro:rt" for everything the runtime supplies.
	imports    map[string]*metals.FnType
	importFrom map[string]string

	// declared maps METALS type names to their emitted declarations.
	declared map[string]*metals.TypeDecl

	// foreignOf maps a module path to the METALS another language made of
	// it, and foreignName is every type name those modules declare.
	//
	// A foreign type keeps the exporter's name — that is what importing
	// one means — so the name is spoken for before this program lowers a
	// line, and a Gyro type that would take it gives way instead. See
	// typeName.
	foreignOf   map[string]*metals.Module
	foreignName map[string]bool

	// importedTypes collects the foreign types the program actually
	// mentions, and importedFrom names the module each belongs to.
	importedTypes map[string]*metals.TypeDecl
	importedFrom  map[string]string

	// reprs maps a type to the synthesized function that renders it, so a
	// recursive type reaches its own repr while it is still being built.
	reprs map[string]string

	// hashes records which types already have a synthesized hash, for the
	// same reason.
	hashes map[string]bool

	// globals is every module-level var, in declaration order; globalOf
	// indexes back from the declaration that introduced them.
	globals  []moduleGlobal
	globalOf map[*ast.VarDecl][]int

	// queue holds functions waiting to be lowered; lowered marks METALS
	// names already emitted or queued.
	queue   []workItem
	lowered map[string]bool

	// errTypes lists the structs that can inhabit an error value, in
	// deterministic order; errVariant maps each to its variant index in
	// the generated %error enum (Native(str) is variant 0). usesError
	// records whether %error was ever mentioned.
	errTypes   []*types.Struct
	errVariant map[*types.Struct]int
	usesError  bool

	// anyTypes lists the types the program ever wraps in an any, in the
	// order the wraps were lowered; anyVariant indexes back from a type's
	// cache key. The set is closed because the whole program is here, which
	// is what lets an any be an ordinary enum. usesAny records whether the
	// %any declaration is needed at all.
	anyTypes   []types.Type
	anyVariant map[string]int
	usesAny    bool
	// anyPrinters names the generated %any printers a call site asked for.
	anyPrinters map[string]bool

	// stdlib is the bundled stdlib module, where Format is declared and the
	// streaming printers a format value is handed to live.
	stdlib *loader.Module

	synthetic int
}

type workItem struct {
	name     string
	decl     *ast.FuncDecl
	bindings map[string]types.Type
}

// ---------------------------------------------------------------------------
// Program indexing
// ---------------------------------------------------------------------------

func (l *lowerer) indexModules() {
	seen := map[string]bool{}
	for _, module := range l.prog.Modules {
		tag := ""
		if module != l.prog.Root {
			tag = moduleBase(module.Path)
			for n := 2; seen[tag]; n++ {
				tag = fmt.Sprintf("%s%d", moduleBase(module.Path), n)
			}
		}
		seen[tag] = true
		l.tags[module.Path] = tag

		if module.Foreign != nil {
			l.foreignOf[module.Path] = module.Foreign
			for name := range module.Foreign.TypeEnv() {
				l.foreignName[name] = true
			}
		}
		if module.Path == stdlib.Path("stdlib") {
			l.stdlib = module
		}

		for _, decl := range module.Tree.Decls {
			l.moduleOf[decl] = module
		}
	}
}

func moduleBase(path string) string {
	if name, bundled := stdlib.NameOf(path); bundled {
		return name
	}
	return strings.TrimSuffix(filepath.Base(path), ".gyro")
}

func (l *lowerer) moduleName() string {
	return "gyro:" + moduleBase(l.prog.Root.Path)
}

func (l *lowerer) findMain() *ast.FuncDecl {
	for _, decl := range l.prog.Root.Tree.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Owner == "" && fn.Name == "main" {
			return fn
		}
	}
	return nil
}

// isIntrinsic reports whether a declaration is one the runtime supplies:
// bundled and bodyless. A bundled declaration that has a body is ordinary
// Gyro code and lowers like any other, module tag included.
func (l *lowerer) isIntrinsic(decl *ast.FuncDecl) bool {
	if decl.Body != nil {
		return false
	}
	module := l.moduleOf[decl]
	return module != nil && stdlib.IsBundled(module.Path)
}

// ---------------------------------------------------------------------------
// Names
// ---------------------------------------------------------------------------

// funcName renders the METALS name of one lowered function: the module
// tag, the owner with its type arguments, the function with its own.
// main stays bare, and runtime intrinsics carry no module tag.
func (l *lowerer) funcName(decl *ast.FuncDecl, bindings map[string]types.Type) string {
	parts := []string{}
	if !l.isIntrinsic(decl) {
		if module := l.moduleOf[decl]; module != nil {
			if tag := l.tags[module.Path]; tag != "" {
				parts = append(parts, tag)
			}
		}
	}
	if decl.Owner != "" {
		parts = append(parts, decl.Owner+typeArgSuffix(decl.OwnerParams, bindings))
	}
	parts = append(parts, decl.Name+typeArgSuffix(decl.TypeParams, bindings))
	return strings.Join(parts, "::")
}

func typeArgSuffix(params []ast.TypeParam, bindings map[string]types.Type) string {
	if len(params) == 0 {
		return ""
	}
	args := make([]string, len(params))
	for i, param := range params {
		bound := bindings[param.Name]
		if bound == nil {
			bound = types.Unknown
		}
		args[i] = bound.String()
	}
	return "<" + strings.Join(args, ", ") + ">"
}

// typeName renders the METALS name of a named Gyro type.
func (l *lowerer) typeName(name, modulePath string, args []types.Type) string {
	full := name
	if len(args) > 0 {
		full += "<" + types.Key(args) + ">"
	}
	if tag := l.tags[modulePath]; tag != "" {
		full = tag + "::" + full
	}
	// A type imported from another producer arrives under the name that
	// producer gave it, and this program cannot rename it without giving
	// up the identity it imported the type for. So a Gyro type of the
	// same name is the one that moves.
	if l.foreignName[full] {
		full = "gyro::" + full
	}
	return full
}

// ---------------------------------------------------------------------------
// Type lowering
// ---------------------------------------------------------------------------

// metalsType lowers a Gyro type, declaring any named types it mentions.
func (l *lowerer) metalsType(t types.Type) metals.Type {
	switch t := t.(type) {
	case *types.Basic:
		return l.metalsBasic(t)
	case *types.Pointer:
		return &metals.Ptr{Elem: l.metalsType(t.Elem)}
	case *types.Optional:
		return &metals.Opt{Elem: l.metalsType(t.Elem)}
	case *types.Slice:
		return &metals.SliceOf{Elem: l.metalsType(t.Elem)}
	case *types.Array:
		return &metals.ArrayOf{Elem: l.metalsType(t.Elem), Len: t.Length}
	case *types.Chan:
		return &metals.ChanOf{Elem: l.metalsType(t.Elem)}
	case *types.Tuple:
		elems := make([]metals.Type, len(t.Elems))
		for i, elem := range t.Elems {
			elems[i] = l.metalsType(elem)
		}
		return &metals.TupleOf{Elems: elems}
	case *types.Func:
		return l.metalsFnType(t)
	case *types.Code:
		code := &metals.CodeType{}
		for _, param := range t.Params {
			code.Params = append(code.Params, l.metalsType(param))
		}
		if !isVoid(t.Result) {
			code.Result = l.metalsType(t.Result)
		}
		return code
	case *types.Future:
		return &metals.FutureOf{Result: l.metalsType(t.Result)}
	case *types.Struct:
		return l.metalsStruct(t)
	case *types.Union:
		return l.metalsUnion(t)
	case *types.Enum:
		return l.metalsEnum(t)
	}
	l.fail("cannot lower the type %s", t)
	return nil
}

func (l *lowerer) metalsBasic(t *types.Basic) metals.Type {
	switch t.Kind {
	case types.I8:
		return metals.TInt8
	case types.I16:
		return metals.TInt16
	case types.I32, types.UntypedInt:
		return metals.TInt32
	case types.I64:
		return metals.TInt64
	case types.U8:
		return metals.TUint8
	case types.U16:
		return metals.TUint16
	case types.U32:
		return metals.TUint32
	case types.U64:
		return metals.TUint64
	case types.Usize:
		return metals.TUsize
	case types.F32:
		return metals.TF32
	case types.F64, types.UntypedFloat:
		return metals.TF64
	case types.RuneKind:
		return metals.TRune
	case types.BoolKind:
		return metals.TBool
	case types.StringKind:
		return metals.TStr
	case types.ErrorKind:
		l.usesError = true
		return &metals.Named{Name: "error"}
	case types.AnyKind:
		l.usesAny = true
		return &metals.Named{Name: "any"}
	case types.AnyPtrKind:
		return &metals.AnyPtr{}
	}
	l.fail("cannot lower the type %s", t)
	return nil
}

func (l *lowerer) metalsFnType(t *types.Func) *metals.FnType {
	out := &metals.FnType{Async: t.Async}
	for _, param := range t.Params {
		out.Params = append(out.Params, l.metalsType(param))
	}
	if !isVoid(t.Result) {
		out.Result = l.metalsType(t.Result)
	}
	return out
}

// metalsUnion lowers a union to METALS' own, which is the same idea with
// the same rules: every member at offset zero, the size the widest
// member's, and nothing recording which one is live.
func (l *lowerer) metalsUnion(t *types.Union) metals.Type {
	if foreign := l.foreignOf[t.Module]; foreign != nil {
		return l.importType(foreign, t.Name)
	}
	name := l.typeName(t.Name, t.Module, nil)
	if l.declared[name] == nil {
		decl := &metals.TypeDecl{Name: name, Union: &metals.UnionShape{}}
		l.declared[name] = decl
		l.module.Types = append(l.module.Types, decl)
		for _, member := range t.Members {
			decl.Union.Members = append(decl.Union.Members, metals.FieldDecl{
				Name: member.Name,
				Type: l.metalsType(member.Type),
			})
		}
	}
	return &metals.Named{Name: name}
}

// metalsStruct lowers a struct type: runtime-owned handles (Array, Mutex,
// File, ...) stay opaque, every other struct becomes a named record.
func (l *lowerer) metalsStruct(t *types.Struct) metals.Type {
	if isOpaqueHandle(t) {
		return &metals.Handle{}
	}
	// A struct another producer declared is imported rather than
	// rebuilt. The Gyro declaration for it was generated from the METALS
	// one, and rebuilding it from the generated text would put a second
	// type of the same shape in the program — right about every field and
	// wrong about which type it is.
	if foreign := l.foreignOf[t.Module]; foreign != nil {
		return l.importType(foreign, t.Name)
	}
	origin := t
	if t.Origin != nil {
		origin = t.Origin
	}
	name := l.typeName(origin.Name, t.Module, t.Args)
	if l.declared[name] == nil {
		decl := &metals.TypeDecl{Name: name, Record: &metals.RecordShape{}}
		l.declared[name] = decl
		l.module.Types = append(l.module.Types, decl)
		for _, field := range t.Fields {
			decl.Record.Fields = append(decl.Record.Fields, metals.FieldDecl{
				Name: field.Name,
				Type: l.metalsType(field.Type),
			})
		}
	}
	return &metals.Named{Name: name}
}

func (l *lowerer) metalsEnum(t *types.Enum) metals.Type {
	origin := t
	if t.Origin != nil {
		origin = t.Origin
	}
	name := l.typeName(origin.Name, t.Module, t.Args)
	if l.declared[name] == nil {
		decl := &metals.TypeDecl{Name: name, Enum: &metals.EnumShape{}}
		l.declared[name] = decl
		l.module.Types = append(l.module.Types, decl)
		for _, variant := range t.Variants {
			payload := make([]metals.Type, len(variant.Payload))
			for i, typ := range variant.Payload {
				payload[i] = l.metalsType(typ)
			}
			decl.Enum.Variants = append(decl.Enum.Variants, metals.VariantDecl{
				Name:    variant.Name,
				Payload: payload,
			})
		}
	}
	return &metals.Named{Name: name}
}

// ---------------------------------------------------------------------------
// The error enum
// ---------------------------------------------------------------------------

// collectErrorTypes enumerates every struct in the program that satisfies
// the structural error contract. Whole-program lowering makes error a
// closed sum: variant 0 is the runtime's Native(str), then one variant
// per concrete error type.
func (l *lowerer) collectErrorTypes() {
	for _, module := range l.prog.Modules {
		for _, decl := range module.Tree.Decls {
			structDecl, ok := decl.(*ast.StructDecl)
			if !ok {
				continue
			}
			t := l.info.Structs[structDecl]
			if t == nil || len(t.TypeParams) > 0 || !isErrorStruct(t) {
				continue
			}
			l.errVariant[t] = len(l.errTypes) + 1
			l.errTypes = append(l.errTypes, t)
		}
	}
}

// isErrorStruct mirrors sema's structural test: a message(): string
// method with a receiver and no generics.
func isErrorStruct(t *types.Struct) bool {
	method := t.Methods["message"]
	if method == nil || method.Recv == types.NoRecv || len(method.TypeParams) > 0 {
		return false
	}
	return len(method.Sig.Params) == 0 && types.Identical(method.Sig.Result, types.String)
}

// finishErrorType emits the %error declaration once anything mentioned it.
func (l *lowerer) finishErrorType() {
	if !l.usesError || l.declared["error"] != nil {
		return
	}
	decl := &metals.TypeDecl{Name: "error", Enum: &metals.EnumShape{
		Variants: []metals.VariantDecl{{Name: "Native", Payload: []metals.Type{metals.TStr}}},
	}}
	l.declared["error"] = decl
	l.module.Types = append(l.module.Types, decl)
	for _, t := range l.errTypes {
		payload := l.metalsType(t)
		decl.Enum.Variants = append(decl.Enum.Variants, metals.VariantDecl{
			Name:    t.Name,
			Payload: []metals.Type{payload},
		})
	}
}

// errorVariantOf returns the %error variant index for a concrete error
// struct.
func (l *lowerer) errorVariantOf(t *types.Struct) int {
	if index, ok := l.errVariant[t]; ok {
		return index
	}
	l.fail("%s is used as an error but was not collected as one", t)
	return 0
}

// ---------------------------------------------------------------------------
// Work queue and imports
// ---------------------------------------------------------------------------

// ensureFunc queues a bodied function for lowering and returns its METALS
// name. Natives never come through here; their imports register at the
// call site, where the concrete signature is recorded.
func (l *lowerer) ensureFunc(decl *ast.FuncDecl, bindings map[string]types.Type) string {
	name := l.funcName(decl, bindings)
	if decl.Body == nil {
		l.fail("@%s has no body and no recorded call signature", name)
	}
	if !l.lowered[name] {
		l.lowered[name] = true
		l.queue = append(l.queue, workItem{name: name, decl: decl, bindings: bindings})
	}
	return name
}

func (l *lowerer) drain() {
	for len(l.queue) > 0 {
		item := l.queue[0]
		l.queue = l.queue[1:]
		l.lowerFunc(item)
	}
}

// ensureNativeImport declares a gyro:rt intrinsic with the concrete
// signature recorded at a call site. Methods take their receiver as a
// leading parameter; runtime-backed receivers are handles.
func (l *lowerer) ensureNativeImport(name string, decl *ast.FuncDecl, sig *types.Func) {
	if l.imports[name] != nil {
		return
	}
	lowered := l.metalsFnType(sig)
	if decl.Recv != nil {
		// A runtime object is a handle; an extension of a built-in type
		// takes the value itself as its receiver.
		receiver := metals.Type(&metals.Handle{})
		if builtin, ok := types.Builtin[decl.Owner]; ok {
			receiver = l.metalsType(builtin)
		}
		lowered.Params = append([]metals.Type{receiver}, lowered.Params...)
	}
	l.imports[name] = lowered
}

// ensureImport registers a synthesized runtime intrinsic such as
// println<i32> or str<bool>.
func (l *lowerer) ensureImport(name string, sig *metals.FnType) {
	if l.imports[name] == nil {
		l.imports[name] = sig
	}
}

// ensureForeignImport declares a function another language's module
// exports. Unlike a runtime intrinsic it names a real module, and the
// linker checks the signature here against the one that module actually
// declares.
//
// A header's function is the exception: the module read from a header
// exports nothing, it imports the function from the native library, and
// the program imports it from there too. What the generated declaration
// says is checked against what the header's prototype said, because the
// native library will not.
func (l *lowerer) ensureForeignImport(foreign *metals.Module, name string, sig *types.Func) {
	if l.imports[name] != nil {
		return
	}
	module := foreign.Name
	if sig.Variadic {
		l.imports[name] = l.variadicFnType(sig)
	} else {
		l.imports[name] = l.metalsFnType(sig)
	}
	if native, declared := nativeImport(foreign, name); declared != nil {
		if !metals.TypesEqual(l.imports[name], declared) {
			l.fail("%s is declared %s here and %s by %s; the declaration was generated from the header",
				name, l.imports[name], declared, foreign.Name)
		}
		module = native
	}
	l.importFrom[name] = module
}

// nativeImport finds the native library a module imports a function from.
func nativeImport(foreign *metals.Module, name string) (string, *metals.FnType) {
	for _, imp := range foreign.Imports {
		if !metals.NativeLibrary(imp.Module) {
			continue
		}
		for _, fn := range imp.Funcs {
			if fn.Name == name {
				return imp.Module, fn.Sig
			}
		}
	}
	return "", nil
}

// variadicFnType is a C variadic function's real signature: the declared
// parameters, then the addresses of what came after them and how many
// there are. The `...anyptr` the generated header writes is a spelling
// for those two — METALS has no variadic call, and the pack is built at
// the call site. See docs/decisions.md, "Varargs are an array a caller
// builds".
func (l *lowerer) variadicFnType(sig *types.Func) *metals.FnType {
	out := &metals.FnType{Async: sig.Async}
	for _, param := range sig.Params[:len(sig.Params)-1] {
		out.Params = append(out.Params, l.metalsType(param))
	}
	out.Params = append(out.Params, varargSlotsType(), varargCountType())
	if !isVoid(sig.Result) {
		out.Result = l.metalsType(sig.Result)
	}
	return out
}

// The two types the pack is passed as: a pointer to the first erased
// address, and a count. Both are what internal/cc's `void **` and `long`
// become, because they are the same two parameters.
func varargSlotsType() metals.Type {
	return &metals.Opt{Elem: &metals.Ptr{Elem: &metals.Opt{Elem: &metals.AnyPtr{}}}}
}

func varargCountType() metals.Type { return metals.TInt64 }

// importType registers a type another producer's module declares and
// returns the reference to it. Everything the shape names comes along:
// a record holding a %Point is unusable without %Point, and the module
// it belongs to is the same one.
func (l *lowerer) importType(foreign *metals.Module, name string) metals.Type {
	if l.importedTypes[name] != nil {
		return &metals.Named{Name: name}
	}
	decl := foreign.TypeEnv()[name]
	if decl == nil {
		l.fail("%s declares no type %%%s, and this program's declarations were generated from it",
			foreign.Name, name)
	}
	l.importedTypes[name] = decl
	l.importedFrom[name] = foreign.Name

	for _, member := range decl.Members() {
		l.importNamed(foreign, member.Type)
	}
	if decl.Enum != nil {
		for _, variant := range decl.Enum.Variants {
			for _, payload := range variant.Payload {
				l.importNamed(foreign, payload)
			}
		}
	}
	return &metals.Named{Name: name}
}

// importNamed imports every named type a type mentions.
func (l *lowerer) importNamed(foreign *metals.Module, t metals.Type) {
	switch t := t.(type) {
	case *metals.Named:
		l.importType(foreign, t.Name)
	case *metals.Ptr:
		l.importNamed(foreign, t.Elem)
	case *metals.Opt:
		l.importNamed(foreign, t.Elem)
	case *metals.SliceOf:
		l.importNamed(foreign, t.Elem)
	case *metals.ArrayOf:
		l.importNamed(foreign, t.Elem)
	case *metals.ChanOf:
		l.importNamed(foreign, t.Elem)
	case *metals.FutureOf:
		l.importNamed(foreign, t.Result)
	case *metals.TupleOf:
		for _, elem := range t.Elems {
			l.importNamed(foreign, elem)
		}
	case *metals.FnType:
		for _, param := range t.Params {
			l.importNamed(foreign, param)
		}
		if t.Result != nil {
			l.importNamed(foreign, t.Result)
		}
	case *metals.CodeType:
		for _, param := range t.Params {
			l.importNamed(foreign, param)
		}
		if t.Result != nil {
			l.importNamed(foreign, t.Result)
		}
	}
}

func (l *lowerer) attachImports() {
	// Grouped by module, and the groups ordered, so the printed IR is the
	// same on every run.
	groups := map[string]*metals.Import{}
	var order []string
	group := func(from string) *metals.Import {
		existing, ok := groups[from]
		if !ok {
			existing = &metals.Import{Module: from}
			groups[from] = existing
			order = append(order, from)
		}
		return existing
	}

	for _, name := range sortedKeys(l.importedTypes) {
		into := group(l.importedFrom[name])
		into.Types = append(into.Types, l.importedTypes[name])
	}
	for _, name := range sortedKeys(l.imports) {
		from := l.importFrom[name]
		if from == "" {
			from = "gyro:rt"
		}
		group(from).Funcs = append(group(from).Funcs,
			metals.ImportedFunc{Name: name, Sig: l.imports[name]})
	}

	sort.Strings(order)
	for _, from := range order {
		l.module.Imports = append(l.module.Imports, groups[from])
	}
}

func sortedKeys[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

func isVoid(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return t == nil || (ok && basic.Kind == types.VoidKind)
}

func isErrorBasic(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind == types.ErrorKind
}

func isNullBasic(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind == types.NullKind
}

func isStringType(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind == types.StringKind
}

func (l *lowerer) syntheticName(prefix string) string {
	l.synthetic++
	return fmt.Sprintf("%s.%d", prefix, l.synthetic)
}
