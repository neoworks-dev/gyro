// Package lisp compiles a statically typed Scheme into METALS. It is the
// second producer for the format, and it exists to keep METALS honest:
// anything the IR assumes about its input that only happens to hold
// because Gyro's lowering produces it shows up here as a wrong answer or
// a verifier complaint.
//
// The language is Scheme's core — define, lambda, if, let, set!, begin,
// cond — with declared types. A type declaration is a separate form,
// `(: name (-> i64 i64))`, and the rest is inferred from it. Declaring
// the types is what keeps a call from Gyro an ordinary typed call rather
// than something that carries a tag and is inspected at run time.
package lisp

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A datum is one piece of read syntax. The reader produces these and the
// compiler pattern-matches on them; there is no separate AST, because in
// a Lisp the syntax already is one.
type datum interface {
	pos() int
}

type (
	symbol struct {
		name   string
		offset int
	}
	intLit struct {
		v      int64
		offset int
	}
	floatLit struct {
		v      float64
		offset int
	}
	strLit struct {
		v      string
		offset int
	}
	boolLit struct {
		v      bool
		offset int
	}
	// list is a parenthesized form. The empty list is a list with no
	// items; this Scheme has no dotted pairs in source.
	list struct {
		items  []datum
		offset int
	}
)

func (d *symbol) pos() int   { return d.offset }
func (d *intLit) pos() int   { return d.offset }
func (d *floatLit) pos() int { return d.offset }
func (d *strLit) pos() int   { return d.offset }
func (d *boolLit) pos() int  { return d.offset }
func (d *list) pos() int     { return d.offset }

// read turns a whole source file into its top-level data.
func read(source string) ([]datum, error) {
	r := &reader{src: source}
	var out []datum
	for {
		r.space()
		if r.at >= len(r.src) {
			return out, nil
		}
		d, err := r.datum()
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
}

type reader struct {
	src string
	at  int
}

func (r *reader) errf(offset int, format string, args ...any) error {
	return &posError{offset: offset, msg: fmt.Sprintf(format, args...)}
}

// space skips whitespace and comments. A `;` runs to end of line, and
// `#|...|#` nests, which is what Scheme specifies.
func (r *reader) space() {
	for r.at < len(r.src) {
		ch := r.src[r.at]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f':
			r.at++
		case ch == ';':
			for r.at < len(r.src) && r.src[r.at] != '\n' {
				r.at++
			}
		case ch == '#' && r.at+1 < len(r.src) && r.src[r.at+1] == '|':
			r.blockComment()
		default:
			return
		}
	}
}

func (r *reader) blockComment() {
	depth := 0
	for r.at < len(r.src) {
		if strings.HasPrefix(r.src[r.at:], "#|") {
			depth++
			r.at += 2
			continue
		}
		if strings.HasPrefix(r.src[r.at:], "|#") {
			depth--
			r.at += 2
			if depth == 0 {
				return
			}
			continue
		}
		r.at++
	}
}

func (r *reader) datum() (datum, error) {
	r.space()
	if r.at >= len(r.src) {
		return nil, r.errf(r.at, "unexpected end of file")
	}

	start := r.at
	switch ch := r.src[r.at]; {
	case ch == '(' || ch == '[':
		return r.list(start)
	case ch == ')' || ch == ']':
		return nil, r.errf(start, "unbalanced %q", string(ch))
	case ch == '"':
		return r.text(start)
	case ch == '#':
		return r.hash(start)
	default:
		return r.atom(start)
	}
}

func (r *reader) list(start int) (datum, error) {
	open := r.src[r.at]
	closer := byte(')')
	if open == '[' {
		closer = ']'
	}
	r.at++

	var items []datum
	for {
		r.space()
		if r.at >= len(r.src) {
			return nil, r.errf(start, "unterminated list")
		}
		if ch := r.src[r.at]; ch == ')' || ch == ']' {
			if ch != closer {
				return nil, r.errf(r.at, "list opened with %q closes with %q", string(open), string(ch))
			}
			r.at++
			return &list{items: items, offset: start}, nil
		}
		item, err := r.datum()
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
}

func (r *reader) text(start int) (datum, error) {
	r.at++ // the opening quote
	var out strings.Builder
	for {
		if r.at >= len(r.src) {
			return nil, r.errf(start, "unterminated string")
		}
		ch := r.src[r.at]
		if ch == '"' {
			r.at++
			return &strLit{v: out.String(), offset: start}, nil
		}
		if ch != '\\' {
			out.WriteByte(ch)
			r.at++
			continue
		}

		r.at++
		if r.at >= len(r.src) {
			return nil, r.errf(start, "unterminated string")
		}
		switch escape := r.src[r.at]; escape {
		case 'n':
			out.WriteByte('\n')
		case 't':
			out.WriteByte('\t')
		case 'r':
			out.WriteByte('\r')
		case '\\':
			out.WriteByte('\\')
		case '"':
			out.WriteByte('"')
		default:
			return nil, r.errf(r.at, "unknown escape \\%s", string(escape))
		}
		r.at++
	}
}

func (r *reader) hash(start int) (datum, error) {
	rest := r.src[r.at:]
	switch {
	case strings.HasPrefix(rest, "#t"):
		r.at += 2
		return &boolLit{v: true, offset: start}, nil
	case strings.HasPrefix(rest, "#f"):
		r.at += 2
		return &boolLit{v: false, offset: start}, nil
	}
	return nil, r.errf(start, "unknown # syntax")
}

// atom reads a symbol or a number. Which it is comes out of the text: a
// token that parses as a number is one, and everything else is a symbol,
// so `-` and `+` stay usable as names.
func (r *reader) atom(start int) (datum, error) {
	for r.at < len(r.src) && !terminates(r.src[r.at]) {
		_, size := utf8.DecodeRuneInString(r.src[r.at:])
		r.at += size
	}
	text := r.src[start:r.at]
	if text == "" {
		return nil, r.errf(start, "expected a datum")
	}

	if looksNumeric(text) {
		if n, err := strconv.ParseInt(text, 0, 64); err == nil {
			return &intLit{v: n, offset: start}, nil
		}
		if f, err := strconv.ParseFloat(text, 64); err == nil {
			return &floatLit{v: f, offset: start}, nil
		}
		return nil, r.errf(start, "%q is not a number Gyro can represent", text)
	}
	return &symbol{name: text, offset: start}, nil
}

// looksNumeric decides whether a token should be read as a number at all.
// A leading sign followed by nothing is the symbol `-`, not a number.
func looksNumeric(text string) bool {
	body := text
	if body[0] == '+' || body[0] == '-' {
		body = body[1:]
	}
	if body == "" {
		return false
	}
	return body[0] >= '0' && body[0] <= '9'
}

func terminates(ch byte) bool {
	switch ch {
	case '(', ')', '[', ']', '"', ';':
		return true
	}
	return ch < utf8.RuneSelf && unicode.IsSpace(rune(ch))
}

// posError carries a source offset so the compiler can render it against
// the file the caller holds.
type posError struct {
	offset int
	msg    string
}

func (e *posError) Error() string { return e.msg }
