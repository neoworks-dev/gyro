package lower

import (
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/sema"
)

// prepareAtomicIntrinsic lowers unsafe's atomic_* functions, each of which
// is one METALS atomic instruction. The pointer lowers at its own type, so
// the cell is whatever T was bound to.
func (f *funcLowerer) prepareAtomicIntrinsic(call *ast.CallExpr, resolved *sema.CallInfo, name string) func() string {
	if name == "atomic_fence" {
		return func() string {
			f.emit(&metals.Fence{})
			return ""
		}
	}

	bound := resolved.Bindings["T"]
	if bound == nil {
		f.l.fail("@%s: %s without a bound T", f.fn.Name, name)
	}
	op := strings.TrimPrefix(name, "atomic_")
	cell := f.l.metalsType(bound)
	if !atomicCell(cell, op) {
		f.l.fail("@%s: %s needs %s, not %s", f.fn.Name, name, atomicCellWanted(op), bound)
	}

	args := make([]string, len(call.Args))
	for index, arg := range call.Args {
		args[index] = f.lowerExpr(arg.Value, resolved.Sig.Params[index])
	}
	return func() string {
		switch op {
		case "store":
			f.emit(&metals.AtomicStore{Ptr: args[0], Val: args[1]})
			return ""
		case "load":
			out := f.newReg("atomic")
			f.emit(&metals.AtomicLoad{Dst: out, Ptr: args[0]})
			return out
		case "cas":
			out := f.newReg("atomic")
			f.emit(&metals.AtomicCas{Dst: out, Ptr: args[0], Expected: args[1], New: args[2]})
			return out
		}
		out := f.newReg("atomic")
		f.emit(&metals.AtomicRMW{Dst: out, Op: op, Ptr: args[0], Val: args[1]})
		return out
	}
}

// atomicCell reports whether a cell of this type can take the operation,
// which is what the verifier would otherwise reject with a message about
// METALS and not about the Gyro type that caused it.
func atomicCell(cell metals.Type, op string) bool {
	if prim, ok := cell.(*metals.Prim); ok {
		switch prim.Kind {
		case metals.I8, metals.I16, metals.I32, metals.I64, metals.Isize,
			metals.U8, metals.U16, metals.U32, metals.U64, metals.Usize:
			return true
		case metals.Bool:
			return !atomicArithmetic(op)
		}
		return false
	}
	if atomicArithmetic(op) {
		return false
	}
	switch cell := cell.(type) {
	case *metals.Ptr, *metals.AnyPtr:
		return true
	case *metals.Opt:
		return metals.NicheOpt(cell)
	}
	return false
}

func atomicArithmetic(op string) bool {
	switch op {
	case "add", "sub", "and", "or", "xor":
		return true
	}
	return false
}

func atomicCellWanted(op string) string {
	if atomicArithmetic(op) {
		return "an integer"
	}
	return "an integer, a bool or a pointer"
}

// prepareFutex lowers futex_wait and futex_wake to the futex system call,
// private to this process, with the operation and the unused arguments as
// constants.
func (f *funcLowerer) prepareFutex(call *ast.CallExpr, resolved *sema.CallInfo, name string) func() string {
	word := f.lowerExpr(call.Args[0].Value, resolved.Sig.Params[0])
	argument := f.lowerExpr(call.Args[1].Value, resolved.Sig.Params[1])
	return func() string {
		constant := func(typ metals.Type, value string) string {
			reg := f.newReg("futex")
			f.emit(&metals.Const{Dst: reg, Type: typ, Value: value})
			return reg
		}
		number := constant(metals.TUsize, "202")
		var args []string
		if name == "futex_wait" {
			// FUTEX_WAIT_PRIVATE, the value the word must still hold, and
			// no timeout.
			args = []string{word, constant(metals.TInt64, "128"), argument, constant(metals.TInt64, "0")}
		} else {
			// FUTEX_WAKE_PRIVATE and how many to wake.
			args = []string{word, constant(metals.TInt64, "129"), argument}
		}
		f.emit(&metals.Syscall{Dst: f.newReg("futex"), Number: number, Args: args})
		return ""
	}
}
