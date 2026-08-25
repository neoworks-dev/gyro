package lower

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/types"
)

// Marshalling for @extern lives here rather than in either backend,
// because the two backends would otherwise write it twice and disagree
// once. What reaches them is a call whose arguments are all one qword
// wide — scalars, handles and pointers — which is a shape they already
// emit.
//
// One conversion is left, and it is the one C's own representation
// forces: a Gyro string is a length-prefixed header where C wants
// NUL-terminated bytes, so `ffi.cstr` builds a copy that lives for the
// call and `ffi.release` ends it. A slice needs nothing — since storage
// is C's, a `u8[]` is a `const uint8_t *` already, and `ffi.data` only
// reads the pointer out of the two-word slice.

// externIntrinsic names one of the runtime's marshalling helpers.
const (
	externCstr      = "ffi.cstr"
	externFromCstr  = "ffi.from_cstr"
	externData      = "ffi.data"
	externRelease   = "ffi.release"
	externOptHandle = "ffi.opt_handle"
	externHandleOpt = "ffi.handle_opt"
)

// prepareExtern lowers a call to a C function: convert what has to be
// converted, call the symbol, release the temporaries, and put the result
// back into the Gyro type the checker promised.
func (f *funcLowerer) prepareExtern(call *ast.CallExpr, resolved *sema.CallInfo, extern types.Extern) func() string {
	args := f.callArgs(call, resolved.Sig, nil)

	return func() string {
		var release []string
		converted := make([]string, len(args))
		for i, arg := range args {
			var temporary bool
			converted[i], temporary = f.marshalToC(arg, resolved.Sig.Params[i])
			if temporary {
				release = append(release, converted[i])
			}
		}

		f.l.ensureExternImport(extern, resolved.Sig)

		out := ""
		if !isVoid(resolved.Sig.Result) {
			out = f.newReg("r")
			f.emit(&metals.Call{Dst: out, Callee: extern.Symbol, Args: converted})
		} else {
			f.emit(&metals.Call{Callee: extern.Symbol, Args: converted})
		}

		// After the call, not before: the callee reads the copies.
		for _, handle := range release {
			f.emitExternHelper(externRelease, &metals.FnType{
				Params: []metals.Type{&metals.Handle{}},
			}, handle)
		}

		if out == "" {
			return ""
		}
		return f.marshalFromC(out, resolved.Sig.Result)
	}
}

// marshalToC converts one argument into the qword C sees, and reports
// whether the result is a temporary the call has to release.
func (f *funcLowerer) marshalToC(arg string, declared types.Type) (string, bool) {
	handle := &metals.Handle{}
	switch t := declared.(type) {
	case *types.Basic:
		if t.Kind == types.StringKind {
			sig := &metals.FnType{Params: []metals.Type{metals.TStr}, Result: handle}
			return f.emitExternHelper(externCstr, sig, arg), true
		}
	case *types.Slice:
		// The elements are already what C reads, so this borrows rather
		// than copies: the pointer is the slice's own, and a library that
		// keeps it keeps a dangling one.
		sig := &metals.FnType{Params: []metals.Type{f.l.metalsType(t)}, Result: handle}
		return f.emitExternHelper(externData, sig, arg), false
	case *types.Optional:
		sig := &metals.FnType{Params: []metals.Type{f.l.metalsType(t)}, Result: handle}
		return f.emitExternHelper(externOptHandle, sig, arg), false
	}
	return arg, false
}

// marshalFromC puts a returned qword back into its Gyro type. A nullable
// handle needs the flag Gyro keeps beside the payload and C answers with
// a null pointer; a string needs copying out of storage the library owns
// and may reuse on the next call.
func (f *funcLowerer) marshalFromC(reg string, declared types.Type) string {
	switch t := declared.(type) {
	case *types.Optional:
		sig := &metals.FnType{
			Params: []metals.Type{&metals.Handle{}},
			Result: f.l.metalsType(declared),
		}
		return f.emitExternHelper(externHandleOpt, sig, reg)
	case *types.Basic:
		if t.Kind == types.StringKind {
			sig := &metals.FnType{
				Params: []metals.Type{&metals.Handle{}},
				Result: metals.TStr,
			}
			return f.emitExternHelper(externFromCstr, sig, reg)
		}
	}
	return reg
}

// emitExternHelper calls one of the marshalling intrinsics, declaring the
// import the first time it is used.
func (f *funcLowerer) emitExternHelper(name string, sig *metals.FnType, args ...string) string {
	f.l.ensureImport(name, sig)
	if sig.Result == nil {
		f.emit(&metals.Call{Callee: name, Args: args})
		return ""
	}
	dst := f.newReg("ffi")
	f.emit(&metals.Call{Dst: dst, Callee: name, Args: args})
	return dst
}

// ensureExternImport declares the C symbol under its library's module.
// The signature is the C-facing one: every parameter a scalar or a
// handle, because marshalling already happened.
func (l *lowerer) ensureExternImport(extern types.Extern, sig *types.Func) {
	if l.imports[extern.Symbol] != nil {
		return
	}
	lowered := &metals.FnType{}
	for _, param := range sig.Params {
		lowered.Params = append(lowered.Params, l.externType(param))
	}
	if !isVoid(sig.Result) {
		lowered.Result = l.externType(sig.Result)
	}
	l.imports[extern.Symbol] = lowered
	l.importFrom[extern.Symbol] = metals.ExternModule(extern.Library)
}

// externType is the METALS type of a Gyro type once it has crossed:
// scalars keep their width, a struct with fields keeps its record type
// because C reads the same bytes, and everything else is a pointer.
func (l *lowerer) externType(t types.Type) metals.Type {
	switch t := t.(type) {
	case *types.Basic:
		if t.Kind == types.StringKind {
			return &metals.Handle{}
		}
	case *types.Struct:
		if t.Extern != "" {
			return &metals.Handle{} // an opaque pointer, by identity
		}
		return l.metalsType(t)
	case *types.Slice, *types.Optional:
		return &metals.Handle{}
	}
	return l.metalsType(t)
}
