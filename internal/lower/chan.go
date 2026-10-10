package lower

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/types"
)

// A channel is library code. A `chan<T>` lowers to an address of the state
// stdlib's chan_* functions keep, and the operations on it are calls to
// them: they move the bytes of an element, so each element travels as the
// address of a cell in the caller's frame, written and read in storage
// representation like any other memory. What lowering supplies is the part
// that knows T: its size, and the cells.

// chanFunc is the stdlib function of a channel operation.
func (f *funcLowerer) chanFunc(name string) string {
	return f.l.ensureFunc(f.l.leafDecl(name), nil)
}

// elemCell makes a cell of the channel's element type in this frame and
// answers it and its erased address. Slot zeroes the cell each time it runs.
func (f *funcLowerer) elemCell(elem types.Type) (cell, erased string) {
	cell = f.newReg("chan.cell")
	f.emit(&metals.Slot{Dst: cell, Type: f.l.metalsType(elem)})
	erased = f.newReg("chan.at")
	f.emit(&metals.PtrErase{Dst: erased, X: cell})
	return cell, erased
}

// chanElem is the element type of the channel an expression has.
func (f *funcLowerer) chanElem(e ast.Expr) types.Type {
	chanType, ok := f.typeOf(e).(*types.Chan)
	if !ok {
		f.l.fail("@%s: a channel operation on a non-channel", f.fn.Name)
	}
	return chanType.Elem
}

func (f *funcLowerer) evalChanLit(e *ast.ChanLit) string {
	chanType, ok := f.typeOf(e).(*types.Chan)
	if !ok {
		f.l.fail("@%s: a channel literal without a channel type", f.fn.Name)
	}
	capacity := ""
	if e.Cap != nil {
		capacity = f.lowerExpr(e.Cap, types.USize)
		if !types.Identical(types.Default(f.typeOf(e.Cap)), types.USize) {
			converted := f.newReg("cap")
			f.emit(&metals.Conv{Dst: converted, X: capacity, To: metals.TUsize})
			capacity = converted
		}
	} else {
		capacity = f.newReg("cap")
		f.emit(&metals.Const{Dst: capacity, Type: metals.TUsize, Value: "0"})
	}
	size := f.sizeConst(f.l.metalsType(chanType.Elem))
	out := f.newReg("ch")
	f.emit(&metals.Call{Dst: out, Callee: f.chanFunc("chan_new"), Args: []string{capacity, size}})
	return out
}

func (f *funcLowerer) sendStmt(s *ast.SendStmt) {
	elem := f.chanElem(s.Chan)
	ch := f.lowerExpr(s.Chan, nil)
	value := f.lowerExpr(s.Value, elem)
	cell, erased := f.elemCell(elem)
	f.emit(&metals.Store{Ptr: cell, Val: value})
	f.emit(&metals.Call{Callee: f.chanFunc("chan_send"), Args: []string{ch, erased}})
}

func (f *funcLowerer) closeChan(ch string) {
	f.emit(&metals.Call{Callee: f.chanFunc("chan_close"), Args: []string{ch}})
}

// recvPair lowers a receive into its (element, open) tuple register.
func (f *funcLowerer) recvPair(recv *ast.RecvExpr) string {
	elem := f.chanElem(recv.Chan)
	ch := f.lowerExpr(recv.Chan, nil)
	return f.receive(ch, elem)
}

// receive waits for an element of a channel and builds the tuple of it and
// whether the channel was open. On a closed channel the cell stays zeroed,
// which is why the language makes reading it a compile error.
func (f *funcLowerer) receive(ch string, elem types.Type) string {
	cell, erased := f.elemCell(elem)
	open := f.newReg("open")
	f.emit(&metals.Call{Dst: open, Callee: f.chanFunc("chan_recv"), Args: []string{ch, erased}})
	value := f.newReg("elem")
	f.emit(&metals.Load{Dst: value, Ptr: cell})
	pair := f.newReg("recv")
	f.emit(&metals.MakeTuple{Dst: pair, Args: []string{value, open}})
	return pair
}

// A select polls: each arm gets a try of its operation, in source order,
// and the first that happens runs its body. With an else the select ends
// there; without, it sleeps a moment and tries again. The channels and the
// values to send are evaluated once, before the first try.
type selectArm struct {
	send bool
	ch   string
	// cell holds the value being sent, or receives the element; erased is
	// its address.
	cell, erased string
	arm          ast.SelectArm
	// name is the register an element lands in, for an arm that binds or
	// assigns one.
	elem types.Type
}

func (f *funcLowerer) selectStmt(s *ast.SelectStmt) {
	arms := make([]*selectArm, len(s.Arms))
	for index, arm := range s.Arms {
		arms[index] = f.prepareSelectArm(arm)
	}

	end := f.newLabel("select.end.")
	top := f.newLabel("select.top.")
	loop := f.nested(func() {
		for _, arm := range arms {
			f.trySelectArm(arm, end)
		}
		if s.Else != nil {
			f.stmtScoped(s.Else)
			f.emit(&metals.Br{Label: end})
			return
		}
		f.emit(&metals.Call{Callee: f.chanFunc("select_pause")})
		f.emit(&metals.Br{Label: top})
	})
	f.emit(&metals.Block{Label: end, Body: []metals.Instr{
		&metals.Loop{Label: top, Body: loop},
	}})
}

func (f *funcLowerer) stmtScoped(s ast.Stmt) {
	f.pushScope()
	f.stmt(s)
	f.popScope()
}

func (f *funcLowerer) prepareSelectArm(arm ast.SelectArm) *selectArm {
	out := &selectArm{arm: arm}
	switch comm := arm.Comm.(type) {
	case *ast.SendStmt:
		out.send = true
		out.elem = f.chanElem(comm.Chan)
		out.ch = f.lowerExpr(comm.Chan, nil)
		value := f.lowerExpr(comm.Value, out.elem)
		out.cell, out.erased = f.elemCell(out.elem)
		f.emit(&metals.Store{Ptr: out.cell, Val: value})
	case *ast.VarDecl:
		recv, ok := comm.Value.(*ast.RecvExpr)
		if !ok || len(comm.Names) != 1 {
			f.l.fail("@%s: a select binding is one name receiving", f.fn.Name)
		}
		out.elem = f.chanElem(recv.Chan)
		out.ch = f.lowerExpr(recv.Chan, nil)
		out.cell, out.erased = f.elemCell(out.elem)
	case *ast.AssignStmt:
		recv, ok := comm.Rhs.(*ast.RecvExpr)
		if !ok || len(comm.Lhs) != 1 {
			f.l.fail("@%s: a select assignment is one target receiving", f.fn.Name)
		}
		out.elem = f.chanElem(recv.Chan)
		out.ch = f.lowerExpr(recv.Chan, nil)
		out.cell, out.erased = f.elemCell(out.elem)
	case *ast.ExprStmt:
		recv, ok := comm.X.(*ast.RecvExpr)
		if !ok {
			f.l.fail("@%s: a select arm waits on a channel operation", f.fn.Name)
		}
		out.elem = f.chanElem(recv.Chan)
		out.ch = f.lowerExpr(recv.Chan, nil)
		out.cell, out.erased = f.elemCell(out.elem)
	default:
		f.l.fail("@%s: cannot lower this select arm", f.fn.Name)
	}
	return out
}

// trySelectArm emits one arm's try and, if it happened, the arm's body and
// a jump out of the select.
func (f *funcLowerer) trySelectArm(arm *selectArm, end string) {
	name := "chan_try_recv"
	if arm.send {
		name = "chan_try_send"
	}
	fired := f.newReg("fired")
	f.emit(&metals.Call{Dst: fired, Callee: f.chanFunc(name), Args: []string{arm.ch, arm.erased}})

	body := f.nested(func() {
		f.pushScope()
		switch comm := arm.arm.Comm.(type) {
		case *ast.VarDecl:
			got := f.newReg(comm.Names[0].Name)
			f.emit(&metals.Load{Dst: got, Ptr: arm.cell})
			if declared := f.localType(comm, comm.Names[0].Name); declared != nil {
				f.bind(comm.Names[0].Name, &local{reg: got, typ: declared, env: -1})
			}
		case *ast.AssignStmt:
			got := f.newReg("got")
			f.emit(&metals.Load{Dst: got, Ptr: arm.cell})
			if target := f.placeOf(comm.Lhs[0]); target != nil {
				target.store(got)
			}
		}
		f.stmt(arm.arm.Body)
		f.popScope()
		f.emit(&metals.Br{Label: end})
	})
	f.emit(&metals.If{Cond: fired, Then: body})
}
