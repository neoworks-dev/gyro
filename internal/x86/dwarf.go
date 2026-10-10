package x86

import (
	"encoding/binary"
	"path/filepath"
)

// The debug sections are the minimum a debugger needs to map an address
// in .text to a source line: one compilation unit covering the section,
// an address range table pointing at it, and the line table. The line
// table follows nasm's rule: a row starts wherever an instruction's
// position differs from the previous row's, so a stretch of instructions
// under one `%line N+0` is one row.

const (
	dwLangMipsAssembler = 0x8001 // nasm's choice for assembly

	lineBase   = -5
	lineRange  = 14
	opcodeBase = 13
)

type lineRow struct {
	offset int64
	file   int
	line   int
}

// rows is the line table: one row per change of position among the
// instructions in .text.
func rows(text *section) ([]lineRow, []string) {
	var out []lineRow
	var files []string
	fileIndex := map[string]int{}
	var last position
	for _, it := range text.items {
		if it.size == 0 || it.kind == itemAlign || it.kind == itemSpace {
			continue
		}
		if len(out) > 0 && it.pos == last {
			continue
		}
		last = it.pos
		index, ok := fileIndex[it.pos.file]
		if !ok {
			files = append(files, it.pos.file)
			index = len(files)
			fileIndex[it.pos.file] = index
		}
		out = append(out, lineRow{offset: it.offset, file: index, line: it.pos.line})
	}
	return out, files
}

// dwarf builds .debug_info, .debug_abbrev, .debug_line and
// .debug_aranges for the text section.
func (a *assembler) dwarf(name string, text *section, textOut *outSection) ([]*outSection, error) {
	info := &outSection{name: ".debug_info", typ: shtProgbits, align: 1, symbol: true}
	abbrev := &outSection{name: ".debug_abbrev", typ: shtProgbits, align: 1, symbol: true}
	line := &outSection{name: ".debug_line", typ: shtProgbits, align: 1, symbol: true}
	aranges := &outSection{name: ".debug_aranges", typ: shtProgbits, align: 1}
	if text == nil {
		return nil, nil
	}

	abbrev.data = []byte{
		1, 0x11, 0, // abbreviation 1: DW_TAG_compile_unit, no children
		0x11, 0x01, // DW_AT_low_pc, DW_FORM_addr
		0x12, 0x01, // DW_AT_high_pc, DW_FORM_addr
		0x10, 0x06, // DW_AT_stmt_list, DW_FORM_data4
		0x03, 0x08, // DW_AT_name, DW_FORM_string
		0x1b, 0x08, // DW_AT_comp_dir, DW_FORM_string
		0x25, 0x08, // DW_AT_producer, DW_FORM_string
		0x13, 0x05, // DW_AT_language, DW_FORM_data2
		0, 0,
		0,
	}

	// .debug_info: a DWARF 3 unit header and the one entry.
	var d []byte
	d = binary.LittleEndian.AppendUint32(d, 0) // unit length, patched below
	d = binary.LittleEndian.AppendUint16(d, 3)
	info.relocs = append(info.relocs, reloc{offset: int64(len(d)), kind: rX86_64_32, out: abbrev})
	d = binary.LittleEndian.AppendUint32(d, 0)
	d = append(d, 8)
	d = append(d, 1)
	info.relocs = append(info.relocs, reloc{offset: int64(len(d)), kind: rX86_64_64, out: textOut})
	d = binary.LittleEndian.AppendUint64(d, 0)
	info.relocs = append(info.relocs, reloc{offset: int64(len(d)), kind: rX86_64_64, out: textOut, addend: text.size})
	d = binary.LittleEndian.AppendUint64(d, 0)
	info.relocs = append(info.relocs, reloc{offset: int64(len(d)), kind: rX86_64_32, out: line})
	d = binary.LittleEndian.AppendUint32(d, 0)
	d = append(append(d, name...), 0)
	// The compilation directory is the input's own, as nasm has it: what
	// a relative file in the line table is found against.
	d = append(append(d, filepath.Dir(name)...), 0)
	d = append(append(d, "gyro"...), 0)
	d = binary.LittleEndian.AppendUint16(d, dwLangMipsAssembler)
	binary.LittleEndian.PutUint32(d, uint32(len(d)-4))
	info.data = d

	// .debug_aranges: the unit covers .text.
	var r []byte
	r = binary.LittleEndian.AppendUint32(r, 0)
	r = binary.LittleEndian.AppendUint16(r, 2)
	aranges.relocs = append(aranges.relocs, reloc{offset: int64(len(r)), kind: rX86_64_32, out: info})
	r = binary.LittleEndian.AppendUint32(r, 0)
	r = append(r, 8, 0)
	r = append(r, 0, 0, 0, 0) // pad the header to twice the address size
	aranges.relocs = append(aranges.relocs, reloc{offset: int64(len(r)), kind: rX86_64_64, out: textOut})
	r = binary.LittleEndian.AppendUint64(r, 0)
	r = binary.LittleEndian.AppendUint64(r, uint64(text.size))
	r = binary.LittleEndian.AppendUint64(r, 0)
	r = binary.LittleEndian.AppendUint64(r, 0)
	binary.LittleEndian.PutUint32(r, uint32(len(r)-4))
	aranges.data = r

	// .debug_line: a DWARF 3 header, then one sequence over .text.
	table, files := rows(text)
	var h []byte
	h = append(h, 1, 1, 0x100+lineBase, lineRange, opcodeBase)
	h = append(h, 0, 1, 1, 1, 1, 0, 0, 0, 1, 0, 0, 1) // standard opcode lengths
	h = append(h, 0)                                  // no include directories
	for _, file := range files {
		h = append(append(h, file...), 0)
		h = append(h, 0, 0, 0) // directory, time, length
	}
	h = append(h, 0)

	var p []byte
	p = append(p, 0, 9, 2) // DW_LNE_set_address
	addressAt := len(p)
	p = binary.LittleEndian.AppendUint64(p, 0)
	addr, file, lineNo := int64(0), 1, 1
	for _, row := range table {
		if row.file != file {
			p = append(p, 4) // DW_LNS_set_file
			p = binary.AppendUvarint(p, uint64(row.file))
			file = row.file
		}
		dl, da := row.line-lineNo, row.offset-addr
		special := int64(dl-lineBase) + lineRange*da + opcodeBase
		if dl >= lineBase && dl < lineBase+lineRange && special <= 255 {
			p = append(p, byte(special))
		} else {
			if dl != 0 {
				p = append(p, 3) // DW_LNS_advance_line
				p = appendSleb(p, int64(dl))
			}
			if da != 0 {
				p = append(p, 2) // DW_LNS_advance_pc
				p = binary.AppendUvarint(p, uint64(da))
			}
			p = append(p, 1) // DW_LNS_copy
		}
		addr, lineNo = row.offset, row.line
	}
	if text.size > addr {
		p = append(p, 2)
		p = binary.AppendUvarint(p, uint64(text.size-addr))
	}
	p = append(p, 0, 1, 1) // DW_LNE_end_sequence

	var l []byte
	l = binary.LittleEndian.AppendUint32(l, 0)
	l = binary.LittleEndian.AppendUint16(l, 3)
	l = binary.LittleEndian.AppendUint32(l, uint32(len(h)))
	l = append(l, h...)
	line.relocs = append(line.relocs, reloc{offset: int64(len(l) + addressAt), kind: rX86_64_64, out: textOut})
	l = append(l, p...)
	binary.LittleEndian.PutUint32(l, uint32(len(l)-4))
	line.data = l

	return []*outSection{aranges, info, abbrev, line}, nil
}

// appendSleb writes a signed LEB128 number, which DWARF's signed operands
// are; Go's varint is zigzag-encoded and is not.
func appendSleb(out []byte, v int64) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 && b&0x40 == 0 || v == -1 && b&0x40 != 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}
