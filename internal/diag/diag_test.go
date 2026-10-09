package diag

import (
	"strings"
	"testing"
)

func TestBagDropsRepeatedMessages(t *testing.T) {
	file := NewFile("test.gyro", "fn t() {\n}\n")
	var bag Bag
	bag.Errorf(file.Position(3), "undeclared type %s", "String")
	bag.Errorf(file.Position(3), "undeclared type %s", "String")
	bag.Errorf(file.Position(9), "undeclared type %s", "String")
	bag.Warnf(file.Position(3), "undeclared type %s", "String")

	if bag.Len() != 3 {
		t.Fatalf("got %d diagnostics, want 3: %v", bag.Len(), bag.Items())
	}
}

func TestDiagnosticPrintsNotesUnderTheMessage(t *testing.T) {
	file := NewFile("test.gyro", "fn t(p: String) {\n}\n")
	var bag Bag
	bag.Add(Diagnostic{
		Severity: SeverityError,
		Pos:      file.Position(8),
		Code:     EUndeclaredType,
		Message:  "undeclared type String",
		Help:     []string{"the built-in type is spelled string"},
	})

	want := "test.gyro:1:9: error[E0402]: undeclared type String\n  help: the built-in type is spelled string"
	if got := bag.Items()[0].String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestItemsSortByPosition(t *testing.T) {
	file := NewFile("test.gyro", "fn t() {\n  var x = 1\n}\n")
	var bag Bag
	bag.Errorf(file.Position(15), "second")
	bag.Errorf(file.Position(2), "first")

	items := bag.Items()
	if !strings.HasPrefix(items[0].Message, "first") {
		t.Errorf("diagnostics are out of order: %v", items)
	}
}

func TestRenderDrawsACodeFrame(t *testing.T) {
	src := "fn t() {\n  println(1)\n}\n"
	file := NewFile("hello.gyro", src)
	var bag Bag
	bag.Track(file)
	bag.Add(Diagnostic{
		Severity: SeverityError,
		Code:     EUndeclaredName,
		Pos:      file.Position(11),
		Label:    "not found in this scope",
		Message:  "undeclared name println",
		Help:     []string{`"stdlib" exports println`},
	})

	want := `error[E0401]: undeclared name println
 --> hello.gyro:2:3
  |
2 |   println(1)
  |   ^^^^^^^ not found in this scope
  |
  = help: "stdlib" exports println

`
	if got := bag.Render(Style{}); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A related span names its own file. Resolving every frame against the
// primary's source would quote line 2 of main.gyro under a path reading
// util.gyro: the wrong source under a correct-looking location.
func TestRenderResolvesARelatedSpanAgainstItsOwnFile(t *testing.T) {
	main := NewFile("main.gyro", "import { v } from \"./util\"\nvar v = 2\n")
	util := NewFile("util.gyro", "// a comment\nexport var v = 1\n")
	var bag Bag
	bag.Track(main)
	bag.Track(util)
	bag.Add(Diagnostic{
		Severity: SeverityError,
		Code:     EAlreadyDeclared,
		Pos:      main.Position(31),
		Message:  "v is already declared in this scope",
		Related: []Span{{
			Pos:   util.Position(20),
			Label: "previous declaration here",
		}},
	})

	want := `error[E0403]: v is already declared in this scope
 --> main.gyro:2:5
  |
2 | var v = 2
  |     ^
 --> util.gyro:2:8
  |
2 | export var v = 1
  |        --- previous declaration here

`
	if got := bag.Render(Style{}); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A frame the bag never saw the source for states its location instead of
// quoting a line out of whichever file was at hand.
func TestRenderDegradesAnUntrackedRelatedSpan(t *testing.T) {
	main := NewFile("main.gyro", "var v = 2\n")
	var bag Bag
	bag.Track(main)
	bag.Add(Diagnostic{
		Severity: SeverityError,
		Code:     EAlreadyDeclared,
		Pos:      main.Position(4),
		Message:  "v is already declared in this scope",
		Related: []Span{{
			Pos:   Position{File: "gone.gyro", Offset: 0, Line: 7, Column: 3},
			Label: "previous declaration here",
		}},
	})

	want := `error[E0403]: v is already declared in this scope
 --> main.gyro:1:5
  |
1 | var v = 2
  |     ^
 --> gone.gyro:7:3 previous declaration here

`
	if got := bag.Render(Style{}); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
