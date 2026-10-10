package lower

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/sema"
)

// A fn value is two words, { code, env } (metals.Layout says so), and mem's
// keep has to read the second and write another. Neither is an instruction:
// a fn value is stored in a slot and viewed as the two words it is, the way
// a producer that knows a layout may. They are private to mem; the language
// has no surface for raw memory.

func envWord() metals.Type { return &metals.Opt{Elem: &metals.AnyPtr{}} }

// fnWords stores a fn value in a slot and answers the slot and a pointer to
// its environment word.
func (f *funcLowerer) fnWords(fn string, fnType metals.Type) (slot, envPtr string) {
	slot = f.newReg("fnval")
	f.emit(&metals.Slot{Dst: slot, Type: fnType})
	f.emit(&metals.Store{Ptr: slot, Val: fn})

	erased := f.newReg("fnval.a")
	f.emit(&metals.PtrErase{Dst: erased, X: slot})
	words := f.newReg("fnval.w")
	f.emit(&metals.PtrCast{Dst: words, X: erased, To: &metals.Ptr{Elem: &metals.ArrayOf{Elem: envWord(), Len: 2}}})
	first := f.newReg("fnval.code")
	f.emit(&metals.ElemPtr{Dst: first, Ptr: words})
	one := f.newReg("one")
	f.emit(&metals.Const{Dst: one, Type: metals.TUsize, Value: "1"})
	envPtr = f.newReg("fnval.env")
	f.emit(&metals.PtrOffset{Dst: envPtr, Ptr: first, N: one})
	return slot, envPtr
}

// prepareEnvIntrinsic lowers unsafe's env_of and with_env.
func (f *funcLowerer) prepareEnvIntrinsic(call *ast.CallExpr, resolved *sema.CallInfo, name string) func() string {
	bound := resolved.Bindings["F"]
	if bound == nil {
		f.l.fail("@%s: %s without a bound F", f.fn.Name, name)
	}
	fnType := f.l.metalsType(bound)
	fn := f.lowerExpr(call.Args[0].Value, nil)

	if name == "env_of" {
		return func() string {
			_, envPtr := f.fnWords(fn, fnType)
			out := f.newReg("env")
			f.emit(&metals.Load{Dst: out, Ptr: envPtr})
			return out
		}
	}
	env := f.lowerExpr(call.Args[1].Value, resolved.Sig.Params[1])
	return func() string {
		slot, envPtr := f.fnWords(fn, fnType)
		f.emit(&metals.Store{Ptr: envPtr, Val: env})
		out := f.newReg("rebound")
		f.emit(&metals.Load{Dst: out, Ptr: slot})
		return out
	}
}
