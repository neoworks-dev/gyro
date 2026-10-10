// Package format rewrites Gyro source into its one canonical layout.
//
// The formatter works on the compiler's own tokens and comments and keeps
// every line break where the author put it: it never breaks or joins a
// line. What it decides is the indentation of each line, the spacing
// between the tokens on it, how many blank lines are kept, where trailing
// comments sit and whether a list written across several lines ends in a
// comma. It has no options.
//
// Source that does not parse is refused untouched. After formatting, the
// result is parsed again and must have the same syntax tree and the same
// comments as the input; if it does not, the formatter has a bug and says
// so instead of returning the text.
package format

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/lexer"
	"github.com/gyro-lang/gyro/internal/parser"
	"github.com/gyro-lang/gyro/internal/token"
)

// ErrSyntax is returned by Source when the input does not parse. The
// parse diagnostics are in the bag the caller passed.
var ErrSyntax = errors.New("source has syntax errors")

const indentWidth = 4

// Source formats one file. Parse diagnostics are added to bag, and the
// error is ErrSyntax when there were any errors among them.
func Source(name, src string, bag *diag.Bag) (string, error) {
	file := diag.NewFile(name, src)
	tree, layout := parser.ParseLayout(file, bag)
	if bag.HasErrors() {
		return "", ErrSyntax
	}

	var scratch diag.Bag
	tokens, comments := lexer.Scan(diag.NewFile(name, src), &scratch)

	f := &formatter{src: src, layout: layout, comments: comments}
	out, err := f.run(tokens)
	if err != nil {
		return "", fmt.Errorf("format %s: %w", name, err)
	}

	if err := verify(name, src, out, ast.Dump(tree), comments); err != nil {
		return "", err
	}
	return out, nil
}

// verify parses the formatted text and compares it with the original.
func verify(name, src, out, want string, comments []lexer.Comment) error {
	var bag diag.Bag
	file := diag.NewFile(name, out)
	tree := parser.ParseFile(file, &bag)
	if bag.HasErrors() {
		return fmt.Errorf("format %s: the formatted text does not parse: %s", name, strings.TrimSpace(bag.Render(diag.Style{})))
	}
	if got := ast.Dump(tree); got != want {
		return fmt.Errorf("format %s: formatting changed the syntax tree", name)
	}

	var scratch diag.Bag
	_, after := lexer.Scan(diag.NewFile(name, out), &scratch)
	if len(after) != len(comments) {
		return fmt.Errorf("format %s: formatting changed the number of comments", name)
	}
	for i := range comments {
		if CommentText(comments[i]) != CommentText(after[i]) {
			return fmt.Errorf("format %s: formatting changed a comment: %q", name, comments[i].Text)
		}
	}
	return nil
}

// CommentText is a comment's text as the formatter keeps it: a line
// comment loses its trailing whitespace, a block comment is untouched.
func CommentText(c lexer.Comment) string {
	if c.Block {
		return c.Text
	}
	return strings.TrimRight(c.Text, " \t\r")
}

// tk is a real token: the lexer's synthesized line terminators are not
// tokens here. They are folded into termBefore on the token after them.
type tk struct {
	token.Token
	end        int
	termBefore bool
}

func (t tk) text(src string) string { return src[t.Offset:t.end] }

// item is a token or a comment, in source order.
type item struct {
	tok     int // index into toks, or -1 for a comment
	comment lexer.Comment
	start   int
	end     int
}

// frame is an open bracket.
type frame struct {
	// savedElem is the indentation the enclosing element started at, which
	// closing the bracket returns to.
	savedElem  int
	lineIndent int  // indentation of the line the bracket opened on
	list       bool // a comma-separated list that takes a trailing comma
}

type line struct {
	indent int
	// ref is what brackets opening on this line measure from. It is the
	// line's own indentation until a bracket that opened on an earlier line
	// closes here, as in `old: i32): i32 {`, after which the line is
	// worth as much as the line that bracket opened on.
	ref int
	// base is indent less the extra level of a continuation line, and
	// baseRef is ref measured from it. A block that opens on a continued
	// line sits one level inside the statement, not inside the
	// continuation: `if a\n    && b {`.
	base, baseRef int
	// opened counts the brackets opened on this line and still open.
	opened   int
	text     strings.Builder
	trailing string // a // comment after the code
	noAlign  bool   // a trailing comment that cannot join a column
	blank    bool   // a blank line precedes this one
}

type formatter struct {
	src      string
	layout   parser.Layout
	comments []lexer.Comment

	toks  []tk
	items []item
	// addComma marks tokens that end the last element of a list written
	// across lines, which gain a trailing comma.
	addComma []bool

	stack []frame
	lines []*line
	cur   *line
	// elemIndent is the indentation of the line the current statement or
	// list element started on; a line that continues it goes one level in.
	elemIndent int
}

func (f *formatter) run(tokens []token.Token) (string, error) {
	if err := f.collect(tokens); err != nil {
		return "", err
	}
	f.mergeItems()
	f.findTrailingCommas()
	f.layoutLines()
	return f.render(), nil
}

// collect keeps the real tokens and works out where each ends.
func (f *formatter) collect(tokens []token.Token) error {
	pending := false
	for i, tok := range tokens {
		switch tok.Kind {
		case token.EOF:
			continue
		case token.Terminator:
			if f.src[tok.Offset] != ';' || (i+1 < len(tokens) && tokens[i+1].Kind == token.Terminator && tokens[i+1].Offset == tok.Offset) {
				pending = true
				continue
			}
		}
		end := tok.Offset + len(tok.Text)
		switch tok.Kind {
		case token.String, token.Char:
			end += 2
		case token.Terminator:
			end = tok.Offset + 1
		default:
			if tok.Kind.IsOperator() {
				end = tok.Offset + len(tok.Kind.String())
			}
		}
		if end > len(f.src) || (!tok.Kind.IsLiteral() && f.src[tok.Offset:end] != tok.Kind.String() && tok.Kind != token.Ident && tok.Kind != token.Terminator) {
			return fmt.Errorf("token %s at offset %d has no source text", tok.Kind, tok.Offset)
		}
		f.toks = append(f.toks, tk{Token: tok, end: end, termBefore: pending})
		pending = false
	}
	return nil
}

func (f *formatter) mergeItems() {
	t, c := 0, 0
	for t < len(f.toks) || c < len(f.comments) {
		if c >= len(f.comments) || (t < len(f.toks) && f.toks[t].Offset < f.comments[c].Offset) {
			f.items = append(f.items, item{tok: t, start: f.toks[t].Offset, end: f.toks[t].end})
			t++
			continue
		}
		cm := f.comments[c]
		f.items = append(f.items, item{tok: -1, comment: cm, start: cm.Offset, end: cm.Offset + len(cm.Text)})
		c++
	}
}

func (f *formatter) newlines(from, to int) int {
	return strings.Count(f.src[from:to], "\n")
}

func isOpener(k token.Kind) bool {
	return k == token.LParen || k == token.LBrace || k == token.LBracket
}

func isCloser(k token.Kind) bool {
	return k == token.RParen || k == token.RBrace || k == token.RBracket
}

// findTrailingCommas marks the last element of every list whose closing
// bracket starts a line, when that element has no comma after it.
func (f *formatter) findTrailingCommas() {
	f.addComma = make([]bool, len(f.toks))
	var open []bool // per open bracket: is it a list
	prevEnd := 0
	for _, it := range f.items {
		if it.tok < 0 {
			prevEnd = it.end
			continue
		}
		t := f.toks[it.tok]
		switch {
		case isOpener(t.Kind):
			open = append(open, f.layout.Lists[t.Offset])
		case isCloser(t.Kind):
			list := false
			if len(open) > 0 {
				list = open[len(open)-1]
				open = open[:len(open)-1]
			}
			if list && it.tok > 0 && f.newlines(prevEnd, it.start) > 0 {
				last := f.toks[it.tok-1]
				if last.Kind != token.Comma && !isOpener(last.Kind) && last.Kind != token.Terminator {
					f.addComma[it.tok-1] = true
				}
			}
		}
		prevEnd = it.end
	}
}

func (f *formatter) layoutLines() {
	prevEnd := 0
	prevTok := -1 // the previous item's token, -1 for a comment or none
	for i, it := range f.items {
		nl := f.newlines(prevEnd, it.start)
		if i == 0 || nl > 0 {
			f.startLine(i, nl >= 2 && i > 0)
		} else if it.tok >= 0 && prevTok >= 0 {
			f.cur.text.WriteString(f.separator(prevTok, it.tok))
		} else if sep := f.commentSeparator(prevTok, it); sep {
			f.cur.text.WriteByte(' ')
		}

		if it.tok < 0 {
			f.comment(it)
		} else {
			f.token(it.tok)
		}
		prevEnd, prevTok = it.end, it.tok
	}
}

func isTrailingLine(it item) bool { return it.tok < 0 && !it.comment.Block }

func (f *formatter) startLine(i int, blank bool) {
	it := f.items[i]
	indent, base := 0, 0
	if it.tok >= 0 {
		indent, base = f.indentFor(it.tok)
	} else {
		indent = f.commentIndent(i)
		base = indent
	}
	f.cur = &line{indent: indent, ref: indent, base: base, baseRef: base, blank: blank}
	f.lines = append(f.lines, f.cur)
}

// indentFor is the indentation of a line that starts with token t, and
// the same less its continuation level.
func (f *formatter) indentFor(t int) (indent, base int) {
	if isCloser(f.toks[t].Kind) {
		if len(f.stack) == 0 {
			return 0, 0
		}
		top := f.stack[len(f.stack)-1].lineIndent
		return top, top
	}
	if f.continues(t) {
		return f.elemIndent + 1, f.elemIndent
	}
	if len(f.stack) > 0 {
		base = f.stack[len(f.stack)-1].lineIndent + 1
	}
	return base, base
}

// continues reports whether token t, first on its line, carries on a
// statement or element from the line before instead of starting one.
func (f *formatter) continues(t int) bool {
	return !f.startsElement(t)
}

// startsElement reports whether token t begins a statement or a list
// element, as opposed to carrying one on.
func (f *formatter) startsElement(t int) bool {
	if t == 0 || f.toks[t].termBefore {
		return true
	}
	switch f.toks[t-1].Kind {
	case token.LBrace, token.LParen, token.LBracket, token.Comma, token.Terminator:
		return true
	}
	return false
}

// commentIndent places a comment on its own line the way gofmt does: with
// the code that follows it, except before a closing bracket, where it
// belongs to what that bracket closes.
func (f *formatter) commentIndent(i int) int {
	for j := i + 1; j < len(f.items); j++ {
		if f.items[j].tok < 0 {
			continue
		}
		t := f.items[j].tok
		if isCloser(f.toks[t].Kind) {
			if len(f.stack) == 0 {
				return 0
			}
			return f.stack[len(f.stack)-1].lineIndent + 1
		}
		indent, _ := f.indentFor(t)
		return indent
	}
	return 0
}

func (f *formatter) comment(it item) {
	text := CommentText(it.comment)
	if it.comment.Block {
		f.cur.text.WriteString(text)
		return
	}
	if f.cur.text.Len() == 0 {
		f.cur.text.WriteString(text)
		return
	}
	f.cur.trailing = text
}

// token appends token t to the current line and tracks brackets.
func (f *formatter) token(t int) {
	tok := f.toks[t]
	if f.startsElement(t) {
		f.elemIndent = f.cur.indent
	}
	if isCloser(tok.Kind) && len(f.stack) > 0 {
		f.elemIndent = f.stack[len(f.stack)-1].savedElem
		open := f.stack[len(f.stack)-1].lineIndent
		f.cur.ref = min(f.cur.ref, open)
		f.cur.baseRef = min(f.cur.baseRef, open)
		f.stack = f.stack[:len(f.stack)-1]
		if f.cur.opened > 0 {
			f.cur.opened--
		}
	}
	f.cur.text.WriteString(tok.text(f.src))
	if f.addComma[t] {
		f.cur.text.WriteByte(',')
	}
	if isOpener(tok.Kind) {
		anchor := f.cur.ref
		if tok.Kind == token.LBrace && f.cur.opened == 0 {
			anchor = f.cur.baseRef
		}
		f.stack = append(f.stack, frame{savedElem: f.elemIndent, lineIndent: anchor, list: f.layout.Lists[tok.Offset]})
		f.cur.opened++
	}
}

// render joins the lines, aligning the // comments that follow code on
// consecutive lines into one column.
func (f *formatter) render() string {
	// Trailing comments: a run of consecutive lines that all have one share
	// a column, one space past the longest line in the run.
	for i := 0; i < len(f.lines); {
		if f.lines[i].trailing == "" {
			i++
			continue
		}
		j := i
		column := 0
		for j < len(f.lines) && f.lines[j].trailing != "" && (j == i || !f.lines[j].blank) {
			if w := f.width(f.lines[j]); w > column {
				column = w
			}
			j++
		}
		for k := i; k < j; k++ {
			l := f.lines[k]
			pad := column + 1 - f.width(l)
			l.text.WriteString(strings.Repeat(" ", pad) + l.trailing)
			l.trailing = ""
		}
		i = j
	}

	var out strings.Builder
	for i, l := range f.lines {
		if l.blank && i > 0 {
			out.WriteByte('\n')
		}
		if l.text.Len() > 0 {
			out.WriteString(strings.Repeat(" ", l.indent*indentWidth))
			out.WriteString(l.text.String())
		}
		out.WriteByte('\n')
	}
	return out.String()
}

// width is the column just past a line's code.
func (f *formatter) width(l *line) int {
	text := l.text.String()
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return l.indent*indentWidth + utf8.RuneCountInString(text)
}

// commentSeparator reports whether a space goes before an item that
// follows a comment, or that is a block comment itself, on one line.
func (f *formatter) commentSeparator(prevTok int, it item) bool {
	if isTrailingLine(it) {
		return false
	}
	if it.tok < 0 {
		// A block comment after a token hugs an opening bracket.
		return prevTok < 0 || (f.toks[prevTok].Kind != token.LParen && f.toks[prevTok].Kind != token.LBracket)
	}
	switch f.toks[it.tok].Kind {
	case token.RParen, token.RBracket, token.Comma:
		return false
	}
	return true
}
