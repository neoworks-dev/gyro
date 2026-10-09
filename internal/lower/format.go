package lower

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/types"
)

// A string literal with a #{} in it is a Format: stdlib's struct holding
// one any per piece, the literal text and each value in a hole. Wrapping
// into an any is a variant constructor and the pieces sit in an array in
// this frame, so building one allocates nothing.
//
// Showing a piece goes through the printers %any already has, which are
// the ones println uses for the same value, so a to_string wins and a
// composite prints its derived form exactly as it does anywhere else.

func (l *lowerer) isFormat(t types.Type) bool {
	strct, ok := t.(*types.Struct)
	return ok && strct.Name == "Format" && strct.Module == l.stdlib.Path
}

func (f *funcLowerer) evalStringLit(e *ast.StringLit) string {
	format, ok := f.typeOf(e).(*types.Struct)
	if !ok || !f.l.isFormat(format) {
		f.l.fail("@%s: an interpolated string without a Format type", f.fn.Name)
	}
	pieces, ok := format.Fields[0].Type.(*types.Slice)
	if !ok {
		f.l.fail("@%s: Format's pieces are not a slice", f.fn.Name)
	}

	elems := make([]string, 0, len(e.Parts))
	for _, part := range e.Parts {
		switch {
		case part.Expr != nil:
			elems = append(elems, f.piece(part.Expr))
		case part.Text != "":
			elems = append(elems, f.wrapAny(f.text(part.Text), types.String))
		}
	}

	array := f.newReg("pieces")
	f.emit(&metals.MakeArray{Dst: array, Elem: f.l.metalsType(pieces.Elem), Args: elems})
	view := f.newReg("pieced")
	f.emit(&metals.Slice{Dst: view, X: array})

	named, ok := f.l.metalsStruct(format).(*metals.Named)
	if !ok {
		f.l.fail("@%s: cannot construct Format", f.fn.Name)
	}
	out := f.newReg("format")
	f.emit(&metals.MakeRecord{Dst: out, TypeName: named.Name, Args: []string{view}})
	return out
}

// piece wraps what a hole holds. An any is already one, and null shows as
// its word.
func (f *funcLowerer) piece(e ast.Expr) string {
	if isNullLiteral(e) {
		return f.wrapAny(f.text("null"), types.String)
	}
	t := f.typeOf(e)
	value := f.lowerExpr(e, nil)
	if isAnyType(t) {
		return value
	}
	return f.wrapAny(value, t)
}

// prepareShow lowers stdlib's show: an any rendered to text.
func (f *funcLowerer) prepareShow(call *ast.CallExpr) func() string {
	piece := f.lowerExpr(call.Args[0].Value, nil)
	return func() string {
		return f.displayReg(piece, types.Any)
	}
}

// prepareStream lowers print, println, eprint or eprintln of a Format to a
// call of stdlib's stream_ function for it, which holds the output lock
// and writes the pieces in order.
func (f *funcLowerer) prepareStream(printer *ast.FuncDecl, message string) func() string {
	callee := f.l.ensureFunc(f.l.stdlibDecl("stream_"+printer.Name), nil)
	return func() string {
		f.emit(&metals.Call{Callee: callee, Args: []string{message}})
		return ""
	}
}

// stdlibDecl finds a function stdlib declares, by name.
func (l *lowerer) stdlibDecl(name string) *ast.FuncDecl {
	for _, decl := range l.stdlib.Tree.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Owner == "" && fn.Name == name {
			return fn
		}
	}
	l.fail("stdlib declares no %s", name)
	return nil
}

// ensureAnyStream records that print or eprint of an any is called and
// returns the name of the printer generated for it.
func (l *lowerer) ensureAnyStream(printer string) string {
	name := printer + "<any>"
	l.ensureAnyPrinter(name)
	return name
}

// buildAnyStream generates print<any> or eprint<any>: a switch over the
// tag that writes the wrapped value as that type. A leaf goes straight to
// the runtime's printer for its type, so nothing is built to show it; a
// to_string or a composite is rendered to text first, as println would.
func (l *lowerer) buildAnyStream(name, printer string) {
	b := &reprBuilder{l: l, fn: &metals.Func{
		Name:   name,
		Params: []metals.Param{{Name: "v", Type: &metals.Named{Name: "any"}}},
	}}
	b.blocks = []*[]metals.Instr{&b.fn.Body}

	tag := b.reg("tag")
	b.emit(&metals.VariantTag{Dst: tag, X: "v"})

	cases := make([]metals.SwitchCase, 0, len(l.anyTypes))
	for index, t := range l.anyTypes {
		body := b.nested(func() {
			got := b.reg("p")
			b.emit(&metals.VariantField{Dst: got, X: "v", Variant: index, Index: 0})
			b.streamPiece(printer, got, t)
			b.emit(&metals.Return{})
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

func (b *reprBuilder) streamPiece(printer, reg string, t types.Type) {
	call := func(of types.Type, arg string) {
		callee := printer + "<" + of.String() + ">"
		b.l.ensureImport(callee, &metals.FnType{Params: []metals.Type{b.l.metalsType(of)}})
		b.emit(&metals.Call{Callee: callee, Args: []string{arg}})
	}
	switch {
	case isStringType(t):
		call(types.String, reg)
	case toStringMethodOf(t) != nil || b.l.reprFor(t) != "":
		call(types.String, b.part(reg, t))
	default:
		if _, leaf := b.l.metalsType(t).(*metals.Prim); !leaf {
			// The runtime has no printer for it: a channel, a function
			// value, a handle. println of one is refused when it is
			// written; here it can only be reached through an any.
			b.emit(&metals.Abort{Message: "no printer for " + t.String()})
			return
		}
		call(t, reg)
	}
}
