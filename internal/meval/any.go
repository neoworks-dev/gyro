package meval

import (
	"github.com/gyro-lang/gyro/internal/metals"
)

// The static type an any.wrap captures belongs to the instruction, not to
// the value that reaches it: `any.wrap %x` boxes %x at %x's register
// type, and that is what any.is and any.unwrap test against later. The
// evaluator does not carry register types through execution, so every
// wrap site is resolved once, up front, from the table the verifier
// already builds.
func anyWrapTypes(module *metals.Module) (map[*metals.AnyWrap]metals.Type, error) {
	regTypes, err := metals.RegisterTypes(module)
	if err != nil {
		return nil, err
	}
	wrapped := map[*metals.AnyWrap]metals.Type{}
	for _, fn := range module.Funcs {
		collectWraps(fn.Body, regTypes[fn.Name], wrapped)
	}
	return wrapped, nil
}

func collectWraps(body []metals.Instr, regs map[string]metals.Type, out map[*metals.AnyWrap]metals.Type) {
	for _, instr := range body {
		switch i := instr.(type) {
		case *metals.AnyWrap:
			out[i] = regs[i.X]
		case *metals.If:
			collectWraps(i.Then, regs, out)
			collectWraps(i.Else, regs, out)
		case *metals.Block:
			collectWraps(i.Body, regs, out)
		case *metals.Loop:
			collectWraps(i.Body, regs, out)
		case *metals.Switch:
			for _, c := range i.Cases {
				collectWraps(c.Body, regs, out)
			}
			collectWraps(i.Default, regs, out)
		case *metals.Select:
			for _, arm := range i.Arms {
				collectWraps(arm.Body, regs, out)
			}
			collectWraps(i.Default, regs, out)
		}
	}
}

// ---------------------------------------------------------------------------
// The six operations
// ---------------------------------------------------------------------------

func (m *machine) wrapAny(instr *metals.AnyWrap, v value) value {
	// Anys do not nest, the way optionals do not: wrapping one yields
	// the same box rather than a box of a box.
	if box, ok := v.(anyVal); ok {
		return box
	}
	typ, ok := m.wrapTypes[instr]
	if !ok {
		m.throw("any.wrap of a register with no resolved type")
	}
	return newAny(typ, v)
}

func newAny(typ metals.Type, v value) anyVal {
	cell := copyValue(v)
	return anyVal{typ: typ, cell: &cell}
}

func (m *machine) useAny(v value, what string) anyVal {
	box, ok := v.(anyVal)
	if !ok {
		m.throw("%s on a value that is not an any", what)
	}
	return box
}

func (m *machine) unwrapAny(box anyVal, to metals.Type, what string) value {
	if !metals.TypesEqual(box.typ, to) {
		m.throw("%s: the any holds %s, not %s", what, box.typ, to)
	}
	return copyValue(*box.cell)
}

// intoAny boxes a value known to have type typ, passing an any through
// unchanged so that an any-typed field or result does not double-wrap.
func intoAny(typ metals.Type, v value) value {
	if _, already := typ.(*metals.Any); already {
		return v
	}
	return newAny(typ, v)
}

// fromAny is intoAny's inverse: it takes what an any-carrying position
// holds and produces a value of the declared type.
func (m *machine) fromAny(typ metals.Type, v value, what string) value {
	if _, keep := typ.(*metals.Any); keep {
		return v
	}
	return m.unwrapAny(m.useAny(v, what), typ, what)
}

// ---------------------------------------------------------------------------
// Named access
// ---------------------------------------------------------------------------

// anyField resolves a named field against the record an any holds. The
// record's own declaration is the source of the name, so any.get and
// any.set reach exactly the fields the type declares and nothing else.
func (m *machine) anyField(box anyVal, name, what string) (recordVal, *metals.FieldDecl, int) {
	record, ok := (*box.cell).(recordVal)
	if !ok {
		m.throw("%s on an any holding %s, which has no named fields", what, box.typ)
	}
	decl := m.types[record.typeName]
	if decl == nil || decl.Record == nil {
		m.throw("%s: %%%s is not a record", what, record.typeName)
	}
	for index := range decl.Record.Fields {
		if decl.Record.Fields[index].Name == name {
			return record, &decl.Record.Fields[index], index
		}
	}
	m.throw("%s: %%%s has no field %q", what, record.typeName, name)
	return recordVal{}, nil, 0
}

func (m *machine) anyGet(box anyVal, name string) value {
	record, field, index := m.anyField(box, name, "any.get")
	return intoAny(field.Type, record.fields[index])
}

func (m *machine) anySet(box anyVal, name string, v value) {
	record, field, index := m.anyField(box, name, "any.set")
	// record.fields is the box's own backing slice, so this write is
	// visible through every other reference to the same box. That is
	// what makes a mutable pair expressible with nothing but an any.
	record.fields[index] = m.fromAny(field.Type, v, "any.set")
}

// ---------------------------------------------------------------------------
// Indirect call
// ---------------------------------------------------------------------------

// anyCall applies the function an any holds. Arguments arrive as anys and
// the result leaves as one, so the boxed signature is what decides how
// each is converted: a caller that knows nothing about the callee still
// hands it values of the types it declared.
func (m *machine) anyCall(box anyVal, args []value) value {
	sig, isFn := box.typ.(*metals.FnType)
	fn, isRef := (*box.cell).(fnVal)
	if !isFn || !isRef {
		m.throw("any.call on an any holding %s, which is not a function", box.typ)
	}
	if len(sig.Params) != len(args) {
		m.throw("any.call passes %d argument(s) to %s", len(args), sig)
	}
	if sig.Result == nil {
		m.throw("any.call of %s, which returns nothing an any could hold", sig)
	}

	converted := make([]value, len(args))
	for i, arg := range args {
		converted[i] = m.fromAny(sig.Params[i], arg, "any.call argument")
	}
	result := m.callFn(fn, converted)
	if future, ok := result.(*futureVal); ok && sig.Async {
		result = future.await()
	}
	return intoAny(sig.Result, result)
}
