package parser

import (
	"strconv"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/lexer"
	"github.com/gyro-lang/gyro/internal/token"
)

// Binary operator binding powers. Higher binds tighter; every operator here
// is left-associative.
const (
	precRange = iota + 1
	precOr
	precAnd
	precBitOr
	precBitXor
	precBitAnd
	precEquality
	precComparison
	precShift
	precAdditive
	precMultiplicative
)

func (p *Parser) parseExpr() ast.Expr {
	return p.parseBinary(precRange)
}

// binaryOp inspects the current token as a binary operator and returns its
// kind, binding power and how many tokens it spans. A right shift spans two
// tokens because the lexer deliberately never merges consecutive `>`.
func (p *Parser) binaryOp() (kind token.Kind, prec int, width int) {
	switch p.kind() {
	case token.LeftArrow:
		// In binary position `<-` can only have been a comparison against a
		// negated operand. The token is split apart by parseBinaryFrom once
		// it has decided to consume it; doing it here would corrupt the
		// stream when the precedence check rejects the operator.
		return token.LeftArrow, precComparison, 1
	case token.DotDot:
		return token.DotDot, precRange, 1
	case token.OrOr:
		return token.OrOr, precOr, 1
	case token.AndAnd:
		return token.AndAnd, precAnd, 1
	case token.Pipe:
		return token.Pipe, precBitOr, 1
	case token.Caret:
		return token.Caret, precBitXor, 1
	case token.Amp:
		return token.Amp, precBitAnd, 1
	case token.Eq:
		return token.Eq, precEquality, 1
	case token.NotEq:
		return token.NotEq, precEquality, 1
	case token.Lt:
		return token.Lt, precComparison, 1
	case token.LtEq:
		return token.LtEq, precComparison, 1
	case token.GtEq:
		return token.GtEq, precComparison, 1
	case token.Gt:
		if p.peekAt(1).Kind == token.Gt && p.adjacent(0, 1) {
			return token.Shr, precShift, 2
		}
		return token.Gt, precComparison, 1
	case token.Shl:
		return token.Shl, precShift, 1
	case token.Plus:
		return token.Plus, precAdditive, 1
	case token.Minus:
		return token.Minus, precAdditive, 1
	case token.PlusMod:
		return token.PlusMod, precAdditive, 1
	case token.MinusMod:
		return token.MinusMod, precAdditive, 1
	case token.Star:
		return token.Star, precMultiplicative, 1
	case token.StarMod:
		return token.StarMod, precMultiplicative, 1
	case token.Slash:
		return token.Slash, precMultiplicative, 1
	case token.Percent:
		return token.Percent, precMultiplicative, 1
	}
	return token.Invalid, 0, 0
}

// splitLeftArrow rewrites the current `<-` token into a `<` followed by a
// `-`, so that `x < -1` parses as a comparison. The channel receive and send
// forms never reach binary position, so this is the only reading left.
func (p *Parser) splitLeftArrow() {
	arrow := p.tokens[p.index]
	p.tokens[p.index] = token.Token{Kind: token.Lt, Offset: arrow.Offset}

	minus := token.Token{Kind: token.Minus, Offset: arrow.Offset + 1}
	p.tokens = append(p.tokens, token.Token{})
	copy(p.tokens[p.index+2:], p.tokens[p.index+1:])
	p.tokens[p.index+1] = minus
}

func (p *Parser) parseBinary(minPrec int) ast.Expr {
	return p.parseBinaryFrom(p.parseCast(), minPrec)
}

// parseBinaryFrom continues the precedence-climbing loop with an operand the
// caller has already parsed. Statement parsing uses it to inspect the token
// after the first operand before deciding whether the statement is a send, an
// assignment or a plain expression.
func (p *Parser) parseBinaryFrom(left ast.Expr, minPrec int) ast.Expr {
	for {
		op, prec, width := p.binaryOp()
		if prec < minPrec {
			return left
		}

		if op == token.LeftArrow {
			p.splitLeftArrow()
			op = token.Lt
		}

		offset := p.offset()
		for i := 0; i < width; i++ {
			p.next()
		}

		right := p.parseBinary(prec + 1)

		if op == token.DotDot {
			left = &ast.RangeExpr{Offset: offset, Lo: left, Hi: right}
			continue
		}
		left = &ast.BinaryExpr{Offset: offset, Op: op, X: left, Y: right}
	}
}

// parseCast applies `as T` conversions. It sits between the binary loop and
// the unary operators, so `-x as f64` converts the negated value and
// `x as f64 * 2` converts before multiplying.
func (p *Parser) parseCast() ast.Expr {
	expr := p.parseUnary()
	for p.at(token.As) {
		offset := p.offset()
		p.next()
		expr = &ast.CastExpr{Offset: offset, X: expr, Type: p.parseType()}
	}
	return expr
}

func (p *Parser) parseUnary() ast.Expr {
	switch p.kind() {
	case token.Minus, token.Bang, token.Tilde, token.Amp, token.Star:
		expr := &ast.UnaryExpr{Offset: p.offset(), Op: p.kind()}
		p.next()
		expr.X = p.parseUnary()
		return expr

	case token.Await:
		expr := &ast.AwaitExpr{Offset: p.offset()}
		p.next()
		expr.X = p.parseUnary()
		return expr

	case token.LeftArrow:
		expr := &ast.RecvExpr{Offset: p.offset()}
		p.next()
		expr.Chan = p.parseUnary()
		return expr
	}
	return p.parsePostfix(p.parsePrimary())
}

// parsePostfix applies calls, indexing and field access to an already
// parsed operand.
func (p *Parser) parsePostfix(expr ast.Expr) ast.Expr {
	for {
		switch p.kind() {
		case token.LParen:
			expr = &ast.CallExpr{Offset: expr.Pos(), Fun: expr, Args: p.parseArgs()}

		case token.LBracket:
			offset := p.offset()
			p.next()
			index := &ast.IndexExpr{Offset: offset, X: expr}
			p.withStructLits(true, func() {
				index.Index = p.parseExpr()
			})
			p.expect(token.RBracket)
			expr = index

		case token.Dot:
			expr = p.parseSelector(expr)

		case token.Lt:
			call, ok := p.tryGenericCall(expr)
			if !ok {
				return expr
			}
			expr = call

		default:
			return expr
		}
	}
}

// parseSelector parses what follows a `.`: a field or method name, or a
// tuple index as in `pair.0`.
func (p *Parser) parseSelector(x ast.Expr) ast.Expr {
	offset := p.offset()
	p.next() // '.'

	switch p.kind() {
	case token.Int:
		tok := p.next()
		return &ast.TupleIndexExpr{Offset: offset, X: x, Index: p.tupleIndex(tok)}

	case token.Float:
		// `pair.0.1` reaches the parser as `.` followed by the float `0.1`,
		// because a digit after a dot reads as a decimal point. After a `.`
		// it can only be two tuple indexes.
		return p.splitTupleFloat(x, offset)
	}

	nameOffset := p.offset()
	name, _ := p.expectIdent()
	return &ast.SelectorExpr{Offset: offset, X: x, Name: name, NameOffset: nameOffset}
}

// tupleIndex decodes an integer token used as a tuple position.
func (p *Parser) tupleIndex(tok token.Token) int {
	index, err := strconv.Atoi(stripSeparators(tok.Text))
	if err != nil {
		p.errorf(tok.Offset, "tuple index must be a plain decimal number")
		return 0
	}
	return index
}

func (p *Parser) splitTupleFloat(x ast.Expr, dotOffset int) ast.Expr {
	tok := p.next()
	first, rest, found := strings.Cut(stripSeparators(tok.Text), ".")
	if !found || first == "" || rest == "" || strings.ContainsAny(rest, "eE") {
		p.errorf(tok.Offset, "expected a tuple index after '.'")
		return x
	}

	inner := &ast.TupleIndexExpr{
		Offset: dotOffset,
		X:      x,
		Index:  p.tupleIndex(token.Token{Text: first, Offset: tok.Offset}),
	}
	return &ast.TupleIndexExpr{
		Offset: tok.Offset,
		X:      inner,
		Index:  p.tupleIndex(token.Token{Text: rest, Offset: tok.Offset + len(first) + 1}),
	}
}

// tryGenericCall speculatively parses `<args>(` after an operand. It only
// commits when the angle brackets close and a call follows, so an ordinary
// comparison such as `a < b` is left for the binary parser.
func (p *Parser) tryGenericCall(fun ast.Expr) (*ast.CallExpr, bool) {
	var typeArgs []ast.TypeArg

	ok := p.trial(func() {
		p.expect(token.Lt)
		for !p.at(token.Gt) && !p.atEOF() {
			typeArgs = append(typeArgs, p.parseTypeArg())
			if !p.accept(token.Comma) {
				break
			}
		}
		p.expect(token.Gt)

		// Without a call the angle brackets were comparisons after all.
		if !p.at(token.LParen) {
			p.errorf(p.offset(), "not a generic call")
		}
	})
	if !ok {
		return nil, false
	}

	return &ast.CallExpr{
		Offset:   fun.Pos(),
		Fun:      fun,
		TypeArgs: typeArgs,
		Args:     p.parseArgs(),
	}, true
}

// parseTypeArg parses one angle-bracketed argument, preferring a type and
// falling back to a constant expression for forms like `VectorAdd<1, 8>`.
func (p *Parser) parseTypeArg() ast.TypeArg {
	var parsed ast.Type
	ok := p.trial(func() {
		parsed = p.parseType()
		// The argument must end here; anything else means this was not a
		// type after all, as in `n<1` followed by more expression.
		if !p.at(token.Comma) && !p.at(token.Gt) {
			p.errorf(p.offset(), "not a type argument")
		}
	})
	if ok {
		return ast.TypeArg{Type: parsed}
	}

	// Stop below comparison precedence so `>` still closes the list.
	return ast.TypeArg{Expr: p.parseBinary(precShift)}
}

// parseArgs parses a parenthesized argument list, allowing the named form
// `@concurrent(threads = 8)`.
func (p *Parser) parseArgs() []ast.Arg {
	p.expect(token.LParen)

	var args []ast.Arg
	p.withStructLits(true, func() {
		p.skipTerminators()
		for !p.at(token.RParen) && !p.atEOF() {
			arg := ast.Arg{Offset: p.offset()}

			if p.at(token.Ident) && p.peekAt(1).Kind == token.Assign {
				arg.Name = p.next().Text
				p.next()
			}
			arg.Value = p.parseExpr()
			arg.Spread = p.accept(token.Ellipsis)
			args = append(args, arg)

			p.skipTerminators()
			if !p.accept(token.Comma) {
				break
			}
			p.skipTerminators()
		}
	})

	p.expect(token.RParen)
	return args
}

func (p *Parser) parsePrimary() ast.Expr {
	offset := p.offset()

	switch p.kind() {
	case token.Int, token.Float:
		tok := p.next()
		return &ast.BasicLit{Offset: offset, Kind: tok.Kind, Value: stripSeparators(tok.Text)}

	case token.String:
		return p.parseStringLit()

	case token.Char:
		tok := p.next()
		return &ast.BasicLit{Offset: offset, Kind: token.Char, Value: lexer.Unescape(tok.Text)}

	case token.True, token.False:
		kind := p.kind()
		p.next()
		return &ast.BasicLit{Offset: offset, Kind: kind, Value: kind.String()}

	case token.Null:
		p.next()
		return &ast.BasicLit{Offset: offset, Kind: token.Null, Value: "null"}

	case token.Self:
		p.next()
		return &ast.SelfExpr{Offset: offset}

	case token.Underscore:
		p.next()
		return &ast.Ident{Offset: offset, Name: "_"}

	case token.Match:
		return p.parseMatch()

	case token.Chan:
		return p.parseChanLit()

	case token.Fn:
		return p.parseClosure()

	case token.Async:
		if p.peekAt(1).Kind == token.Fn {
			return p.parseClosure()
		}

	case token.LParen:
		p.next()
		var elements []ast.Expr
		p.withStructLits(true, func() {
			elements = append(elements, p.parseExpr())
			for p.accept(token.Comma) {
				elements = append(elements, p.parseExpr())
			}
		})
		p.expect(token.RParen)

		// `(expr)` is plain grouping; a comma makes it a tuple. There are no
		// one-element tuples.
		if len(elements) == 1 {
			return elements[0]
		}
		return &ast.TupleLit{Offset: offset, Elements: elements}

	case token.LBracket:
		return p.parseArrayLit()

	case token.Ident:
		return p.parseNameOrStructLit()
	}

	p.errorf(offset, "expected an expression, found %s", describe(p.tok()))
	p.next()
	return &ast.Ident{Offset: offset, Name: "<error>"}
}

// parseNameOrStructLit parses a path and, when a brace may legally follow,
// turns it into a struct literal.
func (p *Parser) parseNameOrStructLit() ast.Expr {
	path := p.parseTypePath()

	// `Stack<i32>{ ... }` instantiates a generic struct. The angle brackets
	// are speculative for the same reason a generic call's are: without the
	// brace that follows, `a < b` is just a comparison.
	if p.at(token.Lt) && !p.noStructLit {
		var args []ast.Type
		ok := p.trial(func() {
			args = p.parseTypeArgs()
			if !p.at(token.LBrace) {
				p.errorf(p.offset(), "not a generic struct literal")
			}
		})
		if ok {
			last := len(path.Segments) - 1
			path.Segments[last].Args = args
			return p.parseStructLitBody(&ast.NamedType{Offset: path.Offset, Path: path})
		}
	}

	if p.at(token.LBrace) && !p.noStructLit {
		return p.parseStructLitBody(&ast.NamedType{Offset: path.Offset, Path: path})
	}

	if path.IsPlainName() {
		return &ast.Ident{Offset: path.Offset, Name: path.Segments[0].Name}
	}
	return path
}

// parsePath parses `a::b::c`, without type arguments on any segment. It is
// used where a bare name is expected, such as a decorator.
func (p *Parser) parsePath() *ast.Path {
	path := &ast.Path{Offset: p.offset()}

	for {
		segment := ast.PathSegment{Offset: p.offset()}
		segment.Name, _ = p.expectIdent()
		path.Segments = append(path.Segments, segment)

		if !p.accept(token.ColonColon) {
			return path
		}
	}
}

// parseTypePath parses a path whose segments may carry type arguments, as in
// `Array<i32>::new` or `ast::cast<i32>`. Angle brackets are only consumed
// when they close and are followed by something that can continue a path or
// a call, so an ordinary comparison is left alone.
func (p *Parser) parseTypePath() *ast.Path {
	path := &ast.Path{Offset: p.offset()}

	for {
		segment := ast.PathSegment{Offset: p.offset()}
		segment.Name, _ = p.expectIdent()

		if p.at(token.Lt) {
			var args []ast.Type
			ok := p.trial(func() {
				args = p.parseTypeArgs()
				if !p.at(token.ColonColon) {
					p.errorf(p.offset(), "not a parameterized path segment")
				}
			})
			if ok {
				segment.Args = args
			}
		}
		path.Segments = append(path.Segments, segment)

		if !p.accept(token.ColonColon) {
			return path
		}
	}
}

func (p *Parser) parseStructLitBody(typ ast.Type) ast.Expr {
	lit := &ast.StructLit{Offset: typ.Pos(), Type: typ}
	p.expect(token.LBrace)

	// `Point{ x: 1 }` names its fields; `Mutex{ 0 }` fills them in
	// declaration order. The first field decides which form the literal is.
	lit.Positional = !(p.at(token.Ident) && p.peekAt(1).Kind == token.Colon)

	p.withStructLits(true, func() {
		p.skipTerminators()
		for !p.at(token.RBrace) && !p.atEOF() {
			field := ast.FieldValue{Offset: p.offset()}
			if !lit.Positional {
				field.Name, _ = p.expectIdent()
				p.expect(token.Colon)
			}
			field.Value = p.parseExpr()
			lit.Fields = append(lit.Fields, field)

			p.skipTerminators()
			if !p.accept(token.Comma) {
				break
			}
			p.skipTerminators()
		}
	})

	p.expect(token.RBrace)
	return lit
}

func (p *Parser) parseArrayLit() ast.Expr {
	lit := &ast.ArrayLit{Offset: p.offset()}
	p.expect(token.LBracket)

	p.withStructLits(true, func() {
		p.skipTerminators()
		for !p.at(token.RBracket) && !p.atEOF() {
			lit.Elements = append(lit.Elements, p.parseExpr())
			p.skipTerminators()
			if !p.accept(token.Comma) {
				break
			}
			p.skipTerminators()
		}
	})

	p.expect(token.RBracket)
	return lit
}

// parseClosure parses an anonymous function:
//
//	fn(a: i32): i32 { return a * 2 }
//	fn(x) x * 2                        // inferred types, expression body
//	fn[move node]() { use(node) }      // explicit capture list
func (p *Parser) parseClosure() ast.Expr {
	closure := &ast.ClosureExpr{Offset: p.offset()}
	closure.Async = p.accept(token.Async)
	p.expect(token.Fn)

	if p.at(token.LBracket) {
		closure.Captures = p.parseCaptureList()
	}

	closure.Params = p.parseClosureParams()
	if p.accept(token.Colon) {
		closure.Results = p.parseResultList()
	}
	closure.Effects = p.parseEffectRow()

	// A brace opens a block body; anything else is a single expression whose
	// value is the closure's result.
	if p.at(token.LBrace) {
		closure.Body = p.parseBlock()
		return closure
	}

	p.withStructLits(true, func() {
		closure.BodyExpr = p.parseExpr()
	})
	return closure
}

// parseCaptureList parses `[move a, move b]`. Copies are implicit and never
// listed, so every entry transfers ownership. `move` is a contextual
// identifier rather than a keyword, so it stays usable as an ordinary name
// everywhere else.
func (p *Parser) parseCaptureList() []ast.Capture {
	p.expect(token.LBracket)

	var captures []ast.Capture
	for !p.at(token.RBracket) && !p.atEOF() {
		capture := ast.Capture{Offset: p.offset()}
		word, _ := p.expectIdent()

		if word == "move" && p.at(token.Ident) {
			capture.Name, _ = p.expectIdent()
		} else {
			p.errorf(capture.Offset, "capture lists hold only `move name` entries; copies are implicit")
			capture.Name = word
		}
		captures = append(captures, capture)

		if !p.accept(token.Comma) {
			break
		}
	}

	p.expect(token.RBracket)
	return captures
}

// parseClosureParams parses a closure's parameter list, where the type
// annotation is optional because it can come from the expected type.
func (p *Parser) parseClosureParams() []ast.Param {
	p.expect(token.LParen)

	var params []ast.Param
	p.skipTerminators()
	for !p.at(token.RParen) && !p.atEOF() {
		param := ast.Param{Offset: p.offset()}
		param.Name, _ = p.expectIdent()

		if p.accept(token.Colon) {
			param.Type = p.parseType()
		}
		if p.accept(token.Assign) {
			p.withStructLits(true, func() {
				param.Default = p.parseExpr()
			})
		}
		params = append(params, param)

		p.skipTerminators()
		if !p.accept(token.Comma) {
			break
		}
		p.skipTerminators()
	}

	p.expect(token.RParen)
	return params
}

// parseChanLit parses `chan<T>()` or `chan<T>(capacity)`.
func (p *Parser) parseChanLit() ast.Expr {
	lit := &ast.ChanLit{Offset: p.offset()}
	p.expect(token.Chan)
	p.expect(token.Lt)
	lit.Elem = p.parseType()
	p.expect(token.Gt)

	p.expect(token.LParen)
	p.withStructLits(true, func() {
		if !p.at(token.RParen) {
			lit.Cap = p.parseExpr()
		}
	})
	p.expect(token.RParen)
	return lit
}

// parseMatch parses `match subject { pattern: body ... }`. Arms end at the
// newline, like every other statement.
func (p *Parser) parseMatch() ast.Expr {
	expr := &ast.MatchExpr{Offset: p.offset()}
	p.expect(token.Match)

	p.withStructLits(false, func() {
		expr.Subject = p.parseExpr()
	})

	p.expect(token.LBrace)
	p.withStructLits(true, func() {
		p.skipTerminators()
		for !p.at(token.RBrace) && !p.atEOF() {
			before := p.index

			arm := ast.MatchArm{Offset: p.offset()}
			arm.Pattern = p.parsePattern()
			p.expect(token.Colon)
			arm.Body = p.parseArmBody()
			expr.Arms = append(expr.Arms, arm)

			p.expectArmEnd()

			if p.index == before {
				p.next()
			}
		}
	})

	p.expect(token.RBrace)
	return expr
}

// expectArmEnd consumes the newline that ends a match or select arm,
// rejecting the comma-separated style.
func (p *Parser) expectArmEnd() {
	if p.at(token.Comma) {
		p.errorf(p.offset(), "arms end at the newline; there is no comma between them")
		p.next()
	}
	p.skipTerminators()
}

// parsePattern parses one match pattern. A bare identifier becomes a binding;
// sema promotes it to a variant reference when the name resolves to one.
func (p *Parser) parsePattern() ast.Pattern {
	offset := p.offset()

	switch p.kind() {
	case token.Underscore:
		p.next()
		return &ast.WildcardPattern{Offset: offset}

	case token.Int, token.Float, token.String, token.Char,
		token.True, token.False, token.Null, token.Minus:
		return &ast.LiteralPattern{Value: p.parseBinary(precShift)}

	// A pattern that opens with `(` or `*` names a type no path can spell,
	// which only an `any` subject can hold. There is no other pattern that
	// starts either way, so no speculation is needed.
	case token.LParen, token.Star:
		return p.parseTypeTestPattern(offset, p.parseType())

	case token.Ident:
		path := p.parsePath()

		// `i32[](xs)`: the path turned out to be a slice or array type's
		// element rather than the whole pattern.
		if p.at(token.LBracket) {
			named := &ast.NamedType{Offset: offset, Path: path}
			return p.parseTypeTestPattern(offset, p.parseArraySuffixes(named))
		}

		if p.at(token.LParen) {
			pattern := &ast.VariantPattern{Offset: offset, Path: path}
			pattern.Payload = p.parsePatternPayload()
			return pattern
		}

		if !path.IsPlainName() {
			return &ast.VariantPattern{Offset: offset, Path: path}
		}
		return &ast.BindingPattern{Offset: offset, Name: path.Segments[0].Name}
	}

	p.errorf(offset, "expected a pattern, found %s", describe(p.tok()))
	p.next()
	return &ast.WildcardPattern{Offset: offset}
}

func (p *Parser) parseTypeTestPattern(offset int, typ ast.Type) ast.Pattern {
	pattern := &ast.TypeTestPattern{Offset: offset, Type: typ}
	pattern.Payload = p.parsePatternPayload()
	return pattern
}

// parsePatternPayload parses the parenthesized part a variant or type test
// binds. An absent list is not an error here: sema reports what the shape
// should have been, with the type in hand to say it with.
func (p *Parser) parsePatternPayload() []ast.Pattern {
	if !p.accept(token.LParen) {
		return nil
	}
	var payload []ast.Pattern
	for !p.at(token.RParen) && !p.atEOF() {
		payload = append(payload, p.parsePattern())
		if !p.accept(token.Comma) {
			break
		}
	}
	p.expect(token.RParen)
	return payload
}

// parseStringLit turns a string token into either a plain literal or an
// interpolated one, re-parsing each `#{...}` segment as an expression.
func (p *Parser) parseStringLit() ast.Expr {
	tok := p.next()
	if !tok.Interpolated {
		return &ast.BasicLit{Offset: tok.Offset, Kind: token.String, Value: lexer.Unescape(tok.Text)}
	}

	// bodyStart is the offset of the first character after the quote, so
	// segment offsets stay valid positions in the original file.
	bodyStart := tok.Offset + 1
	lit := &ast.StringLit{Offset: tok.Offset}
	raw := tok.Text

	literalStart := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' {
			i++
			continue
		}
		if raw[i] != '#' || i+1 >= len(raw) || raw[i+1] != '{' {
			continue
		}

		end, found := matchBrace(raw, i+1)
		if !found {
			break
		}

		if text := raw[literalStart:i]; text != "" {
			lit.Parts = append(lit.Parts, ast.StringPart{
				Offset: bodyStart + literalStart,
				Text:   lexer.Unescape(text),
			})
		}

		inner := raw[i+2 : end]
		lit.Parts = append(lit.Parts, ast.StringPart{
			Offset: bodyStart + i,
			Expr:   p.parseSubExpr(inner, bodyStart+i+2),
		})

		i = end
		literalStart = end + 1
	}

	if text := raw[literalStart:]; text != "" {
		lit.Parts = append(lit.Parts, ast.StringPart{
			Offset: bodyStart + literalStart,
			Text:   lexer.Unescape(text),
		})
	}
	return lit
}

// matchBrace returns the offset of the `}` closing the brace at start.
func matchBrace(s string, start int) (int, bool) {
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// parseSubExpr parses an interpolated segment with a nested parser. The
// segment is prefixed with a blanked-out copy of everything before it in the
// file, so token offsets, lines and columns all match the real source and
// error positions point at the actual text.
func (p *Parser) parseSubExpr(src string, offset int) ast.Expr {
	padded := blankPrefix(p.file.Src, offset) + src
	sub := &Parser{
		file:   diag.NewFile(p.file.Name, padded),
		bag:    p.bag,
		tokens: nil,
	}
	sub.tokens = lexer.Tokens(sub.file, p.bag)

	expr := sub.parseExpr()
	if !sub.atEOF() && !sub.at(token.Terminator) {
		p.errorf(sub.offset(), "unexpected %s in string interpolation", describe(sub.tok()))
	}
	return expr
}

// blankPrefix returns src[:upto] with every character except newlines
// replaced by a space, preserving line and column numbering.
func blankPrefix(src string, upto int) string {
	if upto > len(src) {
		upto = len(src)
	}
	prefix := []byte(src[:upto])
	for i := range prefix {
		if prefix[i] != '\n' {
			prefix[i] = ' '
		}
	}
	return string(prefix)
}

// stripSeparators removes the `_` digit separators from a numeric literal.
func stripSeparators(text string) string {
	if !strings.ContainsRune(text, '_') {
		return text
	}
	return strings.ReplaceAll(text, "_", "")
}
