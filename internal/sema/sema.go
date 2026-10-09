// Package sema checks a parsed Gyro program beyond its syntax.
//
// The checker resolves every name under lexical scoping and types every
// expression. There is no universe scope, so an unknown name is a typo or a
// missing import; literals are context-typed with i32/f64 defaults; there
// are no implicit conversions between typed values; generics are duck-typed,
// so a generic body is checked again at every distinct instantiation; a
// nullable value must be checked against null before it is used, and the
// check narrows it.
//
// It also runs three flow-sensitive checks over a function body, sharing one
// state that merges at branches: definite assignment, match exhaustiveness,
// and the leak analysis that warns when an allocation reaches the end of its
// block with nothing having freed or taken it.
package sema

import (
	"fmt"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/types"
	"math/big"
)

// decorators is the fixed set of names the compiler accepts after `@`.
// Decorators configure compilation itself, so there is nothing an import
// could bind; an unknown name is an error rather than a lookup failure.
var decorators = map[string]bool{
	"comptime": true, "gpu": true, "concurrent": true, "distributed": true,
	"extern": true,
}

type symbolKind int

const (
	// symValue names a function, variable, constant or parameter.
	symValue symbolKind = iota
	// symType names a struct or enum.
	symType
	// symTypeParam names a generic parameter such as the T of max<T>.
	symTypeParam
	// symImport names an imported binding whose module was not loaded, so
	// nothing is known about it; it types as Unknown.
	symImport
	// symNamespace names a wildcard import: `import * as ast`.
	symNamespace
)

type symbol struct {
	kind   symbolKind
	offset int
	decl   ast.Node
	// name is the source spelling. The scopes are keyed by it, but a
	// diagnostic reached through a symbol needs it back.
	name string
	// typ is the symbol's type: the value's type for symValue, the named
	// type itself for symType, the bound type for symTypeParam. nil means
	// not yet computed; Unknown means it failed or cannot be known.
	typ types.Type
	// fn is set for function symbols, including generic ones that have no
	// plain value type.
	fn *types.Method
	// needsAssign marks a local declared without a value: it holds nothing
	// until something writes to it, and definite assignment tracks it.
	needsAssign bool
	// openGuard is set for the value of a two-name receive. The value only
	// exists where the flag is true, so a read outside that check points at
	// the flag rather than at the declaration.
	openGuard     *symbol
	openGuardName string
	// holdsAllocation marks a local bound to an alloc call, which the leak
	// analysis follows until something settles it.
	holdsAllocation bool
	// intrinsic names the bundled function an imported symbol stands for —
	// "alloc", "free", "heap", "panic" or "spawn" — and is empty for everything else. The
	// analyses that care about these have to know the import they came
	// through, since the names are ordinary and can be aliased.
	intrinsic string
	// closureDepth is how many closures deep the symbol was declared. A name
	// used at a greater depth than it was declared at is captured.
	closureDepth int
	// origin is where a slice-typed value's storage lives, which is what
	// borrow checking follows back from a return.
	origin borrowOrigin
	// exports holds the target module's export set for a namespace import,
	// when the loader supplied one.
	exports *Exports
}

// Exports is what one module offers its importers.
type Exports struct {
	names map[string]*symbol
}

type scope struct {
	parent *scope
	names  map[string]*symbol
}

func newScope(parent *scope) *scope {
	return &scope{parent: parent, names: map[string]*symbol{}}
}

func (s *scope) lookup(name string) *symbol {
	for current := s; current != nil; current = current.parent {
		if sym, ok := current.names[name]; ok {
			return sym
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Program state
// ---------------------------------------------------------------------------

// program is the state shared by every module's checker: the caches that
// make generic instances canonical, and the environments that let a generic
// body be re-checked from any call site.
type program struct {
	bag *diag.Bag
	// info accumulates what later stages need; see Info.
	info *Info
	// envs maps each function declaration to its module's checker.
	envs map[*ast.FuncDecl]*checker
	// structInstances and enumInstances canonicalize generic instances, so
	// Pair<i32> is one type everywhere and identity is pointer identity.
	structInstances map[*types.Struct]map[string]*types.Struct
	enumInstances   map[*types.Enum]map[string]*types.Enum
	// instantiated caches which (declaration, type arguments) pairs have
	// been duck-checked, including in-progress ones so recursion converges.
	instantiated map[string]bool
	// inferredEffects memoizes what each rowless function's body does, and
	// the route it does it by, which is what a call to it contributes.
	inferredEffects map[*ast.FuncDecl]effectFacts
	// depth guards against infinitely growing instantiations such as a
	// generic function calling itself with Pair<T>.
	depth int
}

func newProgram(bag *diag.Bag) *program {
	return &program{
		bag:             bag,
		info:            newInfo(),
		envs:            map[*ast.FuncDecl]*checker{},
		structInstances: map[*types.Struct]map[string]*types.Struct{},
		enumInstances:   map[*types.Enum]map[string]*types.Enum{},
		instantiated:    map[string]bool{},
		inferredEffects: map[*ast.FuncDecl]effectFacts{},
	}
}

// funcContext is the function whose body is being checked.
type funcContext struct {
	result   types.Type
	async    bool
	inMethod bool
	selfType types.Type
	// borrows is the set of parameters the signature's row says the result
	// is a view into. Empty means the result must own what it holds.
	borrows map[*symbol]bool
	// borrowChecked is false while a generic body is checked without
	// concrete type arguments: whether `T` is a view is not known yet, so
	// the row is taken on faith until an instantiation makes it answerable.
	borrowChecked bool
	// selfSymbol stands for a method's receiver, which `&self` names. The
	// receiver is not a scope symbol — `self` is its own expression — so
	// borrows need something to point at.
	selfSymbol *symbol
	// generic marks a function with type parameters. Its borrows are
	// conditional: `&a` where `a: T` is load-bearing when T is a view and
	// vacuous when it is not, so whether the source or the result is a view
	// cannot be judged from the declaration.
	generic bool
}

// checker holds the state of one module's check.
type checker struct {
	program    *program
	file       *diag.File
	bag        *diag.Bag
	modulePath string
	module     *scope
	scope      *scope

	// imports maps an import specifier to the exports of the module it
	// resolved to. It is nil when checking a single file without a loader,
	// and an entry is nil when that dependency failed to load.
	imports map[string]*Exports

	// structTypes and enumTypes map local declarations to their types.
	structTypes map[*ast.StructDecl]*types.Struct
	unionTypes  map[*ast.UnionDecl]*types.Union
	enumTypes   map[*ast.EnumDecl]*types.Enum
	funcSigs    map[*ast.FuncDecl]*types.Method

	// consts records the values of untyped integer expressions, so a
	// literal can be checked to fit the type its context requires. It is
	// the program Info's map, shared by every module.
	consts map[ast.Expr]*big.Int

	// rec receives expression types and call resolutions: the shared
	// Info maps normally, an instance's maps during an instantiation
	// check, nil during a lenient generic check.
	rec *recorder

	// fn is the enclosing function, nil at module scope.
	fn *funcContext

	// facts holds the symbols currently narrowed from T? to T by a null
	// check on this control-flow path.
	facts map[*symbol]bool

	// flow is the definite-assignment state on this control-flow path.
	flow flow

	// loops is the stack of enclosing loops, which collect the states that
	// leave through `break`.
	loops []*loopFlow

	// guarded maps the `open` flag of a two-name receive to the values it
	// makes readable.
	guarded map[*symbol][]*symbol

	// allocScopes is the stack of blocks, each holding the allocations
	// declared in it that the leak analysis has yet to see settled.
	allocScopes [][]allocSite

	// allocFloor is the first entry of allocScopes belonging to the
	// function being checked, so a closure's return does not answer for the
	// allocations of the function it sits in.
	allocFloor int

	// reportedLeaks keeps one allocation from being reported at an early
	// exit and again at the end of its block.
	reportedLeaks map[*symbol]bool

	// importPaths maps an import specifier to the module path it resolved
	// to, which is how `alloc` is told from a local function of that name.
	importPaths map[string]string

	// closureDepth counts the closures enclosing the expression being
	// checked. A name used deeper than it was declared is captured.
	closureDepth int

	// captures is the stack of open closures, each collecting the outer
	// names its body reads.
	captures []*captureSet

	// closureCaptures keeps what each closure captured, so a `spawn` can ask
	// about the closure it was handed after that closure has been checked.
	closureCaptures map[*ast.ClosureExpr][]*symbol

	// lenient marks a generic body being checked without concrete type
	// arguments: operations involving a type parameter poison to Unknown
	// instead of erroring, because the real check happens per instantiation.
	lenient bool
}

func newChecker(p *program, file *diag.File, modulePath string, imports map[string]*Exports) *checker {
	c := &checker{
		program:         p,
		file:            file,
		bag:             p.bag,
		modulePath:      modulePath,
		module:          newScope(nil),
		imports:         imports,
		structTypes:     map[*ast.StructDecl]*types.Struct{},
		unionTypes:      map[*ast.UnionDecl]*types.Union{},
		enumTypes:       map[*ast.EnumDecl]*types.Enum{},
		funcSigs:        map[*ast.FuncDecl]*types.Method{},
		consts:          p.info.Consts,
		rec:             &recorder{types: p.info.Types, calls: p.info.Calls, locals: p.info.Locals},
		facts:           map[*symbol]bool{},
		flow:            newFlow(),
		guarded:         map[*symbol][]*symbol{},
		importPaths:     map[string]string{},
		reportedLeaks:   map[*symbol]bool{},
		closureCaptures: map[*ast.ClosureExpr][]*symbol{},
	}
	c.scope = c.module
	return c
}

// CheckProgram checks every module of a loaded program in dependency
// order, so each module's imports are validated against what its
// dependencies actually export. The returned Info is only meaningful
// when the bag stayed free of errors.
func CheckProgram(prog *loader.Program, bag *diag.Bag) *Info {
	p := newProgram(bag)
	computed := map[*loader.Module]*Exports{}
	checkers := make([]*checker, len(prog.Modules))
	for i, module := range prog.Modules {
		imports := map[string]*Exports{}
		paths := map[string]string{}
		for specifier, dependency := range module.Deps {
			imports[specifier] = computed[dependency]
			paths[specifier] = dependency.Path
		}
		c := newChecker(p, module.File, module.Path, imports)
		c.importPaths = paths
		computed[module] = c.run(module.Tree)
		checkers[i] = c
	}
	// Concrete effects are judged after every module has run, so a row sees
	// every same-module body resolved and every instantiation recorded.
	for i, module := range prog.Modules {
		checkers[i].checkModuleEffects(module.Tree)
	}
	return p.info
}

func (c *checker) run(tree *ast.File) *Exports {
	c.collectModule(tree)
	c.buildTypes(tree)
	exports := c.collectExports(tree)
	c.checkDecls(tree)
	return exports
}

func (c *checker) errorf(offset int, format string, args ...any) {
	c.bag.Add(diag.Diagnostic{
		Severity: diag.SeverityError,
		Code:     codes[format],
		Pos:      c.file.Position(offset),
		Message:  fmt.Sprintf(format, args...),
	})
}

// declare adds a name to the current scope. Shadowing an outer scope is
// fine; redeclaring within the same scope is not.
func (c *checker) declare(name string, offset int, kind symbolKind, decl ast.Node) *symbol {
	sym := &symbol{kind: kind, offset: offset, decl: decl}
	c.declareSymbol(name, sym)
	return sym
}

func (c *checker) declareValue(name string, offset int, decl ast.Node, typ types.Type) *symbol {
	sym := &symbol{kind: symValue, offset: offset, decl: decl, typ: typ}
	c.declareSymbol(name, sym)
	c.recordLocal(decl, name, typ)
	return sym
}

func (c *checker) declareSymbol(name string, sym *symbol) {
	sym.name = name
	sym.closureDepth = c.closureDepth
	if name == "" || name == "_" {
		return
	}
	if existing, ok := c.scope.names[name]; ok {
		c.bag.Add(diag.Diagnostic{
			Severity: diag.SeverityError,
			Code:     diag.EAlreadyDeclared,
			Pos:      c.file.Position(sym.offset),
			Message:  fmt.Sprintf("%s is already declared in this scope", name),
			Related: []diag.Span{{
				Pos:   c.file.Position(existing.offset),
				Label: "previous declaration here",
			}},
			Help: []string{"a name may shadow an outer scope, but not another name in the same one"},
		})
		return
	}
	c.scope.names[name] = sym
}

// pushScope enters a nested scope for the duration of fn.
func (c *checker) pushScope(fn func()) {
	saved := c.scope
	c.scope = newScope(saved)
	fn()
	c.scope = saved
}

// ---------------------------------------------------------------------------
// Module collection: names and type shells
// ---------------------------------------------------------------------------

// collectModule declares every top-level name before any type is resolved,
// so module-level declarations can reference each other in any order.
// Structs and enums get their type objects here — empty shells whose fields
// and variants are filled by buildTypes — because a field can point at a
// struct declared further down.
func (c *checker) collectModule(tree *ast.File) {
	for _, imp := range tree.Imports {
		c.collectImport(imp)
	}

	for _, decl := range tree.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			// A method lives under its owner, not in the module namespace
			// — unless the owner is a built-in, which has no declaration
			// to live under. Those are ordinary names, so they import.
			if d.Owner == "" || extendsBuiltin(d) {
				// The bundled module that declares an intrinsic uses it
				// under its own name, so it is tagged there as well as at
				// every import of it.
				c.declare(d.Name, d.Offset, symValue, d).intrinsic = bundledIntrinsic(c.modulePath, d.Name)
			}
		case *ast.StructDecl:
			shell := &types.Struct{
				Name:       d.Name,
				Module:     c.modulePath,
				TypeParams: makeTypeParams(d.TypeParams),
				Methods:    map[string]*types.Method{},
			}
			c.structTypes[d] = shell
			c.program.info.Structs[d] = shell
			c.declare(d.Name, d.Offset, symType, d).typ = shell
		case *ast.UnionDecl:
			shell := &types.Union{
				Name:    d.Name,
				Module:  c.modulePath,
				Methods: map[string]*types.Method{},
			}
			c.unionTypes[d] = shell
			c.program.info.Unions[d] = shell
			c.declare(d.Name, d.Offset, symType, d).typ = shell
		case *ast.EnumDecl:
			shell := &types.Enum{
				Name:       d.Name,
				Module:     c.modulePath,
				TypeParams: makeTypeParams(d.TypeParams),
				Methods:    map[string]*types.Method{},
			}
			c.enumTypes[d] = shell
			c.program.info.Enums[d] = shell
			c.declare(d.Name, d.Offset, symType, d).typ = shell
		case *ast.VarDecl:
			for _, binding := range d.Names {
				c.declare(binding.Name, binding.Offset, symValue, d)
			}
		}
	}
}

func makeTypeParams(params []ast.TypeParam) []*types.TypeParam {
	out := make([]*types.TypeParam, len(params))
	for i, param := range params {
		out[i] = &types.TypeParam{Name: param.Name}
	}
	return out
}

// bundledIntrinsic names the bundled function an import binds, so that the
// analyses recognize it through an alias and do not mistake a local function
// of the same name for it.
func bundledIntrinsic(modulePath, name string) string {
	switch modulePath {
	case stdlib.Path("mem"):
		if name == "alloc" || name == "free" || name == "heap" || name == "panic" {
			return name
		}
	case stdlib.Path("stdlib"):
		if name == "spawn" {
			return name
		}
	}
	return ""
}

func (c *checker) collectImport(imp *ast.Import) {
	exports := c.imports[imp.Path]

	if imp.Wildcard != "" {
		c.declareSymbol(imp.Wildcard, &symbol{
			kind:    symNamespace,
			offset:  imp.Offset,
			decl:    imp,
			exports: exports,
		})
	}

	for _, name := range imp.Names {
		bound := name.Name
		if name.Alias != "" {
			bound = name.Alias
		}

		local := &symbol{kind: symImport, offset: name.Offset, decl: imp, typ: types.Unknown}
		local.intrinsic = bundledIntrinsic(c.importPaths[imp.Path], name.Name)
		if exports != nil {
			entry, ok := exports.names[name.Name]
			if !ok {
				c.errorHintf(name.Offset, didYouMean(name.Name, exportedNames(exports)),
					"%q does not export %s", imp.Path, name.Name)
			} else {
				local.kind = entry.kind
				local.decl = entry.decl
				local.typ = entry.typ
				local.fn = entry.fn
			}
		}
		c.declareSymbol(bound, local)
	}
}

// ---------------------------------------------------------------------------
// Exports
// ---------------------------------------------------------------------------

// collectExports gathers what this module offers importers. It runs after
// buildTypes so the entries carry real types.
func (c *checker) collectExports(tree *ast.File) *Exports {
	exports := &Exports{names: map[string]*symbol{}}
	for _, decl := range tree.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			// Methods ride along with their owner type, not by name. An
			// extension of a built-in has no such owner and exports like
			// any other name.
			if d.Exported && (d.Owner == "" || extendsBuiltin(d)) {
				c.exportLocal(exports, d.Name)
			}
		case *ast.StructDecl:
			if d.Exported {
				c.exportLocal(exports, d.Name)
			}
		case *ast.UnionDecl:
			if d.Exported {
				c.exportLocal(exports, d.Name)
			}
		case *ast.EnumDecl:
			if d.Exported {
				c.exportLocal(exports, d.Name)
			}
		case *ast.VarDecl:
			if d.Exported {
				for _, binding := range d.Names {
					c.exportLocal(exports, binding.Name)
				}
			}
		case *ast.ExportDecl:
			c.collectExportDecl(d, exports)
		}
	}
	return exports
}

func (c *checker) exportLocal(exports *Exports, name string) {
	if sym := c.module.lookup(name); sym != nil {
		exports.names[name] = sym
	}
}

func (c *checker) collectExportDecl(decl *ast.ExportDecl, exports *Exports) {
	if decl.Path == "" {
		c.collectLocalExportList(decl, exports)
		return
	}

	// A re-export pulls from another module's exports; without the loader
	// (or with a failed dependency) there is nothing to validate against.
	source := c.imports[decl.Path]
	if source == nil {
		return
	}

	if decl.Star {
		for name, entry := range source.names {
			exports.names[name] = entry
		}
		return
	}

	for _, name := range decl.Names {
		entry, ok := source.names[name.Name]
		if !ok {
			c.errorHintf(name.Offset, didYouMean(name.Name, exportedNames(source)),
				"%q does not export %s", decl.Path, name.Name)
			continue
		}
		bound := name.Name
		if name.Alias != "" {
			bound = name.Alias
		}
		exports.names[bound] = entry
	}
}

func (c *checker) collectLocalExportList(decl *ast.ExportDecl, exports *Exports) {
	for _, name := range decl.Names {
		// A missing name is reported by checkExport; nothing to add here.
		sym := c.module.lookup(name.Name)
		if sym == nil {
			continue
		}
		bound := name.Name
		if name.Alias != "" {
			bound = name.Alias
		}
		exports.names[bound] = sym
	}
}

// ---------------------------------------------------------------------------
// Declarations
// ---------------------------------------------------------------------------

func (c *checker) checkDecls(tree *ast.File) {
	for _, decl := range tree.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			c.checkFuncDecl(d)
		case *ast.StructDecl:
			c.checkDecorators(d.Decorators)
			c.checkExternStruct(d)
		case *ast.UnionDecl:
			c.checkDecorators(d.Decorators)
			c.checkUnionDecl(d)
		case *ast.EnumDecl:
			c.checkDecorators(d.Decorators)
		case *ast.VarDecl:
			// Module-level values were typed by buildTypes; only the
			// decorators remain.
			c.checkDecorators(d.Decorators)
		case *ast.ExportDecl:
			c.checkExport(d)
		}
	}
}

func (c *checker) checkExport(decl *ast.ExportDecl) {
	// A re-export names things in another module, checked in collectExports.
	if decl.Path != "" || decl.Star {
		return
	}
	for _, name := range decl.Names {
		if c.module.lookup(name.Name) == nil {
			c.errorf(name.Offset, "cannot export %s: it is not declared in this module", name.Name)
		}
	}
}

func (c *checker) checkDecorators(list []*ast.Decorator) {
	for _, decorator := range list {
		if !decorator.Name.IsPlainName() || !decorators[decorator.Name.Segments[0].Name] {
			c.errorf(decorator.Offset, "unknown decorator @%s; the decorators are @comptime, @gpu, @concurrent, @distributed and @extern",
				decoratorName(decorator))
			continue
		}
		// @extern's arguments name a library and a symbol, which are not
		// expressions to type; checkExternFunc reads them itself.
		if decorator.Name.Segments[0].Name == "extern" {
			continue
		}
		for _, arg := range decorator.Args {
			c.expr(arg.Value)
		}
	}
}

func decoratorName(decorator *ast.Decorator) string {
	names := decorator.Name.Names()
	joined := names[0]
	for _, name := range names[1:] {
		joined += "::" + name
	}
	return joined
}
