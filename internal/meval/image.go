package meval

import (
	"encoding/binary"
	"math"

	"github.com/gyro-lang/gyro/internal/metals"
)

// A global's static image.
//
// A global may carry the bytes it starts with in place of the zeros it
// would otherwise hold. The two native tiers put those bytes in the
// object file and are finished; this one holds typed values and has no
// memory to lay them in, so it reads the image back into values instead.
//
// The offsets come from metals.Layout, which is where the native
// backends get theirs, so the three tiers cannot disagree about where a
// member sits. The byte order is the target's — little-endian, the same
// thing a data segment already means.

func (m *machine) decodeImage(t metals.Type, image []byte) value {
	if size := m.layout.Size(t); len(image) != size {
		m.throw("a static image of %d bytes for %s, which is %d", len(image), t, size)
	}
	return m.decode(t, image)
}

func (m *machine) decode(t metals.Type, image []byte) value {
	switch t := t.(type) {
	case *metals.Prim:
		return m.decodePrim(t, image)

	case *metals.Opt:
		// The flagged layout: a word saying whether anything is there,
		// then the payload. A niche optional is a pointer, and a pointer
		// has no static image at all.
		if binary.LittleEndian.Uint64(image) == 0 {
			return nullVal{}
		}
		return m.decode(t.Elem, image[metals.Word:])

	case *metals.ArrayOf:
		stride := m.layout.Size(t.Elem)
		elems := make([]value, t.Len)
		for i := range elems {
			elems[i] = m.decode(t.Elem, image[i*stride:])
		}
		return arrayVal{elems: elems}

	case *metals.TupleOf:
		offsets := m.layout.Offsets(t.Elems)
		elems := make(tupleVal, len(t.Elems))
		for i, elem := range t.Elems {
			elems[i] = m.decode(elem, image[offsets[i]:])
		}
		return elems

	case *metals.Named:
		return m.decodeNamed(t, image)
	}
	m.throw("no static image for %s", t)
	return nil
}

func (m *machine) decodeNamed(t *metals.Named, image []byte) value {
	decl := m.types[t.Name]
	if decl == nil {
		m.throw("no type %%%s", t.Name)
	}
	if decl.Record != nil {
		fields := make([]value, len(decl.Record.Fields))
		for i, field := range decl.Record.Fields {
			fields[i] = m.decode(field.Type, image[m.layout.FieldOffset(decl.Record, i):])
		}
		return recordVal{typeName: t.Name, fields: fields}
	}

	tag := int(binary.LittleEndian.Uint64(image))
	if tag < 0 || tag >= len(decl.Enum.Variants) {
		m.throw("a static image of %%%s with tag %d, which names no variant", t.Name, tag)
	}
	variant := decl.Enum.Variants[tag]
	payload := make([]value, len(variant.Payload))
	for i, field := range variant.Payload {
		payload[i] = m.decode(field, image[m.layout.VariantFieldOffset(decl.Enum, tag, i):])
	}
	return enumVal{typeName: t.Name, tag: tag, payload: payload}
}

func (m *machine) decodePrim(t *metals.Prim, image []byte) value {
	switch t.Kind {
	case metals.Bool:
		return boolVal(image[0] != 0)
	case metals.F32:
		return floatVal{v: float64(math.Float32frombits(binary.LittleEndian.Uint32(image))), kind: t.Kind}
	case metals.F64:
		return floatVal{v: math.Float64frombits(binary.LittleEndian.Uint64(image)), kind: t.Kind}
	case metals.U8:
		return uintVal{v: uint64(image[0]), kind: t.Kind}
	case metals.U16:
		return uintVal{v: uint64(binary.LittleEndian.Uint16(image)), kind: t.Kind}
	case metals.U32:
		return uintVal{v: uint64(binary.LittleEndian.Uint32(image)), kind: t.Kind}
	case metals.U64, metals.Usize:
		return uintVal{v: binary.LittleEndian.Uint64(image), kind: t.Kind}
	case metals.I8:
		return intVal{v: int64(int8(image[0])), kind: t.Kind}
	case metals.I16:
		return intVal{v: int64(int16(binary.LittleEndian.Uint16(image))), kind: t.Kind}
	case metals.I32, metals.Rune:
		return intVal{v: int64(int32(binary.LittleEndian.Uint32(image))), kind: t.Kind}
	case metals.I64:
		return intVal{v: int64(binary.LittleEndian.Uint64(image)), kind: t.Kind}
	}
	m.throw("no static image for %s", t)
	return nil
}
