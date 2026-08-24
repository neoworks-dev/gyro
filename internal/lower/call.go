package lower

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/types"
)

// prepareCall evaluates a call's callee, receiver and arguments and
// returns the function that emits the call itself. Splitting the two is
// what gives defer its semantics: arguments evaluate at registration,
// the call runs at block exit. Ordinary calls invoke the result
// immediately. The emitted call returns its destination register, "" for
// a void call.
func (f *funcLowerer) prepareCall(call *ast.CallExpr) func() string {
	resolved := f.callInfo(call)

	if resolved.Enum != nil {
		return f.prepareVariant(call, resolved)
	}
	if resolved.Method != nil {
		decl := resolved.Method.Decl
		if decl == nil {
			return f.prepareBuiltin(call, resolved)
		}
		if decl.Body == nil {
			return f.prepareNative(call, resolved, decl)
		}
		return f.prepareUserCall(call, resolved, decl)
	}

	// A call through a function value. The callee is evaluated without
	// the coerce step: sema resolves callees outside exprWith, so the
	// node itself carries no recorded type.
	fn := f.eval(call.Fun, nil)
	args := f.callArgs(call, resolved.Sig, nil)
	return func() string {
		if isVoid(resolved.Sig.Result) && !resolved.Sig.Async {
			f.emit(&metals.CallIndirect{Fn: fn, Args: args})
			return ""
		}
		out := f.newReg("r")
		f.emit(&metals.CallIndirect{Dst: out, Fn: fn, Args: args})
		return out
	}
}

func (f *funcLowerer) prepareVariant(call *ast.CallExpr, resolved *sema.CallInfo) func() string {
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
	variant := resolved.Variant
	return func() string {
		out := f.newReg("variant")
		f.emit(&metals.MakeVariant{Dst: out, TypeName: named.Name, Variant: variant, Args: args})
		return out
	}
}

// callArgs lowers the arguments against the concrete signature, filling
// omitted trailing arguments from the declaration's defaults.
func (f *funcLowerer) callArgs(call *ast.CallExpr, sig *types.Func, decl *ast.FuncDecl) []string {
	args := make([]string, 0, len(sig.Params))
	for i := range sig.Params {
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

func (f *funcLowerer) emitCall(name string, args []string, result types.Type, async bool) func() string {
	return func() string {
		if isVoid(result) && !async {
			f.emit(&metals.Call{Callee: name, Args: args})
			return ""
		}
		out := f.newReg("r")
		f.emit(&metals.Call{Dst: out, Callee: name, Args: args})
		return out
	}
}

// ---------------------------------------------------------------------------
// User functions and methods
// ---------------------------------------------------------------------------

func (f *funcLowerer) prepareUserCall(call *ast.CallExpr, resolved *sema.CallInfo, decl *ast.FuncDecl) func() string {
	name := f.l.ensureFunc(decl, resolved.Bindings)

	var args []string
	if decl.Recv != nil {
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			f.l.fail("@%s: a method call without a receiver expression", f.fn.Name)
		}
		args = append(args, f.methodReceiver(selector.X, resolved.Method))
	}
	args = append(args, f.callArgs(call, resolved.Sig, decl)...)
	return f.emitCall(name, args, resolved.Sig.Result, resolved.Sig.Async)
}

// methodReceiver produces the receiver argument: a pointer for *self, a
// value for self, through an existing pointer either way.
func (f *funcLowerer) methodReceiver(recv ast.Expr, method *types.Method) string {
	recvType := f.typeOf(recv)

	if method.Recv == types.PointerRecv {
		if _, ok := recvType.(*types.Pointer); ok {
			return f.lowerExpr(recv, nil)
		}
		if target := f.placeOfAddressable(recv); target != nil {
			if addr := target.addr(); addr != "" {
				return addr
			}
		}
		// A mutating method on an rvalue: give it a cell to scribble on.
		value := f.lowerExpr(recv, nil)
		slot := f.newReg("recv")
		f.emit(&metals.Slot{Dst: slot, Type: f.l.metalsType(recvType)})
		f.emit(&metals.Store{Ptr: slot, Val: value})
		return slot
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

func (f *funcLowerer) prepareNative(call *ast.CallExpr, resolved *sema.CallInfo, decl *ast.FuncDecl) func() string {
	module := f.l.moduleOf[decl]
	if module != nil && module.Path == stdlib.Path("mem") {
		return f.prepareMemIntrinsic(call, resolved, decl)
	}
	if module != nil && module.Path == stdlib.Path("stdlib") && decl.Owner == "" && isPrintName(decl.Name) {
		return f.preparePrint(call, resolved, decl)
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
// object is a handle, which is already a reference, so `*self` needs no
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
	return name == "print" || name == "println" || name == "eprintln"
}

// preparePrint lowers the print family with display semantics: strings
// pass raw, a to_string method wins, a composite goes through its
// synthesized repr, and only the leaves reach a monomorphized runtime
// import.
func (f *funcLowerer) preparePrint(call *ast.CallExpr, resolved *sema.CallInfo, decl *ast.FuncDecl) func() string {
	if len(call.Args) != 1 {
		f.l.fail("@%s: %s takes one argument", f.fn.Name, decl.Name)
	}
	arg := call.Args[0].Value
	argType := f.typeOf(arg)

	var value string
	displayType := argType
	switch {
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
	args := []string{value}
	return func() string {
		f.emit(&metals.Call{Callee: name, Args: args})
		return ""
	}
}

func (f *funcLowerer) prepareMemIntrinsic(call *ast.CallExpr, resolved *sema.CallInfo, decl *ast.FuncDecl) func() string {
	switch decl.Name {
	case "alloc":
		bound := resolved.Bindings["T"]
		if bound == nil {
			f.l.fail("@%s: alloc without a bound T", f.fn.Name)
		}
		elem := f.l.metalsType(bound)
		return func() string {
			out := f.newReg("p")
			f.emit(&metals.New{Dst: out, Type: elem})
			return out
		}
	case "free":
		ptr := f.lowerExpr(call.Args[0].Value, nil)
		return func() string {
			f.emit(&metals.Free{X: ptr})
			return ""
		}
	case "size":
		// The interpreter answers 8 for every type; the lowering bakes in
		// the same placeholder until layout exists.
		return func() string {
			out := f.newReg("size")
			f.emit(&metals.Const{Dst: out, Type: metals.TUsize, Value: "8"})
			return out
		}
	}
	f.l.fail("@%s: unknown mem intrinsic %s", f.fn.Name, decl.Name)
	return nil
}

// ---------------------------------------------------------------------------
// Built-in method surface
// ---------------------------------------------------------------------------

func (f *funcLowerer) prepareBuiltin(call *ast.CallExpr, resolved *sema.CallInfo) func() string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		f.l.fail("@%s: a built-in method call without a receiver", f.fn.Name)
	}
	recv := selector.X
	recvType := f.typeOf(recv)

	switch resolved.Method.Name {
	case "close":
		ch := f.lowerExpr(recv, nil)
		return func() string {
			f.emit(&metals.ChanClose{Ch: ch})
			return ""
		}

	case "len":
		value := f.lowerExpr(recv, nil)
		return func() string {
			out := f.newReg("len")
			f.emit(&metals.Len{Dst: out, X: value})
			return out
		}

	case "runes":
		value := f.lowerExpr(recv, nil)
		f.l.ensureImport("string::runes", &metals.FnType{
			Params: []metals.Type{metals.TStr},
			Result: &metals.SliceOf{Elem: metals.TRune},
		})
		return f.emitCall("string::runes", []string{value}, &types.Slice{Elem: types.Rune}, false)

	case "bytes":
		value := f.lowerExpr(recv, nil)
		f.l.ensureImport("string::bytes", &metals.FnType{
			Params: []metals.Type{metals.TStr},
			Result: &metals.SliceOf{Elem: metals.TUint8},
		})
		return f.emitCall("string::bytes", []string{value}, &types.Slice{Elem: types.Uint8}, false)

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

	case "message":
		name := f.ensureErrorMessage()
		value := f.lowerExpr(recv, nil)
		return f.emitCall(name, []string{value}, types.String, false)
	}
	f.l.fail("@%s: cannot lower the built-in method %s", f.fn.Name, resolved.Method.Name)
	return nil
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
	fn.Body = []metals.Instr{
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
	}
	f.l.module.Funcs = append(f.l.module.Funcs, fn)
	return name
}

// ensureErrorMessage synthesizes the dispatch behind err.message(): a
// switch over the closed %error sum calling each concrete message method.
func (f *funcLowerer) ensureErrorMessage() string {
	const name = "error::message"
	if f.l.lowered[name] {
		return name
	}
	f.l.lowered[name] = true
	f.l.usesError = true

	fn := &metals.Func{
		Name:   name,
		Params: []metals.Param{{Name: "e", Type: &metals.Named{Name: "error"}}},
		Result: metals.TStr,
	}

	sw := &metals.Switch{X: "tag"}
	sw.Cases = append(sw.Cases, metals.SwitchCase{Value: 0, Body: []metals.Instr{
		&metals.VariantField{Dst: "native", X: "e", Variant: 0, Index: 0},
		&metals.Return{Val: "native"},
	}})

	for _, errType := range f.l.errTypes {
		index := f.l.errVariant[errType]
		method := errType.Methods["message"]
		callee := f.l.ensureFunc(method.Decl, nil)
		f.l.metalsStruct(errType)

		body := []metals.Instr{
			&metals.VariantField{Dst: "v", X: "e", Variant: index, Index: 0},
		}
		receiver := "v"
		if method.Recv == types.PointerRecv {
			body = append(body,
				&metals.Slot{Dst: "cell", Type: f.l.metalsType(errType)},
				&metals.Store{Ptr: "cell", Val: "v"},
			)
			receiver = "cell"
		}
		body = append(body,
			&metals.Call{Dst: "msg", Callee: callee, Args: []string{receiver}},
			&metals.Return{Val: "msg"},
		)
		sw.Cases = append(sw.Cases, metals.SwitchCase{Value: index, Body: body})
	}

	fn.Body = []metals.Instr{
		&metals.VariantTag{Dst: "tag", X: "e"},
		sw,
		&metals.Abort{Message: "an error value carries an unknown variant"},
	}
	f.l.module.Funcs = append(f.l.module.Funcs, fn)
	return name
}
