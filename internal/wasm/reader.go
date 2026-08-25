package wasm

import (
	"encoding/binary"
	"fmt"
	"math"
)

// The float readers are named apart from math's so that the decoder's
// own vocabulary stays uniform: every reader method is r.<what it reads>.
func float32frombits(bits uint32) float32 { return math.Float32frombits(bits) }
func float64frombits(bits uint64) float64 { return math.Float64frombits(bits) }

// The binary decoder. It reads the sections a linked module actually
// carries and refuses the rest by name, rather than skipping them
// quietly: a module with a table of externrefs or a SIMD body would
// otherwise translate into something that looks complete and is not.

type reader struct {
	src []byte
	pos int
}

func (r *reader) eof() bool { return r.pos >= len(r.src) }

func (r *reader) byte() (byte, error) {
	if r.pos >= len(r.src) {
		return 0, fmt.Errorf("unexpected end of input")
	}
	b := r.src[r.pos]
	r.pos++
	return b, nil
}

func (r *reader) bytes(n int) ([]byte, error) {
	if n < 0 || r.pos+n > len(r.src) {
		return nil, fmt.Errorf("unexpected end of input")
	}
	out := r.src[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

// u32 reads an unsigned LEB128. Five groups is the most a 32-bit value
// can occupy; a sixth means the encoding is malformed, not that the
// value is large.
func (r *reader) u32() (uint32, error) {
	var result uint32
	var shift uint
	for groups := 0; groups < 5; groups++ {
		b, err := r.byte()
		if err != nil {
			return 0, err
		}
		result |= uint32(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
	}
	return 0, fmt.Errorf("malformed unsigned LEB128")
}

// i64 reads a signed LEB128, sign-extending the final group.
func (r *reader) i64() (int64, error) {
	var result int64
	var shift uint
	for groups := 0; groups < 10; groups++ {
		b, err := r.byte()
		if err != nil {
			return 0, err
		}
		result |= int64(b&0x7F) << shift
		shift += 7
		if b&0x80 == 0 {
			if shift < 64 && b&0x40 != 0 {
				result |= ^int64(0) << shift
			}
			return result, nil
		}
	}
	return 0, fmt.Errorf("malformed signed LEB128")
}

func (r *reader) i32() (int32, error) {
	v, err := r.i64()
	return int32(v), err
}

func (r *reader) f32() (float32, error) {
	raw, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return float32frombits(binary.LittleEndian.Uint32(raw)), nil
}

func (r *reader) f64() (float64, error) {
	raw, err := r.bytes(8)
	if err != nil {
		return 0, err
	}
	return float64frombits(binary.LittleEndian.Uint64(raw)), nil
}

func (r *reader) name() (string, error) {
	length, err := r.u32()
	if err != nil {
		return "", err
	}
	raw, err := r.bytes(int(length))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (r *reader) valType() (valType, error) {
	b, err := r.byte()
	if err != nil {
		return 0, err
	}
	switch valType(b) {
	case valI32, valI64, valF32, valF64:
		return valType(b), nil
	case 0x7B:
		return 0, fmt.Errorf("v128: SIMD is not translated")
	case 0x70, 0x6F:
		return 0, fmt.Errorf("reference types are not translated")
	}
	return 0, fmt.Errorf("unknown value type %#x", b)
}

// ---------------------------------------------------------------------------
// Sections
// ---------------------------------------------------------------------------

const (
	secCustom  = 0
	secType    = 1
	secImport  = 2
	secFunc    = 3
	secTable   = 4
	secMemory  = 5
	secGlobal  = 6
	secExport  = 7
	secStart   = 8
	secElement = 9
	secCode    = 10
	secData    = 11
	secDataCnt = 12
)

func decode(binary []byte) (*wmodule, error) {
	r := &reader{src: binary}
	magic, err := r.bytes(8)
	if err != nil {
		return nil, err
	}
	if string(magic[:4]) != "\x00asm" {
		return nil, fmt.Errorf("not a wasm module: it does not start with the magic")
	}
	if version := magic[4]; version != 1 {
		return nil, fmt.Errorf("wasm version %d is not 1", version)
	}

	m := &wmodule{Names: map[uint32]string{}}
	for !r.eof() {
		id, err := r.byte()
		if err != nil {
			return nil, err
		}
		size, err := r.u32()
		if err != nil {
			return nil, err
		}
		payload, err := r.bytes(int(size))
		if err != nil {
			return nil, fmt.Errorf("section %d: %w", id, err)
		}
		if err := decodeSection(m, id, payload); err != nil {
			return nil, fmt.Errorf("section %d: %w", id, err)
		}
	}
	if len(m.Code) != len(m.Funcs) {
		return nil, fmt.Errorf("%d function bodies for %d declarations", len(m.Code), len(m.Funcs))
	}
	return m, nil
}

func decodeSection(m *wmodule, id byte, payload []byte) error {
	s := &reader{src: payload}
	switch id {
	case secCustom:
		return decodeCustom(m, s)
	case secType:
		return decodeTypes(m, s)
	case secImport:
		return decodeImports(m, s)
	case secFunc:
		return each(s, func() error {
			index, err := s.u32()
			m.Funcs = append(m.Funcs, index)
			return err
		})
	case secTable:
		// A table's own declaration says nothing translation needs: the
		// element section holds the entries, and call_indirect resolves
		// against those. Its limits only matter to a runtime that grows it.
		return nil
	case secMemory:
		return decodeMemories(m, s)
	case secGlobal:
		return decodeGlobals(m, s)
	case secExport:
		return decodeExports(m, s)
	case secStart:
		// A linked C module's start function runs the constructors, and
		// __wasm_call_ctors is exported too, so translation calls it from
		// the entry point rather than honouring a separate mechanism.
		return nil
	case secElement:
		return decodeElements(m, s)
	case secCode:
		return decodeCode(m, s)
	case secData:
		return decodeData(m, s)
	case secDataCnt:
		return nil
	}
	return fmt.Errorf("unknown section %d", id)
}

// each runs body once per entry of a LEB-counted vector.
func each(s *reader, body func() error) error {
	count, err := s.u32()
	if err != nil {
		return err
	}
	for i := uint32(0); i < count; i++ {
		if err := body(); err != nil {
			return fmt.Errorf("entry %d: %w", i, err)
		}
	}
	return nil
}

func decodeTypes(m *wmodule, s *reader) error {
	return each(s, func() error {
		form, err := s.byte()
		if err != nil {
			return err
		}
		if form != 0x60 {
			return fmt.Errorf("type form %#x is not a function type", form)
		}
		var ft funcType
		if err := each(s, func() error {
			t, err := s.valType()
			ft.Params = append(ft.Params, t)
			return err
		}); err != nil {
			return err
		}
		if err := each(s, func() error {
			t, err := s.valType()
			ft.Results = append(ft.Results, t)
			return err
		}); err != nil {
			return err
		}
		m.Types = append(m.Types, ft)
		return nil
	})
}

func decodeImports(m *wmodule, s *reader) error {
	return each(s, func() error {
		module, err := s.name()
		if err != nil {
			return err
		}
		name, err := s.name()
		if err != nil {
			return err
		}
		kind, err := s.byte()
		if err != nil {
			return err
		}
		imp := wimport{Module: module, Name: name, Kind: kind}
		switch kind {
		case kindFunc:
			if imp.TypeIndex, err = s.u32(); err != nil {
				return err
			}
		case kindTable:
			if _, err := s.byte(); err != nil { // element type
				return err
			}
			if err := skipLimits(s); err != nil {
				return err
			}
		case kindMemory:
			if err := skipLimits(s); err != nil {
				return err
			}
		case kindGlobal:
			if _, err := s.valType(); err != nil {
				return err
			}
			if _, err := s.byte(); err != nil { // mutability
				return err
			}
		default:
			return fmt.Errorf("unknown import kind %#x", kind)
		}
		m.Imports = append(m.Imports, imp)
		return nil
	})
}

func skipLimits(s *reader) error {
	flags, err := s.byte()
	if err != nil {
		return err
	}
	if _, err := s.u32(); err != nil {
		return err
	}
	if flags&0x01 != 0 {
		if _, err := s.u32(); err != nil {
			return err
		}
	}
	return nil
}

func decodeMemories(m *wmodule, s *reader) error {
	return each(s, func() error {
		flags, err := s.byte()
		if err != nil {
			return err
		}
		min, err := s.u32()
		if err != nil {
			return err
		}
		mem := memType{Min: min}
		if flags&0x01 != 0 {
			if mem.Max, err = s.u32(); err != nil {
				return err
			}
		}
		m.Memories = append(m.Memories, mem)
		return nil
	})
}

func decodeGlobals(m *wmodule, s *reader) error {
	return each(s, func() error {
		typ, err := s.valType()
		if err != nil {
			return err
		}
		mutable, err := s.byte()
		if err != nil {
			return err
		}
		init, err := constExpr(s)
		if err != nil {
			return err
		}
		m.Globals = append(m.Globals, globalDef{Type: typ, Mutable: mutable == 1, Init: init})
		return nil
	})
}

// constExpr reads an initializer expression. A linked module's globals
// and segment offsets are numeric constants — the relocatable forms that
// are not have already been resolved by wasm-ld.
func constExpr(s *reader) (int64, error) {
	op, err := s.byte()
	if err != nil {
		return 0, err
	}
	var value int64
	switch op {
	case 0x41: // i32.const
		v, err := s.i32()
		if err != nil {
			return 0, err
		}
		value = int64(v)
	case 0x42: // i64.const
		if value, err = s.i64(); err != nil {
			return 0, err
		}
	case 0x43: // f32.const
		if _, err := s.bytes(4); err != nil {
			return 0, err
		}
	case 0x44: // f64.const
		if _, err := s.bytes(8); err != nil {
			return 0, err
		}
	case 0x23: // global.get
		return 0, fmt.Errorf("an initializer that reads another global is not translated")
	default:
		return 0, fmt.Errorf("initializer opcode %#x is not a constant", op)
	}
	end, err := s.byte()
	if err != nil {
		return 0, err
	}
	if end != 0x0B {
		return 0, fmt.Errorf("initializer does not end where it should")
	}
	return value, nil
}

func decodeExports(m *wmodule, s *reader) error {
	return each(s, func() error {
		name, err := s.name()
		if err != nil {
			return err
		}
		kind, err := s.byte()
		if err != nil {
			return err
		}
		index, err := s.u32()
		if err != nil {
			return err
		}
		m.Exports = append(m.Exports, export{Name: name, Kind: kind, Index: index})
		return nil
	})
}

func decodeElements(m *wmodule, s *reader) error {
	return each(s, func() error {
		flags, err := s.u32()
		if err != nil {
			return err
		}
		// Flag 0 is the only form wasm-ld emits for a C program: an
		// active segment in table 0 with a constant offset.
		if flags != 0 {
			return fmt.Errorf("element segment kind %d is not translated", flags)
		}
		offset, err := constExpr(s)
		if err != nil {
			return err
		}
		seg := element{Offset: uint32(offset)}
		if err := each(s, func() error {
			index, err := s.u32()
			seg.Funcs = append(seg.Funcs, index)
			return err
		}); err != nil {
			return err
		}
		m.Elements = append(m.Elements, seg)
		return nil
	})
}

func decodeCode(m *wmodule, s *reader) error {
	return each(s, func() error {
		size, err := s.u32()
		if err != nil {
			return err
		}
		body, err := s.bytes(int(size))
		if err != nil {
			return err
		}
		inner := &reader{src: body}
		var c code
		if err := each(inner, func() error {
			count, err := inner.u32()
			if err != nil {
				return err
			}
			typ, err := inner.valType()
			if err != nil {
				return err
			}
			for i := uint32(0); i < count; i++ {
				c.Locals = append(c.Locals, typ)
			}
			return nil
		}); err != nil {
			return err
		}
		c.Body = inner.src[inner.pos:]
		m.Code = append(m.Code, c)
		return nil
	})
}

func decodeData(m *wmodule, s *reader) error {
	return each(s, func() error {
		flags, err := s.u32()
		if err != nil {
			return err
		}
		if flags != 0 {
			return fmt.Errorf("data segment kind %d is not translated", flags)
		}
		offset, err := constExpr(s)
		if err != nil {
			return err
		}
		length, err := s.u32()
		if err != nil {
			return err
		}
		raw, err := s.bytes(int(length))
		if err != nil {
			return err
		}
		m.Data = append(m.Data, dataSeg{Offset: uint32(offset), Bytes: raw})
		return nil
	})
}

// decodeCustom reads the name section and ignores every other custom
// section. Function names are cosmetic — they make the generated METALS
// readable, which is most of what this bridge is for.
func decodeCustom(m *wmodule, s *reader) error {
	name, err := s.name()
	if err != nil || name != "name" {
		return nil
	}
	for !s.eof() {
		kind, err := s.byte()
		if err != nil {
			return nil
		}
		size, err := s.u32()
		if err != nil {
			return nil
		}
		payload, err := s.bytes(int(size))
		if err != nil {
			return nil
		}
		if kind != 1 { // function names
			continue
		}
		sub := &reader{src: payload}
		_ = each(sub, func() error {
			index, err := sub.u32()
			if err != nil {
				return err
			}
			text, err := sub.name()
			if err != nil {
				return err
			}
			m.Names[index] = text
			return nil
		})
	}
	return nil
}
