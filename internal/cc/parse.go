package cc

import (
	"fmt"
	"strings"
)

// Recursive descent over the token stream, chibicc-shaped: declaration
// specifiers and a declarator, then statements, then expressions by
// precedence climbing.
//
// Errors are raised with panic and caught in parse. A C parser reports
// one error and stops — there is no recovery to do, because a C program
// that does not parse has no separately compilable pieces the way a Gyro
// module's declarations do — and threading an error return through forty
// mutually recursive functions to say so costs more than it explains.

type parser struct {
	toks []token
	i    int
	// stem is the file's name without its directory or extension. A
	// tagless struct is named after it, so that two files each writing
	// `typedef struct { ... } T;` do not both claim the same record.
	stem string

	// structs maps a tag to its type, so that `struct Node *next` inside
	// `struct Node` refers to the one being defined rather than opening a
	// second. order keeps declaration order for the module.
	structs map[string]*ctype
	order   []*ctype

	// typedefs is what makes C's grammar context-sensitive: whether
	// `Foo * x;` declares a variable or multiplies two of them is a
	// question only this table answers.
	typedefs map[string]*ctype
	// enums maps an enumeration constant to its value, and enumTags maps
	// an enum's tag to the type its name spells. An enumeration is an
	// int here, which is what it is on this target.
	enums    map[string]int64
	enumTags map[string]bool
	// anonymous numbers the structs written without a tag, so that two
	// of them in one file get two METALS records.
	anonymous int
}

func parse(file string, toks []token) (out *unit, err error) {
	defer func() {
		if r := recover(); r != nil {
			caught, ok := r.(failure)
			if !ok {
				panic(r)
			}
			out, err = nil, caught.err
		}
	}()

	p := &parser{
		toks:     toks,
		stem:     unitName(file),
		structs:  map[string]*ctype{},
		typedefs: map[string]*ctype{},
		enums:    map[string]int64{},
		enumTags: map[string]bool{},
	}
	return p.unit(), nil
}

func (p *parser) fail(pos position, format string, args ...any) {
	fail(pos, format, args...)
}

func (p *parser) peek() token  { return p.toks[p.i] }
func (p *parser) ahead() token { return p.toks[min(p.i+1, len(p.toks)-1)] }

func (p *parser) next() token {
	tok := p.toks[p.i]
	if tok.kind != tokEOF {
		p.i++
	}
	return tok
}

func (p *parser) at(text string) bool { return p.peek().is(text) }

// atBuiltin matches one of the front end's own names. They are ordinary
// identifiers and not keywords, because `__builtin_` is reserved to the
// implementation and a program that spells one has asked for it — but
// nothing about a C program's own use of identifiers changes.
func (p *parser) atBuiltin(name string) bool {
	tok := p.peek()
	return tok.kind == tokIdent && tok.text == name
}

func (p *parser) accept(text string) bool {
	if p.at(text) {
		p.i++
		return true
	}
	return false
}

func (p *parser) expect(text string) token {
	if !p.at(text) {
		p.fail(p.peek().pos, "expected %q, found %s", text, describe(p.peek()))
	}
	return p.next()
}

func (p *parser) expectIdent() token {
	if p.peek().kind != tokIdent {
		p.fail(p.peek().pos, "expected a name, found %s", describe(p.peek()))
	}
	return p.next()
}

func describe(t token) string {
	switch t.kind {
	case tokEOF:
		return "the end of the file"
	case tokIdent:
		return "the name " + t.text
	case tokInt, tokFloat, tokChar:
		return "a literal"
	case tokStr:
		return "a string"
	}
	return "'" + t.text + "'"
}

// ---------------------------------------------------------------------------
// Declarations
// ---------------------------------------------------------------------------

func (p *parser) unit() *unit {
	out := &unit{}
	for p.peek().kind != tokEOF {
		pos := p.peek().pos
		base, static, isTypedef := p.declSpecifiers()

		// A declaration with no declarator declares a type and nothing
		// else: `struct Node;` opens a tag a later line will define, and
		// `struct Node { ... };` or `enum E { ... };` closes one.
		if p.accept(";") {
			if !base.isMembered() && !base.isInteger() {
				p.fail(pos, "this declaration declares nothing")
			}
			continue
		}

		// A definition is the one declarator that ends its declaration
		// without a semicolon, and C says so by putting a block where the
		// comma or the semicolon would have gone.
		defined := false
		for {
			name, typ := p.declarator(base)

			switch {
			case isTypedef:
				p.defineTypedef(name, typ, pos)

			case typ.isFunc():
				fn := &funcDecl{
					name: name, result: typ.result, params: typ.params,
					variadic: typ.variadic, static: static, pos: pos,
				}
				out.funcs = append(out.funcs, fn)
				if !p.at(";") && !p.at(",") {
					fn.body = p.block()
					defined = true
				}

			default:
				if typ.isVoid() {
					p.fail(pos, "%s is declared void", name)
				}
				global := &globalDecl{name: name, typ: typ, static: static, pos: pos}
				if p.accept("=") {
					global.init = p.initializer()
				}
				out.globals = append(out.globals, global)
			}

			if defined || !p.accept(",") {
				break
			}
		}
		if !defined {
			p.expect(";")
		}
	}
	out.structs = p.order
	out.enums = p.enums
	return out
}

// defineTypedef registers a type name. A struct written without a tag
// takes the typedef's name as its own, which is what makes the
// `typedef struct { ... } Point;` idiom produce a `%Point` record and
// not an anonymous one.
func (p *parser) defineTypedef(name string, typ *ctype, pos position) {
	if typ.isMembered() && strings.HasPrefix(typ.tag, p.stem+"$anon") {
		delete(p.structs, typ.tag)
		typ.tag = name
		if other, taken := p.structs[name]; taken && other != typ {
			p.fail(pos, "%s %s is defined twice", typ.keyword(), name)
		}
		p.structs[name] = typ
	}
	if existing, known := p.typedefs[name]; known && !existing.equals(typ) {
		p.fail(pos, "%s is typedef'd twice with different types", name)
	}
	p.typedefs[name] = typ
}

// declSpecifiers reads the type and storage class in front of a
// declarator. C lets them appear in any order and lets a type be spelled
// in several words, so they are collected and resolved together.
func (p *parser) declSpecifiers() (typ *ctype, static, isTypedef bool) {
	pos := p.peek().pos
	var words []string
	var named *ctype

	// only guards the three spellings that are a whole type by
	// themselves: a struct, an enum and a typedef name each stand alone
	// and none of them combines with `unsigned` or with each other.
	only := func(tok token, what string) {
		if named != nil || len(words) > 0 {
			p.fail(tok.pos, "%s does not combine with another type", what)
		}
	}

	for {
		tok := p.peek()

		if tok.kind == tokIdent {
			// A typedef name is a type only where a type has not been
			// spelled yet: in `unsigned x` the name is the declarator.
			alias, known := p.typedefs[tok.text]
			if !known || named != nil || len(words) > 0 {
				break
			}
			named = alias
			p.next()
			continue
		}
		if tok.kind != tokKeyword {
			break
		}

		switch tok.text {
		case "typedef":
			isTypedef = true
			p.next()
		case "static":
			static = true
			p.next()
		case "extern", "const", "volatile", "register", "auto":
			p.next()

		case "struct":
			only(tok, "struct")
			named = p.aggregateType(kStruct)
		case "union":
			only(tok, "union")
			named = p.aggregateType(kUnion)
		case "enum":
			only(tok, "enum")
			named = p.enumType()

		case "void", "char", "short", "int", "long", "signed", "unsigned",
			"float", "double", "_Bool":
			if named != nil {
				p.fail(tok.pos, "%s does not combine with another type", named)
			}
			words = append(words, tok.text)
			p.next()

		default:
			goto done
		}
	}
done:
	if named != nil {
		return named, static, isTypedef
	}
	if len(words) == 0 {
		p.fail(pos, "a declaration needs a type; this compiler has no implicit int")
	}
	typ = basicType(words)
	if typ == nil {
		p.fail(pos, "%s is not a type", join(words))
	}
	return typ, static, isTypedef
}

// enumType reads an enumeration. C's enumerations are integers with
// named constants, and on this target the integer is int — so the type
// this returns is int, and what the declaration leaves behind is the
// constants.
func (p *parser) enumType() *ctype {
	pos := p.expect("enum").pos
	if p.peek().kind == tokIdent {
		p.enumTags[p.next().text] = true
	} else if !p.at("{") {
		p.fail(pos, "an enum needs a tag or a body")
	}
	if !p.accept("{") {
		return typeInt
	}

	next := int64(0)
	for !p.at("}") {
		if p.peek().kind == tokEOF {
			p.fail(pos, "this enum is never closed")
		}
		name := p.expectIdent()
		if p.accept("=") {
			value, err := p.constantExpr()
			if err != nil {
				p.fail(name.pos, "%s", err)
			}
			next = value
		}
		if _, dup := p.enums[name.text]; dup {
			p.fail(name.pos, "%s is an enumeration constant twice", name.text)
		}
		p.enums[name.text] = next
		next++
		if !p.accept(",") {
			break
		}
	}
	p.expect("}")
	return typeInt
}

// basicType resolves a set of type words. The set is what matters and
// not the order, which is C's rule: `unsigned long int` and `long
// unsigned` are one type.
func basicType(words []string) *ctype {
	counts := map[string]int{}
	for _, w := range words {
		counts[w]++
	}
	longs := counts["long"]
	delete(counts, "long")

	unsigned := counts["unsigned"] > 0
	signed := counts["signed"] > 0
	if unsigned && signed {
		return nil
	}
	delete(counts, "unsigned")
	delete(counts, "signed")

	if len(counts) > 1 {
		return nil
	}
	base := ""
	for w, n := range counts {
		if n > 1 {
			return nil
		}
		base = w
	}

	switch base {
	case "void":
		if unsigned || signed || longs > 0 {
			return nil
		}
		return typeVoid
	case "_Bool":
		if unsigned || signed || longs > 0 {
			return nil
		}
		return typeBool
	case "float":
		if unsigned || signed || longs > 0 {
			return nil
		}
		return typeFloat
	case "double":
		if unsigned || signed {
			return nil
		}
		// `long double` is the x87 80-bit type, which METALS has no
		// primitive for and which no backend here would round the same
		// way twice. Refused rather than silently narrowed to f64.
		if longs > 0 {
			return nil
		}
		return typeDouble
	case "char":
		if longs > 0 {
			return nil
		}
		if unsigned {
			return typeUChar
		}
		return typeChar
	case "short":
		if longs > 0 {
			return nil
		}
		if unsigned {
			return typeUShort
		}
		return typeShort
	case "int", "":
		if base == "" && !unsigned && !signed && longs == 0 {
			return nil
		}
		switch {
		case longs > 2:
			return nil
		case longs > 0:
			if unsigned {
				return typeULong
			}
			return typeLong
		case unsigned:
			return typeUInt
		}
		return typeInt
	}
	return nil
}

func join(words []string) string {
	out := ""
	for i, w := range words {
		if i > 0 {
			out += " "
		}
		out += w
	}
	return out
}

// aggregateType reads `struct Tag` or `union Tag` and, when a body
// follows, fills it in. The tag is registered before the members are
// read so that either may hold a pointer to itself.
//
// The two are one function because C makes them one declaration with two
// layouts: the syntax is identical, the tag namespace is shared, and
// what differs is only where the members end up — which is the layout
// table's answer and not the parser's.
func (p *parser) aggregateType(kind typeKind) *ctype {
	keyword := "struct"
	if kind == kUnion {
		keyword = "union"
	}
	pos := p.expect(keyword).pos

	// One with no tag still needs a METALS name, so it gets one it
	// cannot collide with. `typedef struct { ... } Point;` renames it to
	// Point, which is the spelling that idiom exists to produce.
	tag := ""
	if p.peek().kind == tokIdent {
		tag = p.next().text
	} else {
		if !p.at("{") {
			p.fail(pos, "a %s needs a tag or a body", keyword)
		}
		p.anonymous++
		tag = fmt.Sprintf("%s$anon%d", p.stem, p.anonymous)
	}

	typ, known := p.structs[tag]
	if !known {
		typ = &ctype{kind: kind, tag: tag}
		p.structs[tag] = typ
		p.order = append(p.order, typ)
	}
	// C keeps one namespace for struct, union and enum tags, so this is
	// not two types that happen to share a name.
	if typ.kind != kind {
		p.fail(pos, "%s is a %s here and a %s elsewhere, and C has one tag namespace for both",
			tag, keyword, typ.keyword())
	}
	if !p.at("{") {
		return typ
	}
	if typ.fields != nil {
		p.fail(pos, "%s %s is defined twice", keyword, tag)
	}

	p.expect("{")
	fields := []field{}
	for !p.at("}") {
		if p.peek().kind == tokEOF {
			p.fail(pos, "this %s is never closed", keyword)
		}
		base, static, isTypedef := p.declSpecifiers()
		if static || isTypedef {
			p.fail(pos, "a %s member has no storage class", keyword)
		}
		for {
			name, fieldType := p.declarator(base)
			if fieldType.isFunc() {
				p.fail(pos, "member %s is a function; a %s holds a pointer to one, not the function itself", name, keyword)
			}
			if fieldType.isVoid() {
				p.fail(pos, "member %s is declared void", name)
			}
			// A member needs storage, so its type has to be complete
			// here — which is C's rule and also what rules out an
			// aggregate holding itself. A pointer to an incomplete type
			// is not affected: an address is a word whatever is at the
			// far end, which is what makes a forward declaration useful.
			if !fieldType.isComplete() {
				p.fail(pos, "member %s is %s, which is not complete here", name, fieldType)
			}
			fields = append(fields, field{name: name, typ: fieldType})
			if !p.accept(",") {
				break
			}
		}
		p.expect(";")
	}
	p.expect("}")
	if len(fields) == 0 {
		p.fail(pos, "%s %s has no members, and METALS has no shape with none", keyword, tag)
	}
	typ.fields = fields
	return typ
}

// declarator applies C's declarator syntax to a declaration's type words
// and answers what is declared and under what name. The name is empty
// for an abstract declarator, which is what a cast, a sizeof and an
// unnamed parameter are written with.
//
// C's rule is that a declarator reads like the expression that would use
// the thing it declares: `int (*fp)(int)` says that `(*fp)(int)` is an
// int, so `fp` is a pointer to a function returning int. That is why
// this is recursive, and why a parenthesised declarator has to be read
// twice — the suffix after the closing paren binds tighter than the `*`
// inside it, and when the inside is first reached that suffix has not
// been seen yet. So the inside is skipped, the suffix is applied, and
// then the inside is read again against the type that produced.
func (p *parser) declarator(base *ctype) (string, *ctype) {
	base = p.pointers(base)

	// A `(` here opens a parenthesised declarator unless what follows is
	// a type — `int (*)(int)` against `int (int)` — or nothing at all,
	// which is the unprototyped `()` the suffix refuses by name.
	if p.at("(") && !p.startsTypeAfterParen() && !p.ahead().is(")") {
		open := p.i
		p.skipBalanced()
		base = p.typeSuffix(base)
		after := p.i

		p.i = open + 1
		name, typ := p.declarator(base)
		p.expect(")")
		p.i = after
		return name, typ
	}

	name := ""
	if p.peek().kind == tokIdent {
		name = p.next().text
	}
	return name, p.typeSuffix(base)
}

// pointers reads the `*` run in front of a declarator's name.
func (p *parser) pointers(typ *ctype) *ctype {
	for p.accept("*") {
		typ = pointerTo(typ)
		// A qualifier binds to the pointer rather than the target here,
		// and means as little in either place.
		for p.at("const") || p.at("volatile") {
			p.next()
		}
	}
	return typ
}

// typeSuffix reads the `[N]` and `(params)` that follow a declarator's
// name, which are what make an array and a function.
func (p *parser) typeSuffix(base *ctype) *ctype {
	if p.at("[") {
		return p.arraySuffix(base)
	}
	if p.at("(") {
		params, variadic := p.params()
		return funcOf(base, params, variadic)
	}
	return base
}

// skipBalanced advances past a parenthesised run, the parens included.
// It is what lets a parenthesised declarator be read twice.
func (p *parser) skipBalanced() {
	depth := 0
	for {
		tok := p.next()
		switch {
		case tok.kind == tokEOF:
			p.fail(tok.pos, "this declarator is never closed")
		case tok.is("("):
			depth++
		case tok.is(")"):
			depth--
			if depth == 0 {
				return
			}
		}
	}
}

// arraySuffix reads the `[N]` that follows a declarator's name. C reads
// them left to right and applies them right to left: `int a[3][4]` is
// three arrays of four ints, so the last bracket is the innermost type.
func (p *parser) arraySuffix(base *ctype) *ctype {
	var lengths []int64
	for p.at("[") {
		pos := p.next().pos
		if p.accept("]") {
			// `int a[]` leaves the length to a parameter's decay or to an
			// initializer's element count.
			lengths = append(lengths, -1)
			continue
		}
		length, err := p.constantExpr()
		if err != nil {
			p.fail(pos, "an array's length is a constant: %s", err)
		}
		if length < 0 {
			p.fail(pos, "an array's length cannot be negative")
		}
		p.expect("]")
		lengths = append(lengths, length)
	}

	typ := base
	for i := len(lengths) - 1; i >= 0; i-- {
		if lengths[i] < 0 && i > 0 {
			p.fail(p.peek().pos, "only the outermost dimension of an array may be left empty")
		}
		typ = arrayOf(typ, lengths[i])
	}
	return typ
}

func (p *parser) params() ([]param, bool) {
	pos := p.expect("(").pos
	var out []param

	// `(void)` is C's empty parameter list. A bare `()` is the old
	// unprototyped spelling, which says nothing about the arguments and
	// so cannot become a METALS signature.
	if p.at("void") && p.ahead().is(")") {
		p.next()
		p.expect(")")
		return nil, false
	}
	if p.accept(")") {
		p.fail(pos, "an empty parameter list says nothing about the arguments; write (void)")
	}

	for {
		if p.at("...") {
			// C requires a named parameter before the `...`, and the
			// reason is `va_start`: the arguments after the last named
			// one are what a callee walks, and with no named one there
			// is nothing to start from.
			ellipsis := p.next().pos
			if len(out) == 0 {
				p.fail(ellipsis, "a `...` needs a named parameter before it; va_start has nothing to start from otherwise")
			}
			p.expect(")")
			return out, true
		}
		base, static, isTypedef := p.declSpecifiers()
		if static || isTypedef {
			p.fail(pos, "a parameter has no storage class")
		}
		name, typ := p.declarator(base)
		if typ.isVoid() {
			p.fail(pos, "parameter %s is declared void", name)
		}
		// An array parameter is a pointer, and so is a function
		// parameter. C says so of both, and the first is why
		// `void f(int a[10])` cannot check the ten.
		switch {
		case typ.isArray():
			typ = pointerTo(typ.elem)
		case typ.isFunc():
			typ = pointerTo(typ)
		}
		out = append(out, param{name: name, typ: typ})
		if !p.accept(",") {
			break
		}
	}
	p.expect(")")
	return out, false
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

func (p *parser) block() *blockStmt {
	pos := p.expect("{").pos
	out := &blockStmt{}
	for !p.at("}") {
		if p.peek().kind == tokEOF {
			p.fail(pos, "this block is never closed")
		}
		out.stmts = append(out.stmts, p.stmt())
	}
	p.expect("}")
	return out
}

// startsDecl reports whether a statement position opens a declaration.
// A type keyword answers it outright; a typedef name is the case that
// makes C's grammar need a symbol table to parse at all.
func (p *parser) startsDecl() bool {
	tok := p.peek()
	if tok.kind == tokIdent {
		_, known := p.typedefs[tok.text]
		return known
	}
	if tok.kind != tokKeyword {
		return false
	}
	switch tok.text {
	case "void", "char", "short", "int", "long", "signed", "unsigned",
		"float", "double", "_Bool", "struct", "union", "enum", "typedef",
		"static", "extern", "const", "volatile", "register", "auto":
		return true
	}
	return false
}

func (p *parser) stmt() stmt {
	tok := p.peek()

	switch {
	case p.at("{"):
		return p.block()

	case p.accept(";"):
		return &emptyStmt{}

	case p.at("if"):
		p.next()
		p.expect("(")
		cond := p.expr()
		p.expect(")")
		out := &ifStmt{cond: cond, then: p.stmt()}
		if p.accept("else") {
			out.els = p.stmt()
		}
		return out

	case p.at("while"):
		p.next()
		p.expect("(")
		cond := p.expr()
		p.expect(")")
		return &whileStmt{cond: cond, body: p.stmt()}

	case p.at("do"):
		p.next()
		body := p.stmt()
		p.expect("while")
		p.expect("(")
		cond := p.expr()
		p.expect(")")
		p.expect(";")
		return &doStmt{body: body, cond: cond}

	case p.at("for"):
		return p.forStmt()

	case p.at("switch"):
		return p.switchStmt()

	case p.at("case"):
		p.next()
		value, err := p.constantExpr()
		if err != nil {
			p.fail(tok.pos, "a case label is a constant expression: %v", err)
		}
		p.expect(":")
		return &caseStmt{value: value, pos: tok.pos}

	case p.at("default"):
		p.next()
		p.expect(":")
		return &defaultStmt{pos: tok.pos}

	case p.at("return"):
		p.next()
		out := &returnStmt{pos: tok.pos}
		if !p.at(";") {
			out.x = p.expr()
		}
		p.expect(";")
		return out

	case p.at("break"):
		p.next()
		p.expect(";")
		return &breakStmt{pos: tok.pos}

	case p.at("continue"):
		p.next()
		p.expect(";")
		return &continueStmt{pos: tok.pos}

	case p.startsDecl():
		out := p.decl()
		p.expect(";")
		return out
	}

	out := &exprStmt{x: p.expr()}
	p.expect(";")
	return out
}

// decl reads one local declaration without its semicolon, which is what
// lets a `for` clause share it.
func (p *parser) decl() stmt {
	pos := p.peek().pos
	base, static, isTypedef := p.declSpecifiers()

	out := &declStmt{}
	for {
		name, typ := p.declarator(base)
		if typ.isFunc() {
			p.fail(pos, "a function cannot be declared inside a function")
		}
		if isTypedef {
			// A typedef inside a block names a type for the rest of the
			// block. This parser keeps one table, so the name outlives
			// its scope — a shadowing typedef is rare enough to be worth
			// less than the table that would catch it.
			p.defineTypedef(name, typ, pos)
		} else {
			if typ.isVoid() {
				p.fail(pos, "%s is declared void", name)
			}
			v := &varDecl{name: name, typ: typ, static: static, pos: pos}
			if p.accept("=") {
				v.init = p.initializer()
			}
			out.vars = append(out.vars, v)
		}
		if !p.accept(",") {
			return out
		}
	}
}

// switchStmt reads `switch (e) { ... }`. The braces are required here,
// though C's grammar takes any statement: a switch whose body is not a
// block can hold no case label, so it is a switch that does nothing.
func (p *parser) switchStmt() stmt {
	pos := p.peek().pos
	p.expect("switch")
	p.expect("(")
	out := &switchStmt{x: p.expr(), pos: pos}
	p.expect(")")
	p.expect("{")
	for !p.at("}") {
		out.body = append(out.body, p.stmt())
	}
	p.expect("}")
	return out
}

// initializer reads what follows `=`: an assignment expression, or a
// braced list of them, which nests as deep as the object does. A
// trailing comma is allowed, because C allows it and every generated
// table has one.
func (p *parser) initializer() *initializer {
	pos := p.peek().pos
	if !p.accept("{") {
		return &initializer{x: p.assign(), pos: pos}
	}
	out := &initializer{list: []*initializer{}, pos: pos}
	for !p.at("}") {
		out.list = append(out.list, p.initializer())
		if !p.accept(",") {
			break
		}
	}
	p.expect("}")
	return out
}

func (p *parser) forStmt() stmt {
	p.expect("for")
	p.expect("(")
	out := &forStmt{}

	switch {
	case p.accept(";"):
	case p.startsDecl():
		out.init = p.decl()
		p.expect(";")
	default:
		out.init = &exprStmt{x: p.expr()}
		p.expect(";")
	}

	if !p.at(";") {
		out.cond = p.expr()
	}
	p.expect(";")

	if !p.at(")") {
		out.post = p.expr()
	}
	p.expect(")")

	out.body = p.stmt()
	return out
}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

var assignOps = map[string]bool{
	"=": true, "+=": true, "-=": true, "*=": true, "/=": true, "%=": true,
	"&=": true, "|=": true, "^=": true, "<<=": true, ">>=": true,
}

// binaryPrec is C's precedence for the binary operators, tightest last.
// The comma operator is absent: it sequences two expressions into one
// and nothing in this subset needs it.
var binaryPrec = map[string]int{
	"||": 1,
	"&&": 2,
	"|":  3,
	"^":  4,
	"&":  5,
	"==": 6, "!=": 6,
	"<": 7, ">": 7, "<=": 7, ">=": 7,
	"<<": 8, ">>": 8,
	"+": 9, "-": 9,
	"*": 10, "/": 10, "%": 10,
}

func (p *parser) expr() expr { return p.assign() }

func (p *parser) assign() expr {
	target := p.conditional()
	tok := p.peek()
	if tok.kind == tokPunct && assignOps[tok.text] {
		p.next()
		return &assignExpr{op: tok.text, target: target, value: p.assign(), pos: tok.pos}
	}
	return target
}

func (p *parser) conditional() expr {
	cond := p.binary(1)
	if !p.at("?") {
		return cond
	}
	pos := p.next().pos
	then := p.assign()
	p.expect(":")
	return &condExpr{cond: cond, then: then, els: p.conditional(), pos: pos}
}

func (p *parser) binary(minPrec int) expr {
	left := p.unary()
	for {
		tok := p.peek()
		if tok.kind != tokPunct {
			return left
		}
		prec, ok := binaryPrec[tok.text]
		if !ok || prec < minPrec {
			return left
		}
		p.next()
		// Every binary operator here is left-associative, so the right
		// side stops at anything this tight or looser.
		right := p.binary(prec + 1)
		left = &binaryExpr{op: tok.text, a: left, b: right, pos: tok.pos}
	}
}

func (p *parser) unary() expr {
	tok := p.peek()
	switch {
	case p.at("-"), p.at("+"), p.at("!"), p.at("~"), p.at("*"), p.at("&"):
		p.next()
		return &unaryExpr{op: tok.text, x: p.unary(), pos: tok.pos}

	case p.at("++"), p.at("--"):
		p.next()
		return &incExpr{op: tok.text, x: p.unary(), pos: tok.pos}

	case p.at("(") && p.startsTypeAfterParen():
		p.next()
		typ := p.typeName(tok.pos)
		p.expect(")")
		return &castExpr{typ: typ, x: p.unary(), pos: tok.pos}

	case p.at("sizeof"):
		p.next()
		// `sizeof(T)` names a type and `sizeof x` takes an expression,
		// and `sizeof (x)` is the second one wearing the first one's
		// parentheses. Only the symbol table tells them apart.
		if p.at("(") && p.startsTypeAfterParen() {
			p.next()
			typ := p.typeName(tok.pos)
			p.expect(")")
			return &sizeofExpr{typ: typ, pos: tok.pos}
		}
		return &sizeofExpr{x: p.unary(), pos: tok.pos}

	// The two pieces of <stdarg.h> that ordinary C cannot express. The
	// header is bundled and spells `va_start`, `va_arg`, `va_end` and
	// `va_copy` over them, which is what a real C library's does too —
	// the last two need no builtin, because doing nothing and copying a
	// struct are both things C can already say.
	case p.atBuiltin("__builtin_va_start"):
		p.next()
		p.expect("(")
		ap := p.assign()
		p.expect(")")
		return &vaStartExpr{ap: ap, pos: tok.pos}

	case p.atBuiltin("__builtin_va_arg"):
		p.next()
		p.expect("(")
		ap := p.assign()
		p.expect(",")
		typ := p.typeName(tok.pos)
		p.expect(")")
		return &vaArgExpr{ap: ap, typ: typ, pos: tok.pos}
	}
	return p.postfix()
}

// typeName reads a type with no name after it: what a cast and a sizeof
// name. It is a declaration specifier followed by an abstract
// declarator, which is the same declarator as any other with the
// identifier left out — `int (*)(int)` is `int (*f)(int)` without the f.
func (p *parser) typeName(pos position) *ctype {
	base, static, isTypedef := p.declSpecifiers()
	if static || isTypedef {
		p.fail(pos, "a type name has no storage class")
	}
	name, typ := p.declarator(base)
	if name != "" {
		p.fail(pos, "a type name declares nothing, and this one names %s", name)
	}
	return typ
}

// startsTypeAfterParen distinguishes `(int)x` from `(x)`. Without
// typedef the question is answered by one token.
func (p *parser) startsTypeAfterParen() bool {
	saved := p.i
	p.i++
	is := p.startsDecl()
	p.i = saved
	return is
}

func (p *parser) postfix() expr {
	x := p.primary()
	for {
		tok := p.peek()
		switch {
		case p.at("."), p.at("->"):
			p.next()
			name := p.expectIdent()
			x = &memberExpr{x: x, name: name.text, arrow: tok.text == "->", pos: tok.pos}
		case p.at("("):
			// A call is a postfix operator on whatever came before it,
			// which is what lets `fp(x)` and `(*fp)(x)` and
			// `ops[i].apply(x)` all be calls.
			p.next()
			call := &callExpr{callee: x, pos: tok.pos}
			if !p.at(")") {
				for {
					call.args = append(call.args, p.assign())
					if !p.accept(",") {
						break
					}
				}
			}
			p.expect(")")
			x = call

		case p.at("++"), p.at("--"):
			p.next()
			x = &incExpr{op: tok.text, x: x, postfix: true, pos: tok.pos}
		case p.at("["):
			// `a[i]` is defined as `*(a + i)`, so it is written as that
			// rather than lowered separately. Everything that falls out
			// of the definition — that the index may be the pointer, that
			// `&a[i]` needs no special case — falls out here too.
			p.next()
			index := p.expr()
			p.expect("]")
			x = &unaryExpr{
				op:  "*",
				x:   &binaryExpr{op: "+", a: x, b: index, pos: tok.pos},
				pos: tok.pos,
			}
		default:
			return x
		}
	}
}

func (p *parser) primary() expr {
	tok := p.peek()
	switch tok.kind {
	case tokInt, tokChar:
		p.next()
		return &intLit{val: tok.ival, typ: literalType(tok), pos: tok.pos}

	case tokFloat:
		p.next()
		return &floatLit{val: tok.fval, typ: typeDouble, pos: tok.pos}

	case tokStr:
		// Adjacent literals are one literal, which is how a long message
		// is written across lines and how a macro that expands to one
		// joins the text around it.
		var text strings.Builder
		for p.peek().kind == tokStr {
			text.WriteString(p.next().sval)
		}
		text.WriteByte(0)
		return &strLit{text: text.String(), pos: tok.pos}

	case tokIdent:
		p.next()
		return &identExpr{name: tok.text, pos: tok.pos}
	}

	if p.accept("(") {
		inner := p.expr()
		p.expect(")")
		return inner
	}
	p.fail(tok.pos, "expected an expression, found %s", describe(tok))
	return nil
}

// ---------------------------------------------------------------------------
// Constant expressions
// ---------------------------------------------------------------------------

// constantExpr reads an expression C requires to have a value at compile
// time: an enumerator, and an array's length. It is the ordinary
// expression grammar with a fold over the result rather than a second
// parser, so `enum { A = 1, B = A << 4 }` needs no extra rules.
func (p *parser) constantExpr() (int64, error) {
	return foldConstant(p.conditional(), p.enums)
}

func foldConstant(e expr, enums map[string]int64) (int64, error) {
	switch e := e.(type) {
	case *intLit:
		return int64(e.val), nil

	case *identExpr:
		if value, ok := enums[e.name]; ok {
			return value, nil
		}
		return 0, fmt.Errorf("%s is not a constant", e.name)

	case *unaryExpr:
		x, err := foldConstant(e.x, enums)
		if err != nil {
			return 0, err
		}
		switch e.op {
		case "+":
			return x, nil
		case "-":
			return -x, nil
		case "~":
			return ^x, nil
		case "!":
			return boolValue(x == 0), nil
		}
		return 0, fmt.Errorf("%s is not a constant expression", e.op)

	case *condExpr:
		cond, err := foldConstant(e.cond, enums)
		if err != nil {
			return 0, err
		}
		if cond != 0 {
			return foldConstant(e.then, enums)
		}
		return foldConstant(e.els, enums)

	case *binaryExpr:
		a, err := foldConstant(e.a, enums)
		if err != nil {
			return 0, err
		}
		b, err := foldConstant(e.b, enums)
		if err != nil {
			return 0, err
		}
		// The operator table is the preprocessor's, which computes in the
		// same one width over the same operators. Two of them would be
		// two chances to disagree about `1 << 40`.
		out, err := ppApply(token{kind: tokPunct, text: e.op, pos: e.pos},
			ppValue{v: a}, ppValue{v: b}, true)
		return out.v, err
	}
	return 0, fmt.Errorf("this is not a constant expression")
}

// literalType is the type C gives an integer constant: the first of
// int, long and their unsigned counterparts that holds the value, with
// the suffix setting a floor.
func literalType(tok token) *ctype {
	if tok.kind == tokChar {
		return typeInt
	}
	switch {
	case tok.unsigned && tok.wide:
		return typeULong
	case tok.unsigned:
		if tok.ival > 0xFFFFFFFF {
			return typeULong
		}
		return typeUInt
	case tok.wide:
		return typeLong
	}
	if tok.ival > 0x7FFFFFFF {
		return typeLong
	}
	return typeInt
}
