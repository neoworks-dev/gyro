package lexer

import (
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/token"
)

// scan lexes src and returns the token kinds, dropping the trailing EOF.
func scan(t *testing.T, src string) ([]token.Kind, *diag.Bag) {
	t.Helper()

	var bag diag.Bag
	tokens := Tokens(diag.NewFile("test.gyro", src), &bag)

	kinds := make([]token.Kind, 0, len(tokens))
	for _, tok := range tokens[:len(tokens)-1] {
		kinds = append(kinds, tok.Kind)
	}
	return kinds, &bag
}

func kindString(kinds []token.Kind) string {
	parts := make([]string, len(kinds))
	for i, kind := range kinds {
		parts[i] = kind.String()
	}
	return strings.Join(parts, " ")
}

func TestOperators(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{"+ - * / %", "+ - * / %"},
		{"+= -= *= /= %=", "+= -= *= /= %="},
		{"+% -% *%", "+% -% *%"},
		{"== != < <= > >=", "== != < <= > >="},
		{"&& || ! & | ^ ~", "&& || ! & | ^ ~"},
		{"<< <<= >>=", "<< <<= > >="},
		{":: : .. . , ? @", ":: : .. . , ? @"},
		{"-> <- =>", "-> <- =>"},

		// '>>' is never merged, so nested generics can close cleanly.
		{"Vec<Vec<i32>>", "identifier < identifier < identifier > >"},
	}

	for _, test := range tests {
		kinds, bag := scan(t, test.src)
		if got := kindString(kinds); got != test.want {
			t.Errorf("lexing %q\n got: %s\nwant: %s", test.src, got, test.want)
		}
		if bag.HasErrors() {
			t.Errorf("lexing %q reported errors: %v", test.src, bag.Items())
		}
	}
}

func TestLeftArrowIsGreedy(t *testing.T) {
	// '<-' is scanned greedily, so the ambiguity with a comparison against a
	// negative number exists only when it is written without a space.
	// Splitting the token back apart is the parser's job.
	tests := []struct{ src, want string }{
		{"x<-1", "identifier <- int literal"},
		{"x <- 1", "identifier <- int literal"},
		{"x < -1", "identifier < - int literal"},
		{"ch<-v", "identifier <- identifier"},
	}

	for _, test := range tests {
		kinds, _ := scan(t, test.src)
		if got := kindString(kinds); got != test.want {
			t.Errorf("lexing %q\n got: %s\nwant: %s", test.src, got, test.want)
		}
	}
}

func TestAutomaticTerminators(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "newline after a value ends the statement",
			src:  "a\nb",
			want: "identifier end of statement identifier",
		},
		{
			name: "newline after an operator does not",
			src:  "a +\nb",
			want: "identifier + identifier",
		},
		{
			name: "a leading && continues the condition",
			src:  "a\n&& b",
			want: "identifier && identifier",
		},
		{
			name: "a leading + continues the expression",
			src:  "a\n+ b",
			want: "identifier + identifier",
		},
		{
			name: "a leading == continues the expression",
			src:  "a\n== b",
			want: "identifier == identifier",
		},
		{
			name: "a leading as continues the expression",
			src:  "x\nas f64",
			want: "identifier as identifier",
		},
		{
			name: "a leading identifier starting with as does not continue",
			src:  "x\nassert()",
			want: "identifier end of statement identifier ( )",
		},
		{
			name: "a leading minus starts a new statement",
			src:  "a\n- b",
			want: "identifier end of statement - identifier",
		},
		{
			name: "a leading star starts a new statement",
			src:  "a\n*p = 1",
			want: "identifier end of statement * identifier = int literal",
		},
		{
			name: "a leading receive starts a new statement",
			src:  "a\n<-ch",
			want: "identifier end of statement <- identifier",
		},
		{
			name: "a leading address-of starts a new statement",
			src:  "a\n&b",
			want: "identifier end of statement & identifier",
		},
		{
			name: "blank lines collapse",
			src:  "a\n\n\nb",
			want: "identifier end of statement identifier",
		},
		{
			name: "a leading dot continues a method chain",
			src:  "a\n.b()",
			want: "identifier . identifier ( )",
		},
		{
			name: "a leading comma continues an argument list",
			src:  "f(a\n, b)",
			want: "identifier ( identifier , identifier )",
		},
		{
			name: "a closing brace ends the statement before it",
			src:  "{ a\n}",
			want: "{ identifier end of statement }",
		},
		{
			name: "comments do not suppress the terminator",
			src:  "a // trailing\nb",
			want: "identifier end of statement identifier",
		},
		{
			name: "a block comment spanning lines still terminates",
			src:  "a /* two\nlines */ b",
			want: "identifier end of statement identifier",
		},
		{
			name: "an explicit semicolon is a terminator",
			src:  "a; b",
			want: "identifier end of statement identifier",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kinds, bag := scan(t, test.src)
			if got := kindString(kinds); got != test.want {
				t.Errorf("lexing %q\n got: %s\nwant: %s", test.src, got, test.want)
			}
			if bag.HasErrors() {
				t.Errorf("unexpected errors: %v", bag.Items())
			}
		})
	}
}

func TestNumbers(t *testing.T) {
	tests := []struct {
		src  string
		kind token.Kind
		text string
	}{
		{"42", token.Int, "42"},
		{"0xff", token.Int, "0xff"},
		{"0b1010", token.Int, "0b1010"},
		{"0o755", token.Int, "0o755"},
		{"1_000_000", token.Int, "1_000_000"},
		{"3.14", token.Float, "3.14"},
		{"1e9", token.Float, "1e9"},
		{"1.5e-3", token.Float, "1.5e-3"},

		// A '.' not followed by a digit is field access, not a decimal point.
		{"1.max", token.Int, "1"},
	}

	for _, test := range tests {
		var bag diag.Bag
		tokens := Tokens(diag.NewFile("test.gyro", test.src), &bag)

		if tokens[0].Kind != test.kind || tokens[0].Text != test.text {
			t.Errorf("lexing %q: got %s %q, want %s %q",
				test.src, tokens[0].Kind, tokens[0].Text, test.kind, test.text)
		}
	}
}

func TestStrings(t *testing.T) {
	tests := []struct {
		src          string
		text         string
		interpolated bool
	}{
		{`"hello"`, "hello", false},
		{`"a\nb"`, `a\nb`, false},
		{`"quote \" inside"`, `quote \" inside`, false},
		{`"hi #{name}"`, "hi #{name}", true},
		{`"#{a}#{b}"`, "#{a}#{b}", true},
		{`"#{f(Point{x: 1})}"`, "#{f(Point{x: 1})}", true},
		// An escaped hash is literal text, not the start of interpolation.
		{`"escaped \#{not interp}"`, `escaped \#{not interp}`, false},
	}

	for _, test := range tests {
		var bag diag.Bag
		tokens := Tokens(diag.NewFile("test.gyro", test.src), &bag)

		if tokens[0].Kind != token.String {
			t.Fatalf("lexing %s: got kind %s", test.src, tokens[0].Kind)
		}
		if tokens[0].Text != test.text {
			t.Errorf("lexing %s: got body %q, want %q", test.src, tokens[0].Text, test.text)
		}
		if tokens[0].Interpolated != test.interpolated {
			t.Errorf("lexing %s: interpolated=%v, want %v",
				test.src, tokens[0].Interpolated, test.interpolated)
		}
	}
}

func TestUnterminatedString(t *testing.T) {
	_, bag := scan(t, "\"no closing quote\n")
	if !bag.HasErrors() {
		t.Error("expected an error for an unterminated string")
	}
}

func TestNestedBlockComment(t *testing.T) {
	kinds, bag := scan(t, "a /* outer /* inner */ still outer */ b")
	if want := "identifier identifier"; kindString(kinds) != want {
		t.Errorf("got %s, want %s", kindString(kinds), want)
	}
	if bag.HasErrors() {
		t.Errorf("unexpected errors: %v", bag.Items())
	}
}

func TestKeywords(t *testing.T) {
	src := "var const fn struct enum return if elif else while for in " +
		"match break continue defer async await chan select import from " +
		"as export self true false null _"

	kinds, _ := scan(t, src)
	for i, kind := range kinds {
		if !kind.IsKeyword() {
			t.Errorf("token %d (%s) was not recognized as a keyword", i, kind)
		}
	}
}

// TestNonKeywords guards the names that deliberately stay ordinary
// identifiers: `spawn` is a stdlib function, `move` is contextual, and
// `impl` and `trait` left the language entirely.
func TestNonKeywords(t *testing.T) {
	for _, name := range []string{"spawn", "move", "copy", "println", "len", "impl", "trait"} {
		kinds, _ := scan(t, name)
		if kinds[0] != token.Ident {
			t.Errorf("%q lexed as %s, want an identifier", name, kinds[0])
		}
	}
}

func TestIncrementIsRejected(t *testing.T) {
	for _, src := range []string{"i++", "i--"} {
		_, bag := scan(t, src)
		if !bag.HasErrors() {
			t.Errorf("expected %q to be rejected", src)
			continue
		}
		if !strings.Contains(bag.Items()[0].Message, "= 1") {
			t.Errorf("error should point at the += 1 spelling, got: %s", bag.Items()[0])
		}
	}
}

func TestUnescape(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{`a\nb`, "a\nb"},
		{`a\tb`, "a\tb"},
		{`quote \" here`, `quote " here`},
		{`hash \#{x}`, "hash #{x}"},
		{`\x41`, "A"},
		{`\u{1F600}`, "\U0001F600"},
	}

	for _, test := range tests {
		if got := Unescape(test.in); got != test.want {
			t.Errorf("Unescape(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestPositions(t *testing.T) {
	file := diag.NewFile("test.gyro", "fn main() {\n  var x = 1\n}\n")

	var bag diag.Bag
	tokens := Tokens(file, &bag)

	// The `var` on the second line starts at column 3.
	var found bool
	for _, tok := range tokens {
		if tok.Kind == token.Var {
			pos := file.Position(tok.Offset)
			if pos.Line != 2 || pos.Column != 3 {
				t.Errorf("var at %d:%d, want 2:3", pos.Line, pos.Column)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no var token produced")
	}
}
