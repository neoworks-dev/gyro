// Package sema checks a parsed Gyro file beyond its syntax.
//
// This first pass is name resolution: it collects the module scope, walks
// every body with lexical scoping, and reports names that resolve to
// nothing. It enforces the rules that need no types to check — there is no
// universe scope, so an unknown name is either a typo or a missing import;
// an inner block may shadow but the same block may not redeclare; `self` is
// only legal inside a method; decorator names come from a fixed set.
//
// Type checking, definite assignment, nullable narrowing and the leak
// analysis build on top of this pass and do not exist yet.
package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/loader"
)

// builtinTypes are the type names that are part of the formal syntax. They
// are spellable without an import; everything else has to come from one.
var builtinTypes = map[string]bool{
	"i8": true, "i16": true, "i32": true, "i64": true,
	"u8": true, "u16": true, "u32": true, "u64": true,
	"usize": true, "f32": true, "f64": true, "rune": true,
	"bool": true, "string": true, "void": true, "error": true,
}

// decorators is the fixed set of names the compiler accepts after `@`.
// Decorators configure compilation itself, so there is nothing an import
// could bind; an unknown name is an error rather than a lookup failure.
var decorators = map[string]bool{
	"comptime": true, "gpu": true, "concurrent": true, "distributed": true,
}

type symbolKind int

const (
	// symValue names a function, variable, constant or parameter.
	symValue symbolKind = iota
	// symType names a struct or enum.
	symType
	// symTypeParam names a generic parameter such as the T of max<T>.
	symTypeParam
	// symImport names an imported binding, which may stand for a value or a
	// type; without module loading the checker cannot tell which.
	symImport
	// symNamespace names a wildcard import: `import * as ast`.
	symNamespace
)

type symbol struct {
	kind   symbolKind
	offset int
	decl   ast.Node
	// exports holds the target module's export set for a namespace import,
	// when the loader supplied one.
	exports *Exports
}

// Exports is what one module offers its importers: each exported name with
// the kind of thing it is and the declaration behind it.
type Exports struct {
	names map[string]exportEntry
}

type exportEntry struct {
	kind symbolKind
	decl ast.Node
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

// checker holds the state of one file's check.
type checker struct {
	file   *diag.File
	bag    *diag.Bag
	module *scope
	scope  *scope

	// imports maps an import specifier to the exports of the module it
	// resolved to. It is nil when checking a single file without a loader,
	// and an entry is nil when that dependency failed to load.
	imports map[string]*Exports

	// enums maps an enum's local name to its declaration, so qualified
	// variant patterns and paths can be validated. Imported enums appear
	// under their bound name.
	enums map[string]*ast.EnumDecl

	// inMethod reports whether the innermost function declaration carries a
	// receiver, which is what makes `self` legal. Closures inherit it: a
	// closure inside a method captures self like any other local.
	inMethod bool
}

func newChecker(file *diag.File, bag *diag.Bag, imports map[string]*Exports) *checker {
	c := &checker{
		file:    file,
		bag:     bag,
		module:  newScope(nil),
		imports: imports,
		enums:   map[string]*ast.EnumDecl{},
	}
	c.scope = c.module
	return c
}

// Check resolves every name in one file and reports what fails to resolve.
// Without a loader, imports bind opaquely; use CheckProgram to validate
// them against what the imported modules export.
func Check(tree *ast.File, file *diag.File, bag *diag.Bag) {
	newChecker(file, bag, nil).run(tree)
}

// CheckProgram checks every module of a loaded program in dependency
// order, so each module's imports are validated against what its
// dependencies actually export.
func CheckProgram(program *loader.Program, bag *diag.Bag) {
	computed := map[*loader.Module]*Exports{}
	for _, module := range program.Modules {
		imports := map[string]*Exports{}
		for specifier, dependency := range module.Deps {
			imports[specifier] = computed[dependency]
		}
		computed[module] = newChecker(module.File, bag, imports).run(module.Tree)
	}
}

func (c *checker) run(tree *ast.File) *Exports {
	c.collectModule(tree)
	exports := c.collectExports(tree)
	c.checkDecls(tree)
	return exports
}

func (c *checker) errorf(offset int, format string, args ...any) {
	c.bag.Errorf(c.file.Position(offset), format, args...)
}

// declare adds a name to the current scope. Shadowing an outer scope is
// fine; redeclaring within the same scope is not.
func (c *checker) declare(name string, offset int, kind symbolKind, decl ast.Node) {
	c.declareSymbol(name, &symbol{kind: kind, offset: offset, decl: decl})
}

func (c *checker) declareSymbol(name string, sym *symbol) {
	if name == "" || name == "_" {
		return
	}
	if existing, ok := c.scope.names[name]; ok {
		previous := c.file.Position(existing.offset)
		c.errorf(sym.offset, "%s is already declared in this scope (at %d:%d)",
			name, previous.Line, previous.Column)
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
// Module scope
// ---------------------------------------------------------------------------

// collectModule declares every top-level name before any body is checked,
// so module-level declarations can reference each other in any order.
func (c *checker) collectModule(tree *ast.File) {
	for _, imp := range tree.Imports {
		c.collectImport(imp)
	}

	for _, decl := range tree.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			// A method lives under its owner, not in the module namespace.
			if d.Owner == "" {
				c.declare(d.Name, d.Offset, symValue, d)
			}
		case *ast.StructDecl:
			c.declare(d.Name, d.Offset, symType, d)
		case *ast.EnumDecl:
			c.declare(d.Name, d.Offset, symType, d)
			c.enums[d.Name] = d
		case *ast.VarDecl:
			for _, binding := range d.Names {
				c.declare(binding.Name, binding.Offset, symValue, d)
			}
		}
	}
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

		kind, decl := symImport, ast.Node(imp)
		if exports != nil {
			entry, ok := exports.names[name.Name]
			if !ok {
				c.errorf(name.Offset, "%q does not export %s", imp.Path, name.Name)
			} else {
				kind, decl = entry.kind, entry.decl
			}
		}

		c.declare(bound, name.Offset, kind, decl)
		if enum, ok := decl.(*ast.EnumDecl); ok {
			c.enums[bound] = enum
		}
	}
}

// collectExports gathers what this module offers importers. It runs after
// collectModule so export lists can look local names up in module scope.
func (c *checker) collectExports(tree *ast.File) *Exports {
	exports := &Exports{names: map[string]exportEntry{}}
	for _, decl := range tree.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			// Methods ride along with their owner type, not by name.
			if d.Exported && d.Owner == "" {
				exports.names[d.Name] = exportEntry{kind: symValue, decl: d}
			}
		case *ast.StructDecl:
			if d.Exported {
				exports.names[d.Name] = exportEntry{kind: symType, decl: d}
			}
		case *ast.EnumDecl:
			if d.Exported {
				exports.names[d.Name] = exportEntry{kind: symType, decl: d}
			}
		case *ast.VarDecl:
			if d.Exported {
				for _, binding := range d.Names {
					exports.names[binding.Name] = exportEntry{kind: symValue, decl: d}
				}
			}
		case *ast.ExportDecl:
			c.collectExportDecl(d, exports)
		}
	}
	return exports
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
			c.errorf(name.Offset, "%q does not export %s", decl.Path, name.Name)
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
		exports.names[bound] = exportEntry{kind: sym.kind, decl: sym.decl}
	}
}

// ---------------------------------------------------------------------------
// Declarations
// ---------------------------------------------------------------------------

func (c *checker) checkDecls(tree *ast.File) {
	for _, decl := range tree.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			c.checkFunc(d)
		case *ast.StructDecl:
			c.checkStruct(d)
		case *ast.EnumDecl:
			c.checkEnum(d)
		case *ast.VarDecl:
			c.checkDecorators(d.Decorators)
			c.checkVarValue(d)
		case *ast.ExportDecl:
			c.checkExport(d)
		}
	}
}

func (c *checker) checkExport(decl *ast.ExportDecl) {
	// A re-export names things in another module, which is not loaded here.
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
			c.errorf(decorator.Offset, "unknown decorator @%s; the decorators are @comptime, @gpu, @concurrent and @distributed",
				decoratorName(decorator))
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

func (c *checker) checkStruct(decl *ast.StructDecl) {
	c.checkDecorators(decl.Decorators)
	c.pushScope(func() {
		c.declareTypeParams(decl.TypeParams)
		for _, field := range decl.Fields {
			c.checkType(field.Type)
		}
	})
}

func (c *checker) checkEnum(decl *ast.EnumDecl) {
	c.checkDecorators(decl.Decorators)
	c.pushScope(func() {
		c.declareTypeParams(decl.TypeParams)
		for _, variant := range decl.Variants {
			for _, payload := range variant.Payload {
				c.checkType(payload)
			}
		}
	})
}

func (c *checker) checkFunc(decl *ast.FuncDecl) {
	c.checkDecorators(decl.Decorators)

	if decl.Owner != "" {
		owner := c.module.lookup(decl.Owner)
		if owner == nil {
			c.errorf(decl.Offset, "cannot attach a method to %s: no such type in this module", decl.Owner)
		} else if owner.kind != symType {
			c.errorf(decl.Offset, "cannot attach a method to %s: it is not a type", decl.Owner)
		}
	} else if decl.Recv != nil {
		c.errorf(decl.Recv.Offset, "a receiver needs an owning type: write fn Type::%s(self, ...)", decl.Name)
	}

	savedInMethod := c.inMethod
	c.inMethod = decl.Recv != nil
	defer func() { c.inMethod = savedInMethod }()

	c.pushScope(func() {
		c.declareTypeParams(decl.OwnerParams)
		c.declareTypeParams(decl.TypeParams)

		for _, param := range decl.Params {
			c.checkType(param.Type)
			if param.Default != nil {
				c.expr(param.Default)
			}
			c.declare(param.Name, param.Offset, symValue, decl)
		}
		for _, result := range decl.Results {
			c.checkType(result)
		}

		if decl.Body != nil {
			c.blockInPlace(decl.Body)
		}
	})
}

func (c *checker) declareTypeParams(params []ast.TypeParam) {
	for _, param := range params {
		c.declare(param.Name, param.Offset, symTypeParam, nil)
	}
}

// checkVarValue resolves a var declaration's annotation and initializer
// without declaring its names; declaration order differs between module
// scope (already collected) and locals (bound after the initializer).
func (c *checker) checkVarValue(decl *ast.VarDecl) {
	if decl.Type != nil {
		c.checkType(decl.Type)
	}
	if decl.Value != nil {
		c.expr(decl.Value)
	}
}
