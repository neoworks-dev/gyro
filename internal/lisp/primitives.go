package lisp

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// A primitive compiles to instructions rather than to a call. They are
// ordinary names, so a local binding shadows one: `(let ((+ ...)) ...)`
// means what it says, which is why form checks the scope first.
type primFn func(*fnScope, *list, metals.Type, *scope, *[]metals.Instr) (string, metals.Type, error)

var primitives map[string]primFn

func init() {
	primitives = map[string]primFn{
		"+":             arith,
		"-":             arith,
		"*":             arith,
		"/":             arith,
		"mod":           arith,
		"remainder":     arith,
		"<":             compare,
		"<=":            compare,
		">":             compare,
		">=":            compare,
		"=":             compare,
		"display":       output,
		"displayln":     output,
		"newline":       output,
		"string-append": stringAppend,
	}
}

func (f *fnScope) primitive(head *symbol, d *list, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	return primitives[head.name](f, d, want, s, body)
}

// arith folds the n-ary arithmetic operators left, the way Scheme
// specifies. The operand type comes from the first operand — which takes
// it from the context in turn — and every later operand is checked
// against it, since METALS has no implicit conversions either.
func arith(f *fnScope, d *list, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	op := d.items[0].(*symbol)
	operands := d.items[1:]

	if len(operands) == 0 {
		// (+) is 0 and (*) is 1; the others need an operand.
		switch op.name {
		case "+", "*":
			typ := metals.TInt64
			if isInt, _ := numeric(want); isInt {
				typ = want.(*metals.Prim)
			}
			identity := "0"
			if op.name == "*" {
				identity = "1"
			}
			return f.constant(body, typ, identity), typ, nil
		}
		return "", nil, f.c.errf(d.offset, "%s needs at least one operand", op.name)
	}

	acc, typ, err := f.expr(operands[0], want, s, body)
	if err != nil {
		return "", nil, err
	}
	isInt, isFloat := numeric(typ)
	if !isInt && !isFloat {
		return "", nil, f.c.errf(operands[0].pos(), "%s needs a number, not %s", op.name, typeName(typ))
	}
	if isFloat && (op.name == "mod" || op.name == "remainder") {
		return "", nil, f.c.errf(d.offset, "%s needs integers, not %s", op.name, typeName(typ))
	}

	// (- x) negates and (/ x) would be a reciprocal, which needs a one
	// to divide; only negation is worth the special case.
	if len(operands) == 1 {
		if op.name != "-" {
			return acc, typ, nil
		}
		reg := f.gensym("neg")
		*body = append(*body, &metals.Un{Dst: reg, Op: "neg", X: acc})
		return reg, typ, nil
	}

	mnemonic := arithOp(op.name, isFloat)
	for _, operand := range operands[1:] {
		reg, _, err := f.expr(operand, typ, s, body)
		if err != nil {
			return "", nil, err
		}
		dst := f.gensym(mnemonic)
		*body = append(*body, &metals.Bin{Dst: dst, Op: mnemonic, A: acc, B: reg})
		acc = dst
	}
	return acc, typ, nil
}

func arithOp(name string, isFloat bool) string {
	if isFloat {
		switch name {
		case "+":
			return "fadd"
		case "-":
			return "fsub"
		case "*":
			return "fmul"
		}
		return "fdiv"
	}
	switch name {
	case "+":
		return "iadd"
	case "-":
		return "isub"
	case "*":
		return "imul"
	case "/":
		return "idiv"
	}
	return "irem"
}

// compare is n-ary and chained: (< a b c) holds when a < b and b < c.
// Every operand is evaluated once, before any comparison, so the chain
// costs nothing in evaluation order; only the comparisons short-circuit.
func compare(f *fnScope, d *list, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	op := d.items[0].(*symbol)
	operands := d.items[1:]
	if len(operands) < 2 {
		return "", nil, f.c.errf(d.offset, "%s needs at least two operands", op.name)
	}

	regs := make([]string, len(operands))
	first, typ, err := f.expr(operands[0], nil, s, body)
	if err != nil {
		return "", nil, err
	}
	if typ == nil {
		return "", nil, f.c.errf(operands[0].pos(), "%s needs a value", op.name)
	}
	regs[0] = first
	for i, operand := range operands[1:] {
		reg, _, err := f.expr(operand, typ, s, body)
		if err != nil {
			return "", nil, err
		}
		regs[i+1] = reg
	}

	mnemonic := compareOp(op.name)
	if mnemonic != "eq" {
		if isInt, isFloat := numeric(typ); !isInt && !isFloat {
			if prim, ok := typ.(*metals.Prim); !ok || prim.Kind != metals.Rune {
				return "", nil, f.c.errf(d.offset, "%s orders numbers and runes, not %s", op.name, typeName(typ))
			}
		}
	}

	acc := f.gensym(mnemonic)
	*body = append(*body, &metals.Bin{Dst: acc, Op: mnemonic, A: regs[0], B: regs[1]})
	for i := 1; i+1 < len(regs); i++ {
		var next []metals.Instr
		step := f.gensym(mnemonic)
		next = append(next, &metals.Bin{Dst: step, Op: mnemonic, A: regs[i], B: regs[i+1]})
		next = append(next, &metals.Copy{Dst: acc, X: step})
		*body = append(*body, &metals.If{Cond: acc, Then: next})
	}
	return acc, metals.TBool, nil
}

func compareOp(name string) string {
	switch name {
	case "<":
		return "lt"
	case "<=":
		return "le"
	case ">":
		return "gt"
	case ">=":
		return "ge"
	}
	return "eq"
}

// output covers display, displayln and newline. They are the one place
// this language reaches the runtime, and they reach it the way Gyro does:
// as imports from gyro:rt named for the type they print.
func output(f *fnScope, d *list, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	op := d.items[0].(*symbol)

	if op.name == "newline" {
		if len(d.items) != 1 {
			return "", nil, f.c.errf(d.offset, "newline takes no arguments")
		}
		blank := f.constant(body, metals.TStr, `""`)
		name := f.c.ensureImport("println", &metals.FnType{Params: []metals.Type{metals.TStr}})
		*body = append(*body, &metals.Call{Callee: name, Args: []string{blank}})
		return "", nil, nil
	}

	if len(d.items) != 2 {
		return "", nil, f.c.errf(d.offset, "%s takes one argument", op.name)
	}
	value, typ, err := f.expr(d.items[1], nil, s, body)
	if err != nil {
		return "", nil, err
	}
	if typ == nil {
		return "", nil, f.c.errf(d.items[1].pos(), "%s needs a value", op.name)
	}

	base := "print"
	if op.name == "displayln" {
		base = "println"
	}
	name := f.c.ensureImport(base, &metals.FnType{Params: []metals.Type{typ}})
	*body = append(*body, &metals.Call{Callee: name, Args: []string{value}})
	return "", nil, nil
}

func stringAppend(f *fnScope, d *list, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	operands := d.items[1:]
	if len(operands) == 0 {
		return f.constant(body, metals.TStr, strconv.Quote("")), metals.TStr, nil
	}

	acc, _, err := f.expr(operands[0], metals.TStr, s, body)
	if err != nil {
		return "", nil, err
	}
	for _, operand := range operands[1:] {
		reg, _, err := f.expr(operand, metals.TStr, s, body)
		if err != nil {
			return "", nil, err
		}
		dst := f.gensym("str")
		*body = append(*body, &metals.StrConcat{Dst: dst, A: acc, B: reg})
		acc = dst
	}
	return acc, metals.TStr, nil
}
