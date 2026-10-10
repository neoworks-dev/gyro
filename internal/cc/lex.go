package cc

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The scanner. It produces C's tokens plus the two things only the
// preprocessor cares about: whether a token is the first on its line,
// and whether whitespace came before it. A directive is a `#` at the
// start of a line and nothing else is, and `#x` has to be able to tell
// `a b` from `ab` when it stringizes, so both facts have to survive the
// scan even though neither means anything to the parser.

type position struct {
	file string
	line int
	col  int
}

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokKeyword
	tokInt
	tokFloat
	tokChar
	tokStr
	tokPunct
	// tokInvalid is a run of characters that scans as one token and is
	// not one the language has. It carries its complaint in sval.
	tokInvalid
)

type token struct {
	kind tokenKind
	// text is the token exactly as it was written. Every kind carries it,
	// including the literals, because `##` pastes spellings and an error
	// message reads better with the source's own.
	text string
	// ival and fval carry a numeric literal's value; unsigned and wide
	// carry the suffix, which decides the literal's type. sval is a
	// string literal's contents with the escapes already resolved.
	ival     uint64
	fval     float64
	sval     string
	unsigned bool
	wide     bool
	pos      position

	// bol marks the first token on a line and space marks one with
	// whitespace in front. Both are the preprocessor's.
	bol   bool
	space bool
	// hide is the set of macros this token came out of, which is what
	// stops `#define f(x) f(x)` from expanding forever. Nil until a macro
	// produces the token, and never mutated in place — an expansion that
	// adds a name copies the map first, because two tokens may share one.
	hide map[string]bool
}

func (t token) is(text string) bool {
	return (t.kind == tokPunct || t.kind == tokKeyword) && t.text == text
}

// hidden reports whether this token already came out of the named macro.
func (t token) hidden(name string) bool { return t.hide[name] }

// keywords is the subset this front end knows. `const`, `volatile`,
// `register` and `auto` are accepted and mean nothing: they qualify what
// a C program may do with storage, and every one of those questions is
// answered by METALS' own rules instead.
var keywords = map[string]bool{
	"void": true, "char": true, "short": true, "int": true, "long": true,
	"signed": true, "unsigned": true, "float": true, "double": true,
	"_Bool": true, "struct": true, "union": true, "enum": true,
	"typedef": true, "sizeof": true,
	"static": true, "extern": true,
	"const": true, "volatile": true, "register": true, "auto": true,
	"if": true, "else": true, "while": true, "for": true, "do": true,
	"return": true, "break": true, "continue": true, "goto": true,
	"switch": true, "case": true, "default": true,
}

// punctuators are matched longest-first, which is what keeps `>>=` from
// scanning as `>>` and `=`.
var punctuators = []string{
	"<<=", ">>=", "...",
	"->", "++", "--", "<<", ">>", "<=", ">=", "==", "!=", "&&", "||",
	"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "##",
	"(", ")", "{", "}", "[", "]", ";", ",", ".", "?", ":", "#",
	"+", "-", "*", "/", "%", "<", ">", "&", "|", "^", "~", "!", "=",
}

type scanner struct {
	src  string
	pos  int
	file string
	line int
	// lineStart is the offset the current line begins at, which is what
	// makes a column a column and not a byte offset.
	lineStart int

	// bol and space accumulate across skipped whitespace and attach to
	// the next token.
	bol   bool
	space bool
}

func scan(file, source string) ([]token, error) {
	s := &scanner{src: source, file: file, line: 1, bol: true}
	var out []token
	for {
		tok, err := s.next()
		if err != nil {
			return nil, err
		}
		out = append(out, tok)
		if tok.kind == tokEOF {
			return out, nil
		}
	}
}

func (s *scanner) here() position {
	return position{file: s.file, line: s.line, col: s.pos - s.lineStart + 1}
}

func (s *scanner) next() (token, error) {
	if err := s.skipSpace(); err != nil {
		return token{}, err
	}
	pos := s.here()
	bol, space := s.bol, s.space
	s.bol, s.space = false, false

	mark := func(t token) token {
		t.pos, t.bol, t.space = pos, bol, space
		return t
	}

	if s.pos >= len(s.src) {
		return mark(token{kind: tokEOF}), nil
	}

	ch := s.src[s.pos]
	switch {
	case isIdentStart(ch):
		start := s.pos
		for s.pos < len(s.src) && isIdentPart(s.src[s.pos]) {
			s.pos++
		}
		// A keyword is an identifier here. The preprocessor works on
		// names, and a macro's parameter may be spelled `signed`;
		// what is a keyword is decided once the expansion is done.
		return mark(token{kind: tokIdent, text: s.src[start:s.pos]}), nil

	case ch >= '0' && ch <= '9',
		ch == '.' && s.pos+1 < len(s.src) && isDigit(s.src[s.pos+1]):
		tok, err := s.number(pos)
		if err != nil {
			return token{}, err
		}
		return mark(tok), nil

	case ch == '\'':
		tok, err := s.charLiteral(pos)
		if err != nil {
			return token{}, err
		}
		return mark(tok), nil

	case ch == '"':
		tok, err := s.stringLiteral(pos)
		if err != nil {
			return token{}, err
		}
		return mark(tok), nil
	}

	for _, p := range punctuators {
		if strings.HasPrefix(s.src[s.pos:], p) {
			s.pos += len(p)
			return mark(token{kind: tokPunct, text: p}), nil
		}
	}
	return token{}, errorAt(pos, "stray %q in the program", string(ch))
}

// skipSpace also eats comments and line continuations, since none of the
// three survives into a token. A newline sets bol and everything else
// sets space; a backslash-newline sets neither, which is the whole point
// of writing one — the line it splices is one line.
func (s *scanner) skipSpace() error {
	for s.pos < len(s.src) {
		ch := s.src[s.pos]
		switch {
		case ch == '\\' && s.lineBreakAt(s.pos+1) > 0:
			s.pos += 1 + s.lineBreakAt(s.pos+1)
			s.line++
			s.lineStart = s.pos

		case ch == '\n':
			s.pos++
			s.line++
			s.lineStart = s.pos
			s.bol = true
			s.space = true

		case ch == ' ' || ch == '\t' || ch == '\r' || ch == '\v' || ch == '\f':
			s.pos++
			s.space = true

		case strings.HasPrefix(s.src[s.pos:], "//"):
			for s.pos < len(s.src) && s.src[s.pos] != '\n' {
				s.pos++
			}
			s.space = true

		case strings.HasPrefix(s.src[s.pos:], "/*"):
			start := s.here()
			s.pos += 2
			for {
				if s.pos >= len(s.src) {
					return errorAt(start, "this comment is never closed")
				}
				if s.src[s.pos] == '\n' {
					s.line++
					s.lineStart = s.pos + 1
					// A comment that spans lines still ends a line, so a
					// `#` after it opens a directive.
					s.bol = true
				}
				if strings.HasPrefix(s.src[s.pos:], "*/") {
					s.pos += 2
					break
				}
				s.pos++
			}
			s.space = true

		default:
			return nil
		}
	}
	return nil
}

// lineBreakAt returns the length of the line break at an offset, or 0.
func (s *scanner) lineBreakAt(i int) int {
	switch {
	case i < len(s.src) && s.src[i] == '\n':
		return 1
	case i+1 < len(s.src) && s.src[i] == '\r' && s.src[i+1] == '\n':
		return 2
	}
	return 0
}

// number scans an integer or floating literal. Which one it is cannot be
// known from the first character, so the whole run of literal characters
// is taken first and classified after.
func (s *scanner) number(pos position) (token, error) {
	start := s.pos
	hex := strings.HasPrefix(strings.ToLower(s.src[s.pos:]), "0x")
	if hex {
		s.pos += 2
	}
	float := false
	for s.pos < len(s.src) {
		ch := s.src[s.pos]
		if ch == '.' && !hex {
			float = true
			s.pos++
			continue
		}
		// An exponent's sign is part of the literal, and `e` is a digit
		// in hex, which is why the two cases cannot share a test.
		if !hex && (ch == 'e' || ch == 'E') && s.pos+1 < len(s.src) &&
			(isDigit(s.src[s.pos+1]) || s.src[s.pos+1] == '+' || s.src[s.pos+1] == '-') {
			float = true
			s.pos += 2
			continue
		}
		if isDigit(ch) || (hex && isHexDigit(ch)) {
			s.pos++
			continue
		}
		break
	}
	digits := s.src[start:s.pos]

	// Suffixes: u, l, ul, ull and their cases, in any order C allows.
	tok := token{}
	for s.pos < len(s.src) {
		switch s.src[s.pos] {
		case 'u', 'U':
			tok.unsigned = true
		case 'l', 'L':
			tok.wide = true
		case 'f', 'F':
			if !float {
				return token{}, errorAt(pos, "%sf is a floating suffix on an integer literal", digits)
			}
		default:
			goto done
		}
		s.pos++
	}
done:
	if s.pos < len(s.src) && (isIdentPart(s.src[s.pos]) || s.src[s.pos] == '.') {
		// A run like `32.h` is a preprocessing number: C scans it as one
		// token and only complains if something tries to read it as a
		// number, which text in a branch that is not taken never does.
		// So it is a token that says what is wrong with it, and the
		// parser reports that if the token ever reaches it.
		for s.pos < len(s.src) && (isIdentPart(s.src[s.pos]) || s.src[s.pos] == '.') {
			s.pos++
		}
		raw := s.src[start:s.pos]
		return token{kind: tokInvalid, text: raw, sval: fmt.Sprintf("%q is not a number", raw)}, nil
	}
	tok.text = s.src[start:s.pos]

	if float {
		value, err := strconv.ParseFloat(digits, 64)
		if err != nil && !math.IsInf(value, 0) {
			return token{}, errorAt(pos, "%s is not a number this compiler can read", digits)
		}
		// A literal too large for a double is infinity, which is what
		// <math.h> writes HUGE_VAL as: `1e10000`.
		tok.kind = tokFloat
		tok.fval = value
		return tok, nil
	}

	// 0 leads octal in C, and strconv reads the same prefixes with a base
	// of 0 — including 0b, which C does not have, so it is refused first.
	if strings.HasPrefix(strings.ToLower(digits), "0b") {
		return token{}, errorAt(pos, "%s is not a C integer literal", digits)
	}
	value, err := strconv.ParseUint(digits, 0, 64)
	if err != nil {
		return token{}, errorAt(pos, "%s does not fit an unsigned long long", digits)
	}
	tok.kind = tokInt
	tok.ival = value
	return tok, nil
}

func (s *scanner) charLiteral(pos position) (token, error) {
	start := s.pos
	s.pos++
	if s.pos >= len(s.src) {
		return token{}, errorAt(pos, "this character literal is never closed")
	}

	value, err := s.escapedByte(pos)
	if err != nil {
		return token{}, err
	}

	if s.pos >= len(s.src) || s.src[s.pos] != '\'' {
		return token{}, errorAt(pos, "a character literal holds one character")
	}
	s.pos++
	// A C character constant has type int, and the value of a plain char
	// is signed on this target, so '\xff' is -1 and not 255.
	return token{
		kind: tokChar,
		text: s.src[start:s.pos],
		ival: uint64(int64(int8(value))),
	}, nil
}

func (s *scanner) stringLiteral(pos position) (token, error) {
	start := s.pos
	s.pos++
	var out strings.Builder
	for {
		if s.pos >= len(s.src) || s.src[s.pos] == '\n' {
			return token{}, errorAt(pos, "this string literal is never closed")
		}
		if s.src[s.pos] == '"' {
			s.pos++
			return token{kind: tokStr, text: s.src[start:s.pos], sval: out.String()}, nil
		}
		// A backslash at the end of a line splices it to the next, in
		// the middle of a string as anywhere else.
		if s.src[s.pos] == '\\' && s.lineBreakAt(s.pos+1) > 0 {
			s.pos += 1 + s.lineBreakAt(s.pos+1)
			s.line++
			s.lineStart = s.pos
			continue
		}
		ch, err := s.escapedByte(pos)
		if err != nil {
			return token{}, err
		}
		out.WriteByte(ch)
	}
}

// escapedByte reads one character of a character or string literal,
// resolving an escape sequence into the byte it stands for.
func (s *scanner) escapedByte(pos position) (byte, error) {
	if s.src[s.pos] != '\\' {
		ch := s.src[s.pos]
		s.pos++
		return ch, nil
	}
	s.pos++
	if s.pos >= len(s.src) {
		return 0, errorAt(pos, "this literal ends in a backslash")
	}

	ch := s.src[s.pos]
	switch {
	case ch == 'x':
		s.pos++
		start := s.pos
		for s.pos < len(s.src) && isHexDigit(s.src[s.pos]) {
			s.pos++
		}
		if s.pos == start {
			return 0, errorAt(pos, "\\x needs at least one hex digit")
		}
		value, err := strconv.ParseUint(s.src[start:s.pos], 16, 64)
		if err != nil {
			return 0, errorAt(pos, "\\x%s does not fit a byte", s.src[start:s.pos])
		}
		return byte(value), nil

	case ch >= '0' && ch <= '7':
		// Up to three octal digits, which is the one escape whose length
		// the digits themselves decide.
		start := s.pos
		for s.pos < len(s.src) && s.pos-start < 3 && s.src[s.pos] >= '0' && s.src[s.pos] <= '7' {
			s.pos++
		}
		value, err := strconv.ParseUint(s.src[start:s.pos], 8, 32)
		if err != nil {
			return 0, errorAt(pos, "\\%s is not an octal escape", s.src[start:s.pos])
		}
		return byte(value), nil
	}

	escape, ok := escapes[ch]
	if !ok {
		return 0, errorAt(pos, "\\%c is not an escape this compiler knows", ch)
	}
	s.pos++
	return escape, nil
}

var escapes = map[byte]byte{
	'n': '\n', 't': '\t', 'r': '\r', '\\': '\\',
	'\'': '\'', '"': '"', '?': '?', 'a': 7, 'b': 8, 'f': 12, 'v': 11,
}

func isDigit(ch byte) bool { return ch >= '0' && ch <= '9' }

func isHexDigit(ch byte) bool {
	return isDigit(ch) || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
}

func isIdentStart(ch byte) bool {
	return ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func isIdentPart(ch byte) bool { return isIdentStart(ch) || isDigit(ch) }
