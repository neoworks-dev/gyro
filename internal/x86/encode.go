package x86

import (
	"encoding/binary"
	"fmt"
	"math"
)

// encoded is one instruction's bytes, with at most one field that names
// a symbol: a rip-relative address, or a call's target.
type encoded struct {
	bytes []byte
	fix   *fixup
}

// fixup is a field the object writer fills in once symbols have
// addresses: at offset within the instruction or data item, size bytes
// wide, holding sym+addend, minus the field's own address when pcrel.
type fixup struct {
	offset int
	size   int
	sym    string
	addend int64
	pcrel  bool

	// gottpoff makes the field a GOT entry's offset from the next
	// instruction, which the linker fills in with the symbol's offset in
	// the thread-local block. It is always a relocation, against the
	// symbol itself.
	gottpoff bool
}

// conditions maps every spelling of a condition code to its number.
var conditions = map[string]byte{
	"o": 0x0, "no": 0x1, "b": 0x2, "c": 0x2, "nae": 0x2, "ae": 0x3, "nb": 0x3, "nc": 0x3,
	"e": 0x4, "z": 0x4, "ne": 0x5, "nz": 0x5, "be": 0x6, "na": 0x6, "a": 0x7, "nbe": 0x7,
	"s": 0x8, "ns": 0x9, "p": 0xa, "pe": 0xa, "np": 0xb, "po": 0xb,
	"l": 0xc, "nge": 0xc, "ge": 0xd, "nl": 0xd, "le": 0xe, "ng": 0xe, "g": 0xf, "nle": 0xf,
}

// The ALU group shares one encoding shape; the number is both the /digit
// of its immediate forms and the high bits of its register forms.
var aluOps = map[string]byte{"add": 0, "or": 1, "adc": 2, "sbb": 3, "and": 4, "sub": 5, "xor": 6, "cmp": 7}

var shiftOps = map[string]byte{"rol": 0, "ror": 1, "shl": 4, "sal": 4, "shr": 5, "sar": 7}

// unaryOps are the F6/F7 group: one r/m operand.
var unaryOps = map[string]byte{"not": 2, "neg": 3, "mul": 4, "div": 6, "idiv": 7}

var bitTestOps = map[string]byte{"bt": 4, "bts": 5, "btr": 6, "btc": 7}

// sseOps are the scalar and packed operations of the form xmm, xmm/m:
// mandatory prefix (0 for none) and opcode after 0F.
var sseOps = map[string][2]byte{
	"addsd": {0xf2, 0x58}, "mulsd": {0xf2, 0x59}, "subsd": {0xf2, 0x5c}, "divsd": {0xf2, 0x5e},
	"minsd": {0xf2, 0x5d}, "maxsd": {0xf2, 0x5f}, "sqrtsd": {0xf2, 0x51},
	"addss": {0xf3, 0x58}, "mulss": {0xf3, 0x59}, "subss": {0xf3, 0x5c}, "divss": {0xf3, 0x5e},
	"minss": {0xf3, 0x5d}, "maxss": {0xf3, 0x5f}, "sqrtss": {0xf3, 0x51},
	"cvtsd2ss": {0xf2, 0x5a}, "cvtss2sd": {0xf3, 0x5a},
	"ucomisd": {0x66, 0x2e}, "ucomiss": {0, 0x2e}, "comisd": {0x66, 0x2f}, "comiss": {0, 0x2f},
	"andpd": {0x66, 0x54}, "andnpd": {0x66, 0x55}, "orpd": {0x66, 0x56}, "xorpd": {0x66, 0x57},
	"andps": {0, 0x54}, "andnps": {0, 0x55}, "orps": {0, 0x56}, "xorps": {0, 0x57},
}

// sseSizes is the width of the memory operand each SSE operation reads,
// which an explicit size keyword must agree with.
var sseSizes = map[string]int{
	"addsd": 8, "mulsd": 8, "subsd": 8, "divsd": 8, "minsd": 8, "maxsd": 8, "sqrtsd": 8,
	"addss": 4, "mulss": 4, "subss": 4, "divss": 4, "minss": 4, "maxss": 4, "sqrtss": 4,
	"cvtsd2ss": 8, "cvtss2sd": 4, "ucomisd": 8, "ucomiss": 4, "comisd": 8, "comiss": 4,
	"andpd": 16, "andnpd": 16, "orpd": 16, "xorpd": 16, "andps": 16, "andnps": 16, "orps": 16, "xorps": 16,
}

// encoding accumulates one instruction. Its fields are the parts of an
// x86-64 instruction in order: legacy prefixes, REX, opcode, ModRM, SIB,
// displacement and immediate.
type encoding struct {
	prefixes []byte
	rexW     bool
	rexForce bool // a REX byte even with no bit set, for spl..dil
	opcode   []byte

	hasModRM bool
	regField int // ModRM.reg, 0-15 (bit 3 goes to REX.R)
	rmReg    int // register in ModRM.rm when rmMem is nil
	rmMem    *mem

	// opcodeReg is a register in the opcode's low bits, -1 when none.
	opcodeReg int

	imm []byte
}

func (e *encoding) noteRegs(regs ...reg) {
	for _, r := range regs {
		if r.low {
			e.rexForce = true
		}
	}
}

// build lays out the instruction and returns it with its fixup, if any.
func (e *encoding) build() (encoded, error) {
	var out []byte
	out = append(out, e.prefixes...)

	var rex byte
	if e.rexW {
		rex |= 8
	}
	var modrm []byte
	var fix *fixup
	if e.hasModRM {
		rex |= byte(e.regField>>3) << 2
		if e.rmMem == nil {
			rex |= byte(e.rmReg >> 3)
			modrm = []byte{0xc0 | byte(e.regField&7)<<3 | byte(e.rmReg&7)}
		} else {
			var x, b byte
			var err error
			var at int
			modrm, x, b, at, err = encodeMem(e.regField, e.rmMem)
			if err != nil {
				return encoded{}, err
			}
			rex |= x<<1 | b
			if at >= 0 {
				fix = &fixup{offset: at, size: 4, sym: e.rmMem.sym, pcrel: true, gottpoff: e.rmMem.gottpoff}
			}
			if e.rmMem.segment != 0 {
				out = append(out, e.rmMem.segment)
			}
		}
	}
	if e.opcodeReg >= 8 {
		rex |= 1
	}
	if rex != 0 || e.rexForce {
		out = append(out, 0x40|rex)
	}
	out = append(out, e.opcode...)
	if fix != nil {
		fix.offset += len(out)
	}
	out = append(out, modrm...)
	out = append(out, e.imm...)
	if fix != nil {
		// The processor adds the address of the next instruction, so the
		// field holds the target less the bytes that follow the field.
		fix.addend = e.rmMem.disp - int64(len(out)-fix.offset)
	}
	return encoded{bytes: out, fix: fix}, nil
}

// encodeMem writes ModRM, SIB and displacement for a memory operand. It
// returns REX.X and REX.B, and where a rip-relative field begins (-1
// when there is none), counted from the ModRM byte.
func encodeMem(regField int, m *mem) (bytes []byte, rexX, rexB byte, ripAt int, err error) {
	r := byte(regField&7) << 3
	if m.sym != "" {
		return []byte{0x05 | r, 0, 0, 0, 0}, 0, 0, 1, nil
	}
	if m.base < 0 && m.index < 0 && m.segment != 0 {
		// An absolute address under a segment: a SIB byte with neither
		// base nor index, then a 32-bit displacement.
		if m.disp < math.MinInt32 || m.disp > math.MaxInt32 {
			return nil, 0, 0, -1, fmt.Errorf("displacement %d does not fit 32 bits", m.disp)
		}
		return append([]byte{4 | r, 0x25}, le32(m.disp)...), 0, 0, -1, nil
	}
	if m.base < 0 {
		return nil, 0, 0, -1, fmt.Errorf("an address needs a base register or a symbol")
	}
	if m.disp < math.MinInt32 || m.disp > math.MaxInt32 {
		return nil, 0, 0, -1, fmt.Errorf("displacement %d does not fit 32 bits", m.disp)
	}

	base := byte(m.base & 7)
	var mod byte
	var disp []byte
	switch {
	case m.disp == 0 && base != 5: // rbp and r13 have no displacement-free form
		mod = 0
	case m.disp >= -128 && m.disp <= 127:
		mod, disp = 1, []byte{byte(int8(m.disp))}
	default:
		mod, disp = 2, binary.LittleEndian.AppendUint32(nil, uint32(int32(m.disp)))
	}
	rexB = byte(m.base >> 3)

	if m.index < 0 && base != 4 {
		bytes = append([]byte{mod<<6 | r | base}, disp...)
		return bytes, 0, rexB, -1, nil
	}
	// rsp and r12 as a base, and every index, need a SIB byte. An index of
	// 100 without REX.X means none.
	index := byte(4)
	scale := byte(0)
	if m.index >= 0 {
		index = byte(m.index & 7)
		rexX = byte(m.index >> 3)
		scale = map[int]byte{1: 0, 2: 1, 4: 2, 8: 3}[m.scale]
	}
	bytes = append([]byte{mod<<6 | r | 4, scale<<6 | index<<3 | base}, disp...)
	return bytes, rexX, rexB, -1, nil
}

func le16(v int64) []byte { return binary.LittleEndian.AppendUint16(nil, uint16(v)) }
func le32(v int64) []byte { return binary.LittleEndian.AppendUint32(nil, uint32(v)) }
func le64(v int64) []byte { return binary.LittleEndian.AppendUint64(nil, uint64(v)) }

func fitsInt8(v int64) bool  { return v >= -128 && v <= 127 }
func fitsInt32(v int64) bool { return v >= math.MinInt32 && v <= math.MaxInt32 }

// immediate checks that v fits an operand of size bytes and returns it
// as the processor will see it once sign-extended from that width: a
// 32-bit operand accepts 0xffffffff, which is -1 there.
func immediate(v int64, size int) (int64, error) {
	switch size {
	case 1:
		if v < -128 || v > 255 {
			return 0, fmt.Errorf("%d does not fit a byte", v)
		}
		return int64(int8(v)), nil
	case 2:
		if v < -32768 || v > 65535 {
			return 0, fmt.Errorf("%d does not fit a word", v)
		}
		return int64(int16(v)), nil
	case 4:
		if v < math.MinInt32 || v > math.MaxUint32 {
			return 0, fmt.Errorf("%d does not fit a dword", v)
		}
		return int64(int32(v)), nil
	default:
		if !fitsInt32(v) {
			return 0, fmt.Errorf("%d does not fit a sign-extended dword", v)
		}
		return v, nil
	}
}

// immBytes is an immediate of the operand's full width: 16 bits for a
// word, 32 for a dword and for a qword, which sign-extends it.
func immBytes(v int64, size int) []byte {
	switch size {
	case 1:
		return []byte{byte(v)}
	case 2:
		return le16(v)
	default:
		return le32(v)
	}
}

// operandSize is the width of an r/m operand: a register's own, or the
// size keyword a memory operand was written with.
func operandSize(op operand) int {
	if op.kind == opReg {
		return op.reg.size
	}
	return op.size
}

// sizePrefix sets the operand-size parts of e for a general-purpose
// operation of the given width.
func (e *encoding) sizePrefix(size int) {
	switch size {
	case 2:
		e.prefixes = append(e.prefixes, 0x66)
	case 8:
		e.rexW = true
	}
}

// setRM puts an r/m operand in ModRM.rm.
func (e *encoding) setRM(op operand) {
	e.hasModRM = true
	if op.kind == opReg {
		e.rmReg = op.reg.num
		e.noteRegs(op.reg)
		return
	}
	m := op.mem
	e.rmMem = &m
}

func (e *encoding) setReg(r reg) {
	e.hasModRM = true
	e.regField = r.num
	e.noteRegs(r)
}

func isGP(op operand) bool  { return op.kind == opReg && !op.reg.xmm() }
func isXMM(op operand) bool { return op.kind == opReg && op.reg.xmm() }
func isRM(op operand) bool  { return isGP(op) || op.kind == opMem }

// sameSize checks a register against an r/m operand that may carry its
// own size keyword.
func sameSize(r reg, op operand) error {
	if size := operandSize(op); size != 0 && size != r.size {
		return fmt.Errorf("operand sizes differ")
	}
	return nil
}

// encodeInst encodes every instruction except a jump to a label, whose
// size the assembler chooses.
func encodeInst(prefix, mnemonic string, ops []operand) (encoded, error) {
	e, err := encodeForm(prefix, mnemonic, ops)
	if err != nil {
		return encoded{}, err
	}
	return e.build()
}

func encodeForm(prefix, mnemonic string, ops []operand) (*encoding, error) {
	e := &encoding{opcodeReg: -1}
	switch prefix {
	case "rep":
		switch mnemonic {
		case "movsb", "stosb", "movsq", "stosq":
		default:
			return nil, fmt.Errorf("rep does not apply to %s", mnemonic)
		}
	case "lock":
		// Only the read-modify-write operations on memory are locked.
		if (mnemonic != "xadd" && mnemonic != "cmpxchg") || len(ops) != 2 || ops[0].kind != opMem {
			return nil, fmt.Errorf("lock does not apply to %s", mnemonic)
		}
		e.prefixes = []byte{0xf0}
	}
	unsupported := func() (*encoding, error) {
		return nil, fmt.Errorf("unsupported operands for %s", mnemonic)
	}
	n := len(ops)

	if code, ok := aluOps[mnemonic]; ok {
		if n != 2 {
			return unsupported()
		}
		return e, e.alu(code, ops[0], ops[1])
	}
	if code, ok := shiftOps[mnemonic]; ok {
		if n != 2 || !isRM(ops[0]) {
			return unsupported()
		}
		size := operandSize(ops[0])
		if size == 0 {
			return nil, fmt.Errorf("%s needs an operand size", mnemonic)
		}
		e.sizePrefix(size)
		e.setRM(ops[0])
		e.regField = int(code)
		wide := byte(1)
		if size == 1 {
			wide = 0
		}
		switch {
		case ops[1].kind == opReg && ops[1].reg == registers["cl"]:
			e.opcode = []byte{0xd2 | wide}
		case ops[1].kind == opImm && ops[1].imm == 1:
			e.opcode = []byte{0xd0 | wide}
		case ops[1].kind == opImm && ops[1].imm >= 0 && ops[1].imm <= 255:
			e.opcode = []byte{0xc0 | wide}
			e.imm = []byte{byte(ops[1].imm)}
		default:
			return unsupported()
		}
		return e, nil
	}
	if code, ok := unaryOps[mnemonic]; ok {
		if n != 1 || !isRM(ops[0]) {
			return unsupported()
		}
		return e, e.unary(code, ops[0])
	}
	if code, ok := bitTestOps[mnemonic]; ok {
		if n != 2 || !isRM(ops[0]) {
			return unsupported()
		}
		size := operandSize(ops[0])
		if size < 2 {
			return nil, fmt.Errorf("%s needs a word, dword or qword", mnemonic)
		}
		e.sizePrefix(size)
		e.setRM(ops[0])
		switch {
		case ops[1].kind == opImm && ops[1].imm >= 0 && ops[1].imm <= 255:
			e.opcode = []byte{0x0f, 0xba}
			e.regField = int(code)
			e.imm = []byte{byte(ops[1].imm)}
		case isGP(ops[1]) && ops[1].reg.size == size:
			e.opcode = []byte{0x0f, 0x83 | (code-4)<<3}
			e.setReg(ops[1].reg)
		default:
			return unsupported()
		}
		return e, nil
	}
	if code, ok := sseOps[mnemonic]; ok {
		if n != 2 || !isXMM(ops[0]) || !(isXMM(ops[1]) || ops[1].kind == opMem) {
			return unsupported()
		}
		if ops[1].kind == opMem && ops[1].size != 0 && ops[1].size != sseSizes[mnemonic] {
			return nil, fmt.Errorf("operand sizes differ")
		}
		e.sse(code[0], code[1], ops[0].reg, ops[1])
		return e, nil
	}
	if cc, ok := setcc(mnemonic); ok {
		if n != 1 || !isRM(ops[0]) || operandSize(ops[0]) != 1 {
			return unsupported()
		}
		e.opcode = []byte{0x0f, 0x90 | cc}
		e.setRM(ops[0])
		return e, nil
	}

	switch mnemonic {
	case "mov":
		if n != 2 {
			return unsupported()
		}
		return e, e.mov(ops[0], ops[1])
	case "test":
		if n != 2 {
			return unsupported()
		}
		return e, e.test(ops[0], ops[1])
	case "lea":
		if n != 2 || !isGP(ops[0]) || ops[0].reg.size < 2 || ops[1].kind != opMem {
			return unsupported()
		}
		e.sizePrefix(ops[0].reg.size)
		e.opcode = []byte{0x8d}
		e.setReg(ops[0].reg)
		e.setRM(ops[1])
		return e, nil
	case "xchg", "xadd", "cmpxchg":
		// The memory form, whose first operand is the location: xchg locks
		// it by itself, the other two under a lock prefix.
		if n != 2 || ops[0].kind != opMem || !isGP(ops[1]) {
			return unsupported()
		}
		if err := sameSize(ops[1].reg, ops[0]); err != nil {
			return nil, err
		}
		size := ops[1].reg.size
		e.sizePrefix(size)
		switch mnemonic {
		case "xchg":
			e.opcode = []byte{0x86 | wideBit(size)}
		case "xadd":
			e.opcode = []byte{0x0f, 0xc0 | wideBit(size)}
		default:
			e.opcode = []byte{0x0f, 0xb0 | wideBit(size)}
		}
		e.setRM(ops[0])
		e.setReg(ops[1].reg)
		return e, nil
	case "movzx", "movsx":
		if n != 2 || !isGP(ops[0]) || !isRM(ops[1]) {
			return unsupported()
		}
		from := operandSize(ops[1])
		if from != 1 && from != 2 || ops[0].reg.size <= from {
			return nil, fmt.Errorf("%s needs a byte or word source narrower than its destination", mnemonic)
		}
		if mnemonic == "movsx" && isGP(ops[1]) && ops[0].reg.num == 0 && ops[1].reg.num == 0 && ops[0].reg.size == 2*from {
			// nasm writes the accumulator doubling its own width as cbw
			// or cwde.
			e.sizePrefix(ops[0].reg.size)
			e.opcode = []byte{0x98}
			return e, nil
		}
		e.sizePrefix(ops[0].reg.size)
		op := byte(0xb6)
		if mnemonic == "movsx" {
			op = 0xbe
		}
		if from == 2 {
			op++
		}
		e.opcode = []byte{0x0f, op}
		e.setReg(ops[0].reg)
		e.setRM(ops[1])
		return e, nil
	case "movsxd":
		if n != 2 || !isGP(ops[0]) || ops[0].reg.size != 8 || !isRM(ops[1]) || operandSize(ops[1]) != 4 {
			return unsupported()
		}
		e.rexW = true
		if isGP(ops[1]) && ops[0].reg.num == 0 && ops[1].reg.num == 0 {
			// nasm writes movsxd rax, eax as cdqe.
			e.opcode = []byte{0x98}
			return e, nil
		}
		e.opcode = []byte{0x63}
		e.setReg(ops[0].reg)
		e.setRM(ops[1])
		return e, nil
	case "imul":
		return e, e.imul(ops)
	case "push", "pop":
		if n != 1 || !isGP(ops[0]) || ops[0].reg.size != 8 {
			return unsupported()
		}
		base := byte(0x50)
		if mnemonic == "pop" {
			base = 0x58
		}
		e.opcode = []byte{base}
		e.inOpcode(ops[0].reg)
		return e, nil
	case "call", "jmp":
		// A label operand is handled by the caller; this is the indirect
		// form through a register or memory.
		if n != 1 || !isRM(ops[0]) || (ops[0].kind == opReg && ops[0].reg.size != 8) {
			return unsupported()
		}
		e.opcode = []byte{0xff}
		e.setRM(ops[0])
		e.regField = 2
		if mnemonic == "jmp" {
			e.regField = 4
		}
		return e, nil
	case "movsd", "movss":
		if n != 2 {
			return unsupported()
		}
		prefix, size := byte(0xf2), 8
		if mnemonic == "movss" {
			prefix, size = 0xf3, 4
		}
		switch {
		case isXMM(ops[0]) && (isXMM(ops[1]) || ops[1].kind == opMem):
			if ops[1].kind == opMem && ops[1].size != 0 && ops[1].size != size {
				return nil, fmt.Errorf("operand sizes differ")
			}
			e.sse(prefix, 0x10, ops[0].reg, ops[1])
		case ops[0].kind == opMem && isXMM(ops[1]):
			if ops[0].size != 0 && ops[0].size != size {
				return nil, fmt.Errorf("operand sizes differ")
			}
			e.sse(prefix, 0x11, ops[1].reg, ops[0])
		default:
			return unsupported()
		}
		return e, nil
	case "movq", "movd":
		if n != 2 {
			return unsupported()
		}
		size := 8
		if mnemonic == "movd" {
			size = 4
		}
		switch {
		case isXMM(ops[0]) && isRM(ops[1]) && (operandSize(ops[1]) == size || ops[1].kind == opMem && ops[1].size == 0):
			e.sse(0x66, 0x6e, ops[0].reg, ops[1])
		case isRM(ops[0]) && isXMM(ops[1]) && (operandSize(ops[0]) == size || ops[0].kind == opMem && ops[0].size == 0):
			e.sse(0x66, 0x7e, ops[1].reg, ops[0])
		default:
			return unsupported()
		}
		e.rexW = size == 8
		return e, nil
	case "cvtsi2sd", "cvtsi2ss":
		if n != 2 || !isXMM(ops[0]) || !isRM(ops[1]) {
			return unsupported()
		}
		size := operandSize(ops[1])
		if size != 4 && size != 8 {
			return nil, fmt.Errorf("%s needs a dword or qword source", mnemonic)
		}
		prefix := byte(0xf2)
		if mnemonic == "cvtsi2ss" {
			prefix = 0xf3
		}
		e.sse(prefix, 0x2a, ops[0].reg, ops[1])
		e.rexW = size == 8
		return e, nil
	case "cvttsd2si", "cvtsd2si", "cvttss2si", "cvtss2si":
		if n != 2 || !isGP(ops[0]) || ops[0].reg.size < 4 || !(isXMM(ops[1]) || ops[1].kind == opMem) {
			return unsupported()
		}
		prefix := byte(0xf2)
		if mnemonic == "cvttss2si" || mnemonic == "cvtss2si" {
			prefix = 0xf3
		}
		op := byte(0x2c)
		if mnemonic == "cvtsd2si" || mnemonic == "cvtss2si" {
			op = 0x2d
		}
		e.sse(prefix, op, ops[0].reg, ops[1])
		e.rexW = ops[0].reg.size == 8
		return e, nil
	case "roundsd", "roundss":
		if n != 3 || !isXMM(ops[0]) || !(isXMM(ops[1]) || ops[1].kind == opMem) || ops[2].kind != opImm || ops[2].imm < 0 || ops[2].imm > 255 {
			return unsupported()
		}
		e.prefixes = []byte{0x66}
		e.opcode = []byte{0x0f, 0x3a, 0x0b}
		if mnemonic == "roundss" {
			e.opcode[2] = 0x0a
		}
		e.setReg(ops[0].reg)
		e.setRM(ops[1])
		e.imm = []byte{byte(ops[2].imm)}
		return e, nil
	}

	if n != 0 {
		return unsupported()
	}
	fixed := map[string][]byte{
		"ret": {0xc3}, "leave": {0xc9}, "cqo": {0x48, 0x99}, "cdq": {0x99},
		"syscall": {0x0f, 0x05}, "int3": {0xcc}, "nop": {0x90}, "ud2": {0x0f, 0x0b},
		"movsb": {0xa4}, "stosb": {0xaa}, "movsq": {0x48, 0xa5}, "stosq": {0x48, 0xab},
		"mfence": {0x0f, 0xae, 0xf0},
	}
	code, ok := fixed[mnemonic]
	if !ok {
		return nil, fmt.Errorf("unknown instruction %s", mnemonic)
	}
	if prefix == "rep" {
		e.prefixes = []byte{0xf3}
	}
	e.opcode = code
	return e, nil
}

// setcc reports whether the mnemonic is a setCC and which condition.
func setcc(mnemonic string) (byte, bool) {
	if len(mnemonic) < 4 || mnemonic[:3] != "set" {
		return 0, false
	}
	cc, ok := conditions[mnemonic[3:]]
	return cc, ok
}

// jump reports whether the mnemonic is jmp or a jCC, and which condition;
// -1 is the unconditional jump.
func jump(mnemonic string) (int, bool) {
	if mnemonic == "jmp" {
		return -1, true
	}
	if len(mnemonic) < 2 || mnemonic[0] != 'j' {
		return 0, false
	}
	cc, ok := conditions[mnemonic[1:]]
	return int(cc), ok
}

// sse encodes xmm, xmm/m with a mandatory prefix: the prefix comes
// before REX, the opcode after 0F.
func (e *encoding) sse(prefix, op byte, r reg, rm operand) {
	if prefix != 0 {
		e.prefixes = append(e.prefixes, prefix)
	}
	e.opcode = []byte{0x0f, op}
	e.setReg(r)
	e.setRM(rm)
}

// alu encodes the add/or/adc/sbb/and/sub/xor/cmp group, choosing the
// shortest form as nasm does: an 8-bit immediate when one sign-extends
// to the value, then the accumulator's own opcode, then the general one.
func (e *encoding) alu(code byte, dst, src operand) error {
	switch {
	case isRM(dst) && isGP(src):
		if err := sameSize(src.reg, dst); err != nil {
			return err
		}
		e.sizePrefix(src.reg.size)
		e.opcode = []byte{code<<3 | wideBit(src.reg.size)}
		e.setRM(dst)
		e.setReg(src.reg)
	case isGP(dst) && src.kind == opMem:
		if err := sameSize(dst.reg, src); err != nil {
			return err
		}
		e.sizePrefix(dst.reg.size)
		e.opcode = []byte{code<<3 | 2 | wideBit(dst.reg.size)}
		e.setReg(dst.reg)
		e.setRM(src)
	case isRM(dst) && src.kind == opImm:
		size := operandSize(dst)
		if size == 0 {
			return fmt.Errorf("the operation needs an operand size")
		}
		v, err := immediate(src.imm, size)
		if err != nil {
			return err
		}
		e.sizePrefix(size)
		accumulator := dst.kind == opReg && dst.reg.num == 0
		switch {
		case size == 1 && accumulator:
			e.opcode = []byte{code<<3 | 4}
			e.imm = []byte{byte(v)}
		case size == 1:
			e.opcode = []byte{0x80}
			e.setRM(dst)
			e.regField = int(code)
			e.imm = []byte{byte(v)}
		case fitsInt8(v):
			e.opcode = []byte{0x83}
			e.setRM(dst)
			e.regField = int(code)
			e.imm = []byte{byte(v)}
		case accumulator:
			e.opcode = []byte{code<<3 | 5}
			e.imm = immBytes(v, size)
		default:
			e.opcode = []byte{0x81}
			e.setRM(dst)
			e.regField = int(code)
			e.imm = immBytes(v, size)
		}
	default:
		return fmt.Errorf("unsupported operands")
	}
	return nil
}

func wideBit(size int) byte {
	if size == 1 {
		return 0
	}
	return 1
}

func (e *encoding) unary(code byte, op operand) error {
	size := operandSize(op)
	if size == 0 {
		return fmt.Errorf("the operation needs an operand size")
	}
	e.sizePrefix(size)
	e.opcode = []byte{0xf6 | wideBit(size)}
	e.setRM(op)
	e.regField = int(code)
	return nil
}

func (e *encoding) test(dst, src operand) error {
	// test is symmetric; the register goes in ModRM.reg whichever side
	// it was written on.
	if isGP(dst) && src.kind == opMem {
		dst, src = src, dst
	}
	switch {
	case isRM(dst) && isGP(src):
		if err := sameSize(src.reg, dst); err != nil {
			return err
		}
		e.sizePrefix(src.reg.size)
		e.opcode = []byte{0x84 | wideBit(src.reg.size)}
		e.setRM(dst)
		e.setReg(src.reg)
	case isRM(dst) && src.kind == opImm:
		size := operandSize(dst)
		if size == 0 {
			return fmt.Errorf("test needs an operand size")
		}
		v, err := immediate(src.imm, size)
		if err != nil {
			return err
		}
		e.sizePrefix(size)
		if dst.kind == opReg && dst.reg.num == 0 {
			e.opcode = []byte{0xa8 | wideBit(size)}
		} else {
			e.opcode = []byte{0xf6 | wideBit(size)}
			e.setRM(dst)
			e.regField = 0
		}
		e.imm = immBytes(v, size)
	default:
		return fmt.Errorf("unsupported operands")
	}
	return nil
}

// mov picks nasm's encodings: register to register is the 89 form, and a
// 64-bit immediate takes the shortest of a zero-extending 32-bit move, a
// sign-extended 32-bit one and the full ten-byte form.
func (e *encoding) mov(dst, src operand) error {
	switch {
	case isRM(dst) && isGP(src):
		if err := sameSize(src.reg, dst); err != nil {
			return err
		}
		e.sizePrefix(src.reg.size)
		e.opcode = []byte{0x88 | wideBit(src.reg.size)}
		e.setRM(dst)
		e.setReg(src.reg)
	case isGP(dst) && src.kind == opMem:
		if err := sameSize(dst.reg, src); err != nil {
			return err
		}
		e.sizePrefix(dst.reg.size)
		e.opcode = []byte{0x8a | wideBit(dst.reg.size)}
		e.setReg(dst.reg)
		e.setRM(src)
	case isGP(dst) && src.kind == opImm:
		r := dst.reg
		v := src.imm
		switch {
		case r.size == 8 && v >= 0 && v <= math.MaxUint32:
			e.opcode = []byte{0xb8}
			e.inOpcode(r)
			e.imm = le32(v)
		case r.size == 8 && fitsInt32(v):
			e.rexW = true
			e.opcode = []byte{0xc7}
			e.setRM(dst)
			e.imm = le32(v)
		case r.size == 8:
			e.rexW = true
			e.opcode = []byte{0xb8}
			e.inOpcode(r)
			e.imm = le64(v)
		default:
			v, err := immediate(v, r.size)
			if err != nil {
				return err
			}
			e.sizePrefix(r.size)
			e.opcode = []byte{0xb8}
			if r.size == 1 {
				e.opcode = []byte{0xb0}
			}
			e.inOpcode(r)
			e.imm = immBytes(v, r.size)
		}
	case dst.kind == opMem && src.kind == opImm:
		if dst.size == 0 {
			return fmt.Errorf("mov needs an operand size")
		}
		v, err := immediate(src.imm, dst.size)
		if err != nil {
			return err
		}
		e.sizePrefix(dst.size)
		e.opcode = []byte{0xc6 | wideBit(dst.size)}
		e.setRM(dst)
		e.imm = immBytes(v, dst.size)
	default:
		return fmt.Errorf("unsupported operands")
	}
	return nil
}

// inOpcode puts a register in the low three bits of the opcode, the
// form with no ModRM; its fourth bit is REX.B.
func (e *encoding) inOpcode(r reg) {
	last := len(e.opcode) - 1
	e.opcode[last] |= byte(r.num & 7)
	e.opcodeReg = r.num
	e.noteRegs(r)
}

func (e *encoding) imul(ops []operand) error {
	switch {
	case len(ops) == 1 && isRM(ops[0]):
		return e.unary(5, ops[0])
	case len(ops) == 2 && ops[1].kind == opImm:
		return e.imul([]operand{ops[0], ops[0], ops[1]})
	case len(ops) == 2 && isGP(ops[0]) && isRM(ops[1]):
		if ops[0].reg.size < 2 {
			return fmt.Errorf("imul needs a word, dword or qword")
		}
		if err := sameSize(ops[0].reg, ops[1]); err != nil {
			return err
		}
		e.sizePrefix(ops[0].reg.size)
		e.opcode = []byte{0x0f, 0xaf}
		e.setReg(ops[0].reg)
		e.setRM(ops[1])
	case len(ops) == 3 && isGP(ops[0]) && isRM(ops[1]) && ops[2].kind == opImm:
		size := ops[0].reg.size
		if size < 2 {
			return fmt.Errorf("imul needs a word, dword or qword")
		}
		if err := sameSize(ops[0].reg, ops[1]); err != nil {
			return err
		}
		v, err := immediate(ops[2].imm, size)
		if err != nil {
			return err
		}
		e.sizePrefix(size)
		e.setReg(ops[0].reg)
		e.setRM(ops[1])
		if fitsInt8(v) {
			e.opcode = []byte{0x6b}
			e.imm = []byte{byte(v)}
		} else {
			e.opcode = []byte{0x69}
			e.imm = immBytes(v, size)
		}
	default:
		return fmt.Errorf("unsupported operands for imul")
	}
	return nil
}
