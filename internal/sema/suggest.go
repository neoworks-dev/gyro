package sema

// Diagnostics are the compiler's only user interface for a program that does
// not compile, so an error says what is wrong and the notes under it say what
// the author probably meant: a misspelling, another language's spelling of a
// primitive, or the import that would bind the name.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/types"
)

// maxNotes caps the hints on one diagnostic; past a few a list of guesses
// stops helping and starts hiding the error.
const maxNotes = 3

// builtinAliases maps primitive spellings from other languages to Gyro's.
var builtinAliases = map[string]string{
	"int": "i32", "integer": "i32", "long": "i64", "short": "i16",
	"float": "f64", "double": "f64", "boolean": "bool",
	"str": "string", "char": "rune", "byte": "u8",
}

// errorHintf reports an error with help lines under it.
func (c *checker) errorHintf(offset int, help []string, format string, args ...any) {
	c.bag.Add(diag.Diagnostic{
		Severity: diag.SeverityError,
		Code:     codes[format],
		Pos:      c.file.Position(offset),
		Message:  fmt.Sprintf(format, args...),
		Help:     help,
	})
}

// errorLabelf reports an error with a label printed beside the underline,
// for the messages where the expected type is worth repeating at the span.
func (c *checker) errorLabelf(offset int, label string, format string, args ...any) {
	c.bag.Add(diag.Diagnostic{
		Severity: diag.SeverityError,
		Code:     codes[format],
		Pos:      c.file.Position(offset),
		Message:  fmt.Sprintf(format, args...),
		Label:    label,
	})
}

// reportUndeclared reports a name that resolved to nothing. kind is "name"
// or "type". This is the most common error in a language without a universe
// scope, and the bare message tells the author nothing they do not already
// see, so the notes carry the diagnosis.
func (c *checker) reportUndeclared(offset int, kind, name string) {
	wantType := kind == "type"
	d := diag.Diagnostic{
		Severity: diag.SeverityError,
		Pos:      c.file.Position(offset),
		Label:    "not found in this scope",
		Help:     c.undeclaredNotes(name, wantType),
	}

	d.Code, d.Message = diag.EUndeclaredName, fmt.Sprintf("undeclared name %s", name)
	if wantType {
		d.Code, d.Message = diag.EUndeclaredType, fmt.Sprintf("undeclared type %s", name)
	}
	if len(d.Help) == 0 {
		d.Notes = []string{"nothing is predeclared: a name is either declared in this file or imported"}
	}
	c.bag.Add(d)
}

func (c *checker) undeclaredNotes(name string, wantType bool) []string {
	var notes []string
	if builtin := builtinSpelling(name); builtin != "" {
		notes = append(notes, fmt.Sprintf("the built-in type is spelled %s", builtin))
	}
	notes = append(notes, c.importNotes(name)...)
	if !wantType {
		notes = append(notes, builtinMethodNotes(name)...)
	}
	notes = append(notes, didYouMean(name, c.namesInScope(wantType))...)

	if len(notes) > maxNotes {
		return notes[:maxNotes]
	}
	return notes
}

// builtinSpelling returns the primitive an author reaching for `String` or
// `int` meant, or "" when the name is nothing like one.
func builtinSpelling(name string) string {
	lowered := strings.ToLower(name)
	if lowered == name {
		if alias, ok := builtinAliases[lowered]; ok {
			return alias
		}
		return ""
	}
	if types.Builtin[lowered] != nil {
		return lowered
	}
	return builtinAliases[lowered]
}

// importNotes suggests the import that would bind name: adding it to the
// braces of an import this file already has, or importing a bundled module.
func (c *checker) importNotes(name string) []string {
	var notes []string
	specifiers := make([]string, 0, len(c.imports))
	for specifier := range c.imports {
		specifiers = append(specifiers, specifier)
	}
	sort.Strings(specifiers)

	imported := map[string]bool{}
	for _, specifier := range specifiers {
		imported[specifier] = true
		exports := c.imports[specifier]
		if exports == nil || exports.names[name] == nil {
			continue
		}
		notes = append(notes, fmt.Sprintf("%q exports %s; add it to the import you already have from %q",
			specifier, name, specifier))
	}

	for _, specifier := range stdlib.Lookup().Exports[name] {
		if imported[specifier] {
			continue
		}
		notes = append(notes, fmt.Sprintf("%q exports %s; add: import { %s } from %q",
			specifier, name, name, specifier))
	}
	return notes
}

// builtinMethodNotes catches reaching for a free function that is a method on
// a built-in type, the habit `len(xs)` instead of `xs.len()`.
func builtinMethodNotes(name string) []string {
	owners := stdlib.Lookup().Methods[name]
	if len(owners) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%s is a method on %s, not a free function; call it as value.%s(...)",
		name, strings.Join(owners, " and "), name)}
}

// namesInScope lists every visible name, restricted to types when the
// failure was in type position.
func (c *checker) namesInScope(wantType bool) []string {
	var names []string
	for scope := c.scope; scope != nil; scope = scope.parent {
		for name, sym := range scope.names {
			if wantType && sym.kind != symType && sym.kind != symTypeParam {
				continue
			}
			names = append(names, name)
		}
	}
	return names
}

// didYouMean is the note for the candidate nearest to name, or nothing when
// none is near enough to be a guess rather than a distraction.
func didYouMean(name string, candidates []string) []string {
	match := closest(name, candidates)
	if match == "" {
		return nil
	}
	return []string{fmt.Sprintf("did you mean %s?", match)}
}

// closest picks the candidate nearest to name. Ties break alphabetically so
// the same source always produces the same diagnostic.
func closest(name string, candidates []string) string {
	best := ""
	bestDistance := 0
	for _, candidate := range candidates {
		distance := editDistance(name, candidate)
		if !closeEnough(name, candidate, distance) {
			continue
		}
		if best == "" || distance < bestDistance || (distance == bestDistance && candidate < best) {
			best, bestDistance = candidate, distance
		}
	}
	return best
}

// closeEnough keeps a guess from firing on an unrelated name: one edit for
// short names, two once there is enough text to misspell, and never more
// than half the shorter name, so x is not a guess at z.
func closeEnough(name, candidate string, distance int) bool {
	if distance == 0 {
		return false
	}
	limit := 1
	if len(name) >= 5 && len(candidate) >= 5 {
		limit = 2
	}
	if distance > limit {
		return false
	}

	shorter := len(name)
	if len(candidate) < shorter {
		shorter = len(candidate)
	}
	return distance*2 <= shorter
}

// editDistance is the Levenshtein distance between a and b, over runes so a
// non-ASCII identifier is not measured by its bytes.
func editDistance(a, b string) int {
	from, to := []rune(a), []rune(b)
	previous := make([]int, len(to)+1)
	current := make([]int, len(to)+1)
	for j := range previous {
		previous[j] = j
	}

	for i := 1; i <= len(from); i++ {
		current[0] = i
		for j := 1; j <= len(to); j++ {
			substitution := previous[j-1]
			if from[i-1] != to[j-1] {
				substitution++
			}
			current[j] = minimum(previous[j]+1, current[j-1]+1, substitution)
		}
		previous, current = current, previous
	}
	return previous[len(to)]
}

func minimum(a, b, c int) int {
	best := a
	if b < best {
		best = b
	}
	if c < best {
		best = c
	}
	return best
}

// ---------------------------------------------------------------------------
// Conversions
// ---------------------------------------------------------------------------

// operandHelp says how to reach operands the operator will take. `as` is the
// only conversion in the language, so the help names the direction that is
// safe: a widening cannot fail, and its opposite panics on a value that does
// not fit.
func operandHelp(integerOnly bool, left, right types.Type) []string {
	if help, ok := unconvertibleHelp(integerOnly, left, right); ok {
		return help
	}
	return conversionHelp(left, right)
}

// compoundHelp is the same for `target op= value`, where only the value can
// move: the target's type is fixed by the declaration it came from.
func compoundHelp(integerOnly bool, target, value types.Type) []string {
	if help, ok := unconvertibleHelp(integerOnly, target, value); ok {
		return help
	}

	convert := fmt.Sprintf("the target is %s, so convert the value: `x as %s`", target, target)
	if widensTo(value, target) {
		return []string{convert + " widens and always succeeds"}
	}
	if isRune(target) || isRune(value) {
		return []string{convert}
	}
	return []string{convert, fmt.Sprintf("that is not a widening, so a value outside %s panics", target)}
}

// unconvertibleHelp covers the mismatches a conversion does not answer: one
// side is outside what `as` takes at all, or the two already agree and the
// operator is refusing the type itself.
func unconvertibleHelp(integerOnly bool, left, right types.Type) ([]string, bool) {
	if !convertsWithAs(left) || !convertsWithAs(right) {
		return []string{"`as` converts between the numeric types and rune; nothing else converts, implicitly or otherwise"}, true
	}
	if !types.Identical(left, right) {
		return nil, false
	}
	if integerOnly {
		return []string{"this operator takes integers; convert first, as in `x as i64`"}, true
	}
	return []string{fmt.Sprintf("%s does no arithmetic; convert first, as in `x as i32`", left)}, true
}

func conversionHelp(left, right types.Type) []string {
	if isRune(left) || isRune(right) {
		// Whether a code point outside the Unicode range survives the trip
		// through an integer is not settled, so no direction is promised.
		return []string{fmt.Sprintf("convert one side with `as`: `x as %s` or `x as %s`", left, right)}
	}

	leftWidens, rightWidens := widensTo(left, right), widensTo(right, left)
	switch {
	case leftWidens && rightWidens:
		// One range under two names, u64 and usize: either way is safe.
		return []string{fmt.Sprintf("convert either side with `as`: `x as %s` and `x as %s` both always succeed",
			left, right)}
	case leftWidens:
		return widenHelp(left, right)
	case rightWidens:
		return widenHelp(right, left)
	}
	return []string{
		fmt.Sprintf("convert one side with `as`: `x as %s` or `x as %s`", left, right),
		"neither direction is a widening, so a value outside the target's range panics",
	}
}

func widenHelp(narrow, wide types.Type) []string {
	return []string{
		fmt.Sprintf("convert the %s side: `x as %s` widens and always succeeds", narrow, wide),
		fmt.Sprintf("the other direction, `x as %s`, panics when the value does not fit", narrow),
	}
}

// convertsWithAs reports whether `as` accepts the type at all.
func convertsWithAs(t types.Type) bool {
	return types.IsNumeric(t) || isRune(t)
}

func isRune(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind == types.RuneKind
}

// widensTo reports whether every value of from is also a value of to, which
// is exactly when `as` cannot panic. Integer to float counts only while the
// float's mantissa holds the integer exactly; a lossy conversion does not
// panic, but it is not a widening either.
func widensTo(from, to types.Type) bool {
	fromBits, fromSigned, fromFloat := numericShape(from)
	toBits, toSigned, toFloat := numericShape(to)
	if fromBits == 0 || toBits == 0 {
		return false
	}
	if fromBits == toBits && fromSigned == toSigned && fromFloat == toFloat {
		// Two names for one range, u64 and usize.
		return true
	}

	if toFloat {
		if fromFloat {
			return fromBits <= toBits
		}
		return fromBits <= mantissaBits(toBits)
	}
	if fromFloat {
		return false
	}
	if fromSigned == toSigned {
		return fromBits <= toBits
	}
	// An unsigned value fits a signed type only with a bit to spare for the
	// sign; the reverse never fits, since negatives have nowhere to go.
	return !fromSigned && toSigned && fromBits < toBits
}

// numericShape describes a numeric primitive as width, signedness and
// whether it is a float. Width 0 means the type is not one; rune is left
// out, since its range is narrower than its width.
func numericShape(t types.Type) (bits int, signed, isFloat bool) {
	basic, ok := t.(*types.Basic)
	if !ok {
		return 0, false, false
	}
	switch basic.Kind {
	case types.I8:
		return 8, true, false
	case types.I16:
		return 16, true, false
	case types.I32:
		return 32, true, false
	case types.I64:
		return 64, true, false
	case types.U8:
		return 8, false, false
	case types.U16:
		return 16, false, false
	case types.U32:
		return 32, false, false
	case types.U64, types.Usize:
		return 64, false, false
	case types.F32:
		return 32, false, true
	case types.F64:
		return 64, false, true
	}
	return 0, false, false
}

// mantissaBits is how wide an integer a float represents exactly.
func mantissaBits(floatBits int) int {
	if floatBits == 32 {
		return 24
	}
	return 53
}

// ---------------------------------------------------------------------------
// Candidate lists for member lookups
// ---------------------------------------------------------------------------

// memberNames lists what may follow a dot on t: its fields and the methods
// that take a receiver.
func memberNames(t types.Type) []string {
	switch typ := t.(type) {
	case *types.Struct:
		names := make([]string, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			names = append(names, field.Name)
		}
		return append(names, methodNames(structOrigin(typ).Methods, true)...)
	case *types.Enum:
		return methodNames(enumOrigin(typ).Methods, true)
	}
	return nil
}

// associatedNames lists what may follow `::` on a struct: its associated
// functions, the ones declared without a receiver.
func associatedNames(strct *types.Struct) []string {
	return methodNames(strct.Methods, false)
}

// variantNames lists an enum's variants and its associated functions, both
// of which are written Enum::name.
func variantNames(enum *types.Enum) []string {
	names := make([]string, 0, len(enum.Variants))
	for _, variant := range enum.Variants {
		names = append(names, variant.Name)
	}
	return append(names, methodNames(enum.Methods, false)...)
}

func methodNames(methods map[string]*types.Method, withReceiver bool) []string {
	var names []string
	for name, method := range methods {
		if (method.Recv != types.NoRecv) != withReceiver {
			continue
		}
		names = append(names, name)
	}
	return names
}

func fieldNames(strct *types.Struct) []string {
	names := make([]string, 0, len(strct.Fields))
	for _, field := range strct.Fields {
		names = append(names, field.Name)
	}
	return names
}

func exportedNames(exports *Exports) []string {
	if exports == nil {
		return nil
	}
	names := make([]string, 0, len(exports.names))
	for name := range exports.names {
		names = append(names, name)
	}
	return names
}

// A generic instance shares its origin's method set, so member suggestions
// come from the declaration rather than the instance.
func structOrigin(strct *types.Struct) *types.Struct {
	if strct.Origin != nil {
		return strct.Origin
	}
	return strct
}

func enumOrigin(enum *types.Enum) *types.Enum {
	if enum.Origin != nil {
		return enum.Origin
	}
	return enum
}
