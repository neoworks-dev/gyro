package cgen

import (
	"fmt"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// The layout rules are docs/native.md's: storage is C's, and a register
// is one machine word. That split is what this file spells in C — a
// register is a local of its widest type, a member is a local of its
// real one — and C's own implicit conversions are the narrowing and the
// widening between them, including an f32's rounding.
//
// The offsets themselves are metals.Layout's, so the two backends cannot
// pad a struct differently. Every generated typedef then carries a
// _Static_assert on its size and one per member offset, so a
// disagreement with `cc` is a build error rather than a wrong answer at
// the runtime boundary.

const wordSize = metals.Word

// sizeOf is a type's storage size.
func (g *generator) sizeOf(t metals.Type) int { return g.layout.Size(t) }

func (g *generator) namedSize(name string) int {
	return g.layout.Size(&metals.Named{Name: name})
}

func (g *generator) widestPayload(enum *metals.EnumShape) int {
	return g.layout.PayloadSize(enum)
}

// ---------------------------------------------------------------------------
// Staging
// ---------------------------------------------------------------------------

// The runtime stores what it holds — a channel's elements, an Array's, a
// future's result — in storage representation, so that a slice of an
// Array is the same bytes a C function would read. A narrow scalar
// therefore cannot be handed over as the address of its register: the
// register is wider, and an f32's register is a double. It goes through
// a temporary of the storage type, which is also where C's conversion
// does the narrowing and the rounding.

// stagedOut runs emit with the address of a register's value as the
// runtime wants to read it.
func (f *frame) stagedOut(reg string, emit func(address string)) {
	t := f.typeOf(reg)
	if !metals.Narrow(t) {
		emit("(void *)&" + f.reg(reg))
		return
	}
	name := f.g.stageName()
	f.line("{")
	f.indent++
	f.line("%s %s = %s;", f.g.memberType(t), name, f.reg(reg))
	emit("(void *)&" + name)
	f.indent--
	f.line("}")
}

// stagedIn is the other direction: emit writes through the address, and
// the value lands back in the register afterwards.
func (f *frame) stagedIn(reg string, emit func(address string)) {
	t := f.typeOf(reg)
	if !metals.Narrow(t) {
		emit("(void *)&" + f.reg(reg))
		return
	}
	name := f.g.stageName()
	f.line("{")
	f.indent++
	f.line("%s %s = 0;", f.g.memberType(t), name)
	emit("(void *)&" + name)
	f.line("%s = %s;", f.reg(reg), name)
	f.indent--
	f.line("}")
}

func (g *generator) stageName() string {
	g.stageN++
	return fmt.Sprintf("st%d", g.stageN)
}

// ---------------------------------------------------------------------------
// C type names
// ---------------------------------------------------------------------------

// memberType is the C spelling of a type where it is stored: a record's
// field, a tuple's element, an array's, a cell a pointer addresses. This
// is the C type a foreign declaration of the same struct would use, and
// it is why a Gyro struct and a C struct are the same bytes.
func (g *generator) memberType(t metals.Type) string {
	prim, ok := t.(*metals.Prim)
	if !ok {
		return g.cType(t)
	}
	switch prim.Kind {
	case metals.I8:
		return "int8_t"
	case metals.I16:
		return "int16_t"
	case metals.I32, metals.Rune:
		return "int32_t"
	case metals.U8:
		return "uint8_t"
	case metals.U16:
		return "uint16_t"
	case metals.U32:
		return "uint32_t"
	case metals.Bool:
		// One byte, and unsigned, so that reading it back widens to 0 or
		// 1 rather than to a sign.
		return "uint8_t"
	case metals.F32:
		return "float"
	}
	return g.cType(t)
}

// cType is the C spelling of a METALS type as a register holds it:
// scalars at their widest, so that arithmetic happens in one width and
// C's integer promotions cannot narrow it behind the backend's back.
// Aggregates are interned: the first mention emits a typedef into the
// types section, dependencies first, so the generated file reads top to
// bottom.
func (g *generator) cType(t metals.Type) string {
	switch t := t.(type) {
	case *metals.Prim:
		switch t.Kind {
		case metals.F32, metals.F64:
			return "double"
		case metals.Str:
			return "const gy_str *"
		case metals.U8, metals.U16, metals.U32, metals.U64, metals.Usize:
			return "uint64_t"
		default:
			return "int64_t"
		}
	case *metals.Ptr, *metals.ChanOf, *metals.FutureOf, *metals.Handle:
		return "void *"
	case *metals.FnType:
		return "gy_fn"
	case *metals.CodeType:
		return "gy_code"
	case *metals.SliceOf:
		return "gy_slice"
	case *metals.Any:
		return "gy_any"
	case *metals.Opt:
		// A niche optional is its own payload, so it is spelled as one and
		// never becomes a struct with a flag in front of it. A code
		// pointer keeps its own spelling: C does not convert one to
		// `void *`, and the two are only the same size by convention.
		if metals.NicheOpt(t) {
			if _, code := t.Elem.(*metals.CodeType); code {
				return "gy_code"
			}
			return "void *"
		}
	}

	key := t.String()
	if name, ok := g.typeNames[key]; ok {
		return name
	}
	if g.building[key] {
		g.fail("type %s contains itself", key)
	}
	g.building[key] = true
	// The counter advances before the body is built: emitting it interns
	// the member types, and those must not be handed the same number.
	g.typeN++
	name := fmt.Sprintf("T%d", g.typeN)
	// The name is published only after the body is emitted, so a nested
	// mention of the same type is caught by the cycle guard above rather
	// than referring to a typedef that does not exist yet.
	body := g.typeBody(t, name)
	delete(g.building, key)
	g.typeNames[key] = name

	fmt.Fprintf(&g.typedefs, "\n/* %s */\n%s\n", comment(key), body)
	fmt.Fprintf(&g.typedefs, "_Static_assert(sizeof(%s) == %d, %s);\n",
		name, g.sizeOf(t), cStringLiteral("layout of "+key))
	for _, member := range g.memberOffsets(t) {
		fmt.Fprintf(&g.typedefs, "_Static_assert(offsetof(%s, %s) == %d, %s);\n",
			name, member.name, member.offset, cStringLiteral("layout of "+key))
	}
	return name
}

type memberOffset struct {
	name   string
	offset int
}

// memberOffsets is what the generated asserts hold `cc` to: our own
// answer for where each member sits. A size assert alone would pass on
// two structs that pad differently and total the same.
func (g *generator) memberOffsets(t metals.Type) []memberOffset {
	var out []memberOffset
	add := func(name string, offset int) { out = append(out, memberOffset{name, offset}) }

	switch t := t.(type) {
	case *metals.Opt:
		add("value", wordSize)
	case *metals.TupleOf:
		for index := range t.Elems {
			add(fmt.Sprintf("e%d", index), g.layout.TupleOffset(t, index))
		}
	case *metals.Named:
		decl := g.types[t.Name]
		if decl == nil {
			return nil
		}
		if decl.Record != nil {
			for index := range decl.Record.Fields {
				add(fmt.Sprintf("f%d", index), g.layout.FieldOffset(decl.Record, index))
			}
			return out
		}
		for index, variant := range decl.Enum.Variants {
			for at := range variant.Payload {
				add(fmt.Sprintf("u.v%d.p%d", index, at),
					g.layout.VariantFieldOffset(decl.Enum, index, at))
			}
		}
	}
	return out
}

func (g *generator) typeBody(t metals.Type, name string) string {
	var out strings.Builder
	switch t := t.(type) {
	case *metals.Opt:
		fmt.Fprintf(&out, "typedef struct { int64_t flag; %s value; } %s;", g.memberType(t.Elem), name)

	case *metals.TupleOf:
		out.WriteString("typedef struct {")
		for index, elem := range t.Elems {
			fmt.Fprintf(&out, " %s e%d;", g.memberType(elem), index)
		}
		fmt.Fprintf(&out, " } %s;", name)

	case *metals.ArrayOf:
		// A zero-length array is a GNU extension, which is what keeps its
		// size at 0 the way the layout table says.
		fmt.Fprintf(&out, "typedef struct { %s v[%d]; } %s;", g.memberType(t.Elem), t.Len, name)

	case *metals.Named:
		return g.namedBody(t.Name, name)
	}
	return out.String()
}

func (g *generator) namedBody(typeName, name string) string {
	decl := g.types[typeName]
	if decl == nil {
		g.fail("unknown type %%%s", typeName)
	}

	var out strings.Builder
	if decl.Record != nil {
		out.WriteString("typedef struct {")
		for index, field := range decl.Record.Fields {
			fmt.Fprintf(&out, " %s f%d; /* %s */", g.memberType(field.Type), index, comment(field.Name))
		}
		fmt.Fprintf(&out, " } %s;", name)
		return out.String()
	}

	// An enum is { tag, payload }: the payload is a union of one struct
	// per variant that carries one. The tag is a whole word and nothing
	// is aligned more than that, so every payload starts at eight.
	fmt.Fprintf(&out, "typedef struct {\n\tint64_t tag;\n")
	if g.widestPayload(decl.Enum) > 0 {
		out.WriteString("\tunion {\n")
		for index, variant := range decl.Enum.Variants {
			if len(variant.Payload) == 0 {
				continue
			}
			out.WriteString("\t\tstruct {")
			for at, payload := range variant.Payload {
				fmt.Fprintf(&out, " %s p%d;", g.memberType(payload), at)
			}
			fmt.Fprintf(&out, " } v%d; /* %s */\n", index, comment(variant.Name))
		}
		out.WriteString("\t} u;\n")
	}
	fmt.Fprintf(&out, "} %s;", name)
	return out.String()
}

// ---------------------------------------------------------------------------
// Integer kinds
// ---------------------------------------------------------------------------

// intKindInfo describes an integer primitive for the arithmetic emitter,
// exactly as the nasm backend understands it.
type intKindInfo struct {
	width  int // bits
	signed bool
}

var intKinds = map[metals.PrimKind]intKindInfo{
	metals.I8:    {8, true},
	metals.I16:   {16, true},
	metals.I32:   {32, true},
	metals.I64:   {64, true},
	metals.Rune:  {32, true},
	metals.U8:    {8, false},
	metals.U16:   {16, false},
	metals.U32:   {32, false},
	metals.U64:   {64, false},
	metals.Usize: {64, false},
	metals.Bool:  {8, false},
}
