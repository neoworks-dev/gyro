// Package lexer turns Gyro source text into a stream of tokens.
//
// Gyro has no statement-terminating semicolons in normal source, so the
// lexer synthesizes token.Terminator at newlines that end a statement. The
// rule is the same shape as Go's: a newline terminates the statement when
// the preceding token could legally end one, unless the following line
// begins with a token that can only continue an expression.
package lexer

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/token"
)

const eof = -1

// Lexer scans a single source file.
type Lexer struct {
	file *diag.File
	src  string
	bag  *diag.Bag

	// offset is the byte index of the character in ch.
	offset int
	// readOffset is the byte index just past ch.
	readOffset int
	ch         rune

	// lastKind is the kind of the most recently emitted token, used to
	// decide whether a newline terminates a statement.
	lastKind token.Kind

	// comments collects every comment skipped so far, in source order.
	// They never reach the token stream; Scan hands them out beside it.
	comments []Comment
}

// Comment is a comment the lexer skipped over. Text is the whole source
// text of the comment, delimiters included.
type Comment struct {
	Offset int
	Text   string
	// Block reports a /* ... */ comment; otherwise it is a // comment that
	// runs to the end of its line.
	Block bool
}

// New creates a lexer over the given file. Diagnostics are appended to bag.
func New(file *diag.File, bag *diag.Bag) *Lexer {
	lexer := &Lexer{
		file:     file,
		src:      file.Src,
		bag:      bag,
		lastKind: token.Invalid,
	}
	lexer.advance()
	return lexer
}

// Tokens scans the whole file and returns every token, ending with EOF.
func Tokens(file *diag.File, bag *diag.Bag) []token.Token {
	lexer := New(file, bag)
	var tokens []token.Token
	for {
		tok := lexer.Next()
		tokens = append(tokens, tok)
		if tok.Kind == token.EOF {
			return tokens
		}
	}
}

// Scan is Tokens plus the comments the token stream leaves out. The token
// stream is exactly Tokens' and does not depend on whether anyone asks for
// the comments.
func Scan(file *diag.File, bag *diag.Bag) ([]token.Token, []Comment) {
	lexer := New(file, bag)
	var tokens []token.Token
	for {
		tok := lexer.Next()
		tokens = append(tokens, tok)
		if tok.Kind == token.EOF {
			return tokens, lexer.comments
		}
	}
}

func (l *Lexer) advance() {
	if l.readOffset >= len(l.src) {
		l.offset = len(l.src)
		l.ch = eof
		return
	}

	l.offset = l.readOffset
	if l.src[l.readOffset] < utf8.RuneSelf {
		l.ch = rune(l.src[l.readOffset])
		l.readOffset++
		return
	}

	r, width := utf8.DecodeRuneInString(l.src[l.readOffset:])
	l.ch = r
	l.readOffset += width
}

// peek returns the character after ch without consuming anything.
func (l *Lexer) peek() rune {
	if l.readOffset >= len(l.src) {
		return eof
	}
	r, _ := utf8.DecodeRuneInString(l.src[l.readOffset:])
	return r
}

func (l *Lexer) errorf(offset int, format string, args ...any) {
	l.bag.Add(diag.Diagnostic{
		Severity: diag.SeverityError,
		Code:     codes[format],
		Pos:      l.file.Position(offset),
		Message:  fmt.Sprintf(format, args...),
	})
}

// Next returns the next token, synthesizing terminators as needed.
func (l *Lexer) Next() token.Token {
	sawNewline, newlineOffset := l.skipSpaceAndComments()

	if sawNewline && l.needsTerminator() {
		return l.emit(token.Token{Kind: token.Terminator, Offset: newlineOffset})
	}

	offset := l.offset

	switch {
	case l.ch == eof:
		return l.emit(token.Token{Kind: token.EOF, Offset: offset})
	case isIdentStart(l.ch):
		return l.emit(l.scanIdent())
	case isDigit(l.ch):
		return l.emit(l.scanNumber())
	case l.ch == '"':
		return l.emit(l.scanString())
	case l.ch == '\'':
		return l.emit(l.scanChar())
	}

	return l.emit(l.scanOperator())
}

func (l *Lexer) emit(tok token.Token) token.Token {
	l.lastKind = tok.Kind
	return tok
}

// skipSpaceAndComments consumes whitespace and comments, reporting whether a
// newline was crossed and at what offset the first such newline sat.
func (l *Lexer) skipSpaceAndComments() (sawNewline bool, newlineOffset int) {
	for {
		switch {
		case l.ch == '\n':
			if !sawNewline {
				sawNewline, newlineOffset = true, l.offset
			}
			l.advance()
		case l.ch == ' ' || l.ch == '\t' || l.ch == '\r':
			l.advance()
		case l.ch == '/' && l.peek() == '/':
			l.skipLineComment()
		case l.ch == '/' && l.peek() == '*':
			if l.skipBlockComment() && !sawNewline {
				sawNewline, newlineOffset = true, l.offset
			}
		default:
			return sawNewline, newlineOffset
		}
	}
}

func (l *Lexer) skipLineComment() {
	start := l.offset
	for l.ch != '\n' && l.ch != eof {
		l.advance()
	}
	l.comments = append(l.comments, Comment{Offset: start, Text: l.src[start:l.offset]})
}

// skipBlockComment consumes a /* ... */ comment, which may nest, and reports
// whether it spanned a newline.
func (l *Lexer) skipBlockComment() (spannedNewline bool) {
	start := l.offset
	defer func() {
		l.comments = append(l.comments, Comment{Offset: start, Text: l.src[start:l.offset], Block: true})
	}()
	l.advance() // '/'
	l.advance() // '*'

	depth := 1
	for depth > 0 {
		switch {
		case l.ch == eof:
			l.errorf(start, "unterminated block comment")
			return spannedNewline
		case l.ch == '\n':
			spannedNewline = true
			l.advance()
		case l.ch == '/' && l.peek() == '*':
			depth++
			l.advance()
			l.advance()
		case l.ch == '*' && l.peek() == '/':
			depth--
			l.advance()
			l.advance()
		default:
			l.advance()
		}
	}
	return spannedNewline
}

// needsTerminator reports whether a newline at the current position ends a
// statement. It considers both the previous token and the token that is
// about to start.
func (l *Lexer) needsTerminator() bool {
	if !endsStatement(l.lastKind) {
		return false
	}
	return !l.continuesExpression()
}

// endsStatement includes '>' and '?' because types can end a line in a
// signature-only declaration: `fn Array<T>::new(): Array<T>` and
// `fn find(): *Node?`. The cost is that a trailing '>' cannot split an
// expression — but operators lead the next line in Gyro, they never trail.
func endsStatement(kind token.Kind) bool {
	switch kind {
	case token.Ident, token.Int, token.Float, token.String, token.Char,
		token.Return, token.Break, token.Continue, token.Self,
		token.True, token.False, token.Null, token.Underscore,
		token.RParen, token.RBracket, token.RBrace,
		token.Gt, token.Question:
		return true
	}
	return false
}

// continuesExpression reports whether the line starting at the current
// character cannot begin a new statement, so the newline before it is not a
// terminator. Leading '.' covers method chains, ',' ')' ']' cover multi-line
// lists, and a leading binary operator lets a long expression break before
// the operator as well as after it.
//
// '-', '*' and '&' are deliberately absent: a statement can begin with them
// (negation, assignment through a pointer, address-of), so those operators
// must trail the line they belong to.
func (l *Lexer) continuesExpression() bool {
	switch l.ch {
	case '.', ',', ')', ']', '+', '/', '%', '|', '^', '>', '=':
		return true
	case '<':
		// A leading '<-' is a receive statement, not a comparison.
		return l.peek() != '-'
	case '!':
		// '!=' continues; a bare '!' begins a negation.
		return l.peek() == '='
	case '&':
		// '&&' continues; a bare '&' begins an address-of.
		return l.peek() == '&'
	case 'a':
		return l.atWord("as")
	}
	return false
}

// atWord reports whether the source at the current position reads exactly
// the given word followed by something that cannot extend an identifier.
func (l *Lexer) atWord(word string) bool {
	end := l.offset + len(word)
	if end > len(l.src) || l.src[l.offset:end] != word {
		return false
	}
	if end == len(l.src) {
		return true
	}
	next, _ := utf8.DecodeRuneInString(l.src[end:])
	return !isIdentPart(next)
}

func (l *Lexer) scanIdent() token.Token {
	offset := l.offset
	for isIdentPart(l.ch) {
		l.advance()
	}
	text := l.src[offset:l.offset]
	return token.Token{Kind: token.Lookup(text), Text: text, Offset: offset}
}

// scanNumber scans an integer or float literal. Underscores are permitted as
// digit separators and are kept in the token text for the parser to strip.
func (l *Lexer) scanNumber() token.Token {
	offset := l.offset
	kind := token.Int

	if l.ch == '0' {
		switch l.peek() {
		case 'x', 'X':
			l.advance()
			l.advance()
			l.scanDigits(isHexDigit)
			return l.numberToken(token.Int, offset)
		case 'b', 'B':
			l.advance()
			l.advance()
			l.scanDigits(isBinDigit)
			return l.numberToken(token.Int, offset)
		case 'o', 'O':
			l.advance()
			l.advance()
			l.scanDigits(isOctDigit)
			return l.numberToken(token.Int, offset)
		}
	}

	l.scanDigits(isDigit)

	// A '.' is only a decimal point when a digit follows; otherwise it is
	// field access or a range, as in `1.max()` or `0..10`.
	if l.ch == '.' && isDigit(l.peek()) {
		kind = token.Float
		l.advance()
		l.scanDigits(isDigit)
	}

	if l.ch == 'e' || l.ch == 'E' {
		kind = token.Float
		l.advance()
		if l.ch == '+' || l.ch == '-' {
			l.advance()
		}
		if !isDigit(l.ch) {
			l.errorf(l.offset, "exponent has no digits")
		}
		l.scanDigits(isDigit)
	}

	return l.numberToken(kind, offset)
}

func (l *Lexer) numberToken(kind token.Kind, offset int) token.Token {
	return token.Token{Kind: kind, Text: l.src[offset:l.offset], Offset: offset}
}

func (l *Lexer) scanDigits(valid func(rune) bool) {
	for valid(l.ch) || l.ch == '_' {
		l.advance()
	}
}

// scanString scans a double-quoted string. The token text is the raw body
// between the quotes: escapes stay encoded so that offsets into #{...}
// interpolation segments remain valid source offsets.
func (l *Lexer) scanString() token.Token {
	offset := l.offset
	l.advance() // opening quote

	bodyStart := l.offset
	interpolated := false

	for {
		switch {
		case l.ch == eof || l.ch == '\n':
			l.errorf(offset, "unterminated string literal")
			return token.Token{Kind: token.String, Text: l.src[bodyStart:l.offset], Offset: offset}
		case l.ch == '"':
			body := l.src[bodyStart:l.offset]
			l.advance() // closing quote
			return token.Token{Kind: token.String, Text: body, Offset: offset, Interpolated: interpolated}
		case l.ch == '\\':
			l.advance()
			l.scanEscape(offset)
		case l.ch == '#' && l.peek() == '{':
			interpolated = true
			l.skipInterpolation(offset)
		default:
			l.advance()
		}
	}
}

// skipInterpolation consumes a #{...} segment, tracking brace depth so that
// nested braces (struct literals, blocks) do not end it early.
func (l *Lexer) skipInterpolation(stringOffset int) {
	l.advance() // '#'
	l.advance() // '{'

	depth := 1
	for depth > 0 {
		switch {
		case l.ch == eof:
			l.errorf(stringOffset, "unterminated interpolation in string literal")
			return
		case l.ch == '{':
			depth++
			l.advance()
		case l.ch == '}':
			depth--
			l.advance()
		case l.ch == '"':
			// A nested string inside the interpolation.
			l.advance()
			for l.ch != '"' && l.ch != eof && l.ch != '\n' {
				if l.ch == '\\' {
					l.advance()
				}
				l.advance()
			}
			l.advance()
		default:
			l.advance()
		}
	}
}

func (l *Lexer) scanChar() token.Token {
	offset := l.offset
	l.advance() // opening quote

	bodyStart := l.offset
	length := 0
	for l.ch != '\'' {
		if l.ch == eof || l.ch == '\n' {
			l.errorf(offset, "unterminated char literal")
			return token.Token{Kind: token.Char, Text: l.src[bodyStart:l.offset], Offset: offset}
		}
		if l.ch == '\\' {
			l.advance()
			l.scanEscape(offset)
		} else {
			l.advance()
		}
		length++
	}

	body := l.src[bodyStart:l.offset]
	l.advance() // closing quote

	if length != 1 {
		l.errorf(offset, "char literal must contain exactly one character")
	}
	return token.Token{Kind: token.Char, Text: body, Offset: offset}
}

// scanEscape consumes the character(s) after a backslash and validates them.
func (l *Lexer) scanEscape(literalOffset int) {
	switch l.ch {
	case 'n', 't', 'r', '0', '\\', '\'', '"', '#':
		l.advance()
	case 'x':
		l.advance()
		for i := 0; i < 2; i++ {
			if !isHexDigit(l.ch) {
				l.errorf(l.offset, "\\x escape needs exactly two hex digits")
				return
			}
			l.advance()
		}
	case 'u':
		l.advance()
		if l.ch != '{' {
			l.errorf(l.offset, "\\u escape needs a braced code point, as in \\u{1F600}")
			return
		}
		l.advance()
		for isHexDigit(l.ch) {
			l.advance()
		}
		if l.ch != '}' {
			l.errorf(l.offset, "unterminated \\u escape")
			return
		}
		l.advance()
	case eof:
		l.errorf(literalOffset, "unterminated escape sequence")
	default:
		l.errorf(l.offset, "unknown escape sequence \\%c", l.ch)
		l.advance()
	}
}

// scanOperator scans punctuation, always preferring the longest match.
func (l *Lexer) scanOperator() token.Token {
	offset := l.offset
	ch := l.ch
	l.advance()

	make := func(kind token.Kind) token.Token {
		return token.Token{Kind: kind, Offset: offset}
	}
	// accept consumes the current character and returns kind when it matches.
	accept := func(want rune, kind token.Kind) (token.Token, bool) {
		if l.ch != want {
			return token.Token{}, false
		}
		l.advance()
		return make(kind), true
	}

	switch ch {
	case '+':
		if l.ch == '+' {
			l.advance()
			l.errorf(offset, "Gyro has no ++; increment is spelled += 1")
			return token.Token{Kind: token.Invalid, Text: "++", Offset: offset}
		}
		if tok, ok := accept('=', token.PlusEq); ok {
			return tok
		}
		if tok, ok := accept('%', token.PlusMod); ok {
			return tok
		}
		return make(token.Plus)
	case '-':
		if l.ch == '-' {
			l.advance()
			l.errorf(offset, "Gyro has no --; decrement is spelled -= 1")
			return token.Token{Kind: token.Invalid, Text: "--", Offset: offset}
		}
		if tok, ok := accept('=', token.MinusEq); ok {
			return tok
		}
		if tok, ok := accept('>', token.Arrow); ok {
			return tok
		}
		if tok, ok := accept('%', token.MinusMod); ok {
			return tok
		}
		return make(token.Minus)
	case '*':
		if tok, ok := accept('=', token.StarEq); ok {
			return tok
		}
		if tok, ok := accept('%', token.StarMod); ok {
			return tok
		}
		return make(token.Star)
	case '/':
		if tok, ok := accept('=', token.SlashEq); ok {
			return tok
		}
		return make(token.Slash)
	case '%':
		if tok, ok := accept('=', token.PercentEq); ok {
			return tok
		}
		return make(token.Percent)
	case '&':
		if tok, ok := accept('&', token.AndAnd); ok {
			return tok
		}
		if tok, ok := accept('=', token.AmpEq); ok {
			return tok
		}
		return make(token.Amp)
	case '|':
		if tok, ok := accept('|', token.OrOr); ok {
			return tok
		}
		if tok, ok := accept('=', token.PipeEq); ok {
			return tok
		}
		return make(token.Pipe)
	case '^':
		if tok, ok := accept('=', token.CaretEq); ok {
			return tok
		}
		return make(token.Caret)
	case '~':
		return make(token.Tilde)
	case '!':
		if tok, ok := accept('=', token.NotEq); ok {
			return tok
		}
		return make(token.Bang)
	case '=':
		if tok, ok := accept('=', token.Eq); ok {
			return tok
		}
		if tok, ok := accept('>', token.FatArrow); ok {
			return tok
		}
		return make(token.Assign)
	case '<':
		if l.ch == '<' {
			l.advance()
			if tok, ok := accept('=', token.ShlEq); ok {
				return tok
			}
			return make(token.Shl)
		}
		if tok, ok := accept('=', token.LtEq); ok {
			return tok
		}
		// '<-' is scanned greedily as one token. It is genuinely ambiguous
		// with `x < -1`, so the parser splits it back into '<' and '-'
		// whenever it appears in binary-operator position.
		if tok, ok := accept('-', token.LeftArrow); ok {
			return tok
		}
		return make(token.Lt)
	case '>':
		// '>>' is not scanned as one token: nested generic arguments such as
		// Result<Vec<i32>, error> would otherwise close with a single '>>'.
		// The parser joins two Gt tokens when it wants a shift.
		if tok, ok := accept('=', token.GtEq); ok {
			return tok
		}
		return make(token.Gt)
	case ':':
		if tok, ok := accept(':', token.ColonColon); ok {
			return tok
		}
		return make(token.Colon)
	case '.':
		if tok, ok := accept('.', token.DotDot); ok {
			// '...' is a third dot on top of the range operator; no range
			// bound may start with '.', so the greedy scan is unambiguous.
			if tok2, ok := accept('.', token.Ellipsis); ok {
				return tok2
			}
			return tok
		}
		return make(token.Dot)
	case ',':
		return make(token.Comma)
	case ';':
		return make(token.Terminator)
	case '?':
		return make(token.Question)
	case '@':
		return make(token.At)
	case '(':
		return make(token.LParen)
	case ')':
		return make(token.RParen)
	case '{':
		return make(token.LBrace)
	case '}':
		return make(token.RBrace)
	case '[':
		return make(token.LBracket)
	case ']':
		return make(token.RBracket)
	}

	l.errorf(offset, "unexpected character %q", ch)
	return token.Token{Kind: token.Invalid, Text: string(ch), Offset: offset}
}

func isIdentStart(ch rune) bool {
	return ch == '_' || unicode.IsLetter(ch)
}

func isIdentPart(ch rune) bool {
	return ch == '_' || unicode.IsLetter(ch) || unicode.IsDigit(ch)
}

func isDigit(ch rune) bool    { return ch >= '0' && ch <= '9' }
func isBinDigit(ch rune) bool { return ch == '0' || ch == '1' }
func isOctDigit(ch rune) bool { return ch >= '0' && ch <= '7' }

func isHexDigit(ch rune) bool {
	return isDigit(ch) || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
}

// Unescape decodes the escape sequences in a raw string or char literal body.
// It is used by the parser once interpolation segments have been split out.
func Unescape(raw string) string {
	if !strings.ContainsRune(raw, '\\') {
		return raw
	}

	var out strings.Builder
	out.Grow(len(raw))

	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 >= len(raw) {
			out.WriteByte(raw[i])
			continue
		}

		i++
		switch raw[i] {
		case 'n':
			out.WriteByte('\n')
		case 't':
			out.WriteByte('\t')
		case 'r':
			out.WriteByte('\r')
		case '0':
			out.WriteByte(0)
		case 'x':
			if i+2 < len(raw) {
				out.WriteByte(byte(parseHex(raw[i+1 : i+3])))
				i += 2
			}
		case 'u':
			end := strings.IndexByte(raw[i:], '}')
			if end > 0 {
				out.WriteRune(rune(parseHex(raw[i+2 : i+end])))
				i += end
			}
		default:
			out.WriteByte(raw[i])
		}
	}
	return out.String()
}

func parseHex(s string) int64 {
	var value int64
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= '0' && ch <= '9':
			value = value*16 + int64(ch-'0')
		case ch >= 'a' && ch <= 'f':
			value = value*16 + int64(ch-'a'+10)
		case ch >= 'A' && ch <= 'F':
			value = value*16 + int64(ch-'A'+10)
		}
	}
	return value
}
