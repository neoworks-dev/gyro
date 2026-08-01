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

// frame emits one function. Every register lives in a stack slot; the
// slot at rbp-8 holds the incoming sret pointer, rbp-16 is scratch.
type frame struct {
	g       *generator
	fn      *metals.Func
	types   map[string]metals.Type
	offsets map[string]int // register -> rbp displacement of slot start
	areas   map[string]int // Slot instruction dst -> rbp displacement
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
		labels:  map[string]string{},
		panics:  map[string]string{},
	}
	f.layoutFrame()

	g.text.WriteString("\n")
	g.comment("fn @%s", fn.Name)
	g.label(g.symbolFor(fn.Name))
	g.ins("push rbp")
	g.ins("mov rbp, rsp")
	g.ins("sub rsp, %d", f.size)
	f.spillParams()
	f.body(fn.Body)

	// The lowering always terminates a body; this backstop only runs if
	// a void function's last path falls through.
	g.ins("leave")
	g.ins("ret")
	f.emitPanicLabels()
}

// layoutFrame assigns every register a slot and every `slot` instruction
// an area, in sorted order so output is deterministic.
func (f *frame) layoutFrame() {
	current := 16 // sret + scratch

	names := make([]string, 0, len(f.types))
	for name := range f.types {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		current += f.g.sizeOf(f.types[name])
		f.offsets[name] = -current
	}

	walkInstrs(f.fn.Body, func(instr metals.Instr) {
		slot, ok := instr.(*metals.Slot)
		if !ok {
			return
		}
		if _, seen := f.areas[slot.Dst]; seen {
			return
		}
		current += f.g.sizeOf(slot.Type)
		f.areas[slot.Dst] = -current
	})

	f.size = (current + 15) / 16 * 16
}

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

func (f *frame) slot(reg string) memLoc {
	offset, ok := f.offsets[reg]
	if !ok {
		f.g.fail("@%s: register %%%s has no slot", f.fn.Name, reg)
	}
	return memLoc{base: "rbp", off: offset}
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
		slot := f.slot(param.Name)
		class := g.classOf(param.Type)
		size := g.sizeOf(param.Type)

		switch {
		case class == classSSE && sseIndex < len(sseArgRegs):
			g.ins("movsd %s, %s", slot, sseArgRegs[sseIndex])
			sseIndex++
		case class == classInt && intIndex < len(intArgRegs):
			g.ins("mov %s, %s", slot, intArgRegs[intIndex])
			intIndex++
		case class == classMem && intIndex < len(intArgRegs):
			g.ins("mov %s, %s", slot, intArgRegs[intIndex])
			intIndex++
			expansions = append(expansions, expand{slot: slot, size: size})
		default:
			if class == classMem {
				expansions = append(expansions, expand{slot: slot, size: size, stackSrc: stackOffset})
			} else if class == classSSE {
				g.ins("mov rax, %s", memLoc{"rbp", stackOffset})
				g.ins("mov %s, rax", slot)
			} else {
				g.ins("mov rax, %s", memLoc{"rbp", stackOffset})
				g.ins("mov %s, rax", slot)
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
		g.ins("lea rdi, %s", e.slot)
		g.ins("mov rcx, %d", e.size/8)
		g.ins("rep movsq")
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

// copyMem copies size bytes; size is always a multiple of 8. Clobbers
// rax, and rsi/rdi/rcx beyond 16 bytes. Base registers of dst and src
// must survive the clobbers (rbp always does; rdx and r10 are safe).
func (f *frame) copyMem(dst, src memLoc, size int) {
	g := f.g
	if size == 8 {
		g.ins("mov rax, %s", src)
		g.ins("mov %s, rax", dst)
		return
	}
	if size == 16 {
		g.ins("mov rax, %s", src)
		g.ins("mov %s, rax", dst)
		g.ins("mov rax, %s", src.plus(8))
		g.ins("mov %s, rax", dst.plus(8))
		return
	}
	g.ins("lea rsi, %s", src)
	g.ins("lea rdi, %s", dst)
	g.ins("mov rcx, %d", size/8)
	g.ins("rep movsq")
}

// zeroMem clears size bytes. Clobbers rax, and rdi/rcx beyond 16 bytes.
func (f *frame) zeroMem(dst memLoc, size int) {
	g := f.g
	if size <= 16 {
		for at := 0; at < size; at += 8 {
			g.ins("mov qword %s, 0", dst.plus(at))
		}
		return
	}
	g.ins("lea rdi, %s", dst)
	g.ins("xor eax, eax")
	g.ins("mov rcx, %d", size/8)
	g.ins("rep stosq")
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
		f.copyMem(f.slot(i.Dst), f.slot(i.X), g.sizeOf(f.typeOf(i.Dst)))

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
		f.zeroMem(memLoc{"rbp", area}, g.sizeOf(i.Type))
		g.ins("lea rax, %s", memLoc{"rbp", area})
		g.ins("mov %s, rax", f.slot(i.Dst))

	case *metals.New:
		g.ins("mov rdi, %d", g.sizeOf(i.Type))
		g.ins("call %s", g.extern("gy_alloc"))
		g.ins("mov %s, rax", f.slot(i.Dst))

	case *metals.Free:
		g.ins("mov rdi, %s", f.slot(i.X))
		g.ins("call %s", g.extern("gy_free"))

	case *metals.Load:
		g.ins("mov rdx, %s", f.slot(i.Ptr))
		f.copyMem(f.slot(i.Dst), memLoc{base: "rdx"}, g.sizeOf(f.typeOf(i.Dst)))

	case *metals.Store:
		g.ins("mov rdx, %s", f.slot(i.Ptr))
		f.copyMem(memLoc{base: "rdx"}, f.slot(i.Val), g.sizeOf(f.typeOf(i.Val)))

	case *metals.FieldPtr:
		record := f.recordBehindPtr(i.Ptr)
		g.ins("mov rax, %s", f.slot(i.Ptr))
		offset := g.fieldOffset(record, i.Index)
		if offset != 0 {
			g.ins("lea rax, [rax+%d]", offset)
		}
		g.ins("mov %s, rax", f.slot(i.Dst))

	case *metals.MakeRecord:
		record := g.types[i.TypeName].Record
		dst := f.slot(i.Dst)
		for index, arg := range i.Args {
			size := g.sizeOf(record.Fields[index].Type)
			f.copyMem(dst.plus(g.fieldOffset(record, index)), f.slot(arg), size)
		}

	case *metals.FieldGet:
		record := f.recordOf(i.X)
		size := g.sizeOf(record.Fields[i.Index].Type)
		f.copyMem(f.slot(i.Dst), f.slot(i.X).plus(g.fieldOffset(record, i.Index)), size)

	case *metals.FieldSet:
		record := f.recordOf(i.X)
		size := g.sizeOf(record.Fields[i.Index].Type)
		f.copyMem(f.slot(i.X).plus(g.fieldOffset(record, i.Index)), f.slot(i.Val), size)

	case *metals.MakeVariant:
		enum := g.types[i.TypeName].Enum
		dst := f.slot(i.Dst)
		f.zeroMem(dst, g.namedSize(i.TypeName))
		g.ins("mov qword %s, %d", dst, i.Variant)
		for index, arg := range i.Args {
			size := g.sizeOf(enum.Variants[i.Variant].Payload[index])
			f.copyMem(dst.plus(g.variantFieldOffset(enum, i.Variant, index)), f.slot(arg), size)
		}

	case *metals.VariantTag:
		g.ins("mov rax, %s", f.slot(i.X))
		g.ins("mov %s, rax", f.slot(i.Dst))

	case *metals.VariantField:
		enum := f.enumOf(i.X)
		size := g.sizeOf(enum.Variants[i.Variant].Payload[i.Index])
		offset := g.variantFieldOffset(enum, i.Variant, i.Index)
		f.copyMem(f.slot(i.Dst), f.slot(i.X).plus(offset), size)

	case *metals.MakeTuple:
		tuple := f.tupleOf(i.Dst)
		dst := f.slot(i.Dst)
		for index, arg := range i.Args {
			f.copyMem(dst.plus(g.tupleOffset(tuple, index)), f.slot(arg), g.sizeOf(tuple.Elems[index]))
		}

	case *metals.TupleGet:
		tuple := f.tupleOf(i.X)
		offset := g.tupleOffset(tuple, i.Index)
		f.copyMem(f.slot(i.Dst), f.slot(i.X).plus(offset), g.sizeOf(tuple.Elems[i.Index]))

	case *metals.MakeArray:
		size := g.sizeOf(i.Elem)
		dst := f.slot(i.Dst)
		for index, arg := range i.Args {
			f.copyMem(dst.plus(index*size), f.slot(arg), size)
		}

	case *metals.Slice:
		array, ok := f.typeOf(i.X).(*metals.ArrayOf)
		if !ok {
			g.fail("@%s: slice.of a non-array", f.fn.Name)
		}
		g.ins("lea rax, %s", f.slot(i.X))
		g.ins("mov %s, rax", f.slot(i.Dst))
		g.ins("mov qword %s, %d", f.slot(i.Dst).plus(8), array.Len)

	case *metals.IndexGet:
		element := f.indexedElement(i.X, i.Index)
		f.copyMem(f.slot(i.Dst), element, g.sizeOf(f.typeOf(i.Dst)))

	case *metals.IndexSet:
		element := f.indexedElement(i.X, i.Index)
		f.copyMem(element, f.slot(i.Val), g.sizeOf(f.typeOf(i.Val)))

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
		g.ins("mov qword %s, 0", f.slot(i.Dst).plus(8))

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
		g.ins("mov rax, %s", f.slot(i.Cond))
		g.ins("test rax, rax")
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
		g.ins("mov rdi, %s", f.slot(i.Ch))
		g.ins("lea rsi, %s", f.slot(i.Val))
		g.ins("call %s", g.extern("gy_chan_send"))

	case *metals.Recv:
		f.emitRecv(i)

	case *metals.ChanClose:
		g.ins("mov rdi, %s", f.slot(i.Ch))
		g.ins("call %s", g.extern("gy_chan_close"))

	case *metals.Await:
		g.ins("mov rdi, %s", f.slot(i.X))
		g.ins("lea rsi, %s", f.slot(i.Dst))
		g.ins("call %s", g.extern("gy_future_get"))

	case *metals.Select:
		f.emitSelect(i)

	case *metals.OptWrap:
		opt, ok := f.typeOf(i.Dst).(*metals.Opt)
		if !ok {
			g.fail("@%s: opt.wrap into a non-optional", f.fn.Name)
		}
		g.ins("mov qword %s, 1", f.slot(i.Dst))
		f.copyMem(f.slot(i.Dst).plus(8), f.slot(i.X), g.sizeOf(opt.Elem))

	case *metals.OptUnwrap:
		g.ins("mov rax, %s", f.slot(i.X))
		g.ins("test rax, rax")
		g.ins("jz %s", f.panicLabel("unwrap of a null value"))
		f.copyMem(f.slot(i.Dst), f.slot(i.X).plus(8), g.sizeOf(f.typeOf(i.Dst)))

	default:
		g.fail("@%s: the native backend does not implement %T yet", f.fn.Name, instr)
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
	g.ins("mov rax, %s", f.slot(i.Cond))
	g.ins("test rax, rax")
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
	g.ins("mov rax, %s", f.slot(i.X))
	end := g.newLabel()
	caseLabels := make([]string, len(i.Cases))
	for index, c := range i.Cases {
		caseLabels[index] = g.newLabel()
		g.ins("cmp rax, %d", c.Value)
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
			f.copyMem(memLoc{base: "rdx"}, f.slot(i.Val), g.sizeOf(result))
			g.ins("mov rax, rdx")
		}
	}
	g.ins("leave")
	g.ins("ret")
}

// ---------------------------------------------------------------------------
// Indexing and lengths
// ---------------------------------------------------------------------------

// indexedElement bounds-checks and leaves the element address in rdx,
// returned as a location. Clobbers rax, rcx, rdx.
func (f *frame) indexedElement(x, index string) memLoc {
	g := f.g
	var elemSize int
	switch t := f.typeOf(x).(type) {
	case *metals.SliceOf:
		elemSize = g.sizeOf(t.Elem)
		g.ins("mov rdx, %s", f.slot(x))
		g.ins("mov rcx, %s", f.slot(x).plus(8))
	case *metals.ArrayOf:
		elemSize = g.sizeOf(t.Elem)
		g.ins("lea rdx, %s", f.slot(x))
		g.ins("mov rcx, %d", t.Len)
	default:
		g.fail("@%s: indexing a %s", f.fn.Name, t)
	}
	g.ins("mov rax, %s", f.slot(index))
	g.ins("cmp rax, rcx")
	ok := g.newLabel()
	g.ins("jb %s", ok)
	g.ins("mov rdi, rax")
	g.ins("mov rsi, rcx")
	g.ins("call %s", g.extern("gy_abort_bounds"))
	g.label(ok)
	g.ins("imul rax, %d", elemSize)
	g.ins("add rdx, rax")
	return memLoc{base: "rdx"}
}

func (f *frame) emitLen(i *metals.Len) {
	g := f.g
	switch t := f.typeOf(i.X).(type) {
	case *metals.SliceOf:
		g.ins("mov rax, %s", f.slot(i.X).plus(8))
	case *metals.ArrayOf:
		g.ins("mov rax, %d", t.Len)
	case *metals.Prim:
		if t.Kind != metals.Str {
			g.fail("@%s: len of a %s", f.fn.Name, t)
		}
		g.ins("mov rax, %s", f.slot(i.X))
		g.ins("mov rax, [rax]")
	default:
		g.fail("@%s: len of a %s", f.fn.Name, t)
	}
	g.ins("mov %s, rax", f.slot(i.Dst))
}

// ---------------------------------------------------------------------------
// Channels and select
// ---------------------------------------------------------------------------

func (f *frame) emitRecv(i *metals.Recv) {
	g := f.g
	tuple := f.tupleOf(i.Dst)
	elemSize := g.sizeOf(tuple.Elems[0])
	f.zeroMem(f.slot(i.Dst), elemSize)
	g.ins("mov rdi, %s", f.slot(i.Ch))
	g.ins("lea rsi, %s", f.slot(i.Dst))
	g.ins("call %s", g.extern("gy_chan_recv"))
	g.ins("mov %s, rax", f.slot(i.Dst).plus(elemSize))
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
			g.ins("mov rdi, %s", f.slot(arm.Ch))
			g.ins("lea rsi, %s", f.slot(arm.Val))
			g.ins("call %s", g.extern("gy_chan_try_send"))
		} else {
			f.zeroMem(f.slot(arm.Dst), g.sizeOf(f.typeOf(arm.Dst)))
			g.ins("mov rdi, %s", f.slot(arm.Ch))
			g.ins("lea rsi, %s", f.slot(arm.Dst))
			g.ins("call %s", g.extern("gy_chan_try_recv"))
		}
		g.ins("test rax, rax")
		g.ins("jnz %s", armLabels[index])
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

func (f *frame) recordBehindPtr(reg string) *metals.RecordShape {
	pointer, ok := f.typeOf(reg).(*metals.Ptr)
	if !ok {
		f.g.fail("@%s: %%%s is not a pointer", f.fn.Name, reg)
	}
	named, ok := pointer.Elem.(*metals.Named)
	if !ok {
		f.g.fail("@%s: %%%s does not point at a record", f.fn.Name, reg)
	}
	decl := f.g.types[named.Name]
	if decl == nil || decl.Record == nil {
		f.g.fail("@%s: %%%s does not point at a record", f.fn.Name, reg)
	}
	return decl.Record
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func (f *frame) emitConst(i *metals.Const) {
	g := f.g
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
		g.ins("lea rax, [%s]", g.strLabel(decoded))
		g.ins("mov %s, rax", dst)

	case metals.F32, metals.F64:
		parsed, err := strconv.ParseFloat(i.Value, 64)
		if err != nil {
			g.fail("@%s: bad float constant %s", f.fn.Name, i.Value)
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

// storeImm writes a 64-bit immediate to a slot, through rax when it does
// not fit the sign-extended 32 bits `mov qword` accepts.
func (f *frame) storeImm(dst memLoc, value int64, comment string) {
	g := f.g
	if value >= -2147483648 && value <= 2147483647 {
		g.ins("mov qword %s, %d ; %s", dst, value, comment)
		return
	}
	g.ins("mov rax, %d ; %s", value, comment)
	g.ins("mov %s, rax", dst)
}
