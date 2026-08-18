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
		prog:       prog,
		info:       info,
		moduleOf:   map[ast.Node]*loader.Module{},
		tags:       map[string]string{},
		imports:    map[string]*metals.FnType{},
		declared:   map[string]*metals.TypeDecl{},
		reprs:      map[string]string{},
		globalOf:   map[*ast.VarDecl][]int{},
		lowered:    map[string]bool{},
		errVariant: map[*types.Struct]int{},
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
	mainName := l.ensureFunc(main, nil)
	l.drain()
	l.emitGlobalInit(mainName)
	l.finishErrorType()
	l.attachImports()
	return l.module, nil
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

	// imports collects the gyro:rt intrinsics the program actually uses.
	imports map[string]*metals.FnType

	// declared maps METALS type names to their emitted declarations.
	declared map[string]*metals.TypeDecl

	// reprs maps a type to the synthesized function that renders it, so a
	// recursive type reaches its own repr while it is still being built.
	reprs map[string]string

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
	case *types.Future:
		return &metals.FutureOf{Result: l.metalsType(t.Result)}
	case *types.Struct:
		return l.metalsStruct(t)
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

// metalsStruct lowers a struct type: runtime-owned handles (Array, Mutex,
// File, ...) stay opaque, every other struct becomes a named record.
func (l *lowerer) metalsStruct(t *types.Struct) metals.Type {
	if isOpaqueHandle(t) {
		return &metals.Handle{}
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
		lowered.Params = append([]metals.Type{&metals.Handle{}}, lowered.Params...)
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

func (l *lowerer) attachImports() {
	if len(l.imports) == 0 {
		return
	}
	names := make([]string, 0, len(l.imports))
	for name := range l.imports {
		names = append(names, name)
	}
	sort.Strings(names)
	imp := &metals.Import{Module: "gyro:rt"}
	for _, name := range names {
		imp.Funcs = append(imp.Funcs, metals.ImportedFunc{Name: name, Sig: l.imports[name]})
	}
	l.module.Imports = []*metals.Import{imp}
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
