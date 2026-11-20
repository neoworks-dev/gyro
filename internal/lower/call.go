package lower

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// lowerCall lowers a call: its callee, receiver and arguments, then the
// call itself. It returns the register holding the result, "" for a void
// call.
func (f *funcLowerer) lowerCall(call *ast.CallExpr) string {
	if f.isErrorNew(call) {
		return f.lowerErrorNew(call)
	}
	resolved := f.callInfo(call)

	if resolved.Enum != nil {
		return f.lowerVariant(call, resolved)
	}
	if resolved.Dyn != nil {
		return f.lowerDynCall(call, resolved)
	}
	if resolved.Method != nil {
		decl := resolved.Method.Decl
		if decl == nil {
			return f.lowerBuiltin(call, resolved)
		}
		if decl.Body == nil {
			return f.lowerNative(call, resolved, decl)
		}
		return f.lowerUserCall(call, resolved, decl)
	}

	// A call through a function value. The callee is evaluated without
	// the coerce step: sema resolves callees outside exprWith, so the
	// node itself carries no recorded type.
	fn := f.eval(call.Fun, nil)
	args := f.callArgs(call, resolved.Sig, nil)
	if resolved.Sig.Async {
		return f.startAsyncIndirect(fn, args, resolved.Sig)
	}
	if isVoid(resolved.Sig.Result) {
		f.emit(&metals.CallIndirect{Fn: fn, Args: args})
		return ""
	}
	out := f.newReg("r")
	f.emit(&metals.CallIndirect{Dst: out, Fn: fn, Args: args})
	return out
}

func (f *funcLowerer) lowerVariant(call *ast.CallExpr, resolved *sema.CallInfo) string {
	enum := resolved.Enum
	payload := enum.Variants[resolved.Variant].Payload
	args := make([]string, len(call.Args))
	for i, arg := range call.Args {
		var want types.Type
		if i < len(payload) {
			want = payload[i]
		}
		args[i] = f.lowerExpr(arg.Value, want)
	}
	named := f.l.metalsEnum(enum).(*metals.Named)
	out := f.newReg("variant")
	f.emit(&metals.MakeVariant{Dst: out, TypeName: named.Name, Variant: resolved.Variant, Args: args})
	return out
}

// callArgs lowers the arguments against the concrete signature, filling
// omitted trailing arguments from the declaration's defaults.
func (f *funcLowerer) callArgs(call *ast.CallExpr, sig *types.Func, decl *ast.FuncDecl) []string {
	args := make([]string, 0, len(sig.Params))
	pack := packIndex(sig)
	for i := range sig.Params {
		if i == pack {
			args = append(args, f.packArgs(call, sig.Params[i], i))
			continue
		}
		if i < len(call.Args) && decl != nil && decl.Params[i].Inout {
			args = append(args, f.inoutAddress(call.Args[i].Value))
			continue
		}
		if i < len(call.Args) {
			args = append(args, f.lowerExpr(call.Args[i].Value, sig.Params[i]))
			continue
		}
		if decl == nil || i >= len(decl.Params) || decl.Params[i].Default == nil {
			f.l.fail("@%s: a call misses argument %d and there is no default", f.fn.Name, i+1)
		}
		args = append(args, f.lowerExpr(decl.Params[i].Default, sig.Params[i]))
	}
	return args
}

// varargPack builds what a C variadic callee is handed: a pointer to the
// addresses of the arguments after the last named one, and how many
// there are. It is the same pair internal/cc builds at a C call site,
// because it is the same convention — METALS has no variadic call, and a
// callee that can only walk its extra arguments in order asking for one
// type at a time needs nothing more than addresses.
//
// The addresses are the caller's: what a `...anyptr` parameter takes is
// `&x as anyptr`, so the storage read through is a local of this
// function and outlives the call by construction. That is the part C
// leaves to the programmer.
func (f *funcLowerer) varargPack(call *ast.CallExpr, at int) []string {
	rest := call.Args[min(at, len(call.Args)):]

	count := f.newReg("va.count")
	f.emit(&metals.Const{Dst: count, Type: metals.TInt64, Value: strconv.Itoa(len(rest))})

	elem := &metals.Opt{Elem: &metals.AnyPtr{}}
	if len(rest) == 0 {
		none := f.newReg("va.slots")
		f.emit(&metals.Const{Dst: none, Type: varargSlotsType(), Null: true})
		return []string{none, count}
	}

	cells := make([]string, len(rest))
	for i, arg := range rest {
		address := f.lowerExpr(arg.Value, types.AnyPtr)
		held := f.newReg("va.addr")
		f.emit(&metals.OptWrap{Dst: held, X: address})
		cells[i] = held
	}

	value := f.newReg("va.args")
	f.emit(&metals.MakeArray{Dst: value, Elem: elem, Args: cells})
	storage := f.newReg("va.cells")
	f.emit(&metals.Slot{Dst: storage, Type: &metals.ArrayOf{Elem: elem, Len: int64(len(cells))}})
	f.emit(&metals.Store{Ptr: storage, Val: value})

	first := f.newReg("va.first")
	f.emit(&metals.ElemPtr{Dst: first, Ptr: storage})
	slots := f.newReg("va.slots")
	f.emit(&metals.OptWrap{Dst: slots, X: first})
	return []string{slots, count}
}

// packIndex is the position of a signature's variadic parameter, or -1.
func packIndex(sig *types.Func) int {
	if !sig.Variadic || len(sig.Params) == 0 {
		return -1
	}
	return len(sig.Params) - 1
}

// packArgs builds the slice a variadic parameter receives: an array of the
// remaining arguments, borrowed as a slice. A spread argument already is
// that slice and passes straight through, and a pack of any wraps each
// element on the way in.
func (f *funcLowerer) packArgs(call *ast.CallExpr, param types.Type, at int) string {
	rest := call.Args[min(at, len(call.Args)):]
	if len(rest) == 1 && rest[0].Spread {
		return f.lowerExpr(rest[0].Value, param)
	}

	slice, ok := param.(*types.Slice)
	if !ok {
		f.l.fail("@%s: a variadic parameter is not a slice", f.fn.Name)
	}
	elems := make([]string, len(rest))
	for i, arg := range rest {
		if isAnyType(slice.Elem) {
			elems[i] = f.wrapAny(f.lowerExpr(arg.Value, nil), f.typeOf(arg.Value))
			continue
		}
		elems[i] = f.lowerExpr(arg.Value, slice.Elem)
	}

	array := f.newReg("pack")
	f.emit(&metals.MakeArray{Dst: array, Elem: f.l.metalsType(slice.Elem), Args: elems})
	view := f.newReg("packed")
	f.emit(&metals.Slice{Dst: view, X: array})
	return view
}

// emitCall calls a function by name, or starts the call on a thread of its
// own when it is async.
func (f *funcLowerer) emitCall(name string, args []string, result types.Type, async bool) string {
	if async {
		return f.startAsync(name, args, result)
	}
	if isVoid(result) {
		f.emit(&metals.Call{Callee: name, Args: args})
		return ""
	}
	out := f.newReg("r")
	f.emit(&metals.Call{Dst: out, Callee: name, Args: args})
	return out
}

// ---------------------------------------------------------------------------
// User functions and methods
// ---------------------------------------------------------------------------

func (f *funcLowerer) lowerUserCall(call *ast.CallExpr, resolved *sema.CallInfo, decl *ast.FuncDecl) string {
	name := f.l.ensureFunc(decl, resolved.Bindings)

	var args []string
	owed := f.collectWriteBacks(func() {
		if decl.Recv != nil {
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				f.l.fail("@%s: a method call without a receiver expression", f.fn.Name)
			}
			args = append(args, f.methodReceiver(selector.X, resolved.Method))
		}
		args = append(args, f.callArgs(call, resolved.Sig, decl)...)
	})
	out := f.emitCall(name, args, resolved.Sig.Result, resolved.Sig.Async)
	f.settleWriteBacks(owed)
	return out
}

// inoutAddress is what an inout parameter receives: the address of the place
// the argument names, so that the callee changes it where it lies. sema
// refuses an argument that is not a place.
func (f *funcLowerer) inoutAddress(arg ast.Expr) string {
	target := f.placeOfAddressable(arg)
	if target == nil {
		f.l.fail("@%s: an inout argument is not a place", f.fn.Name)
	}
	if _, stored := target.typ.(*types.Optional); stored {
		if _, used := f.typeOf(arg).(*types.Optional); !used {
			return f.inoutNarrowed(arg, target)
		}
	}
	addr := target.addr()
	if addr == "" {
		f.l.fail("@%s: an inout argument has no address", f.fn.Name)
	}
	return addr
}

// inoutNarrowed passes a local the checker narrowed from `T?` to `T`. It is
// stored as the optional, and the payload has no address of its own, so the
// callee gets a cell holding the payload and what it leaves there is put
// back when the call returns. Nothing else can see the local meanwhile,
// which is what inout promises.
func (f *funcLowerer) inoutNarrowed(arg ast.Expr, target *place) string {
	payload := f.typeOf(arg)
	cell := f.newReg("inout")
	f.emit(&metals.Slot{Dst: cell, Type: f.l.metalsType(payload)})
	f.emit(&metals.Store{Ptr: cell, Val: f.narrowed(target.load(), target.typ, payload)})
	f.writeBacks = append(f.writeBacks, writeBack{cell: cell, target: target})
	return cell
}

// A writeBack is what a call owes an inout argument that went through a
// cell: the payload the callee left there, wrapped again and stored in
// the place it came from.
type writeBack struct {
	cell   string
	target *place
}

// collectWriteBacks runs the lowering of a call's receiver and arguments and
// returns what they owe once the call has returned. A call nested in an
// argument collects its own.
func (f *funcLowerer) collectWriteBacks(lowerArgs func()) []writeBack {
	outer := f.writeBacks
	f.writeBacks = nil
	lowerArgs()
	owed := f.writeBacks
	f.writeBacks = outer
	return owed
}

// settleWriteBacks pays what a call owes, once it has returned.
func (f *funcLowerer) settleWriteBacks(owed []writeBack) {
	for _, back := range owed {
		held := f.newReg("inout.v")
		f.emit(&metals.Load{Dst: held, Ptr: back.cell})
		wrapped := f.newReg("inout.o")
		f.emit(&metals.OptWrap{Dst: wrapped, X: held})
		back.target.store(wrapped)
	}
}

// methodReceiver produces the receiver argument: a pointer for inout self, a
// value for self, through an existing pointer either way.
func (f *funcLowerer) methodReceiver(recv ast.Expr, method *types.Method) string {
	recvType := f.typeOf(recv)

	if method.Recv == types.InoutRecv {
		if _, ok := recvType.(*types.Pointer); ok {
			return f.lowerExpr(recv, nil)
		}
		return f.inoutAddress(recv)
	}

	if pointer, ok := recvType.(*types.Pointer); ok {
		ptr := f.lowerExpr(recv, nil)
		return f.pointerPlace(ptr, pointer.Elem).load()
	}
	return f.lowerExpr(recv, nil)
}

// ---------------------------------------------------------------------------
// Runtime natives
// ---------------------------------------------------------------------------

func (f *funcLowerer) lowerNative(call *ast.CallExpr, resolved *sema.CallInfo, decl *ast.FuncDecl) string {
	module := f.l.moduleOf[decl]

	// A declaration in a module another language produced is a real
	// cross-module call: the callee lives in its own METALS module and
	// the linker resolves the name.
	if module != nil && module.Foreign != nil {
		f.l.ensureForeignImport(module.Foreign, decl.Name, resolved.Sig)
		var args []string
		if resolved.Sig.Variadic {
			named := resolved.Sig.Params[:len(resolved.Sig.Params)-1]
			for i, param := range named {
				args = append(args, f.lowerExpr(call.Args[i].Value, param))
			}
			args = append(args, f.varargPack(call, len(named))...)
		} else {
			args = f.callArgs(call, resolved.Sig, decl)
		}
		return f.emitCall(decl.Name, args, resolved.Sig.Result, resolved.Sig.Async)
	}

	if module != nil && module.Path == stdlib.Path("mem") {
		if out, ok := f.lowerMemIntrinsic(call, resolved, decl); ok {
			return out
		}
	}
	if module != nil && module.Path == stdlib.Path("unsafe") {
		if out, ok := f.lowerUnsafeIntrinsic(call, resolved, decl); ok {
			return out
		}
	}
	if module != nil && module.Path == stdlib.Path("math") {
		return f.lowerMathIntrinsic(call, decl)
	}
	if module != nil && module.Path == stdlib.Path("mem") && decl.Owner == "" && decl.Name == "show" {
		return f.lowerShow(call)
	}
	if module != nil && module.Path == stdlib.Path("stdlib") && decl.Owner == "" && isPrintName(decl.Name) {
		return f.lowerPrint(call, resolved, decl)
	}

	name := f.l.funcName(decl, resolved.Bindings)
	f.l.ensureNativeImport(name, decl, resolved.Sig)

	var args []string
	if decl.Recv != nil {
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			f.l.fail("@%s: a method call without a receiver expression", f.fn.Name)
		}
		args = append(args, f.nativeReceiver(selector.X))
	}
	args = append(args, f.callArgs(call, resolved.Sig, decl)...)
	return f.emitCall(name, args, resolved.Sig.Result, resolved.Sig.Async)
}

// nativeReceiver produces the receiver of a runtime method. A runtime
// object is a handle, which is already a reference, so `inout self` needs no
// pointer — but the receiver expression may still be one, as it is inside
// a function taking `*Array<T>`, and then it has to be loaded through.
func (f *funcLowerer) nativeReceiver(recv ast.Expr) string {
	if pointer, ok := f.typeOf(recv).(*types.Pointer); ok {
		ptr := f.lowerExpr(recv, nil)
		return f.pointerPlace(ptr, pointer.Elem).load()
	}
	return f.lowerExpr(recv, nil)
}

func isPrintName(name string) bool {
	return name == "print" || name == "println" || name == "eprint" || name == "eprintln"
}

// lowerPrint lowers the print family with display semantics: strings
// pass raw, a to_string method wins, a composite goes through its
// synthesized repr, and only the leaves reach a monomorphized runtime
// import.
func (f *funcLowerer) lowerPrint(call *ast.CallExpr, resolved *sema.CallInfo, decl *ast.FuncDecl) string {
	if len(call.Args) != 1 {
		f.l.fail("@%s: %s takes one argument", f.fn.Name, decl.Name)
	}
	arg := call.Args[0].Value
	argType := f.typeOf(arg)

	var value string
	displayType := argType
	switch {
	case f.l.isFormat(argType):
		return f.lowerStream(decl, f.lowerExpr(arg, nil))
	case isAnyType(argType) && (decl.Name == "print" || decl.Name == "eprint"):
		name := f.l.ensureAnyStream(decl.Name)
		value := f.lowerExpr(arg, nil)
		f.emit(&metals.Call{Callee: name, Args: []string{value}})
		return ""
	case isNullLiteral(arg):
		value = f.text("null")
		displayType = types.String
	case isStringType(argType):
		value = f.lowerExpr(arg, nil)
		displayType = types.String
	case f.toStringMethod(argType) != nil:
		value = f.callToString(arg, argType, f.toStringMethod(argType))
		displayType = types.String
	case f.l.reprFor(argType) != "":
		value = f.displayReg(f.lowerExpr(arg, nil), argType)
		displayType = types.String
	default:
		value = f.lowerExpr(arg, nil)
	}

	name := decl.Name + "<" + displayType.String() + ">"
	f.l.ensureImport(name, &metals.FnType{Params: []metals.Type{f.l.metalsType(displayType)}})
	f.emit(&metals.Call{Callee: name, Args: []string{value}})
	return ""
}

// mathUnary and mathBinary map the math module's names onto the METALS
// float operators. These are instructions rather than gyro:rt imports
// because each is one machine instruction or a handful, and because a
// producer that has them as opcodes — wasm does, so C does — would
// otherwise have to call out of the sandbox to round a number. Making
// them instructions means Gyro and C reach the same implementation.
var mathUnary = map[string]string{
	"sqrt": "fsqrt", "abs": "fabs",
	"ceil": "fceil", "floor": "ffloor",
	"trunc": "ftrunc", "round": "fnearest",
}

var mathBinary = map[string]string{
	"min": "fmin", "max": "fmax", "copysign": "fcopysign",
}

func (f *funcLowerer) lowerMathIntrinsic(call *ast.CallExpr, decl *ast.FuncDecl) string {
	if op, ok := mathUnary[decl.Name]; ok {
		x := f.lowerExpr(call.Args[0].Value, nil)
		out := f.newReg(decl.Name)
		f.emit(&metals.Un{Dst: out, Op: op, X: x})
		return out
	}
	if op, ok := mathBinary[decl.Name]; ok {
		x := f.lowerExpr(call.Args[0].Value, nil)
		y := f.lowerExpr(call.Args[1].Value, nil)
		out := f.newReg(decl.Name)
		f.emit(&metals.Bin{Dst: out, Op: op, A: x, B: y})
		return out
	}
	f.l.fail("@%s: math has no intrinsic %s", f.fn.Name, decl.Name)
	return ""
}

// lowerMemIntrinsic lowers the mem intrinsics the compiler answers
// itself: the layout of a type. It reports false for the ones the runtime
// supplies, which lower as ordinary natives.
func (f *funcLowerer) lowerMemIntrinsic(call *ast.CallExpr, resolved *sema.CallInfo, decl *ast.FuncDecl) (string, bool) {
	switch decl.Name {
	case "start_thread":
		return f.startThread(call), true
	case "end_thread":
		f.emit(&metals.ThreadExit{})
		return "", true
	case "size", "align":
		bound := resolved.Bindings["T"]
		if bound == nil {
			f.l.fail("@%s: %s without a bound T", f.fn.Name, decl.Name)
		}
		lowered := f.l.metalsType(bound)
		layout := metals.NewLayout(f.l.module, func(format string, args ...any) {
			f.l.fail("@%s: %s: "+format, append([]any{f.fn.Name, decl.Name}, args...)...)
		})
		answer := layout.Size(lowered)
		if decl.Name == "align" {
			answer = layout.Align(lowered)
		}
		out := f.newReg(decl.Name)
		f.emit(&metals.Const{Dst: out, Type: metals.TUsize, Value: strconv.Itoa(answer)})
		return out, true
	}
	return "", false
}

// lowerUnsafeIntrinsic lowers the unsafe intrinsics the compiler answers
// itself: putting a type back on an address, moving one along, a closure's
// environment, and a system call. It reports false for the rest of the
// module, which lowers as ordinary natives.
func (f *funcLowerer) lowerUnsafeIntrinsic(call *ast.CallExpr, resolved *sema.CallInfo, decl *ast.FuncDecl) (string, bool) {
	switch decl.Name {
	case "env_of", "with_env":
		return f.lowerEnvIntrinsic(call, resolved, decl.Name), true
	case "thread_local":
		return f.threadLocal(resolved.Bindings), true
	case "syscall0", "syscall1", "syscall2", "syscall3", "syscall4", "syscall5", "syscall6":
		// Each argument lowers at the type its own parameter was bound to,
		// so a pointer stays a pointer all the way into the instruction.
		number := f.lowerExpr(call.Args[0].Value, types.USize)
		var args []string
		for index, arg := range call.Args[1:] {
			args = append(args, f.lowerExpr(arg.Value, resolved.Sig.Params[index+1]))
		}
		out := f.newReg("sys")
		f.emit(&metals.Syscall{Dst: out, Number: number, Args: args})
		return out, true
	case "futex_wait", "futex_wake":
		return f.lowerFutex(call, resolved, decl.Name), true
	case "atomic_load", "atomic_store", "atomic_swap", "atomic_cas", "atomic_add", "atomic_sub",
		"atomic_and", "atomic_or", "atomic_xor", "atomic_fence":
		return f.lowerAtomicIntrinsic(call, resolved, decl.Name), true
	case "slice_from_raw":
		bound := resolved.Bindings["T"]
		if bound == nil {
			f.l.fail("@%s: slice_from_raw without a bound T", f.fn.Name)
		}
		target := f.l.metalsType(&types.Pointer{Elem: bound})
		address := f.lowerExpr(call.Args[0].Value, types.AnyPtr)
		length := f.lowerExpr(call.Args[1].Value, types.USize)
		first := f.newReg("first")
		f.emit(&metals.PtrCast{Dst: first, X: address, To: target})
		out := f.newReg("raw")
		f.emit(&metals.SliceFrom{Dst: out, Ptr: first, Len: length})
		return out, true
	case "offset":
		// The address `bytes` past base is a pointer to bytes moved along by
		// that many elements, and then forgotten again.
		address := f.lowerExpr(call.Args[0].Value, types.AnyPtr)
		bytes := f.lowerExpr(call.Args[1].Value, types.USize)
		typed := f.newReg("raw")
		f.emit(&metals.PtrCast{Dst: typed, X: address, To: &metals.Ptr{Elem: metals.TUint8}})
		moved := f.newReg("moved")
		f.emit(&metals.PtrOffset{Dst: moved, Ptr: typed, N: bytes})
		out := f.newReg("shifted")
		f.emit(&metals.PtrErase{Dst: out, X: moved})
		return out, true
	case "ptr_to_int":
		address := f.lowerExpr(call.Args[0].Value, types.AnyPtr)
		out := f.newReg("addr")
		f.emit(&metals.PtrAddr{Dst: out, X: address})
		return out, true
	case "ptr_from_int":
		bound := resolved.Bindings["T"]
		if bound == nil {
			f.l.fail("@%s: ptr_from_int without a bound T", f.fn.Name)
		}
		target := f.l.metalsType(&types.Pointer{Elem: bound})
		number := f.lowerExpr(call.Args[0].Value, types.USize)
		address := f.newReg("claimed")
		f.emit(&metals.PtrFrom{Dst: address, X: number, To: &metals.AnyPtr{}})
		out := f.newReg("restored")
		f.emit(&metals.PtrCast{Dst: out, X: address, To: target})
		return out, true
	case "cast":
		bound := resolved.Bindings["T"]
		if bound == nil {
			f.l.fail("@%s: cast without a bound T", f.fn.Name)
		}
		target := f.l.metalsType(&types.Pointer{Elem: bound})
		address := f.lowerExpr(call.Args[0].Value, types.AnyPtr)
		out := f.newReg("restored")
		f.emit(&metals.PtrCast{Dst: out, X: address, To: target})
		return out, true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Built-in method surface
// ---------------------------------------------------------------------------

func (f *funcLowerer) lowerBuiltin(call *ast.CallExpr, resolved *sema.CallInfo) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		f.l.fail("@%s: a built-in method call without a receiver", f.fn.Name)
	}
	recv := selector.X
	recvType := f.typeOf(recv)

	switch resolved.Method.Name {
	case "close":
		ch := f.lowerExpr(recv, nil)
		f.closeChan(ch)
		return ""

	case "len":
		value := f.lowerExpr(recv, nil)
		out := f.newReg("len")
		f.emit(&metals.Len{Dst: out, X: value})
		return out

	case "compare":
		left := f.lowerExpr(recv, nil)
		right := f.lowerExpr(call.Args[0].Value, recvType)
		// Ordering strings walks their bytes, so it belongs to the
		// runtime; the numeric comparisons are two instructions and are
		// synthesized instead.
		if types.IsString(recvType) {
			f.l.ensureImport("string::compare", &metals.FnType{
				Params: []metals.Type{metals.TStr, metals.TStr},
				Result: metals.TInt32,
			})
			return f.emitCall("string::compare", []string{left, right}, types.Int32, false)
		}
		return f.emitCall(f.ensureCompareFn(recvType), []string{left, right}, types.Int32, false)

	case "hash":
		value := f.lowerExpr(recv, nil)
		return f.emitCall(f.l.hashFor(recvType), []string{value}, types.Uint64, false)

	case "eq":
		left := f.lowerExpr(recv, nil)
		right := f.lowerExpr(call.Args[0].Value, recvType)
		return f.equalValues(left, right, types.Default(recvType))

	case "add", "sub", "mul", "div", "rem":
		left := f.lowerExpr(recv, nil)
		right := f.lowerExpr(call.Args[0].Value, recvType)
		op := map[string]token.Kind{
			"add": token.Plus, "sub": token.Minus, "mul": token.Star, "div": token.Slash, "rem": token.Percent,
		}[resolved.Method.Name]
		out := f.newReg(resolved.Method.Name)
		f.emit(&metals.Bin{Dst: out, Op: binOpFor(op, types.Default(recvType), f), A: left, B: right})
		return out

	case "neg":
		value := f.lowerExpr(recv, nil)
		out := f.newReg("neg")
		f.emit(&metals.Un{Dst: out, Op: "neg", X: value})
		return out

	case "message":
		name := f.ensureErrorMessage()
		value := f.lowerExpr(recv, nil)
		return f.emitCall(name, []string{value}, types.String, false)
	}
	f.l.fail("@%s: cannot lower the built-in method %s", f.fn.Name, resolved.Method.Name)
	return ""
}

// ensureCompareFn synthesizes the three-way compare the ordered
// primitives carry as a method.
func (f *funcLowerer) ensureCompareFn(t types.Type) string {
	t = types.Default(t)
	name := t.String() + "::compare"
	if f.l.lowered[name] {
		return name
	}
	f.l.lowered[name] = true

	lowered := f.l.metalsType(t)
	fn := &metals.Func{
		Name:   name,
		Params: []metals.Param{{Name: "a", Type: lowered}, {Name: "b", Type: lowered}},
		Result: metals.TInt32,
	}
	if types.IsFloat(t) {
		// compare is a total order, unlike the IEEE operators: NaN is
		// above every number and equal to itself, so a comparator built
		// from compare stays consistent. `x != x` is the NaN test.
		fn.Body = append(fn.Body,
			&metals.Bin{Dst: "a_nan", Op: "ne", A: "a", B: "a"},
			&metals.Bin{Dst: "b_nan", Op: "ne", A: "b", B: "b"},
			&metals.If{Cond: "a_nan", Then: []metals.Instr{
				&metals.If{Cond: "b_nan", Then: []metals.Instr{
					&metals.Const{Dst: "both_nan", Type: metals.TInt32, Value: "0"},
					&metals.Return{Val: "both_nan"},
				}},
				&metals.Const{Dst: "nan_above", Type: metals.TInt32, Value: "1"},
				&metals.Return{Val: "nan_above"},
			}},
			&metals.If{Cond: "b_nan", Then: []metals.Instr{
				&metals.Const{Dst: "nan_below", Type: metals.TInt32, Value: "-1"},
				&metals.Return{Val: "nan_below"},
			}},
		)
	}
	fn.Body = append(fn.Body,
		&metals.Bin{Dst: "below", Op: "lt", A: "a", B: "b"},
		&metals.If{Cond: "below", Then: []metals.Instr{
			&metals.Const{Dst: "minus", Type: metals.TInt32, Value: "-1"},
			&metals.Return{Val: "minus"},
		}},
		&metals.Bin{Dst: "above", Op: "gt", A: "a", B: "b"},
		&metals.If{Cond: "above", Then: []metals.Instr{
			&metals.Const{Dst: "plus", Type: metals.TInt32, Value: "1"},
			&metals.Return{Val: "plus"},
		}},
		&metals.Const{Dst: "zero", Type: metals.TInt32, Value: "0"},
		&metals.Return{Val: "zero"},
	)
	f.l.module.Funcs = append(f.l.module.Funcs, fn)
	return name
}
