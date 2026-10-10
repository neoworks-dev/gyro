package sema

import "github.com/gyro-lang/gyro/internal/diag"

// codes maps a diagnostic's format string to the code it is reported under.
// The catalogue is one table rather than an argument at every reporting
// site; internal/diag's catalogue test fails when a message is missing here.
var codes = map[string]diag.Code{
	// Names. The undeclared-name and already-declared diagnostics are built
	// by hand in suggest.go and sema.go, and the leak warning in leak.go, so
	// their codes sit at those sites rather than here.
	"cannot move %s: no such name in scope":                                                   diag.EMoveUnknownName,
	"unknown decorator @%s; the decorators are @comptime, @gpu, @concurrent and @distributed": diag.EUnknownDecorator,

	"%s cannot be imported from a C header: it %s":                               diag.EHeaderName,
	"%s declares more than a program wants; name what is imported from a header": diag.EHeaderName,

	// Format values. A string literal with a #{} borrows what it names, so
	// it is only ever passed on.
	"a format value can only be passed as an argument, because it cannot outlive the statement it is written in":   diag.EFormatEscapes,
	"a Format cannot be a field, element or result type, because it cannot outlive the statement it is written in": diag.EFormatEscapes,
	"a closure cannot capture a format value, because it cannot outlive the statement it is written in":            diag.EFormatEscapes,
	"string interpolation builds a Format, which mem declares; this program does not load mem":                     diag.EFormatEscapes,

	// A closure's environment is a record in the frame that wrote it.
	"this returns a closure that borrows this frame": diag.EEscapingLocalView,
	"%s takes a function value; %s is not one":       diag.EKeepNotFunction,

	// A view stored into something, and values in an arena.
	"this stores a closure that borrows this frame into %s, which outlives it":                         diag.EStoredFrameView,
	"this stores a view into storage that ends with this function into %s, which outlives it":          diag.EStoredFrameView,
	"this stores a closure written in a loop body into %s, which is declared outside the loop":         diag.ELoopSharedStore,
	"this stores a view of storage written in a loop body into %s, which is declared outside the loop": diag.ELoopSharedStore,
	"this stores a value allocated from %s into %s, which outlives it":                                 diag.EArenaEscape,
	"this returns a value allocated from %s, which ends with this function":                            diag.EArenaEscape,
	"%s was allocated from %s, which has been %s":                                                      diag.EArenaUseAfterEnd,

	// Unsafe code. Calls to unsafe functions and reads of union members
	// are the unchecked operations; a plain fn may do neither.
	"%s is unsafe to call; this function is a plain fn":                      diag.EUnsafeOperation,
	"reading the union member %s is unsafe; this function is a plain fn":     diag.EUnsafeOperation,
	"&%s makes a pointer to an unsafe function; this function is a plain fn": diag.EUnsafeOperation,
	"import safe vouches for a C file's functions; %q is a Gyro module":      diag.EVouchGyro,

	// A name that resolves to the wrong kind of thing
	"%s is a type, not a value":                                                             diag.EWrongKind,
	"%s is a type, not a function":                                                          diag.EWrongKind,
	"%s is a value, not a type":                                                             diag.EWrongKind,
	"%s is a module, not a value":                                                           diag.EWrongKind,
	"%s is a module, not a function":                                                        diag.EWrongKind,
	"%s is a module, not a type":                                                            diag.EWrongKind,
	"%s is a type parameter, not a value":                                                   diag.EWrongKind,
	"%s is a type parameter, not a function":                                                diag.EWrongKind,
	"%s is a type parameter; it has no members":                                             diag.EWrongKind,
	"%s is a type parameter and takes no type arguments":                                    diag.EWrongKind,
	"%s is a method; call it: .%s(...)":                                                     diag.EWrongKind,
	"%s is an associated function; call it as %s::%s(...)":                                  diag.EWrongKind,
	"%s takes a receiver; call it on a value":                                               diag.EWrongKind,
	"%s takes a receiver; call it on a value: value.%s(...)":                                diag.EWrongKind,
	"%s is a type; a literal is written %s{ ... } and an associated function %s::name(...)": diag.EWrongKind,
	"%s is a value; :: reaches into modules and types":                                      diag.EWrongKind,
	"%s::%s is a value; :: reaches into modules and types":                                  diag.EWrongKind,
	"%s::%s is a value, not a type":                                                         diag.EWrongKind,
	"%s::%s cannot be reached into further":                                                 diag.EWrongKind,
	"%s is not a value; it cannot be assigned":                                              diag.EWrongKind,
	"%s is not a struct; only structs have literals":                                        diag.EWrongKind,
	"%s is generic; it must be called so its type parameters can be inferred":               diag.EWrongKind,

	// Members
	"%s has no field or method %s":                 diag.ENoSuchMember,
	"%s has no field %s":                           diag.ENoSuchMember,
	"%s has no method %s":                          diag.ENoSuchMember,
	"%s has no variant %s":                         diag.ENoSuchMember,
	"%s has no associated function %s":             diag.ENoSuchMember,
	"%s has no element %d":                         diag.ENoSuchMember,
	"%s has no members; only structs and enums do": diag.ENoSuchMember,
	"%s has no member %s":                          diag.ENoSuchMember,
	"%s has no message(): string method, so an error can never hold one":                                                    diag.ENoSuchMember,
	"a literal of %s is not allowed here: the member %s is private to its module; construct it with an associated function": diag.EPrivateField,
	"%s.%s is private to its module": diag.EPrivateField,
	"a literal of %s is not allowed here: the field %s is private to its module; construct it with an associated function": diag.EPrivateField,

	// Modules
	"%q does not export %s":                               diag.ENotExported,
	"%s does not export %s":                               diag.ENotExported,
	"cannot export %s: it is not declared in this module": diag.EExportUndeclared,

	// Types
	"cannot use %s as %s in %s":                                   diag.EMismatchedTypes,
	"cannot use %s as %s in assignment":                           diag.EMismatchedTypes,
	"a float literal cannot become %s":                            diag.EMismatchedTypes,
	"the array literal mixes %s and %s":                           diag.EMismatchedTypes,
	"this function returns nothing; the value has no place to go": diag.EMismatchedTypes,
	"this function returns %s; the return gives nothing":          diag.EMismatchedTypes,
	"this function returns %s; the return gives %d values":        diag.EMismatchedTypes,
	"the match arms disagree: %s here, %s before":                 diag.EMatchArmsDisagree,
	"the literal %s does not fit %s":                              diag.ELiteralOutOfRange,
	"division by zero in a constant expression":                   diag.EDivisionByZero,

	// Operators
	"operator %s needs %s, got %s and %s":                                            diag.EBadOperands,
	"operator %s needs matching numeric operands, got %s and %s":                     diag.EBadOperands,
	"operator %s needs integers, got float literals":                                 diag.EBadOperands,
	"operator %s shifts integers, not %s":                                            diag.EBadOperands,
	"operator %s orders numeric types and rune; ordering %s is spelled a.compare(b)": diag.EBadOperands,
	"operator ! needs a bool, not %s":                                                diag.EBadOperands,
	"operator - negates numeric values, not %s":                                      diag.EBadOperands,
	"operator ~ inverts integers, not %s":                                            diag.EBadOperands,
	"a shift count is an integer, not %s":                                            diag.EBadOperands,
	"strings do not concatenate with +; interpolate instead: \"#{a}#{b}\"":           diag.EBadOperands,
	"cannot compare %s with %s":                                                      diag.ENotComparable,
	"slices do not compare with ==; compare the contents explicitly":                 diag.ENotComparable,

	// Calls
	"%s takes %d argument(s), got %d":                               diag.EWrongArgCount,
	"%s takes %d to %d arguments, got %d":                           diag.EWrongArgCount,
	"%s takes at least %d argument(s), got %d":                      diag.EWrongArgCount,
	"arguments are positional; only decorator arguments take names": diag.EWrongArgCount,
	"%s::%s carries %d value(s), got %d":                            diag.EWrongArgCount,
	"%s::%s carries a payload; construct it with its values":        diag.EWrongArgCount,
	"%s::%s carries no payload; write it without parentheses":       diag.EWrongArgCount,
	"%s is not callable":                                            diag.ENotCallable,
	"%s is %s, which is not callable":                               diag.ENotCallable,

	// Variadics
	"only the last parameter may be variadic":                                            diag.EVariadicParam,
	"a variadic parameter takes no default; it is empty when nothing is passed":          diag.EVariadicParam,
	"%s has no variadic parameter to spread into":                                        diag.EBadSpread,
	"a spread argument is the whole pack, so it must be the only one %s receives for it": diag.EBadSpread,

	// Generics
	"%s takes %d type argument(s), got %d":                                  diag.ETypeArgs,
	"%s takes no type arguments":                                            diag.ETypeArgs,
	"%s is not generic; it takes no type arguments":                         diag.ETypeArgs,
	"%s has %d type parameters, the method names %d":                        diag.ETypeArgs,
	"cannot infer %s for %s from these arguments; write %s<...> explicitly": diag.ETypeArgs,
	"cannot infer %s for %s; write %s<...> explicitly":                      diag.ETypeArgs,
	"in the instantiation %s required here":                                 diag.ETypeArgs,
	"generic instantiation of %s is too deep; the type keeps growing":       diag.EInstantiationDepth,

	// Composites
	"%s cannot be indexed":                                                                              diag.ENotIndexable,
	"cannot dereference %s; it is not a pointer":                                                        diag.ENotIndexable,
	"positional access needs a tuple, not %s":                                                           diag.ENotIndexable,
	"%s has %d elements, the assignment has %d targets":                                                 diag.ETupleArity,
	"%s has %d elements, the declaration binds %d names":                                                diag.ETupleArity,
	"destructuring %d names needs a tuple, not %s":                                                      diag.ETupleArity,
	"destructuring %d targets needs a tuple, not %s":                                                    diag.ETupleArity,
	"%s has %d fields, the literal provides %d; a literal sets every field":                             diag.EStructLiteral,
	"the literal misses the field %s; there are no zero values, so a literal sets every field":          diag.EStructLiteral,
	"the field %s is set twice":                                                                         diag.EStructLiteral,
	"a union literal sets exactly one member, not %d":                                                   diag.EStructLiteral,
	"a union literal names the member it sets; position would not say which":                            diag.EStructLiteral,
	"as converts between numeric types and rune; %s as %s does not":                                     diag.EInvalidConversion,
	"an any cannot hold %s":                                                                             diag.EInvalidConversion,
	"an any cannot hold %s, so it cannot be unwrapped to one":                                           diag.EInvalidConversion,
	"indexes are usize, not %s; lengths and indexes never mix with signed arithmetic implicitly":        diag.EIndexNotUsize,
	"range bounds are usize, not %s; lengths and indexes never mix with signed arithmetic implicitly":   diag.EIndexNotUsize,
	"a string cannot be narrowed":                                                                       diag.ENotIndexable,
	"%s cannot be narrowed; a slice, a fixed-size array and an Array<T> are what have a window to take": diag.ENotIndexable,

	// The address of a declared function
	"%s is async, and what runs it is this program's scheduler rather than a caller": diag.ENoAddress,
	"%s is variadic, and a code pointer carries no argument pack":                    diag.ENoAddress,
	"a narrowing has no address; it is a view built here, not storage":               diag.ENoAddress,

	// Nullability
	"this value is %s and may be null; check it against null before using it":      diag.ENullableUnchecked,
	"this value is %s and may be null; check it against null before dereferencing": diag.ENullableUnchecked,
	"this value is %s and may be null; check it against null before indexing":      diag.ENullableUnchecked,
	"this value is %s and may be null; check it against null before narrowing it":  diag.ENullableUnchecked,
	"this value is %s and may be null; check it against null before iterating":     diag.ENullableUnchecked,
	"%s can never be null": diag.ENullableUnchecked,

	// Patterns
	"a %s pattern can never match %s":                                                       diag.EPatternNeverMatches,
	"this pattern is %s; it can never match %s":                                             diag.EPatternNeverMatches,
	"a pattern names an enum variant or a struct type test; this is neither":                diag.EPatternNeverMatches,
	"a type test binds the whole value: %s(name)":                                           diag.EPatternNeverMatches,
	"a type test needs an error subject whose concrete type is unknown; this subject is %s": diag.EPatternNeverMatches,
	"an any never holds %s, so this pattern can never match":                                diag.EPatternNeverMatches,
	"a type test on %s needs an any subject":                                                diag.EPatternNeverMatches,
	"%s::%s carries %d value(s), the pattern binds %d":                                      diag.EPatternNeverMatches,
	"this match leaves %s uncovered":                                                        diag.EMatchNotExhaustive,
	"%s is read before it is assigned":                                                      diag.EUnassignedRead,
	"%s holds a value only where %s is true":                                                diag.EUnassignedRead,

	// Borrows
	"&%s names no parameter of this function":                       diag.EBadBorrowSource,
	"&%s borrows from %s, which is not a view":                      diag.EBadBorrowSource,
	"this row borrows, but the result is not a view":                diag.EBadBorrowSource,
	"this returns a view into %s, which the row does not name":      diag.EUnnamedBorrow,
	"this returns a view into storage that ends with this function": diag.EEscapingLocalView,

	// Effects
	"%s does %s, which the row does not name":         diag.EUnnamedEffect,
	"%s is an intrinsic with no row, so it may do %s": diag.EUnnamedEffect,
	"%s is not an effect":                             diag.EUnknownEffect,

	// Statements
	"a condition is a bool, not %s":                                                        diag.EConditionNotBool,
	"cannot assign to this expression":                                                     diag.ENotAssignable,
	"cannot assign to a narrowing; assign its elements, or the slice variable":             diag.ENotAssignable,
	"_ discards a value; there is nothing to update":                                       diag.ENotAssignable,
	"a compound assignment takes one target":                                               diag.EAssignmentForm,
	"a declaration needs a type or a value":                                                diag.EBindingNeedsType,
	"a const needs its value here":                                                         diag.EBindingNeedsType,
	"the error result is dropped here; discarding one must be written out: var _, _ = ...": diag.EDroppedError,
	"the parameter %s needs a type; nothing here says what the closure is":                 diag.EClosureParams,
	"this closure takes %d parameter(s), the context wants %s":                             diag.EClosureParams,

	// Methods and receivers
	"self is only available inside a method":                          diag.ESelfOutsideMethod,
	"a receiver needs an owning type: write fn Type::%s(self, ...)":   diag.ESelfOutsideMethod,
	"%s already has a method %s":                                      diag.EMethodConflict,
	"cannot attach a method to %s: it is not a type":                  diag.EMethodConflict,
	"cannot attach a method to %s: no such type in this module":       diag.EMethodConflict,
	"%s takes no type parameters":                                     diag.ETypeArgs,
	"an extension of %s needs a receiver: write fn %s::%s(self, ...)": diag.ESelfOutsideMethod,

	// The entry point
	"main takes no type parameters":                          diag.EEntryPointSignature,
	"main returns nothing":                                   diag.EEntryPointSignature,
	"main takes no parameters, or one string[] of arguments": diag.EEntryPointSignature,

	// Concurrency
	"await needs a call to an async fn, not %s":                    diag.EAwaitMisuse,
	"await only works inside an async fn":                          diag.EAwaitMisuse,
	"this async call does nothing until it is awaited":             diag.EAwaitMisuse,
	"send needs a channel, not %s":                                 diag.EChannelMisuse,
	"receive needs a channel, not %s":                              diag.EChannelMisuse,
	"a channel capacity is an integer, not %s":                     diag.EChannelMisuse,
	"a receive binding needs no annotation; it is (element, open)": diag.EChannelMisuse,

	// Iteration
	"a range is not a value; `..` only appears in for..in":                                                      diag.ERangeMisuse,
	"a range needs matching integer endpoints, got %s and %s":                                                   diag.ERangeMisuse,
	"a string does not iterate directly; use .runes(a) for code points":                                         diag.EIterationMisuse,
	"%s is not iterable; for..in takes slices, arrays, ranges, channels, or a type with next(*self): (T, bool)": diag.EIterationMisuse,
}
