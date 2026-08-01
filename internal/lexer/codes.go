package lexer

import "github.com/gyro-lang/gyro/internal/diag"

// codes maps a diagnostic's format string to the code it is reported under.
// The catalogue is one table rather than an argument at every reporting
// site; internal/diag's catalogue test fails when a message is missing here.
var codes = map[string]diag.Code{
	"unexpected character %q": diag.EUnexpectedCharacter,

	"unterminated block comment":                   diag.EUnterminatedLiteral,
	"unterminated string literal":                  diag.EUnterminatedLiteral,
	"unterminated char literal":                    diag.EUnterminatedLiteral,
	"unterminated interpolation in string literal": diag.EUnterminatedLiteral,

	"unterminated escape sequence":                           diag.EInvalidEscape,
	"unknown escape sequence \\%c":                           diag.EInvalidEscape,
	"unterminated \\u escape":                                diag.EInvalidEscape,
	"\\x escape needs exactly two hex digits":                diag.EInvalidEscape,
	"\\u escape needs a braced code point, as in \\u{1F600}": diag.EInvalidEscape,

	"exponent has no digits":                          diag.EMalformedLiteral,
	"char literal must contain exactly one character": diag.EMalformedLiteral,

	"Gyro has no ++; increment is spelled += 1": diag.ENoSuchOperator,
	"Gyro has no --; decrement is spelled -= 1": diag.ENoSuchOperator,
}
