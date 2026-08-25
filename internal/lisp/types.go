package lisp

import "github.com/gyro-lang/gyro/internal/metals"

// Types are written as data, like everything else: `i64` is a symbol and
// a function type is the list `(-> i64 i64)`, whose last element is the
// result. `void` is the result of a function that returns nothing, and is
// spelled only there.
//
// The set is deliberately the part of METALS that crosses a boundary
// without conversion. A Lisp that could name `ptr<T>` would be able to
// write an export the linker has to reject.
func parseType(d datum) (metals.Type, error) {
	switch d := d.(type) {
	case *symbol:
		if prim, ok := metals.Prims[d.name]; ok {
			return prim, nil
		}
		switch d.name {
		case "string":
			// `str` is the METALS spelling and comes out of Prims; this
			// is the alias a Scheme reader expects to work.
			return metals.TStr, nil
		case "any":
			return &metals.Any{}, nil
		case "void":
			return nil, &posError{offset: d.offset, msg: "void is a result, not a type: it may only be the last element of a (-> ...)"}
		}
		return nil, &posError{offset: d.offset, msg: "unknown type " + d.name}

	case *list:
		return parseFnType(d)
	}
	return nil, &posError{offset: d.pos(), msg: "expected a type"}
}

func parseFnType(d *list) (metals.Type, error) {
	if len(d.items) == 0 {
		return nil, &posError{offset: d.offset, msg: "expected a type, found ()"}
	}
	arrow, ok := d.items[0].(*symbol)
	if !ok || arrow.name != "->" {
		return nil, &posError{offset: d.offset, msg: "a compound type is (-> params... result)"}
	}
	if len(d.items) < 2 {
		return nil, &posError{offset: d.offset, msg: "(-> ...) needs a result type; use (-> void) for none"}
	}

	sig := &metals.FnType{}
	for _, item := range d.items[1 : len(d.items)-1] {
		param, err := parseType(item)
		if err != nil {
			return nil, err
		}
		sig.Params = append(sig.Params, param)
	}

	// The result is the one position `void` is allowed in, where it
	// means METALS's absent result rather than a type.
	last := d.items[len(d.items)-1]
	if name, ok := last.(*symbol); ok && name.name == "void" {
		return sig, nil
	}
	result, err := parseType(last)
	if err != nil {
		return nil, err
	}
	sig.Result = result
	return sig, nil
}

// numeric reports whether a type takes the integer or float instruction
// set, and which.
func numeric(t metals.Type) (isInt, isFloat bool) {
	prim, ok := t.(*metals.Prim)
	if !ok {
		return false, false
	}
	switch prim.Kind {
	case metals.I8, metals.I16, metals.I32, metals.I64,
		metals.U8, metals.U16, metals.U32, metals.U64, metals.Usize:
		return true, false
	case metals.F32, metals.F64:
		return false, true
	}
	return false, false
}

func isBool(t metals.Type) bool {
	prim, ok := t.(*metals.Prim)
	return ok && prim.Kind == metals.Bool
}

func typeName(t metals.Type) string {
	if t == nil {
		return "void"
	}
	return t.String()
}
