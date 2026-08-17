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
