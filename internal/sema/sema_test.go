package sema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/parser"
)

// check parses and name-checks src, returning the diagnostics bag.
func check(t *testing.T, src string) *diag.Bag {
	t.Helper()

	file := diag.NewFile("test.gyro", src)
	var bag diag.Bag
	tree := parser.ParseFile(file, &bag)
	if bag.HasErrors() {
		for _, item := range bag.Items() {
			t.Errorf("parse error: %s", item)
		}
		t.Fatalf("source did not parse cleanly:\n%s", src)
	}

	Check(tree, file, &bag)
	return &bag
}

// checkOK asserts that src resolves without diagnostics.
func checkOK(t *testing.T, src string) {
	t.Helper()
	bag := check(t, src)
	for _, item := range bag.Items() {
		t.Errorf("unexpected diagnostic: %s", item)
	}
}

// checkError asserts that src produces a diagnostic containing want.
func checkError(t *testing.T, src, want string) {
	t.Helper()
	bag := check(t, src)
	if !bag.HasErrors() {
		t.Fatalf("expected an error containing %q, got none", want)
	}
	for _, item := range bag.Items() {
		if strings.Contains(item.Message, want) {
			return
		}
	}
	t.Errorf("no diagnostic contains %q; got: %v", want, bag.Items())
}

func TestUndeclaredNames(t *testing.T) {
	checkError(t, "fn t() {\n  var x = missing\n}\n", "undeclared name missing")
	checkError(t, "fn t() {\n  println(1)\n}\n", "undeclared name println")
	checkError(t, "fn t(p: Missing) {\n}\n", "undeclared type Missing")
}

func TestImportsBindNames(t *testing.T) {
	checkOK(t, "import { println } from \"stdlib\"\n\nfn t() {\n  println(1)\n}\n")
	checkOK(t, "import { open as fopen } from \"fs\"\n\nfn t() {\n  var f = fopen(\"x\")\n}\n")
	checkOK(t, "import * as mem from \"mem\"\n\nfn t() {\n  var p = mem::size()\n}\n")
}

func TestNoUniverseScope(t *testing.T) {
	// spawn, alloc and friends are stdlib, not builtins: unimported use fails.
	checkError(t, "fn t() {\n  spawn(fn() {})\n}\n", "undeclared name spawn")
	checkError(t, "fn t() {\n  var p = alloc()\n}\n", "undeclared name alloc")
}

func TestModuleScopeIsOrderIndependent(t *testing.T) {
	checkOK(t, "fn a(): i32 {\n  return b()\n}\n\nfn b(): i32 {\n  return 1\n}\n")
}

func TestShadowing(t *testing.T) {
	checkOK(t, "fn t() {\n  var x = 1\n  if true {\n    var x = 2\n    var y = x\n  }\n}\n")
	checkError(t, "fn t() {\n  var x = 1\n  var x = 2\n}\n", "already declared in this scope")
	checkError(t, "fn t(a: i32, a: i32) {\n}\n", "already declared in this scope")
}

func TestInitializerResolvesBeforeBinding(t *testing.T) {
	// `var x = x` refers to an outer x or fails; the local is not yet bound.
	checkError(t, "fn t() {\n  var x = x\n}\n", "undeclared name x")
	checkOK(t, "fn t() {\n  var x = 1\n  if true {\n    var x = x\n  }\n}\n")
}

func TestSelfOnlyInMethods(t *testing.T) {
	checkOK(t, "struct S {\n  v: i32\n}\n\nfn S::get(self): i32 {\n  return self.v\n}\n")
	checkError(t, "fn t() {\n  var x = self\n}\n", "self is only available inside a method")

	// A closure inside a method still sees self.
	checkOK(t, "struct S {\n  v: i32\n}\n\nfn S::get(self): fn(): i32 {\n  return fn() self.v\n}\n")
}

func TestMethodsNeedTheirType(t *testing.T) {
	checkError(t, "fn Missing::m(self) {\n}\n", "no such type in this module")
	checkError(t, "var v = 1\n\nfn v::m(self) {\n}\n", "not a type")
	checkError(t, "fn m(self) {\n}\n", "a receiver needs an owning type")
}

func TestGenericTypeParamsResolve(t *testing.T) {
	checkOK(t, "fn max<T>(a: T, b: T): T {\n  return a\n}\n")
	checkOK(t, "struct Pair<T> {\n  first: T\n  second: T\n}\n\nfn Pair<T>::first_of(self): T {\n  return self.first\n}\n")
}

func TestEnumVariantsAreChecked(t *testing.T) {
	src := "enum Shape {\n  Circle(f64),\n  Point,\n}\n\n"
	checkOK(t, src+"fn t(s: Shape): f64 {\n  return match s {\n    Shape::Circle(r): r\n    Shape::Point: 0.0\n  }\n}\n")
	checkError(t, src+"fn t(): Shape {\n  return Shape::Square\n}\n", "Shape has no variant Square")
}

func TestPatternBindingsScopeToTheirArm(t *testing.T) {
	src := "enum E {\n  A(i32),\n  B,\n}\n\n" +
		"fn t(e: E): i32 {\n  return match e {\n    E::A(v): v\n    E::B: 0\n  }\n}\n"
	checkOK(t, src)

	leak := "enum E {\n  A(i32),\n}\n\n" +
		"fn t(e: E): i32 {\n  var x = match e {\n    E::A(v): v\n  }\n  return v\n}\n"
	checkError(t, leak, "undeclared name v")
}

func TestMoveCaptureNeedsTheName(t *testing.T) {
	checkOK(t, "fn t(): fn() {\n  var node = 1\n  return fn[move node]() {\n    var x = node\n  }\n}\n")
	checkError(t, "fn t() {\n  var f = fn[move ghost]() {}\n}\n", "cannot move ghost")
}

func TestDecoratorsAreAFixedSet(t *testing.T) {
	checkOK(t, "@gpu fn k(a: *f32) {\n}\n")
	checkOK(t, "@concurrent(threads = 8) fn w() {\n}\n")
	checkError(t, "@wasm fn w() {\n}\n", "unknown decorator @wasm")
}

func TestExportListNamesMustExist(t *testing.T) {
	checkOK(t, "fn a() {\n}\n\nexport { a }\n")
	checkError(t, "export { ghost }\n", "cannot export ghost")
}

func TestBuiltinTypesNeedNoImport(t *testing.T) {
	checkOK(t, "fn t(a: i32, b: usize, c: rune, d: error?): bool {\n  return true\n}\n")
	checkError(t, "fn t(a: i32<i8>) {\n}\n", "takes no type arguments")
}

// checkProgram writes files into a temp dir, loads the program rooted at
// root and checks it, returning the diagnostics bag.
func checkProgram(t *testing.T, files map[string]string, root string) *diag.Bag {
	t.Helper()

	dir := t.TempDir()
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var bag diag.Bag
	program, err := loader.Load(filepath.Join(dir, root), &bag)
	if err != nil {
		t.Fatal(err)
	}
	if bag.HasErrors() {
		t.Fatalf("program did not load cleanly: %v", bag.Items())
	}

	CheckProgram(program, &bag)
	return &bag
}

func programOK(t *testing.T, files map[string]string, root string) {
	t.Helper()
	bag := checkProgram(t, files, root)
	for _, item := range bag.Items() {
		t.Errorf("unexpected diagnostic: %s", item)
	}
}

func programError(t *testing.T, files map[string]string, root, want string) {
	t.Helper()
	bag := checkProgram(t, files, root)
	if !bag.HasErrors() {
		t.Fatalf("expected an error containing %q, got none", want)
	}
	for _, item := range bag.Items() {
		if strings.Contains(item.Message, want) {
			return
		}
	}
	t.Errorf("no diagnostic contains %q; got: %v", want, bag.Items())
}

// programWarning asserts a warning containing want, and no errors alongside
// it: the leak analysis must never block a program from compiling.
func programWarning(t *testing.T, files map[string]string, root, want string) {
	t.Helper()
	bag := checkProgram(t, files, root)
	if bag.HasErrors() {
		t.Fatalf("expected only a warning, got errors: %v", bag.Items())
	}
	for _, item := range bag.Items() {
		if strings.Contains(item.Message, want) {
			return
		}
	}
	t.Errorf("no diagnostic contains %q; got: %v", want, bag.Items())
}

const utilModule = "export fn helper() {\n}\n\n" +
	"export struct Shape {\n}\n\n" +
	"fn hidden() {\n}\n"

func TestImportsAreValidatedAgainstExports(t *testing.T) {
	programOK(t, map[string]string{
		"main.gyro": "import { helper, Shape } from \"./util\"\n\nfn t(s: Shape) {\n  helper()\n}\n",
		"util.gyro": utilModule,
	}, "main.gyro")

	programError(t, map[string]string{
		"main.gyro": "import { hidden } from \"./util\"\n",
		"util.gyro": utilModule,
	}, "main.gyro", "does not export hidden")
}

func TestImportedNamesCarryTheirKind(t *testing.T) {
	programError(t, map[string]string{
		"main.gyro": "import { helper } from \"./util\"\n\nfn t(x: helper) {\n}\n",
		"util.gyro": utilModule,
	}, "main.gyro", "helper is a value, not a type")
}

func TestImportAliasesBindTheAlias(t *testing.T) {
	programOK(t, map[string]string{
		"main.gyro": "import { helper as run } from \"./util\"\n\nfn t() {\n  run()\n}\n",
		"util.gyro": utilModule,
	}, "main.gyro")
}

func TestNamespaceMembersAreChecked(t *testing.T) {
	programOK(t, map[string]string{
		"main.gyro": "import * as util from \"./util\"\n\nfn t(s: util::Shape) {\n  util::helper()\n}\n",
		"util.gyro": utilModule,
	}, "main.gyro")

	programError(t, map[string]string{
		"main.gyro": "import * as util from \"./util\"\n\nfn t() {\n  util::nope()\n}\n",
		"util.gyro": utilModule,
	}, "main.gyro", "util does not export nope")

	programError(t, map[string]string{
		"main.gyro": "import * as util from \"./util\"\n\nfn t(x: util::helper) {\n}\n",
		"util.gyro": utilModule,
	}, "main.gyro", "util::helper is a value, not a type")
}

func TestReexportsFlowThrough(t *testing.T) {
	programOK(t, map[string]string{
		"main.gyro":   "import { helper } from \"./facade\"\n\nfn t() {\n  helper()\n}\n",
		"facade.gyro": "export { helper } from \"./util\"\n",
		"util.gyro":   utilModule,
	}, "main.gyro")

	programOK(t, map[string]string{
		"main.gyro":   "import { Shape } from \"./facade\"\n\nfn t(s: Shape) {\n}\n",
		"facade.gyro": "export * from \"./util\"\n",
		"util.gyro":   utilModule,
	}, "main.gyro")

	programError(t, map[string]string{
		"main.gyro":   "import \"./facade\"\n",
		"facade.gyro": "export { ghost } from \"./util\"\n",
		"util.gyro":   utilModule,
	}, "main.gyro", "does not export ghost")
}

func TestImportedEnumVariantsAreChecked(t *testing.T) {
	shapes := "export enum Kind {\n  Circle(f64),\n  Point,\n}\n"

	programOK(t, map[string]string{
		"main.gyro":   "import { Kind } from \"./shapes\"\n\nfn t(): Kind {\n  return Kind::Point\n}\n",
		"shapes.gyro": shapes,
	}, "main.gyro")

	programError(t, map[string]string{
		"main.gyro":   "import { Kind } from \"./shapes\"\n\nfn t(): Kind {\n  return Kind::Square\n}\n",
		"shapes.gyro": shapes,
	}, "main.gyro", "Kind has no variant Square")
}

func TestExamplesResolve(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.gyro"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no example programs found")
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			var bag diag.Bag
			program, err := loader.Load(path, &bag)
			if err != nil {
				t.Fatal(err)
			}
			if !bag.HasErrors() {
				CheckProgram(program, &bag)
			}

			for _, item := range bag.Items() {
				t.Errorf("%s", item)
			}
		})
	}
}
