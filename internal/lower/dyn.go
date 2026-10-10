package lower

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/types"
)

// Trait values. A `dyn Trait` is a record of two words: the address of the
// value, with its type forgotten, and the address of the table of its
// type's methods.
//
//	type %"dyn<Element>" = record { data: anyptr, table: ptr<%"vtable<Element>"> }
//	type %"vtable<Element>" = record { paint: code(anyptr) -> void, ... }
//
// A table is a record of code pointers, one per method of the trait and of
// the traits it requires, and there is one per (type, trait) for the whole
// program. Its storage is a global, written by the module's initializer
// because an address is not known until the program is laid out.
//
// A code pointer in a table cannot be the implementing type's method, whose
// receiver is a Button or a *Button; it takes the erased address instead.
// So each entry is a thunk that puts the type back, loads the receiver if
// the method takes it by value, and calls the method. Calling through a
// table is then one indirect call whose first argument is the data word.

// vtable is one method table the program needs.
type vtable struct {
	global  string
	recType *metals.Named
	// entries are the thunks, in the order of the table's fields.
	entries []string
}

func isDynType(t types.Type) bool {
	_, ok := t.(*types.Dyn)
	return ok
}

// dynTypeName and tableTypeName are the METALS names of a trait value and
// of the table behind it.
func (l *lowerer) dynNames(ref *types.TraitRef) (dyn, table string) {
	if len(ref.Args) > 0 {
		l.fail("a trait value of %s is not supported: a dyn trait takes no type parameters", ref)
	}
	return l.typeName("dyn<"+ref.Trait.Name+">", ref.Trait.Module, nil),
		l.typeName("vtable<"+ref.Trait.Name+">", ref.Trait.Module, nil)
}

// metalsDyn declares a trait value's record and the table type it points
// at.
func (l *lowerer) metalsDyn(t *types.Dyn) metals.Type {
	dyn, table := l.dynNames(t.Ref)
	if l.declared[dyn] == nil {
		tableDecl := &metals.TypeDecl{Name: table, Record: &metals.RecordShape{}}
		l.declared[table] = tableDecl
		l.module.Types = append(l.module.Types, tableDecl)

		decl := &metals.TypeDecl{Name: dyn, Record: &metals.RecordShape{}}
		l.declared[dyn] = decl
		l.module.Types = append(l.module.Types, decl)
		decl.Record.Fields = []metals.FieldDecl{
			{Name: "data", Type: &metals.AnyPtr{}},
			{Name: "table", Type: &metals.Ptr{Elem: &metals.Named{Name: table}}},
		}

		for _, entry := range t.Ref.AllMethods() {
			code := &metals.CodeType{Params: []metals.Type{&metals.AnyPtr{}}}
			for _, param := range entry.Method.Sig.Params {
				code.Params = append(code.Params, l.metalsType(param))
			}
			if !isVoid(entry.Method.Sig.Result) {
				code.Result = l.metalsType(entry.Method.Sig.Result)
			}
			tableDecl.Record.Fields = append(tableDecl.Record.Fields, metals.FieldDecl{
				Name: entry.Method.Name,
				Type: code,
			})
		}
	}
	return &metals.Named{Name: dyn}
}

// tableFor returns the table of a type's methods for a trait, declaring its
// storage and the thunks that fill it the first time it is asked for.
func (l *lowerer) tableFor(t types.Type, ref *types.TraitRef) *vtable {
	dyn := &types.Dyn{Ref: ref}
	l.metalsDyn(dyn)
	_, tableName := l.dynNames(ref)

	concrete := l.metalsType(t)
	key := concrete.String() + " as " + tableName
	if existing, ok := l.vtables[key]; ok {
		return existing
	}

	table := &vtable{
		global:  "vtable<" + concrete.String() + " as " + ref.Trait.Name + ">",
		recType: &metals.Named{Name: tableName},
	}
	l.vtables[key] = table
	l.vtableOrder = append(l.vtableOrder, table)
	l.module.Globals = append(l.module.Globals, &metals.Global{Name: table.global, Type: table.recType})

	for _, entry := range ref.AllMethods() {
		table.entries = append(table.entries, l.thunkFor(t, entry))
	}
	return table
}

// thunkFor builds the entry of a table for one method of one type.
func (l *lowerer) thunkFor(t types.Type, entry *types.TraitMethod) string {
	method := methodOfType(t, entry.Method.Name)
	if method == nil || method.Decl == nil {
		l.fail("%s implements %s but has no method %s to put in its table", t, entry.Trait.Name, entry.Method.Name)
	}
	callee := l.ensureFunc(method.Decl, methodBindings(method, t))

	name := "thunk<" + l.metalsType(t).String() + " as " + entry.Trait.Name + ">::" + entry.Method.Name
	if l.lowered[name] {
		return name
	}
	l.lowered[name] = true

	fn := &metals.Func{
		Name:   name,
		Params: []metals.Param{{Name: "self", Type: &metals.AnyPtr{}}},
	}
	args := []string{}
	for i, param := range entry.Method.Sig.Params {
		argName := "a" + itoa(i)
		fn.Params = append(fn.Params, metals.Param{Name: argName, Type: l.metalsType(param)})
		args = append(args, argName)
	}
	if !isVoid(entry.Method.Sig.Result) {
		fn.Result = l.metalsType(entry.Method.Sig.Result)
	}

	// Put the type back on the address; a method taking its receiver by
	// value gets what is at it.
	fn.Body = append(fn.Body, &metals.PtrCast{Dst: "p", X: "self", To: &metals.Ptr{Elem: l.metalsType(t)}})
	receiver := "p"
	if method.Recv == types.ValueRecv {
		fn.Body = append(fn.Body, &metals.Load{Dst: "v", Ptr: "p"})
		receiver = "v"
	}
	call := &metals.Call{Callee: callee, Args: append([]string{receiver}, args...)}
	if fn.Result != nil {
		call.Dst = "r"
		fn.Body = append(fn.Body, call, &metals.Return{Val: "r"})
	} else {
		fn.Body = append(fn.Body, call, &metals.Return{})
	}
	l.module.Funcs = append(l.module.Funcs, fn)
	return name
}

// methodOfType finds the method a named type carries under a name,
// whether its author wrote it or the trait's default was installed.
func methodOfType(t types.Type, name string) *types.Method {
	switch t := t.(type) {
	case *types.Struct:
		return methodOn(t, name)
	case *types.Enum:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		return origin.Methods[name]
	case *types.Union:
		return t.Methods[name]
	}
	return nil
}

// methodBindings are the by-name type arguments a method of t is
// monomorphized with: the owner's, or for a default body installed from a
// trait, the type standing for Self.
func methodBindings(method *types.Method, t types.Type) map[string]types.Type {
	if method.Impl != nil {
		return map[string]types.Type{"Self": t}
	}
	return ownerBindings(method.Decl, t)
}

// emitTableFills writes every method table in the program: one store of
// the table's record into its global.
func (l *lowerer) emitTableFills(f *funcLowerer) {
	for _, table := range l.vtableOrder {
		address := f.newReg("table.p")
		f.emit(&metals.GlobalPtr{Dst: address, Name: table.global})
		var codes []string
		for _, entry := range table.entries {
			code := f.newReg("entry")
			f.emit(&metals.CodeRef{Dst: code, Callee: entry})
			codes = append(codes, code)
		}
		record := f.newReg("table")
		f.emit(&metals.MakeRecord{Dst: record, TypeName: table.recType.Name, Args: codes})
		f.emit(&metals.Store{Ptr: address, Val: record})
	}
}

// lowerDynCast makes a trait value of a pointer.
func (f *funcLowerer) lowerDynCast(value string, from types.Type, to *types.Dyn) string {
	pointer, ok := from.(*types.Pointer)
	if !ok {
		f.l.fail("@%s: a trait value is made of a pointer, not %s", f.fn.Name, from)
	}
	table := f.l.tableFor(pointer.Elem, to.Ref)

	data := f.newReg("data")
	f.emit(&metals.PtrErase{Dst: data, X: value})
	address := f.newReg("table")
	f.emit(&metals.GlobalPtr{Dst: address, Name: table.global})

	out := f.newReg("dyn")
	named := f.l.metalsDyn(to).(*metals.Named)
	f.emit(&metals.MakeRecord{Dst: out, TypeName: named.Name, Args: []string{data, address}})
	return out
}

// prepareDynCall dispatches a method called on a trait value through its
// table.
func (f *funcLowerer) prepareDynCall(call *ast.CallExpr, resolved *sema.CallInfo) func() string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		f.l.fail("@%s: a trait value's method is called on a receiver", f.fn.Name)
	}

	index := -1
	for i, entry := range resolved.Dyn.Ref.AllMethods() {
		if entry.Method == resolved.Method {
			index = i
		}
	}
	if index < 0 {
		f.l.fail("@%s: %s is not in the table of %s", f.fn.Name, resolved.Method.Name, resolved.Dyn)
	}

	// The receiver is the trait value, or a pointer to one.
	recvType := f.typeOf(selector.X)
	value := f.lowerExpr(selector.X, nil)
	if pointer, ok := recvType.(*types.Pointer); ok {
		value = f.pointerPlace(value, pointer.Elem).load()
	}
	args := f.callArgs(call, resolved.Sig, nil)

	return func() string {
		data := f.newReg("data")
		f.emit(&metals.FieldGet{Dst: data, X: value, Index: 0})
		table := f.newReg("table")
		f.emit(&metals.FieldGet{Dst: table, X: value, Index: 1})
		slot := f.newReg("entry.p")
		f.emit(&metals.FieldPtr{Dst: slot, Ptr: table, Index: index})
		code := f.newReg("entry")
		f.emit(&metals.Load{Dst: code, Ptr: slot})

		all := append([]string{data}, args...)
		if isVoid(resolved.Sig.Result) {
			f.emit(&metals.CallIndirect{Fn: code, Args: all})
			return ""
		}
		out := f.newReg("r")
		f.emit(&metals.CallIndirect{Dst: out, Fn: code, Args: all})
		return out
	}
}
