package parser

import "github.com/gyro-lang/gyro/internal/diag"

// codes maps a diagnostic's format string to the code it is reported under.
// The catalogue is one table rather than an argument at every reporting
// site; internal/diag's catalogue test fails when a message is missing here.
var codes = map[string]diag.Code{
	"expected %s, found %s":                 diag.EUnexpectedToken,
	"unexpected %s in string interpolation": diag.EUnexpectedToken,
	"expected a declaration, found %s":      diag.EExpectedDecl,
	"expected an expression, found %s":      diag.EExpectedExpr,
	"expected a type, found %s":             diag.EExpectedType,
	"expected a pattern, found %s":          diag.EExpectedPattern,
	"expected end of statement, found %s":   diag.EUnterminatedStmt,

	"expected { or * after import; Gyro has no default exports":        diag.EImportForm,
	"generic parameters take no bounds; Gyro generics are duck-typed":  diag.EGenericBounds,
	"capture lists hold only `move name` entries; copies are implicit": diag.ECaptureForm,
	"declaration needs either a type annotation or an initializer":     diag.EBindingNeedsType,
	"const declaration needs an initializer":                           diag.EBindingNeedsType,
	"arms end at the newline; there is no comma between them":          diag.EMatchArmForm,
	"defer needs a function call":                                      diag.EDeferNeedsCall,
	"tuple index must be a plain decimal number":                       diag.ETupleIndexForm,
	"expected a tuple index after '.'":                                 diag.ETupleIndexForm,
	"decorators cannot be attached to an export list":                  diag.EDecoratorPlace,
	"expected an assignment after a comma-separated target list":       diag.EAssignmentForm,

	// These abort a speculative parse rather than reaching the user: inside
	// a trial, errorf panics before it reports. They have no code because
	// nothing ever prints them.
	"not a generic call":               diag.NoCode,
	"not a generic struct literal":     diag.NoCode,
	"not a type argument":              diag.NoCode,
	"not a parameterized path segment": diag.NoCode,
}
