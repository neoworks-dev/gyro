package lower

import (
	"fmt"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/types"
)

// Errors. An Error is a closed sum over everything in the program that
// implements the trait, which whole-program lowering can enumerate: the
// generated %error enum has one variant for the plain error Error::new
// makes, then one per implementing struct or enum, each carrying its value.
// It is owned and allocates nothing, which is what lets it be returned up
// the stack. A `T | Error` is a two-variant enum, the value or the %error.

// The variants of %error and of a generated result enum.
const (
	plainError  = 0
	resultValue = 0
	resultError = 1
)

// mainRunName is the METALS name of main's body when it returns a result.
// The exported main is then the entry wrapper that acts on the result.
const mainRunName = "main::run"

// implementsError reports a declared type that says `impl Error for` it.
func implementsError(t types.Type) bool {
	var impls []*types.Impl
	switch t := t.(type) {
	case *types.Struct:
		impls = t.Impls
	case *types.Enum:
		impls = t.Impls
	}
	for _, impl := range impls {
		if impl.Trait.Kind == "Error" {
			return true
		}
	}
	return false
}

// declaredType is the type a struct or enum declaration builds, or nil.
func (l *lowerer) declaredType(decl ast.Node) types.Type {
	switch decl := decl.(type) {
	case *ast.StructDecl:
		if t := l.info.Structs[decl]; t != nil {
			return t
		}
	case *ast.EnumDecl:
		if t := l.info.Enums[decl]; t != nil {
			return t
		}
	}
	return nil
}

// collectErrorTypes enumerates every type in the program that implements
// Error, in declaration order, so the %error variants are stable.
func (l *lowerer) collectErrorTypes() {
	for _, module := range l.prog.Modules {
		for _, decl := range module.Tree.Decls {
			t := l.declaredType(decl)
			if t == nil || !implementsError(t) {
				continue
			}
			l.errVariant[t] = len(l.errTypes) + 1
			l.errTypes = append(l.errTypes, t)
		}
	}
}

// finishErrorType emits the %error declaration once anything mentioned it.
func (l *lowerer) finishErrorType() {
	if !l.usesError || l.declared["error"] != nil {
		return
	}
	decl := &metals.TypeDecl{Name: "error", Enum: &metals.EnumShape{}}
	l.declared["error"] = decl
	l.module.Types = append(l.module.Types, decl)

	decl.Enum.Variants = append(decl.Enum.Variants, metals.VariantDecl{
		Name:    "message",
		Payload: []metals.Type{metals.TStr},
	})
	for _, t := range l.errTypes {
		decl.Enum.Variants = append(decl.Enum.Variants, metals.VariantDecl{
			Name:    t.String(),
			Payload: []metals.Type{l.metalsType(t)},
		})
	}
}

// errorVariantOf returns the %error variant index for a type that
// implements Error.
func (l *lowerer) errorVariantOf(t types.Type) int {
	if index, ok := l.errVariant[t]; ok {
		return index
	}
	l.fail("%s is used as an Error but was not collected as one", t)
	return 0
}

// metalsResult lowers `T | Error` to an enum of the value and the %error.
// It is named for its spelling, which no declared type can take.
func (l *lowerer) metalsResult(t *types.Result) metals.Type {
	value := l.metalsType(t.Value)
	name := value.String() + " | Error"
	if l.declared[name] == nil {
		decl := &metals.TypeDecl{Name: name, Enum: &metals.EnumShape{Variants: []metals.VariantDecl{
			{Name: "value", Payload: []metals.Type{value}},
			{Name: "error", Payload: []metals.Type{l.metalsType(types.Error)}},
		}}}
		l.declared[name] = decl
		l.module.Types = append(l.module.Types, decl)
	}
	return &metals.Named{Name: name}
}

// wrapError makes an Error of a value that implements the trait.
func (f *funcLowerer) wrapError(reg string, from types.Type) string {
	if isErrorBasic(from) {
		return reg
	}
	f.l.usesError = true
	f.l.metalsType(from)
	out := f.newReg("err")
	f.emit(&metals.MakeVariant{
		Dst:      out,
		TypeName: "error",
		Variant:  f.l.errorVariantOf(from),
		Args:     []string{reg},
	})
	return out
}

// wrapResult makes a `T | Error` of a value or of an error. A value that is
// both, as an Error is when T is one, is the value.
func (f *funcLowerer) wrapResult(reg string, from types.Type, to *types.Result) string {
	variant, payload := resultValue, f.coerce(reg, from, to.Value)
	if !types.Identical(types.Default(from), to.Value) && f.isErrorValue(from) {
		variant, payload = resultError, f.wrapError(reg, from)
	}
	return f.makeResult(to, variant, payload)
}

// isErrorValue reports a type whose values are errors.
func (f *funcLowerer) isErrorValue(t types.Type) bool {
	_, collected := f.l.errVariant[t]
	return isErrorBasic(t) || collected
}

func (f *funcLowerer) makeResult(t *types.Result, variant int, payload string) string {
	out := f.newReg("result")
	f.emit(&metals.MakeVariant{
		Dst:      out,
		TypeName: f.l.metalsResult(t).(*metals.Named).Name,
		Variant:  variant,
		Args:     []string{payload},
	})
	return out
}

// isErrorNew reports a call of Error::new. The checker resolved its path
// to the trait, the one thing a call can reach through a trait.
func (f *funcLowerer) isErrorNew(call *ast.CallExpr) bool {
	path, ok := call.Fun.(*ast.Path)
	if !ok {
		return false
	}
	_, trait := f.refOf(path).(*ast.TraitDecl)
	return trait
}

// lowerErrorNew makes the plain error, which is only its message.
func (f *funcLowerer) lowerErrorNew(call *ast.CallExpr) string {
	message := f.lowerExpr(call.Args[0].Value, types.String)
	f.l.usesError = true
	out := f.newReg("err")
	f.emit(&metals.MakeVariant{Dst: out, TypeName: "error", Variant: plainError, Args: []string{message}})
	return out
}

// ensureErrorMessage synthesizes the dispatch behind err.message(): a
// switch over the closed %error sum, answering the plain error's own text
// or calling each implementer's message method.
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
	sw.Cases = append(sw.Cases, metals.SwitchCase{Value: plainError, Body: []metals.Instr{
		&metals.VariantField{Dst: "text", X: "e", Variant: plainError, Index: 0},
		&metals.Return{Val: "text"},
	}})

	for _, errType := range f.l.errTypes {
		index := f.l.errVariant[errType]
		callee := f.l.ensureFunc(methodsOf(errType)["message"].Decl, nil)
		f.l.metalsType(errType)

		// Registers are named per variant: one holds a different type in
		// each case, and a register's type is fixed at first assignment.
		value, msg := fmt.Sprintf("v%d", index), fmt.Sprintf("msg%d", index)
		sw.Cases = append(sw.Cases, metals.SwitchCase{Value: index, Body: []metals.Instr{
			&metals.VariantField{Dst: value, X: "e", Variant: index, Index: 0},
			&metals.Call{Dst: msg, Callee: callee, Args: []string{value}},
			&metals.Return{Val: msg},
		}})
	}

	fn.Body = append(fn.Body,
		&metals.VariantTag{Dst: "tag", X: "e"},
		sw,
		&metals.Abort{Message: "an error value carries an unknown variant"},
	)
	f.l.module.Funcs = append(f.l.module.Funcs, fn)
	return name
}

// methodsOf is the method table of a struct or an enum.
func methodsOf(t types.Type) map[string]*types.Method {
	switch t := t.(type) {
	case *types.Struct:
		return t.Methods
	case *types.Enum:
		return t.Methods
	}
	return nil
}

// tryExpr lowers `x?`: on an error, return it from this function, and
// otherwise carry on with the value.
func (f *funcLowerer) tryExpr(e *ast.TryExpr) string {
	operand := f.lowerExpr(e.X, nil)
	switch operandType := f.typeOf(e.X).(type) {
	case *types.Result:
		f.returnResultError(operand)
		value := f.newReg("ok")
		f.emit(&metals.VariantField{Dst: value, X: operand, Variant: resultValue, Index: 0})
		return value
	case *types.Optional:
		f.returnOptionalError(operand, operandType)
		return ""
	}
	f.l.fail("@%s: ? on a type that is not a result", f.fn.Name)
	return ""
}

// returnResultError returns the error of a result when it holds one.
func (f *funcLowerer) returnResultError(result string) {
	tag := f.newReg("tag")
	f.emit(&metals.VariantTag{Dst: tag, X: result})
	failed := f.tagEquals(tag, resultError)
	f.emit(&metals.If{Cond: failed, Then: f.nested(func() {
		failure := f.newReg("failure")
		f.emit(&metals.VariantField{Dst: failure, X: result, Variant: resultError, Index: 0})
		f.returnError(failure)
	})})
}

// returnOptionalError returns the error of an Error? when it holds one.
func (f *funcLowerer) returnOptionalError(optional string, t *types.Optional) {
	null := f.newReg("null")
	f.emit(&metals.Const{Dst: null, Type: f.l.metalsType(t), Null: true})
	absent := f.newReg("ok")
	f.emit(&metals.Bin{Dst: absent, Op: "eq", A: optional, B: null})
	failed := f.newReg("failed")
	f.emit(&metals.Un{Dst: failed, Op: "not", X: absent})
	f.emit(&metals.If{Cond: failed, Then: f.nested(func() {
		failure := f.newReg("failure")
		f.emit(&metals.OptUnwrap{Dst: failure, X: optional})
		f.returnError(failure)
	})})
}

// returnError leaves the function with an Error, in the form its result
// takes: the error of a result, or an Error? that is not null.
func (f *funcLowerer) returnError(failure string) {
	f.leave()
	if result, ok := f.result.(*types.Result); ok {
		f.emit(&metals.Return{Val: f.makeResult(result, resultError, failure)})
		return
	}
	wrapped := f.newReg("some")
	f.emit(&metals.OptWrap{Dst: wrapped, X: failure})
	f.emit(&metals.Return{Val: wrapped})
}

// returnSuccess is the end of a function whose result is an Error?: nothing
// went wrong, and null says so.
func (f *funcLowerer) returnSuccess() {
	f.leave()
	null := f.newReg("null")
	f.emit(&metals.Const{Dst: null, Type: f.l.metalsType(f.result), Null: true})
	f.emit(&metals.Return{Val: null})
}

// isErrorOptional reports `Error?`, the result of a function that has no
// value and succeeds by reaching its end.
func isErrorOptional(t types.Type) bool {
	optional, ok := t.(*types.Optional)
	return ok && isErrorBasic(optional.Elem)
}

// entryWrapper is the exported main of a program whose main takes an Io or
// returns a result. The runtime passes the arguments alone, so the wrapper
// makes the process's Io and hands it over first; and it hands what came
// back to stdlib, which ends the process with the status or the error.
func (l *lowerer) entryWrapper(run *metals.Func, result types.Type) {
	wrapper := &metals.Func{Name: "main", Export: true, Params: run.Params}
	var args []string
	if l.mainTakesIo() {
		wrapper.Params = run.Params[1:]
		maker := l.ensureFunc(l.leafDecl("process_io"), nil)
		wrapper.Body = append(wrapper.Body, &metals.Call{Dst: "process_io", Callee: maker})
		args = append(args, "process_io")
	}
	for _, param := range wrapper.Params {
		args = append(args, param.Name)
	}
	l.module.Funcs = append(l.module.Funcs, wrapper)
	if len(l.mainDecl.Results) == 0 {
		wrapper.Body = append(wrapper.Body, &metals.Call{Callee: run.Name, Args: args}, &metals.Return{})
		return
	}
	wrapper.Body = append(wrapper.Body, &metals.Call{Dst: "returned", Callee: run.Name, Args: args})

	outcome := "returned"
	if _, fallible := result.(*types.Result); !fallible {
		// A bare i32 is the exit status, which is a result that cannot fail.
		status := &types.Result{Value: types.Int32}
		outcome = "outcome"
		wrapper.Body = append(wrapper.Body, &metals.MakeVariant{
			Dst:      outcome,
			TypeName: l.metalsResult(status).(*metals.Named).Name,
			Variant:  resultValue,
			Args:     []string{"returned"},
		})
	}
	finish := l.ensureFunc(l.leafDecl("finish_main"), nil)
	wrapper.Body = append(wrapper.Body,
		&metals.Call{Callee: finish, Args: []string{outcome}},
		&metals.Return{},
	)
}

// mainTakesIo reports a main whose first parameter is the process's Io.
func (l *lowerer) mainTakesIo() bool {
	main := l.mainDecl
	if len(main.Params) == 0 {
		return false
	}
	return sema.IsIo(l.info.Locals[main][main.Params[0].Name])
}
