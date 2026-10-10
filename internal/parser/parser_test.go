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
			want: "Call Fun: Ident m TypeArg: NamedType Result<_, _> NamedType i32 NamedType error Arg: Ident v",
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

func TestClosures(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want string
	}{
		{
			name: "annotated with a block body",
			expr: "fn(a: i32): i32 { return a * 2 }",
			want: "Closure Param a: Type: NamedType i32 Result: NamedType i32 " +
				"Body: Block Return Binary * Ident a Lit int literal 2",
		},
		{
			name: "inferred types and an expression body",
			expr: "fn(x) x * 2",
			want: "Closure Param x: Body: Binary * Ident x Lit int literal 2",
		},
		{
			name: "no parameters and no result",
			expr: "fn() { greet() }",
			want: "Closure Body: Block ExprStmt Call Fun: Ident greet",
		},
		{
			name: "move capture list",
			expr: "fn[move node]() { free(node) }",
			want: "Closure [move node] Body: Block ExprStmt Call Fun: Ident free Arg: Ident node",
		},
		{
			name: "several move captures",
			expr: "fn[move a, move b]() { use(a) }",
			want: "Closure [move a, move b] Body: Block ExprStmt Call Fun: Ident use Arg: Ident a",
		},
		{
			name: "async closure",
			expr: "async fn(): i32 { return 1 }",
			want: "Closure async Result: NamedType i32 Body: Block Return Lit int literal 1",
		},
		{
			name: "multi-value result",
			expr: "fn(a): (i32, error?) { return a }",
			want: "Closure Param a: Result: NamedType i32 Result: OptionalType NamedType error " +
				"Body: Block Return Ident a",
		},
		{
			name: "closure as a call argument",
			expr: "xs.map(fn(x) x * 2)",
			want: "Call Fun: Selector .map Ident xs Arg: Closure Param x: " +
				"Body: Binary * Ident x Lit int literal 2",
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

func TestFunctionTypeIsNotAClosure(t *testing.T) {
	// `fn(i32): bool` in type position is a type; in expression position the
	// same keyword starts a closure. The two never collide.
	got := dumpOf(t, "fn t(f: fn(i32): bool) {\n}\n")
	want := "File test.gyro Func t Param f: Type: FuncType Param: NamedType i32 " +
		"Result: NamedType bool Body: Block"
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
			src:  "export fn f(): (i32, error?) {\n}\n",
			want: "File test.gyro Func export f Result: NamedType i32 Result: OptionalType NamedType error Body: Block",
		},
		{
			name: "async function",
			src:  "async fn f() {\n}\n",
			want: "File test.gyro Func async f Body: Block",
		},
		{
			name: "method with a pointer receiver",
			src:  "fn Stack::push(*self, v: i32) {\n}\n",
			want: "File test.gyro Func Stack::push Recv: *self " +
				"Param v: Type: NamedType i32 Body: Block",
		},
		{
			name: "effect row",
			src:  "export fn f() !{ io } {\n}\n",
			want: "File test.gyro Func export f Effects: !{io} Body: Block",
		},
		{
			name: "effect row variable",
			src:  "export fn f() !e {\n}\n",
			want: "File test.gyro Func export f Effects: !e Body: Block",
		},
		{
			name: "borrow entry",
			src:  "fn first(xs: i32[]): i32[] !{ &xs } {\n}\n",
			want: "File test.gyro Func first Param xs: Type: ArrayType NamedType i32 " +
				"Result: ArrayType NamedType i32 Effects: !{&xs} Body: Block",
		},
		{
			name: "borrows beside an effect",
			src:  "fn pick(a: i32[], b: i32[]): i32[] !{ io, &a } {\n}\n",
			want: "File test.gyro Func pick Param a: Type: ArrayType NamedType i32 " +
				"Param b: Type: ArrayType NamedType i32 " +
				"Result: ArrayType NamedType i32 Effects: !{io, &a} Body: Block",
		},
		{
			name: "several borrows",
			src:  "fn spans(a: i32[], b: i32[]): i32[] !{ &a, &b } {\n}\n",
			want: "File test.gyro Func spans Param a: Type: ArrayType NamedType i32 " +
				"Param b: Type: ArrayType NamedType i32 " +
				"Result: ArrayType NamedType i32 Effects: !{&a, &b} Body: Block",
		},
		{
			name: "borrow with a row variable",
			src:  "fn head(s: string): string !{ io, &s, ..e } {\n}\n",
			want: "File test.gyro Func head Param s: Type: NamedType string " +
				"Result: NamedType string Effects: !{io, &s, ..e} Body: Block",
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

// `from` separates an import list from its path and is an ordinary
// identifier everywhere else, the way `move` and `copy` are. A library
// that wants a `from` constructor must be able to write one.
func TestFromIsContextual(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{"fn String::from(text: string): String {\n}\n",
			"File test.gyro Func String::from Param text: Type: NamedType string Result: NamedType String Body: Block"},
		{"fn t() {\n  var from = 1\n}\n",
			"File test.gyro Func t Body: Block Var from Value: Lit int literal 1"},
		{"fn t(from: i32): i32 {\n  return from\n}\n",
			"File test.gyro Func t Param from: Type: NamedType i32 Result: NamedType i32 Body: Block Return Ident from"},
	}

	for _, test := range tests {
		if got := dumpOf(t, test.src); got != test.want {
			t.Errorf("parsing %q\n got: %s\nwant: %s", test.src, got, test.want)
		}
	}
}

// A Gyro program cannot declare a union, so `union` is an ordinary
// identifier there. Only the text generated for a foreign module reads it
// as a declaration, because a union is C's and arrives through an import.
func TestUnionIsDeclaredOnlyByForeignText(t *testing.T) {
	if got, want := dumpOf(t, "fn t() {\n  var union = 1\n}\n"),
		"File test.gyro Func t Body: Block Var union Value: Lit int literal 1"; got != want {
		t.Errorf("a variable named union\n got: %s\nwant: %s", got, want)
	}

	var bag diag.Bag
	Parse("test.gyro", "union Bits {\n  whole: i32\n}\n", &bag)
	if !bag.HasErrors() {
		t.Error("a Gyro program declared a union")
	}

	bag = diag.Bag{}
	tree := ParseForeign(diag.NewFile("foreign.gyro", "export union Bits {\n  export whole: i32\n}\n"), &bag)
	if bag.HasErrors() || len(tree.Decls) != 1 {
		t.Fatalf("generated text did not parse: %v", bag.Items())
	}
	if _, ok := tree.Decls[0].(*ast.UnionDecl); !ok {
		t.Errorf("generated text declared %T, want a union", tree.Decls[0])
	}
}

// `unsafe` and `safe` mean something only in front of `fn`, and in front of
// the names of an import. Everywhere else they are ordinary identifiers.
func TestUnsafeAndSafeAreContextual(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{"unsafe fn f() {\n}\n", "File test.gyro Func unsafe f Body: Block"},
		{"safe fn f() {\n}\n", "File test.gyro Func safe f Body: Block"},
		{"export unsafe fn f() {\n}\n", "File test.gyro Func export unsafe f Body: Block"},
		{"export safe fn f<T>(x: T): T {\n  return x\n}\n",
			"File test.gyro Func export safe f<T> Param x: Type: NamedType T Result: NamedType T Body: Block Return Ident x"},
		{"safe async fn f() {\n}\n", "File test.gyro Func safe async f Body: Block"},
		{"unsafe fn Cell::peek(self): i32 !{}\n",
			"File test.gyro Func unsafe Cell::peek Recv: self Result: NamedType i32 Effects: !{}"},
		{"@gpu(\"m\")\nsafe fn sqrt(x: f64): f64 !{}\n",
			"File test.gyro Func safe sqrt Decorator @gpu Arg: Lit string literal \"m\" Param x: Type: NamedType f64 Result: NamedType f64 Effects: !{}"},
		{`import safe { a } from "m.c"` + "\n", `File test.gyro Import safe {a} from "m.c"`},
		{`import safe * as m from "m.c"` + "\n", `File test.gyro Import safe * as m from "m.c"`},

		// Names, wherever a name can be written.
		{"fn t(unsafe: i32, safe: i32): i32 {\n  return unsafe + safe\n}\n",
			"File test.gyro Func t Param unsafe: Type: NamedType i32 Param safe: Type: NamedType i32 Result: NamedType i32 Body: Block Return Binary + Ident unsafe Ident safe"},
		{"fn t() {\n  var safe = 1\n  var unsafe = safe\n}\n",
			"File test.gyro Func t Body: Block Var safe Value: Lit int literal 1 Var unsafe Value: Ident safe"},
		{"fn safe() {\n}\n", "File test.gyro Func safe Body: Block"},
		{"fn unsafe() {\n}\n", "File test.gyro Func unsafe Body: Block"},
		{`import { safe, unsafe } from "m"` + "\n", `File test.gyro Import {safe, unsafe} from "m"`},
	}

	for _, test := range tests {
		if got := dumpOf(t, test.src); got != test.want {
			t.Errorf("parsing %q\n got: %s\nwant: %s", test.src, got, test.want)
		}
	}
}

func TestUnsafeAndSafeTogetherAreRejected(t *testing.T) {
	var bag diag.Bag
	file := Parse("test.gyro", "unsafe safe fn f() {\n}\n", &bag)

	if bag.Len() != 1 || bag.Items()[0].Code != diag.EFuncModifier {
		t.Fatalf("expected one E0218, got %v", bag.Items())
	}
	if len(file.Decls) != 1 {
		t.Errorf("the function should still parse, got %d declarations", len(file.Decls))
	}
}

func TestDefaultImportIsRejected(t *testing.T) {
	// There are no default exports, so a bare name has nothing to bind to.
	var bag diag.Bag
	Parse("test.gyro", "import def from \"m\"\n", &bag)

	if !bag.HasErrors() {
		t.Fatal("expected a default import to be rejected")
	}
	if !strings.Contains(bag.Items()[0].Message, "default export") {
		t.Errorf("error should mention default exports, got: %s", bag.Items()[0])
	}
}

func TestNullableTypes(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "the marker binds to the whole type",
			src:  "fn f(n: *Node?) {\n}\n",
			want: "File test.gyro Func f Param n: Type: OptionalType PointerType NamedType Node Body: Block",
		},
		{
			name: "parentheses move it inward",
			src:  "fn f(n: *(Node?)) {\n}\n",
			want: "File test.gyro Func f Param n: Type: PointerType OptionalType NamedType Node Body: Block",
		},
		{
			name: "a plain pointer is never nullable",
			src:  "fn f(n: *Node) {\n}\n",
			want: "File test.gyro Func f Param n: Type: PointerType NamedType Node Body: Block",
		},
		{
			name: "nullable slice element type",
			src:  "fn f(n: i32[]?) {\n}\n",
			want: "File test.gyro Func f Param n: Type: OptionalType ArrayType NamedType i32 Body: Block",
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

func TestMethods(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "borrowing receiver",
			src:  "fn Version::compare(self, other: Version): i32 {\n}\n",
			want: "File test.gyro Func Version::compare Recv: self " +
				"Param other: Type: NamedType Version Result: NamedType i32 Body: Block",
		},
		{
			name: "exported associated function",
			src:  "export fn Stack::new(capacity: i32): Stack {\n}\n",
			want: "File test.gyro Func export Stack::new Param capacity: Type: NamedType i32 " +
				"Result: NamedType Stack Body: Block",
		},
		{
			name: "method on a generic type",
			src:  "fn Pair<T>::swap(*self) {\n}\n",
			want: "File test.gyro Func Pair<T>::swap Recv: *self Body: Block",
		},
		{
			name: "unbounded generic function",
			src:  "fn max<T>(a: T, b: T): T {\n}\n",
			want: "File test.gyro Func max<T> Param a: Type: NamedType T " +
				"Param b: Type: NamedType T Result: NamedType T Body: Block",
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

func TestGenericBounds(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "one bound",
			src:  "fn max<T: Ord>(a: T, b: T): T {\n}\n",
			want: "File test.gyro Func max<T: Ord> Param a: Type: NamedType T " +
				"Param b: Type: NamedType T Result: NamedType T Body: Block",
		},
		{
			name: "bounds join with an ampersand",
			src:  "fn show<K: Eq & Hash, V>(k: K) {\n}\n",
			want: "File test.gyro Func show<K: Eq & Hash, V> Param k: Type: NamedType K Body: Block",
		},
		{
			name: "a bound may take arguments",
			src:  "fn sum<I: Iterator<i32>>(it: I) {\n}\n",
			want: "File test.gyro Func sum<I: Iterator<_>> Param it: Type: NamedType I Body: Block",
		},
		{
			name: "a type declares bounds on its parameters",
			src:  "struct Map<K: Eq & Hash, V> {\n}\n",
			want: "File test.gyro Struct Map<K: Eq & Hash, V>",
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

func TestOwnerParametersTakeNoBounds(t *testing.T) {
	var bag diag.Bag
	Parse("test.gyro", "fn Pair<T: Eq>::swap(*self) {\n}\n", &bag)

	if !bag.HasErrors() {
		t.Fatal("expected a bound on an owner parameter to be rejected")
	}
	if !strings.Contains(bag.Items()[0].Message, "declared on the type") {
		t.Errorf("error should point at the type, got: %s", bag.Items()[0])
	}
}

func TestTraitsAndImpls(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "a trait lists signatures",
			src:  "trait Element {\n  fn paint(self)\n  fn size(self): i32\n}\n",
			want: "File test.gyro Trait Element Func paint Recv: self Func size Recv: self Result: NamedType i32",
		},
		{
			name: "a default body",
			src:  "export trait Named {\n  fn name(self): string\n  fn shout(self): string {\n    return self.name()\n  }\n}\n",
			want: "File test.gyro Trait export Named Func name Recv: self Result: NamedType string " +
				"Func shout Recv: self Result: NamedType string Body: Block Return Call Fun: Selector .name Self",
		},
		{
			name: "requiring other traits and taking parameters",
			src:  "trait Widget<T>: Element & Named {\n}\n",
			want: "File test.gyro Trait Widget<T>: Element & Named",
		},
		{
			name: "an impl with a block",
			src:  "impl Element for Button {\n  fn paint(self) {\n  }\n}\n",
			want: "File test.gyro Impl Trait: NamedType Element For: NamedType Button Func Button::paint Recv: self Body: Block",
		},
		{
			name: "an impl without a block derives",
			src:  "impl Eq for Point\n",
			want: "File test.gyro Impl derived Trait: NamedType Eq For: NamedType Point",
		},
		{
			name: "an impl for a generic type names its parameters",
			src:  "impl Ord for Pair<T> {\n  fn compare(self, other: Pair<T>): i32 {\n  }\n}\n",
			want: "File test.gyro Impl Trait: NamedType Ord For: NamedType Pair<_> NamedType T " +
				"Func Pair<T>::compare Recv: self Param other: Type: NamedType Pair<_> NamedType T Result: NamedType i32 Body: Block",
		},
		{
			name: "a trait declares associated types",
			src:  "trait Container {\n  type Item\n  type Iter: Iterator<Self::Item> & Named\n  fn first(self): Self::Item\n}\n",
			want: "File test.gyro Trait Container Assoc Item: Assoc Iter: Bound: NamedType Iterator<_> NamedType Self::Item " +
				"Bound: NamedType Named Func first Recv: self Result: NamedType Self::Item",
		},
		{
			name: "an impl gives associated types",
			src:  "impl Container for Box<T> {\n  type Item = T\n}\n",
			want: "File test.gyro Impl Trait: NamedType Container For: NamedType Box<_> NamedType T Assoc Item: Type: NamedType T",
		},
		{
			name: "a bound pins an associated type",
			src:  "fn f<C: Pair<i32, Item = string>>(c: C) {\n}\n",
			want: "File test.gyro Func f<C: Pair<_, Item = _>> Param c: Type: NamedType C Body: Block",
		},
		{
			name: "type stays an identifier",
			src:  "fn f(e: Event): i32 {\n  var type = e.type\n  return type\n}\n",
			want: "File test.gyro Func f Param e: Type: NamedType Event Result: NamedType i32 Body: Block " +
				"Var type Value: Selector .type Ident e Return Ident type",
		},
		{
			name: "dyn is a type",
			src:  "var xs: dyn Element\n",
			want: "File test.gyro Var xs Type: DynType NamedType Element",
		},
		{
			name: "dyn in a cast",
			src:  "fn f() {\n  var e = p as dyn Element\n}\n",
			want: "File test.gyro Func f Body: Block Var e Value: Cast Ident p Type: DynType NamedType Element",
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

func TestDynIsContextual(t *testing.T) {
	// A name may still be `dyn`; only a name after it makes a trait value.
	var bag diag.Bag
	Parse("test.gyro", "fn f() {\n  var dyn = 3\n  var x = dyn + 1\n}\n", &bag)
	if bag.HasErrors() {
		t.Fatalf("dyn should be an ordinary identifier here: %v", bag.Items())
	}
}

func TestImplMisuse(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"an owner inside the block", "impl Eq for P {\n  fn P::eq(self) {\n  }\n}\n", "without an owner"},
		{"only methods", "impl Eq for P {\n  var x = 1\n}\n", "expected a method or an associated type in the impl"},
		{"a pointer is not a declared type", "impl Eq for *P\n", "declared in this module"},
		{"arguments are parameter names", "impl Eq for Pair<*T>\n", "type's own parameters"},
		{"an exported impl", "export impl Eq for P\n", "not exported"},
		{"trait owner", "trait T {\n  fn T::f(self)\n}\n", "without an owner"},
		{"a trait gives no type", "trait T {\n  type Item = i32\n}\n", "declares an associated type without giving it"},
		{"an impl states no bounds", "impl T for P {\n  type Item: Eq = i32\n}\n", "its bounds are the trait's"},
		{"an impl gives a type", "impl T for P {\n  type Item\n}\n", "expected ="},
		{"no argument after a pin", "fn f<C: T<Item = i32, i32>>(c: C) {\n}\n", "cannot follow a pinned associated type"},
		{"a pin is for bounds", "var x: Box<Item = i32>\n", "expected >"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var bag diag.Bag
			Parse("test.gyro", test.src, &bag)
			if !bag.HasErrors() {
				t.Fatal("expected an error")
			}
			if !strings.Contains(bag.Items()[0].Message, test.want) {
				t.Errorf("got %q, want it to mention %q", bag.Items()[0].Message, test.want)
			}
		})
	}
}

func TestTuples(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want string
	}{
		{
			name: "tuple literal",
			expr: `(1, "a")`,
			want: `TupleLit Lit int literal 1 Lit string literal "a"`,
		},
		{
			name: "grouping stays grouping",
			expr: "(1)",
			want: "Lit int literal 1",
		},
		{
			name: "tuple index",
			expr: "pair.0",
			want: "TupleIndex .0 Ident pair",
		},
		{
			name: "nested tuple index splits the float",
			expr: "pair.0.1",
			want: "TupleIndex .1 TupleIndex .0 Ident pair",
		},
		{
			name: "tuple index then field",
			expr: "pair.0.name",
			want: "Selector .name TupleIndex .0 Ident pair",
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

func TestVariadicParameterAndSpread(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "variadic parameter carries the element type",
			src:  "fn f(xs: ...i32) {\n}\n",
			want: "File test.gyro Func f Param xs ...: Type: NamedType i32 Body: Block",
		},
		{
			name: "a fixed parameter may precede the pack",
			src:  "fn f(label: string, rest: ...any) {\n}\n",
			want: "File test.gyro Func f Param label: Type: NamedType string " +
				"Param rest ...: Type: NamedType any Body: Block",
		},
		{
			name: "a spread argument is marked",
			src:  "fn f() {\n  g(xs...)\n}\n",
			want: "File test.gyro Func f Body: Block ExprStmt Call Fun: Ident g Arg ...: Ident xs",
		},
		{
			name: "spread does not swallow the following argument",
			src:  "fn f() {\n  g(xs..., y)\n}\n",
			want: "File test.gyro Func f Body: Block ExprStmt Call Fun: Ident g " +
				"Arg ...: Ident xs Arg: Ident y",
		},
		{
			name: "a range still scans as two dots",
			src:  "fn f() {\n  for i in 0..3 {\n  }\n}\n",
			want: "File test.gyro Func f Body: Block ForIn i Iterable: Range Lit int literal 0 " +
				"Lit int literal 3 Body: Block",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := dumpOf(t, test.src)
			if got != test.want {
				t.Errorf("got:  %s\nwant: %s", got, test.want)
			}
		})
	}
}

func TestTupleTypeInChannel(t *testing.T) {
	got := dumpOf(t, "fn f(ch: chan<(i32, error?)>) {\n}\n")
	want := "File test.gyro Func f Param ch: Type: ChanType TupleType " +
		"NamedType i32 OptionalType NamedType error Body: Block"
	if got != want {
		t.Errorf("got:  %s\nwant: %s", got, want)
	}
}

func TestParameterizedPaths(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{
			// Type arguments on a non-final segment: the associated function
			// of a generic type.
			expr: "Array<i32>::new()",
			want: "Call Fun: Path Array<_>::new",
		},
		{
			expr: "Stack::new(16)",
			want: "Call Fun: Path Stack::new Arg: Lit int literal 16",
		},
		{
			expr: "Stack<i32>{ top: 0 }",
			want: "StructLit Type: NamedType Stack<_> NamedType i32 Field top: Lit int literal 0",
		},
		{
			// Still a comparison: no brace, no call, no `::`.
			expr: "a < b",
			want: "Binary < Ident a Ident b",
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

func TestWrappingOperators(t *testing.T) {
	got := dumpOf(t, wrap("hash *% 31 +% byte"))
	want := "File test.gyro Func t Body: Block Var x Value: Binary +% " +
		"Binary *% Ident hash Lit int literal 31 Ident byte"
	if got != want {
		t.Errorf("got:  %s\nwant: %s", got, want)
	}
}

func TestMatchAndSelect(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "match arms end at the newline",
			src:  "fn t() {\n  var x = match n {\n    0: \"zero\"\n    _: \"other\"\n  }\n}\n",
			want: "File test.gyro Func t Body: Block Var x Value: Match Subject: Ident n " +
				`Arm: Pattern: LiteralPattern Lit int literal 0 Body: ExprStmt Lit string literal "zero" ` +
				`Arm: Pattern: Wildcard Body: ExprStmt Lit string literal "other"`,
		},
		{
			name: "variant patterns are qualified and bind their payload",
			src:  "fn t() {\n  var x = match s {\n    Shape::Circle(r): r\n    Shape::Point: return 0\n  }\n}\n",
			want: "File test.gyro Func t Body: Block Var x Value: Match Subject: Ident s " +
				"Arm: Pattern: VariantPattern Shape::Circle BindingPattern r Body: ExprStmt Ident r " +
				"Arm: Pattern: VariantPattern Shape::Point Body: Return Lit int literal 0",
		},
		{
			name: "a type test binds the whole value",
			src:  "fn t() {\n  var x = match err {\n    RangeError(e): e\n    _: null\n  }\n}\n",
			want: "File test.gyro Func t Body: Block Var x Value: Match Subject: Ident err " +
				"Arm: Pattern: VariantPattern RangeError BindingPattern e Body: ExprStmt Ident e " +
				"Arm: Pattern: Wildcard Body: ExprStmt Lit null null",
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

func TestMatchArmCommaIsRejected(t *testing.T) {
	var bag diag.Bag
	Parse("test.gyro", "fn t() {\n  var x = match n {\n    0: \"zero\",\n    _: \"other\"\n  }\n}\n", &bag)

	if !bag.HasErrors() {
		t.Fatal("expected the arm comma to be rejected")
	}
	if !strings.Contains(bag.Items()[0].Message, "newline") {
		t.Errorf("error should point at the newline style, got: %s", bag.Items()[0])
	}
}

func TestForms(t *testing.T) {
	sources := []string{
		"fn t() {\n  for var i = 0; i < 10; i += 1 {\n  }\n}\n",
		"fn t() {\n  for v in 0..10 {\n  }\n}\n",
		"fn t() {\n  while a < b {\n    break\n  }\n}\n",
		"fn t() {\n  defer free(p)\n}\n",
		"fn t() {\n  var p = alloc<Node>(arena.allocator())\n}\n",
		"fn t(with: i32): i32 {\n  return with\n}\n",
		"fn t() {\n  spawn(fn() worker(ch))\n}\n",
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

// There is no `with` statement: the allocator a call uses is an argument
// written at the call, so the word is an ordinary identifier.
func TestWithIsNotAStatement(t *testing.T) {
	var bag diag.Bag
	Parse("test.gyro", "fn t() {\n  with arena.allocator()\n}\n", &bag)

	if !bag.HasErrors() {
		t.Fatal("expected `with expr` to be rejected")
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
		"fn", "fn t(", "fn t() {", "struct", "match {", "select {",
		"var", "var x =", "import", "export", "@", "}}}", "((((", "fn t() { ]",
		"enum E { A(", "fn t() { if }", "fn t() { for ; ; }", "chan<",
		"fn T::", "fn T::m(", "fn t() { var x = (1,", "fn t() { var x = p.",
		"trait", "trait T {", "trait T: {", "trait T<", "trait T { fn", "trait T { var }",
		"trait T { fn f(self) {", "impl", "impl T", "impl T for", "impl T for S {",
		"impl T for S { fn", "impl T for S { var }", "impl T for *S {", "impl T for S<",
		"trait T { type", "trait T { type X: }", "impl T for S { type", "impl T for S { type X =", "fn f<T: A<X =", "fn f<T: A<X = >>(",
		"fn f<T:", "fn f<T: A &", "fn f<T: >", "var x: dyn", "var x: dyn T<", "x as dyn",
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
