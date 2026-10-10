// Package parser builds a Gyro syntax tree from source text.
//
// The parser is a hand-written recursive-descent parser for declarations and
// statements, with a precedence-climbing loop for expressions. It buffers the
// whole token stream up front, which makes the bounded backtracking needed to
// disambiguate `a < b` from `f<T>(x)` a matter of resetting an index.
package parser

import (
	"fmt"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/lexer"
	"github.com/gyro-lang/gyro/internal/token"
)

// trialFailure is panicked to abandon a speculative parse. It never escapes
// the trial that raised it.
type trialFailure struct{}

// Parser holds the state of one file's parse.
type Parser struct {
	file   *diag.File
	bag    *diag.Bag
	tokens []token.Token
	index  int

	// inTrial suppresses diagnostics and converts errors into a panic that
	// unwinds to the enclosing trial call.
	inTrial bool
	// noStructLit disables `Name { ... }` struct literals while parsing the
	// header of an if, while, for or match, where the brace must open the
	// body instead. Rust solves the same ambiguity the same way.
	noStructLit bool
	// foreign marks the text the loader generates from a foreign module's
	// exports. It alone may declare a union: a union is a C type, and a
	// Gyro program only ever receives one through an import.
	foreign bool

	// angles and lists are what a formatter needs to know about the tokens
	// and the token stream cannot say. They record offsets as the parse
	// commits to a reading, and a failed trial forgets what it recorded.
	angles []int
	lists  []int
}

// Layout is what the parser knows about a file's tokens that the tokens
// themselves do not say. A formatter works on tokens, and `<` is a
// comparison in one place and a bracket in another; only the parse tells.
type Layout struct {
	// Angles holds the offsets of the `<` and `>` tokens that delimit type
	// arguments or type parameters.
	Angles map[int]bool
	// Lists holds the offsets of the `(`, `{` and `[` tokens that open a
	// comma-separated list taking a trailing comma: call arguments,
	// parameters, struct literals and array literals.
	Lists map[int]bool
}

// ParseLayout is ParseFile that also reports the file's Layout.
func ParseLayout(file *diag.File, bag *diag.Bag) (*ast.File, Layout) {
	bag.Track(file)
	parser := &Parser{
		file:   file,
		bag:    bag,
		tokens: lexer.Tokens(file, bag),
	}
	tree := parser.parseFile()

	layout := Layout{Angles: map[int]bool{}, Lists: map[int]bool{}}
	for _, offset := range parser.angles {
		layout.Angles[offset] = true
	}
	for _, offset := range parser.lists {
		layout.Lists[offset] = true
	}
	return tree, layout
}

// expectAngle is expect for the `<` and `>` that bracket type arguments.
func (p *Parser) expectAngle(kind token.Kind) token.Token {
	if p.kind() == kind {
		p.angles = append(p.angles, p.offset())
	}
	return p.expect(kind)
}

// noteList records the token at the parser as opening a list that takes a
// trailing comma.
func (p *Parser) noteList() {
	p.lists = append(p.lists, p.offset())
}

// Parse parses one source file. It returns a file even when errors were
// reported, so later stages can still inspect whatever was recovered.
func Parse(name, src string, bag *diag.Bag) *ast.File {
	file := diag.NewFile(name, src)
	bag.Track(file)
	parser := &Parser{
		file:   file,
		bag:    bag,
		tokens: lexer.Tokens(file, bag),
	}
	return parser.parseFile()
}

// ParseFile is Parse with the diag.File already built, for callers that need
// to resolve positions themselves.
func ParseFile(file *diag.File, bag *diag.Bag) *ast.File {
	bag.Track(file)
	parser := &Parser{
		file:   file,
		bag:    bag,
		tokens: lexer.Tokens(file, bag),
	}
	return parser.parseFile()
}

// ParseForeign parses the Gyro text generated from a foreign module's
// exports. It is ParseFile plus the one declaration a Gyro program cannot
// write, the union, so that a C union arrives as the type C declared.
func ParseForeign(file *diag.File, bag *diag.Bag) *ast.File {
	bag.Track(file)
	parser := &Parser{
		file:    file,
		bag:     bag,
		tokens:  lexer.Tokens(file, bag),
		foreign: true,
	}
	return parser.parseFile()
}

// ---------------------------------------------------------------------------
// Token access
// ---------------------------------------------------------------------------

func (p *Parser) tok() token.Token {
	return p.tokens[p.index]
}

func (p *Parser) kind() token.Kind {
	return p.tokens[p.index].Kind
}

func (p *Parser) offset() int {
	return p.tokens[p.index].Offset
}

// peekAt returns the token n positions ahead, clamped to the final EOF.
func (p *Parser) peekAt(n int) token.Token {
	index := p.index + n
	if index >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[index]
}

func (p *Parser) at(kind token.Kind) bool {
	return p.kind() == kind
}

func (p *Parser) atEOF() bool {
	return p.kind() == token.EOF
}

func (p *Parser) next() token.Token {
	tok := p.tokens[p.index]
	if p.index < len(p.tokens)-1 {
		p.index++
	}
	return tok
}

// accept consumes the current token when it matches and reports whether it did.
func (p *Parser) accept(kind token.Kind) bool {
	if p.kind() != kind {
		return false
	}
	p.next()
	return true
}

// expect consumes the current token, reporting an error when it does not match.
func (p *Parser) expect(kind token.Kind) token.Token {
	if p.kind() != kind {
		p.errorf(p.offset(), "expected %s, found %s", kind, describe(p.tok()))
		return token.Token{Kind: token.Invalid, Offset: p.offset()}
	}
	return p.next()
}

// expectIdent consumes an identifier and returns its text.
func (p *Parser) expectIdent() (string, int) {
	tok := p.expect(token.Ident)
	return tok.Text, tok.Offset
}

func describe(tok token.Token) string {
	switch tok.Kind {
	case token.Ident, token.Int, token.Float:
		return tok.Kind.String() + " " + tok.Text
	case token.String:
		return "string literal"
	default:
		return tok.Kind.String()
	}
}

func (p *Parser) errorf(offset int, format string, args ...any) {
	if p.inTrial {
		panic(trialFailure{})
	}
	p.bag.Add(diag.Diagnostic{
		Severity: diag.SeverityError,
		Code:     codes[format],
		Pos:      p.file.Position(offset),
		Message:  fmt.Sprintf(format, args...),
	})
}

// ---------------------------------------------------------------------------
// Terminators
// ---------------------------------------------------------------------------

// skipTerminators consumes any run of statement terminators. Blank lines and
// stray semicolons are never significant.
func (p *Parser) skipTerminators() {
	for p.at(token.Terminator) {
		p.next()
	}
}

// expectTerminator consumes the end of a statement. A closing brace or end of
// file also ends a statement, so `{ var x = 1 }` on one line is legal.
func (p *Parser) expectTerminator() {
	switch p.kind() {
	case token.Terminator:
		p.skipTerminators()
	case token.RBrace, token.EOF:
		// Handled by the enclosing block.
	default:
		p.errorf(p.offset(), "expected end of statement, found %s", describe(p.tok()))
		p.syncStatement()
	}
}

// syncStatement skips ahead to a point where parsing can resume after an
// error, without consuming the brace that closes the enclosing block.
func (p *Parser) syncStatement() {
	depth := 0
	for {
		switch p.kind() {
		case token.EOF:
			return
		case token.Terminator:
			if depth == 0 {
				p.skipTerminators()
				return
			}
			p.next()
		case token.LBrace, token.LParen, token.LBracket:
			depth++
			p.next()
		case token.RBrace, token.RParen, token.RBracket:
			if depth == 0 {
				return
			}
			depth--
			p.next()
		default:
			p.next()
		}
	}
}

// ---------------------------------------------------------------------------
// Speculative parsing
// ---------------------------------------------------------------------------

// trial runs fn with diagnostics suppressed. If fn reports an error, the
// token position is restored and trial reports false.
func (p *Parser) trial(fn func()) (ok bool) {
	saved, savedInTrial := p.index, p.inTrial
	savedAngles, savedLists := len(p.angles), len(p.lists)
	p.inTrial = true
	ok = true

	defer func() {
		p.inTrial = savedInTrial
		if r := recover(); r != nil {
			if _, isTrial := r.(trialFailure); !isTrial {
				panic(r)
			}
			p.index = saved
			ok = false
		}
		if !ok {
			p.index = saved
			p.angles, p.lists = p.angles[:savedAngles], p.lists[:savedLists]
		}
	}()

	fn()
	return ok
}

// withStructLits runs fn with struct-literal parsing forced on or off. Call
// arguments and bracketed subexpressions re-enable it even inside an `if`
// header, because the delimiters remove the ambiguity.
func (p *Parser) withStructLits(allowed bool, fn func()) {
	saved := p.noStructLit
	p.noStructLit = !allowed
	fn()
	p.noStructLit = saved
}
