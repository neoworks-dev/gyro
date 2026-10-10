package nasm

import (
	"fmt"

	"github.com/gyro-lang/gyro/internal/metals"
)

// eqHelperFor returns the symbol of a synthesized structural-equality
// function for an aggregate type: fn(a ptr in rdi, b ptr in rsi) -> rax.
// The body walks the static layout inline; only str fields call out.
func (g *generator) eqHelperFor(t metals.Type) string {
	key := t.String()
	if symbol, ok := g.eqHelpers[key]; ok {
		return symbol
	}
	symbol := fmt.Sprintf("EQ_%d", len(g.eqHelpers))
	g.eqHelpers[key] = symbol
	g.pending = append(g.pending, func() { g.emitEqHelper(symbol, t) })
	return symbol
}

func (g *generator) emitEqHelper(symbol string, t metals.Type) {
	g.text.WriteString("\n")
	g.comment("structural equality for %s", t)
	g.label(symbol)
	g.ins("push rbp")
	g.ins("mov rbp, rsp")

	unequal := g.newLabel()
	g.emitEqCompare(t, 0, unequal)

	g.ins("mov eax, 1")
	g.ins("pop rbp")
	g.ins("ret")
	g.label(unequal)
	g.ins("xor eax, eax")
	g.ins("pop rbp")
	g.ins("ret")
}

// emitEqCompare compares the values at [rdi+offset] and [rsi+offset],
// jumping to unequal when they differ. Preserves rdi and rsi.
func (g *generator) emitEqCompare(t metals.Type, offset int, unequal string) {
	a := memLoc{base: "rdi", off: offset}
	b := memLoc{base: "rsi", off: offset}

	switch t := t.(type) {
	case *metals.Prim:
		switch t.Kind {
		case metals.F32:
			// Compared as the single precision storage holds, which is the
			// value an f32 register would round to anyway.
			g.ins("movss xmm0, dword %s", a)
			g.ins("ucomiss xmm0, dword %s", b)
			g.ins("jp %s", unequal)
			g.ins("jne %s", unequal)
		case metals.F64:
			g.ins("movsd xmm0, %s", a)
			g.ins("ucomisd xmm0, %s", b)
			g.ins("jp %s", unequal)
			g.ins("jne %s", unequal)
		case metals.Str:
			g.ins("push rdi")
			g.ins("push rsi")
			g.ins("mov rdi, %s", a)
			g.ins("mov rsi, %s", b)
			g.ins("call %s", g.extern("gy_str_eq"))
			g.ins("pop rsi")
			g.ins("pop rdi")
			g.ins("test rax, rax")
			g.ins("jz %s", unequal)
		default:
			g.emitScalarCompare(t, a, b, unequal)
		}

	case *metals.Ptr, *metals.AnyPtr, *metals.ChanOf, *metals.Handle, *metals.CodeType:
		g.emitWordCompare(a, b, unequal)

	case *metals.SliceOf, *metals.FnType:
		// The evaluator holds these to be never equal.
		g.ins("jmp %s", unequal)

	case *metals.Opt:
		if metals.NicheOpt(t) {
			// The niche makes the whole value the payload, and a pointer's
			// equality was already identity, so one compare answers both.
			g.emitWordCompare(a, b, unequal)
			break
		}
		bothNull := g.newLabel()
		g.ins("mov rax, %s", a)
		g.ins("cmp rax, %s", b)
		g.ins("jne %s", unequal)
		g.ins("test rax, rax")
		g.ins("jz %s", bothNull)
		g.emitEqCompare(t.Elem, offset+8, unequal)
		g.label(bothNull)

	case *metals.TupleOf:
		for index, elem := range t.Elems {
			g.emitEqCompare(elem, offset+g.tupleOffset(t, index), unequal)
		}

	case *metals.ArrayOf:
		stride := g.sizeOf(t.Elem)
		for index := 0; index < int(t.Len); index++ {
			g.emitEqCompare(t.Elem, offset+index*stride, unequal)
		}

	case *metals.Named:
		g.emitNamedCompare(t.Name, offset, unequal)

	default:
		g.fail("no equality for type %s", t)
	}
}

func (g *generator) emitWordCompare(a, b memLoc, unequal string) {
	g.ins("mov rax, %s", a)
	g.ins("cmp rax, %s", b)
	g.ins("jne %s", unequal)
}

// emitScalarCompare compares two integers where they are stored, which
// for a narrow one is fewer bytes than a word: reading the whole word
// would compare the padding, or the field next door.
func (g *generator) emitScalarCompare(t *metals.Prim, a, b memLoc, unequal string) {
	access, narrow := narrowAccess[t.Kind]
	if !narrow {
		g.emitWordCompare(a, b, unequal)
		return
	}
	view := narrow64(access.bits)
	g.ins("mov %s, %s %s", view, access.keyword, a)
	g.ins("cmp %s, %s %s", view, access.keyword, b)
	g.ins("jne %s", unequal)
}

func (g *generator) emitNamedCompare(name string, offset int, unequal string) {
	decl := g.types[name]
	if decl == nil {
		g.fail("unknown type %%%s", name)
	}
	if decl.Record != nil {
		for index, field := range decl.Record.Fields {
			g.emitEqCompare(field.Type, offset+g.fieldOffset(decl.Record, index), unequal)
		}
		return
	}

	g.emitWordCompare(memLoc{"rdi", offset}, memLoc{"rsi", offset}, unequal)
	after := g.newLabel()
	for tag, variant := range decl.Enum.Variants {
		if len(variant.Payload) == 0 {
			continue
		}
		next := g.newLabel()
		// The payload compares clobber rax, so the tag reloads each round.
		g.ins("mov rax, %s", memLoc{base: "rdi", off: offset})
		g.ins("cmp rax, %d", tag)
		g.ins("jne %s", next)
		for index, payload := range variant.Payload {
			g.emitEqCompare(payload, offset+g.variantFieldOffset(decl.Enum, tag, index), unequal)
		}
		g.ins("jmp %s", after)
		g.label(next)
	}
	g.label(after)
}
