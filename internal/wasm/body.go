package wasm

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// One function body: wasm's operand stack becomes METALS registers, and
// wasm's control stack becomes METALS' nesting.
//
// The stack-to-register mapping is the only interesting part. A wasm
// stack slot has no name and no fixed type — depth 3 may hold an i32 in
// one basic block and an f64 in another — but a METALS register's type
// is fixed at its first assignment. Naming a register after both its
// depth and its type (%s3_i32, %s3_f64) makes the two models agree
// exactly, with no analysis: two values that could collide have
// different names because they have different types, and two that share
// a name share a type by construction.
//
// Every register the body ever writes is zero-initialized at the top.
// That is not tidiness. METALS requires a register to be assigned on
// every path before it is read, and a value that reaches a block's end
// through one branch and a br through another would otherwise have to be
// proven assigned by an analysis this translator has no reason to carry.
// A wasm local is zero-initialized anyway, so for most of these the
// prologue is not even an addition.

type bodyTranslator struct {
	t      *translator
	fn     *metals.Func
	src    *wmodule
	result metals.Type

	// locals is every local's type, parameters included and first.
	locals []valType

	// stack is the abstract operand stack: one entry per value, holding
	// the register it lives in.
	stack []stackEntry
	// ctrl is the control stack, innermost last.
	ctrl []*ctrlFrame
	// frames parallels ctrl: the instructions accumulated at each level.
	frames []*bodyBuilder

	// types records every register the body assigns, so the prologue can
	// initialize them. Sorted at the end, so the output is deterministic.
	types map[string]metals.Type

	// temps counts the scratch registers handed out for values that are
	// not on the operand stack.
	temps int

	// A comparison feeding a br_if or an if reuses the bool it already
	// produced, instead of testing the 0/1 it was widened into. Compiled
	// C is mostly this pattern, and without it every branch reads three
	// instructions instead of one.
	//
	// pendingCompare is what the instruction just translated produced;
	// step moves it into lastCompare and clears it, so the reuse only
	// fires when the comparison is immediately before the branch. A stack
	// slot's register name is reused at the same depth, so anything
	// looser would let a stale flag stand in for an unrelated value.
	lastCompare    comparison
	pendingCompare comparison

	// labels numbers the control labels within this function.
	labels int
	// unreachable is set after return, br or unreachable: wasm's stack
	// goes polymorphic there and the code cannot run, so it is tracked
	// for structure and not translated.
	unreachable bool
}

type stackEntry struct {
	reg string
	typ valType
}

// comparison pairs the i32 a comparison was widened into with the bool
// it came from.
type comparison struct {
	value string
	flag  string
	valid bool
}

// ctrlFrame is one enclosing block, loop or if.
type ctrlFrame struct {
	label string
	loop  bool
	// isIf marks a frame whose METALS shape is an if inside a block; the
	// then-body is stashed when else arrives.
	isIf     bool
	thenBody []metals.Instr
	cond     string
	inElse   bool

	// result is the value type the frame leaves on the stack, and the
	// register it is collected in. Both branches of a br and the
	// fall-through write that register, which is how a value crosses a
	// structured jump.
	result    valType
	resultReg string

	// depth is the operand stack height when the frame opened, so that
	// end and else can restore it.
	depth int
	// targeted records whether any br names this frame. An untargeted
	// block wrapper is dropped, which keeps the output readable.
	targeted bool
}

// ---------------------------------------------------------------------------
// Registers
// ---------------------------------------------------------------------------

// note records a register's type for the prologue.
func (b *bodyTranslator) note(reg string, typ metals.Type) string {
	b.types[reg] = typ
	return reg
}

// slot names the register holding operand-stack depth d of type v.
func (b *bodyTranslator) slot(depth int, v valType) string {
	return b.note("s"+strconv.Itoa(depth)+"_"+v.String(), v.metalsType())
}

// temp hands out a scratch register of a METALS type, for values that
// never appear on the wasm stack: masked shift counts, widened
// comparisons, addresses.
func (b *bodyTranslator) temp(typ metals.Type) string {
	b.temps++
	return b.note("t"+strconv.Itoa(b.temps)+"_"+typeSuffix(typ), typ)
}

// typeSuffix is a type's name flattened into something a register name
// can carry.
func typeSuffix(typ metals.Type) string {
	if prim, ok := typ.(*metals.Prim); ok {
		return prim.String()
	}
	return "v"
}

func (b *bodyTranslator) push(v valType) string {
	reg := b.slot(len(b.stack), v)
	b.stack = append(b.stack, stackEntry{reg: reg, typ: v})
	return reg
}

func (b *bodyTranslator) pop() (stackEntry, error) {
	if len(b.stack) == 0 {
		return stackEntry{}, fmt.Errorf("the operand stack is empty")
	}
	top := b.stack[len(b.stack)-1]
	b.stack = b.stack[:len(b.stack)-1]
	return top, nil
}

// pop2 pops the two operands of a binary instruction, in the order they
// were pushed: wasm puts the right-hand side on top.
func (b *bodyTranslator) pop2() (left, right stackEntry, err error) {
	if right, err = b.pop(); err != nil {
		return
	}
	left, err = b.pop()
	return
}

// ---------------------------------------------------------------------------
// Emission
// ---------------------------------------------------------------------------

func (b *bodyTranslator) emit(instr metals.Instr) {
	if b.unreachable {
		return
	}
	b.frames[len(b.frames)-1].emit(instr)
}

// constant materializes a literal into a fresh register.
func (b *bodyTranslator) constant(typ metals.Type, value string) string {
	reg := b.temp(typ)
	b.emit(&metals.Const{Dst: reg, Type: typ, Value: value})
	return reg
}

func (b *bodyTranslator) nextLabel(kind string) string {
	b.labels++
	return kind + strconv.Itoa(b.labels)
}

// ---------------------------------------------------------------------------
// Driving the body
// ---------------------------------------------------------------------------

func (b *bodyTranslator) run(body []byte, params int) error {
	// The function's own frame: its label is the one a `return`-shaped
	// br at depth len(ctrl)-1 targets, and its result is the function's.
	outer := &ctrlFrame{label: b.nextLabel("fn"), result: b.resultVal()}
	if outer.result != 0 {
		outer.resultReg = b.temp(outer.result.metalsType())
	}
	b.ctrl = []*ctrlFrame{outer}
	b.frames = []*bodyBuilder{{}}

	r := &reader{src: body}
	for !r.eof() {
		if len(b.ctrl) == 0 {
			return fmt.Errorf("instructions after the body ended")
		}
		if err := b.step(r); err != nil {
			return err
		}
		if len(b.ctrl) == 0 {
			break
		}
	}
	if len(b.frames) != 1 {
		return fmt.Errorf("the body ends inside %d unclosed block(s)", len(b.frames)-1)
	}

	b.fn.Body = append(b.prologue(params), b.frames[0].instrs...)
	return nil
}

func (b *bodyTranslator) resultVal() valType {
	if b.result == nil {
		return 0
	}
	for _, v := range []valType{valI32, valI64, valF32, valF64} {
		if metals.TypesEqual(v.metalsType(), b.result) {
			return v
		}
	}
	return 0
}

// prologue zero-initializes every register the body assigns, except the
// parameters, which arrive assigned. Wasm locals start at zero, so this
// is their initialization as much as it is METALS' definedness rule.
func (b *bodyTranslator) prologue(params int) []metals.Instr {
	names := make([]string, 0, len(b.types))
	for name := range b.types {
		names = append(names, name)
	}
	sort.Strings(names)

	parameters := map[string]bool{}
	for i := 0; i < params; i++ {
		parameters[localName(i)] = true
	}

	var out []metals.Instr
	for _, name := range names {
		if parameters[name] {
			continue
		}
		typ := b.types[name]
		// A pointer has no zero to write, and needs none: the only
		// pointers here address a global, and each is produced by the
		// global.ptr immediately above the load or store that reads it.
		if _, isPrim := typ.(*metals.Prim); !isPrim {
			continue
		}
		out = append(out, &metals.Const{Dst: name, Type: typ, Value: zeroLiteral(typ)})
	}
	return out
}

// ---------------------------------------------------------------------------
// Control flow
// ---------------------------------------------------------------------------

// blockType reads a block's signature. Only the two forms a linked C
// module carries are accepted: no result, or one value.
func (b *bodyTranslator) blockType(r *reader) (valType, error) {
	// The encoding is a signed LEB, so a negative value is one of the
	// inline forms and a non-negative one indexes the type section.
	save := r.pos
	raw, err := r.i64()
	if err != nil {
		return 0, err
	}
	if raw >= 0 {
		return 0, fmt.Errorf("a block with a full function type (%d) is multi-value, "+
			"which is not translated", raw)
	}
	if raw == -0x40 {
		return 0, nil
	}
	r.pos = save
	return r.valType()
}

// openFrame pushes a control frame and the accumulator its body goes to.
func (b *bodyTranslator) openFrame(frame *ctrlFrame) {
	frame.depth = len(b.stack)
	if frame.result != 0 {
		frame.resultReg = b.temp(frame.result.metalsType())
	}
	b.ctrl = append(b.ctrl, frame)
	b.frames = append(b.frames, &bodyBuilder{})
}

// frameAt resolves a br's relative depth to the frame it names.
func (b *bodyTranslator) frameAt(depth uint32) (*ctrlFrame, error) {
	index := len(b.ctrl) - 1 - int(depth)
	if index < 0 {
		return nil, fmt.Errorf("br %d reaches past the function's own frame", depth)
	}
	return b.ctrl[index], nil
}

// branchTo emits the jump to a frame, carrying its result value when it
// has one. A branch to the function's own frame is a return.
func (b *bodyTranslator) branchTo(depth uint32) error {
	frame, err := b.frameAt(depth)
	if err != nil {
		return err
	}
	if frame.result != 0 && !frame.loop {
		// The value the branch carries is on top of the stack. It stays
		// there: wasm's br does not pop beyond what it carries, and the
		// code after a br is unreachable anyway.
		if len(b.stack) == 0 {
			return fmt.Errorf("br %d carries a %s but the stack is empty", depth, frame.result)
		}
		top := b.stack[len(b.stack)-1]
		if top.typ != frame.result {
			return fmt.Errorf("br %d carries a %s where the block wants a %s",
				depth, top.typ, frame.result)
		}
		b.emit(&metals.Copy{Dst: frame.resultReg, X: top.reg})
	}
	if frame.loop && frame.result != 0 {
		return fmt.Errorf("br %d re-enters a loop carrying a value, which is not translated", depth)
	}

	if index := len(b.ctrl) - 1 - int(depth); index == 0 {
		// The outermost frame: leaving it is returning.
		b.emit(&metals.Return{Val: b.ctrl[0].resultReg})
		return nil
	}
	frame.targeted = true
	b.emit(&metals.Br{Label: frame.label})
	return nil
}

// condition turns a wasm i32 condition into a METALS bool, reusing the
// comparison that produced it when there was one.
func (b *bodyTranslator) condition(value stackEntry) string {
	if b.lastCompare.valid && b.lastCompare.value == value.reg {
		return b.lastCompare.flag
	}
	zero := b.constant(value.typ.metalsType(), "0")
	flag := b.temp(metals.TBool)
	b.emit(&metals.Bin{Dst: flag, Op: "ne", A: value.reg, B: zero})
	return flag
}

// closeFrame ends a block, loop or if and folds its body into the level
// above, leaving the frame's result on the operand stack.
func (b *bodyTranslator) closeFrame() error {
	frame := b.ctrl[len(b.ctrl)-1]
	inner := b.frames[len(b.frames)-1]

	// A frame that falls out of its end with a value writes it to the
	// same register a br to the frame would have.
	if frame.result != 0 && !b.unreachable {
		if len(b.stack) == 0 {
			return fmt.Errorf("a block ends wanting a %s with nothing on the stack", frame.result)
		}
		top := b.stack[len(b.stack)-1]
		inner.emit(&metals.Copy{Dst: frame.resultReg, X: top.reg})
	}

	b.ctrl = b.ctrl[:len(b.ctrl)-1]
	b.frames = b.frames[:len(b.frames)-1]
	b.unreachable = false
	b.stack = b.stack[:frame.depth]

	var built metals.Instr
	switch {
	case frame.isIf:
		then, els := frame.thenBody, inner.instrs
		if !frame.inElse {
			then, els = inner.instrs, nil
		}
		built = &metals.If{Cond: frame.cond, Then: then, Else: els}
	case frame.loop:
		built = &metals.Loop{Label: frame.label, Body: inner.instrs}
	default:
		built = &metals.Block{Label: frame.label, Body: inner.instrs}
	}

	// An if is wrapped in a block only when something branches out of
	// it; a loop always keeps its label, because its label is how the
	// loop is re-entered and a loop nobody re-enters is still a loop.
	if frame.isIf && frame.targeted {
		built = &metals.Block{Label: frame.label, Body: []metals.Instr{built}}
	}
	// A plain block nobody branches to is nothing at all: its body runs
	// once in order, which is what the enclosing level already does.
	if !frame.isIf && !frame.loop && !frame.targeted {
		b.frames[len(b.frames)-1].instrs =
			append(b.frames[len(b.frames)-1].instrs, inner.instrs...)
	} else {
		b.frames[len(b.frames)-1].emit(built)
	}

	if frame.result != 0 {
		reg := b.push(frame.result)
		b.frames[len(b.frames)-1].emit(&metals.Copy{Dst: reg, X: frame.resultReg})
	}
	return nil
}
