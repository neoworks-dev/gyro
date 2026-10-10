package cc

import "fmt"

// foldTyped folds an integer constant expression and keeps the type C
// gives it. foldConstant answers a value for an array length or an
// enumerator, where the type does not matter; a macro imported into
// another language has to say whether it is a u32 or an i64, and the
// answer is the usual arithmetic conversions applied the way the
// compiler that reads the macro would apply them.
func foldTyped(e expr, enums map[string]int64) (int64, *ctype, error) {
	switch e := e.(type) {
	case *intLit:
		return int64(e.val), e.typ, nil

	case *identExpr:
		if value, ok := enums[e.name]; ok {
			return value, typeInt, nil
		}
		return 0, nil, fmt.Errorf("%s is not a constant", e.name)

	case *castExpr:
		x, _, err := foldTyped(e.x, enums)
		if err != nil {
			return 0, nil, err
		}
		if !e.typ.isInteger() {
			return 0, nil, fmt.Errorf("a cast to %s is not an integer constant", e.typ)
		}
		v, err := truncateTo(e.typ, x)
		return v, e.typ, err

	case *sizeofExpr:
		v, err := foldConstant(e, enums)
		return v, typeULong, err

	case *unaryExpr:
		x, t, err := foldTyped(e.x, enums)
		if err != nil {
			return 0, nil, err
		}
		if e.op == "!" {
			return boolValue(x == 0), typeInt, nil
		}
		t = promote(t)
		switch e.op {
		case "+":
		case "-":
			x = -x
		case "~":
			x = ^x
		default:
			return 0, nil, fmt.Errorf("%s is not a constant expression", e.op)
		}
		v, err := truncateTo(t, x)
		return v, t, err

	case *condExpr:
		cond, _, err := foldTyped(e.cond, enums)
		if err != nil {
			return 0, nil, err
		}
		a, ta, err := foldTyped(e.then, enums)
		if err != nil {
			return 0, nil, err
		}
		b, tb, err := foldTyped(e.els, enums)
		if err != nil {
			return 0, nil, err
		}
		t := usualArithmetic(ta, tb)
		chosen := b
		if cond != 0 {
			chosen = a
		}
		v, err := truncateTo(t, chosen)
		return v, t, err

	case *binaryExpr:
		a, ta, err := foldTyped(e.a, enums)
		if err != nil {
			return 0, nil, err
		}
		b, tb, err := foldTyped(e.b, enums)
		if err != nil {
			return 0, nil, err
		}
		switch e.op {
		case "&&", "||", "==", "!=", "<", ">", "<=", ">=":
			common := usualArithmetic(ta, tb)
			a, _ = truncateTo(common, a)
			b, _ = truncateTo(common, b)
			out, err := ppApply(token{kind: tokPunct, text: e.op, pos: e.pos},
				ppValue{v: a, unsigned: common.isUnsigned()}, ppValue{v: b, unsigned: common.isUnsigned()}, true)
			return out.v, typeInt, err
		case "<<", ">>":
			t := promote(ta)
			out, err := ppApply(token{kind: tokPunct, text: e.op, pos: e.pos},
				ppValue{v: a, unsigned: t.isUnsigned()}, ppValue{v: b}, true)
			if err != nil {
				return 0, nil, err
			}
			v, err := truncateTo(t, out.v)
			return v, t, err
		}
		common := usualArithmetic(ta, tb)
		out, err := ppApply(token{kind: tokPunct, text: e.op, pos: e.pos},
			ppValue{v: a, unsigned: common.isUnsigned()}, ppValue{v: b, unsigned: common.isUnsigned()}, true)
		if err != nil {
			return 0, nil, err
		}
		v, err := truncateTo(common, out.v)
		return v, common, err
	}
	return 0, nil, fmt.Errorf("this is not a constant expression")
}
