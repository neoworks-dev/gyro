package cgen

import (
	"fmt"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// eqHelperFor returns the name of a synthesized structural-equality
// function for an aggregate type, emitting it once. The body walks the
// static layout; only str compares call out to the runtime.
func (g *generator) eqHelperFor(t metals.Type) string {
	key := t.String()
	if name, ok := g.eqHelpers[key]; ok {
		return name
	}
	name := fmt.Sprintf("EQ%d", len(g.eqHelpers))
	g.eqHelpers[key] = name
	spelling := g.cType(t)
	fmt.Fprintf(&g.decls, "static int64_t %s(const %s *a, const %s *b);\n", name, spelling, spelling)
	g.pending = append(g.pending, func() { g.emitEqHelper(name, spelling, t) })
	return name
}

func (g *generator) emitEqHelper(name, spelling string, t metals.Type) {
	g.raw("\n/* structural equality for %s */", comment(t.String()))
	g.raw("static int64_t %s(const %s *a, const %s *b) {", name, spelling, spelling)
	var body strings.Builder
	g.eqChecks(&body, 1, t, "(*a)", "(*b)")
	g.text.WriteString(body.String())
	g.line("return 1;")
	g.raw("}")
}

// eqChecks writes the comparisons for one type at a pair of access
// paths, returning 0 from the helper the moment two values differ.
func (g *generator) eqChecks(out *strings.Builder, depth int, t metals.Type, a, b string) {
	write := func(format string, args ...any) {
		out.WriteString(strings.Repeat("\t", depth))
		fmt.Fprintf(out, format, args...)
		out.WriteString("\n")
	}

	switch t := t.(type) {
	case *metals.Prim:
		if t.Kind == metals.Str {
			write("if (!gy_str_eq(%s, %s)) return 0;", a, b)
			return
		}
		write("if (!(%s == %s)) return 0;", a, b)

	case *metals.Ptr, *metals.ChanOf, *metals.FutureOf, *metals.Handle:
		write("if (!(%s == %s)) return 0;", a, b)

	case *metals.SliceOf, *metals.FnType:
		// The evaluator holds these to be never equal, so a value that
		// contains one never equals anything, itself included.
		write("return 0;")

	case *metals.Opt:
		write("if (%s.flag != %s.flag) return 0;", a, b)
		write("if (%s.flag) {", a)
		g.eqChecks(out, depth+1, t.Elem, a+".value", b+".value")
		write("}")

	case *metals.TupleOf:
		for index, elem := range t.Elems {
			g.eqChecks(out, depth, elem, fmt.Sprintf("%s.e%d", a, index), fmt.Sprintf("%s.e%d", b, index))
		}

	case *metals.ArrayOf:
		for index := int64(0); index < t.Len; index++ {
			g.eqChecks(out, depth, t.Elem, fmt.Sprintf("%s.v[%d]", a, index), fmt.Sprintf("%s.v[%d]", b, index))
		}

	case *metals.Named:
		g.eqNamedChecks(out, depth, write, t.Name, a, b)

	default:
		g.fail("no equality for type %s", t)
	}
}

func (g *generator) eqNamedChecks(out *strings.Builder, depth int, write func(string, ...any), name, a, b string) {
	decl := g.types[name]
	if decl == nil {
		g.fail("unknown type %%%s", name)
	}
	if decl.Record != nil {
		for index, field := range decl.Record.Fields {
			g.eqChecks(out, depth, field.Type, fmt.Sprintf("%s.f%d", a, index), fmt.Sprintf("%s.f%d", b, index))
		}
		return
	}

	write("if (%s.tag != %s.tag) return 0;", a, b)
	if g.widestPayload(decl.Enum) == 0 {
		return
	}
	write("switch (%s.tag) {", a)
	for index, variant := range decl.Enum.Variants {
		if len(variant.Payload) == 0 {
			continue
		}
		write("case %d: {", index)
		for at, payload := range variant.Payload {
			g.eqChecks(out, depth+2, payload,
				fmt.Sprintf("%s.u.v%d.p%d", a, index, at),
				fmt.Sprintf("%s.u.v%d.p%d", b, index, at))
		}
		write("} break;")
	}
	write("}")
}
