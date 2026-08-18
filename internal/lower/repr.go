package lower

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/types"
)

// Derived printing. A type with no to_string of its own still prints, and
// what it prints is built here rather than in each backend: one METALS
// function per composite type, which every tier then reaches as an
// ordinary call.
//
// The runtime keeps the primitives — an integer's digits and a float's
// shortest round trip are not worth open-coding — so a synthesized repr
// concatenates its parts and calls str<i32> for the leaves.
//
// The shape matches the interpreter, which is the specification:
//
//	Point{ x: 1, y: 2 }
//	Shape::Circle(2.5)
//	(1, "a")
//	[1, 2, 3]
//	&7
//
// A string is raw at the top level and quoted inside a composite, which
// is the one place display and repr disagree.

// reprFor returns the METALS function that renders t, or "" when t has no
// synthesized form and the runtime has to answer for it.
func (l *lowerer) reprFor(t types.Type) string {
	if !synthesizedRepr(t) {
		return ""
	}
	key := t.String()
	if name, ok := l.reprs[key]; ok {
		return name
	}

	name := "repr<" + key + ">"
	// Registered before the body is built: a recursive type reaches its
	// own repr while that repr is still being written.
	l.reprs[key] = name
	l.buildRepr(name, t)
	return name
}

// synthesizedRepr reports the types whose representation is built rather
// than asked of the runtime.
func synthesizedRepr(t types.Type) bool {
	switch t := t.(type) {
	case *types.Struct:
		return !isOpaqueHandle(t)
	case *types.Enum, *types.Tuple, *types.Slice, *types.Array, *types.Pointer:
		return true
	case *types.Optional:
		return true
	}
	return false
}

func (l *lowerer) buildRepr(name string, t types.Type) {
	b := &reprBuilder{l: l, fn: &metals.Func{
		Name:   name,
		Params: []metals.Param{{Name: "v", Type: l.metalsType(t)}},
		Result: metals.TStr,
	}}
	b.blocks = []*[]metals.Instr{&b.fn.Body}

	switch t := t.(type) {
	case *types.Struct:
		b.structBody(t)
	case *types.Enum:
		b.enumBody(t)
	case *types.Tuple:
		b.tupleBody(t)
	case *types.Slice:
		b.elemsBody(t.Elem)
	case *types.Array:
		b.elemsBody(t.Elem)
	case *types.Pointer:
		b.pointerBody(t)
	case *types.Optional:
		b.optionalBody(t)
	}

	l.module.Funcs = append(l.module.Funcs, b.fn)
}

// ---------------------------------------------------------------------------
// The builder
// ---------------------------------------------------------------------------

type reprBuilder struct {
	l      *lowerer
	fn     *metals.Func
	blocks []*[]metals.Instr
	regs   map[string]int
	labels int
}

func (b *reprBuilder) emit(instr metals.Instr) {
	top := b.blocks[len(b.blocks)-1]
	*top = append(*top, instr)
}

func (b *reprBuilder) nested(fill func()) []metals.Instr {
	var body []metals.Instr
	b.blocks = append(b.blocks, &body)
	fill()
	b.blocks = b.blocks[:len(b.blocks)-1]
	return body
}

func (b *reprBuilder) reg(hint string) string {
	if b.regs == nil {
		b.regs = map[string]int{}
	}
	n := b.regs[hint]
	b.regs[hint] = n + 1
	if n == 0 {
		return hint
	}
	return hint + "." + strconv.Itoa(n)
}

func (b *reprBuilder) label(hint string) string {
	b.labels++
	return hint + strconv.Itoa(b.labels)
}

// text materializes a literal string.
func (b *reprBuilder) text(value string) string {
	dst := b.reg("lit")
	b.emit(&metals.Const{Dst: dst, Type: metals.TStr, Value: strconv.Quote(value)})
	return dst
}

// concat joins the parts left to right, materializing literals as it goes.
// A part is either a register name or a literal, distinguished by the
// caller through concatPart.
func (b *reprBuilder) concat(parts ...concatPart) string {
	acc := ""
	for _, part := range parts {
		reg := part.reg
		if part.literal != "" || reg == "" {
			reg = b.text(part.literal)
		}
		if acc == "" {
			acc = reg
			continue
		}
		joined := b.reg("s")
		b.emit(&metals.StrConcat{Dst: joined, A: acc, B: reg})
		acc = joined
	}
	if acc == "" {
		acc = b.text("")
	}
	return acc
}

type concatPart struct {
	literal string
	reg     string
}

func lit(text string) concatPart { return concatPart{literal: text} }
func val(reg string) concatPart  { return concatPart{reg: reg} }

// part renders one nested value: a composite through its own repr, a
// struct with a to_string through that, everything else through the
// runtime — which quotes a string, since this is never the top level.
func (b *reprBuilder) part(reg string, t types.Type) string {
	if method := toStringMethodOf(t); method != nil {
		bindings := ownerBindings(method.Decl, t)
		callee := b.l.ensureFunc(method.Decl, bindings)
		dst := b.reg("shown")
		b.emit(&metals.Call{Dst: dst, Callee: callee, Args: []string{reg}})
		return dst
	}
	if callee := b.l.reprFor(t); callee != "" {
		dst := b.reg("shown")
		b.emit(&metals.Call{Dst: dst, Callee: callee, Args: []string{reg}})
		return dst
	}
	return b.runtimeStr(reg, t)
}

func (b *reprBuilder) runtimeStr(reg string, t types.Type) string {
	name := "str<" + t.String() + ">"
	b.l.ensureImport(name, &metals.FnType{
		Params: []metals.Type{b.l.metalsType(t)},
		Result: metals.TStr,
	})
	dst := b.reg("shown")
	b.emit(&metals.Call{Dst: dst, Callee: name, Args: []string{reg}})
	return dst
}

// ---------------------------------------------------------------------------
// One body per shape
// ---------------------------------------------------------------------------

func (b *reprBuilder) structBody(t *types.Struct) {
	parts := []concatPart{lit(t.Name + "{ ")}
	for index, field := range t.Fields {
		if index > 0 {
			parts = append(parts, lit(", "))
		}
		got := b.reg("f")
		b.emit(&metals.FieldGet{Dst: got, X: "v", Index: index})
		parts = append(parts, lit(field.Name+": "), val(b.part(got, field.Type)))
	}
	parts = append(parts, lit(" }"))
	b.emit(&metals.Return{Val: b.concat(parts...)})
}

// enumBody switches on the tag. Each variant is its own arm because the
// payload types differ, so there is nothing to share between them.
func (b *reprBuilder) enumBody(t *types.Enum) {
	tag := b.reg("tag")
	b.emit(&metals.VariantTag{Dst: tag, X: "v"})

	prefix := t.Name + "::"
	cases := make([]metals.SwitchCase, 0, len(t.Variants))
	for index, variant := range t.Variants {
		body := b.nested(func() {
			b.emit(&metals.Return{Val: b.variantText(prefix, index, variant)})
		})
		cases = append(cases, metals.SwitchCase{Value: index, Body: body})
	}

	b.emit(&metals.Switch{
		X:       tag,
		Cases:   cases,
		Default: b.nested(func() { b.emit(&metals.Abort{Message: "unknown variant"}) }),
	})
}

func (b *reprBuilder) variantText(prefix string, index int, variant types.Variant) string {
	if len(variant.Payload) == 0 {
		return b.text(prefix + variant.Name)
	}
	parts := []concatPart{lit(prefix + variant.Name + "(")}
	for at, payload := range variant.Payload {
		if at > 0 {
			parts = append(parts, lit(", "))
		}
		got := b.reg("p")
		b.emit(&metals.VariantField{Dst: got, X: "v", Variant: index, Index: at})
		parts = append(parts, val(b.part(got, payload)))
	}
	return b.concat(append(parts, lit(")"))...)
}

func (b *reprBuilder) tupleBody(t *types.Tuple) {
	parts := []concatPart{lit("(")}
	for index, elem := range t.Elems {
		if index > 0 {
			parts = append(parts, lit(", "))
		}
		got := b.reg("e")
		b.emit(&metals.TupleGet{Dst: got, X: "v", Index: index})
		parts = append(parts, val(b.part(got, elem)))
	}
	b.emit(&metals.Return{Val: b.concat(append(parts, lit(")"))...)})
}

// elemsBody walks a slice or an array, appending to an accumulator. The
// separator is what forces the index test: the first element takes none.
func (b *reprBuilder) elemsBody(elem types.Type) {
	acc := b.reg("acc")
	b.emit(&metals.Const{Dst: acc, Type: metals.TStr, Value: `"["`})

	length := b.reg("n")
	b.emit(&metals.Len{Dst: length, X: "v"})
	index := b.reg("i")
	b.emit(&metals.Const{Dst: index, Type: metals.TUsize, Value: "0"})

	done := b.label("done")
	loop := b.label("walk")
	b.emit(&metals.Block{Label: done, Body: b.nested(func() {
		b.emit(&metals.Loop{Label: loop, Body: b.nested(func() {
			atEnd := b.reg("end")
			b.emit(&metals.Bin{Dst: atEnd, Op: "ge", A: index, B: length})
			b.emit(&metals.BrIf{Cond: atEnd, Label: done})

			b.emit(&metals.If{
				Cond: b.nonZero(index),
				Then: b.nested(func() { b.appendText(acc, ", ") }),
			})

			got := b.reg("x")
			b.emit(&metals.IndexGet{Dst: got, X: "v", Index: index})
			b.emit(&metals.StrConcat{Dst: acc, A: acc, B: b.part(got, elem)})

			one := b.reg("one")
			b.emit(&metals.Const{Dst: one, Type: metals.TUsize, Value: "1"})
			b.emit(&metals.Bin{Dst: index, Op: "iadd", A: index, B: one})
			b.emit(&metals.Br{Label: loop})
		})})
	})})

	b.appendText(acc, "]")
	b.emit(&metals.Return{Val: acc})
}

func (b *reprBuilder) nonZero(reg string) string {
	zero := b.reg("zero")
	b.emit(&metals.Const{Dst: zero, Type: metals.TUsize, Value: "0"})
	cond := b.reg("notFirst")
	b.emit(&metals.Bin{Dst: cond, Op: "ne", A: reg, B: zero})
	return cond
}

func (b *reprBuilder) appendText(acc, text string) {
	b.emit(&metals.StrConcat{Dst: acc, A: acc, B: b.text(text)})
}

func (b *reprBuilder) pointerBody(t *types.Pointer) {
	pointee := b.reg("target")
	b.emit(&metals.Load{Dst: pointee, Ptr: "v"})
	b.emit(&metals.Return{Val: b.concat(lit("&"), val(b.part(pointee, t.Elem)))})
}

// optionalBody prints the value or the word null, which is what makes an
// absent field readable rather than a hole.
func (b *reprBuilder) optionalBody(t *types.Optional) {
	empty := b.reg("none")
	b.emit(&metals.Const{Dst: empty, Type: b.l.metalsType(t), Null: true})
	absent := b.reg("isNull")
	b.emit(&metals.Bin{Dst: absent, Op: "eq", A: "v", B: empty})

	b.emit(&metals.If{
		Cond: absent,
		Then: b.nested(func() { b.emit(&metals.Return{Val: b.text("null")}) }),
	})

	present := b.reg("some")
	b.emit(&metals.OptUnwrap{Dst: present, X: "v"})
	b.emit(&metals.Return{Val: b.part(present, t.Elem)})
}
