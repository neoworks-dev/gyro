package nasm

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// memLoc is an addressable location: a displacement off a base register.
type memLoc struct {
	base string
	off  int
}

func (m memLoc) String() string {
	if m.off == 0 {
		return "[" + m.base + "]"
	}
	if m.off < 0 {
		return fmt.Sprintf("[%s-%d]", m.base, -m.off)
	}
	return fmt.Sprintf("[%s+%d]", m.base, m.off)
}

func (m memLoc) plus(delta int) memLoc {
	return memLoc{base: m.base, off: m.off + delta}
}

// loc is where a value lives: a machine register the allocator handed
// it, an immediate the instruction carries, or memory. Formatting a loc
// yields the operand in all three cases, so an emitter whose instruction
// takes any of them needs no case analysis; the ones that need an
// address, an operand size keyword, an interior offset or a modifiable
// destination ask for it and fail on the forms that cannot supply it.
type loc struct {
	reg   string // "" when the value is not in a machine register
	mem   memLoc
	imm   int64
	isImm bool
}

func regAt(machine string) loc { return loc{reg: machine} }
func memAt(m memLoc) loc       { return loc{mem: m} }
func immAt(value int64) loc    { return loc{imm: value, isImm: true} }

func (l loc) isReg() bool { return l.reg != "" }

func (l loc) String() string {
	switch {
	case l.reg != "":
		return l.reg
	case l.isImm:
		return strconv.FormatInt(l.imm, 10)
	}
	return l.mem.String()
}

// plus addresses a word inside an aggregate, which only memory has.
func (l loc) plus(delta int) loc {
	if l.reg != "" || l.isImm {
		failf("a value in %s has no interior offset", l)
	}
	return loc{mem: l.mem.plus(delta)}
}

// address returns the location to hand a `lea`.
func (l loc) address() memLoc {
	if l.reg != "" || l.isImm {
		failf("a value in %s has no address", l)
	}
	return l.mem
}

// sized prefixes an operand-size keyword, which NASM needs on memory and
// rejects on a register. An immediate has no size of its own, so an
// instruction that needs one has to materialize it first.
func (l loc) sized(keyword string) string {
	if l.isImm {
		failf("the immediate %s carries no operand size", l)
	}
	if l.reg != "" {
		return l.reg
	}
	return keyword + " " + l.mem.String()
}

// frame emits one function. Every register owns a stack slot, and the
// allocator may give the hottest of them a machine register to live in
// instead; the slot at rbp-8 holds the incoming sret pointer, rbp-16 is
// scratch.
type frame struct {
	g       *generator
	fn      *metals.Func
	types   map[string]metals.Type
	offsets map[string]int // register -> rbp displacement of slot start
	areas   map[string]int // Slot instruction dst -> rbp displacement
	// anyArgs gives each any.call site the stack it gathers its
	// arguments into, since the runtime walks them as an array.
	anyArgs map[*metals.AnyCall]int
	consts  map[string]int64
	homes   map[string]string
	saved   []string       // callee-saved registers the homes occupy
	savedAt map[string]int // machine register -> rbp displacement
	// stageAt is the one word a narrow scalar passes through on its way
	// to or from the runtime, which stores in storage representation.
	stageAt int
	size    int
	labels  map[string]string // METALS label -> asm jump target
	panics  map[string]string // abort message -> local label
}

const sretSlot = -8
const scratchSlot = -16

func (g *generator) genFunc(fn *metals.Func) {
	f := &frame{
		g:       g,
		fn:      fn,
		types:   g.regTypes[fn.Name],
		offsets: map[string]int{},
		areas:   map[string]int{},
		anyArgs: map[*metals.AnyCall]int{},
		savedAt: map[string]int{},
		labels:  map[string]string{},
		panics:  map[string]string{},
	}
	f.consts = g.immediates(fn)
	alloc := g.allocate(fn, f.types, f.consts)
	f.homes, f.saved = alloc.homes, alloc.saved
	f.layoutFrame()

	g.text.WriteString("\n")
	g.comment("fn @%s", fn.Name)
	// The prologue belongs to the line the function is declared on, so
	// the position goes before it rather than with the first statement.
	if len(fn.Body) > 0 {
		if loc, ok := fn.Body[0].(*metals.Loc); ok {
			g.mark(loc)
		}
	}
	// A debugger names a frame by the symbol nearest below its pc, and
	// every local label is one. A function that says how long it is is
	// the symbol that covers its own labels.
	symbol := g.symbolFor(fn.Name)
	g.text.WriteString("global " + symbol + ":function (" + symbol + ".end - " + symbol + ")\n")
	g.label(symbol)
	g.ins("push rbp")
	g.ins("mov rbp, rsp")
	g.ins("sub rsp, %d", f.size)
	f.saveCallee()
	f.spillParams()
	// main is the entry point itself in this backend, so "before
	// anything runs" is the top of its frame.
	if fn.Name == "main" {
		g.genMemoryInit()
	}
	f.body(fn.Body)

	// The lowering always terminates a body; this backstop only runs if
	// a void function's last path falls through.
	f.restoreCallee()
	g.ins("leave")
	g.ins("ret")
	f.emitPanicLabels()
	g.label(symbol + ".end")
}

// saveCallee parks the callee-saved registers the homes took over. They
// go to dedicated frame slots rather than the stack so that the frame
// size, and with it rsp's alignment, stays what layoutFrame computed.
func (f *frame) saveCallee() {
	for _, machine := range f.saved {
		f.g.ins("mov %s, %s", memLoc{"rbp", f.savedAt[machine]}, machine)
	}
}

func (f *frame) restoreCallee() {
	for _, machine := range f.saved {
		f.g.ins("mov %s, %s", machine, memLoc{"rbp", f.savedAt[machine]})
	}
}

// layoutFrame assigns every register a slot and every `slot` instruction
// an area, in sorted order so output is deterministic.
func (f *frame) layoutFrame() {
	current := 16 // sret + scratch

	for _, machine := range f.saved {
		current += wordSize
		f.savedAt[machine] = -current
	}

	current += wordSize
	f.stageAt = -current

	// Every allocation is rounded to a whole word, so that a cell whose
	// size is not one — a two-byte record, an array of bytes — leaves the
	// next one word-aligned. Nothing is aligned more than a word, so that
	// rounding is the whole of the alignment this frame owes.
	names := make([]string, 0, len(f.types))
	for name := range f.types {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		current += words(f.g.slotOf(f.types[name]))
		f.offsets[name] = -current
	}

	walkInstrs(f.fn.Body, func(instr metals.Instr) {
		switch instr := instr.(type) {
		case *metals.Slot:
			if _, seen := f.areas[instr.Dst]; seen {
				return
			}
			current += words(f.g.sizeOf(instr.Type))
			f.areas[instr.Dst] = -current
		case *metals.AnyCall:
			// One argument array per site, so a call inside a loop
			// reuses its own space instead of allocating.
			current += max(len(instr.Args), 1) * 2 * wordSize
			f.anyArgs[instr] = -current
		}
	})

	f.size = (current + 15) / 16 * 16
}

// words rounds a size up to a whole number of words, which is the
// granularity a frame hands out.
func words(size int) int { return (size + wordSize - 1) / wordSize * wordSize }

// walkInstrs visits every instruction, structured bodies included.
func walkInstrs(body []metals.Instr, visit func(metals.Instr)) {
	for _, instr := range body {
		visit(instr)
		switch i := instr.(type) {
		case *metals.If:
			walkInstrs(i.Then, visit)
			walkInstrs(i.Else, visit)
		case *metals.Block:
			walkInstrs(i.Body, visit)
		case *metals.Loop:
			walkInstrs(i.Body, visit)
		case *metals.Switch:
			for _, c := range i.Cases {
				walkInstrs(c.Body, visit)
			}
			walkInstrs(i.Default, visit)
		case *metals.Select:
			for _, arm := range i.Arms {
				walkInstrs(arm.Body, visit)
			}
			walkInstrs(i.Default, visit)
		}
	}
}

// slot is where a METALS register's value lives.
func (f *frame) slot(reg string) loc {
	if value, ok := f.consts[reg]; ok {
		return immAt(value)
	}
	if home, ok := f.homes[reg]; ok {
		return regAt(home)
	}
	offset, ok := f.offsets[reg]
	if !ok {
		f.g.fail("@%s: register %%%s has no slot", f.fn.Name, reg)
	}
	return memAt(memLoc{base: "rbp", off: offset})
}

// addr is the frame slot of a register the emitter needs an address of.
// Neither a home nor an immediate is offered to one of those, so
// reaching a folded register here is a missing entry in the roles table,
// not a legal program.
func (f *frame) addr(reg string) memLoc {
	value := f.slot(reg)
	if value.isReg() || value.isImm {
		f.g.fail("@%s: register %%%s lives in %s and has no address", f.fn.Name, reg, value)
	}
	return value.address()
}

func (f *frame) typeOf(reg string) metals.Type {
	t, ok := f.types[reg]
	if !ok {
		f.g.fail("@%s: register %%%s has no type", f.fn.Name, reg)
	}
	return t
}

// ---------------------------------------------------------------------------
// Parameters
// ---------------------------------------------------------------------------

var intArgRegs = []string{"rdi", "rsi", "rdx", "rcx", "r8", "r9"}
var sseArgRegs = []string{"xmm0", "xmm1", "xmm2", "xmm3", "xmm4", "xmm5", "xmm6", "xmm7"}

// spillParams saves incoming arguments into their slots. Aggregates
// arrive as pointers; their pointer parks at the slot start and a second
// pass expands it in place, reading the pointer before overwriting it.
func (f *frame) spillParams() {
	g := f.g
	intIndex, sseIndex, stackOffset := 0, 0, 16
	if g.classOf(resultOrNil(f.fn)) == classMem {
		g.ins("mov %s, rdi", memLoc{"rbp", sretSlot})
		intIndex = 1
	}

	type expand struct {
		slot     memLoc
		size     int
		stackSrc int // 0 when the pointer was parked in the slot
	}
	var expansions []expand

	for _, param := range f.fn.Params {
		class := g.classOf(param.Type)
		size := g.sizeOf(param.Type)

		switch {
		case class == classSSE && sseIndex < len(sseArgRegs):
			g.ins("movsd %s, %s", f.slot(param.Name), sseArgRegs[sseIndex])
			sseIndex++
		case class == classInt && intIndex < len(intArgRegs):
			g.ins("mov %s, %s", f.slot(param.Name), intArgRegs[intIndex])
			intIndex++
		case class == classMem && intIndex < len(intArgRegs):
			slot := f.addr(param.Name)
			g.ins("mov %s, %s", slot, intArgRegs[intIndex])
			intIndex++
			expansions = append(expansions, expand{slot: slot, size: size})
		default:
			if class == classMem {
				expansions = append(expansions, expand{slot: f.addr(param.Name), size: size, stackSrc: stackOffset})
			} else {
				f.copyMem(f.slot(param.Name), memAt(memLoc{"rbp", stackOffset}), wordSize)
			}
			stackOffset += 8
		}
	}

	for _, e := range expansions {
		if e.stackSrc != 0 {
			g.ins("mov rsi, %s", memLoc{"rbp", e.stackSrc})
		} else {
			g.ins("mov rsi, %s", e.slot)
		}
		f.copyMem(memAt(e.slot), memAt(memLoc{base: "rsi"}), e.size)
	}
}

func resultOrNil(fn *metals.Func) metals.Type {
	if fn.Result == nil {
		return metals.TBool // any scalar: "no sret needed"
	}
	return fn.Result
}

// ---------------------------------------------------------------------------
// Moves
// ---------------------------------------------------------------------------

// unrollWords is the aggregate size, in words, up to which a move is
// written out word by word. `rep movsq` costs tens of cycles before it
// moves anything, so it only wins on the aggregates nobody writes.
const unrollWords = 8

// chunks is the move a byte count is spent in, widest first. A C-shaped
// aggregate is a multiple of its own alignment and nothing more, so a
// three-byte record is three bytes and the tail has to be moved as one.
var chunks = []struct {
	size    int
	keyword string
	reg     string
}{
	{8, "qword", "rax"},
	{4, "dword", "eax"},
	{2, "word", "ax"},
	{1, "byte", "al"},
}

// The moves below belong to the generator rather than to a frame: the
// async thunks are emitted without one and move the same bytes. Every
// frame forwards to them, which is what keeps the call sites reading as
// f.copyMem.

func (f *frame) copyMem(dst, src loc, size int)       { f.g.copyMem(dst, src, size) }
func (f *frame) zeroMem(dst loc, size int)            { f.g.zeroMem(dst, size) }
func (f *frame) storeImm(dst loc, v int64, at string) { f.g.storeImm(dst, v, at) }
func (f *frame) storeWord(dst loc, value int64)       { f.g.storeWord(dst, value) }
func (f *frame) loadWork(machine string, src loc)     { f.g.loadWork(machine, src) }
func (f *frame) storeWork(dst loc, machine string)    { f.g.storeWork(dst, machine) }
func (f *frame) inReg(value loc, scratch string) loc  { return f.g.inReg(value, scratch) }

func (f *frame) loadMember(dst, src loc, t metals.Type)  { f.g.loadMember(dst, src, t) }
func (f *frame) storeMember(dst, src loc, t metals.Type) { f.g.storeMember(dst, src, t) }

// copyMem copies size bytes. Clobbers rax, and rsi/rdi/rcx past
// unrollWords. Base registers of dst and src must survive the clobbers
// (rbp always does; rdx and r10 are safe). Anything wider than a word is
// an aggregate, which never has a home.
func (g *generator) copyMem(dst, src loc, size int) {
	if dst == src {
		// The lowering emits `%x = copy %x` after mutating in place.
		return
	}
	if size == wordSize {
		switch {
		case src.isImm:
			g.storeImm(dst, src.imm, "")
		case dst.isReg() || src.isReg():
			g.ins("mov %s, %s", dst, src)
		default:
			g.ins("mov rax, %s", src)
			g.ins("mov %s, rax", dst)
		}
		return
	}
	from, to := src.address(), dst.address()
	if size > unrollWords*wordSize {
		g.ins("lea rsi, %s", from)
		g.ins("lea rdi, %s", to)
		g.ins("mov rcx, %d", size)
		g.ins("rep movsb")
		return
	}
	at := 0
	for _, chunk := range chunks {
		for size-at >= chunk.size {
			g.ins("mov %s, %s %s", chunk.reg, chunk.keyword, from.plus(at))
			g.ins("mov %s %s, %s", chunk.keyword, to.plus(at), chunk.reg)
			at += chunk.size
		}
	}
}

// zeroMem clears size bytes. Clobbers rax, and rdi/rcx past unrollWords.
func (g *generator) zeroMem(dst loc, size int) {
	if dst.isReg() {
		g.ins("mov %s, 0", dst)
		return
	}
	if size > unrollWords*wordSize {
		g.ins("lea rdi, %s", dst.mem)
		g.ins("xor eax, eax")
		g.ins("mov rcx, %d", size)
		g.ins("rep stosb")
		return
	}
	at := 0
	for _, chunk := range chunks {
		for size-at >= chunk.size {
			g.ins("mov %s %s, 0", chunk.keyword, dst.mem.plus(at))
			at += chunk.size
		}
	}
}

// storeWord writes an immediate that fits 32 bits to a whole word.
func (g *generator) storeWord(dst loc, value int64) {
	g.ins("mov %s, %d", dst.sized("qword"), value)
}

// ---------------------------------------------------------------------------
// Working registers
// ---------------------------------------------------------------------------

// narrowRegs names the 8-, 16- and 32-bit views of every register an
// operation may compute in.
var narrowRegs = map[string][3]string{
	"rax": {"al", "ax", "eax"},
	"rbx": {"bl", "bx", "ebx"},
	"rcx": {"cl", "cx", "ecx"},
	"rdx": {"dl", "dx", "edx"},
	"r8":  {"r8b", "r8w", "r8d"},
	"r12": {"r12b", "r12w", "r12d"},
	"r13": {"r13b", "r13w", "r13d"},
	"r14": {"r14b", "r14w", "r14d"},
	"r15": {"r15b", "r15w", "r15d"},
}

func narrow(machine string, bits int) string {
	names, ok := narrowRegs[machine]
	if !ok {
		failf("no %d-bit view of %s", bits, machine)
	}
	switch bits {
	case 8:
		return names[0]
	case 16:
		return names[1]
	}
	return names[2]
}

// work picks the register an operation computes in. A destination that
// has a home computes in it, so the result stays where it belongs
// instead of travelling out through rax and back; an operand still
// needed by the operation rules its own home out.
func (f *frame) work(dst loc, operands ...loc) string {
	if !dst.isReg() {
		return "rax"
	}
	for _, operand := range operands {
		if operand.String() == dst.reg {
			return "rax"
		}
	}
	return dst.reg
}

func (g *generator) loadWork(machine string, src loc) {
	if src.String() != machine {
		g.ins("mov %s, %s", machine, src)
	}
}

func (g *generator) storeWork(dst loc, machine string) {
	if dst.String() != machine {
		g.ins("mov %s, %s", dst, machine)
	}
}

// inReg yields a register operand for a value, materializing it in
// scratch only when it is not already in a register of its own.
func (g *generator) inReg(value loc, scratch string) loc {
	if value.isReg() {
		return value
	}
	g.ins("mov %s, %s", scratch, value)
	return regAt(scratch)
}

// leaInto computes an address straight into the destination's home,
// through rax only when it has none.
func (f *frame) leaInto(dst loc, address string) {
	out := "rax"
	if dst.isReg() {
		out = dst.reg
	}
	f.g.ins("lea %s, %s", out, address)
	f.storeWork(dst, out)
}

// testZero sets ZF from a whole word in place: jz takes the zero branch.
func (f *frame) testZero(value loc) {
	if value.isImm {
		value = f.inReg(value, "rax")
	}
	if value.isReg() {
		f.g.ins("test %s, %s", value, value)
		return
	}
	f.g.ins("cmp %s, 0", value.sized("qword"))
}

// ---------------------------------------------------------------------------
// Aborts
// ---------------------------------------------------------------------------

// panicLabel returns a local label that aborts with the message; the
// bodies collect at the end of the function.
func (f *frame) panicLabel(message string) string {
	if label, ok := f.panics[message]; ok {
		return label
	}
	label := f.g.newLabel()
	f.panics[message] = label
	return label
}

func (f *frame) emitPanicLabels() {
	g := f.g
	messages := make([]string, 0, len(f.panics))
	for message := range f.panics {
		messages = append(messages, message)
	}
	sort.Strings(messages)
	for _, message := range messages {
		g.label(f.panics[message])
		f.emitAbort(message)
	}
}

func (f *frame) emitAbort(message string) {
	g := f.g
	g.ins("lea rdi, [%s]", g.strLabel(message))
	g.ins("call %s", g.extern("gy_abort"))
}

// ---------------------------------------------------------------------------
// Instructions
// ---------------------------------------------------------------------------

func (f *frame) body(instrs []metals.Instr) {
	for _, instr := range instrs {
		f.instr(instr)
	}
}

func (f *frame) instr(instr metals.Instr) {
	g := f.g
	switch i := instr.(type) {
	case *metals.Const:
		f.emitConst(i)

	case *metals.Copy:
		if _, folded := f.consts[i.Dst]; !folded {
			f.copyMem(f.slot(i.Dst), f.slot(i.X), g.slotOf(f.typeOf(i.Dst)))
		}

	case *metals.Bin:
		f.emitBin(i)

	case *metals.Un:
		f.emitUn(i)

	case *metals.Conv:
		f.emitConv(i)

	case *metals.Slot:
		area, ok := f.areas[i.Dst]
		if !ok {
			g.fail("@%s: slot %%%s has no area", f.fn.Name, i.Dst)
		}
		f.zeroMem(memAt(memLoc{"rbp", area}), g.sizeOf(i.Type))
		f.leaInto(f.slot(i.Dst), memLoc{"rbp", area}.String())

	case *metals.GlobalPtr:
		f.leaInto(f.slot(i.Dst), "["+g.globalSymbol(i.Name)+"]")

	case *metals.New:
		g.ins("mov rdi, %d", g.sizeOf(i.Type))
		g.ins("call %s", g.extern("gy_alloc"))
		g.ins("mov %s, rax", f.slot(i.Dst))

	case *metals.AnyWrap:
		f.emitAnyWrap(i)

	case *metals.AnyUnwrap:
		f.emitAnyUnwrap(i)

	case *metals.AnyIs:
		f.emitAnyIs(i)

	case *metals.AnyGet:
		f.emitAnyGet(i)

	case *metals.AnySet:
		f.emitAnySet(i)

	case *metals.AnyCall:
		f.emitAnyCall(i)

	case *metals.Free:
		g.ins("mov rdi, %s", f.slot(i.X))
		g.ins("call %s", g.extern("gy_free"))

	case *metals.Load:
		g.ins("mov rdx, %s", f.slot(i.Ptr))
		f.loadMember(f.slot(i.Dst), memAt(memLoc{base: "rdx"}), f.typeOf(i.Dst))

	case *metals.Store:
		g.ins("mov rdx, %s", f.slot(i.Ptr))
		f.storeMember(memAt(memLoc{base: "rdx"}), f.slot(i.Val), f.typeOf(i.Val))

	case *metals.FieldPtr:
		// A union's members all begin where the union does, so the
		// offset is zero and the pointer is the same address — which is
		// what sharing storage means at this level.
		offset := g.layout.MemberOffset(f.declBehindPtr(i.Ptr), i.Index)
		if offset == 0 {
			f.copyMem(f.slot(i.Dst), f.slot(i.Ptr), wordSize)
			return
		}
		base := f.inReg(f.slot(i.Ptr), "rax")
		f.leaInto(f.slot(i.Dst), fmt.Sprintf("[%s+%d]", base, offset))

	case *metals.ElemPtr:
		// An array's storage starts at its first element, so the decay
		// is the same address with a different type over it.
		f.copyMem(f.slot(i.Dst), f.slot(i.Ptr), wordSize)

	case *metals.PtrOffset:
		size := g.sizeOf(f.typeOf(i.Ptr).(*metals.Ptr).Elem)
		g.ins("mov rax, %s", f.slot(i.Ptr))
		g.ins("mov rcx, %s", f.slot(i.N))
		g.ins("imul rcx, rcx, %d", size)
		g.ins("add rax, rcx")
		f.storeWork(f.slot(i.Dst), "rax")

	case *metals.PtrErase, *metals.PtrCast, *metals.PtrAddr, *metals.PtrFrom:
		// All four are moves. Erasing a type, putting one back, and
		// reading an address as a number or a number as an address change
		// what the format will let the next instruction do with the word,
		// and nothing about the word.
		var dst, src string
		switch i := instr.(type) {
		case *metals.PtrErase:
			dst, src = i.Dst, i.X
		case *metals.PtrCast:
			dst, src = i.Dst, i.X
		case *metals.PtrAddr:
			dst, src = i.Dst, i.X
		case *metals.PtrFrom:
			dst, src = i.Dst, i.X
		}
		f.copyMem(f.slot(dst), f.slot(src), wordSize)

	case *metals.PtrDiff:
		size := g.sizeOf(f.typeOf(i.A).(*metals.Ptr).Elem)
		g.ins("mov rax, %s", f.slot(i.A))
		g.ins("sub rax, %s", f.slot(i.B))
		g.ins("mov rcx, %d", size)
		g.ins("cqo")
		g.ins("idiv rcx")
		f.storeWork(f.slot(i.Dst), "rax")

	case *metals.MakeRecord:
		record := g.types[i.TypeName].Record
		dst := f.slot(i.Dst)
		for index, arg := range i.Args {
			field := record.Fields[index].Type
			f.storeMember(dst.plus(g.fieldOffset(record, index)), f.slot(arg), field)
		}

	case *metals.FieldGet:
		record := f.recordOf(i.X)
		field := record.Fields[i.Index].Type
		f.loadMember(f.slot(i.Dst), f.slot(i.X).plus(g.fieldOffset(record, i.Index)), field)

	case *metals.FieldSet:
		record := f.recordOf(i.X)
		field := record.Fields[i.Index].Type
		f.storeMember(f.slot(i.X).plus(g.fieldOffset(record, i.Index)), f.slot(i.Val), field)

	case *metals.MakeVariant:
		enum := g.types[i.TypeName].Enum
		dst := f.slot(i.Dst)
		f.zeroMem(dst, g.namedSize(i.TypeName))
		f.storeWord(dst, int64(i.Variant))
		for index, arg := range i.Args {
			payload := enum.Variants[i.Variant].Payload[index]
			f.storeMember(dst.plus(g.variantFieldOffset(enum, i.Variant, index)), f.slot(arg), payload)
		}

	case *metals.VariantTag:
		// The tag is the enum's first word.
		f.copyMem(f.slot(i.Dst), f.slot(i.X), wordSize)

	case *metals.VariantField:
		enum := f.enumOf(i.X)
		payload := enum.Variants[i.Variant].Payload[i.Index]
		offset := g.variantFieldOffset(enum, i.Variant, i.Index)
		f.loadMember(f.slot(i.Dst), f.slot(i.X).plus(offset), payload)

	case *metals.MakeTuple:
		tuple := f.tupleOf(i.Dst)
		dst := f.slot(i.Dst)
		for index, arg := range i.Args {
			f.storeMember(dst.plus(g.tupleOffset(tuple, index)), f.slot(arg), tuple.Elems[index])
		}

	case *metals.TupleGet:
		tuple := f.tupleOf(i.X)
		offset := g.tupleOffset(tuple, i.Index)
		f.loadMember(f.slot(i.Dst), f.slot(i.X).plus(offset), tuple.Elems[i.Index])

	case *metals.MakeArray:
		stride := g.sizeOf(i.Elem)
		dst := f.slot(i.Dst)
		for index, arg := range i.Args {
			f.storeMember(dst.plus(index*stride), f.slot(arg), i.Elem)
		}

	case *metals.Slice:
		var length int64
		switch t := f.typeOf(i.X).(type) {
		case *metals.ArrayOf:
			length = int64(t.Len)
			g.ins("lea rax, %s", f.addr(i.X))
		case *metals.Ptr:
			array, ok := t.Elem.(*metals.ArrayOf)
			if !ok {
				g.fail("@%s: slice.of through a pointer to %s", f.fn.Name, t.Elem)
			}
			// The storage is where the pointer says, so the address is
			// the register's value rather than its own cell.
			length = int64(array.Len)
			g.ins("mov rax, %s", f.slot(i.X))
		default:
			g.fail("@%s: slice.of a %s", f.fn.Name, t)
		}
		g.ins("mov %s, rax", f.slot(i.Dst))
		f.storeWord(f.slot(i.Dst).plus(8), length)

	case *metals.SliceFrom:
		g.ins("mov rax, %s", f.slot(i.Ptr))
		g.ins("mov rcx, %s", f.slot(i.Len))
		g.ins("mov %s, rax", f.slot(i.Dst))
		g.ins("mov %s, rcx", f.slot(i.Dst).plus(8))

	case *metals.SliceSub:
		f.emitSliceSub(i)

	case *metals.Syscall:
		// Linux takes the number in rax and the arguments in rdi, rsi,
		// rdx, r10, r8 and r9, and the instruction overwrites rcx and
		// r11. None of them is ever a register's home (only the
		// callee-saved ones are), so every operand loads straight from
		// where it lives. A register holds a scalar widened to a qword,
		// so a narrow integer is already the word the kernel reads.
		for index, arg := range i.Args {
			g.ins("mov %s, %s", syscallRegs[index], f.slot(arg))
		}
		g.ins("mov rax, %s", f.slot(i.Number))
		g.ins("syscall")
		g.ins("mov %s, rax", f.slot(i.Dst))

	case *metals.AtomicLoad, *metals.AtomicStore, *metals.AtomicRMW, *metals.AtomicCas, *metals.Fence:
		f.emitAtomic(instr)

	case *metals.IndexGet:
		element := f.indexedElement(i.X, i.Index)
		f.loadMember(f.slot(i.Dst), element, f.typeOf(i.Dst))

	case *metals.IndexSet:
		element := f.indexedElement(i.X, i.Index)
		f.storeMember(element, f.slot(i.Val), f.typeOf(i.Val))

	case *metals.IndexPtr:
		// The same check and the same address arithmetic index.get does;
		// what differs is that the address is the result rather than a
		// place to load from.
		element := f.indexedElement(i.Ptr, i.Index)
		g.ins("lea rax, %s", element)
		f.storeWork(f.slot(i.Dst), "rax")

	case *metals.Len:
		f.emitLen(i)

	case *metals.StrConcat:
		g.ins("mov rdi, %s", f.slot(i.A))
		g.ins("mov rsi, %s", f.slot(i.B))
		g.ins("call %s", g.extern("gy_str_concat"))
		g.ins("mov %s, rax", f.slot(i.Dst))

	case *metals.Call:
		f.emitCall(i)

	case *metals.FnRef:
		if g.funcs[i.Callee] == nil {
			g.fail("@%s: fn.ref of the runtime function @%s is not supported yet", f.fn.Name, i.Callee)
		}
		g.ins("lea rax, [%s]", g.symbolFor(i.Callee))
		g.ins("mov %s, rax", f.slot(i.Dst))
		f.storeWord(f.slot(i.Dst).plus(8), 0)

	case *metals.CodeRef:
		if g.funcs[i.Callee] == nil {
			g.fail("@%s: code.ref of the runtime function @%s is not supported", f.fn.Name, i.Callee)
		}
		g.ins("lea rax, [%s]", g.symbolFor(i.Callee))
		g.ins("mov %s, rax", f.slot(i.Dst))

	case *metals.MakeClosure:
		if g.funcs[i.Callee] == nil {
			g.fail("@%s: closure over the runtime function @%s is not supported", f.fn.Name, i.Callee)
		}
		g.ins("lea rax, [%s]", g.symbolFor(i.Callee))
		g.ins("mov %s, rax", f.slot(i.Dst))
		g.ins("mov rax, %s", f.slot(i.Env))
		g.ins("mov %s, rax", f.slot(i.Dst).plus(8))

	case *metals.CallIndirect:
		f.emitCallIndirect(i)

	case *metals.Return:
		f.emitReturn(i)

	case *metals.Loc:
		g.mark(i)

	case *metals.Abort:
		f.emitAbort(i.Message)

	case *metals.If:
		f.emitIf(i)

	case *metals.Block:
		end := g.newLabel()
		f.withLabel(i.Label, end, i.Body)
		g.label(end)

	case *metals.Loop:
		top := g.newLabel()
		g.label(top)
		f.withLabel(i.Label, top, i.Body)

	case *metals.Br:
		g.ins("jmp %s", f.jumpTarget(i.Label))

	case *metals.BrIf:
		f.testZero(f.slot(i.Cond))
		g.ins("jnz %s", f.jumpTarget(i.Label))

	case *metals.Switch:
		f.emitSwitch(i)

	case *metals.ChanNew:
		if i.Cap == "" {
			g.ins("xor edi, edi")
		} else {
			g.ins("mov rdi, %s", f.slot(i.Cap))
		}
		g.ins("mov rsi, %d", g.sizeOf(i.Elem))
		g.ins("call %s", g.extern("gy_chan_new"))
		g.ins("mov %s, rax", f.slot(i.Dst))

	case *metals.Send:
		element := f.elementOut(i.Val)
		g.ins("mov rdi, %s", f.slot(i.Ch))
		g.ins("lea rsi, %s", element)
		g.ins("call %s", g.extern("gy_chan_send"))

	case *metals.Recv:
		f.emitRecv(i)

	case *metals.ChanClose:
		g.ins("mov rdi, %s", f.slot(i.Ch))
		g.ins("call %s", g.extern("gy_chan_close"))

	case *metals.Await:
		g.ins("mov rdi, %s", f.slot(i.X))
		g.ins("lea rsi, %s", f.elementIn(i.Dst))
		g.ins("call %s", g.extern("gy_future_get"))
		f.elementBack(i.Dst)

	case *metals.Select:
		f.emitSelect(i)

	case *metals.OptWrap:
		opt, ok := f.typeOf(i.Dst).(*metals.Opt)
		if !ok {
			g.fail("@%s: opt.wrap into a non-optional", f.fn.Name)
		}
		if metals.NicheOpt(opt) {
			f.copyMem(f.slot(i.Dst), f.slot(i.X), wordSize)
			break
		}
		f.storeWord(f.slot(i.Dst), 1)
		f.storeMember(f.slot(i.Dst).plus(wordSize), f.slot(i.X), opt.Elem)

	case *metals.OptUnwrap:
		// Zero is the absent case either way: a niche optional is the null
		// pointer itself, and a flagged one keeps its flag in the first word.
		f.testZero(f.slot(i.X))
		g.ins("jz %s", f.panicLabel("unwrap of a null value"))
		if opt, ok := f.typeOf(i.X).(*metals.Opt); ok && metals.NicheOpt(opt) {
			f.copyMem(f.slot(i.Dst), f.slot(i.X), wordSize)
			break
		}
		f.loadMember(f.slot(i.Dst), f.slot(i.X).plus(wordSize), f.typeOf(i.Dst))

	case *metals.Trunc:
		f.emitReinterpret(i.Dst, i.X, i.To)

	case *metals.Bitcast:
		f.emitReinterpret(i.Dst, i.X, i.To)

	default:
		// The linear-memory instructions carry their own dispatch, so the
		// byte-level machinery stays in memory.go.
		if !f.emitMemory(instr) {
			g.fail("@%s: the native backend does not implement %T yet", f.fn.Name, instr)
		}
	}
}

// ---------------------------------------------------------------------------
// Control flow
// ---------------------------------------------------------------------------

func (f *frame) withLabel(name, target string, body []metals.Instr) {
	saved, had := f.labels[name]
	f.labels[name] = target
	f.body(body)
	if had {
		f.labels[name] = saved
	} else {
		delete(f.labels, name)
	}
}

func (f *frame) jumpTarget(label string) string {
	target, ok := f.labels[label]
	if !ok {
		f.g.fail("@%s: br to unknown label $%s", f.fn.Name, label)
	}
	return target
}

func (f *frame) emitIf(i *metals.If) {
	g := f.g
	f.testZero(f.slot(i.Cond))
	end := g.newLabel()
	if len(i.Else) == 0 {
		g.ins("jz %s", end)
		f.body(i.Then)
		g.label(end)
		return
	}
	otherwise := g.newLabel()
	g.ins("jz %s", otherwise)
	f.body(i.Then)
	g.ins("jmp %s", end)
	g.label(otherwise)
	f.body(i.Else)
	g.label(end)
}

func (f *frame) emitSwitch(i *metals.Switch) {
	g := f.g
	// The case bodies come after every comparison, so a scrutinee that
	// has to be loaded is loaded once.
	x := f.inReg(f.slot(i.X), "rax")
	end := g.newLabel()
	caseLabels := make([]string, len(i.Cases))
	for index, c := range i.Cases {
		caseLabels[index] = g.newLabel()
		g.ins("cmp %s, %d", x, c.Value)
		g.ins("je %s", caseLabels[index])
	}
	defaultLabel := end
	if len(i.Default) > 0 {
		defaultLabel = g.newLabel()
	}
	g.ins("jmp %s", defaultLabel)
	for index, c := range i.Cases {
		g.label(caseLabels[index])
		f.body(c.Body)
		g.ins("jmp %s", end)
	}
	if len(i.Default) > 0 {
		g.label(defaultLabel)
		f.body(i.Default)
	}
	g.label(end)
}

func (f *frame) emitReturn(i *metals.Return) {
	g := f.g
	if i.Val != "" {
		result := f.typeOf(i.Val)
		switch g.classOf(result) {
		case classSSE:
			g.ins("movsd xmm0, %s", f.slot(i.Val))
		case classInt:
			g.ins("mov rax, %s", f.slot(i.Val))
		case classMem:
			g.ins("mov rdx, %s", memLoc{"rbp", sretSlot})
			f.copyMem(memAt(memLoc{base: "rdx"}), f.slot(i.Val), g.sizeOf(result))
			g.ins("mov rax, rdx")
		}
	}
	f.restoreCallee()
	g.ins("leave")
	g.ins("ret")
}

// ---------------------------------------------------------------------------
// Indexing and lengths
// ---------------------------------------------------------------------------

// indexedElement bounds-checks and leaves the element address in rdx,
// returned as a location. Clobbers rax, rcx, rdx.
func (f *frame) indexedElement(x, index string) loc {
	g := f.g
	var elemSize int
	var length string // an operand cmp and mov both take: rcx or a literal
	switch t := f.typeOf(x).(type) {
	case *metals.SliceOf:
		elemSize = g.sizeOf(t.Elem)
		g.ins("mov rdx, %s", f.slot(x))
		g.ins("mov rcx, %s", f.slot(x).plus(8))
		length = "rcx"
	case *metals.ArrayOf:
		elemSize = g.sizeOf(t.Elem)
		g.ins("lea rdx, %s", f.addr(x))
		length = strconv.FormatInt(t.Len, 10)
	case *metals.Ptr:
		// index.ptr's base: the array is where the pointer says, so the
		// address is the register's value rather than its slot, and the
		// extent is the static one the pointee type carries.
		array, ok := t.Elem.(*metals.ArrayOf)
		if !ok {
			g.fail("@%s: indexing through a pointer to %s", f.fn.Name, t.Elem)
		}
		elemSize = g.sizeOf(array.Elem)
		g.ins("mov rdx, %s", f.slot(x))
		length = strconv.FormatInt(array.Len, 10)
	default:
		g.fail("@%s: indexing a %s", f.fn.Name, t)
	}

	at := f.inReg(f.slot(index), "rax")
	g.ins("cmp %s, %s", at, length)
	ok := g.newLabel()
	g.ins("jb %s", ok)
	g.ins("mov rdi, %s", at)
	g.ins("mov rsi, %s", length)
	g.ins("call %s", g.extern("gy_abort_bounds"))
	g.label(ok)
	if elemSize == wordSize {
		g.ins("lea rdx, [rdx+%s*%d]", at, elemSize)
	} else {
		g.ins("imul rax, %s, %d", at, elemSize)
		g.ins("add rdx, rax")
	}
	return memAt(memLoc{base: "rdx"})
}

// emitSliceSub narrows a slice onto the storage it already views: the
// data pointer moves forward by lo elements and the length becomes
// hi - lo. Clobbers rax, rcx, rdx, rsi, rdi.
//
// The check is two unsigned compares, so a bound that would be negative
// as a signed value is a huge unsigned one and fails the same test. The
// abort still prints it signed, which is what indexing already does.
func (f *frame) emitSliceSub(i *metals.SliceSub) {
	g := f.g
	slice, ok := f.typeOf(i.X).(*metals.SliceOf)
	if !ok {
		g.fail("@%s: slice.sub of a %s", f.fn.Name, f.typeOf(i.X))
	}

	g.ins("mov rax, %s", f.slot(i.Lo))
	g.ins("mov rsi, %s", f.slot(i.Hi))
	g.ins("mov rcx, %s", f.slot(i.X).plus(8))

	fail, done := g.newLabel(), g.newLabel()
	g.ins("cmp rax, rsi")
	g.ins("ja %s", fail)
	g.ins("cmp rsi, rcx")
	g.ins("jbe %s", done)
	g.label(fail)
	g.ins("mov rdi, rax")
	g.ins("mov rdx, rcx")
	g.ins("call %s", g.extern("gy_abort_slice"))
	g.label(done)

	g.ins("mov rdx, %s", f.slot(i.X))
	g.ins("sub rsi, rax")
	if stride := g.sizeOf(slice.Elem); stride != 1 {
		g.ins("imul rax, rax, %d", stride)
	}
	g.ins("add rdx, rax")

	dst := f.slot(i.Dst)
	g.ins("mov %s, rdx", dst)
	g.ins("mov %s, rsi", dst.plus(8))
}

func (f *frame) emitLen(i *metals.Len) {
	g := f.g
	dst := f.slot(i.Dst)
	switch t := f.typeOf(i.X).(type) {
	case *metals.SliceOf:
		f.copyMem(dst, f.slot(i.X).plus(8), wordSize)
	case *metals.ArrayOf:
		f.storeImm(dst, t.Len, "len")
	case *metals.Prim:
		if t.Kind != metals.Str {
			g.fail("@%s: len of a %s", f.fn.Name, t)
		}
		// A string header starts with its length.
		work := f.work(dst)
		f.loadWork(work, f.slot(i.X))
		g.ins("mov %s, [%s]", work, work)
		f.storeWork(dst, work)
	default:
		g.fail("@%s: len of a %s", f.fn.Name, t)
	}
}

// ---------------------------------------------------------------------------
// Channels and select
// ---------------------------------------------------------------------------

// emitRecv fills the tuple<T, bool> a receive answers with. The element
// lands in the tuple's own first field, which is already in storage
// representation, so nothing is staged; the open flag is a bool and is
// stored as the byte one is.
func (f *frame) emitRecv(i *metals.Recv) {
	g := f.g
	tuple := f.tupleOf(i.Dst)
	f.zeroMem(f.slot(i.Dst), g.sizeOf(tuple))
	g.ins("mov rdi, %s", f.slot(i.Ch))
	g.ins("lea rsi, %s", f.addr(i.Dst))
	g.ins("call %s", g.extern("gy_chan_recv"))
	f.storeMember(f.slot(i.Dst).plus(g.tupleOffset(tuple, 1)), regAt("rax"), tuple.Elems[1])
}

func (f *frame) emitSelect(i *metals.Select) {
	g := f.g
	top := g.newLabel()
	end := g.newLabel()
	g.label(top)

	armLabels := make([]string, len(i.Arms))
	for index, arm := range i.Arms {
		armLabels[index] = g.newLabel()
		if arm.Send {
			element := f.elementOut(arm.Val)
			g.ins("mov rdi, %s", f.slot(arm.Ch))
			g.ins("lea rsi, %s", element)
			g.ins("call %s", g.extern("gy_chan_try_send"))
			g.ins("test rax, rax")
			g.ins("jnz %s", armLabels[index])
			continue
		}
		f.zeroMem(f.slot(arm.Dst), g.slotOf(f.typeOf(arm.Dst)))
		g.ins("mov rdi, %s", f.slot(arm.Ch))
		g.ins("lea rsi, %s", f.elementIn(arm.Dst))
		g.ins("call %s", g.extern("gy_chan_try_recv"))
		// The arm is taken only if the receive succeeded, so the value is
		// read back out of staging on that path alone.
		missed := g.newLabel()
		g.ins("test rax, rax")
		g.ins("jz %s", missed)
		f.elementBack(arm.Dst)
		g.ins("jmp %s", armLabels[index])
		g.label(missed)
	}
	if len(i.Default) > 0 {
		f.body(i.Default)
		g.ins("jmp %s", end)
	} else {
		g.ins("call %s", g.extern("gy_select_pause"))
		g.ins("jmp %s", top)
	}

	for index, arm := range i.Arms {
		g.label(armLabels[index])
		f.body(arm.Body)
		g.ins("jmp %s", end)
	}
	g.label(end)
}

// ---------------------------------------------------------------------------
// Type lookups
// ---------------------------------------------------------------------------

func (f *frame) recordOf(reg string) *metals.RecordShape {
	named, ok := f.typeOf(reg).(*metals.Named)
	if !ok {
		f.g.fail("@%s: %%%s is not a record", f.fn.Name, reg)
	}
	decl := f.g.types[named.Name]
	if decl == nil || decl.Record == nil {
		f.g.fail("@%s: %%%s is not a record", f.fn.Name, reg)
	}
	return decl.Record
}

func (f *frame) enumOf(reg string) *metals.EnumShape {
	named, ok := f.typeOf(reg).(*metals.Named)
	if !ok {
		f.g.fail("@%s: %%%s is not an enum", f.fn.Name, reg)
	}
	decl := f.g.types[named.Name]
	if decl == nil || decl.Enum == nil {
		f.g.fail("@%s: %%%s is not an enum", f.fn.Name, reg)
	}
	return decl.Enum
}

func (f *frame) tupleOf(reg string) *metals.TupleOf {
	tuple, ok := f.typeOf(reg).(*metals.TupleOf)
	if !ok {
		f.g.fail("@%s: %%%s is not a tuple", f.fn.Name, reg)
	}
	return tuple
}

// declBehindPtr is the record or union a pointer addresses. The two
// answer the same question about a member's name and type and different
// ones about its offset, which is metals.Layout's to settle.
func (f *frame) declBehindPtr(reg string) *metals.TypeDecl {
	pointer, ok := f.typeOf(reg).(*metals.Ptr)
	if !ok {
		f.g.fail("@%s: %%%s is not a pointer", f.fn.Name, reg)
	}
	named, ok := pointer.Elem.(*metals.Named)
	if !ok {
		f.g.fail("@%s: %%%s does not point at a record", f.fn.Name, reg)
	}
	decl := f.g.types[named.Name]
	if decl == nil || decl.Members() == nil {
		f.g.fail("@%s: %%%s does not point at a record", f.fn.Name, reg)
	}
	return decl
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func (f *frame) emitConst(i *metals.Const) {
	g := f.g
	if _, folded := f.consts[i.Dst]; folded {
		// The value rides in every instruction that reads it.
		return
	}
	dst := f.slot(i.Dst)
	if i.Null {
		f.zeroMem(dst, g.sizeOf(f.typeOf(i.Dst)))
		return
	}
	prim, ok := i.Type.(*metals.Prim)
	if !ok {
		g.fail("@%s: constant of type %s", f.fn.Name, i.Type)
	}

	switch prim.Kind {
	case metals.Str:
		decoded, err := strconv.Unquote(i.Value)
		if err != nil {
			g.fail("@%s: bad string constant %s", f.fn.Name, i.Value)
		}
		f.leaInto(dst, "["+g.strLabel(decoded)+"]")

	case metals.F32, metals.F64:
		parsed, err := strconv.ParseFloat(i.Value, 64)
		if err != nil {
			g.fail("@%s: bad float constant %s", f.fn.Name, i.Value)
		}
		if prim.Kind == metals.F32 {
			// A constant is an operation producing an f32 like any other,
			// so it rounds through single precision before the register
			// carries it as a double.
			parsed = float64(float32(parsed))
		}
		f.storeImm(dst, int64(floatBits(parsed)), strconv.FormatFloat(parsed, 'g', -1, 64))

	case metals.Bool:
		value := int64(0)
		if i.Value == "true" {
			value = 1
		}
		f.storeImm(dst, value, i.Value)

	case metals.Rune:
		decoded, _, _, err := strconv.UnquoteChar(i.Value[1:len(i.Value)-1], '\'')
		if err != nil {
			g.fail("@%s: bad rune constant %s", f.fn.Name, i.Value)
		}
		f.storeImm(dst, int64(decoded), i.Value)

	default:
		if parsed, err := strconv.ParseInt(i.Value, 0, 64); err == nil {
			f.storeImm(dst, parsed, i.Value)
			return
		}
		parsed, err := strconv.ParseUint(i.Value, 0, 64)
		if err != nil {
			g.fail("@%s: bad integer constant %s", f.fn.Name, i.Value)
		}
		f.storeImm(dst, int64(parsed), i.Value)
	}
}

// storeImm writes a 64-bit immediate to a value's home, through rax when
// it is memory and the value does not fit the sign-extended 32 bits
// `mov qword` accepts.
func (g *generator) storeImm(dst loc, value int64, comment string) {
	if comment != "" {
		comment = " ; " + comment
	}
	if dst.isReg() || value >= -2147483648 && value <= 2147483647 {
		g.ins("mov %s, %d%s", dst.sized("qword"), value, comment)
		return
	}
	g.ins("mov rax, %d%s", value, comment)
	g.ins("mov %s, rax", dst)
}
