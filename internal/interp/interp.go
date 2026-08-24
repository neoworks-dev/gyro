package interp

import (
	"fmt"
	"io"
	"os"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/types"
)

// env is one lexical scope. Cells are addressable so & can point at them.
type env struct {
	parent *env
	names  map[string]*Value
}

func newEnv(parent *env) *env {
	return &env{parent: parent, names: map[string]*Value{}}
}

func (e *env) lookup(name string) *Value {
	for current := e; current != nil; current = current.parent {
		if cell, ok := current.names[name]; ok {
			return cell
		}
	}
	return nil
}

func (e *env) bind(name string, v Value) *Value {
	cell := new(Value)
	*cell = v
	if name != "" && name != "_" {
		e.names[name] = cell
	}
	return cell
}

// funcDef is a callable: a declared function, possibly a native one.
type funcDef struct {
	decl   *ast.FuncDecl
	module *moduleEnv
	native nativeFunc
}

// typeDef is a struct or enum declaration with its defining module, which
// is where its methods live.
type typeDef struct {
	name       string
	module     *moduleEnv
	structDecl *ast.StructDecl
	enumDecl   *ast.EnumDecl
}

// binding is one name in a module's scope or export set.
type binding struct {
	cell *Value
	fn   *funcDef
	typ  *typeDef
	ns   *moduleEnv
}

type moduleEnv struct {
	path    string
	file    *diag.File
	scope   map[string]*binding
	methods map[string]map[string]*funcDef
	exports map[string]*binding
}

func (m *moduleEnv) methodOf(typeName, method string) *funcDef {
	if table, ok := m.methods[typeName]; ok {
		return table[method]
	}
	return nil
}

// interp is one program run.
type interp struct {
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	args    []string
	modules map[*loader.Module]*moduleEnv
	// runtimeTypes are the bundled types whose values the runtime owns —
	// Array, Mutex, File — keyed by name. A runtime value carries no
	// declaration of its own, so this is how a method written beside the
	// declaration is found.
	runtimeTypes map[string]*typeDef
	// exit aborts the process on panic; tests replace it.
	exit func(code int)
}

// gyroPanic is a programmer error in the interpreted program. It aborts.
type gyroPanic struct {
	message  string
	position diag.Position
}

// Panic is returned by Run when the program aborted.
type Panic struct {
	Message  string
	Position diag.Position
}

func (p *Panic) Error() string {
	return fmt.Sprintf("panic: %s\n  at %s", p.Message, p.Position)
}

// exitSignal carries os.exit out of the program. It is not a gyroPanic:
// nothing is reported, and deferred calls do not run on the way out.
type exitSignal struct {
	code int
}

// Exit is returned by Run when the program called os.exit. It is a
// termination, not a failure; the driver owns turning it into the
// process exit code.
type Exit struct {
	Code int
}

func (e *Exit) Error() string {
	return fmt.Sprintf("exit %d", e.Code)
}

// Run executes a loaded, checked program from its root module's main.
// args is the command line as main would see it, args[0] included; it is
// ignored when main declares no parameters.
func Run(program *loader.Program, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	in := &interp{
		stdin:        stdin,
		stdout:       stdout,
		stderr:       stderr,
		args:         args,
		modules:      map[*loader.Module]*moduleEnv{},
		runtimeTypes: map[string]*typeDef{},
		exit:         os.Exit,
	}
	return in.run(program)
}

// entryArgs supplies main's arguments, or none when it takes none. Sema
// has already held the signature to those two shapes.
func (in *interp) entryArgs(def *funcDef) []Value {
	if def.decl == nil || len(def.decl.Params) == 0 {
		return nil
	}
	elems := make([]Value, len(in.args))
	for i, arg := range in.args {
		elems[i] = StringVal(arg)
	}
	return []Value{SliceVal{Elems: elems}}
}

func (in *interp) run(program *loader.Program) (err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if signal, ok := r.(exitSignal); ok {
			err = &Exit{Code: signal.code}
			return
		}
		p, ok := r.(gyroPanic)
		if !ok {
			panic(r)
		}
		err = &Panic{Message: p.message, Position: p.position}
		fmt.Fprintf(in.stderr, "panic: %s\n  at %s\n", p.message, p.position)
	}()

	for _, module := range program.Modules {
		in.setupModule(module)
	}
	for _, module := range program.Modules {
		in.initModule(module)
	}

	root := in.modules[program.Root]
	mainBinding, ok := root.scope["main"]
	if !ok || mainBinding.fn == nil {
		return fmt.Errorf("%s has no fn main", root.file.Name)
	}

	result := in.callFunction(mainBinding.fn, nil, in.entryArgs(mainBinding.fn))
	if future, ok := result.(*FutureVal); ok {
		future.await()
	}
	return nil
}

// setupModule declares every top-level name so that initialization and
// bodies can reference the module in any order.
func (in *interp) setupModule(module *loader.Module) {
	m := &moduleEnv{
		path:    module.Path,
		file:    module.File,
		scope:   map[string]*binding{},
		methods: map[string]map[string]*funcDef{},
		exports: map[string]*binding{},
	}
	in.modules[module] = m

	for _, decl := range module.Tree.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			def := &funcDef{decl: d, module: m}
			if d.Body == nil {
				def.native = nativeFor(module.Path, d.Owner, d.Name)
			}
			// A method on a built-in type is an ordinary module-scope
			// name, since the type it extends has no declaration to hang
			// it off; that is also what makes it travel by import.
			if d.Owner != "" && types.Builtin[d.Owner] == nil {
				table := m.methods[d.Owner]
				if table == nil {
					table = map[string]*funcDef{}
					m.methods[d.Owner] = table
				}
				table[d.Name] = def
				continue
			}
			m.scope[d.Name] = &binding{fn: def}
		case *ast.StructDecl:
			def := &typeDef{name: d.Name, module: m, structDecl: d}
			m.scope[d.Name] = &binding{typ: def}
			// A bundled struct with no fields is an opaque handle: its
			// values come from the runtime and carry no declaration.
			if stdlib.IsBundled(module.Path) && len(d.Fields) == 0 {
				in.runtimeTypes[d.Name] = def
			}
		case *ast.EnumDecl:
			m.scope[d.Name] = &binding{typ: &typeDef{name: d.Name, module: m, enumDecl: d}}
		case *ast.VarDecl:
			for _, name := range d.Names {
				if !name.Blank {
					cell := new(Value)
					*cell = UninitVal{}
					m.scope[name.Name] = &binding{cell: cell}
				}
			}
		}
	}
}

// initModule binds imports, computes exports, and evaluates module-level
// values. Dependency order guarantees every dependency is complete.
func (in *interp) initModule(module *loader.Module) {
	m := in.modules[module]

	for _, imp := range module.Tree.Imports {
		dep := in.modules[module.Deps[imp.Path]]
		if dep == nil {
			continue
		}
		if imp.Wildcard != "" {
			m.scope[imp.Wildcard] = &binding{ns: dep}
		}
		for _, name := range imp.Names {
			bound := name.Name
			if name.Alias != "" {
				bound = name.Alias
			}
			if entry, ok := dep.exports[name.Name]; ok {
				m.scope[bound] = entry
			}
		}
	}

	in.collectExports(module, m)

	// Module values evaluate in source order, in the module scope.
	f := &frame{in: in, module: m}
	env := newEnv(nil)
	for _, decl := range module.Tree.Decls {
		d, ok := decl.(*ast.VarDecl)
		if !ok || d.Value == nil {
			continue
		}
		value := f.eval(d.Value, env)
		f.bindDeclared(d, value, func(name string, v Value) {
			if entry, ok := m.scope[name]; ok && entry.cell != nil {
				*entry.cell = v
			}
		})
	}
}

func (in *interp) collectExports(module *loader.Module, m *moduleEnv) {
	for _, decl := range module.Tree.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Exported && (d.Owner == "" || types.Builtin[d.Owner] != nil) {
				m.exports[d.Name] = m.scope[d.Name]
			}
		case *ast.StructDecl:
			if d.Exported {
				m.exports[d.Name] = m.scope[d.Name]
			}
		case *ast.EnumDecl:
			if d.Exported {
				m.exports[d.Name] = m.scope[d.Name]
			}
		case *ast.VarDecl:
			if d.Exported {
				for _, name := range d.Names {
					if !name.Blank {
						m.exports[name.Name] = m.scope[name.Name]
					}
				}
			}
		case *ast.ExportDecl:
			in.collectExportDecl(module, m, d)
		}
	}
}

func (in *interp) collectExportDecl(module *loader.Module, m *moduleEnv, d *ast.ExportDecl) {
	if d.Path != "" {
		dep := in.modules[module.Deps[d.Path]]
		if dep == nil {
			return
		}
		if d.Star {
			for name, entry := range dep.exports {
				m.exports[name] = entry
			}
			return
		}
		for _, name := range d.Names {
			bound := name.Name
			if name.Alias != "" {
				bound = name.Alias
			}
			if entry, ok := dep.exports[name.Name]; ok {
				m.exports[bound] = entry
			}
		}
		return
	}
	for _, name := range d.Names {
		bound := name.Name
		if name.Alias != "" {
			bound = name.Alias
		}
		if entry, ok := m.scope[name.Name]; ok {
			m.exports[bound] = entry
		}
	}
}

func (in *interp) panicf(m *moduleEnv, offset int, format string, args ...any) {
	panic(gyroPanic{
		message:  fmt.Sprintf(format, args...),
		position: m.file.Position(offset),
	})
}

// abortFromGoroutine implements "a panic aborts the whole process" for
// panics raised on spawned threads and async bodies.
func (in *interp) abortFromGoroutine(r any) {
	if signal, ok := r.(exitSignal); ok {
		in.exit(signal.code)
		return
	}
	p, ok := r.(gyroPanic)
	if !ok {
		panic(r)
	}
	fmt.Fprintf(in.stderr, "panic: %s\n  at %s\n", p.message, p.position)
	in.exit(1)
}

// lookupBinding finds a name in the module scope shape.
func (m *moduleEnv) lookupBinding(name string) *binding {
	return m.scope[name]
}
