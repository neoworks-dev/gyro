package x86

import (
	"fmt"
	"strconv"
	"strings"
)

// The text the backend writes is a small subset of NASM: one statement
// per line, a `;` comment to the end of the line, and `%line` for the
// debugger. A line parses into one of these.
type stmtKind int

const (
	stmtLabel stmtKind = iota
	stmtInst
	stmtSection
	stmtGlobal
	stmtExtern
	stmtData  // db, dq
	stmtSpace // resb
	stmtAlign
)

type stmt struct {
	kind stmtKind
	line int // physical line, for errors
	pos  position

	name     string    // label, section, global or extern name
	mnemonic string    // instruction, or db/dq
	prefix   string    // "rep"
	operands []operand // instruction operands and data items
	count    int64     // resb size or align boundary

	// sizeOf is a global's `:function (end - start)`: the two labels
	// whose distance is the symbol's size.
	function  bool
	sizeEnd   string
	sizeStart string

	// data is a global's `:data`, a variable rather than code.
	data bool

	// options are what follows a section's name: `nobits alloc noexec
	// write align=8 tls`.
	options []string
}

// position is where the line table credits a line: the source file and
// line a %line directive names, or the assembly's own line without one.
type position struct {
	file string
	line int
}

type operandKind int

const (
	opReg operandKind = iota
	opMem
	opImm
	opSym
)

// operand is one instruction operand or data item. A symbol may carry an
// addend: `dq S_3` and `[M_m+16]` both reference a label at an offset.
type operand struct {
	kind operandKind
	reg  reg
	mem  mem
	imm  int64
	sym  string
	size int // an explicit byte/word/dword/qword, 0 when none was written
}

// mem is a memory operand: base + index*scale + disp, or a symbol plus
// disp, which `default rel` makes relative to the instruction pointer.
type mem struct {
	base, index int // -1 when absent
	scale       int
	disp        int64
	sym         string

	// gottpoff is `[rel sym wrt ..gottpoff]`: the symbol's offset in the
	// thread-local block, read from the global offset table.
	gottpoff bool

	// segment is the override byte of `[fs:0]`, 0 when there is none.
	segment byte
}

type reg struct {
	num  int  // 0-15
	size int  // 1, 2, 4, 8, or 16 for an xmm register
	low  bool // spl, bpl, sil or dil, which exist only under a REX prefix
}

func (r reg) xmm() bool { return r.size == 16 }

var registers = map[string]reg{}

func init() {
	names64 := []string{"rax", "rcx", "rdx", "rbx", "rsp", "rbp", "rsi", "rdi"}
	names32 := []string{"eax", "ecx", "edx", "ebx", "esp", "ebp", "esi", "edi"}
	names16 := []string{"ax", "cx", "dx", "bx", "sp", "bp", "si", "di"}
	names8 := []string{"al", "cl", "dl", "bl", "spl", "bpl", "sil", "dil"}
	for i := range 8 {
		registers[names64[i]] = reg{num: i, size: 8}
		registers[names32[i]] = reg{num: i, size: 4}
		registers[names16[i]] = reg{num: i, size: 2}
		registers[names8[i]] = reg{num: i, size: 1, low: i >= 4}
	}
	for i := 8; i < 16; i++ {
		n := strconv.Itoa(i)
		registers["r"+n] = reg{num: i, size: 8}
		registers["r"+n+"d"] = reg{num: i, size: 4}
		registers["r"+n+"w"] = reg{num: i, size: 2}
		registers["r"+n+"b"] = reg{num: i, size: 1}
	}
	for i := range 16 {
		registers["xmm"+strconv.Itoa(i)] = reg{num: i, size: 16}
	}
}

var sizeKeywords = map[string]int{"byte": 1, "word": 2, "dword": 4, "qword": 8}

// parse reads the whole text. name is what a line no %line covers is
// credited to, the way nasm credits its input file.
func parse(source, name string) ([]stmt, error) {
	var stmts []stmt
	// The line table's position: without a %line it is the physical
	// line; after `%line N+M file` the next line is N and each one after
	// it adds M.
	file, base, step, from := name, 0, 0, 0
	lined := false

	lines := strings.Split(source, "\n")
	for i, text := range lines {
		physical := i + 1
		pos := position{file: name, line: physical}
		if lined {
			pos = position{file: file, line: base + (physical-from-1)*step}
		}

		trimmed := strings.TrimSpace(text)
		if rest, ok := strings.CutPrefix(trimmed, "%line "); ok {
			f, b, s, err := parseLineDirective(rest)
			if err != nil {
				return nil, fmt.Errorf("x86: line %d: %v", physical, err)
			}
			file, base, step, from, lined = f, b, s, physical, true
			continue
		}
		if at := strings.IndexByte(trimmed, ';'); at >= 0 {
			trimmed = strings.TrimSpace(trimmed[:at])
		}
		if trimmed == "" {
			continue
		}
		parsed, err := parseStatement(trimmed)
		if err != nil {
			return nil, fmt.Errorf("x86: line %d: %v: %s", physical, err, trimmed)
		}
		for j := range parsed {
			parsed[j].line = physical
			parsed[j].pos = pos
		}
		stmts = append(stmts, parsed...)
	}
	return stmts, nil
}

// parseLineDirective reads `N+M "file"`. The file is quoted the way Go
// quotes, which is how the backend writes it.
func parseLineDirective(rest string) (string, int, int, error) {
	numbers, quoted, ok := strings.Cut(rest, " ")
	if !ok {
		return "", 0, 0, fmt.Errorf("%%line needs a file")
	}
	first, second, ok := strings.Cut(numbers, "+")
	if !ok {
		second = "1"
	}
	line, err1 := strconv.Atoi(first)
	step, err2 := strconv.Atoi(second)
	if err1 != nil || err2 != nil {
		return "", 0, 0, fmt.Errorf("malformed %%line %q", numbers)
	}
	file, err := strconv.Unquote(strings.TrimSpace(quoted))
	if err != nil {
		return "", 0, 0, fmt.Errorf("malformed %%line file %s", quoted)
	}
	return file, line, step, nil
}

func parseStatement(text string) ([]stmt, error) {
	// A label may share its line with what follows it.
	if word, rest, found := cutWord(text); found && strings.HasSuffix(word, ":") && isSymbol(word[:len(word)-1]) {
		out := []stmt{{kind: stmtLabel, name: word[:len(word)-1]}}
		if rest = strings.TrimSpace(rest); rest != "" {
			more, err := parseStatement(rest)
			if err != nil {
				return nil, err
			}
			out = append(out, more...)
		}
		return out, nil
	}

	word, rest, _ := cutWord(text)
	rest = strings.TrimSpace(rest)
	switch word {
	case "default":
		if rest != "rel" {
			return nil, fmt.Errorf("only `default rel` is supported")
		}
		return nil, nil
	case "section":
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return nil, fmt.Errorf("section needs a name")
		}
		return []stmt{{kind: stmtSection, name: fields[0], options: fields[1:]}}, nil
	case "extern":
		if !isSymbol(rest) {
			return nil, fmt.Errorf("malformed extern")
		}
		return []stmt{{kind: stmtExtern, name: rest}}, nil
	case "global":
		return parseGlobal(rest)
	case "align":
		n, err := parseInt(rest)
		if err != nil || n <= 0 || n&(n-1) != 0 {
			return nil, fmt.Errorf("align needs a power of two")
		}
		return []stmt{{kind: stmtAlign, count: n}}, nil
	case "resb":
		n, err := parseInt(rest)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("resb needs a size")
		}
		return []stmt{{kind: stmtSpace, count: n}}, nil
	case "db", "dq":
		var items []operand
		for _, field := range strings.Split(rest, ",") {
			item, err := parseOperand(strings.TrimSpace(field))
			if err != nil {
				return nil, err
			}
			if item.kind != opImm && item.kind != opSym {
				return nil, fmt.Errorf("%s takes numbers and symbols", word)
			}
			if item.kind == opSym && word == "db" {
				return nil, fmt.Errorf("db cannot hold an address")
			}
			items = append(items, item)
		}
		return []stmt{{kind: stmtData, mnemonic: word, operands: items}}, nil
	}

	s := stmt{kind: stmtInst, mnemonic: word}
	if word == "rep" || word == "lock" {
		s.prefix = word
		s.mnemonic, rest, _ = cutWord(rest)
		rest = strings.TrimSpace(rest)
	}
	if rest != "" {
		for _, field := range strings.Split(rest, ",") {
			op, err := parseOperand(strings.TrimSpace(field))
			if err != nil {
				return nil, err
			}
			s.operands = append(s.operands, op)
		}
	}
	return []stmt{s}, nil
}

// parseGlobal reads `name` or `name:function (end - start)`.
func parseGlobal(rest string) ([]stmt, error) {
	name, kind, typed := strings.Cut(rest, ":")
	if !isSymbol(name) {
		return nil, fmt.Errorf("malformed global")
	}
	s := stmt{kind: stmtGlobal, name: name}
	if !typed {
		return []stmt{s}, nil
	}
	kind, size, _ := strings.Cut(kind, " ")
	switch kind {
	case "data":
		if strings.TrimSpace(size) != "" {
			return nil, fmt.Errorf("a data symbol takes no size")
		}
		s.data = true
		return []stmt{s}, nil
	case "function":
	default:
		return nil, fmt.Errorf("unsupported symbol type %q", kind)
	}
	s.function = true
	size = strings.TrimSpace(size)
	if size == "" {
		return []stmt{s}, nil
	}
	inner, ok := strings.CutPrefix(size, "(")
	inner, ok2 := strings.CutSuffix(inner, ")")
	end, start, ok3 := strings.Cut(inner, "-")
	end, start = strings.TrimSpace(end), strings.TrimSpace(start)
	if !ok || !ok2 || !ok3 || !isSymbol(end) || !isSymbol(start) {
		return nil, fmt.Errorf("a symbol's size must be (end - start)")
	}
	s.sizeEnd, s.sizeStart = end, start
	return []stmt{s}, nil
}

func cutWord(text string) (string, string, bool) {
	at := strings.IndexAny(text, " \t")
	if at < 0 {
		return text, "", true
	}
	return text[:at], text[at:], true
}

func parseOperand(text string) (operand, error) {
	if text == "" {
		return operand{}, fmt.Errorf("missing operand")
	}
	size := 0
	if word, rest, _ := cutWord(text); sizeKeywords[word] != 0 {
		size = sizeKeywords[word]
		text = strings.TrimSpace(rest)
	}
	if strings.HasPrefix(text, "[") {
		if !strings.HasSuffix(text, "]") {
			return operand{}, fmt.Errorf("unclosed memory operand")
		}
		m, err := parseMem(text[1 : len(text)-1])
		if err != nil {
			return operand{}, err
		}
		return operand{kind: opMem, mem: m, size: size}, nil
	}
	if size != 0 {
		return operand{}, fmt.Errorf("a size keyword belongs on a memory operand")
	}
	if r, ok := registers[text]; ok {
		return operand{kind: opReg, reg: r}, nil
	}
	if n, err := parseInt(text); err == nil {
		return operand{kind: opImm, imm: n}, nil
	}
	sym, addend, err := parseSymbolExpr(text)
	if err != nil {
		return operand{}, err
	}
	return operand{kind: opSym, sym: sym, imm: addend}, nil
}

// parseSymbolExpr reads `sym`, `sym+N` or `sym-N`.
func parseSymbolExpr(text string) (string, int64, error) {
	at := strings.IndexAny(text, "+-")
	name, addend := text, int64(0)
	if at > 0 {
		name = strings.TrimSpace(text[:at])
		n, err := parseInt(strings.TrimSpace(text[at+1:]))
		if err != nil {
			return "", 0, fmt.Errorf("malformed operand %q", text)
		}
		addend = n
		if text[at] == '-' {
			addend = -n
		}
	}
	if !isSymbol(name) {
		return "", 0, fmt.Errorf("malformed operand %q", text)
	}
	return name, addend, nil
}

// parseMem reads the inside of brackets: a sum of at most one base
// register, one index register (optionally scaled), numbers and one
// symbol. The first unscaled register is the base, as nasm takes it.
func parseMem(text string) (mem, error) {
	m := mem{base: -1, index: -1, scale: 1}
	if rest, ok := strings.CutSuffix(strings.TrimSpace(text), " wrt ..gottpoff"); ok {
		rest, ok = strings.CutPrefix(rest, "rel ")
		if !ok {
			return m, fmt.Errorf("wrt ..gottpoff needs rel")
		}
		sym := strings.TrimSpace(rest)
		if !isSymbol(sym) {
			return m, fmt.Errorf("wrt ..gottpoff needs a symbol")
		}
		m.sym, m.gottpoff = sym, true
		return m, nil
	}
	text = strings.ReplaceAll(text, " ", "")
	if seg, rest, ok := strings.Cut(text, ":"); ok {
		if seg != "fs" {
			return m, fmt.Errorf("only the fs segment override is supported")
		}
		m.segment, text = 0x64, rest
	}
	if text == "" {
		return m, fmt.Errorf("empty memory operand")
	}
	sign := int64(1)
	for len(text) > 0 {
		end := strings.IndexAny(text[1:], "+-") + 1
		if end == 0 {
			end = len(text)
		}
		term := text[:end]
		switch term[0] {
		case '+':
			sign, term = 1, term[1:]
		case '-':
			sign, term = -1, term[1:]
		}
		text = text[end:]
		if term == "" {
			return m, fmt.Errorf("malformed memory operand")
		}

		regName, scaleText, scaled := strings.Cut(term, "*")
		if r, ok := registers[regName]; ok {
			if sign < 0 || r.size != 8 {
				return m, fmt.Errorf("an address register must be a positive 64-bit register")
			}
			scale := int64(1)
			if scaled {
				n, err := parseInt(scaleText)
				if err != nil || (n != 1 && n != 2 && n != 4 && n != 8) {
					return m, fmt.Errorf("scale must be 1, 2, 4 or 8")
				}
				scale = n
			}
			switch {
			case !scaled && m.base < 0:
				m.base = r.num
			case m.index < 0:
				m.index, m.scale = r.num, int(scale)
			default:
				return m, fmt.Errorf("too many registers in an address")
			}
			continue
		}
		if n, err := parseInt(term); err == nil {
			m.disp += sign * n
			continue
		}
		if !isSymbol(term) || sign < 0 || m.sym != "" {
			return m, fmt.Errorf("malformed memory operand term %q", term)
		}
		m.sym = term
	}
	if m.sym != "" && (m.base >= 0 || m.index >= 0) {
		return m, fmt.Errorf("a symbol address cannot also use registers")
	}
	if m.segment != 0 && m.sym != "" {
		return m, fmt.Errorf("a segment override takes a number or registers, not a symbol")
	}
	if m.index == 4 {
		return m, fmt.Errorf("rsp cannot be an index register")
	}
	return m, nil
}

// parseInt reads a decimal or 0x number. A value past int64 wraps, the
// way nasm reads an unsigned 64-bit constant.
func parseInt(text string) (int64, error) {
	negative := false
	if rest, ok := strings.CutPrefix(text, "-"); ok {
		negative, text = true, rest
	}
	if text == "" || text[0] < '0' || text[0] > '9' {
		return 0, fmt.Errorf("not a number")
	}
	n, err := strconv.ParseUint(text, 0, 64)
	if err != nil {
		return 0, err
	}
	if negative {
		if n > 1<<63 {
			return 0, fmt.Errorf("number out of range")
		}
		return -int64(n), nil
	}
	return int64(n), nil
}

func isSymbol(name string) bool {
	if name == "" {
		return false
	}
	for i, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch == '_', ch == '.', ch == '?', ch == '$', ch == '@':
		case ch >= '0' && ch <= '9' && i > 0:
		default:
			return false
		}
	}
	if _, isReg := registers[name]; isReg {
		return false
	}
	return true
}
