package parser

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
)

// parseType parses a type expression, including the `[N]` and `[]` suffixes.
func (p *Parser) parseType() ast.Type {
	base := p.parseBaseType()

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
		return &ast.PointerType{Offset: offset, Elem: p.parseType()}

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
		named := &ast.NamedType{Offset: offset, Path: p.parsePath()}
		named.Args = p.parseTypeArgs()
		return named
	}

	p.errorf(offset, "expected a type, found %s", describe(p.tok()))
	p.next()
	return &ast.NamedType{Offset: offset, Path: &ast.Path{Offset: offset, Segments: []string{"<error>"}}}
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
