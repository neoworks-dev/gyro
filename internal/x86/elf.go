package x86

import (
	"encoding/binary"
	"fmt"
	"sort"
)

const (
	shtProgbits = 1
	shtSymtab   = 2
	shtStrtab   = 3
	shtRela     = 4
	shtNobits   = 8

	shfWrite = 0x1
	shfAlloc = 0x2
	shfExec  = 0x4

	stbLocal  = 0
	stbGlobal = 1

	sttNotype  = 0
	sttFunc    = 2
	sttSection = 3
	sttFile    = 4

	shnAbs = 0xfff1

	rX86_64_64   = 1
	rX86_64_PC32 = 2
	rX86_64_32   = 10
)

// outSection is a section as the file holds it, program and bookkeeping
// alike.
type outSection struct {
	name    string
	typ     uint32
	flags   uint64
	align   int64
	data    []byte
	size    int64 // for SHT_NOBITS, which has no data
	link    uint32
	info    uint32
	entsize int64

	relocs []reloc // what the matching .rela section will hold
	symbol bool    // gets a section symbol, as a relocation target
	index  int
}

type strtab struct {
	data []byte
	at   map[string]uint32
}

func newStrtab() *strtab { return &strtab{data: []byte{0}, at: map[string]uint32{"": 0}} }

func (t *strtab) add(s string) uint32 {
	if at, ok := t.at[s]; ok {
		return at
	}
	at := uint32(len(t.data))
	t.data = append(append(t.data, s...), 0)
	t.at[s] = at
	return at
}

// object writes the relocatable file: the program's sections, the debug
// sections, a relocation section for each that needs one, and the symbol
// table.
func (a *assembler) object(name string) ([]byte, error) {
	var sections []*outSection
	bySection := map[*section]*outSection{}
	for _, sec := range a.order {
		out := &outSection{name: sec.name, typ: shtProgbits, flags: sec.flags, align: sec.align, data: sec.data, relocs: sec.relocs, symbol: true}
		if sec.nobits {
			out.typ, out.size = shtNobits, sec.size
		}
		bySection[sec] = out
		sections = append(sections, out)
	}

	text := a.sections[".text"]
	var textOut *outSection
	if text != nil {
		textOut = bySection[text]
	}
	debug, err := a.dwarf(name, text, textOut)
	if err != nil {
		return nil, err
	}
	sections = append(sections, debug...)

	for i, sec := range sections {
		sec.index = i + 1
	}

	// The symbol table: locals first, as ELF requires, then globals.
	type elfSym struct {
		name  uint32
		info  byte
		shndx uint16
		value uint64
		size  uint64
	}
	strings := newStrtab()
	syms := []elfSym{{}}
	syms = append(syms, elfSym{name: strings.add(name), info: stbLocal<<4 | sttFile, shndx: shnAbs})
	sectionSym := map[*outSection]uint32{}
	for _, sec := range sections {
		if sec.symbol {
			sectionSym[sec] = uint32(len(syms))
			syms = append(syms, elfSym{info: stbLocal<<4 | sttSection, shndx: uint16(sec.index)})
		}
	}

	var defined, externs []*symbol
	for _, sym := range a.symbols {
		switch {
		case sym.defined:
			defined = append(defined, sym)
		case sym.extern:
			externs = append(externs, sym)
		}
	}
	sort.Slice(defined, func(i, j int) bool { return defined[i].order < defined[j].order })
	sort.Slice(externs, func(i, j int) bool { return externs[i].name < externs[j].name })

	index := map[*symbol]uint32{}
	add := func(sym *symbol, bind byte) error {
		s := elfSym{name: strings.add(sym.name), info: bind << 4}
		if sym.defined {
			s.shndx = uint16(bySection[sym.section].index)
			s.value = uint64(sym.offset)
		}
		if sym.function {
			s.info |= sttFunc
			if sym.sizeEnd != "" {
				end, start := a.symbols[sym.sizeEnd], a.symbols[sym.sizeStart]
				if end == nil || start == nil || !end.defined || !start.defined || end.section != start.section {
					return fmt.Errorf("x86: the size of %s is not a distance between two labels", sym.name)
				}
				s.size = uint64(end.offset - start.offset)
			}
		}
		index[sym] = uint32(len(syms))
		syms = append(syms, s)
		return nil
	}
	for _, sym := range defined {
		if !sym.global {
			if err := add(sym, stbLocal); err != nil {
				return nil, err
			}
		}
	}
	firstGlobal := len(syms)
	for _, sym := range externs {
		if err := add(sym, stbGlobal); err != nil {
			return nil, err
		}
	}
	for _, sym := range defined {
		if sym.global {
			if err := add(sym, stbGlobal); err != nil {
				return nil, err
			}
		}
	}

	symtabAt := len(sections) + 1
	var relas []*outSection
	for _, sec := range sections {
		if len(sec.relocs) == 0 {
			continue
		}
		var data []byte
		for _, r := range sec.relocs {
			var target uint32
			switch {
			case r.sym != nil:
				target = index[r.sym]
			case r.target != nil:
				target = sectionSym[bySection[r.target]]
			default:
				target = sectionSym[r.out]
			}
			data = binary.LittleEndian.AppendUint64(data, uint64(r.offset))
			data = binary.LittleEndian.AppendUint64(data, uint64(target)<<32|uint64(r.kind))
			data = binary.LittleEndian.AppendUint64(data, uint64(r.addend))
		}
		relas = append(relas, &outSection{
			name: ".rela" + sec.name, typ: shtRela, align: 8, data: data, entsize: 24,
			link: uint32(symtabAt), info: uint32(sec.index),
		})
	}
	// Relocation sections go after .symtab, .strtab and .shstrtab, so the
	// table's own index is known while they are written.
	var symtab []byte
	for _, s := range syms {
		symtab = binary.LittleEndian.AppendUint32(symtab, s.name)
		symtab = append(symtab, s.info, 0)
		symtab = binary.LittleEndian.AppendUint16(symtab, s.shndx)
		symtab = binary.LittleEndian.AppendUint64(symtab, s.value)
		symtab = binary.LittleEndian.AppendUint64(symtab, s.size)
	}
	sections = append(sections,
		&outSection{name: ".symtab", typ: shtSymtab, align: 8, data: symtab, entsize: 24,
			link: uint32(symtabAt + 1), info: uint32(firstGlobal)},
		&outSection{name: ".strtab", typ: shtStrtab, align: 1, data: strings.data},
	)
	shstrtab := &outSection{name: ".shstrtab", typ: shtStrtab, align: 1}
	sections = append(sections, shstrtab)
	sections = append(sections, relas...)

	names := newStrtab()
	nameAt := make([]uint32, len(sections))
	for i, sec := range sections {
		nameAt[i] = names.add(sec.name)
	}
	shstrtab.data = names.data

	// The file: the header, every section's contents at its alignment,
	// then the section header table.
	file := make([]byte, 64)
	offsets := make([]int64, len(sections))
	for i, sec := range sections {
		align := max(sec.align, 1)
		for int64(len(file))%align != 0 {
			file = append(file, 0)
		}
		offsets[i] = int64(len(file))
		if sec.typ != shtNobits {
			file = append(file, sec.data...)
		}
	}
	for len(file)%8 != 0 {
		file = append(file, 0)
	}
	shoff := len(file)
	file = append(file, make([]byte, 64)...) // the null section
	for i, sec := range sections {
		size := int64(len(sec.data))
		if sec.typ == shtNobits {
			size = sec.size
		}
		file = binary.LittleEndian.AppendUint32(file, nameAt[i])
		file = binary.LittleEndian.AppendUint32(file, sec.typ)
		file = binary.LittleEndian.AppendUint64(file, sec.flags)
		file = binary.LittleEndian.AppendUint64(file, 0)
		file = binary.LittleEndian.AppendUint64(file, uint64(offsets[i]))
		file = binary.LittleEndian.AppendUint64(file, uint64(size))
		file = binary.LittleEndian.AppendUint32(file, sec.link)
		file = binary.LittleEndian.AppendUint32(file, sec.info)
		file = binary.LittleEndian.AppendUint64(file, uint64(max(sec.align, 1)))
		file = binary.LittleEndian.AppendUint64(file, uint64(sec.entsize))
	}

	header := file[:64]
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0})
	binary.LittleEndian.PutUint16(header[16:], 1)  // ET_REL
	binary.LittleEndian.PutUint16(header[18:], 62) // EM_X86_64
	binary.LittleEndian.PutUint32(header[20:], 1)  // EV_CURRENT
	binary.LittleEndian.PutUint64(header[40:], uint64(shoff))
	binary.LittleEndian.PutUint16(header[52:], 64) // e_ehsize
	binary.LittleEndian.PutUint16(header[58:], 64) // e_shentsize
	binary.LittleEndian.PutUint16(header[60:], uint16(len(sections)+1))
	binary.LittleEndian.PutUint16(header[62:], uint16(shstrtab.index0(sections)))
	return file, nil
}

// index0 is a section's index in the header table, the null section
// being 0.
func (s *outSection) index0(sections []*outSection) int {
	for i, sec := range sections {
		if sec == s {
			return i + 1
		}
	}
	return 0
}
