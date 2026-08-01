package nasm

import (
	"github.com/gyro-lang/gyro/internal/metals"
)

// Layout rules, from docs/native.md: every scalar occupies one qword,
// aggregates are laid out inline field after field, everything is a
// multiple of 8 and aligned to 8. Nothing is packed.

const wordSize = 8

// sizeOf returns a type's static size in bytes.
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
	widest := 0
	for _, variant := range decl.Enum.Variants {
		size := 0
		for _, payload := range variant.Payload {
			size += g.sizeOf(payload)
		}
		if size > widest {
			widest = size
		}
	}
	return wordSize + widest // { tag, payload }
}

// fieldOffset returns a record field's offset inside the record.
func (g *generator) fieldOffset(record *metals.RecordShape, index int) int {
	offset := 0
	for i := 0; i < index; i++ {
		offset += g.sizeOf(record.Fields[i].Type)
	}
	return offset
}

// variantFieldOffset returns a payload field's offset inside the enum,
// tag included.
func (g *generator) variantFieldOffset(enum *metals.EnumShape, variant, index int) int {
	offset := wordSize
	for i := 0; i < index; i++ {
		offset += g.sizeOf(enum.Variants[variant].Payload[i])
	}
	return offset
}

// tupleOffset returns an element's offset inside the tuple.
func (g *generator) tupleOffset(tuple *metals.TupleOf, index int) int {
	offset := 0
	for i := 0; i < index; i++ {
		offset += g.sizeOf(tuple.Elems[i])
	}
	return offset
}

// argClass says how a value travels in a call: integer register, SSE
// register, or by pointer to the caller's value.
type argClass int

const (
	classInt argClass = iota
	classSSE
	classMem
)

func (g *generator) classOf(t metals.Type) argClass {
	switch t := t.(type) {
	case *metals.Prim:
		if t.Kind == metals.F32 || t.Kind == metals.F64 {
			return classSSE
		}
		return classInt
	case *metals.Ptr, *metals.ChanOf, *metals.FutureOf, *metals.Handle:
		return classInt
	}
	return classMem
}

// intKindInfo describes an integer primitive for the arithmetic emitter.
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
