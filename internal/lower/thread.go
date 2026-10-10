package lower

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// Threads and the things built on them are library code. METALS knows how
// to start a thread, how to end one, a cell each thread has of its own, the
// atomics and the system calls; spawn, futures, channels and mutexes are
// Gyro over those, in mem and stdlib. What lowering adds is the part that
// has to know the program's types: packing an async call's arguments for
// another thread, and a thread's entry point to unpack them.

// startThread lowers mem's start_thread to thread.start. The entry has to
// be a declared function, since the instruction names one.
func (f *funcLowerer) startThread(call *ast.CallExpr) func() string {
	entry, ok := call.Args[0].Value.(*ast.UnaryExpr)
	if !ok || entry.Op != token.Amp {
		f.l.fail("@%s: start_thread takes the address of a declared function", f.fn.Name)
	}
	decl, ok := f.refOf(entry.X).(*ast.FuncDecl)
	if !ok {
		f.l.fail("@%s: start_thread takes the address of a declared function", f.fn.Name)
	}
	body := f.l.ensureFunc(decl, nil)
	data := f.lowerExpr(call.Args[1].Value, types.AnyPtr)
	return func() string {
		f.emit(&metals.ThreadStart{
			Dst:   f.newReg("thread"),
			Stack: f.nullStack(),
			Body:  body,
			Data:  data,
		})
		return ""
	}
}

// nullStack is the stack operand that leaves the stack to the backend.
func (f *funcLowerer) nullStack() string {
	none := f.newReg("stack")
	f.emit(&metals.Const{Dst: none, Type: &metals.Opt{Elem: &metals.AnyPtr{}}, Null: true})
	return none
}

// threadLocal lowers unsafe's thread_local<T>: the address of the calling
// thread's cell for T, which is a thread_local global named for the type.
func (f *funcLowerer) threadLocal(resolved typesBinding) func() string {
	bound := resolved["T"]
	if bound == nil {
		f.l.fail("@%s: thread_local without a bound T", f.fn.Name)
	}
	lowered := f.l.metalsType(bound)
	name := "thread_local<" + lowered.String() + ">"
	if !f.l.threadLocals[name] {
		f.l.threadLocals[name] = true
		f.l.module.Globals = append(f.l.module.Globals, &metals.Global{Name: name, Type: lowered, ThreadLocal: true})
	}
	return func() string {
		out := f.newReg("tls")
		f.emit(&metals.GlobalPtr{Dst: out, Name: name})
		return out
	}
}

type typesBinding = map[string]types.Type

// ---------------------------------------------------------------------------
// Async calls
// ---------------------------------------------------------------------------

// An async call runs on a thread of its own and answers a future. The thread
// is started with one anyptr, so the call's arguments travel in a record on
// the heap: the future first, then (for a call through a function value) the
// function value, then each argument. A thunk per callee unpacks it, runs the
// call, stores the result in the future and ends the thread's work on it. The
// thunk is built once the callee is lowered, because the callee's parameters
// are what its record is made of.
type asyncThunk struct {
	name   string
	record string
	// callee is the function a direct call runs, and fn the type of the
	// function value an indirect one runs, nil for a direct call.
	callee string
	fn     *metals.FnType
}

func (l *lowerer) asyncThunkFor(name string, callee string, fn *metals.FnType) *asyncThunk {
	if thunk := l.asyncThunks[name]; thunk != nil {
		return thunk
	}
	thunk := &asyncThunk{name: name, record: name + ".args", callee: callee, fn: fn}
	l.asyncThunks[name] = thunk
	l.asyncOrder = append(l.asyncOrder, thunk)
	return thunk
}

// startAsync starts a direct async call and answers its future.
func (f *funcLowerer) startAsync(callee string, args []string, result types.Type) string {
	thunk := f.l.asyncThunkFor("async::"+callee, callee, nil)
	return f.packAsync(thunk, "", args, result)
}

// startAsyncIndirect starts an async call through a function value.
func (f *funcLowerer) startAsyncIndirect(fn string, args []string, sig *types.Func) string {
	value := f.l.metalsFnType(sig)
	thunk := f.l.asyncThunkFor("async::call "+value.String(), "", value)
	return f.packAsync(thunk, fn, args, sig.Result)
}

func (f *funcLowerer) packAsync(thunk *asyncThunk, fn string, args []string, result types.Type) string {
	newFuture := f.l.ensureFunc(f.l.leafDecl("future_new"), nil)

	record := f.newReg("async.args")
	f.emit(&metals.New{Dst: record, Type: &metals.Named{Name: thunk.record}})

	size := ""
	if isVoid(result) {
		size = f.sizeConst(nil)
	} else {
		size = f.sizeConst(f.l.metalsType(result))
	}
	// The functions the thunk calls are lowered with the rest.
	for _, name := range []string{"future_enter", "future_result", "future_leave"} {
		f.l.ensureFunc(f.l.leafDecl(name), nil)
	}
	future := f.newReg("future")
	f.emit(&metals.Call{Dst: future, Callee: newFuture, Args: []string{size}})

	fields := []string{future}
	if thunk.fn != nil {
		fields = append(fields, fn)
	}
	fields = append(fields, args...)
	for index, value := range fields {
		cell := f.newReg("async.field")
		f.emit(&metals.FieldPtr{Dst: cell, Ptr: record, Index: index})
		f.emit(&metals.Store{Ptr: cell, Val: value})
	}

	data := f.newReg("async.data")
	f.emit(&metals.PtrErase{Dst: data, X: record})
	f.emit(&metals.ThreadStart{Dst: f.newReg("thread"), Stack: f.nullStack(), Body: thunk.name, Data: data})
	return future
}

// await waits for a future and answers its result.
func (f *funcLowerer) await(e *ast.AwaitExpr) string {
	future := f.lowerExpr(e.X, nil)
	wait := f.l.ensureFunc(f.l.leafDecl("future_wait"), nil)
	result := f.typeOfVoidOK(e)

	if isVoid(result) {
		none := f.newReg("nowhere")
		f.emit(&metals.Const{Dst: none, Type: &metals.Opt{Elem: &metals.AnyPtr{}}, Null: true})
		f.emit(&metals.Call{Callee: wait, Args: []string{future, none}})
		return ""
	}

	cell := f.newReg("awaited.cell")
	f.emit(&metals.Slot{Dst: cell, Type: f.l.metalsType(result)})
	erased := f.newReg("awaited.at")
	f.emit(&metals.PtrErase{Dst: erased, X: cell})
	held := f.newReg("awaited.to")
	f.emit(&metals.OptWrap{Dst: held, X: erased})
	f.emit(&metals.Call{Callee: wait, Args: []string{future, held}})
	out := f.newReg("awaited")
	f.emit(&metals.Load{Dst: out, Ptr: cell})
	return out
}

// sizeConst is a register holding the size of a type in bytes. The size is
// written in once every type is declared, in finishAsync: the error and any
// types are declared after the functions that use them, and a size cannot
// be laid out without them. A nil type is zero bytes.
func (f *funcLowerer) sizeConst(t metals.Type) string {
	size := &metals.Const{Dst: f.newReg("size"), Type: metals.TUsize, Value: "0"}
	f.emit(size)
	if t != nil {
		f.l.sizeFixups = append(f.l.sizeFixups, sizeFixup{where: size, typ: t, in: f.fn.Name})
	}
	return size.Dst
}

type sizeFixup struct {
	where *metals.Const
	typ   metals.Type
	in    string
}

// finishAsync writes the sizes and builds the thunks the program asked
// for. It runs once everything else is lowered, because both need every
// type declared and every callee's parameters known; the functions a thunk
// calls were asked for at the call sites, so building one lowers nothing.
func (l *lowerer) finishAsync() {
	layout := metals.NewLayout(l.module, func(format string, args ...any) {
		l.fail("a size: "+format, args...)
	})
	for _, fixup := range l.sizeFixups {
		fixup.where.Value = strconv.Itoa(layout.Size(fixup.typ))
	}
	for _, thunk := range l.asyncOrder {
		l.buildAsyncThunk(thunk)
	}
}

func (l *lowerer) funcNamed(name string) *metals.Func {
	for _, fn := range l.module.Funcs {
		if fn.Name == name {
			return fn
		}
	}
	return nil
}

func (l *lowerer) buildAsyncThunk(thunk *asyncThunk) {
	var params []metals.Type
	var result metals.Type
	if thunk.fn != nil {
		params, result = thunk.fn.Params, thunk.fn.Result
	} else {
		callee := l.funcNamed(thunk.callee)
		if callee == nil {
			l.fail("an async call of @%s, which has no body", thunk.callee)
		}
		for _, param := range callee.Params {
			params = append(params, param.Type)
		}
		result = callee.Result
	}

	decl := &metals.TypeDecl{Name: thunk.record, Record: &metals.RecordShape{}}
	decl.Record.Fields = append(decl.Record.Fields, metals.FieldDecl{Name: "future", Type: &metals.AnyPtr{}})
	if thunk.fn != nil {
		decl.Record.Fields = append(decl.Record.Fields, metals.FieldDecl{Name: "fn", Type: thunk.fn})
	}
	for index, param := range params {
		decl.Record.Fields = append(decl.Record.Fields, metals.FieldDecl{Name: "a" + strconv.Itoa(index), Type: param})
	}
	l.declared[thunk.record] = decl
	l.module.Types = append(l.module.Types, decl)

	fn := &metals.Func{
		Name:   thunk.name,
		Params: []metals.Param{{Name: "data", Type: &metals.AnyPtr{}}},
	}
	emit := func(instr metals.Instr) { fn.Body = append(fn.Body, instr) }
	field := func(index int, name string) string {
		cell, value := name+".p", name
		emit(&metals.FieldPtr{Dst: cell, Ptr: "record", Index: index})
		emit(&metals.Load{Dst: value, Ptr: cell})
		return value
	}

	emit(&metals.PtrCast{Dst: "record", X: "data", To: &metals.Ptr{Elem: &metals.Named{Name: thunk.record}}})
	future := field(0, "future")
	enter := l.ensureFunc(l.leafDecl("future_enter"), nil)
	emit(&metals.Call{Callee: enter, Args: []string{future}})

	next := 1
	value := ""
	if thunk.fn != nil {
		value = field(next, "fn")
		next++
	}
	args := make([]string, len(params))
	for index := range params {
		args[index] = field(next+index, "a"+strconv.Itoa(index))
	}
	// The arguments are out of the record, so it can go before the call:
	// the callee may run for as long as it likes.
	emit(&metals.Free{X: "record"})

	call := ""
	if result != nil {
		call = "result"
	}
	if thunk.fn != nil {
		emit(&metals.CallIndirect{Dst: call, Fn: value, Args: args})
	} else {
		emit(&metals.Call{Dst: call, Callee: thunk.callee, Args: args})
	}

	if result != nil {
		cell := l.ensureFunc(l.leafDecl("future_result"), nil)
		emit(&metals.Call{Dst: "cell", Callee: cell, Args: []string{future}})
		emit(&metals.PtrCast{Dst: "to", X: "cell", To: &metals.Ptr{Elem: result}})
		emit(&metals.Store{Ptr: "to", Val: "result"})
	}
	leave := l.ensureFunc(l.leafDecl("future_leave"), nil)
	emit(&metals.Call{Callee: leave, Args: []string{future}})
	emit(&metals.Return{})
	l.module.Funcs = append(l.module.Funcs, fn)
}
