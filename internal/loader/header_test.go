package loader

import (
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// The generated header is what Gyro's checker reads about another
// producer's module. A named type in an export used to make the export
// unimportable; now the type is declared too — as a declaration of that
// module's type, which lowering imports rather than rebuilds.

func generated(t *testing.T, source string) string {
	t.Helper()
	module, err := metals.Parse(source)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatalf("verify: %v", err)
	}
	text, err := header(module)
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	return text
}

func wants(t *testing.T, text string, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if !strings.Contains(text, line) {
			t.Errorf("the generated header has no %q:\n%s", line, text)
		}
	}
}

func TestHeaderDeclaresARecord(t *testing.T) {
	text := generated(t, `
module "cc:shapes"

type %Point = record { x: i32, y: i32 }

export fn @flip(%p: %Point) -> %Point {
  return %p
}
`)
	// Every member is exported: a C struct has no private ones.
	wants(t, text,
		"export struct Point {",
		"  export x: i32",
		"  export y: i32",
		"export fn flip(a0: Point): Point",
	)
}

// A member of a named type brings that type with it. Declaring %Line
// without %Point would be a shape Gyro cannot lay out.
func TestHeaderDeclaresWhatAMemberNames(t *testing.T) {
	text := generated(t, `
module "cc:shapes"

type %Point = record { x: i32, y: i32 }
type %Line = record { a: %Point, b: %Point }

export fn @span(%l: %Line) -> i32 {
  %n = const.i32 0
  return %n
}
`)
	wants(t, text, "export struct Point {", "export struct Line {", "  export a: Point")
}

// An opaque type is a fieldless struct, which is what Gyro already
// writes for something it may hold and not look inside.
func TestHeaderDeclaresAnOpaqueTypeAsAFieldlessStruct(t *testing.T) {
	text := generated(t, `
module "cc:sdl"

type %Window = opaque

export fn @open() -> opt<ptr<%Window>> {
  %n = const.null opt<ptr<%Window>>
  return %n
}
`)
	wants(t, text, "export struct Window {}", "export fn open(): *Window?")
}

// A type Gyro has no declaration for takes the exports that mention it
// out of the header, and leaves the rest. A library is not written for
// this importer, and one helper it cannot spell should not make the
// other forty unimportable.
func TestHeaderSkipsWhatItCannotDeclare(t *testing.T) {
	text := generated(t, `
module "cc:mixed"

type %Bits = union { whole: i32, half: u16 }
type %Point = record { x: i32 }

export fn @punned(%b: %Bits) -> i32 {
  %n = const.i32 0
  return %n
}

export fn @plain(%p: %Point) -> i32 {
  %n = const.i32 0
  return %n
}
`)
	wants(t, text, "export struct Point {", "export fn plain(a0: Point): i32")
	if strings.Contains(text, "punned") || strings.Contains(text, "Bits") {
		t.Errorf("a union reached the header:\n%s", text)
	}
}

// A record whose member is one Gyro cannot spell is itself unspellable,
// and so is everything holding it. The fixpoint is what says so.
func TestHeaderPropagatesUnspellability(t *testing.T) {
	text := generated(t, `
module "cc:mixed"

type %Bits = union { whole: i32, half: u16 }
type %Holder = record { bits: %Bits }
type %Outer = record { held: %Holder }

export fn @outer(%o: %Outer) -> i32 {
  %n = const.i32 0
  return %n
}
`)
	if strings.Contains(text, "Outer") || strings.Contains(text, "Holder") {
		t.Errorf("a type holding a union reached the header:\n%s", text)
	}
}

// A type that names itself through a pointer is ordinary, and believing
// everything first is what keeps the fixpoint from looping on it.
func TestHeaderDeclaresARecursiveRecord(t *testing.T) {
	text := generated(t, `
module "cc:list"

type %Node = record { value: i32, next: opt<ptr<%Node>> }

export fn @head(%n: ptr<%Node>) -> i32 {
  %p = field.ptr %n, 0
  %v = load %p
  return %v
}
`)
	wants(t, text, "export struct Node {", "  export next: *Node?")
}

// A member Gyro cannot name is the same refusal as a type it cannot
// name. Nothing here renames anything: the name is the exporter's.
func TestHeaderRefusesAKeywordMember(t *testing.T) {
	text := generated(t, `
module "cc:kw"

type %Thing = record { match: i32 }

export fn @use(%t: %Thing) -> i32 {
  %n = const.i32 0
  return %n
}
`)
	if strings.Contains(text, "Thing") {
		t.Errorf("a struct with a member named after a keyword reached the header:\n%s", text)
	}
}

// A type nothing exported mentions is the module's own business, the
// same as a function it did not export.
func TestHeaderLeavesOutAnUnmentionedType(t *testing.T) {
	text := generated(t, `
module "cc:private"

type %Guts = record { seed: i32 }

export fn @seed() -> i32 {
  %n = const.i32 0
  return %n
}
`)
	if strings.Contains(text, "Guts") {
		t.Errorf("an internal type reached the header:\n%s", text)
	}
}
