package interp

import (
	"github.com/gyro-lang/gyro/internal/ast"
)

// prepareCall resolves the callee and evaluates the arguments now, and
// returns the invocation as a thunk. Ordinary calls run it immediately;
// defer registers it for block exit, which is exactly Go's argument
// timing.
func (f *frame) prepareCall(call *ast.CallExpr, env *env) func() Value {
	args := make([]Value, len(call.Args))
	for i, arg := range call.Args {
		args[i] = copyValue(f.eval(arg.Value, env))
	}
	typeArgs := typeArgList(call)

	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.prepareMethodCall(fun, args, env)
	case *ast.Path:
		return f.preparePathCall(fun, args, typeArgs, call.Offset, env)
	case *ast.Ident:
		return f.prepareNamedCall(fun, args, typeArgs, call.Offset, env)
	}

	// Anything else evaluates to a closure value.
	value := f.eval(call.Fun, env)
	return f.closureThunk(value, args, call.Fun.Pos())
}

func typeArgList(call *ast.CallExpr) []ast.Type {
	var out []ast.Type
	for _, arg := range call.TypeArgs {
		if arg.Type != nil {
			out = append(out, arg.Type)
		}
	}
	return out
}

func (f *frame) closureThunk(value Value, args []Value, offset int) func() Value {
	closure, ok := value.(*ClosureVal)
	if !ok {
		f.in.panicf(f.module, offset, "value is not callable")
	}
	return func() Value {
		return f.in.callClosure(closure, args)
	}
}

// prepareNamedCall handles f(...) for a bare name: a local closure, a
// module function (possibly native), or a module value holding a closure.
func (f *frame) prepareNamedCall(fun *ast.Ident, args []Value, typeArgs []ast.Type, offset int, env *env) func() Value {
	if cell := env.lookup(fun.Name); cell != nil {
		return f.closureThunk(*cell, args, fun.Offset)
	}

	entry := f.module.lookupBinding(fun.Name)
	if entry == nil {
		f.in.panicf(f.module, fun.Offset, "undefined name %s", fun.Name)
	}
	if entry.fn != nil {
		return f.defThunk(entry.fn, nil, args, typeArgs, offset)
	}
	if entry.cell != nil {
		return f.closureThunk(*entry.cell, args, fun.Offset)
	}
	f.in.panicf(f.module, fun.Offset, "%s is not callable", fun.Name)
	return nil
}

// defThunk invokes a declared function: native bodies dispatch to the
// runtime, async declarations produce a future.
func (f *frame) defThunk(def *funcDef, self Value, args []Value, typeArgs []ast.Type, offset int) func() Value {
	if def.native != nil {
		return func() Value {
			result := def.native(&nativeCall{
				frame:    f,
				offset:   offset,
				typeArgs: typeArgs,
				self:     self,
				args:     defaultedArgs(args),
			})
			if def.decl != nil && def.decl.Async {
				return resolvedFuture(result)
			}
			return result
		}
	}
	return func() Value {
		return f.in.callFunction(def, self, args)
	}
}

func defaultedArgs(args []Value) []Value {
	out := make([]Value, len(args))
	for i, arg := range args {
		out[i] = defaultUntyped(arg)
	}
	return out
}

func resolvedFuture(result Value) Value {
	future := &FutureVal{ch: make(chan Value, 1)}
	future.ch <- result
	return future
}

// ---------------------------------------------------------------------------
// Method calls
// ---------------------------------------------------------------------------

func (f *frame) prepareMethodCall(fun *ast.SelectorExpr, args []Value, env *env) func() Value {
	cell := f.receiverCell(fun.X, env)

	if thunk := f.nativeMethodThunk(cell, fun.Name, args, fun.Offset); thunk != nil {
		return thunk
	}

	if def := f.userMethodOf(*cell, fun.Name); def != nil {
		self := f.receiverFor(def, cell)
		return f.defThunk(def, self, args, nil, fun.Offset)
	}

	// A struct field holding a closure.
	if strct, ok := (*cell).(StructVal); ok {
		if index := strct.Def.fieldIndex(fun.Name); index >= 0 {
			return f.closureThunk(strct.Fields[index], args, fun.Offset)
		}
	}

	f.in.panicf(f.module, fun.Offset, "%s has no method %s", f.in.repr(*cell), fun.Name)
	return nil
}

// receiverCell resolves the method receiver to a cell so *self methods
// can mutate in place; an rvalue receiver gets a fresh cell. A pointer
// receiver value auto-dereferences to the cell it points at.
func (f *frame) receiverCell(e ast.Expr, env *env) *Value {
	cell := f.addressableCell(e, env)
	if cell == nil {
		value := f.eval(e, env)
		cell = cellOf(value)
	}
	if pointer, ok := (*cell).(PointerVal); ok {
		return pointer.Cell
	}
	return cell
}

func (f *frame) addressableCell(e ast.Expr, env *env) *Value {
	switch e := e.(type) {
	case *ast.Ident:
		if e.Name == "_" {
			return nil
		}
		if cell := env.lookup(e.Name); cell != nil {
			return cell
		}
		if entry := f.module.lookupBinding(e.Name); entry != nil && entry.cell != nil {
			return entry.cell
		}
		return nil
	case *ast.SelectorExpr:
		return f.fieldCell(e, env)
	case *ast.IndexExpr:
		return f.indexCell(e, env)
	case *ast.SelfExpr:
		return env.lookup("self")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Path calls and path values
// ---------------------------------------------------------------------------

// pathTarget is what a :: path resolves to at runtime.
type pathTarget struct {
	def     *funcDef
	typ     *typeDef
	variant string
	cell    *Value
}

func (f *frame) resolvePath(path *ast.Path, offset int) pathTarget {
	segments := path.Segments
	entry := f.module.lookupBinding(segments[0].Name)
	if entry == nil {
		f.in.panicf(f.module, offset, "undefined name %s", segments[0].Name)
	}

	if entry.ns != nil {
		if len(segments) == 1 {
			f.in.panicf(f.module, offset, "%s is a module", segments[0].Name)
		}
		member, ok := entry.ns.exports[segments[1].Name]
		if !ok {
			f.in.panicf(f.module, offset, "%s does not export %s", segments[0].Name, segments[1].Name)
		}
		return f.memberTarget(member, segments[2:], offset)
	}
	return f.memberTarget(entry, segments[1:], offset)
}

func (f *frame) memberTarget(entry *binding, rest []ast.PathSegment, offset int) pathTarget {
	if entry.typ != nil && len(rest) > 0 {
		return f.typeMember(entry.typ, rest[0].Name, offset)
	}
	return pathTarget{def: entry.fn, typ: entry.typ, cell: entry.cell}
}

func (f *frame) typeMember(def *typeDef, name string, offset int) pathTarget {
	if def.enumDecl != nil {
		for _, variant := range def.enumDecl.Variants {
			if variant.Name == name {
				return pathTarget{typ: def, variant: name}
			}
		}
	}
	if method := def.module.methodOf(def.name, name); method != nil {
		return pathTarget{def: method}
	}
	f.in.panicf(f.module, offset, "%s has no member %s", def.name, name)
	return pathTarget{}
}

func (f *frame) preparePathCall(fun *ast.Path, args []Value, typeArgs []ast.Type, offset int, env *env) func() Value {
	target := f.resolvePath(fun, fun.Offset)

	if target.variant != "" {
		def, variant := target.typ, target.variant
		return func() Value {
			return EnumVal{Def: def, Variant: variant, Payload: defaultedArgs(args)}
		}
	}
	if target.def != nil {
		return f.defThunk(target.def, nil, args, typeArgs, offset)
	}
	if target.cell != nil {
		return f.closureThunk(*target.cell, args, offset)
	}
	f.in.panicf(f.module, fun.Offset, "this path is not callable")
	return nil
}

// evalPath is a path in value position: a namespaced value, a function
// value, or a payload-less enum variant.
func (f *frame) evalPath(path *ast.Path, env *env) Value {
	target := f.resolvePath(path, path.Offset)
	switch {
	case target.variant != "":
		return EnumVal{Def: target.typ, Variant: target.variant}
	case target.cell != nil:
		return *target.cell
	case target.def != nil:
		return f.closureFor(target.def)
	}
	f.in.panicf(f.module, path.Offset, "this path is not a value")
	return nil
}
