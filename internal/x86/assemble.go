// Package x86 assembles the text internal/nasm generates into an ELF64
// relocatable object, the file nasm made of it before. The text stays the
// backend's output and its readable form; this package reads exactly the
// subset of NASM the backend writes — its instructions, sections, data,
// symbols and %line — and nothing else, so a form it does not know is an
// error rather than a guess.
//
// The bytes are nasm's: where an instruction has several encodings it
// picks the one nasm picks, and jumps are relaxed the way nasm relaxes
// them, starting short and growing only the ones that do not reach. That
// keeps the object comparable byte for byte against nasm's on the same
// text, which is how the package is tested.
package x86

import (
	"fmt"
	"strings"
)

// Assemble turns the backend's assembly text into an ELF64 relocatable
// object with a DWARF line table. name is the file the line table credits
// for lines no %line directive covers, as nasm credits its input file.
func Assemble(source, name string) ([]byte, error) {
	stmts, err := parse(source, name)
	if err != nil {
		return nil, err
	}
	a := &assembler{
		symbols:  map[string]*symbol{},
		sections: map[string]*section{},
	}
	if err := a.collect(stmts); err != nil {
		return nil, err
	}
	a.relax()
	if err := a.emit(); err != nil {
		return nil, err
	}
	return a.object(name)
}

type section struct {
	name   string
	index  int // position among the program's sections, in declaration order
	nobits bool
	flags  uint64
	align  int64
	items  []*item
	data   []byte
	size   int64
	relocs []reloc
}

// reloc is a relocation against a symbol table entry: a section's own
// symbol for anything defined here, an extern's for anything not.
type reloc struct {
	offset int64
	kind   uint32
	sym    *symbol     // an extern
	target *section    // or a program section's own symbol
	out    *outSection // or a debug section's
	addend int64
}

type symbol struct {
	name    string
	section *section // nil for an extern
	offset  int64
	defined bool
	global  bool
	extern  bool
	order   int // definition order, which is the symbol table's

	function           bool
	data               bool
	sizeEnd, sizeStart string
}

type itemKind int

const (
	itemLabel itemKind = iota
	itemCode
	itemJump
	itemData
	itemSpace
	itemAlign
)

// item is one statement placed in a section.
type item struct {
	kind   itemKind
	line   int
	pos    position
	offset int64
	size   int64

	code   encoded // itemCode and itemData
	fixups []fixup // itemData: one per address in a dq

	// A jump to a label: the condition (-1 for jmp), the target, and
	// whether it currently takes the short form.
	cond   int
	target string
	short  bool

	sym   *symbol // itemLabel
	align int64   // itemAlign
}

type assembler struct {
	order    []*section
	sections map[string]*section
	symbols  map[string]*symbol
	nsyms    int
}

// knownSections are the sections the backend writes, with nasm's flags
// and starting alignment for each.
var knownSections = map[string]struct {
	flags  uint64
	align  int64
	nobits bool
}{
	".text":   {shfAlloc | shfExec, 16, false},
	".rodata": {shfAlloc, 4, false},
	".data":   {shfAlloc | shfWrite, 4, false},
	".bss":    {shfAlloc | shfWrite, 4, true},
}

// sectionOptions reads what a section directive says beyond its name:
// `nobits alloc noexec write align=8 tls`. A section the backend does not
// know by name has only what its options give it.
func sectionOptions(options []string) (flags uint64, align int64, nobits bool, err error) {
	align = 1
	for _, option := range options {
		switch {
		case option == "nobits":
			nobits = true
		case option == "alloc":
			flags |= shfAlloc
		case option == "write":
			flags |= shfWrite
		case option == "exec":
			flags |= shfExec
		case option == "tls":
			flags |= shfTLS
		case option == "noexec" || option == "progbits":
		case strings.HasPrefix(option, "align="):
			n, perr := parseInt(strings.TrimPrefix(option, "align="))
			if perr != nil || n <= 0 || n&(n-1) != 0 {
				return 0, 0, false, fmt.Errorf("align needs a power of two")
			}
			align = n
		default:
			return 0, 0, false, fmt.Errorf("unsupported section option %s", option)
		}
	}
	return flags, align, nobits, nil
}

func (a *assembler) symbol(name string) *symbol {
	s, ok := a.symbols[name]
	if !ok {
		s = &symbol{name: name, order: -1}
		a.symbols[name] = s
	}
	return s
}

// collect places every statement in its section. A label beginning with
// a dot belongs to the last label that does not, and the same prefix
// applies to every reference to it, as in nasm.
func (a *assembler) collect(stmts []stmt) error {
	var current *section
	scope := ""
	qualify := func(name string) string {
		if strings.HasPrefix(name, ".") {
			return scope + name
		}
		return name
	}
	fail := func(s stmt, format string, args ...any) error {
		return fmt.Errorf("x86: line %d: %s", s.line, fmt.Sprintf(format, args...))
	}

	for _, s := range stmts {
		switch s.kind {
		case stmtSection:
			known, ok := knownSections[s.name]
			if len(s.options) > 0 {
				flags, align, nobits, err := sectionOptions(s.options)
				if err != nil {
					return fail(s, "%v", err)
				}
				known.flags, known.align, known.nobits, ok = flags, align, nobits, true
			}
			if !ok {
				return fail(s, "unknown section %s", s.name)
			}
			sec, ok := a.sections[s.name]
			if !ok {
				sec = &section{name: s.name, index: len(a.order), flags: known.flags, align: known.align, nobits: known.nobits}
				a.sections[s.name] = sec
				a.order = append(a.order, sec)
			}
			current = sec
			continue
		case stmtGlobal:
			sym := a.symbol(s.name)
			sym.global = true
			if s.function {
				sym.function = true
				sym.sizeEnd, sym.sizeStart = s.sizeEnd, s.sizeStart
			}
			sym.data = sym.data || s.data
			continue
		case stmtExtern:
			a.symbol(s.name).extern = true
			continue
		}

		if current == nil {
			return fail(s, "a statement outside any section")
		}
		it := &item{line: s.line, pos: s.pos}
		switch s.kind {
		case stmtLabel:
			if !strings.HasPrefix(s.name, ".") {
				scope = s.name
			}
			sym := a.symbol(qualify(s.name))
			if sym.defined {
				return fail(s, "%s is defined twice", sym.name)
			}
			sym.defined, sym.section, sym.order = true, current, a.nsyms
			a.nsyms++
			it.kind, it.sym = itemLabel, sym

		case stmtAlign:
			it.kind, it.align = itemAlign, s.count
			current.align = max(current.align, s.count)

		case stmtSpace:
			if !current.nobits {
				return fail(s, "resb belongs in .bss")
			}
			it.kind, it.size = itemSpace, s.count

		case stmtData:
			if current.nobits {
				return fail(s, "%s in a section with no contents", s.mnemonic)
			}
			it.kind = itemData
			width := 1
			if s.mnemonic == "dq" {
				width = 8
			}
			for _, op := range s.operands {
				at := len(it.code.bytes)
				if op.kind == opSym {
					it.fixups = append(it.fixups, fixup{offset: at, size: 8, sym: qualify(op.sym), addend: op.imm})
					it.code.bytes = append(it.code.bytes, make([]byte, 8)...)
					continue
				}
				if width == 1 {
					if op.imm < -128 || op.imm > 255 {
						return fail(s, "%d does not fit a byte", op.imm)
					}
					it.code.bytes = append(it.code.bytes, byte(op.imm))
				} else {
					it.code.bytes = append(it.code.bytes, le64(op.imm)...)
				}
			}
			it.size = int64(len(it.code.bytes))

		case stmtInst:
			if current.nobits {
				return fail(s, "an instruction in a section with no contents")
			}
			if cond, ok := jump(s.mnemonic); ok && len(s.operands) == 1 && s.operands[0].kind == opSym {
				it.kind, it.cond, it.target, it.short = itemJump, cond, qualify(s.operands[0].sym), true
				if s.operands[0].imm != 0 {
					return fail(s, "a jump target takes no offset")
				}
				break
			}
			ops := make([]operand, len(s.operands))
			copy(ops, s.operands)
			if s.mnemonic == "call" && len(ops) == 1 && ops[0].kind == opSym {
				// A direct call is always rel32.
				it.kind = itemCode
				it.code = encoded{
					bytes: []byte{0xe8, 0, 0, 0, 0},
					fix:   &fixup{offset: 1, size: 4, sym: qualify(ops[0].sym), addend: ops[0].imm - 4, pcrel: true},
				}
				it.size = 5
				break
			}
			for i := range ops {
				if ops[i].kind == opMem && ops[i].mem.sym != "" {
					ops[i].mem.sym = qualify(ops[i].mem.sym)
				}
				if ops[i].kind == opSym {
					return fail(s, "%s cannot take the address %s as an operand", s.mnemonic, ops[i].sym)
				}
			}
			code, err := encodeInst(s.prefix, s.mnemonic, ops)
			if err != nil {
				return fail(s, "%v", err)
			}
			it.kind, it.code, it.size = itemCode, code, int64(len(code.bytes))
		}
		current.items = append(current.items, it)
	}

	for _, sym := range a.symbols {
		switch {
		case sym.extern && sym.defined:
			return fmt.Errorf("x86: %s is both extern and defined", sym.name)
		case sym.global && !sym.defined && !sym.extern:
			return fmt.Errorf("x86: global %s is never defined", sym.name)
		}
	}
	return nil
}

// relax chooses each jump's size the way nasm does, so the bytes are
// nasm's. Every pass walks the code once and sizes each jump by the
// distance to its target as currently known: where the target lies
// behind, its offset in this pass; where it lies ahead, its offset in the
// previous pass; and in the first pass, where nothing ahead is known yet,
// short. Passes repeat until no label moves.
//
// The result is a consistent choice — every short jump reaches and every
// long one would not reach from its short form — but not always the
// smallest: a jump whose target is 128 bytes away only because the jump
// itself is long stays long. nasm settles on that answer and so does
// this. A sequence of passes that keeps moving labels is cut off after
// maxPasses, and from then on a jump may only grow, which always ends.
func (a *assembler) relax() {
	const maxPasses = 100
	previous := map[*symbol]int64{}
	for pass := 0; ; pass++ {
		current := map[*symbol]int64{}
		for _, sec := range a.order {
			var at int64
			for _, it := range sec.items {
				it.offset = at
				switch it.kind {
				case itemLabel:
					it.sym.offset = at
					current[it.sym] = at
				case itemJump:
					target := a.symbols[it.target]
					short := false
					if target != nil && target.defined && target.section == sec {
						known, behind := current[target]
						if !behind {
							known, behind = previous[target]
						}
						// In the first pass a label ahead is unknown, and
						// nasm is optimistic about it.
						short = !behind || fitsInt8(known-(at+2))
					}
					if pass >= maxPasses && !it.short {
						short = false
					}
					it.short = short
					it.size = jumpSize(it.cond, it.short)
				case itemAlign:
					it.size = (it.align - at%it.align) % it.align
				}
				at += it.size
			}
			sec.size = at
		}
		if pass > 0 && sameOffsets(previous, current) {
			return
		}
		previous = current
	}
}

func sameOffsets(a, b map[*symbol]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for sym, at := range a {
		if b[sym] != at {
			return false
		}
	}
	return true
}

func jumpSize(cond int, short bool) int64 {
	switch {
	case short:
		return 2
	case cond < 0:
		return 5
	default:
		return 6
	}
}

// emit writes every section's bytes and resolves its fixups: a
// pc-relative reference within its own section becomes a plain
// displacement, and anything else a relocation.
func (a *assembler) emit() error {
	for _, sec := range a.order {
		if sec.nobits {
			continue
		}
		sec.data = make([]byte, 0, sec.size)
		for _, it := range sec.items {
			switch it.kind {
			case itemCode, itemData:
				sec.data = append(sec.data, it.code.bytes...)
				if it.code.fix != nil {
					if err := a.resolve(sec, it, *it.code.fix); err != nil {
						return err
					}
				}
				for _, fix := range it.fixups {
					if err := a.resolve(sec, it, fix); err != nil {
						return err
					}
				}
			case itemJump:
				target := a.symbols[it.target]
				if target == nil || !target.defined {
					return fmt.Errorf("x86: line %d: jump to undefined label %s", it.line, it.target)
				}
				if target.section != sec {
					return fmt.Errorf("x86: line %d: jump to %s in another section", it.line, it.target)
				}
				disp := target.offset - (it.offset + it.size)
				switch {
				case it.short && it.cond < 0:
					sec.data = append(sec.data, 0xeb, byte(int8(disp)))
				case it.short:
					sec.data = append(sec.data, 0x70|byte(it.cond), byte(int8(disp)))
				case it.cond < 0:
					sec.data = append(sec.data, 0xe9)
					sec.data = append(sec.data, le32(disp)...)
				default:
					sec.data = append(sec.data, 0x0f, 0x80|byte(it.cond))
					sec.data = append(sec.data, le32(disp)...)
				}
			case itemAlign:
				// nasm pads with nop, in data as in code.
				for range it.size {
					sec.data = append(sec.data, 0x90)
				}
			}
		}
	}
	return nil
}

func (a *assembler) resolve(sec *section, it *item, fix fixup) error {
	sym := a.symbols[fix.sym]
	if sym == nil || !sym.defined && !sym.extern {
		return fmt.Errorf("x86: line %d: undefined symbol %s", it.line, fix.sym)
	}
	at := it.offset + int64(fix.offset)
	if sym.defined && fix.pcrel && sym.section == sec {
		value := sym.offset + fix.addend - at
		if !fitsInt32(value) {
			return fmt.Errorf("x86: line %d: %s is out of reach", it.line, fix.sym)
		}
		copy(sec.data[at:], le32(value))
		return nil
	}

	if fix.gottpoff {
		if !sym.defined && !sym.extern {
			return fmt.Errorf("x86: line %d: undefined symbol %s", it.line, fix.sym)
		}
		sec.relocs = append(sec.relocs, reloc{offset: at, kind: rX86_64_GOTTPOFF, sym: sym, addend: fix.addend})
		return nil
	}

	r := reloc{offset: at, addend: fix.addend}
	switch {
	case fix.pcrel:
		r.kind = rX86_64_PC32
	case fix.size == 8:
		r.kind = rX86_64_64
	default:
		return fmt.Errorf("x86: line %d: no relocation for a %d-byte absolute address", it.line, fix.size)
	}
	if sym.defined {
		// nasm relocates against the section, with the symbol's offset
		// folded into the addend, whether or not the symbol is global.
		r.target = sym.section
		r.addend += sym.offset
	} else {
		r.sym = sym
	}
	sec.relocs = append(sec.relocs, r)
	return nil
}
