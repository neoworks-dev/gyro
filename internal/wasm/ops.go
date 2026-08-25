package wasm

import (
	"fmt"
	"math"
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// The opcode dispatch. Most of it is a direct correspondence; what is
// worth reading is the handful of places where wasm and METALS disagree
// about what an operation means, because those are exactly the places a
// C front end will have to make the same decision.
//
//   - Signedness. A wasm i32 has none; the operation carries it, which is
//     why there is a div_s and a div_u. METALS types carry it instead, so
//     an unsigned operation reads its operands through bitcast, works in
//     u32, and casts back. Nothing is copied and nothing traps: bitcast is
//     a reinterpretation.
//
//   - Shift counts. Wasm reduces a count modulo the width; METALS follows
//     Go, where a count at or past the width yields zero. The count is
//     masked before the shift rather than the two being called equivalent.
//
//   - i32.rem_s of INT_MIN by -1 is 0 in wasm and a trap in METALS, which
//     matches C's undefined behaviour rather than wasm's definition. The
//     divisor is tested first.
//
//   - Comparisons produce an i32 in wasm and a bool in METALS. The bool is
//     kept alongside the widened i32, so a comparison feeding a branch —
//     which is nearly all of them in compiled C — reads as one instruction.

func (b *bodyTranslator) step(r *reader) error {
	// Only the instruction immediately before this one may hand down a
	// comparison flag to reuse.
	b.lastCompare, b.pendingCompare = b.pendingCompare, comparison{}

	op, err := r.byte()
	if err != nil {
		return err
	}

	switch op {
	// --- control -----------------------------------------------------
	case 0x00: // unreachable
		b.emit(&metals.Abort{Message: "unreachable"})
		b.unreachable = true
		return nil
	case 0x01: // nop
		return nil

	case 0x02, 0x03: // block, loop
		result, err := b.blockType(r)
		if err != nil {
			return err
		}
		kind := "b"
		if op == 0x03 {
			kind = "loop"
		}
		b.openFrame(&ctrlFrame{label: b.nextLabel(kind), loop: op == 0x03, result: result})
		return nil

	case 0x04: // if
		result, err := b.blockType(r)
		if err != nil {
			return err
		}
		value, err := b.pop()
		if err != nil {
			return err
		}
		cond := b.condition(value)
		b.openFrame(&ctrlFrame{label: b.nextLabel("if"), isIf: true, cond: cond, result: result})
		return nil

	case 0x05: // else
		frame := b.ctrl[len(b.ctrl)-1]
		if !frame.isIf || frame.inElse {
			return fmt.Errorf("else outside an if")
		}
		// The then-arm's value is collected here, because closeFrame will
		// only see the else-arm's.
		if frame.result != 0 && !b.unreachable && len(b.stack) > 0 {
			top := b.stack[len(b.stack)-1]
			b.frames[len(b.frames)-1].emit(&metals.Copy{Dst: frame.resultReg, X: top.reg})
		}
		frame.thenBody = b.frames[len(b.frames)-1].instrs
		frame.inElse = true
		b.frames[len(b.frames)-1] = &bodyBuilder{}
		b.stack = b.stack[:frame.depth]
		b.unreachable = false
		return nil

	case 0x0B: // end
		if len(b.ctrl) == 1 {
			return b.endFunction()
		}
		return b.closeFrame()

	case 0x0C: // br
		depth, err := r.u32()
		if err != nil {
			return err
		}
		if err := b.branchTo(depth); err != nil {
			return err
		}
		b.unreachable = true
		return nil

	case 0x0D: // br_if
		depth, err := r.u32()
		if err != nil {
			return err
		}
		value, err := b.pop()
		if err != nil {
			return err
		}
		cond := b.condition(value)
		return b.conditionalBranch(cond, depth)

	case 0x0E: // br_table
		return b.brTable(r)

	case 0x0F: // return
		if b.result != nil {
			top, err := b.pop()
			if err != nil {
				return err
			}
			b.emit(&metals.Return{Val: top.reg})
		} else {
			b.emit(&metals.Return{})
		}
		b.unreachable = true
		return nil

	case 0x10: // call
		index, err := r.u32()
		if err != nil {
			return err
		}
		return b.call(index)

	case 0x11: // call_indirect
		return b.callIndirect(r)

	// --- parametric --------------------------------------------------
	case 0x1A: // drop
		_, err := b.pop()
		return err

	case 0x1B, 0x1C: // select, select with an explicit type
		if op == 0x1C {
			if err := each(r, func() error { _, err := r.valType(); return err }); err != nil {
				return err
			}
		}
		return b.selectOp()

	// --- variables ---------------------------------------------------
	case 0x20, 0x21, 0x22: // local.get, local.set, local.tee
		index, err := r.u32()
		if err != nil {
			return err
		}
		return b.localOp(op, index)

	case 0x23, 0x24: // global.get, global.set
		index, err := r.u32()
		if err != nil {
			return err
		}
		return b.globalOp(op, index)

	// --- memory ------------------------------------------------------
	case 0x3F, 0x40: // memory.size, memory.grow
		if _, err := r.u32(); err != nil { // memory index
			return err
		}
		if err := b.wantMemory("memory.size"); err != nil {
			return err
		}
		if op == 0x3F {
			pages := b.temp(metals.TUint32)
			b.emit(&metals.MemSize{Dst: pages, Memory: memoryName})
			b.emit(&metals.Bitcast{Dst: b.push(valI32), X: pages, To: metals.TInt32})
			return nil
		}
		by, err := b.pop()
		if err != nil {
			return err
		}
		pages := b.temp(metals.TUint32)
		b.emit(&metals.Bitcast{Dst: pages, X: by.reg, To: metals.TUint32})
		b.emit(&metals.MemGrow{Dst: b.push(valI32), Memory: memoryName, Pages: pages})
		return nil

	// --- constants ---------------------------------------------------
	case 0x41: // i32.const
		value, err := r.i32()
		if err != nil {
			return err
		}
		b.emit(&metals.Const{Dst: b.push(valI32), Type: metals.TInt32,
			Value: strconv.FormatInt(int64(value), 10)})
		return nil
	case 0x42: // i64.const
		value, err := r.i64()
		if err != nil {
			return err
		}
		b.emit(&metals.Const{Dst: b.push(valI64), Type: metals.TInt64,
			Value: strconv.FormatInt(value, 10)})
		return nil
	case 0x43: // f32.const
		value, err := r.f32()
		if err != nil {
			return err
		}
		b.emit(&metals.Const{Dst: b.push(valF32), Type: metals.TF32,
			Value: floatLiteral(float64(value))})
		return nil
	case 0x44: // f64.const
		value, err := r.f64()
		if err != nil {
			return err
		}
		b.emit(&metals.Const{Dst: b.push(valF64), Type: metals.TF64,
			Value: floatLiteral(value)})
		return nil

	// --- the prefixed opcodes ----------------------------------------
	case 0xFC:
		return b.prefixed(r)
	}

	if access, ok := memoryOps[op]; ok {
		return b.memoryAccess(r, access)
	}
	if err := b.numeric(op); err != nil {
		return err
	}
	return nil
}

// endFunction closes the outermost frame: whatever is on the stack is
// the result, and a body that ran off its end returns.
func (b *bodyTranslator) endFunction() error {
	if b.result != nil && !b.unreachable {
		top, err := b.pop()
		if err != nil {
			return err
		}
		b.frames[0].emit(&metals.Return{Val: top.reg})
	} else if !b.unreachable {
		b.frames[0].emit(&metals.Return{})
	} else {
		// The body ended unreachable, but METALS wants every path to end
		// in a return, and the verifier does not know this one cannot be
		// taken.
		b.unreachable = false
		if b.result != nil {
			zero := b.constant(b.result, zeroLiteral(b.result))
			b.frames[0].emit(&metals.Return{Val: zero})
		} else {
			b.frames[0].emit(&metals.Return{})
		}
	}
	b.ctrl = nil
	return nil
}

// conditionalBranch is br_if. METALS' br_if takes a bool and a label, so
// a branch to an enclosing block is one instruction — but a branch that
// carries a value, or that is really a return, needs the work done under
// an if instead.
func (b *bodyTranslator) conditionalBranch(cond string, depth uint32) error {
	frame, err := b.frameAt(depth)
	if err != nil {
		return err
	}
	index := len(b.ctrl) - 1 - int(depth)
	if index > 0 && frame.result == 0 {
		frame.targeted = true
		b.emit(&metals.BrIf{Cond: cond, Label: frame.label})
		return nil
	}
	taken, err := b.captureBody(func() error { return b.branchTo(depth) })
	if err != nil {
		return err
	}
	b.emit(&metals.If{Cond: cond, Then: taken})
	return nil
}

// brTable becomes a switch whose every arm branches. An index the table
// does not name falls to the default, which is what wasm says too.
func (b *bodyTranslator) brTable(r *reader) error {
	count, err := r.u32()
	if err != nil {
		return err
	}
	depths := make([]uint32, count)
	for i := range depths {
		if depths[i], err = r.u32(); err != nil {
			return err
		}
	}
	fallback, err := r.u32()
	if err != nil {
		return err
	}
	index, err := b.pop()
	if err != nil {
		return err
	}

	sw := &metals.Switch{X: index.reg}
	for value, depth := range depths {
		arm, err := b.captureBody(func() error { return b.branchTo(depth) })
		if err != nil {
			return err
		}
		sw.Cases = append(sw.Cases, metals.SwitchCase{Value: value, Body: arm})
	}
	if sw.Default, err = b.captureBody(func() error { return b.branchTo(fallback) }); err != nil {
		return err
	}
	b.emit(sw)
	b.unreachable = true
	return nil
}

// captureBody runs an emitter into a fresh accumulator and returns what
// it produced, so a nested arm can be built without a second emit path.
func (b *bodyTranslator) captureBody(build func() error) ([]metals.Instr, error) {
	b.frames = append(b.frames, &bodyBuilder{})
	err := build()
	inner := b.frames[len(b.frames)-1]
	b.frames = b.frames[:len(b.frames)-1]
	if err != nil {
		return nil, err
	}
	return inner.instrs, nil
}

// ---------------------------------------------------------------------------
// Calls, locals, globals
// ---------------------------------------------------------------------------

func (b *bodyTranslator) call(index uint32) error {
	if int(index) >= len(b.t.names) {
		return fmt.Errorf("call names function %d, of %d", index, len(b.t.names))
	}
	ft, err := b.src.typeOfFunc(index)
	if err != nil {
		return err
	}
	args, err := b.popArgs(len(ft.Params))
	if err != nil {
		return err
	}
	call := &metals.Call{Callee: b.t.names[index], Args: args}
	if err := b.finishCall(call, ft); err != nil {
		return err
	}
	b.emit(call)
	return nil
}

func (b *bodyTranslator) callIndirect(r *reader) error {
	typeIndex, err := r.u32()
	if err != nil {
		return err
	}
	if _, err := r.u32(); err != nil { // table index
		return err
	}
	dispatcher, err := b.t.dispatcher(typeIndex)
	if err != nil {
		return err
	}
	ft := &b.src.Types[typeIndex]

	slot, err := b.pop()
	if err != nil {
		return err
	}
	args, err := b.popArgs(len(ft.Params))
	if err != nil {
		return err
	}
	call := &metals.Call{Callee: dispatcher, Args: append([]string{slot.reg}, args...)}
	if err := b.finishCall(call, ft); err != nil {
		return err
	}
	b.emit(call)
	return nil
}

// popArgs takes n arguments off the stack and returns them in call
// order: wasm pushes the first argument first, so the stack holds them
// already in order and only needs to be cut.
func (b *bodyTranslator) popArgs(n int) ([]string, error) {
	if len(b.stack) < n {
		return nil, fmt.Errorf("a call wants %d argument(s) and the stack holds %d", n, len(b.stack))
	}
	args := make([]string, n)
	for i, entry := range b.stack[len(b.stack)-n:] {
		args[i] = entry.reg
	}
	b.stack = b.stack[:len(b.stack)-n]
	return args, nil
}

func (b *bodyTranslator) finishCall(call *metals.Call, ft *funcType) error {
	switch len(ft.Results) {
	case 0:
	case 1:
		call.Dst = b.push(ft.Results[0])
	default:
		return fmt.Errorf("a call returning %d values is not translated", len(ft.Results))
	}
	return nil
}

func (b *bodyTranslator) localOp(op byte, index uint32) error {
	if int(index) >= len(b.locals) {
		return fmt.Errorf("local %d, of %d", index, len(b.locals))
	}
	typ := b.locals[index]
	name := b.note(localName(int(index)), typ.metalsType())

	if op == 0x20 { // local.get
		b.emit(&metals.Copy{Dst: b.push(typ), X: name})
		return nil
	}
	value, err := b.pop()
	if err != nil {
		return err
	}
	if value.typ != typ {
		return fmt.Errorf("local %d is a %s and the value is a %s", index, typ, value.typ)
	}
	b.emit(&metals.Copy{Dst: name, X: value.reg})
	if op == 0x22 { // local.tee leaves the value behind
		b.emit(&metals.Copy{Dst: b.push(typ), X: name})
	}
	return nil
}

func (b *bodyTranslator) globalOp(op byte, index uint32) error {
	if int(index) >= len(b.src.Globals) {
		return fmt.Errorf("global %d, of %d", index, len(b.src.Globals))
	}
	typ := b.src.Globals[index].Type
	pointer := b.temp(&metals.Ptr{Elem: typ.metalsType()})
	b.emit(&metals.GlobalPtr{Dst: pointer, Name: globalName(int(index))})

	if op == 0x23 { // global.get
		b.emit(&metals.Load{Dst: b.push(typ), Ptr: pointer})
		return nil
	}
	value, err := b.pop()
	if err != nil {
		return err
	}
	b.emit(&metals.Store{Ptr: pointer, Val: value.reg})
	return nil
}

func (b *bodyTranslator) selectOp() error {
	value, err := b.pop()
	if err != nil {
		return err
	}
	cond := b.condition(value)
	right, err := b.pop()
	if err != nil {
		return err
	}
	left, err := b.pop()
	if err != nil {
		return err
	}
	if left.typ != right.typ {
		return fmt.Errorf("select over a %s and a %s", left.typ, right.typ)
	}
	dst := b.push(left.typ)
	b.emit(&metals.If{
		Cond: cond,
		Then: []metals.Instr{&metals.Copy{Dst: dst, X: left.reg}},
		Else: []metals.Instr{&metals.Copy{Dst: dst, X: right.reg}},
	})
	return nil
}

// ---------------------------------------------------------------------------
// Helpers shared with the numeric opcodes
// ---------------------------------------------------------------------------

func (b *bodyTranslator) wantMemory(what string) error {
	if !b.t.hasMemory {
		return fmt.Errorf("%s, but the module declares no memory", what)
	}
	return nil
}

// floatLiteral renders a float constant the way METALS parses one back.
// Go's shortest round-trip form is what the printer emits for a constant
// it read, so producing it here keeps the text canonical.
func floatLiteral(v float64) string {
	// A non-finite constant has no digits to suffix. METALS spells these
	// `inf`, `-inf` and `nan`: `inf` and `nan` scan as words the way
	// `true` does, and `-inf` as a signed number, so all three read back
	// with no change to the format.
	if math.IsInf(v, 1) {
		return "inf"
	}
	if math.IsInf(v, -1) {
		return "-inf"
	}
	if math.IsNaN(v) {
		return "nan"
	}

	text := strconv.FormatFloat(v, 'g', -1, 64)
	// A whole number prints without a point, and const.f64 4 would then
	// read as an integer literal.
	for _, ch := range text {
		if ch == '.' || ch == 'e' || ch == 'E' {
			return text
		}
	}
	return text + ".0"
}
