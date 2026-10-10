package stdlib

import (
	"sync"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/parser"
)

// Index summarizes the bundled modules for diagnostics: which module exports
// a name, and which types carry a method of a name. It lets a checker turn
// "undeclared name println" into the import that would bind it.
type Index struct {
	// Exports maps an exported name to the specifiers that export it.
	Exports map[string][]string
	// Methods maps a method name to the types declaring it.
	Methods map[string][]string
}

var (
	indexOnce sync.Once
	index     Index
)

// Lookup returns the index, parsing the declaration files on first use.
func Lookup() Index {
	indexOnce.Do(buildIndex)
	return index
}

func buildIndex() {
	index = Index{Exports: map[string][]string{}, Methods: map[string][]string{}}
	for _, name := range Modules() {
		source, ok := Source(name)
		if !ok {
			continue
		}
		specifier := name

		var bag diag.Bag
		tree := parser.Parse(specifier, source, &bag)
		if tree == nil || bag.HasErrors() {
			continue
		}
		indexModule(specifier, tree)
	}
}

func indexModule(specifier string, tree *ast.File) {
	for _, decl := range tree.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Owner != "" {
				index.Methods[d.Name] = appendUnique(index.Methods[d.Name], d.Owner)
				continue
			}
			addExport(specifier, d.Name, d.Exported)
		case *ast.StructDecl:
			addExport(specifier, d.Name, d.Exported)
		case *ast.EnumDecl:
			addExport(specifier, d.Name, d.Exported)
		case *ast.TraitDecl:
			addExport(specifier, d.Name, d.Exported)
		case *ast.VarDecl:
			for _, binding := range d.Names {
				addExport(specifier, binding.Name, d.Exported)
			}
		}
	}
}

func addExport(specifier, name string, exported bool) {
	if !exported {
		return
	}
	index.Exports[name] = appendUnique(index.Exports[name], specifier)
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}
