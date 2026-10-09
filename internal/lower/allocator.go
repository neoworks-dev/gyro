package lower

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
)

// A function whose row names alloc takes the allocator as a trailing
// parameter (chapter 4.5). Two places hand such a function on without a
// call site to pass one at: taking it as a `fn` value, and taking its
// address as a `code` pointer. Each gets a small synthesized function that
// supplies the allocator — the current one, captured in an environment,
// for a fn value; the heap for a code pointer, since a C caller has
// nowhere to put one.

// allocatingFnValue is a named allocating function taken as a value: a
// closure over the allocator current here, so a later call allocates
// from it wherever the call happens.
func (f *funcLowerer) allocatingFnValue(decl *ast.FuncDecl, target string) string {
	wrapper, envName := f.l.ensureFnWrapper(decl, target)

	envPtr := f.newReg("env")
	f.emit(&metals.New{Dst: envPtr, Type: &metals.Named{Name: envName}})
	fieldPtr := f.newReg("cap")
	f.emit(&metals.FieldPtr{Dst: fieldPtr, Ptr: envPtr, Index: 0})
	f.emit(&metals.Store{Ptr: fieldPtr, Val: f.currentAllocator()})

	out := f.newReg("fn")
	f.emit(&metals.MakeClosure{Dst: out, Callee: wrapper, Env: envPtr})
	return out
}

// allocatingCodeValue is `&f` on an allocating function: the address of a
// thunk that supplies the heap and calls f.
func (f *funcLowerer) allocatingCodeValue(decl *ast.FuncDecl, target string) string {
	thunk := f.l.ensureHeapThunk(decl, target)
	out := f.newReg("code")
	f.emit(&metals.CodeRef{Dst: out, Callee: thunk})
	return out
}

// withAllocator appends the current allocator to a call's arguments when
// the callee takes one. It is for the calls the lowering makes on its own
// — to_string, an iterator's next — which have no call expression to
// pass through prepareUserCall.
func (f *funcLowerer) withAllocator(decl *ast.FuncDecl, args ...string) []string {
	if f.l.allocates(decl) {
		args = append(args, f.currentAllocator())
	}
	return args
}

// heapFunc is the name of mem's heap(), queued for lowering.
func (l *lowerer) heapFunc() string {
	return l.ensureFunc(l.memDecl("heap"), nil)
}

// ensureFnWrapper synthesizes `target.fn`: a lifted body whose environment
// holds one allocator, forwarding its parameters to target with that
// allocator appended. It returns the wrapper's name and its environment
// type's.
func (l *lowerer) ensureFnWrapper(decl *ast.FuncDecl, target string) (string, string) {
	name := target + ".fn"
	envName := name + ".env"
	if l.lowered[name] {
		return name, envName
	}
	l.lowered[name] = true

	allocator := l.metalsType(l.allocatorStruct())
	l.declared[envName] = &metals.TypeDecl{Name: envName, Record: &metals.RecordShape{
		Fields: []metals.FieldDecl{{Name: allocatorLocal, Type: allocator}},
	}}
	l.module.Types = append(l.module.Types, l.declared[envName])

	fn := &metals.Func{Name: name, Async: decl.Async}
	fn.Params = append(fn.Params, metals.Param{Name: "env", Type: &metals.Ptr{Elem: &metals.Named{Name: envName}}})
	args := l.forwardedParams(decl, fn)

	fieldPtr := "cap"
	fn.Body = append(fn.Body, &metals.FieldPtr{Dst: fieldPtr, Ptr: "env", Index: 0})
	fn.Body = append(fn.Body, &metals.Load{Dst: allocatorLocal, Ptr: fieldPtr})
	l.forwardCall(decl, fn, target, append(args, allocatorLocal))

	l.module.Funcs = append(l.module.Funcs, fn)
	return name, envName
}

// ensureHeapThunk synthesizes `target.code`: the same parameters as
// target, the heap supplied inside, so the result has a plain address.
func (l *lowerer) ensureHeapThunk(decl *ast.FuncDecl, target string) string {
	name := target + ".code"
	if l.lowered[name] {
		return name
	}
	l.lowered[name] = true

	fn := &metals.Func{Name: name}
	args := l.forwardedParams(decl, fn)
	fn.Body = append(fn.Body, &metals.Call{Dst: allocatorLocal, Callee: l.heapFunc()})
	l.forwardCall(decl, fn, target, append(args, allocatorLocal))

	l.module.Funcs = append(l.module.Funcs, fn)
	return name
}

// forwardedParams declares target's own parameters on a synthesized
// function and returns their registers. A value taken of a method would
// need its receiver here; none is, since a method is not a bare name.
func (l *lowerer) forwardedParams(decl *ast.FuncDecl, fn *metals.Func) []string {
	if decl.Recv != nil {
		l.fail("@%s: a method is not a value", decl.Name)
	}
	locals := l.info.Locals[decl]
	var args []string
	for i, param := range decl.Params {
		typ := locals[param.Name]
		if typ == nil {
			l.fail("@%s: parameter %s has no recorded type", decl.Name, param.Name)
		}
		reg := "p" + strconv.Itoa(i)
		fn.Params = append(fn.Params, metals.Param{Name: reg, Type: l.metalsType(typ)})
		args = append(args, reg)
	}
	if result := locals["return"]; !isVoid(result) {
		fn.Result = l.metalsType(result)
	}
	return args
}

// forwardCall emits the call to target and the return of its result. An
// async target is started rather than called, so an async wrapper awaits
// the future it gets back and returns the value, as its own caller
// expects of an async body.
func (l *lowerer) forwardCall(decl *ast.FuncDecl, fn *metals.Func, target string, args []string) {
	if decl.Async {
		fn.Body = append(fn.Body, &metals.Call{Dst: "started", Callee: target, Args: args})
		if fn.Result == nil {
			fn.Body = append(fn.Body, &metals.Await{X: "started"}, &metals.Return{})
			return
		}
		fn.Body = append(fn.Body, &metals.Await{Dst: "r", X: "started"}, &metals.Return{Val: "r"})
		return
	}
	if fn.Result == nil {
		fn.Body = append(fn.Body, &metals.Call{Callee: target, Args: args}, &metals.Return{})
		return
	}
	fn.Body = append(fn.Body, &metals.Call{Dst: "r", Callee: target, Args: args}, &metals.Return{Val: "r"})
}
