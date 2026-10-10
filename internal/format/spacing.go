package format

import (
	"strings"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/lexer"
	"github.com/gyro-lang/gyro/internal/token"
)

// separator is the text between token a and the token b that follows it on
// the same line.
func (f *formatter) separator(a, b int) string {
	if f.needSpace(a, b) {
		// Two or more spaces between tokens are alignment, as in a table of
		// numbers or a column of match arms, and are kept.
		if gap := f.src[f.toks[a].end:f.toks[b].Offset]; len(gap) > 1 && strings.Trim(gap, " ") == "" {
			return gap
		}
		return " "
	}
	// Never let two tokens written tight read as one longer token.
	if f.wouldMerge(a, b) {
		return " "
	}
	return ""
}

// operandEnd reports whether token t can be the last token of an operand,
// which is what tells a binary operator from a prefix one.
func (f *formatter) operandEnd(t int) bool {
	tok := f.toks[t]
	switch tok.Kind {
	case token.Ident, token.Int, token.Float, token.String, token.Char,
		token.True, token.False, token.Null, token.Self, token.Underscore,
		token.RParen, token.RBracket, token.RBrace, token.Question:
		return true
	case token.Gt:
		return f.layout.Angles[tok.Offset]
	}
	return false
}

// prefix reports whether token t is an operator written before its
// operand: a negation, a dereference, an address, a receive, a spread.
func (f *formatter) prefix(t int) bool {
	switch f.toks[t].Kind {
	case token.Tilde:
		return true
	case token.Minus, token.Star, token.Amp, token.Bang, token.LeftArrow, token.Ellipsis, token.DotDot:
		// `import * as name` and `export * from` are wildcards, not a dereference.
		if t > 0 && f.toks[t].Kind == token.Star && (f.toks[t-1].Kind == token.Import || f.toks[t-1].Kind == token.Export) {
			return false
		}
		return t == 0 || f.toks[t].termBefore || !f.operandEnd(t-1)
	}
	return false
}

func (f *formatter) angle(t int) bool { return f.layout.Angles[f.toks[t].Offset] }

func (f *formatter) needSpace(a, b int) bool {
	ka, kb := f.toks[a].Kind, f.toks[b].Kind

	switch {
	case kb == token.Comma, kb == token.RParen, kb == token.RBracket, kb == token.Terminator:
		return false
	case ka == token.LBrace:
		return kb != token.RBrace
	case kb == token.RBrace:
		return ka != token.LBrace
	case ka == token.LParen, ka == token.LBracket:
		return false
	case kb == token.LBrace:
		// `Point{ x: 1 }` hugs its name, and `!{ io }` its bang.
		return !f.layout.Lists[f.toks[b].Offset] && ka != token.Bang
	case ka == token.Dot, kb == token.Dot, ka == token.ColonColon, kb == token.ColonColon:
		return false
	case ka == token.Gt && !f.angle(a) && (kb == token.Gt || kb == token.GtEq) && f.toks[a].end == f.toks[b].Offset:
		// A shift is two adjacent `>` tokens, which the parser joins.
		return false
	case kb == token.Colon, kb == token.Question, ka == token.At, ka == token.Bang:
		return false
	case ka == token.Colon:
		return true
	case kb == token.Lt && f.angle(b), ka == token.Lt && f.angle(a), kb == token.Gt && f.angle(b):
		return false
	case ka == token.Gt && f.angle(a):
		return kb != token.LParen && kb != token.LBracket
	case f.prefix(a):
		return false
	case kb == token.Ellipsis, kb == token.DotDot:
		// Prefix forms were left to the default; these are the postfix
		// spread and the infix range.
		return f.prefix(b)
	case ka == token.DotDot:
		return false
	case kb == token.LParen:
		switch ka {
		case token.Ident, token.RParen, token.RBracket, token.Self, token.Fn:
			return false
		}
		return true
	case kb == token.LBracket:
		switch ka {
		case token.Ident, token.String, token.Char, token.RParen, token.RBracket, token.Self, token.Fn, token.Question, token.RBrace:
			return false
		}
		return true
	}
	return true
}

// wouldMerge reports whether writing a and b with nothing between them
// would lex as something other than those two tokens.
func (f *formatter) wouldMerge(a, b int) bool {
	ta, tb := f.toks[a].text(f.src), f.toks[b].text(f.src)
	if !isPunct(ta[len(ta)-1]) || !isPunct(tb[0]) {
		return false
	}
	var bag diag.Bag
	joined := ta + tb
	tokens := lexer.Tokens(diag.NewFile("", joined), &bag)
	return len(tokens) != 3 || tokens[0].Kind != f.toks[a].Kind || tokens[1].Kind != f.toks[b].Kind
}

func isPunct(c byte) bool {
	switch c {
	case '+', '-', '*', '/', '%', '&', '|', '^', '~', '<', '>', '!', '=', ':', '.', '?', '@':
		return true
	}
	return false
}
