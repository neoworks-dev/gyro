package diag

import "sort"

// Code identifies a kind of diagnostic, printed as error[E0401]. Codes are
// stable: what fails with E0401 today fails with E0401 tomorrow, so a code
// can be searched for, explained by `gyro explain`, and matched by an
// editor. A diagnostic with no code prints as a plain error.
//
// The ranges group by compiler stage: E01xx lexical, E02xx syntax, E03xx
// modules and imports, E04xx names, E05xx types, E06xx statements.
type Code string

const (
	NoCode Code = ""

	// Lexical
	EUnexpectedCharacter Code = "E0101"
	EUnterminatedLiteral Code = "E0102"
	EInvalidEscape       Code = "E0103"
	EMalformedLiteral    Code = "E0104"
	ENoSuchOperator      Code = "E0105"

	// Syntax
	EUnexpectedToken  Code = "E0201"
	EExpectedDecl     Code = "E0202"
	EExpectedExpr     Code = "E0203"
	EExpectedType     Code = "E0204"
	EExpectedPattern  Code = "E0205"
	EUnterminatedStmt Code = "E0206"
	EImportForm       Code = "E0208"
	EGenericBounds    Code = "E0209"
	ECaptureForm      Code = "E0210"
	EBindingNeedsType Code = "E0211"
	EMatchArmForm     Code = "E0212"
	EDeferNeedsCall   Code = "E0213"
	ETupleIndexForm   Code = "E0214"
	EDecoratorPlace   Code = "E0215"
	EAssignmentForm   Code = "E0216"

	// Modules and imports
	EModuleNotFound   Code = "E0301"
	EModuleUnreadable Code = "E0302"
	ECircularImport   Code = "E0303"
	ENoSuchStdlib     Code = "E0304"
	ENotExported      Code = "E0305"
	EExportUndeclared Code = "E0306"

	// Names
	EUndeclaredName    Code = "E0401"
	EUndeclaredType    Code = "E0402"
	EAlreadyDeclared   Code = "E0403"
	EWrongKind         Code = "E0404"
	ENoSuchMember      Code = "E0405"
	EUnknownDecorator  Code = "E0406"
	ESelfOutsideMethod Code = "E0407"
	EMoveUnknownName   Code = "E0408"

	// Types
	EMismatchedTypes     Code = "E0501"
	ELiteralOutOfRange   Code = "E0502"
	EBadOperands         Code = "E0503"
	EWrongArgCount       Code = "E0504"
	ETypeArgs            Code = "E0505"
	ENotIndexable        Code = "E0506"
	ETupleArity          Code = "E0507"
	EStructLiteral       Code = "E0508"
	EInvalidConversion   Code = "E0509"
	ENullableUnchecked   Code = "E0510"
	ENotComparable       Code = "E0511"
	EPatternNeverMatches Code = "E0512"
	EIndexNotUsize       Code = "E0513"
	EConditionNotBool    Code = "E0514"
	EInstantiationDepth  Code = "E0515"
	EMatchArmsDisagree   Code = "E0516"
	EDroppedError        Code = "E0517"
	EClosureParams       Code = "E0518"
	EDivisionByZero      Code = "E0519"
	ENotCallable         Code = "E0520"
	EMatchNotExhaustive  Code = "E0521"
	EUnassignedRead      Code = "E0522"

	// Statements
	EAwaitMisuse     Code = "E0601"
	ENotAssignable   Code = "E0602"
	EMethodConflict  Code = "E0603"
	EChannelMisuse   Code = "E0604"
	ERangeMisuse     Code = "E0605"
	EIterationMisuse Code = "E0606"
	EPrivateField    Code = "E0607"
)

// explanation is the long form behind a code, printed by `gyro explain`.
type explanation struct {
	title string
	body  string
}

var explanations = map[Code]explanation{
	EUnexpectedCharacter: {"unexpected character",
		"The character is not part of any Gyro token."},
	EUnterminatedLiteral: {"unterminated literal",
		"A string, char, interpolation or block comment reached the end of the\nfile, or the end of its line, without closing."},
	EInvalidEscape: {"invalid escape sequence",
		"Gyro understands \\n, \\r, \\t, \\\\, \\', \\\", \\0, \\xNN and \\u{...}."},
	EMalformedLiteral: {"malformed literal",
		"The literal's shape is wrong: a char literal holds exactly one\ncharacter, and an exponent needs digits after e."},
	ENoSuchOperator: {"no such operator",
		"Gyro has no ++ or --. Increment is spelled += 1, decrement -= 1, so\nthere is one way to write it and no prefix/postfix question."},

	EUnexpectedToken: {"unexpected token",
		"The parser needed one thing and found another."},
	EExpectedDecl: {"expected a declaration",
		"Only declarations live at the top level of a module: fn, struct, enum,\nvar, const, import, export and decorators on them."},
	EExpectedExpr:    {"expected an expression", "A value was needed here."},
	EExpectedType:    {"expected a type", "A type was needed here."},
	EExpectedPattern: {"expected a pattern", "A match arm begins with a pattern."},
	EUnterminatedStmt: {"statement did not end",
		"Statements end at a newline; the tokens after it belong to no\nstatement. A statement continues across a newline only inside\nbrackets or after an operator."},
	EImportForm: {"malformed import",
		"An import is `import { name } from \"path\"` or `import * as name from\n\"path\"`. Gyro has no default exports, so there is no default import."},
	EGenericBounds: {"generic parameters take no bounds",
		"Generics are duck-typed: a body is checked against every instantiation,\nso a bound would restate what the body already requires."},
	ECaptureForm: {"malformed capture list",
		"A capture list holds `move name` entries. Copies are implicit, so\n`copy name` is only needed to force a copy of a moved name."},
	EBindingNeedsType: {"binding needs a type or a value",
		"There are no zero values: a var without an initializer needs an\nannotation, and a const always needs its value."},
	EMatchArmForm: {"malformed match arm",
		"Arms end at the newline. There is no comma between them."},
	EDeferNeedsCall: {"defer needs a call",
		"defer takes a function call, so there is something to run later."},
	ETupleIndexForm: {"malformed tuple index",
		"A tuple element is reached with .0, .1, ... — a plain decimal number."},
	EDecoratorPlace: {"decorator in the wrong place",
		"Decorators attach to a declaration, not to an export list."},
	EAssignmentForm: {"malformed assignment",
		"A comma-separated target list is a destructuring assignment and needs\none value to destructure."},

	EModuleNotFound: {"module not found",
		"The import specifier resolved to nothing: a relative path that is not\nthere, or a bare name, which needs the Stack package manager."},
	EModuleUnreadable: {"module could not be read",
		"The file the specifier names exists in the graph but could not be\nloaded."},
	ECircularImport: {"circular import",
		"Modules form a cycle. The trace lists it; break it by moving the\nshared declarations into a third module."},
	ENoSuchStdlib: {"no such bundled module",
		"The standard library is a fixed set of modules imported by bare name;\nthe message lists them. A specifier that is neither one of those nor a\nrelative path would be a package under .stack/, which needs the Stack\npackage manager."},
	ENotExported: {"name is not exported",
		"Visibility is explicit: a declaration is visible to importers only\nwith `export`. There is no capitalization rule."},
	EExportUndeclared: {"export names an undeclared name",
		"An export list may only name declarations of this module."},

	EUndeclaredName: {"undeclared name",
		"The name resolves to nothing. Gyro has no universe scope: every name\nis declared in this file or imported, `println` included."},
	EUndeclaredType: {"undeclared type",
		"The type resolves to nothing. Primitives are lowercase (i32, string,\nbool); everything else is declared or imported."},
	EAlreadyDeclared: {"name already declared",
		"A name may shadow an outer scope but may not be declared twice in one\nscope."},
	EWrongKind: {"name used as the wrong kind of thing",
		"The name resolves, but to a type where a value was needed, a module\nwhere a function was, a method used without a call, or similar."},
	ENoSuchMember: {"no such member",
		"The type has no field, method, variant or associated function of that\nname."},
	EUnknownDecorator: {"unknown decorator",
		"The decorators are @comptime, @gpu, @concurrent and @distributed."},
	ESelfOutsideMethod: {"self outside a method",
		"self names the receiver, so it exists only in a fn declared on a type."},
	EMoveUnknownName: {"move captures an unknown name",
		"A capture list moves names that exist in the enclosing scope."},

	EMismatchedTypes: {"mismatched types",
		"Gyro converts nothing implicitly between typed values: i32 does not\nbecome i64, and u8 does not become u16. Convert with `as`."},
	ELiteralOutOfRange: {"literal does not fit",
		"An integer literal is checked against the type its context requires."},
	EBadOperands: {"operator does not apply",
		"The operator is not defined for these operand types, or the operands\n" +
			"do not match each other. Mixed numeric types are converted with `as`:\n" +
			"a widening always succeeds, and a narrowing panics on a value that\n" +
			"does not fit."},
	EWrongArgCount: {"wrong number of arguments",
		"A call passes exactly as many arguments as the signature declares,\nafter defaults are filled in."},
	ETypeArgs: {"type arguments do not work out",
		"They could not be inferred from the arguments, or the count does not\nmatch the declaration. Write them explicitly: name<T>(...)."},
	ENotIndexable: {"value cannot be indexed or reached into",
		"Indexing works on arrays and slices, dereference on pointers, and\nmembers exist on structs and enums."},
	ETupleArity: {"tuple arity does not match",
		"The number of names or targets differs from the number of elements."},
	EStructLiteral: {"malformed struct literal",
		"There are no zero values, so a literal sets every field exactly once,\nand private fields are set only inside their module."},
	EInvalidConversion: {"invalid conversion",
		"`as` converts between numeric types and rune. Nothing else converts."},
	ENullableUnchecked: {"nullable value used without a check",
		"A T? must be compared against null first; the comparison narrows it to\nT on the path where it is not null."},
	ENotComparable: {"values cannot be compared",
		"== is derived for types whose fields all derive it; ordering is\nnumeric, and everything else spells it a.compare(b)."},
	EPatternNeverMatches: {"pattern can never match",
		"The pattern's type is not the subject's, so the arm is dead."},
	EIndexNotUsize: {"index is not usize",
		"Lengths and indexes are usize and never mix with signed arithmetic\nimplicitly."},
	EConditionNotBool: {"condition is not bool",
		"Conditions are bool. There is no truthiness."},
	EInstantiationDepth: {"generic instantiation is too deep",
		"Each instantiation produced another one; the type keeps growing."},
	EMatchArmsDisagree: {"match arms disagree",
		"A match is an expression, so every arm produces the same type."},
	EUnassignedRead: {"value is read before it is assigned",
		"Gyro has no zero values, so `var x: i32` holds nothing until something\nwrites to it. Every path reaching a read must have written it: assigning\nin one branch of an if and not the other is not enough. Taking the\naddress with &x counts as a write, since the storage is real and what\nhappens through the pointer cannot be followed here."},
	EMatchNotExhaustive: {"match is not exhaustive",
		"A match over an enum covers every variant or ends in `_`. Enumerating\nthe variants is what makes adding one later break the matches that have\nto change, so the omission is reported rather than deferred to a panic\nat runtime."},
	EDroppedError: {"error result is dropped",
		"A fallible call returns (value, error?). Dropping the error is\nallowed but never silent: write the discard out."},
	EClosureParams: {"closure parameters do not work out",
		"A closure's parameters take their types from the context; where\nnothing says what the closure is, annotate them."},
	EDivisionByZero: {"division by zero",
		"The divisor folds to zero at compile time."},
	ENotCallable: {"value is not callable",
		"Only functions, methods and associated functions are called."},

	EAwaitMisuse: {"await in the wrong place",
		"await takes a call to an async fn and appears only inside an async fn."},
	ENotAssignable: {"expression cannot be assigned to",
		"Assignment targets a name, a field, an index or a dereference."},
	EMethodConflict: {"method cannot be attached",
		"The owner is not a type of this module, or it already has a method of\nthat name."},
	EChannelMisuse: {"channel operation on a non-channel",
		"Send, receive and capacity need a channel."},
	ERangeMisuse: {"range in the wrong place",
		"`..` builds the range of a for..in loop and is not a value."},
	EIterationMisuse: {"value does not iterate",
		"for..in walks arrays, slices, ranges and channels. A string iterates\nthrough .runes()."},
	EPrivateField: {"field is private to its module",
		"Field visibility is explicit; construct the value with an associated\nfunction instead."},
}

// Explain returns the title and body registered for a code.
func Explain(code Code) (title, body string, found bool) {
	entry, ok := explanations[code]
	if !ok {
		return "", "", false
	}
	return entry.title, entry.body, true
}

// Codes lists every registered code in order, for `gyro explain` with no
// argument.
func Codes() []Code {
	list := make([]Code, 0, len(explanations))
	for code := range explanations {
		list = append(list, code)
	}
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
	return list
}
