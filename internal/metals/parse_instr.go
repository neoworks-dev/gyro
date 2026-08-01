package metals

import "strconv"

// instrBlock parses instructions until the closing brace, which it
// consumes; the caller owns the newline (or `else`) after it.
func (p *parser) instrBlock() ([]Instr, error) {
	var body []Instr
	for {
		p.skipNewlines()
		if p.atPunct("}") {
			p.scan.next()
			return body, nil
		}
		instr, err := p.instr()
		if err != nil {
			return nil, err
		}
		body = append(body, instr)
	}
}

// bracedBlock parses "{ NL instrs }" and the line end after the brace.
func (p *parser) bracedBlock() ([]Instr, error) {
	if err := p.punct("{"); err != nil {
		return nil, err
	}
	if err := p.endOfLine(); err != nil {
		return nil, err
	}
	body, err := p.instrBlock()
	if err != nil {
		return nil, err
	}
	return body, p.endOfLine()
}

func (p *parser) instr() (Instr, error) {
	tok := p.scan.peek()
	if tok.kind == tReg {
		return p.dstInstr()
	}
	if tok.kind != tWord {
		return nil, p.errf("expected an instruction, found %q", tok.text)
	}
	return p.stmtInstr(tok.text)
}

// regList parses "(%a, %b)".
func (p *parser) regList() ([]string, error) {
	if err := p.punct("("); err != nil {
		return nil, err
	}
	var out []string
	for !p.atPunct(")") {
		if len(out) > 0 {
			if err := p.punct(","); err != nil {
				return nil, err
			}
		}
		name, err := p.reg()
		if err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	p.scan.next() // )
	return out, nil
}

// line finishes a simple instruction.
func (p *parser) line(instr Instr, err error) (Instr, error) {
	if err != nil {
		return nil, err
	}
	return instr, p.endOfLine()
}

// ---------------------------------------------------------------------------
// `%dst = op ...` forms
// ---------------------------------------------------------------------------

func (p *parser) dstInstr() (Instr, error) {
	dst, err := p.reg()
	if err != nil {
		return nil, err
	}
	if err := p.punct("="); err != nil {
		return nil, err
	}

	op := p.scan.next()
	if op.kind != tWord {
		return nil, p.errf("expected an opcode, found %q", op.text)
	}

	if elem, ok := cutPrefix(op.text, "const."); ok {
		return p.constInstr(dst, elem)
	}
	if _, ok := BinOps[op.text]; ok {
		a, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		b, err := p.reg()
		return p.line(&Bin{Dst: dst, Op: op.text, A: a, B: b}, err)
	}
	if _, ok := UnOps[op.text]; ok {
		x, err := p.reg()
		return p.line(&Un{Dst: dst, Op: op.text, X: x}, err)
	}

	switch op.text {
	case "copy", "load", "len", "slice.of", "variant.tag", "recv", "await",
		"opt.wrap", "opt.unwrap", "dyn.wrap":
		x, err := p.reg()
		if err != nil {
			return nil, err
		}
		single := map[string]Instr{
			"copy":        &Copy{Dst: dst, X: x},
			"load":        &Load{Dst: dst, Ptr: x},
			"len":         &Len{Dst: dst, X: x},
			"slice.of":    &Slice{Dst: dst, X: x},
			"variant.tag": &VariantTag{Dst: dst, X: x},
			"recv":        &Recv{Dst: dst, Ch: x},
			"await":       &Await{Dst: dst, X: x},
			"opt.wrap":    &OptWrap{Dst: dst, X: x},
			"opt.unwrap":  &OptUnwrap{Dst: dst, X: x},
			"dyn.wrap":    &DynWrap{Dst: dst, X: x},
		}
		return p.line(single[op.text], nil)

	case "conv", "dyn.unwrap":
		x, err := p.reg()
		if err != nil {
			return nil, err
		}
		if tok := p.scan.next(); tok.kind != tArrow {
			return nil, p.errf("expected ->, found %q", tok.text)
		}
		to, err := p.typeExpr()
		if err != nil {
			return nil, err
		}
		if op.text == "conv" {
			return p.line(&Conv{Dst: dst, X: x, To: to}, nil)
		}
		return p.line(&DynUnwrap{Dst: dst, X: x, To: to}, nil)

	case "slot", "new":
		typ, err := p.typeExpr()
		if err != nil {
			return nil, err
		}
		if op.text == "slot" {
			return p.line(&Slot{Dst: dst, Type: typ}, nil)
		}
		return p.line(&New{Dst: dst, Type: typ}, nil)

	case "field.ptr", "field.get", "tuple.get":
		x, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		index, err := p.intLit()
		if err != nil {
			return nil, err
		}
		byOp := map[string]Instr{
			"field.ptr": &FieldPtr{Dst: dst, Ptr: x, Index: index},
			"field.get": &FieldGet{Dst: dst, X: x, Index: index},
			"tuple.get": &TupleGet{Dst: dst, X: x, Index: index},
		}
		return p.line(byOp[op.text], nil)

	case "record":
		name, err := p.reg()
		if err != nil {
			return nil, err
		}
		args, err := p.regList()
		return p.line(&MakeRecord{Dst: dst, TypeName: name, Args: args}, err)

	case "variant":
		name, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		variant, err := p.intLit()
		if err != nil {
			return nil, err
		}
		made := &MakeVariant{Dst: dst, TypeName: name, Variant: variant}
		if p.atPunct("(") {
			made.Args, err = p.regList()
			if err != nil {
				return nil, err
			}
		}
		return p.line(made, nil)

	case "variant.field":
		x, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		variant, err := p.intLit()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		index, err := p.intLit()
		return p.line(&VariantField{Dst: dst, X: x, Variant: variant, Index: index}, err)

	case "tuple":
		args, err := p.regList()
		return p.line(&MakeTuple{Dst: dst, Args: args}, err)

	case "array":
		elem, err := p.angled()
		if err != nil {
			return nil, err
		}
		args, err := p.regList()
		return p.line(&MakeArray{Dst: dst, Elem: elem, Args: args}, err)

	case "index.get":
		x, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		index, err := p.reg()
		return p.line(&IndexGet{Dst: dst, X: x, Index: index}, err)

	case "str.concat":
		a, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		b, err := p.reg()
		return p.line(&StrConcat{Dst: dst, A: a, B: b}, err)

	case "call":
		callee, err := p.global()
		if err != nil {
			return nil, err
		}
		args, err := p.regList()
		return p.line(&Call{Dst: dst, Callee: callee, Args: args}, err)

	case "call.indirect":
		fn, err := p.reg()
		if err != nil {
			return nil, err
		}
		args, err := p.regList()
		return p.line(&CallIndirect{Dst: dst, Fn: fn, Args: args}, err)

	case "fn.ref":
		callee, err := p.global()
		return p.line(&FnRef{Dst: dst, Callee: callee}, err)

	case "closure":
		callee, err := p.global()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		env, err := p.reg()
		return p.line(&MakeClosure{Dst: dst, Callee: callee, Env: env}, err)

	case "chan.new":
		elem, err := p.angled()
		if err != nil {
			return nil, err
		}
		made := &ChanNew{Dst: dst, Elem: elem}
		if p.scan.peek().kind == tReg {
			made.Cap, _ = p.reg()
		}
		return p.line(made, nil)

	case "dyn.call":
		x, err := p.reg()
		if err != nil {
			return nil, err
		}
		args, err := p.regList()
		return p.line(&DynCall{Dst: dst, X: x, Args: args}, err)

	case "dyn.get":
		x, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		name := p.scan.next()
		if name.kind != tString {
			return nil, p.errf("dyn.get wants a quoted name")
		}
		return p.line(&DynGet{Dst: dst, X: x, Name: name.text}, nil)
	}
	return nil, p.errf("unknown opcode %q", op.text)
}

func (p *parser) constInstr(dst, suffix string) (Instr, error) {
	if suffix == "null" {
		typ, err := p.typeExpr()
		if err != nil {
			return nil, err
		}
		return p.line(&Const{Dst: dst, Type: typ, Null: true}, nil)
	}
	prim, ok := Prims[suffix]
	if !ok {
		return nil, p.errf("const.%s is not a primitive constant", suffix)
	}
	value := p.scan.next()
	switch value.kind {
	case tNumber, tChar, tWord: // words cover true/false
		return p.line(&Const{Dst: dst, Type: prim, Value: value.text}, nil)
	case tString:
		// A str constant's Value keeps its quotes, so it prints back as
		// written.
		return p.line(&Const{Dst: dst, Type: prim, Value: strconv.Quote(value.text)}, nil)
	}
	return nil, p.errf("bad constant %q", value.text)
}

// ---------------------------------------------------------------------------
// Statement forms
// ---------------------------------------------------------------------------

func (p *parser) stmtInstr(op string) (Instr, error) {
	p.scan.next() // the opcode word

	switch op {
	case "free", "chan.close":
		x, err := p.reg()
		if err != nil {
			return nil, err
		}
		if op == "free" {
			return p.line(&Free{X: x}, nil)
		}
		return p.line(&ChanClose{Ch: x}, nil)

	case "store", "send":
		a, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		b, err := p.reg()
		if err != nil {
			return nil, err
		}
		if op == "store" {
			return p.line(&Store{Ptr: a, Val: b}, nil)
		}
		return p.line(&Send{Ch: a, Val: b}, nil)

	case "field.set":
		x, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		index, err := p.intLit()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		val, err := p.reg()
		return p.line(&FieldSet{X: x, Index: index, Val: val}, err)

	case "index.set":
		x, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		index, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		val, err := p.reg()
		return p.line(&IndexSet{X: x, Index: index, Val: val}, err)

	case "call":
		callee, err := p.global()
		if err != nil {
			return nil, err
		}
		args, err := p.regList()
		return p.line(&Call{Callee: callee, Args: args}, err)

	case "call.indirect":
		fn, err := p.reg()
		if err != nil {
			return nil, err
		}
		args, err := p.regList()
		return p.line(&CallIndirect{Fn: fn, Args: args}, err)

	case "return":
		if p.scan.peek().kind == tReg {
			val, _ := p.reg()
			return p.line(&Return{Val: val}, nil)
		}
		return p.line(&Return{}, nil)

	case "abort":
		message := p.scan.next()
		if message.kind != tString {
			return nil, p.errf("abort wants its quoted message")
		}
		return p.line(&Abort{Message: message.text}, nil)

	case "br":
		label, err := p.label()
		return p.line(&Br{Label: label}, err)

	case "br_if":
		cond, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		label, err := p.label()
		return p.line(&BrIf{Cond: cond, Label: label}, err)

	case "dyn.set":
		x, err := p.reg()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		name := p.scan.next()
		if name.kind != tString {
			return nil, p.errf("dyn.set wants a quoted name")
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		val, err := p.reg()
		return p.line(&DynSet{X: x, Name: name.text, Val: val}, err)

	case "if":
		return p.ifInstr()
	case "block", "loop":
		label, err := p.label()
		if err != nil {
			return nil, err
		}
		body, err := p.bracedBlock()
		if err != nil {
			return nil, err
		}
		if op == "block" {
			return &Block{Label: label, Body: body}, nil
		}
		return &Loop{Label: label, Body: body}, nil
	case "switch":
		return p.switchInstr()
	case "select":
		return p.selectInstr()
	}
	return nil, p.errf("unknown instruction %q", op)
}

func (p *parser) ifInstr() (Instr, error) {
	cond, err := p.reg()
	if err != nil {
		return nil, err
	}
	if err := p.punct("{"); err != nil {
		return nil, err
	}
	if err := p.endOfLine(); err != nil {
		return nil, err
	}
	then, err := p.instrBlock()
	if err != nil {
		return nil, err
	}

	out := &If{Cond: cond, Then: then}
	if p.scan.peek().text == "else" {
		p.scan.next()
		out.Else, err = p.bracedBlock()
		return out, err
	}
	return out, p.endOfLine()
}

func (p *parser) switchInstr() (Instr, error) {
	x, err := p.reg()
	if err != nil {
		return nil, err
	}
	if err := p.punct("{"); err != nil {
		return nil, err
	}
	if err := p.endOfLine(); err != nil {
		return nil, err
	}

	out := &Switch{X: x}
	for {
		p.skipNewlines()
		if p.atPunct("}") {
			p.scan.next()
			return out, p.endOfLine()
		}
		tok := p.scan.next()
		switch tok.text {
		case "case":
			value, err := p.intLit()
			if err != nil {
				return nil, err
			}
			body, err := p.bracedBlock()
			if err != nil {
				return nil, err
			}
			out.Cases = append(out.Cases, SwitchCase{Value: value, Body: body})
		case "default":
			out.Default, err = p.bracedBlock()
			if err != nil {
				return nil, err
			}
		default:
			return nil, p.errf("switch arms are case or default, not %q", tok.text)
		}
	}
}

func (p *parser) selectInstr() (Instr, error) {
	if err := p.punct("{"); err != nil {
		return nil, err
	}
	if err := p.endOfLine(); err != nil {
		return nil, err
	}

	out := &Select{}
	for {
		p.skipNewlines()
		if p.atPunct("}") {
			p.scan.next()
			return out, p.endOfLine()
		}
		tok := p.scan.next()
		switch tok.text {
		case "recv":
			dst, err := p.reg()
			if err != nil {
				return nil, err
			}
			if err := p.punct("="); err != nil {
				return nil, err
			}
			ch, err := p.reg()
			if err != nil {
				return nil, err
			}
			body, err := p.bracedBlock()
			if err != nil {
				return nil, err
			}
			out.Arms = append(out.Arms, SelectArm{Dst: dst, Ch: ch, Body: body})
		case "send":
			ch, err := p.reg()
			if err != nil {
				return nil, err
			}
			if err := p.punct(","); err != nil {
				return nil, err
			}
			val, err := p.reg()
			if err != nil {
				return nil, err
			}
			body, err := p.bracedBlock()
			if err != nil {
				return nil, err
			}
			out.Arms = append(out.Arms, SelectArm{Send: true, Ch: ch, Val: val, Body: body})
		case "default":
			var err error
			out.Default, err = p.bracedBlock()
			if err != nil {
				return nil, err
			}
		default:
			return nil, p.errf("select arms are recv, send or default, not %q", tok.text)
		}
	}
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}
