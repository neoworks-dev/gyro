package loader_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/stdlib"
)

// writeTree materializes files under a fresh temp dir and returns its path.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, source := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func load(t *testing.T, files map[string]string, root string) (*loader.Program, *diag.Bag) {
	t.Helper()

	dir := writeTree(t, files)
	var bag diag.Bag
	program, err := loader.Load(filepath.Join(dir, root), &bag)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return program, &bag
}

func wantError(t *testing.T, bag *diag.Bag, fragment string) {
	t.Helper()
	for _, item := range bag.Items() {
		if strings.Contains(item.Message, fragment) {
			return
		}
	}
	t.Errorf("no diagnostic contains %q; got: %v", fragment, bag.Items())
}

func TestRelativeImportLoadsBesideTheImporter(t *testing.T) {
	program, bag := load(t, map[string]string{
		"main.gyro": "import { helper } from \"./util\"\n\nfn main() {\n  helper()\n}\n",
		"util.gyro": "export fn helper() {\n}\n",
	}, "main.gyro")

	if bag.HasErrors() {
		t.Fatalf("unexpected errors: %v", bag.Items())
	}
	if len(program.Modules) != 2 {
		t.Fatalf("want 2 modules, got %d", len(program.Modules))
	}

	// Dependencies come first, the root last.
	if program.Modules[len(program.Modules)-1] != program.Root {
		t.Error("root is not last in dependency order")
	}
	if dep := program.Root.Deps["./util"]; dep == nil || !strings.HasSuffix(dep.Path, "util.gyro") {
		t.Errorf("./util resolved to %v", dep)
	}
}

func TestTwoSpellingsLoadOneModule(t *testing.T) {
	program, bag := load(t, map[string]string{
		"main.gyro": "import { a } from \"./util\"\nimport { b } from \"./util.gyro\"\n",
		"util.gyro": "export fn a() {\n}\n\nexport fn b() {\n}\n",
	}, "main.gyro")

	if bag.HasErrors() {
		t.Fatalf("unexpected errors: %v", bag.Items())
	}
	if len(program.Modules) != 2 {
		t.Fatalf("want the two spellings deduplicated, got %d modules", len(program.Modules))
	}
	if program.Root.Deps["./util"] != program.Root.Deps["./util.gyro"] {
		t.Error("the two spellings resolved to different modules")
	}
}

func TestCircularImportsAreRejected(t *testing.T) {
	_, bag := load(t, map[string]string{
		"a.gyro": "import { b } from \"./b\"\n\nexport fn a() {\n}\n",
		"b.gyro": "import { a } from \"./a\"\n\nexport fn b() {\n}\n",
	}, "a.gyro")
	wantError(t, bag, "circular import")
}

func TestSelfImportIsRejected(t *testing.T) {
	_, bag := load(t, map[string]string{
		"a.gyro": "import { a } from \"./a\"\n\nexport fn a() {\n}\n",
	}, "a.gyro")
	wantError(t, bag, "circular import")
}

func TestMissingRelativeModule(t *testing.T) {
	_, bag := load(t, map[string]string{
		"main.gyro": "import { x } from \"./ghost\"\n",
	}, "main.gyro")
	wantError(t, bag, "cannot load \"./ghost\"")
}

func TestUnknownStdlibModule(t *testing.T) {
	_, bag := load(t, map[string]string{
		"main.gyro": "import { x } from \"@gyro/nope\"\n",
	}, "main.gyro")
	wantError(t, bag, "no stdlib module")
}

func TestBarePackagesAreNotImplemented(t *testing.T) {
	_, bag := load(t, map[string]string{
		"main.gyro": "import { pad } from \"leftpad\"\n",
	}, "main.gyro")
	wantError(t, bag, "packages under .stack/ are not implemented yet")
}

func TestReexportPathIsADependency(t *testing.T) {
	program, bag := load(t, map[string]string{
		"main.gyro": "export { helper } from \"./util\"\n",
		"util.gyro": "export fn helper() {\n}\n",
	}, "main.gyro")

	if bag.HasErrors() {
		t.Fatalf("unexpected errors: %v", bag.Items())
	}
	if program.Root.Deps["./util"] == nil {
		t.Error("re-export path was not loaded as a dependency")
	}
}

// Every bundled stdlib module has to parse and name-check on its own; this
// is the stubs' regression test.
func TestStdlibModulesAreClean(t *testing.T) {
	names := stdlib.Modules()
	if len(names) == 0 {
		t.Fatal("no bundled stdlib modules found")
	}

	var imports strings.Builder
	for _, name := range names {
		imports.WriteString("import \"@gyro/" + name + "\"\n")
	}

	program, bag := load(t, map[string]string{
		"main.gyro": imports.String(),
	}, "main.gyro")
	sema.CheckProgram(program, bag)

	for _, item := range bag.Items() {
		t.Errorf("%s", item)
	}
}
