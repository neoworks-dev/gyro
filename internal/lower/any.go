package lower

import (
	"strconv"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/types"
)

// An `any` is a closed sum. Gyro compiles whole programs, so the set of
// types the source ever wraps is known by the time lowering finishes, and
// the value needs no runtime type tag beyond the one an enum already has:
// `%any` is generated with one variant per wrapped type, wrapping is
// `variant`, testing is `variant.tag` and unwrapping is `variant.field`.
//
// The variants are collected as they are met rather than up front, so the
// declaration and the two synthesized printers are emitted after every
// function has been lowered — see finishAnyType.

// anyVariantOf returns the %any variant index for a wrapped type,
// assigning one the first time that type is wrapped.
func (l *lowerer) anyVariantOf(t types.Type) int {
	l.usesAny = true
	key := t.String()
	if index, ok := l.anyVariant[key]; ok {
		return index
	}
	index := len(l.anyTypes)
	l.anyVariant[key] = index
	l.anyTypes = append(l.anyTypes, t)
	return index
}

// settleAnyTypes lowers what the any printers will call, while there is
// still a queue to lower it from. A printer shows a variant through its
// to_string or its derived repr, and either may reach functions nothing
// else called; those bodies may wrap further types, so this runs until the
// set of wrapped types stops growing.
func (l *lowerer) settleAnyTypes() {
	if len(l.anyPrinters) == 0 {
		return
	}
	for seen := 0; ; {
		l.drain()
		if seen == len(l.anyTypes) {
			return
		}
		for ; seen < len(l.anyTypes); seen++ {
			t := l.anyTypes[seen]
			if isStringType(t) {
				continue
			}
			if method := toStringMethodOf(t); method != nil {
				l.ensureFunc(method.Decl, ownerBindings(method.Decl, t))
				continue
			}
			l.reprFor(t)
		}
	}
}

// finishAnyType emits the %any declaration and its printers once every
// wrap in the program has been seen.
func (l *lowerer) finishAnyType() {
	if !l.usesAny || l.declared["any"] != nil {
		return
	}
	decl := &metals.TypeDecl{Name: "any", Enum: &metals.EnumShape{}}
	l.declared["any"] = decl
	l.module.Types = append(l.module.Types, decl)

	for index, t := range l.anyTypes {
		decl.Enum.Variants = append(decl.Enum.Variants, metals.VariantDecl{
			Name:    anyVariantName(t, index),
			Payload: []metals.Type{l.metalsType(t)},
		})
	}
	if l.anyPrinters[anyReprName] {
		l.buildAnyRepr(anyReprName, false)
	}
	if l.anyPrinters[anyDisplayName] {
		l.buildAnyRepr(anyDisplayName, true)
	}
	for _, printer := range []string{"print", "eprint"} {
		if name := printer + "<any>"; l.anyPrinters[name] {
			l.buildAnyStream(name, printer)
		}
	}
}

// ensureAnyPrinter records that one of the two %any printers is called, so
// that finishAnyType generates it. Nothing prints an any in most programs.
func (l *lowerer) ensureAnyPrinter(name string) {
	l.usesAny = true
	if l.anyPrinters == nil {
		l.anyPrinters = map[string]bool{}
	}
	l.anyPrinters[name] = true
}

// anyVariantName renders a type as a METALS variant name, which has to be
// one word. The index keeps two types that sanitize alike apart.
func anyVariantName(t types.Type, index int) string {
	var b strings.Builder
	for _, r := range t.String() {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String() + "_" + strconv.Itoa(index)
}

const (
	// anyReprName quotes a wrapped string, the way a value nested in a
	// composite prints; anyDisplayName leaves it raw, the way println and
	// #{} show a value at the top level.
	anyReprName    = "repr<any>"
	anyDisplayName = "display<any>"
)

// buildAnyRepr generates the printer for %any: a switch over the tag that
// unwraps each variant and prints it as that type.
func (l *lowerer) buildAnyRepr(name string, raw bool) {
	b := &reprBuilder{l: l, fn: &metals.Func{
		Name:   name,
		Params: []metals.Param{{Name: "v", Type: &metals.Named{Name: "any"}}},
		Result: metals.TStr,
	}}
	b.blocks = []*[]metals.Instr{&b.fn.Body}

	tag := b.reg("tag")
	b.emit(&metals.VariantTag{Dst: tag, X: "v"})

	cases := make([]metals.SwitchCase, 0, len(l.anyTypes))
	for index, t := range l.anyTypes {
		body := b.nested(func() {
			got := b.reg("p")
			b.emit(&metals.VariantField{Dst: got, X: "v", Variant: index, Index: 0})
			if raw && isStringType(t) {
				b.emit(&metals.Return{Val: got})
				return
			}
			b.emit(&metals.Return{Val: b.part(got, t)})
		})
		cases = append(cases, metals.SwitchCase{Value: index, Body: body})
	}

	b.emit(&metals.Switch{
		X:       tag,
		Cases:   cases,
		Default: b.nested(func() { b.emit(&metals.Abort{Message: "unknown variant"}) }),
	})
	l.module.Funcs = append(l.module.Funcs, b.fn)
}

// ---------------------------------------------------------------------------
// Wrapping, testing and unwrapping at a use site
// ---------------------------------------------------------------------------

// wrapAny lowers `x as any` and the implicit wrap a pack of any performs
// on each of its arguments.
func (f *funcLowerer) wrapAny(value string, t types.Type) string {
	index := f.l.anyVariantOf(t)
	out := f.newReg("any")
	f.emit(&metals.MakeVariant{Dst: out, TypeName: "any", Variant: index, Args: []string{value}})
	return out
}

// anyHolds emits the tag test behind a type test and an unwrapping `as`.
func (f *funcLowerer) anyHolds(value string, t types.Type) string {
	index := f.l.anyVariantOf(t)
	tag := f.newReg("tag")
	f.emit(&metals.VariantTag{Dst: tag, X: value})
	want := f.newReg("want")
	f.emit(&metals.Const{Dst: want, Type: metals.TInt32, Value: strconv.Itoa(index)})
	out := f.newReg("holds")
	f.emit(&metals.Bin{Dst: out, Op: "eq", A: tag, B: want})
	return out
}

// anyPayloadOf reads the wrapped value, having established the tag.
func (f *funcLowerer) anyPayloadOf(value string, t types.Type) string {
	index := f.l.anyVariantOf(t)
	out := f.newReg("held")
	f.emit(&metals.VariantField{Dst: out, X: value, Variant: index, Index: 0})
	return out
}

// unwrapAny lowers `v as T` on an any: the tag has to agree, and a
// disagreement aborts the way a narrowing conversion that does not fit
// does.
func (f *funcLowerer) unwrapAny(value string, t types.Type) string {
	holds := f.anyHolds(value, t)
	missed := f.newReg("missed")
	f.emit(&metals.Un{Dst: missed, Op: "not", X: holds})
	f.emit(&metals.If{
		Cond: missed,
		Then: f.nested(func() {
			f.emit(&metals.Abort{Message: "any does not hold " + t.String()})
		}),
	})
	return f.anyPayloadOf(value, t)
}
