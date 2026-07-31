package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
)

// parseOK parses src and fails the test if any diagnostic was reported.
func parseOK(t *testing.T, src string) *ast.File {
	t.Helper()

	var bag diag.Bag
	file := Parse("test.gyro", src, &bag)
	if bag.Len() > 0 {
		for _, item := range bag.Items() {
			t.Errorf("unexpected diagnostic: %s", item)
		}
		t.Fatalf("source did not parse cleanly:\n%s", src)
	}
	return file
}

// dumpOf parses src and returns its tree dump with indentation collapsed to
// single spaces, so tests can state the expected shape on one line.
func dumpOf(t *testing.T, src string) string {
	t.Helper()

	dump := ast.Dump(parseOK(t, src))
	lines := strings.Split(strings.TrimSpace(dump), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.Join(lines, " ")
}

// wrap puts an expression in a minimal function so it can be parsed alone.
func wrap(expr string) string {
	return "fn t() {\n  var x = " + expr + "\n}\n"
}

func TestExpressionPrecedence(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{"1 + 2 * 3", "Binary + Lit int literal 1 Binary * Lit int literal 2 Lit int literal 3"},
		{"1 * 2 + 3", "Binary + Binary * Lit int literal 1 Lit int literal 2 Lit int literal 3"},
		{"a || b && c", "Binary || Ident a Binary && Ident b Ident c"},
		{"a == b || c", "Binary || Binary == Ident a Ident b Ident c"},
		{"a & b | c", "Binary | Binary & Ident a Ident b Ident c"},
		{"a < b == c", "Binary == Binary < Ident a Ident b Ident c"},
		{"a - b - c", "Binary - Binary - Ident a Ident b Ident c"},

		// '>>' is two adjacent '>' tokens rejoined by the parser.
		{"a >> 2", "Binary >> Ident a Lit int literal 2"},
		{"a >> b + c", "Binary >> Ident a Binary + Ident b Ident c"},
	}

	for _, test := range tests {
		got := dumpOf(t, wrap(test.expr))
		want := "File test.gyro Func t Body: Block Var x Value: " + test.want
		if got != want {
			t.Errorf("parsing %q\n got: %s\nwant: %s", test.expr, got, want)
		}
	}
}

func TestCastBindsTighterThanBinaryOperators(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		// Unary binds tighter than `as`, so the negation happens first.
		{"-x as f64", "Cast Unary - Ident x Type: NamedType f64"},
		{"x as f64 * 2", "Binary * Cast Ident x Type: NamedType f64 Lit int literal 2"},
	}

	for _, test := range tests {
		got := dumpOf(t, wrap(test.expr))
		want := "File test.gyro Func t Body: Block Var x Value: " + test.want
		if got != want {
			t.Errorf("parsing %q\n got: %s\nwant: %s", test.expr, got, want)
		}
	}
}

func TestGenericCallVersusComparison(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want string
	}{
		{
			name: "a plain comparison stays a comparison",
			expr: "a < b",
			want: "Binary < Ident a Ident b",
		},
		{
			name: "two comparisons are not a generic call",
			expr: "a < b && c > d",
			want: "Binary && Binary < Ident a Ident b Binary > Ident c Ident d",
		},
		{
			name: "angle brackets followed by a call are type arguments",
			expr: "max<i32>(3, 7)",
			want: "Call Fun: Ident max TypeArg: NamedType i32 Arg: Lit int literal 3 Arg: Lit int literal 7",
		},
		{
			name: "nested type arguments close on two separate angle brackets",
			expr: "m<Result<i32, error>>(v)",
			want: "Call Fun: Ident m TypeArg: NamedType Result NamedType i32 NamedType error Arg: Ident v",
		},
		{
			name: "constant type arguments are kept as expressions",
			expr: "VectorAdd<1, 8>(A, B)",
			want: "Call Fun: Ident VectorAdd TypeArg: Lit int literal 1 TypeArg: Lit int literal 8 Arg: Ident A Arg: Ident B",
		},
		{
			name: "a comparison against a call is not a generic call",
			expr: "a < b(c)",
			want: "Binary < Ident a Call Fun: Ident b Arg: Ident c",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := dumpOf(t, wrap(test.expr))
			want := "File test.gyro Func t Body: Block Var x Value: " + test.want
			if got != want {
				t.Errorf("parsing %q\n got: %s\nwant: %s", test.expr, got, want)
			}
		})
	}
}

func TestReceiveVersusNegativeComparison(t *testing.T) {
	// `x<-1` is genuinely ambiguous at the token level: the lexer always
	// produces '<-'. In expression position it can only be a comparison, so
	// the parser splits it apart and both spellings agree.
	spaced := dumpOf(t, wrap("x < -1"))
	tight := dumpOf(t, wrap("x<-1"))

	want := "File test.gyro Func t Body: Block Var x Value: Binary < Ident x Unary - Lit int literal 1"
	if spaced != want {
		t.Errorf("`x < -1` got:  %s\nwant: %s", spaced, want)
	}
	if tight != want {
		t.Errorf("`x<-1` got:  %s\nwant: %s", tight, want)
	}
}

func TestSendStatement(t *testing.T) {
	got := dumpOf(t, "fn t() {\n  ch <- 1\n}\n")
	want := "File test.gyro Func t Body: Block Send Chan: Ident ch Value: Lit int literal 1"
	if got != want {
		t.Errorf("got:  %s\nwant: %s", got, want)
	}
}

func TestStructLiteralForms(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{
			expr: "Point{ x: 1, y: 2 }",
			want: "StructLit Type: NamedType Point Field x: Lit int literal 1 Field y: Lit int literal 2",
		},
		{
			expr: "Mutex{ 0 }",
			want: "StructLit positional Type: NamedType Mutex Field 0: Lit int literal 0",
		},
	}

	for _, test := range tests {
		got := dumpOf(t, wrap(test.expr))
		want := "File test.gyro Func t Body: Block Var x Value: " + test.want
		if got != want {
			t.Errorf("parsing %q\n got: %s\nwant: %s", test.expr, got, want)
		}
	}
}

func TestStructLiteralNotAllowedInConditionHeader(t *testing.T) {
	// The brace after the condition opens the body, so `Point` is a plain
	// identifier here rather than the start of a literal.
	got := dumpOf(t, "fn t() {\n  if Point {\n    return\n  }\n}\n")
	want := "File test.gyro Func t Body: Block If Cond: Ident Point Then: Block Return"
	if got != want {
		t.Errorf("got:  %s\nwant: %s", got, want)
	}
}

func TestStringInterpolation(t *testing.T) {
	got := dumpOf(t, wrap(`"a #{b + c} d"`))
	want := "File test.gyro Func t Body: Block Var x Value: StringLit " +
		`Text "a " Interp: Binary + Ident b Ident c Text " d"`
	if got != want {
		t.Errorf("got:  %s\nwant: %s", got, want)
	}
}

func TestInterpolationErrorsPointAtRealPositions(t *testing.T) {
	var bag diag.Bag
	Parse("test.gyro", "fn t() {\n  var x = \"v: #{ + }\"\n}\n", &bag)

	items := bag.Items()
	if len(items) == 0 {
		t.Fatal("expected an error inside the interpolation")
	}
	// The bad token sits on line 2, not on a synthetic line 1.
	if items[0].Pos.Line != 2 {
		t.Errorf("error reported at line %d, want 2: %s", items[0].Pos.Line, items[0])
	}
}

func TestElifDesugarsToNestedIf(t *testing.T) {
	src := "fn t() {\n  if a {\n  } elif b {\n  } else {\n  }\n}\n"
	got := dumpOf(t, src)
	want := "File test.gyro Func t Body: Block If Cond: Ident a Then: Block " +
		"Else: If Cond: Ident b Then: Block Else: Block"
	if got != want {
		t.Errorf("got:  %s\nwant: %s", got, want)
	}
}

func TestElseOnItsOwnLine(t *testing.T) {
	// A terminator is synthesized after the closing brace; the else on the
	// next line must still attach to the same if.
	src := "fn t() {\n  if a {\n  }\n  else {\n  }\n}\n"
	got := dumpOf(t, src)
	want := "File test.gyro Func t Body: Block If Cond: Ident a Then: Block Else: Block"
	if got != want {
		t.Errorf("got:  %s\nwant: %s", got, want)
	}
}

func TestDeclarations(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "multi-value binding",
			src:  "fn t() {\n  var value, err = f()\n}\n",
			want: "File test.gyro Func t Body: Block Var value, err Value: Call Fun: Ident f",
		},
		{
			name: "exported function with a tuple result",
			src:  "export fn f(): (i32, error) {\n}\n",
			want: "File test.gyro Func export f Result: NamedType i32 Result: NamedType error Body: Block",
		},
		{
			name: "async function",
			src:  "async fn f() {\n}\n",
			want: "File test.gyro Func async f Body: Block",
		},
		{
			name: "method with a pointer receiver",
			src:  "impl Stack {\n  fn push(*self, v: i32) {\n  }\n}\n",
			want: "File test.gyro Impl Type: NamedType Stack Func push Recv: *self " +
				"Param v: Type: NamedType i32 Body: Block",
		},
		{
			name: "effect row",
			src:  "export fn f() !{ io, alloc } {\n}\n",
			want: "File test.gyro Func export f Effects: !{io, alloc} Body: Block",
		},
		{
			name: "effect row variable",
			src:  "export fn f() !e {\n}\n",
			want: "File test.gyro Func export f Effects: !e Body: Block",
		},
		{
			name: "channel type",
			src:  "fn f(ch: chan<i32>) {\n}\n",
			want: "File test.gyro Func f Param ch: Type: ChanType NamedType i32 Body: Block",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := dumpOf(t, test.src); got != test.want {
				t.Errorf("got:  %s\nwant: %s", got, test.want)
			}
		})
	}
}

func TestImportsAndExports(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{`import { a, b as c } from "m"`, `Import {a, b as c} from "m"`},
		{`import * as m from "m"`, `Import * as m from "m"`},
		{`import def from "m"`, `Import def from "m"`},
		{`import "m"`, `Import <side effects> from "m"`},
		{`export { a, b }`, `Export {a, b}`},
		{`export { a } from "m"`, `Export {a} from "m"`},
		{`export * from "m"`, `Export * from "m"`},
	}

	for _, test := range tests {
		got := dumpOf(t, test.src+"\n")
		if want := "File test.gyro " + test.want; got != want {
			t.Errorf("parsing %q\n got: %s\nwant: %s", test.src, got, want)
		}
	}
}

func TestMatchAndSelect(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "match arms use colons",
			src:  "fn t() {\n  var x = match n {\n    0: \"zero\",\n    _: \"other\",\n  }\n}\n",
			want: "File test.gyro Func t Body: Block Var x Value: Match Subject: Ident n " +
				`Arm: Pattern: LiteralPattern Lit int literal 0 Body: ExprStmt Lit string literal "zero" ` +
				`Arm: Pattern: Wildcard Body: ExprStmt Lit string literal "other"`,
		},
		{
			name: "variant patterns bind their payload",
			src:  "fn t() {\n  var x = match r {\n    Ok(g): g,\n    Err(e): return e,\n  }\n}\n",
			want: "File test.gyro Func t Body: Block Var x Value: Match Subject: Ident r " +
				"Arm: Pattern: VariantPattern Ok BindingPattern g Body: ExprStmt Ident g " +
				"Arm: Pattern: VariantPattern Err BindingPattern e Body: Return Ident e",
		},
		{
			name: "select arms bind with var, never with :=",
			src:  "fn t() {\n  select {\n    var v = <-ch: use(v)\n    else: idle()\n  }\n}\n",
			want: "File test.gyro Func t Body: Block Select " +
				"Arm: Comm: Var v Value: Recv Ident ch Body: ExprStmt Call Fun: Ident use Arg: Ident v " +
				"Else: ExprStmt Call Fun: Ident idle",
		},
		{
			name: "select send arm",
			src:  "fn t() {\n  select {\n    ch <- 1: done()\n  }\n}\n",
			want: "File test.gyro Func t Body: Block Select " +
				"Arm: Comm: Send Chan: Ident ch Value: Lit int literal 1 " +
				"Body: ExprStmt Call Fun: Ident done",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := dumpOf(t, test.src); got != test.want {
				t.Errorf("got:  %s\nwant: %s", got, test.want)
			}
		})
	}
}

func TestForms(t *testing.T) {
	sources := []string{
		"fn t() {\n  for var i = 0; i < 10; i++ {\n  }\n}\n",
		"fn t() {\n  for v in 0..10 {\n  }\n}\n",
		"fn t() {\n  while a < b {\n    break\n  }\n}\n",
		"fn t() {\n  defer free(p)\n}\n",
		"fn t() {\n  spawn worker(ch)\n}\n",
		"fn t() {\n  var x = await f()\n}\n",
		"fn t() {\n  var c = chan<i32>(64)\n}\n",
		"fn t() {\n  var c = chan<i32>()\n}\n",
		"enum Shape {\n  Circle(f64),\n  Rect(f64, f64),\n  Point,\n}\n",
		"struct P {\n  export x: i32\n  y: f64\n}\n",
		"@gpu fn k(a: *f32) {\n}\n",
		"@concurrent(threads = 8) fn w() {\n}\n",
		"fn t(f: fn(i32): bool) {\n}\n",
		"fn t(xs: i32[], m: i32[4]) {\n}\n",
		"fn t() {\n  var p: *ast::Literal = null\n}\n",
		"fn t() {\n  x >>= 2\n  y <<= 1\n  z %= 3\n}\n",
	}

	for _, src := range sources {
		parseOK(t, src)
	}
}

func TestErrorRecoveryReportsAndContinues(t *testing.T) {
	// A broken first function must not stop the second from being parsed.
	src := "fn a() {\n  var = 1\n}\n\nfn b() {\n}\n"

	var bag diag.Bag
	file := Parse("test.gyro", src, &bag)

	if !bag.HasErrors() {
		t.Fatal("expected an error for the malformed declaration")
	}
	if len(file.Decls) != 2 {
		t.Errorf("recovered %d declarations, want 2", len(file.Decls))
	}
}

func TestParserAlwaysTerminates(t *testing.T) {
	// Malformed input must never leave the parser without forward progress.
	sources := []string{
		"fn", "fn t(", "fn t() {", "struct", "impl {", "match {", "select {",
		"var", "var x =", "import", "export", "@", "}}}", "((((", "fn t() { ]",
		"enum E { A(", "fn t() { if }", "fn t() { for ; ; }", "chan<",
	}

	for _, src := range sources {
		done := make(chan struct{})
		go func() {
			defer close(done)
			var bag diag.Bag
			Parse("test.gyro", src, &bag)
		}()

		select {
		case <-done:
		case <-timeout():
			t.Fatalf("parser did not terminate on %q", src)
		}
	}
}

func TestExamplesParse(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.gyro"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no example programs found")
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			var bag diag.Bag
			Parse(path, string(source), &bag)

			for _, item := range bag.Items() {
				t.Errorf("%s", item)
			}
		})
	}
}

// timeout returns a channel that fires after a generous parse budget.
func timeout() <-chan time.Time {
	return time.After(5 * time.Second)
}
