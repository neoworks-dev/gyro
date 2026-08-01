package metals

import (
	"fmt"
	"strconv"
	"unicode"
)

// Parse reads the canonical text form back into a module. It reports the
// first syntax error it hits; verification is a separate, deliberate step.
func Parse(source string) (*Module, error) {
	p := &parser{scan: newScanner(source)}
	module, err := p.module()
	if err != nil {
		return nil, fmt.Errorf("metals: line %d: %w", p.scan.line, err)
	}
	return module, nil
}

// ---------------------------------------------------------------------------
// Scanner
// ---------------------------------------------------------------------------

type tokKind int

const (
	tEOF tokKind = iota
	tNewline
	tWord   // module, fn, iadd, const.i32, array, ...
	tReg    // %name — registers and named types share the sigil
	tGlobal // @name
	tLabel  // $name
	tString // "..."
	tNumber // 42, -1, 1.5, 0xff
	tChar   // 'a'
	tPunct  // = , ( ) { } < > :
	tArrow  // ->
)

type token struct {
	kind tokKind
	text string
}

type scanner struct {
	src  string
	pos  int
	line int
	// pending is the one-token lookahead.
	pending *token
}

func newScanner(src string) *scanner {
	return &scanner{src: src, line: 1}
}

func (s *scanner) peek() token {
	if s.pending == nil {
		tok := s.scan()
		s.pending = &tok
	}
	return *s.pending
}

func (s *scanner) next() token {
	tok := s.peek()
	s.pending = nil
	return tok
}

func (s *scanner) scan() token {
	// Skip spaces and comments; newlines are tokens.
	for s.pos < len(s.src) {
		ch := s.src[s.pos]
		if ch == ' ' || ch == '\t' || ch == '\r' {
			s.pos++
			continue
		}
		if ch == ';' {
			for s.pos < len(s.src) && s.src[s.pos] != '\n' {
				s.pos++
			}
			continue
		}
		break
	}
	if s.pos >= len(s.src) {
		return token{kind: tEOF}
	}

	ch := s.src[s.pos]
	switch {
	case ch == '\n':
		s.pos++
		s.line++
		return token{kind: tNewline}
	case ch == '%' || ch == '@' || ch == '$':
		s.pos++
		kinds := map[byte]tokKind{'%': tReg, '@': tGlobal, '$': tLabel}
		// A quoted name carries characters a bare word cannot:
		// @"Pair<i32>::sum".
		if s.pos < len(s.src) && s.src[s.pos] == '"' {
			str := s.scanString()
			return token{kind: kinds[ch], text: str.text}
		}
		return token{kind: kinds[ch], text: s.word()}
	case ch == '"':
		return s.scanString()
	case ch == '\'':
		return s.scanChar()
	case ch == '-':
		if s.pos+1 < len(s.src) && s.src[s.pos+1] == '>' {
			s.pos += 2
			return token{kind: tArrow, text: "->"}
		}
		s.pos++
		return token{kind: tNumber, text: "-" + s.word()}
	case ch >= '0' && ch <= '9':
		return token{kind: tNumber, text: s.word()}
	case isWordStart(rune(ch)):
		return token{kind: tWord, text: s.word()}
	}
	s.pos++
	return token{kind: tPunct, text: string(ch)}
}

func isWordStart(ch rune) bool {
	return unicode.IsLetter(ch) || ch == '_'
}

// word consumes identifier-ish text; dots stay inside so mnemonics like
// const.i32 and field.ptr are single tokens, and so are number bodies.
func (s *scanner) word() string {
	start := s.pos
	for s.pos < len(s.src) {
		ch := rune(s.src[s.pos])
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' || ch == '.' {
			s.pos++
			continue
		}
		break
	}
	return s.src[start:s.pos]
}

func (s *scanner) scanString() token {
	start := s.pos
	s.pos++ // opening quote
	for s.pos < len(s.src) && s.src[s.pos] != '"' {
		if s.src[s.pos] == '\\' {
			s.pos++
		}
		s.pos++
	}
	s.pos++ // closing quote
	text, err := strconv.Unquote(s.src[start:s.pos])
	if err != nil {
		text = ""
	}
	return token{kind: tString, text: text}
}

func (s *scanner) scanChar() token {
	start := s.pos
	s.pos++
	for s.pos < len(s.src) && s.src[s.pos] != '\'' {
		if s.src[s.pos] == '\\' {
			s.pos++
		}
		s.pos++
	}
	s.pos++
	return token{kind: tChar, text: s.src[start:s.pos]}
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

type parser struct {
	scan *scanner
}

func (p *parser) errf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

func (p *parser) skipNewlines() {
	for p.scan.peek().kind == tNewline {
		p.scan.next()
	}
}

func (p *parser) endOfLine() error {
	tok := p.scan.next()
	if tok.kind != tNewline && tok.kind != tEOF {
		return p.errf("expected end of line, found %q", tok.text)
	}
	return nil
}

func (p *parser) word(want string) error {
	tok := p.scan.next()
	if tok.kind != tWord || tok.text != want {
		return p.errf("expected %q, found %q", want, tok.text)
	}
	return nil
}

func (p *parser) punct(want string) error {
	tok := p.scan.next()
	if tok.kind != tPunct || tok.text != want {
		return p.errf("expected %q, found %q", want, tok.text)
	}
	return nil
}

func (p *parser) atPunct(want string) bool {
	tok := p.scan.peek()
	return tok.kind == tPunct && tok.text == want
}

func (p *parser) reg() (string, error) {
	tok := p.scan.next()
	if tok.kind != tReg {
		return "", p.errf("expected a %%register, found %q", tok.text)
	}
	return tok.text, nil
}

func (p *parser) global() (string, error) {
	tok := p.scan.next()
	if tok.kind != tGlobal {
		return "", p.errf("expected an @name, found %q", tok.text)
	}
	return tok.text, nil
}

func (p *parser) label() (string, error) {
	tok := p.scan.next()
	if tok.kind != tLabel {
		return "", p.errf("expected a $label, found %q", tok.text)
	}
	return tok.text, nil
}

func (p *parser) intLit() (int, error) {
	tok := p.scan.next()
	if tok.kind != tNumber {
		return 0, p.errf("expected a number, found %q", tok.text)
	}
	value, err := strconv.Atoi(tok.text)
	if err != nil {
		return 0, p.errf("bad number %q", tok.text)
	}
	return value, nil
}

func (p *parser) module() (*Module, error) {
	p.skipNewlines()
	if err := p.word("module"); err != nil {
		return nil, err
	}
	name := p.scan.next()
	if name.kind != tString {
		return nil, p.errf("module wants its quoted name")
	}
	if err := p.endOfLine(); err != nil {
		return nil, err
	}

	m := &Module{Name: name.text}
	for {
		p.skipNewlines()
		tok := p.scan.peek()
		if tok.kind == tEOF {
			return m, nil
		}
		if tok.kind != tWord {
			return nil, p.errf("expected a declaration, found %q", tok.text)
		}

		var err error
		switch tok.text {
		case "import":
			err = p.importDecl(m)
		case "type":
			err = p.typeDecl(m)
		case "global":
			err = p.globalDecl(m)
		case "export", "async", "fn":
			err = p.funcDecl(m)
		default:
			err = p.errf("unknown declaration %q", tok.text)
		}
		if err != nil {
			return nil, err
		}
	}
}

func (p *parser) importDecl(m *Module) error {
	p.scan.next() // import
	name := p.scan.next()
	if name.kind != tString {
		return p.errf("import wants its quoted module name")
	}
	if err := p.punct("{"); err != nil {
		return err
	}
	if err := p.endOfLine(); err != nil {
		return err
	}

	imp := &Import{Module: name.text}
	for {
		p.skipNewlines()
		if p.atPunct("}") {
			p.scan.next()
			m.Imports = append(m.Imports, imp)
			return p.endOfLine()
		}

		async := false
		if p.scan.peek().text == "async" {
			p.scan.next()
			async = true
		}
		if err := p.word("fn"); err != nil {
			return err
		}
		fnName, err := p.global()
		if err != nil {
			return err
		}
		sig, err := p.signature(async)
		if err != nil {
			return err
		}
		imp.Funcs = append(imp.Funcs, ImportedFunc{Name: fnName, Sig: sig})
		if err := p.endOfLine(); err != nil {
			return err
		}
	}
}

// signature parses "(T, T) -> R" with unnamed parameters.
func (p *parser) signature(async bool) (*FnType, error) {
	if err := p.punct("("); err != nil {
		return nil, err
	}
	sig := &FnType{Async: async}
	for !p.atPunct(")") {
		if len(sig.Params) > 0 {
			if err := p.punct(","); err != nil {
				return nil, err
			}
		}
		typ, err := p.typeExpr()
		if err != nil {
			return nil, err
		}
		sig.Params = append(sig.Params, typ)
	}
	p.scan.next() // )

	if p.scan.peek().kind == tArrow {
		p.scan.next()
		result, err := p.typeExpr()
		if err != nil {
			return nil, err
		}
		sig.Result = result
	}
	return sig, nil
}

func (p *parser) typeDecl(m *Module) error {
	p.scan.next() // type
	name, err := p.reg()
	if err != nil {
		return err
	}
	if err := p.punct("="); err != nil {
		return err
	}

	kind := p.scan.next()
	decl := &TypeDecl{Name: name}
	switch kind.text {
	case "record":
		if err := p.recordShape(decl); err != nil {
			return err
		}
	case "enum":
		if err := p.enumShape(decl); err != nil {
			return err
		}
	default:
		return p.errf("a type is a record or an enum, not %q", kind.text)
	}
	m.Types = append(m.Types, decl)
	return p.endOfLine()
}

func (p *parser) recordShape(decl *TypeDecl) error {
	if err := p.punct("{"); err != nil {
		return err
	}
	shape := &RecordShape{}
	for !p.atPunct("}") {
		if len(shape.Fields) > 0 {
			if err := p.punct(","); err != nil {
				return err
			}
		}
		name := p.scan.next()
		if name.kind != tWord {
			return p.errf("expected a field name, found %q", name.text)
		}
		if err := p.punct(":"); err != nil {
			return err
		}
		typ, err := p.typeExpr()
		if err != nil {
			return err
		}
		shape.Fields = append(shape.Fields, FieldDecl{Name: name.text, Type: typ})
	}
	p.scan.next() // }
	decl.Record = shape
	return nil
}

func (p *parser) enumShape(decl *TypeDecl) error {
	if err := p.punct("{"); err != nil {
		return err
	}
	shape := &EnumShape{}
	for !p.atPunct("}") {
		if len(shape.Variants) > 0 {
			if err := p.punct(","); err != nil {
				return err
			}
		}
		name := p.scan.next()
		if name.kind != tWord {
			return p.errf("expected a variant name, found %q", name.text)
		}
		variant := VariantDecl{Name: name.text}
		if p.atPunct("(") {
			p.scan.next()
			for !p.atPunct(")") {
				if len(variant.Payload) > 0 {
					if err := p.punct(","); err != nil {
						return err
					}
				}
				typ, err := p.typeExpr()
				if err != nil {
					return err
				}
				variant.Payload = append(variant.Payload, typ)
			}
			p.scan.next() // )
		}
		shape.Variants = append(shape.Variants, variant)
	}
	p.scan.next() // }
	decl.Enum = shape
	return nil
}

func (p *parser) globalDecl(m *Module) error {
	p.scan.next() // global
	name, err := p.global()
	if err != nil {
		return err
	}
	if err := p.punct(":"); err != nil {
		return err
	}
	typ, err := p.typeExpr()
	if err != nil {
		return err
	}
	if err := p.punct("="); err != nil {
		return err
	}
	value := p.scan.next()
	if value.kind != tNumber && value.kind != tString && value.kind != tChar && value.kind != tWord {
		return p.errf("a global takes a literal value")
	}
	m.Globals = append(m.Globals, &Global{Name: name, Type: typ, Value: value.text})
	return p.endOfLine()
}

func (p *parser) funcDecl(m *Module) error {
	fn := &Func{}
	for {
		tok := p.scan.peek()
		if tok.text == "export" {
			p.scan.next()
			fn.Export = true
			continue
		}
		if tok.text == "async" {
			p.scan.next()
			fn.Async = true
			continue
		}
		break
	}
	if err := p.word("fn"); err != nil {
		return err
	}

	name, err := p.global()
	if err != nil {
		return err
	}
	fn.Name = name

	if err := p.punct("("); err != nil {
		return err
	}
	for !p.atPunct(")") {
		if len(fn.Params) > 0 {
			if err := p.punct(","); err != nil {
				return err
			}
		}
		paramName, err := p.reg()
		if err != nil {
			return err
		}
		if err := p.punct(":"); err != nil {
			return err
		}
		typ, err := p.typeExpr()
		if err != nil {
			return err
		}
		fn.Params = append(fn.Params, Param{Name: paramName, Type: typ})
	}
	p.scan.next() // )

	if p.scan.peek().kind == tArrow {
		p.scan.next()
		result, err := p.typeExpr()
		if err != nil {
			return err
		}
		fn.Result = result
	}

	if err := p.punct("{"); err != nil {
		return err
	}
	if err := p.endOfLine(); err != nil {
		return err
	}
	body, err := p.instrBlock()
	if err != nil {
		return err
	}
	fn.Body = body
	m.Funcs = append(m.Funcs, fn)
	return p.endOfLine()
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

func (p *parser) typeExpr() (Type, error) {
	tok := p.scan.next()
	if tok.kind == tReg {
		return &Named{Name: tok.text}, nil
	}
	if tok.kind != tWord {
		return nil, p.errf("expected a type, found %q", tok.text)
	}

	if prim, ok := Prims[tok.text]; ok {
		return prim, nil
	}

	switch tok.text {
	case "dyn":
		return &Dyn{}, nil
	case "handle":
		return &Handle{}, nil
	case "ptr":
		elem, err := p.angled()
		return &Ptr{Elem: elem}, err
	case "opt":
		elem, err := p.angled()
		return &Opt{Elem: elem}, err
	case "slice":
		elem, err := p.angled()
		return &SliceOf{Elem: elem}, err
	case "chan":
		elem, err := p.angled()
		return &ChanOf{Elem: elem}, err
	case "future":
		elem, err := p.angled()
		return &FutureOf{Result: elem}, err
	case "array":
		if err := p.punct("<"); err != nil {
			return nil, err
		}
		elem, err := p.typeExpr()
		if err != nil {
			return nil, err
		}
		if err := p.punct(","); err != nil {
			return nil, err
		}
		length, err := p.intLit()
		if err != nil {
			return nil, err
		}
		if err := p.punct(">"); err != nil {
			return nil, err
		}
		return &ArrayOf{Elem: elem, Len: int64(length)}, nil
	case "tuple":
		if err := p.punct("<"); err != nil {
			return nil, err
		}
		tuple := &TupleOf{}
		for !p.atPunct(">") {
			if len(tuple.Elems) > 0 {
				if err := p.punct(","); err != nil {
					return nil, err
				}
			}
			elem, err := p.typeExpr()
			if err != nil {
				return nil, err
			}
			tuple.Elems = append(tuple.Elems, elem)
		}
		p.scan.next() // >
		return tuple, nil
	case "fn":
		return p.signature(false)
	case "async":
		if err := p.word("fn"); err != nil {
			return nil, err
		}
		return p.signature(true)
	}
	return nil, p.errf("unknown type %q", tok.text)
}

func (p *parser) angled() (Type, error) {
	if err := p.punct("<"); err != nil {
		return nil, err
	}
	elem, err := p.typeExpr()
	if err != nil {
		return nil, err
	}
	if err := p.punct(">"); err != nil {
		return nil, err
	}
	return elem, nil
}
