package diag_test

// The compiler attaches codes by looking a diagnostic's format string up in
// its package's codes.go table. That keeps the catalogue in one reviewable
// place, but it also means a reworded message silently loses its code. This
// test reads the sources: every format string handed to a reporting helper
// must appear in the table, and every table entry must name a code the
// catalogue explains.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
)

// reporters are the functions whose first string argument is a diagnostic
// format string. errorHintf and errorLabelf take theirs after a leading
// argument, hence the offsets.
var reporters = map[string]int{
	"errorf":      1,
	"warnf":       1,
	"errorHintf":  2,
	"errorLabelf": 2,
}

func TestEveryMessageHasACode(t *testing.T) {
	for _, pkg := range []string{"lexer", "parser", "sema"} {
		t.Run(pkg, func(t *testing.T) {
			dir := filepath.Join("..", pkg)
			table := tableKeys(t, filepath.Join(dir, "codes.go"))

			for _, format := range reportedFormats(t, dir) {
				if _, ok := table[format]; !ok {
					t.Errorf("%s reports %q with no entry in %s/codes.go", pkg, format, pkg)
				}
			}
		})
	}
}

func TestEveryCodeIsExplained(t *testing.T) {
	constants := codeConstants(t)
	for _, pkg := range []string{"lexer", "parser", "sema"} {
		table := tableKeys(t, filepath.Join("..", pkg, "codes.go"))
		for format, name := range table {
			if name == "NoCode" {
				continue
			}
			code, ok := constants[name]
			if !ok {
				t.Errorf("%s: %q maps to diag.%s, which is not a code", pkg, format, name)
				continue
			}
			if _, _, found := diag.Explain(diag.Code(code)); !found {
				t.Errorf("%s: %q maps to %s, which has no explanation", pkg, format, code)
			}
		}
	}
}

// codeConstants reads this package's own catalogue, mapping each exported
// constant's name to the code it stands for.
func codeConstants(t *testing.T) map[string]string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "codes.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing codes.go: %v", err)
	}

	constants := map[string]string{}
	for _, decl := range file.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			if code, ok := literalString(value.Values[0]); ok {
				constants[value.Names[0].Name] = code
			}
		}
	}
	return constants
}

// tableKeys reads a codes.go and returns its format string -> constant name
// map, "EUndeclaredName" for diag.EUndeclaredName.
func tableKeys(t *testing.T, path string) map[string]string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	keys := map[string]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		composite, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, element := range composite.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := literalString(pair.Key)
			if !ok {
				continue
			}
			keys[key] = selectorName(pair.Value)
		}
		return true
	})
	return keys
}

func selectorName(expr ast.Expr) string {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	return selector.Sel.Name
}

// reportedFormats collects the format strings passed to a reporting helper
// anywhere in a package.
func reportedFormats(t *testing.T, dir string) []string {
	t.Helper()

	packages, err := parser.ParseDir(token.NewFileSet(), dir, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}

	var formats []string
	for _, pkg := range packages {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			formats = append(formats, formatsIn(file)...)
		}
	}
	return formats
}

func formatsIn(file *ast.File) []string {
	var formats []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		index, ok := reporters[calleeName(call.Fun)]
		if !ok || len(call.Args) <= index {
			return true
		}
		if format, ok := literalString(call.Args[index]); ok {
			formats = append(formats, format)
		}
		return true
	})
	return formats
}

func calleeName(fun ast.Expr) string {
	if selector, ok := fun.(*ast.SelectorExpr); ok {
		return selector.Sel.Name
	}
	if ident, ok := fun.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

func literalString(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	return value, true
}
