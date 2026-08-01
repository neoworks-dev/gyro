// Package loader turns one root file into a parsed program. It resolves
// import specifiers, reads and parses each module exactly once, rejects
// circular imports with a trace, and returns the modules in dependency
// order so later passes can walk them front to back.
package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/parser"
	"github.com/gyro-lang/gyro/internal/stdlib"
)

// Module is one loaded, parsed source file.
type Module struct {
	// Path identifies the module: an absolute file path on disk, or the
	// specifier itself for bundled modules such as "@gyro/stdlib".
	Path string
	File *diag.File
	Tree *ast.File
	// Deps maps each import specifier written in this module to the module
	// it resolved to. Re-export paths count as imports.
	Deps map[string]*Module
}

// Program is a root module plus everything it transitively imports.
type Program struct {
	Root *Module
	// Modules holds every loaded module in dependency order: a module's
	// dependencies come before it, and the root comes last.
	Modules []*Module
}

// Resolver turns an import specifier into a module identity and its source.
// fromDir is the directory of the importing file, or "" when the importer
// is not on disk. It is an interface so other providers can slot in later:
// packages under .stack/, and foreign-language modules once METALS exists.
type Resolver interface {
	Resolve(fromDir string, specifier string) (path string, source string, err error)
}

// Load reads the program rooted at rootPath using the default resolver.
// The returned error covers only failure to read the root; everything
// found inside the graph is reported through the bag.
func Load(rootPath string, bag *diag.Bag) (*Program, error) {
	return LoadWith(diskResolver{}, rootPath, bag)
}

// LoadWith is Load with a caller-supplied resolver.
func LoadWith(resolver Resolver, rootPath string, bag *diag.Bag) (*Program, error) {
	source, err := os.ReadFile(rootPath)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}

	state := &loadState{
		resolver: resolver,
		bag:      bag,
		modules:  map[string]*Module{},
		loading:  map[string]bool{},
	}
	root := state.add(absolute, string(source))
	return &Program{Root: root, Modules: state.order}, nil
}

type loadState struct {
	resolver Resolver
	bag      *diag.Bag
	modules  map[string]*Module
	// loading marks modules whose imports are still being resolved; hitting
	// one again means the graph has a cycle.
	loading map[string]bool
	// chain is the stack of in-progress module paths, for the cycle trace.
	chain []string
	order []*Module
}

// add parses a module and recursively loads its dependencies, appending the
// module to the order once everything below it is in.
func (s *loadState) add(path, source string) *Module {
	module := &Module{
		Path: path,
		File: diag.NewFile(displayPath(path), source),
		Deps: map[string]*Module{},
	}
	s.modules[path] = module
	s.loading[path] = true
	s.chain = append(s.chain, path)

	module.Tree = parser.ParseFile(module.File, s.bag)
	s.resolveDependencies(module)

	s.chain = s.chain[:len(s.chain)-1]
	delete(s.loading, path)
	s.order = append(s.order, module)
	return module
}

func (s *loadState) resolveDependencies(module *Module) {
	for _, imp := range module.Tree.Imports {
		s.dependency(module, imp.Path, imp.Offset)
	}
	for _, decl := range module.Tree.Decls {
		if export, ok := decl.(*ast.ExportDecl); ok && export.Path != "" {
			s.dependency(module, export.Path, export.Offset)
		}
	}
}

func (s *loadState) dependency(from *Module, specifier string, offset int) {
	if specifier == "" || from.Deps[specifier] != nil {
		return
	}

	fromDir := ""
	if !strings.HasPrefix(from.Path, "@") {
		fromDir = filepath.Dir(from.Path)
	}

	path, source, err := s.resolver.Resolve(fromDir, specifier)
	if err != nil {
		s.bag.Errorf(from.File.Position(offset), "%s", err)
		return
	}
	if s.loading[path] {
		s.bag.Errorf(from.File.Position(offset), "circular import: %s", s.cycleTrace(path))
		return
	}

	target, ok := s.modules[path]
	if !ok {
		target = s.add(path, source)
	}
	from.Deps[specifier] = target
}

// cycleTrace renders the loading chain from the repeated module back to
// itself: "a.gyro -> b.gyro -> a.gyro".
func (s *loadState) cycleTrace(path string) string {
	start := 0
	for index, entry := range s.chain {
		if entry == path {
			start = index
			break
		}
	}

	var names []string
	for _, entry := range s.chain[start:] {
		names = append(names, s.modules[entry].File.Name)
	}
	names = append(names, s.modules[path].File.Name)
	return strings.Join(names, " -> ")
}

// displayPath shortens an absolute path to be relative to the working
// directory when that is a strict shortening; diagnostics stay readable
// without losing the file's identity.
func displayPath(path string) string {
	if strings.HasPrefix(path, "@") {
		return path
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return path
	}
	relative, err := filepath.Rel(workingDir, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return path
	}
	return relative
}

// diskResolver implements the resolution rules the docs specify: @gyro/...
// is the bundled standard library, ./x and ../x are files beside the
// importer, and a bare name is a .stack/ package — unimplemented until the
// Stack package manager decides the layout.
type diskResolver struct{}

func (diskResolver) Resolve(fromDir, specifier string) (string, string, error) {
	if name, ok := strings.CutPrefix(specifier, "@gyro/"); ok {
		source, found := stdlib.Source(name)
		if !found {
			return "", "", fmt.Errorf("no stdlib module %q; the bundled modules are @gyro/{%s}",
				specifier, strings.Join(stdlib.Modules(), ", "))
		}
		return specifier, source, nil
	}

	if strings.HasPrefix(specifier, "./") || strings.HasPrefix(specifier, "../") {
		return resolveRelative(fromDir, specifier)
	}

	if strings.HasPrefix(specifier, "@") {
		return "", "", fmt.Errorf("cannot resolve %q: only the @gyro namespace is bundled", specifier)
	}
	return "", "", fmt.Errorf("cannot resolve %q: packages under .stack/ are not implemented yet", specifier)
}

func resolveRelative(fromDir, specifier string) (string, string, error) {
	if fromDir == "" {
		return "", "", fmt.Errorf("cannot resolve %q: bundled modules import only @gyro modules", specifier)
	}

	path := filepath.Join(fromDir, specifier)
	if filepath.Ext(path) == "" {
		path += ".gyro"
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}

	source, err := os.ReadFile(absolute)
	if err != nil {
		return "", "", fmt.Errorf("cannot load %q: %v", specifier, err)
	}
	return absolute, string(source), nil
}
