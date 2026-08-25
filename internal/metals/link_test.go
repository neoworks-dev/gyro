package metals

import (
	"strings"
	"testing"
)

func parsed(t *testing.T, source string) *Module {
	t.Helper()
	module, err := Parse(source)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Verify(module); err != nil {
		t.Fatalf("verify: %v", err)
	}
	return module
}

// The exporter is a module in another language, so the linker has two
// signatures to compare. It is not another *runtime* — a Scheme that
// compiles into METALS ends up in the same module on the same heap — so
// no boundary rule applies between these two.
const exporter = `
module "lisp:fib"

export fn @fib(%n: i64) -> i64 {
  return %n
}

fn @private(%n: i64) -> i64 {
  return %n
}
`

const importer = `
module "gyro:main"

import "gyro:rt" {
  fn @"println<i64>"(i64)
}

import "lisp:fib" {
  fn @fib(i64) -> i64
}

export fn @main() {
  %n = const.i64 10
  %r = call @fib(%n)
  call @"println<i64>"(%r)
  return
}
`

func linked(t *testing.T, sources ...string) *Module {
	t.Helper()
	modules := make([]*Module, len(sources))
	for i, source := range sources {
		modules[i] = parsed(t, source)
	}
	module, err := Link(modules)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	// A linked program is what every consumer takes, so it has to satisfy
	// the same verifier a hand-written module does.
	if err := Verify(module); err != nil {
		t.Fatalf("the linked module does not verify: %v\n%s", err, Print(module))
	}
	return module
}

func refusedLink(t *testing.T, fragment string, sources ...string) {
	t.Helper()
	modules := make([]*Module, len(sources))
	for i, source := range sources {
		modules[i] = parsed(t, source)
	}
	_, err := Link(modules)
	if err == nil {
		t.Fatalf("expected a link error containing %q, the program linked", fragment)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("expected a link error containing %q, got %v", fragment, err)
	}
}

func TestLinkQualifiesTheDependency(t *testing.T) {
	module := linked(t, exporter, importer)

	if module.Name != "gyro:main" {
		t.Errorf("linked module is %q, want the root's name gyro:main", module.Name)
	}

	names := map[string]bool{}
	for _, fn := range module.Funcs {
		names[fn.Name] = true
	}
	// The root keeps its names so that main stays main; everything the
	// dependency owns is qualified, so two producers may both call a
	// function fib without colliding.
	for _, want := range []string{"main", "lisp:fib::fib", "lisp:fib::private"} {
		if !names[want] {
			t.Errorf("linked module has no @%s; it has %v", want, names)
		}
	}
	if names["fib"] {
		t.Error("the dependency's fib survived unqualified")
	}
}

func TestLinkRewritesCallsToTheDependency(t *testing.T) {
	printed := Print(linked(t, exporter, importer))
	if !strings.Contains(printed, `call @"lisp:fib::fib"`) {
		t.Errorf("the call was not rewritten to the qualified name:\n%s", printed)
	}
	// The import is resolved, not carried through: the callee is in the
	// module now.
	if strings.Contains(printed, `import "lisp:fib"`) {
		t.Errorf("the resolved import survived into the output:\n%s", printed)
	}
	// The runtime import is not a module in the program, so it stays.
	if !strings.Contains(printed, `import "gyro:rt"`) {
		t.Errorf("the runtime import was dropped:\n%s", printed)
	}
}

// This is the check the format had nowhere to make before: an importer
// declares what it expects and only the linker sees both sides.
func TestLinkCatchesASignatureMismatch(t *testing.T) {
	refusedLink(t, "imports @fib from \"lisp:fib\" as fn(i32) -> i32, but it is fn(i64) -> i64",
		exporter, strings.NewReplacer(
			"fn @fib(i64) -> i64", "fn @fib(i32) -> i32",
			"%n = const.i64 10", "%n = const.i32 10",
			`fn @"println<i64>"(i64)`, `fn @"println<i32>"(i32)`,
			`call @"println<i64>"(%r)`, `call @"println<i32>"(%r)`,
		).Replace(importer))
}

func TestLinkCatchesAMissingExport(t *testing.T) {
	refusedLink(t, "which does not declare it",
		exporter, strings.ReplaceAll(importer, "@fib", "@nope"))
}

func TestLinkCatchesAnUnexportedName(t *testing.T) {
	refusedLink(t, "declares it but does not export it",
		exporter, strings.ReplaceAll(importer, "@fib", "@private"))
}

func TestLinkRejectsADuplicateModule(t *testing.T) {
	refusedLink(t, "appears twice", exporter, exporter, importer)
}

// A pointer between two producers of METALS is not a crossing: they are
// one module on one heap by the time anything runs, which is the whole
// reason to compile a language in rather than link it. This is what a C
// front end needs and what the rule used to refuse.
func TestLinkPassesAPointerBetweenProducers(t *testing.T) {
	linked(t, `
module "lisp:cells"

export fn @cell() -> ptr<i64> {
  %p = slot i64
  %zero = const.i64 0
  store %p, %zero
  return %p
}
`, `
module "gyro:main"

import "lisp:cells" {
  fn @cell() -> ptr<i64>
}

export fn @main() {
  %p = call @cell()
  return
}
`)
}

// The importer's own declaration passes its own verifier, because the
// %Cell it declares holds no pointer. Only the linker sees the shape the
// exporter actually has, which is why the export side of the boundary
// rule is checked here.
func TestLinkChecksTheBoundaryOfWhatItResolves(t *testing.T) {
	refusedLink(t, "@hold crosses a runtime boundary; ptr<i64> cannot", `
module "py:host"

type %Cell = record { p: ptr<i64> }

export fn @hold(%c: %Cell) {
  return
}
`, `
module "gyro:main"

type %Cell = record { p: i64 }

import "py:host" {
  fn @hold(%Cell)
}

export fn @main() {
  return
}
`)
}

// One module is already the whole program, and linking it must leave it
// exactly as it was: a Gyro program with no foreign imports lowers to
// what it always did.
func TestLinkOfOneModuleIsIdentity(t *testing.T) {
	only := parsed(t, exporter)
	module, err := Link([]*Module{only})
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if module != only {
		t.Error("linking a single module rebuilt it")
	}
}

func TestLinkQualifiesTypesAndGlobals(t *testing.T) {
	module := linked(t, `
module "lisp:shapes"

type %Point = record { x: i64, y: i64 }

global @origin: %Point

export fn @zero() -> %Point {
  %p = global.ptr @origin
  %v = load %p
  return %v
}
`, `
module "gyro:main"

type %Point = record { x: i64 }

import "lisp:shapes" {
  fn @zero() -> %Point
}

export fn @main() {
  return
}
`)

	types := map[string]bool{}
	for _, decl := range module.Types {
		types[decl.Name] = true
	}
	// Both producers declared a %Point; neither loses to the other.
	for _, want := range []string{"Point", "lisp:shapes::Point"} {
		if !types[want] {
			t.Errorf("linked module has no %%%s; it has %v", want, types)
		}
	}
	if len(module.Globals) != 1 || module.Globals[0].Name != "lisp:shapes::origin" {
		t.Errorf("the dependency's global was not qualified: %v", module.Globals)
	}
}
