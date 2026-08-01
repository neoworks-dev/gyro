package sema

import (
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
)

// checkNote asserts that some diagnostic carries a note containing want.
func checkNote(t *testing.T, src, want string) {
	t.Helper()
	assertNote(t, check(t, src), want)
}

func programNote(t *testing.T, files map[string]string, root, want string) {
	t.Helper()
	assertNote(t, checkProgram(t, files, root), want)
}

func assertNote(t *testing.T, bag *diag.Bag, want string) {
	t.Helper()
	for _, item := range bag.Items() {
		for _, note := range append(item.Notes, item.Help...) {
			if strings.Contains(note, want) {
				return
			}
		}
	}
	t.Errorf("no diagnostic carries a note containing %q; got: %v", want, bag.Items())
}

func TestUndeclaredNamesSuggestTheBuiltinSpelling(t *testing.T) {
	checkNote(t, "fn t(p: String) {\n}\n", "the built-in type is spelled string")
	checkNote(t, "fn t(p: Int) {\n}\n", "the built-in type is spelled i32")
	checkNote(t, "fn t(p: boolean) {\n}\n", "the built-in type is spelled bool")
}

func TestUndeclaredNamesSuggestAnImport(t *testing.T) {
	checkNote(t, "fn t() {\n  println(1)\n}\n", "import { println } from \"@gyro/stdlib\"")
	checkNote(t, "fn t() {\n  var xs = Array<i32>::new()\n}\n", "import { Array } from \"@gyro/stdlib\"")

	// The module is already imported, so the fix is one more name in its
	// braces rather than a second import statement.
	programNote(t, map[string]string{
		"main.gyro": "import { println } from \"@gyro/stdlib\"\n\nfn t() {\n  eprintln(1)\n}\n",
	}, "main.gyro", "add it to the import you already have from \"@gyro/stdlib\"")
}

func TestUndeclaredNamesSuggestAMethod(t *testing.T) {
	checkNote(t, "fn t(xs: i32[]) {\n  var n = len(xs)\n}\n", "len is a method on Array")
}

func TestUndeclaredNamesSuggestASpelling(t *testing.T) {
	checkNote(t, "fn t() {\n  var counter = 1\n  counter = countr + 1\n}\n", "did you mean counter?")
	checkNote(t, "struct Point {\n  x: i32\n}\n\nfn t(p: Pont) {\n}\n", "did you mean Point?")
}

// A guess is worth printing only when it is close; an unrelated name is
// noise, and so is a one-letter neighbour of a one-letter name.
func TestUnrelatedNamesGetNoGuess(t *testing.T) {
	bag := check(t, "fn t() {\n  var counter = 1\n  var v = zqx\n}\n")
	for _, item := range bag.Items() {
		for _, note := range append(item.Notes, item.Help...) {
			if strings.Contains(note, "did you mean") {
				t.Errorf("unexpected guess: %s", note)
			}
		}
	}
	assertNote(t, bag, "nothing is predeclared")
}

func TestMemberErrorsSuggestASpelling(t *testing.T) {
	const point = "struct Point {\n  count: i32\n}\n\n" +
		"fn Point::length(self): i32 {\n  return self.count\n}\n\n" +
		"fn Point::new(): Point {\n  return Point{ count: 0 }\n}\n\n"

	checkNote(t, point+"fn t(p: Point): i32 {\n  return p.conut\n}\n", "did you mean count?")
	checkNote(t, point+"fn t(p: Point): i32 {\n  return p.lenght()\n}\n", "did you mean length?")
	checkNote(t, point+"fn t() {\n  var p = Point{ conut: 1 }\n}\n", "did you mean count?")
	checkNote(t, point+"fn t(): Point {\n  return Point::mew()\n}\n", "did you mean new?")

	const shape = "enum Shape {\n  Square(i32),\n  Point,\n}\n\n"
	checkNote(t, shape+"fn t(): Shape {\n  return Shape::Squere(1)\n}\n", "did you mean Square?")
}

func TestMissingExportSuggestsASpelling(t *testing.T) {
	programNote(t, map[string]string{
		"main.gyro": "import { helepr } from \"./util\"\n\nfn t() {\n}\n",
		"util.gyro": utilModule,
	}, "main.gyro", "did you mean helper?")
}

func TestEditDistanceCountsEdits(t *testing.T) {
	cases := []struct {
		from, to string
		want     int
	}{
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"counter", "countr", 1},
		{"length", "lenght", 2},
		{"kitten", "sitting", 3},
	}
	for _, c := range cases {
		if got := editDistance(c.from, c.to); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.from, c.to, got, c.want)
		}
	}
}
