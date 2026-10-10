// Package token defines the lexical tokens of the Gyro language.
package token

import "strconv"

// Kind identifies the lexical class of a token.
type Kind int

const (
	Invalid Kind = iota
	EOF

	// Terminator is a statement terminator. It is produced by a literal ';'
	// in the source and synthesized by the lexer at a newline that ends a
	// statement (see lexer.needsTerminator).
	Terminator

	literalBegin
	Ident  // main
	Int    // 42, 0xff, 0b1010, 0o755
	Float  // 3.14, 1e9
	String // "hello", "hi #{name}"
	Char   // 'a'
	literalEnd

	operatorBegin
	Plus       // +
	Minus      // -
	Star       // *
	Slash      // /
	Percent    // %
	PlusMod    // +%  wrapping add
	MinusMod   // -%  wrapping subtract
	StarMod    // *%  wrapping multiply
	Amp        // &
	Pipe       // |
	Caret      // ^
	Tilde      // ~
	Shl        // <<
	Shr        // >>
	AndAnd     // &&
	OrOr       // ||
	Bang       // !
	Assign     // =
	PlusEq     // +=
	MinusEq    // -=
	StarEq     // *=
	SlashEq    // /=
	PercentEq  // %=
	AmpEq      // &=
	PipeEq     // |=
	CaretEq    // ^=
	ShlEq      // <<=
	ShrEq      // >>=
	Eq         // ==
	NotEq      // !=
	Lt         // <
	LtEq       // <=
	Gt         // >
	GtEq       // >=
	Arrow      // ->
	LeftArrow  // <-
	FatArrow   // =>
	ColonColon // ::
	Colon      // :
	Dot        // .
	DotDot     // ..
	Ellipsis   // ...
	Comma      // ,
	Question   // ?
	At         // @
	LParen     // (
	RParen     // )
	LBrace     // {
	RBrace     // }
	LBracket   // [
	RBracket   // ]
	operatorEnd

	keywordBegin
	Var
	Const
	Fn
	Struct
	Enum
	Trait
	Impl
	Return
	If
	Elif
	Else
	While
	For
	In
	Match
	Break
	Continue
	Defer
	Async
	Await
	Chan
	Select
	Import
	As
	Export
	Self
	True
	False
	Null
	Underscore // _ is a keyword-like blank identifier
	keywordEnd
)

var kindNames = map[Kind]string{
	Invalid:    "invalid",
	EOF:        "end of file",
	Terminator: "end of statement",

	Ident:  "identifier",
	Int:    "int literal",
	Float:  "float literal",
	String: "string literal",
	Char:   "char literal",

	Plus:       "+",
	Minus:      "-",
	Star:       "*",
	Slash:      "/",
	Percent:    "%",
	PlusMod:    "+%",
	MinusMod:   "-%",
	StarMod:    "*%",
	Amp:        "&",
	Pipe:       "|",
	Caret:      "^",
	Tilde:      "~",
	Shl:        "<<",
	Shr:        ">>",
	AndAnd:     "&&",
	OrOr:       "||",
	Bang:       "!",
	Assign:     "=",
	PlusEq:     "+=",
	MinusEq:    "-=",
	StarEq:     "*=",
	SlashEq:    "/=",
	PercentEq:  "%=",
	AmpEq:      "&=",
	PipeEq:     "|=",
	CaretEq:    "^=",
	ShlEq:      "<<=",
	ShrEq:      ">>=",
	Eq:         "==",
	NotEq:      "!=",
	Lt:         "<",
	LtEq:       "<=",
	Gt:         ">",
	GtEq:       ">=",
	Arrow:      "->",
	LeftArrow:  "<-",
	FatArrow:   "=>",
	ColonColon: "::",
	Colon:      ":",
	Dot:        ".",
	DotDot:     "..",
	Ellipsis:   "...",
	Comma:      ",",
	Question:   "?",
	At:         "@",
	LParen:     "(",
	RParen:     ")",
	LBrace:     "{",
	RBrace:     "}",
	LBracket:   "[",
	RBracket:   "]",

	Var:        "var",
	Const:      "const",
	Fn:         "fn",
	Struct:     "struct",
	Enum:       "enum",
	Trait:      "trait",
	Impl:       "impl",
	Return:     "return",
	If:         "if",
	Elif:       "elif",
	Else:       "else",
	While:      "while",
	For:        "for",
	In:         "in",
	Match:      "match",
	Break:      "break",
	Continue:   "continue",
	Defer:      "defer",
	Async:      "async",
	Await:      "await",
	Chan:       "chan",
	Select:     "select",
	Import:     "import",
	As:         "as",
	Export:     "export",
	Self:       "self",
	True:       "true",
	False:      "false",
	Null:       "null",
	Underscore: "_",
}

func (k Kind) String() string {
	if name, ok := kindNames[k]; ok {
		return name
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

func (k Kind) IsLiteral() bool  { return k > literalBegin && k < literalEnd }
func (k Kind) IsOperator() bool { return k > operatorBegin && k < operatorEnd }
func (k Kind) IsKeyword() bool  { return k > keywordBegin && k < keywordEnd }

var keywords = map[string]Kind{
	"var":      Var,
	"const":    Const,
	"fn":       Fn,
	"struct":   Struct,
	"enum":     Enum,
	"trait":    Trait,
	"impl":     Impl,
	"return":   Return,
	"if":       If,
	"elif":     Elif,
	"else":     Else,
	"while":    While,
	"for":      For,
	"in":       In,
	"match":    Match,
	"break":    Break,
	"continue": Continue,
	"defer":    Defer,
	"async":    Async,
	"await":    Await,
	"chan":     Chan,
	"select":   Select,
	"import":   Import,
	"as":       As,
	"export":   Export,
	"self":     Self,
	"true":     True,
	"false":    False,
	"null":     Null,
	"_":        Underscore,
}

// Lookup maps an identifier to its keyword kind, or Ident if it is not one.
func Lookup(ident string) Kind {
	if kind, ok := keywords[ident]; ok {
		return kind
	}
	return Ident
}

// Token is a single lexeme with its source offset.
type Token struct {
	Kind Kind
	// Text is the literal source text for identifiers and literals. For
	// string and char literals it holds the raw body between the quotes,
	// with escapes still encoded, so that byte offsets inside an
	// interpolated string stay meaningful.
	Text string
	// Offset is the byte offset of the token's first character.
	Offset int
	// Interpolated marks a string literal containing at least one #{...}
	// segment, so the parser knows to split it.
	Interpolated bool
}

func (t Token) String() string {
	if t.Text != "" {
		return t.Kind.String() + "(" + t.Text + ")"
	}
	return t.Kind.String()
}
