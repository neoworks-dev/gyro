package cgen

import (
	"fmt"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// The layout rules are docs/native.md's, unchanged: every scalar occupies
// one qword, aggregates sit inline field after field, everything is a
// multiple of 8 and aligned to 8. C reproduces that for free — every
// member of every generated struct is itself 8 bytes wide and 8-aligned,
// so the compiler has nothing to pad. Each typedef carries a
// _Static_assert on its size, which turns any drift into a build error
// instead of a wrong answer at the runtime boundary.

const wordSize = 8

func (g *generator) sizeOf(t metals.Type) int {
	switch t := t.(type) {
	case *metals.Prim:
		return wordSize
	case *metals.Ptr, *metals.ChanOf, *metals.FutureOf, *metals.Handle:
		return wordSize
	case *metals.FnType:
		return 2 * wordSize // { code, env }
	case *metals.SliceOf:
		return 2 * wordSize // { data, len }
	case *metals.Opt:
		return wordSize + g.sizeOf(t.Elem) // { flag, payload }
	case *metals.TupleOf:
		total := 0
		for _, elem := range t.Elems {
			total += g.sizeOf(elem)
		}
		return total
	case *metals.ArrayOf:
		return int(t.Len) * g.sizeOf(t.Elem)
	case *metals.Named:
		return g.namedSize(t.Name)
	}
	g.fail("no layout for type %s", t)
	return 0
}

func (g *generator) namedSize(name string) int {
	decl := g.types[name]
	if decl == nil {
		g.fail("unknown type %%%s", name)
	}
	if decl.Record != nil {
		total := 0
		for _, field := range decl.Record.Fields {
			total += g.sizeOf(field.Type)
		}
		return total
	}
	return wordSize + g.widestPayload(decl.Enum)
}

func (g *generator) widestPayload(enum *metals.EnumShape) int {
	widest := 0
	for _, variant := range enum.Variants {
		size := 0
		for _, payload := range variant.Payload {
			size += g.sizeOf(payload)
		}
		if size > widest {
			widest = size
		}
	}
	return widest
}

// ---------------------------------------------------------------------------
// C type names
// ---------------------------------------------------------------------------

// cType is the C spelling of a METALS type. Aggregates are interned: the
// first mention emits a typedef into the types section, dependencies
// first, so the generated file reads top to bottom.
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
	case *metals.SliceOf:
		return "gy_slice"
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
	return name
}

func (g *generator) typeBody(t metals.Type, name string) string {
	var out strings.Builder
	switch t := t.(type) {
	case *metals.Opt:
		fmt.Fprintf(&out, "typedef struct { int64_t flag; %s value; } %s;", g.cType(t.Elem), name)

	case *metals.TupleOf:
		out.WriteString("typedef struct {")
		for index, elem := range t.Elems {
			fmt.Fprintf(&out, " %s e%d;", g.cType(elem), index)
		}
		fmt.Fprintf(&out, " } %s;", name)

	case *metals.ArrayOf:
		// A zero-length array is a GNU extension, which is what keeps its
		// size at 0 the way the layout table says.
		fmt.Fprintf(&out, "typedef struct { %s v[%d]; } %s;", g.cType(t.Elem), t.Len, name)

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
			fmt.Fprintf(&out, " %s f%d; /* %s */", g.cType(field.Type), index, comment(field.Name))
		}
		fmt.Fprintf(&out, " } %s;", name)
		return out.String()
	}

	// An enum is { tag, payload }: the payload is a union of one struct
	// per variant that carries one, which is exactly the widest variant
	// wide and needs no padding, since every payload field is a qword.
	fmt.Fprintf(&out, "typedef struct {\n\tint64_t tag;\n")
	if g.widestPayload(decl.Enum) > 0 {
		out.WriteString("\tunion {\n")
		for index, variant := range decl.Enum.Variants {
			if len(variant.Payload) == 0 {
				continue
			}
			out.WriteString("\t\tstruct {")
			for at, payload := range variant.Payload {
				fmt.Fprintf(&out, " %s p%d;", g.cType(payload), at)
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
