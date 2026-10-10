package format

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/lexer"
	"github.com/gyro-lang/gyro/internal/parser"
)

// Each case is written by hand: the input as an author might leave it and
// the layout the formatter must produce.
var cases = []struct {
	name string
	in   string
	want string
}{
	{
		name: "indentation follows brackets",
		in:   "fn main() {\n  if x {\n      f()\n   }\n}\n",
		want: "fn main() {\n    if x {\n        f()\n    }\n}\n",
	},
	{
		name: "binary operators and commas",
		in:   "fn f() {\n  var x=a+b*c\n  g(a,b , c)\n}\n",
		want: "fn f() {\n    var x = a + b * c\n    g(a, b, c)\n}\n",
	},
	{
		name: "no space inside parens and brackets",
		in:   "fn f() {\n  g( a, xs[ 1 ] )\n}\n",
		want: "fn f() {\n    g(a, xs[1])\n}\n",
	},
	{
		name: "braces hug struct names and pad their contents",
		in:   "fn f() {\n  var p = Point{x: 1, y: 2}\n  var e = Empty{ }\n  var q = Stack<i32>{ items: xs }\n}\n",
		want: "fn f() {\n    var p = Point{ x: 1, y: 2 }\n    var e = Empty{}\n    var q = Stack<i32>{ items: xs }\n}\n",
	},
	{
		name: "signatures and effect rows",
		in:   "fn name(a:i32,b : string):Result !{io,&a}{\n  return\n}\nfn g(): i32 !e\n",
		want: "fn name(a: i32, b: string): Result !{ io, &a } {\n    return\n}\nfn g(): i32 !e\n",
	},
	{
		name: "unary and binary operators",
		in:   "fn f() {\n  var a = -x\n  var b = x-y\n  var c = *p * *q\n  var d = &x\n  var e = !ok\n  var f = x < -1\n  var g = <-ch\n  ch <- v\n}\n",
		want: "fn f() {\n    var a = -x\n    var b = x - y\n    var c = *p * *q\n    var d = &x\n    var e = !ok\n    var f = x < -1\n    var g = <-ch\n    ch <- v\n}\n",
	},
	{
		name: "generics hug their brackets and comparisons do not",
		in:   "fn f() {\n  var a = Array<i32>::new(h)\n  var b = x<y\n  var c = Map<string, Vec<i32>>::new(h)\n  var d = a>>2\n}\n",
		want: "fn f() {\n    var a = Array<i32>::new(h)\n    var b = x < y\n    var c = Map<string, Vec<i32>>::new(h)\n    var d = a >> 2\n}\n",
	},
	{
		name: "ranges, wildcards and casts",
		in:   "import * as m from \"./m\"\nfn f() {\n  for i in 0 .. 10 {\n    var s = xs[1 .. 3]\n    var n = i as f64\n  }\n}\n",
		want: "import * as m from \"./m\"\nfn f() {\n    for i in 0..10 {\n        var s = xs[1..3]\n        var n = i as f64\n    }\n}\n",
	},
	{
		name: "alignment padding is kept",
		in:   "var t = [\n  1,   2,\n  30,  4,\n]\n",
		want: "var t = [\n    1,   2,\n    30,  4,\n]\n",
	},
	{
		name: "blank lines collapse to one and single ones stay",
		in:   "fn a() {}\n\n\n\nfn b() {\n\n  f()\n\n}\n",
		want: "fn a() {}\n\nfn b() {\n\n    f()\n\n}\n",
	},
	{
		name: "leading and trailing blank lines go",
		in:   "\n\nfn a() {}\n\n\n",
		want: "fn a() {}\n",
	},
	{
		name: "own-line comments take the indentation of the code after them",
		in:   "fn f() {\n// before\n  f()\n      // inside\n  g()\n// last\n}\n// end\n",
		want: "fn f() {\n    // before\n    f()\n    // inside\n    g()\n    // last\n}\n// end\n",
	},
	{
		name: "trailing comments on consecutive lines share a column",
		in:   "fn f() {\n  var a = 1 // one\n  var bcd = 2   // two\n  var e = 3\n  var f = 4 // four\n}\n",
		want: "fn f() {\n    var a = 1   // one\n    var bcd = 2 // two\n    var e = 3\n    var f = 4 // four\n}\n",
	},
	{
		name: "block comments stay where they are",
		in:   "fn f(/* a */ x: i32) {\n/* one\n   two */\n  f()\n}\n",
		want: "fn f(/* a */ x: i32) {\n    /* one\n   two */\n    f()\n}\n",
	},
	{
		name: "a list across lines gains a trailing comma",
		in:   "fn f() {\n  g(\n    a,\n    b\n  )\n  var p = Point{\n    x: 1,\n    y: 2\n  }\n  var xs = [\n    1,\n    2 // two\n  ]\n}\n",
		want: "fn f() {\n    g(\n        a,\n        b,\n    )\n    var p = Point{\n        x: 1,\n        y: 2,\n    }\n    var xs = [\n        1,\n        2, // two\n    ]\n}\n",
	},
	{
		name: "parameter lists across lines gain one too",
		in:   "fn f(\n  a: i32,\n  b: i32\n): i32 {\n  return a\n}\n",
		want: "fn f(\n    a: i32,\n    b: i32,\n): i32 {\n    return a\n}\n",
	},
	{
		name: "a list on one line is left on one line",
		in:   "fn f() {\n  g(a, b)\n}\n",
		want: "fn f() {\n    g(a, b)\n}\n",
	},
	{
		name: "grouping parens and blocks never gain commas",
		in:   "fn f() {\n  var x = (a\n    + b\n  )\n  if c {\n    g()\n  }\n}\n",
		want: "fn f() {\n    var x = (a\n        + b\n    )\n    if c {\n        g()\n    }\n}\n",
	},
	{
		name: "continuation lines are indented one level",
		in:   "fn f() {\n  return a\n  || b\n      || c\n}\n",
		want: "fn f() {\n    return a\n        || b\n        || c\n}\n",
	},
	{
		name: "a block after a continued condition sits inside the statement",
		in:   "fn f() {\n  if a\n  && b {\n    g()\n  }\n}\n",
		want: "fn f() {\n    if a\n        && b {\n        g()\n    }\n}\n",
	},
	{
		name: "a closing paren on the line of a body brace",
		in:   "fn f(a: i32,\n  b: i32): i32 {\n  return a\n}\n",
		want: "fn f(a: i32,\n    b: i32): i32 {\n    return a\n}\n",
	},
	{
		name: "semicolons",
		in:   "fn f() {\n  for var i = 0;i < 3;i += 1 {\n    g()\n  }\n}\n",
		want: "fn f() {\n    for var i = 0; i < 3; i += 1 {\n        g()\n    }\n}\n",
	},
	{
		name: "strings are untouched",
		in:   "fn f() {\n  g(\"a  +  #{x+y}\")\n}\n",
		want: "fn f() {\n    g(\"a  +  #{x+y}\")\n}\n",
	},
}

func TestFormat(t *testing.T) {
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bag diag.Bag
			got, err := Source("case.gyro", tc.in, &bag)
			if err != nil {
				t.Fatalf("%v\n%s", err, bag.Render(diag.Style{}))
			}
			if got != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}

			again, err := Source("case.gyro", got, &bag)
			if err != nil || again != got {
				t.Fatalf("not idempotent: %v\n%s", err, again)
			}
		})
	}
}

func TestRefusesSourceThatDoesNotParse(t *testing.T) {
	var bag diag.Bag
	_, err := Source("bad.gyro", "fn main( {\n  var x =\n", &bag)
	if !errors.Is(err, ErrSyntax) {
		t.Fatalf("want ErrSyntax, got %v", err)
	}
	if !bag.HasErrors() {
		t.Fatal("the parse diagnostics were not reported")
	}
}

// repositoryRoot is the module root, two levels above this package.
const repositoryRoot = "../.."

// gyroFiles lists every .gyro file in the repository, leaving out other
// worktrees and installed packages.
func gyroFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".worktrees", "node_modules", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".gyro") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 100 {
		t.Fatalf("found only %d .gyro files; is the repository root wrong?", len(files))
	}
	return files
}

func parseDump(name, src string) (string, bool) {
	var bag diag.Bag
	tree := parser.ParseFile(diag.NewFile(name, src), &bag)
	return ast.Dump(tree), !bag.HasErrors()
}

func commentTexts(src string) []string {
	var bag diag.Bag
	_, comments := lexer.Scan(diag.NewFile("", src), &bag)
	var texts []string
	for _, c := range comments {
		texts = append(texts, CommentText(c))
	}
	return texts
}

// TestRepositoryFilesKeepTheirMeaning formats every .gyro file in the
// repository that parses and checks what must never change: the syntax
// tree, the comments, and that a second pass changes nothing.
func TestRepositoryFilesKeepTheirMeaning(t *testing.T) {
	for _, path := range gyroFiles(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)

		before, parses := parseDump(path, src)
		var bag diag.Bag
		out, err := Source(path, src, &bag)
		if !parses {
			if !errors.Is(err, ErrSyntax) || !bag.HasErrors() {
				t.Errorf("%s does not parse and was not refused with diagnostics: %v", path, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}

		after, ok := parseDump(path, out)
		if !ok {
			t.Errorf("%s: the formatted text does not parse", path)
			continue
		}
		if after != before {
			t.Errorf("%s: formatting changed the syntax tree", path)
		}

		want, got := commentTexts(src), commentTexts(out)
		if strings.Join(want, "\x00") != strings.Join(got, "\x00") {
			t.Errorf("%s: formatting changed the comments", path)
		}

		var again diag.Bag
		twice, err := Source(path, out, &again)
		if err != nil || twice != out {
			t.Errorf("%s: formatting is not idempotent (%v)", path, err)
		}
	}
}

// TestRepositoryIsFormatted is `gofmt -l` for Gyro: it fails, naming the
// files, when a .gyro file that parses is not already in the formatter's
// layout. Run `gyro fmt .` to fix them.
func TestRepositoryIsFormatted(t *testing.T) {
	var unformatted []string
	for _, path := range gyroFiles(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var bag diag.Bag
		out, err := Source(path, string(data), &bag)
		if errors.Is(err, ErrSyntax) {
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if out != string(data) {
			unformatted = append(unformatted, path)
		}
	}
	if len(unformatted) > 0 {
		t.Errorf("not formatted, run `gyro fmt` on them:\n\t%s", strings.Join(unformatted, "\n\t"))
	}
}
