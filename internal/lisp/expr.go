package lisp

import (
	"fmt"
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// expr compiles one form. want is the type the context needs, or nil when
// the form is in statement position or its type is to be inferred; the
// literals are the only forms that actually read it, which is as far as
// this language takes bidirectional checking.
func (f *fnScope) expr(d datum, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	reg, typ, err := f.exprInner(d, want, s, body)
	if err != nil {
		return "", nil, err
	}
	if want != nil && !metals.TypesEqual(typ, want) {
		return "", nil, f.c.errf(d.pos(), "expected %s, found %s", typeName(want), typeName(typ))
	}
	return reg, typ, nil
}

func (f *fnScope) exprInner(d datum, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	switch d := d.(type) {
	case *intLit:
		return f.intLiteral(d, want, body)
	case *floatLit:
		typ := metals.TF64
		if _, isFloat := numeric(want); isFloat {
			typ = want.(*metals.Prim)
		}
		return f.constant(body, typ, strconv.FormatFloat(d.v, 'g', -1, 64)), typ, nil
	case *strLit:
		return f.constant(body, metals.TStr, strconv.Quote(d.v)), metals.TStr, nil
	case *boolLit:
		return f.constant(body, metals.TBool, strconv.FormatBool(d.v)), metals.TBool, nil
	case *symbol:
		return f.variable(d, s, body)
	case *list:
		return f.form(d, want, s, body)
	}
	return "", nil, f.c.errf(d.pos(), "unexpected form")
}

// intLiteral takes its type from the context: an integer written where an
// i32 is wanted is an i32, and one written nowhere in particular is an
// i64. A literal that does not fit the type the context asks for is an
// error rather than a silent truncation.
func (f *fnScope) intLiteral(d *intLit, want metals.Type, body *[]metals.Instr) (string, metals.Type, error) {
	if _, isFloat := numeric(want); isFloat {
		typ := want.(*metals.Prim)
		return f.constant(body, typ, strconv.FormatFloat(float64(d.v), 'g', -1, 64)), typ, nil
	}

	typ := metals.TInt64
	if isInt, _ := numeric(want); isInt {
		typ = want.(*metals.Prim)
	}
	if !fits(d.v, typ) {
		return "", nil, f.c.errf(d.offset, "%d does not fit %s", d.v, typ)
	}
	return f.constant(body, typ, strconv.FormatInt(d.v, 10)), typ, nil
}

func fits(v int64, t *metals.Prim) bool {
	var bits int
	signed := true
	switch t.Kind {
	case metals.I8:
		bits = 8
	case metals.I16:
		bits = 16
	case metals.I32:
		bits = 32
	case metals.I64:
		return true
	case metals.U8:
		bits, signed = 8, false
	case metals.U16:
		bits, signed = 16, false
	case metals.U32:
		bits, signed = 32, false
	case metals.U64, metals.Usize:
		return v >= 0
	default:
		return false
	}
	if !signed {
		return v >= 0 && v>>bits == 0
	}
	limit := int64(1) << (bits - 1)
	return v >= -limit && v < limit
}

func (f *fnScope) constant(body *[]metals.Instr, typ metals.Type, text string) string {
	reg := f.gensym("c")
	*body = append(*body, &metals.Const{Dst: reg, Type: typ, Value: text})
	return reg
}

// variable resolves a name: a local first, then a top-level function,
// which as a value is a function reference.
func (f *fnScope) variable(d *symbol, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	if b, ok := s.lookup(d.name); ok {
		return b.reg, b.typ, nil
	}
	if sig, ok := f.c.sigs[d.name]; ok {
		reg := f.gensym(d.name)
		*body = append(*body, &metals.FnRef{Dst: reg, Callee: d.name})
		return reg, sig, nil
	}
	return "", nil, f.c.errf(d.offset, "%s is not defined", d.name)
}

// form compiles a compound form: a special form when the head is one of
// the keywords, a primitive when it names one, and otherwise a call.
func (f *fnScope) form(d *list, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	if len(d.items) == 0 {
		return "", nil, f.c.errf(d.offset, "() is not a form")
	}

	if head, ok := d.items[0].(*symbol); ok {
		switch head.name {
		case "if":
			return f.ifForm(d, want, s, body)
		case "let":
			return f.letForm(d, want, s, body, false)
		case "let*":
			return f.letForm(d, want, s, body, true)
		case "begin":
			return f.sequence(d.items[1:], want, newScope(s), body)
		case "set!":
			return f.setForm(d, s, body)
		case "cond":
			return f.condForm(d, want, s, body)
		case "lambda":
			return f.lambdaForm(d, want, s, body)
		case "and", "or":
			return f.andOr(d, head.name == "and", s, body)
		case "not":
			return f.notForm(d, s, body)
		case "define", ":", "export":
			return "", nil, f.c.errf(head.offset, "%s belongs at the top level", head.name)
		}
		if _, isPrimitive := primitives[head.name]; isPrimitive {
			if _, shadowed := s.lookup(head.name); !shadowed {
				return f.primitive(head, d, want, s, body)
			}
		}
	}
	return f.call(d, s, body)
}

// ---------------------------------------------------------------------------
// Special forms
// ---------------------------------------------------------------------------

func (f *fnScope) ifForm(d *list, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	if len(d.items) != 3 && len(d.items) != 4 {
		return "", nil, f.c.errf(d.offset, "an if is (if test then) or (if test then else)")
	}
	cond, _, err := f.expr(d.items[1], metals.TBool, s, body)
	if err != nil {
		return "", nil, err
	}

	// A one-armed if has no value: there is no second branch to produce
	// one, and METALS counts a register as assigned only when every path
	// assigns it.
	if len(d.items) == 3 {
		var then []metals.Instr
		if _, _, err := f.expr(d.items[2], nil, newScope(s), &then); err != nil {
			return "", nil, err
		}
		*body = append(*body, &metals.If{Cond: cond, Then: then})
		return "", nil, nil
	}

	var thenBody, elseBody []metals.Instr
	thenReg, thenType, err := f.expr(d.items[2], want, newScope(s), &thenBody)
	if err != nil {
		return "", nil, err
	}
	elseReg, elseType, err := f.expr(d.items[3], firstNonNil(want, thenType), newScope(s), &elseBody)
	if err != nil {
		return "", nil, err
	}

	if thenType == nil || elseType == nil {
		*body = append(*body, &metals.If{Cond: cond, Then: thenBody, Else: elseBody})
		return "", nil, nil
	}
	if !metals.TypesEqual(thenType, elseType) {
		return "", nil, f.c.errf(d.offset, "the branches of this if produce %s and %s",
			typeName(thenType), typeName(elseType))
	}

	// Both arms write the same register, which is what makes the if an
	// expression: after it, that register is assigned on every path.
	result := f.gensym("if")
	thenBody = append(thenBody, &metals.Copy{Dst: result, X: thenReg})
	elseBody = append(elseBody, &metals.Copy{Dst: result, X: elseReg})
	*body = append(*body, &metals.If{Cond: cond, Then: thenBody, Else: elseBody})
	return result, thenType, nil
}

func firstNonNil(a, b metals.Type) metals.Type {
	if a != nil {
		return a
	}
	return b
}

func (f *fnScope) letForm(d *list, want metals.Type, s *scope, body *[]metals.Instr, sequential bool) (string, metals.Type, error) {
	if len(d.items) < 3 {
		return "", nil, f.c.errf(d.offset, "a let is (let ((name value)...) body...)")
	}
	bindings, ok := d.items[1].(*list)
	if !ok {
		return "", nil, f.c.errf(d.items[1].pos(), "a let needs a binding list")
	}

	inner := newScope(s)
	// `let` evaluates its initializers in the enclosing scope and `let*`
	// in the one it is building, which is the whole difference.
	init := s
	if sequential {
		init = inner
	}
	for _, entry := range bindings.items {
		pair, ok := entry.(*list)
		if !ok || len(pair.items) != 2 {
			return "", nil, f.c.errf(entry.pos(), "a binding is (name value)")
		}
		name, ok := pair.items[0].(*symbol)
		if !ok {
			return "", nil, f.c.errf(pair.items[0].pos(), "a binding needs a name")
		}
		value, typ, err := f.expr(pair.items[1], nil, init, body)
		if err != nil {
			return "", nil, err
		}
		if typ == nil {
			return "", nil, f.c.errf(pair.items[1].pos(), "%s would be bound to nothing", name.name)
		}
		// The binding gets its own register rather than aliasing the
		// initializer's, so a later set! cannot write through to it.
		reg := f.gensym(name.name)
		*body = append(*body, &metals.Copy{Dst: reg, X: value})
		inner.bind(name.name, reg, typ)
	}
	return f.sequence(d.items[2:], want, inner, body)
}

func (f *fnScope) setForm(d *list, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	if len(d.items) != 3 {
		return "", nil, f.c.errf(d.offset, "an assignment is (set! name value)")
	}
	name, ok := d.items[1].(*symbol)
	if !ok {
		return "", nil, f.c.errf(d.items[1].pos(), "set! assigns to a name")
	}
	b, ok := s.lookup(name.name)
	if !ok {
		return "", nil, f.c.errf(name.offset, "%s is not a local; set! cannot assign to a definition", name.name)
	}
	if b.captured {
		return "", nil, f.c.errf(name.offset,
			"%s was captured by this lambda, and captures are copies; assigning to it would not be visible outside", name.name)
	}

	// A register's type is fixed at its first assignment, so the value
	// is checked against what the binding already holds.
	value, _, err := f.expr(d.items[2], b.typ, s, body)
	if err != nil {
		return "", nil, err
	}
	*body = append(*body, &metals.Copy{Dst: b.reg, X: value})
	return "", nil, nil
}

func (f *fnScope) condForm(d *list, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	if len(d.items) < 2 {
		return "", nil, f.c.errf(d.offset, "a cond needs at least one clause")
	}
	// A cond is nested ifs, so it is rewritten into them rather than
	// compiled twice. The last clause must be `else` whenever the cond
	// has to produce a value, and that falls out of the if rule.
	return f.clause(d.items[1:], want, s, body)
}

func (f *fnScope) clause(clauses []datum, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	head, ok := clauses[0].(*list)
	if !ok || len(head.items) < 2 {
		return "", nil, f.c.errf(clauses[0].pos(), "a cond clause is (test body...)")
	}

	if name, isElse := head.items[0].(*symbol); isElse && name.name == "else" {
		if len(clauses) > 1 {
			return "", nil, f.c.errf(clauses[1].pos(), "this clause follows an else")
		}
		return f.sequence(head.items[1:], want, newScope(s), body)
	}
	if len(clauses) == 1 {
		if want == nil {
			cond, _, err := f.expr(head.items[0], metals.TBool, s, body)
			if err != nil {
				return "", nil, err
			}
			var then []metals.Instr
			if _, _, err := f.sequence(head.items[1:], nil, newScope(s), &then); err != nil {
				return "", nil, err
			}
			*body = append(*body, &metals.If{Cond: cond, Then: then})
			return "", nil, nil
		}
		return "", nil, f.c.errf(head.offset, "this cond has to produce a %s, so it needs an else clause", typeName(want))
	}

	cond, _, err := f.expr(head.items[0], metals.TBool, s, body)
	if err != nil {
		return "", nil, err
	}
	var thenBody, elseBody []metals.Instr
	thenReg, thenType, err := f.sequence(head.items[1:], want, newScope(s), &thenBody)
	if err != nil {
		return "", nil, err
	}
	elseReg, elseType, err := f.clause(clauses[1:], firstNonNil(want, thenType), s, &elseBody)
	if err != nil {
		return "", nil, err
	}

	if thenType == nil || elseType == nil {
		*body = append(*body, &metals.If{Cond: cond, Then: thenBody, Else: elseBody})
		return "", nil, nil
	}
	if !metals.TypesEqual(thenType, elseType) {
		return "", nil, f.c.errf(head.offset, "the clauses of this cond produce %s and %s",
			typeName(thenType), typeName(elseType))
	}
	result := f.gensym("cond")
	thenBody = append(thenBody, &metals.Copy{Dst: result, X: thenReg})
	elseBody = append(elseBody, &metals.Copy{Dst: result, X: elseReg})
	*body = append(*body, &metals.If{Cond: cond, Then: thenBody, Else: elseBody})
	return result, thenType, nil
}

// andOr compiles the short-circuiting connectives. Both fold left, and
// both write one register that every path assigns.
func (f *fnScope) andOr(d *list, isAnd bool, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	if len(d.items) < 2 {
		// (and) is true and (or) is false, as Scheme has it.
		return f.constant(body, metals.TBool, strconv.FormatBool(isAnd)), metals.TBool, nil
	}

	result, _, err := f.expr(d.items[1], metals.TBool, s, body)
	if err != nil {
		return "", nil, err
	}
	acc := f.gensym("acc")
	*body = append(*body, &metals.Copy{Dst: acc, X: result})

	for _, operand := range d.items[2:] {
		cond := acc
		if !isAnd {
			// `or` continues while the accumulator is still false.
			cond = f.gensym("more")
			*body = append(*body, &metals.Un{Dst: cond, Op: "not", X: acc})
		}
		var next []metals.Instr
		reg, _, err := f.expr(operand, metals.TBool, s, &next)
		if err != nil {
			return "", nil, err
		}
		next = append(next, &metals.Copy{Dst: acc, X: reg})
		*body = append(*body, &metals.If{Cond: cond, Then: next})
	}
	return acc, metals.TBool, nil
}

func (f *fnScope) notForm(d *list, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	if len(d.items) != 2 {
		return "", nil, f.c.errf(d.offset, "not takes one argument")
	}
	x, _, err := f.expr(d.items[1], metals.TBool, s, body)
	if err != nil {
		return "", nil, err
	}
	reg := f.gensym("not")
	*body = append(*body, &metals.Un{Dst: reg, Op: "not", X: x})
	return reg, metals.TBool, nil
}

// ---------------------------------------------------------------------------
// Calls
// ---------------------------------------------------------------------------

// call compiles an application. A name that resolves to a top-level
// function is a direct call; anything else has to evaluate to a function
// value and goes through call.indirect.
func (f *fnScope) call(d *list, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	if name, ok := d.items[0].(*symbol); ok {
		if _, local := s.lookup(name.name); !local {
			sig, isTopLevel := f.c.sigs[name.name]
			if !isTopLevel {
				return "", nil, f.c.errf(name.offset, "%s is not defined", name.name)
			}
			args, err := f.arguments(d, sig, s, body)
			if err != nil {
				return "", nil, err
			}
			dst := ""
			if sig.Result != nil {
				dst = f.gensym(name.name)
			}
			*body = append(*body, &metals.Call{Dst: dst, Callee: name.name, Args: args})
			return dst, sig.Result, nil
		}
	}

	callee, typ, err := f.expr(d.items[0], nil, s, body)
	if err != nil {
		return "", nil, err
	}
	sig, ok := typ.(*metals.FnType)
	if !ok {
		return "", nil, f.c.errf(d.items[0].pos(), "%s is not a function", typeName(typ))
	}
	args, err := f.arguments(d, sig, s, body)
	if err != nil {
		return "", nil, err
	}
	dst := ""
	if sig.Result != nil {
		dst = f.gensym("call")
	}
	*body = append(*body, &metals.CallIndirect{Dst: dst, Fn: callee, Args: args})
	return dst, sig.Result, nil
}

func (f *fnScope) arguments(d *list, sig *metals.FnType, s *scope, body *[]metals.Instr) ([]string, error) {
	given := d.items[1:]
	if len(given) != len(sig.Params) {
		return nil, f.c.errf(d.offset, "this call passes %d argument(s) to %s", len(given), sig)
	}
	args := make([]string, len(given))
	for i, arg := range given {
		reg, _, err := f.expr(arg, sig.Params[i], s, body)
		if err != nil {
			return nil, err
		}
		args[i] = reg
	}
	return args, nil
}

// ensureImport declares a runtime intrinsic the first time it is used.
// The name carries the instantiation the way Gyro's lowering spells it,
// `println<i64>`, because the backends dispatch on the base name and the
// parameter type together.
func (c *compiler) ensureImport(base string, sig *metals.FnType) string {
	name := base
	if len(sig.Params) > 0 {
		name = fmt.Sprintf("%s<%s>", base, sig.Params[0])
	}
	if c.imported[name] {
		return name
	}
	c.imported[name] = true

	for _, imp := range c.module.Imports {
		if imp.Module == "gyro:rt" {
			imp.Funcs = append(imp.Funcs, metals.ImportedFunc{Name: name, Sig: sig})
			return name
		}
	}
	c.module.Imports = append(c.module.Imports, &metals.Import{
		Module: "gyro:rt",
		Funcs:  []metals.ImportedFunc{{Name: name, Sig: sig}},
	})
	return name
}
