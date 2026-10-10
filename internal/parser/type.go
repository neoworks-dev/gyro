package parser

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
)

// parseType parses a type expression.
//
// A trailing `?` binds to the whole type, so `*Node?` is a nullable pointer
// rather than a pointer to a nullable value. Write `*(Node?)` for the latter.
func (p *Parser) parseType() ast.Type {
	typ := p.parseSuffixType()

	for p.at(token.Question) {
		typ = &ast.OptionalType{Offset: p.offset(), Elem: typ}
		p.next()
	}
	return typ
}

// parseSuffixType parses a type with its `[N]` and `[]` suffixes but without
// a trailing `?`, so that the nullable marker stays outermost.
func (p *Parser) parseSuffixType() ast.Type {
	return p.parseArraySuffixes(p.parseBaseType())
}

// parseArraySuffixes applies the `[N]` and `[]` suffixes to a base type
// that has already been parsed. A pattern reaches it that way: the path
// came first, and only the `[` after it says the pattern names a slice.
func (p *Parser) parseArraySuffixes(base ast.Type) ast.Type {
	for p.at(token.LBracket) {
		array := &ast.ArrayType{Offset: p.offset(), Elem: base}
		p.next()
		if !p.at(token.RBracket) {
			array.Length = p.parseBinary(precShift)
		}
		p.expect(token.RBracket)
		base = array
	}
	return base
}

func (p *Parser) parseBaseType() ast.Type {
	offset := p.offset()

	switch p.kind() {
	case token.Star:
		p.next()
		// parseSuffixType, not parseType: a trailing `?` belongs to the
		// pointer, not to what it points at.
		return &ast.PointerType{Offset: offset, Elem: p.parseSuffixType()}

	case token.LParen:
		return p.parseParenType()

	case token.Fn, token.Async:
		return p.parseFuncType()

	case token.Chan:
		p.next()
		p.expect(token.Lt)
		elem := p.parseType()
		p.expect(token.Gt)
		return &ast.ChanType{Offset: offset, Elem: elem}

	case token.Ident:
		// `code` is contextual: only a `(` right after it makes a code
		// pointer type, so a program may still name something `code`.
		if p.tok().Text == "code" && p.peekAt(1).Kind == token.LParen {
			return p.parseCodeType()
		}
		// `dyn` is contextual in the same way: it makes a trait value only
		// when a name follows it.
		if p.tok().Text == "dyn" && p.peekAt(1).Kind == token.Ident {
			p.next()
			return &ast.DynType{Offset: offset, Trait: p.parseBound()}
		}
		return &ast.NamedType{Offset: offset, Path: p.parseTypePathInType()}
	}

	p.errorf(offset, "expected a type, found %s", describe(p.tok()))
	p.next()
	return &ast.NamedType{
		Offset: offset,
		Path: &ast.Path{
			Offset:   offset,
			Segments: []ast.PathSegment{{Offset: offset, Name: "<error>"}},
		},
	}
}

// parseTypePathInType parses a path inside a type, where angle brackets are
// unambiguous: there is no comparison operator to confuse them with, so no
// speculation is needed.
func (p *Parser) parseTypePathInType() *ast.Path {
	path := &ast.Path{Offset: p.offset()}

	for {
		segment := ast.PathSegment{Offset: p.offset()}
		segment.Name, _ = p.expectIdent()
		segment.Args = p.parseTypeArgs()
		path.Segments = append(path.Segments, segment)

		if !p.accept(token.ColonColon) {
			return path
		}
	}
}

// parseTypeArgs parses an optional `<A, B>` argument list on a named type.
func (p *Parser) parseTypeArgs() []ast.Type {
	if !p.at(token.Lt) {
		return nil
	}
	p.next()

	var args []ast.Type
	for !p.at(token.Gt) && !p.atEOF() {
		args = append(args, p.parseType())
		if !p.accept(token.Comma) {
			break
		}
	}
	p.expect(token.Gt)
	return args
}

// parseParenType parses `(T)` as plain grouping and `(A, B)` as a tuple.
func (p *Parser) parseParenType() ast.Type {
	offset := p.offset()
	p.expect(token.LParen)

	var elements []ast.Type
	for !p.at(token.RParen) && !p.atEOF() {
		elements = append(elements, p.parseType())
		if !p.accept(token.Comma) {
			break
		}
	}
	p.expect(token.RParen)

	if len(elements) == 1 {
		return elements[0]
	}
	return &ast.TupleType{Offset: offset, Elements: elements}
}

// parseCodeType parses `code(A, B): C`, the address of a function with
// nothing beside it. No `async` and no effect row: neither would be a
// claim anyone checked, since what is at the address may not even have
// been compiled by this compiler.
func (p *Parser) parseCodeType() ast.Type {
	typ := &ast.CodeType{Offset: p.offset()}
	p.next() // code
	p.expect(token.LParen)

	for !p.at(token.RParen) && !p.atEOF() {
		typ.Params = append(typ.Params, p.parseType())
		if !p.accept(token.Comma) {
			break
		}
	}
	p.expect(token.RParen)

	typ.Results = p.parseResults()
	return typ
}

// parseFuncType parses `fn(A, B): C` for function-valued parameters.
func (p *Parser) parseFuncType() ast.Type {
	typ := &ast.FuncType{Offset: p.offset()}
	typ.Async = p.accept(token.Async)
	p.expect(token.Fn)
	p.expect(token.LParen)

	for !p.at(token.RParen) && !p.atEOF() {
		typ.Params = append(typ.Params, p.parseType())
		if !p.accept(token.Comma) {
			break
		}
	}
	p.expect(token.RParen)

	typ.Results = p.parseResults()
	typ.Effects = p.parseEffectRow()
	return typ
}
