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
	// asmLabel is the symbol the last declarator was renamed to.
	asmLabel string

	// tolerant is the mode a header is read in. A header declares far
	// more than any program uses, written in whatever C its library
	// needed, so a declaration this front end cannot read is dropped and
	// the ones around it are kept: naming it is the error, and not
	// owning a header that declares it. A `.c` file's own code is still
	// refused whole, because there is nothing to drop it in favour of.
	//
	// It also skips function bodies. A header's inline functions are
	// there for a C compiler to inline, not for this program to call.
	tolerant bool
	// extern is set by declSpecifiers when the declaration said so.
	extern bool
	// skipped is what tolerance dropped, with the reason.
	skipped []skippedDecl
}

// skippedDecl is a declaration a tolerant parse could not read.
type skippedDecl struct {
	pos    position
	reason string
}

func parse(file string, toks []token, tolerant bool) (out *unit, err error) {
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
		tolerant: tolerant,
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
	case tokInvalid:
		return t.sval
	}
	return "'" + t.text + "'"
}

// ---------------------------------------------------------------------------
// Declarations
// ---------------------------------------------------------------------------

func (p *parser) unit() *unit {
	out := &unit{externs: map[string]*ctype{}}
	for p.peek().kind != tokEOF {
		if !p.tolerant {
			p.topLevel(out)
			continue
		}
		start := p.i
		pos := p.peek().pos
		if reason := p.attempt(func() { p.topLevel(out) }); reason != "" {
			p.skipped = append(p.skipped, skippedDecl{pos: pos, reason: reason})
			p.i = start
			p.skipDeclaration()
		}
	}
	out.structs = p.order
	out.enums = p.enums
	out.typedefs = p.typedefs
	out.skipped = p.skipped
	return out
}

// attempt runs one step of the parse and answers what stopped it, or "".
func (p *parser) attempt(step func()) (reason string) {
	defer func() {
		if r := recover(); r != nil {
			caught, ok := r.(failure)
			if !ok {
				panic(r)
			}
			reason = caught.err.Error()
		}
	}()
	step()
	return ""
}

// skipDeclaration moves past the declaration that did not read: through
// its semicolon, or through the braces of a function body. It always
// consumes something, which is what keeps a header made of nothing but
// unreadable declarations from looping.
func (p *parser) skipDeclaration() {
	depth := 0
	beforeBrace := ""
	prev := ""
	for {
		tok := p.next()
		switch {
		case tok.kind == tokEOF:
			return
		case tok.is("(") || tok.is("["):
			depth++
		case tok.is(")") || tok.is("]"):
			depth--
		case tok.is("{"):
			if depth == 0 {
				beforeBrace = prev
			}
			depth++
		case tok.is("}"):
			depth--
			if depth == 0 && beforeBrace == ")" {
				return
			}
		case tok.is(";") && depth <= 0:
			return
		}
		prev = tok.text
	}
}

// skipBlock moves past a function body without reading it.
func (p *parser) skipBlock() {
	pos := p.expect("{").pos
	depth := 1
	for depth > 0 {
		tok := p.next()
		switch {
		case tok.kind == tokEOF:
			p.fail(pos, "this block is never closed")
		case tok.is("{"):
			depth++
		case tok.is("}"):
			depth--
		}
	}
}

// topLevel reads one declaration at file scope.
func (p *parser) topLevel(out *unit) {
	pos := p.peek().pos

	if p.atIdent("_Static_assert") {
		// A compile-time check on the header's own assumptions. The
		// ones that matter hold, because the layout is the target's.
		p.next()
		p.skipBalanced()
		p.expect(";")
		return
	}
	if p.accept(";") {
		return
	}

	base, static, isTypedef := p.declSpecifiers()
	external := p.extern

	// A declaration with no declarator declares a type and nothing
	// else: `struct Node;` opens a tag a later line will define, and
	// `struct Node { ... };` or `enum E { ... };` closes one.
	if p.accept(";") {
		if !base.isMembered() && !base.isInteger() {
			p.fail(pos, "this declaration declares nothing")
		}
		return
	}

	// A definition is the one declarator that ends its declaration
	// without a semicolon, and C says so by putting a block where the
	// comma or the semicolon would have gone.
	defined := false
	for {
		name, typ := p.declarator(base)
		symbol := p.asmLabel
		p.asmLabel = ""

		switch {
		case isTypedef:
			p.defineTypedef(name, typ, pos)

		case typ.isFunc():
			fn := &funcDecl{
				name: name, result: typ.result, params: typ.params,
				variadic: typ.variadic, static: static, pos: pos,
				label: symbol,
			}
			out.funcs = append(out.funcs, fn)
			if !p.at(";") && !p.at(",") {
				if p.tolerant {
					p.skipBlock()
					fn.inline = true
				} else {
					fn.body = p.block()
				}
				defined = true
			}

		case external && !p.at("="):
			// `extern int errno;` says another module owns the object.
			// This one has nowhere to put it, and reading it would be
			// reading storage nobody gave an address in the IR.
			out.externs[name] = typ

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

// defineTypedef registers a type name. A struct written without a tag
// takes the typedef's name as its own, which is what makes the
// `typedef struct { ... } Point;` idiom produce a `%Point` record and
// not an anonymous one.
func (p *parser) defineTypedef(name string, typ *ctype, pos position) {
	// In a header, the types C uses for sizes and addresses are the
	// pointer-sized integer METALS has a name for, so a prototype that
	// says size_t is a signature that says usize. Everything else about
	// the type is unsigned long's.
	if p.tolerant && (name == "size_t" || name == "uintptr_t") && typ.kind == kULong && !typ.usize {
		clone := *typ
		clone.usize = true
		typ = &clone
	}
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
	p.extern = false

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
			if p.skipDecoration() {
				continue
			}
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
		case "extern":
			p.extern = true
			p.next()
		case "const", "volatile", "register", "auto":
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

// decorations are the words GNU and C99 put in front of and around a
// declaration that say nothing a program here can act on: an inline
// function is still a function, a restrict pointer is still a pointer,
// and an attribute is the library talking to its own compiler. The
// attributes that change the layout of what they decorate are the
// exception, and are refused by name.
var decorations = map[string]bool{
	"inline": true, "__inline": true, "__inline__": true,
	"restrict": true, "__restrict": true, "__restrict__": true,
	"_Noreturn": true, "__extension__": true,
	"__const": true, "__const__": true, "__volatile": true, "__volatile__": true,
	"__signed": true, "__signed__": true,
}

func (p *parser) atIdent(name string) bool {
	tok := p.peek()
	return tok.kind == tokIdent && tok.text == name
}

// skipDecoration consumes one decoration, an attribute list or an
// assembler label if the next token begins one.
func (p *parser) skipDecoration() bool {
	tok := p.peek()
	if tok.kind != tokIdent {
		return false
	}
	switch {
	case decorations[tok.text]:
		p.next()
		return true
	case tok.text == "__attribute__" || tok.text == "__attribute":
		p.next()
		start := p.i
		p.skipBalanced()
		for _, inner := range p.toks[start:p.i] {
			switch inner.text {
			case "packed", "__packed__", "aligned", "__aligned__", "vector_size", "__vector_size__",
				"mode", "__mode__", "transparent_union", "__transparent_union__":
				p.fail(tok.pos, "__attribute__((%s)) changes the layout of what it decorates, and this front end lays out C's defaults only", inner.text)
			}
		}
		return true
	case tok.text == "__asm__" || tok.text == "__asm" || tok.text == "asm":
		p.next()
		for p.peek().kind == tokKeyword && (p.at("volatile") || p.at("const")) || p.atIdent("__volatile__") {
			p.next()
		}
		start := p.i
		p.skipBalanced()
		if label := p.toks[start+1 : p.i-1]; len(label) == 1 && label[0].kind == tokStr {
			p.asmLabel = label[0].sval
		}
		return true
	}
	return false
}

// skipDecorations consumes all of them.
func (p *parser) skipDecorations() {
	for p.skipDecoration() {
	}
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
	// `short int`, `long int` and `unsigned long long int` spell the
	// type without the int, which is why int is dropped when another
	// word already says which integer this is.
	if counts["int"] > 0 && (longs > 0 || counts["short"] > 0) {
		delete(counts, "int")
	}

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
		// way twice. It can be named, because a header does, and
		// nothing that needs its value can be lowered: that is where
		// it is refused, rather than silently narrowed to f64.
		if longs == 1 {
			return typeLongDouble
		}
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
	p.skipDecorations()

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
		if p.atIdent("_Static_assert") {
			p.next()
			p.skipBalanced()
			p.expect(";")
			continue
		}
		base, static, isTypedef := p.declSpecifiers()
		if static || isTypedef {
			p.fail(pos, "a %s member has no storage class", keyword)
		}
		for {
			// An unnamed `:0` has no declarator at all: it says only
			// where the next bit-field starts.
			if p.at(":") {
				fields = append(fields, p.bitField(pos, "", base))
				if !p.accept(",") {
					break
				}
				continue
			}
			name, fieldType := p.declarator(base)
			if p.at(":") {
				fields = append(fields, p.bitField(pos, name, fieldType))
				if !p.accept(",") {
					break
				}
				continue
			}
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
	p.skipDecorations()
	if len(fields) == 0 {
		p.fail(pos, "%s %s has no members, and METALS has no shape with none", keyword, tag)
	}
	// A zero-width bit-field says where the next one starts and reserves
	// nothing, so a body of only those declares no storage at all.
	// Where the rest of them end up is the lowering's question, since it
	// takes the size of every member before them.
	reserves := false
	for _, f := range fields {
		if f.bits == nil || f.bits.width > 0 {
			reserves = true
		}
	}
	if !reserves {
		p.fail(pos, "%s %s is nothing but zero-width bit-fields, which reserve no storage", keyword, tag)
	}
	typ.fields = fields
	return typ
}

// bitField reads the `: width` half of a bit-field declaration. The
// width lands in the field's bits, and where those bits go is pack's
// question, since it depends on everything declared before them.
func (p *parser) bitField(pos position, name string, declared *ctype) field {
	p.expect(":")
	bits, err := p.constantExpr()
	if err != nil {
		p.fail(pos, "%s", err)
	}
	if !declared.isInteger() {
		p.fail(pos, "a bit-field counts bits of an integer, and %s is not one", declared)
	}
	if bits < 0 || bits > int64(width(declared))*8 {
		p.fail(pos, "a bit-field of %d bits does not fit a %s", bits, declared)
	}
	if bits == 0 && name != "" {
		// A zero-width bit-field is the instruction "start the next one
		// at a fresh unit", and an instruction has no name.
		p.fail(pos, "bit-field %s has no bits; only an unnamed one may be zero-width", name)
	}
	return field{
		name: name,
		typ:  declared,
		bits: &bitfield{width: int(bits), signed: !declared.isUnsigned()},
	}
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
	name, typ := p.declaratorBody(base)
	p.skipDecorations()
	return name, typ
}

func (p *parser) declaratorBody(base *ctype) (string, *ctype) {
	p.skipDecorations()
	base = p.pointers(base)
	p.skipDecorations()

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
		for p.at("const") || p.at("volatile") || p.skipDecoration() {
			if p.at("const") || p.at("volatile") {
				p.next()
			}
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

	case p.at("goto"):
		p.next()
		name := p.expectIdent()
		p.expect(";")
		return &gotoStmt{name: name.text, pos: tok.pos}

	// A name followed by a colon is a label. Nothing else in C's
	// statement grammar begins that way — the conditional operator's
	// colon needs a `?` in front of it — so one token of lookahead
	// settles it, and a typedef name is a label here rather than a
	// declaration for the same reason.
	case tok.kind == tokIdent && p.ahead().is(":"):
		p.next()
		p.next()
		return &labelStmt{name: tok.text, pos: tok.pos}

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

	case *sizeofExpr:
		typ := e.typ
		if typ == nil {
			typ = staticType(e.x)
		}
		if typ != nil {
			if size, _, ok := sizeAlign(typ); ok {
				return size, nil
			}
		}
		return 0, fmt.Errorf("this sizeof is not a constant this front end can fold")

	case *castExpr:
		x, err := foldConstant(e.x, enums)
		if err != nil {
			return 0, err
		}
		return truncateTo(e.typ, x)
	}
	return 0, fmt.Errorf("this is not a constant expression")
}

// staticType is the type of the few expressions whose type a parse can
// know without a symbol table: a cast, and a member reached through one.
// That is `sizeof(((T *)0)->member)`, which is how a header measures a
// member without an object to measure it on.
func staticType(e expr) *ctype {
	switch e := e.(type) {
	case *castExpr:
		return e.typ
	case *unaryExpr:
		if e.op == "*" {
			if inner := staticType(e.x); inner != nil && inner.isPointer() {
				return inner.elem
			}
		}
	case *memberExpr:
		base := staticType(e.x)
		if base == nil {
			return nil
		}
		if e.arrow {
			if !base.isPointer() {
				return nil
			}
			base = base.elem
		}
		for _, f := range base.fields {
			if f.name == e.name {
				return f.typ
			}
		}
	}
	return nil
}

// truncateTo is what converting a constant to an integer type does to it.
func truncateTo(t *ctype, v int64) (int64, error) {
	switch t.kind {
	case kBool:
		return boolValue(v != 0), nil
	case kChar:
		return int64(int8(v)), nil
	case kUChar:
		return int64(uint8(v)), nil
	case kShort:
		return int64(int16(v)), nil
	case kUShort:
		return int64(uint16(v)), nil
	case kInt:
		return int64(int32(v)), nil
	case kUInt:
		return int64(uint32(v)), nil
	case kLong, kULong, kPtr:
		return v, nil
	}
	return 0, fmt.Errorf("a cast to %s is not an integer constant", t)
}

// sizeAlign is the size and alignment of a type that needs no layout
// pass to answer: scalars, arrays and aggregates of them. It is what a
// header's `char pad[3 * sizeof(int)]` needs while the parse is still
// running, before any record has been laid out. A bit-field is the
// reason an aggregate may not answer, because packing is the layout
// pass's.
func sizeAlign(t *ctype) (size, align int64, ok bool) {
	switch t.kind {
	case kBool, kChar, kUChar:
		return 1, 1, true
	case kShort, kUShort:
		return 2, 2, true
	case kInt, kUInt, kFloat:
		return 4, 4, true
	case kLong, kULong, kDouble, kPtr:
		return 8, 8, true
	case kLongDouble:
		return 16, 16, true
	case kArray:
		if t.length < 0 {
			return 0, 0, false
		}
		elem, a, ok := sizeAlign(t.elem)
		return elem * t.length, a, ok
	case kStruct, kUnion:
		if t.fields == nil {
			return 0, 0, false
		}
		var offset, strictest int64 = 0, 1
		for _, f := range t.fields {
			if f.bits != nil {
				return 0, 0, false
			}
			s, a, ok := sizeAlign(f.typ)
			if !ok {
				return 0, 0, false
			}
			strictest = max(strictest, a)
			if t.kind == kUnion {
				offset = max(offset, s)
				continue
			}
			offset = (offset+a-1)/a*a + s
		}
		return (offset + strictest - 1) / strictest * strictest, strictest, true
	}
	return 0, 0, false
}

// literalType is the type C gives an integer constant: the first of
// int, long and their unsigned counterparts that holds the value, with
// the suffix setting a floor.
func literalType(tok token) *ctype {
	if tok.kind == tokChar {
		return typeInt
	}
	// A constant written in hex or octal takes the unsigned type when the
	// signed one is too small, and a decimal one does not; that is the
	// difference between 0x80000000 and 2147483648.
	notDecimal := len(tok.text) > 1 && tok.text[0] == '0'
	switch {
	case tok.unsigned && tok.wide:
		return typeULong
	case tok.unsigned:
		if tok.ival > 0xFFFFFFFF {
			return typeULong
		}
		return typeUInt
	case tok.wide:
		if tok.ival > 0x7FFFFFFFFFFFFFFF {
			return typeULong
		}
		return typeLong
	}
	if tok.ival > 0x7FFFFFFFFFFFFFFF {
		return typeULong
	}
	if tok.ival > 0xFFFFFFFF {
		return typeLong
	}
	if tok.ival > 0x7FFFFFFF {
		if notDecimal {
			return typeUInt
		}
		return typeLong
	}
	return typeInt
}
