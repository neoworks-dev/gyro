package lisp

import (
	"sort"
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// A lambda is lifted to a top-level function taking its captured
// environment as a first parameter, which is exactly the shape METALS's
// `closure` partially applies. Nothing here is special to Lisp — Gyro's
// lowering does the same thing — and that is the point: two front ends
// reaching the same instruction the same way is evidence the instruction
// is the right one.
//
// Parameters are written either bare, when the context already says what
// the function's type is, or as (name type) pairs when it does not:
//
//	(map-i64 xs (lambda (x) (* x 2)))     ; map-i64 declares the type
//	(let ((f (lambda ((x i64)) (* x 2)))) ; nothing to take it from
//
// Captures are by copy, as they are in Gyro. A lambda that assigns to a
// captured name is rejected rather than silently writing to the copy.
func (f *fnScope) lambdaForm(d *list, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	if len(d.items) < 3 {
		return "", nil, f.c.errf(d.offset, "a lambda is (lambda (params...) body...)")
	}
	params, ok := d.items[1].(*list)
	if !ok {
		return "", nil, f.c.errf(d.items[1].pos(), "a lambda needs a parameter list")
	}

	names, types, err := f.lambdaParams(params, want)
	if err != nil {
		return "", nil, err
	}

	var wantResult metals.Type
	if sig, ok := want.(*metals.FnType); ok {
		wantResult = sig.Result
	}
	return f.closureValue(names, types, d.items[2:], wantResult, s, body)
}

// closureValue lifts a body to a top-level function and produces the
// function value for it. Both a lambda and an internal define arrive
// here; they differ only in where the parameter types came from.
//
// Everything the body uses that the parameters do not bind, and that the
// enclosing function has a binding for, is captured. A name that resolves
// to a top-level function or a primitive is not: those need no
// environment.
func (f *fnScope) closureValue(names []string, types []metals.Type, forms []datum, wantResult metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	bound := map[string]bool{}
	for _, name := range names {
		bound[name] = true
	}
	used := map[string]bool{}
	for _, form := range forms {
		freeNames(form, bound, used)
	}
	var captures []string
	for name := range used {
		if _, isLocal := s.lookup(name); isLocal {
			captures = append(captures, name)
		}
	}
	// The IR has to come out the same on every run, and map order does
	// not.
	sort.Strings(captures)

	lifted, resultType, err := f.lift(forms, names, types, captures, wantResult, s)
	if err != nil {
		return "", nil, err
	}

	value := f.gensym("fn")
	if len(captures) == 0 {
		// With nothing to capture there is no environment, so the
		// lifted function is referenced directly.
		*body = append(*body, &metals.FnRef{Dst: value, Callee: lifted})
		return value, &metals.FnType{Params: types, Result: resultType}, nil
	}

	// The environment is heap storage, filled field by field: the
	// closure outlives this frame, so the captures cannot live in it.
	env := f.gensym("env")
	*body = append(*body, &metals.New{Dst: env, Type: &metals.Named{Name: lifted + ".env"}})
	for i, name := range captures {
		b, _ := s.lookup(name)
		field := f.gensym("field")
		*body = append(*body, &metals.FieldPtr{Dst: field, Ptr: env, Index: i})
		*body = append(*body, &metals.Store{Ptr: field, Val: b.reg})
	}
	*body = append(*body, &metals.MakeClosure{Dst: value, Callee: lifted, Env: env})
	return value, &metals.FnType{Params: types, Result: resultType}, nil
}

// lambdaParams reads the parameter list in either spelling and returns
// the names and their types.
func (f *fnScope) lambdaParams(params *list, want metals.Type) ([]string, []metals.Type, error) {
	names := make([]string, len(params.items))
	types := make([]metals.Type, len(params.items))

	annotated := 0
	for _, item := range params.items {
		if _, ok := item.(*list); ok {
			annotated++
		}
	}
	if annotated != 0 && annotated != len(params.items) {
		return nil, nil, f.c.errf(params.offset,
			"either every parameter of this lambda carries a type or none does")
	}

	if annotated == 0 {
		sig, ok := want.(*metals.FnType)
		if !ok {
			return nil, nil, f.c.errf(params.offset,
				"nothing here says what this lambda's parameters are; write them as (name type)")
		}
		if len(sig.Params) != len(params.items) {
			return nil, nil, f.c.errf(params.offset,
				"this lambda takes %d parameter(s) where %s is wanted", len(params.items), sig)
		}
		for i, item := range params.items {
			name, ok := item.(*symbol)
			if !ok {
				return nil, nil, f.c.errf(item.pos(), "a parameter is a name")
			}
			names[i], types[i] = name.name, sig.Params[i]
		}
		return names, types, nil
	}

	for i, item := range params.items {
		pair := item.(*list)
		if len(pair.items) != 2 {
			return nil, nil, f.c.errf(pair.offset, "a typed parameter is (name type)")
		}
		name, ok := pair.items[0].(*symbol)
		if !ok {
			return nil, nil, f.c.errf(pair.items[0].pos(), "a parameter is a name")
		}
		typ, err := parseType(pair.items[1])
		if err != nil {
			return nil, nil, err
		}
		names[i], types[i] = name.name, typ
	}
	return names, types, nil
}

// lift compiles the lambda's body as a top-level function and returns its
// name and result type. The environment, when there is one, is a record
// declared alongside it.
func (f *fnScope) lift(forms []datum, names []string, types []metals.Type, captures []string, wantResult metals.Type, outer *scope) (string, metals.Type, error) {
	f.c.lambdas++
	// `#` keeps the name outside what any source can write, so a lifted
	// function can never collide with a definition.
	name := "lambda#" + strconv.Itoa(f.c.lambdas)

	fn := &metals.Func{Name: name}
	inner := &fnScope{c: f.c, fn: fn}
	s := newScope(nil)

	var body []metals.Instr
	if len(captures) > 0 {
		envType := name + ".env"
		decl := &metals.TypeDecl{Name: envType, Record: &metals.RecordShape{}}
		envReg := inner.gensym("env")
		fn.Params = append(fn.Params, metals.Param{Name: envReg, Type: &metals.Ptr{Elem: &metals.Named{Name: envType}}})

		for index, captured := range captures {
			b, _ := outer.lookup(captured)
			decl.Record.Fields = append(decl.Record.Fields, metals.FieldDecl{Name: captured, Type: b.typ})

			field := inner.gensym("field")
			reg := inner.gensym(captured)
			body = append(body, &metals.FieldPtr{Dst: field, Ptr: envReg, Index: index})
			body = append(body, &metals.Load{Dst: reg, Ptr: field})
			s.names[captured] = binding{reg: reg, typ: b.typ, captured: true}
		}
		f.c.module.Types = append(f.c.module.Types, decl)
	}

	for i, param := range names {
		reg := inner.gensym(param)
		fn.Params = append(fn.Params, metals.Param{Name: reg, Type: types[i]})
		s.bind(param, reg, types[i])
	}

	result, resultType, err := inner.sequence(forms, wantResult, s, &body)
	if err != nil {
		return "", nil, err
	}
	if resultType == nil {
		body = append(body, &metals.Return{})
	} else {
		body = append(body, &metals.Return{Val: result})
	}

	fn.Result = resultType
	fn.Body = body
	f.c.module.Funcs = append(f.c.module.Funcs, fn)
	return name, resultType, nil
}

// freeNames collects every name a form uses without binding it first.
// The binding forms have to be understood here, or a lambda would capture
// its own locals.
func freeNames(d datum, bound map[string]bool, out map[string]bool) {
	switch d := d.(type) {
	case *symbol:
		if !bound[d.name] {
			out[d.name] = true
		}
	case *list:
		freeInList(d, bound, out)
	}
}

func freeInList(d *list, bound map[string]bool, out map[string]bool) {
	if len(d.items) == 0 {
		return
	}
	head, isSymbol := d.items[0].(*symbol)
	if !isSymbol {
		for _, item := range d.items {
			freeNames(item, bound, out)
		}
		return
	}

	switch head.name {
	case "let", "let*":
		if len(d.items) < 3 {
			return
		}
		bindings, ok := d.items[1].(*list)
		if !ok {
			return
		}
		inner := extend(bound)
		for _, entry := range bindings.items {
			pair, ok := entry.(*list)
			if !ok || len(pair.items) != 2 {
				continue
			}
			// A `let` sees its initializers in the outer scope and a
			// `let*` in the one it is building.
			if head.name == "let*" {
				freeNames(pair.items[1], inner, out)
			} else {
				freeNames(pair.items[1], bound, out)
			}
			if name, ok := pair.items[0].(*symbol); ok {
				inner[name.name] = true
			}
		}
		for _, form := range d.items[2:] {
			freeNames(form, inner, out)
		}

	case "lambda":
		if len(d.items) < 3 {
			return
		}
		params, ok := d.items[1].(*list)
		if !ok {
			return
		}
		inner := extend(bound)
		for _, item := range params.items {
			switch param := item.(type) {
			case *symbol:
				inner[param.name] = true
			case *list:
				if len(param.items) > 0 {
					if name, ok := param.items[0].(*symbol); ok {
						inner[name.name] = true
					}
				}
			}
		}
		for _, form := range d.items[2:] {
			freeNames(form, inner, out)
		}

	case "set!":
		// The target is a use as well as an assignment: a lambda that
		// writes to an outer name still refers to it.
		for _, item := range d.items[1:] {
			freeNames(item, bound, out)
		}

	case "cond":
		for _, clause := range d.items[1:] {
			entry, ok := clause.(*list)
			if !ok || len(entry.items) == 0 {
				continue
			}
			start := 0
			if name, ok := entry.items[0].(*symbol); ok && name.name == "else" {
				start = 1
			}
			for _, form := range entry.items[start:] {
				freeNames(form, bound, out)
			}
		}

	default:
		for _, item := range d.items {
			freeNames(item, bound, out)
		}
	}
}

func extend(bound map[string]bool) map[string]bool {
	inner := make(map[string]bool, len(bound))
	for name := range bound {
		inner[name] = true
	}
	return inner
}
