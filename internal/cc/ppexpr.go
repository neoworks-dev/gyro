package cc

// `#if` expressions.
//
// This is a third language again: the operands are integers and nothing
// else, every identifier that survives expansion is 0, and the
// arithmetic is done in the widest integer the target has. None of it
// reaches the parser, so it gets its own evaluator rather than a mode
// bit in the one that does.

// ppValue is an integer with its signedness, which C's `#if` needs for
// the same reason its expressions do: `-1 < 0u` is false.
type ppValue struct {
	v        int64
	unsigned bool
}

func (a ppValue) truthy() bool { return a.v != 0 }

func (pp *preprocessor) conditionValue(kind string, directive token, rest []token) (bool, error) {
	switch kind {
	case "ifdef", "ifndef":
		if len(rest) == 0 || (rest[0].kind != tokIdent && rest[0].kind != tokKeyword) {
			return false, errorAt(directive.pos, "#%s needs a name", kind)
		}
		defined := pp.macros[rest[0].text] != nil
		return defined == (kind == "ifdef"), nil
	}

	if len(rest) == 0 {
		return false, errorAt(directive.pos, "#if needs an expression")
	}

	// `defined X` is answered before expansion, because expanding it
	// first would replace the very name it is asking about.
	resolved, err := pp.resolveDefined(directive, rest)
	if err != nil {
		return false, err
	}
	expanded, err := pp.expandAll(resolved)
	if err != nil {
		return false, err
	}
	// A `defined` that a macro produced is still an operator, which is
	// undefined behaviour in C and which every compiler answers anyway.
	expanded, err = pp.resolveDefined(directive, expanded)
	if err != nil {
		return false, err
	}

	e := &ppExpr{toks: expanded, at: directive.pos, live: true}
	value, err := e.ternary()
	if err != nil {
		return false, err
	}
	if e.i < len(e.toks) {
		return false, errorAt(e.toks[e.i].pos, "this #if expression has %s left over", describe(e.toks[e.i]))
	}
	return value.truthy(), nil
}

func (pp *preprocessor) resolveDefined(directive token, toks []token) ([]token, error) {
	var out []token
	for i := 0; i < len(toks); i++ {
		if toks[i].kind != tokIdent || toks[i].text != "defined" {
			out = append(out, toks[i])
			continue
		}

		i++
		wrapped := i < len(toks) && toks[i].is("(")
		if wrapped {
			i++
		}
		if i >= len(toks) || (toks[i].kind != tokIdent && toks[i].kind != tokKeyword) {
			return nil, errorAt(directive.pos, "defined needs a name")
		}
		answer := "0"
		if pp.macros[toks[i].text] != nil {
			answer = "1"
		}
		out = append(out, token{kind: tokInt, text: answer, ival: uint64(answer[0] - '0'), pos: toks[i].pos})

		if wrapped {
			i++
			if i >= len(toks) || !toks[i].is(")") {
				return nil, errorAt(directive.pos, "defined( needs a closing ')'")
			}
		}
	}
	return out, nil
}

type ppExpr struct {
	toks []token
	i    int
	at   position
	// live says whether the sub-expression being read decides anything.
	// `#if defined(N) && 100 / N > 3` is written on the understanding
	// that the right side is not evaluated when the left is false, so a
	// dead operand's division by zero has to be read and dropped rather
	// than reported.
	live bool
}

// dead evaluates fn with live cleared and restores it after.
func (e *ppExpr) dead(when bool, fn func() (ppValue, error)) (ppValue, error) {
	saved := e.live
	e.live = e.live && !when
	value, err := fn()
	e.live = saved
	return value, err
}

func (e *ppExpr) peek() (token, bool) {
	if e.i >= len(e.toks) {
		return token{}, false
	}
	return e.toks[e.i], true
}

func (e *ppExpr) accept(text string) bool {
	if tok, ok := e.peek(); ok && tok.is(text) {
		e.i++
		return true
	}
	return false
}

func (e *ppExpr) where() position {
	if tok, ok := e.peek(); ok {
		return tok.pos
	}
	return e.at
}

func (e *ppExpr) ternary() (ppValue, error) {
	cond, err := e.binary(1)
	if err != nil {
		return ppValue{}, err
	}
	if !e.accept("?") {
		return cond, nil
	}
	then, err := e.dead(!cond.truthy(), e.ternary)
	if err != nil {
		return ppValue{}, err
	}
	if !e.accept(":") {
		return ppValue{}, errorAt(e.where(), "this #if has a '?' without a ':'")
	}
	els, err := e.dead(cond.truthy(), e.ternary)
	if err != nil {
		return ppValue{}, err
	}
	if cond.truthy() {
		return then, nil
	}
	return els, nil
}

func (e *ppExpr) binary(minPrec int) (ppValue, error) {
	left, err := e.unary()
	if err != nil {
		return ppValue{}, err
	}
	for {
		tok, ok := e.peek()
		if !ok || tok.kind != tokPunct {
			return left, nil
		}
		prec, known := binaryPrec[tok.text]
		if !known || prec < minPrec {
			return left, nil
		}
		e.i++

		skip := (tok.text == "&&" && !left.truthy()) || (tok.text == "||" && left.truthy())
		right, err := e.dead(skip, func() (ppValue, error) { return e.binary(prec + 1) })
		if err != nil {
			return ppValue{}, err
		}
		left, err = ppApply(tok, left, right, e.live)
		if err != nil {
			return ppValue{}, err
		}
	}
}

func (e *ppExpr) unary() (ppValue, error) {
	tok, ok := e.peek()
	if !ok {
		return ppValue{}, errorAt(e.at, "this #if expression ends early")
	}

	switch {
	case e.accept("+"):
		return e.unary()
	case e.accept("-"):
		v, err := e.unary()
		return ppValue{v: -v.v, unsigned: v.unsigned}, err
	case e.accept("!"):
		v, err := e.unary()
		return ppValue{v: boolValue(!v.truthy())}, err
	case e.accept("~"):
		v, err := e.unary()
		return ppValue{v: ^v.v, unsigned: v.unsigned}, err
	case e.accept("("):
		v, err := e.ternary()
		if err != nil {
			return ppValue{}, err
		}
		if !e.accept(")") {
			return ppValue{}, errorAt(e.where(), "this #if has a '(' without a ')'")
		}
		return v, nil
	}

	switch tok.kind {
	case tokInt, tokChar:
		e.i++
		return ppValue{v: int64(tok.ival), unsigned: tok.unsigned}, nil
	case tokIdent, tokKeyword:
		// Every name that survived expansion is zero. That is C's rule
		// and not a fallback: it is what makes `#if FEATURE` work on a
		// build where nobody defined FEATURE.
		e.i++
		return ppValue{}, nil
	case tokFloat:
		return ppValue{}, errorAt(tok.pos, "a #if expression is integers only, and %s is not one", tok.text)
	case tokStr:
		return ppValue{}, errorAt(tok.pos, "a #if expression is integers only, and a string is not one")
	}
	return ppValue{}, errorAt(tok.pos, "expected a #if operand, found %s", describe(tok))
}

func ppApply(op token, a, b ppValue, live bool) (ppValue, error) {
	// One unsigned operand makes the whole operation unsigned, which is
	// the usual arithmetic conversions at the only width `#if` has.
	unsigned := a.unsigned || b.unsigned
	x, y := uint64(a.v), uint64(b.v)

	num := func(v int64) ppValue { return ppValue{v: v, unsigned: unsigned} }
	flag := func(b bool) ppValue { return ppValue{v: boolValue(b)} }

	switch op.text {
	case "||":
		return flag(a.truthy() || b.truthy()), nil
	case "&&":
		return flag(a.truthy() && b.truthy()), nil
	case "|":
		return num(a.v | b.v), nil
	case "^":
		return num(a.v ^ b.v), nil
	case "&":
		return num(a.v & b.v), nil
	case "==":
		return flag(a.v == b.v), nil
	case "!=":
		return flag(a.v != b.v), nil
	case "<":
		if unsigned {
			return flag(x < y), nil
		}
		return flag(a.v < b.v), nil
	case "<=":
		if unsigned {
			return flag(x <= y), nil
		}
		return flag(a.v <= b.v), nil
	case ">":
		if unsigned {
			return flag(x > y), nil
		}
		return flag(a.v > b.v), nil
	case ">=":
		if unsigned {
			return flag(x >= y), nil
		}
		return flag(a.v >= b.v), nil
	case "<<":
		return num(a.v << uint64(b.v&63)), nil
	case ">>":
		if unsigned {
			return num(int64(x >> uint64(b.v&63))), nil
		}
		return num(a.v >> uint64(b.v&63)), nil
	case "+":
		return num(a.v + b.v), nil
	case "-":
		return num(a.v - b.v), nil
	case "*":
		return num(a.v * b.v), nil
	case "/", "%":
		if b.v == 0 {
			if !live {
				return num(0), nil
			}
			return ppValue{}, errorAt(op.pos, "this #if divides by zero")
		}
		if unsigned {
			if op.text == "/" {
				return num(int64(x / y)), nil
			}
			return num(int64(x % y)), nil
		}
		if op.text == "/" {
			return num(a.v / b.v), nil
		}
		return num(a.v % b.v), nil
	}
	return ppValue{}, errorAt(op.pos, "%s is not an operator a #if expression has", op.text)
}

func boolValue(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
