package parser

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/lexer"
	"github.com/gyro-lang/gyro/internal/token"
)

func (p *Parser) parseFile() *ast.File {
	file := &ast.File{Name: p.file.Name}

	p.skipTerminators()
	for p.at(token.Import) {
		file.Imports = append(file.Imports, p.parseImport())
		p.skipTerminators()
	}

	for !p.atEOF() {
		before := p.index

		decl := p.parseDecl()
		if decl != nil {
			file.Decls = append(file.Decls, decl)
			// An impl block's methods are also declarations of their own,
			// with the block's type as owner, so that everything that
			// handles a method handles these.
			if impl, ok := decl.(*ast.ImplDecl); ok {
				for _, method := range impl.Methods {
					file.Decls = append(file.Decls, method)
				}
			}
		}
		p.skipTerminators()

		// Guarantee forward progress even when parseDecl bailed out on a
		// token it does not know how to start a declaration with.
		if p.index == before {
			p.next()
		}
	}
	return file
}

// parseImport parses every import form. A bare `import "path"` imports the
// module for its side effects only and binds nothing.
func (p *Parser) parseImport() *ast.Import {
	node := &ast.Import{Offset: p.offset()}
	p.expect(token.Import)

	// `safe` is contextual: it vouches for the names only when a list or
	// a wildcard follows, and is nothing else in this position.
	if p.at(token.Ident) && p.tok().Text == "safe" &&
		(p.peekAt(1).Kind == token.LBrace || p.peekAt(1).Kind == token.Star) {
		p.next()
		node.Safe = true
	}

	switch {
	case p.at(token.String):
		node.Path = lexer.Unescape(p.next().Text)
		p.expectTerminator()
		return node

	case p.accept(token.Star):
		p.expect(token.As)
		node.Wildcard, _ = p.expectIdent()

	case p.at(token.LBrace):
		node.Names = p.parseNameList()

	default:
		// Gyro has no default exports, so there is nothing a bare name
		// could bind to.
		p.errorf(p.offset(), "expected { or * after import; Gyro has no default exports")
		p.syncStatement()
		return node
	}

	p.expectFrom()
	node.Path = lexer.Unescape(p.expect(token.String).Text)
	p.expectTerminator()
	return node
}

// `from` is contextual, the way `move` and `copy` are: it separates an
// import list from its path and is an ordinary identifier everywhere
// else, so a method may be named `String::from`.
func (p *Parser) atFrom() bool {
	return p.at(token.Ident) && p.tok().Text == "from"
}

func (p *Parser) acceptFrom() bool {
	if !p.atFrom() {
		return false
	}
	p.next()
	return true
}

func (p *Parser) expectFrom() {
	if !p.acceptFrom() {
		p.errorf(p.offset(), "expected from after an import list")
	}
}

// parseNameList parses a braced `{ a, b as c }` list, shared by imports and
// standalone exports.
func (p *Parser) parseNameList() []ast.ImportName {
	p.expect(token.LBrace)
	p.skipTerminators()

	var names []ast.ImportName
	for !p.at(token.RBrace) && !p.atEOF() {
		name := ast.ImportName{Offset: p.offset()}
		name.Name, _ = p.expectIdent()
		if p.accept(token.As) {
			name.Alias, _ = p.expectIdent()
		}
		names = append(names, name)

		p.skipTerminators()
		if !p.accept(token.Comma) {
			break
		}
		p.skipTerminators()
	}

	p.expect(token.RBrace)
	return names
}

// parseExportDecl parses a standalone `export { ... }` or `export * from ...`.
func (p *Parser) parseExportDecl() *ast.ExportDecl {
	decl := &ast.ExportDecl{Offset: p.offset()}
	p.expect(token.Export)

	if p.accept(token.Star) {
		decl.Star = true
	} else {
		decl.Names = p.parseNameList()
	}

	// `from` is required for a star export and optional for a name list.
	if decl.Star {
		p.expectFrom()
		decl.Path = lexer.Unescape(p.expect(token.String).Text)
	} else if p.acceptFrom() {
		decl.Path = lexer.Unescape(p.expect(token.String).Text)
	}

	p.expectTerminator()
	return decl
}

// parseDecl parses one top-level declaration together with any decorators
// and the optional `export` modifier that precede it.
func (p *Parser) parseDecl() ast.Decl {
	decorators := p.parseDecorators()
	offset := p.offset()
	if len(decorators) > 0 {
		offset = decorators[0].Offset
	}

	// A standalone `export { ... }` or `export * from ...` is a declaration
	// in its own right rather than a modifier on the one that follows.
	if p.at(token.Export) && (p.peekAt(1).Kind == token.LBrace || p.peekAt(1).Kind == token.Star) {
		if len(decorators) > 0 {
			p.errorf(offset, "decorators cannot be attached to an export list")
		}
		return p.parseExportDecl()
	}

	exported := p.accept(token.Export)

	if p.foreign && p.at(token.Ident) && p.tok().Text == "union" {
		decl := p.parseUnionDecl()
		decl.Offset, decl.Decorators, decl.Exported = offset, decorators, exported
		return decl
	}
	if p.atFuncModifier() {
		fn := p.parseFuncDecl()
		fn.Offset, fn.Decorators, fn.Exported = offset, decorators, exported
		return fn
	}

	switch p.kind() {
	case token.Fn, token.Async:
		fn := p.parseFuncDecl()
		fn.Offset, fn.Decorators, fn.Exported = offset, decorators, exported
		return fn
	case token.Struct:
		decl := p.parseStructDecl()
		decl.Offset, decl.Decorators, decl.Exported = offset, decorators, exported
		return decl
	case token.Enum:
		decl := p.parseEnumDecl()
		decl.Offset, decl.Decorators, decl.Exported = offset, decorators, exported
		return decl
	case token.Trait:
		decl := p.parseTraitDecl()
		decl.Offset, decl.Decorators, decl.Exported = offset, decorators, exported
		return decl
	case token.Impl:
		decl := p.parseImplDecl()
		decl.Offset = offset
		if exported {
			p.errorf(offset, "an impl is not exported; it belongs to the type it is for")
		}
		if len(decorators) > 0 {
			p.errorf(offset, "decorators cannot be attached to an impl")
		}
		return decl
	case token.Var, token.Const:
		decl := p.parseVarDecl()
		decl.Offset, decl.Decorators, decl.Exported = offset, decorators, exported
		p.expectTerminator()
		return decl
	}

	p.errorf(p.offset(), "expected a declaration, found %s", describe(p.tok()))
	p.syncStatement()
	return nil
}

// parseDecorators parses a run of `@name` / `@name(args)` annotations.
func (p *Parser) parseDecorators() []*ast.Decorator {
	var decorators []*ast.Decorator
	for p.at(token.At) {
		decorator := &ast.Decorator{Offset: p.offset()}
		p.next()
		decorator.Name = p.parsePath()

		if p.at(token.LParen) {
			decorator.HasParens = true
			decorator.Args = p.parseArgs()
		}
		decorators = append(decorators, decorator)
		p.skipTerminators()
	}
	return decorators
}

// `unsafe` and `safe` are contextual, the way `move` and `copy` are: they
// mean something only in front of `fn` (or `async fn`) and are ordinary
// identifiers everywhere else.
func (p *Parser) atFuncModifier() bool {
	if !p.at(token.Ident) {
		return false
	}
	if word := p.tok().Text; word != "unsafe" && word != "safe" {
		return false
	}
	next := p.peekAt(1)
	if next.Kind == token.Fn || next.Kind == token.Async {
		return true
	}
	// A second modifier is still a function; it is reported once parsed.
	return next.Kind == token.Ident && (next.Text == "unsafe" || next.Text == "safe") &&
		(p.peekAt(2).Kind == token.Fn || p.peekAt(2).Kind == token.Async)
}

// parseFuncDecl parses `fn name<T>(params): results { body }` and the
// method form `fn Type::name(self, ...)`. The caller fills in the offset,
// decorators and export flag.
func (p *Parser) parseFuncDecl() *ast.FuncDecl {
	fn := &ast.FuncDecl{Offset: p.offset()}
	for p.atFuncModifier() {
		offset := p.offset()
		if p.tok().Text == "unsafe" {
			fn.Unsafe = true
		} else {
			fn.Safe = true
		}
		p.next()
		if fn.Unsafe && fn.Safe {
			p.errorf(offset, "a function is unsafe to call or vouched for, not both")
		}
	}
	fn.Async = p.accept(token.Async)
	p.expect(token.Fn)

	name, _ := p.expectIdent()
	params := p.parseTypeParams()

	// A `::` after the first name makes this a method: the name was the
	// owning type — with its parameters, as in `fn Pair<T>::swap` — and the
	// function's own name follows.
	if p.accept(token.ColonColon) {
		fn.Owner, fn.OwnerParams = name, params
		for _, param := range params {
			if len(param.Bounds) > 0 {
				p.errorf(param.Offset, "an owner's bounds are declared on the type, not on its methods")
			}
		}
		fn.Name, _ = p.expectIdent()
		fn.TypeParams = p.parseTypeParams()
	} else {
		fn.Name, fn.TypeParams = name, params
	}

	fn.Recv, fn.Params = p.parseParams()
	fn.Results = p.parseResults()
	fn.Effects = p.parseEffectRow()

	if p.at(token.LBrace) {
		fn.Body = p.parseBlock()
	} else {
		// A body-less signature is how an external or interface method is
		// written; sema decides whether that is allowed here.
		p.expectTerminator()
	}
	return fn
}

// parseTypeParams parses an optional `<T, U: Eq & Hash>` list after a
// declaration name.
func (p *Parser) parseTypeParams() []ast.TypeParam {
	if !p.at(token.Lt) {
		return nil
	}
	p.next()

	var params []ast.TypeParam
	for !p.at(token.Gt) && !p.atEOF() {
		name, offset := p.expectIdent()
		param := ast.TypeParam{Offset: offset, Name: name}

		if p.accept(token.Colon) {
			param.Bounds = append(param.Bounds, p.parseBound())
			for p.accept(token.Amp) {
				param.Bounds = append(param.Bounds, p.parseBound())
			}
		}
		params = append(params, param)

		if !p.accept(token.Comma) {
			break
		}
	}
	p.expect(token.Gt)
	return params
}

// parseBound parses one trait in a bound: a path with its arguments.
func (p *Parser) parseBound() ast.Type {
	offset := p.offset()
	if !p.at(token.Ident) {
		p.errorf(offset, "expected a trait name, found %s", describe(p.tok()))
		return &ast.NamedType{
			Offset: offset,
			Path: &ast.Path{
				Offset:   offset,
				Segments: []ast.PathSegment{{Offset: offset, Name: "<error>"}},
			},
		}
	}
	return &ast.NamedType{Offset: offset, Path: p.parseTypePathInType()}
}

// parseTraitDecl parses `trait Name<T>: Super & Super { fn method(self) ... }`.
// A method with a body is a default every implementing type shares.
func (p *Parser) parseTraitDecl() *ast.TraitDecl {
	decl := &ast.TraitDecl{Offset: p.offset()}
	p.expect(token.Trait)
	decl.Name, _ = p.expectIdent()
	decl.TypeParams = p.parseTypeParams()
	for _, param := range decl.TypeParams {
		if len(param.Bounds) > 0 {
			p.errorf(param.Offset, "a trait's parameter takes no bounds")
		}
	}

	if p.accept(token.Colon) {
		decl.Supers = append(decl.Supers, p.parseBound())
		for p.accept(token.Amp) {
			decl.Supers = append(decl.Supers, p.parseBound())
		}
	}

	p.expect(token.LBrace)
	p.skipTerminators()

	// Inside the trait, Self is the implementing type and the trait's own
	// parameters follow it, so a default body is a function generic over
	// all of them.
	owner := []ast.TypeParam{{Offset: decl.Offset, Name: "Self"}}
	owner = append(owner, decl.TypeParams...)

	for !p.at(token.RBrace) && !p.atEOF() {
		before := p.index
		if p.at(token.Fn) || p.at(token.Async) || p.atFuncModifier() {
			method := p.parseFuncDecl()
			method.Trait = decl
			if method.Owner != "" {
				p.errorf(method.Offset, "a trait's method is written without an owner: fn %s(...)", method.Name)
			}
			method.OwnerParams = owner
			decl.Methods = append(decl.Methods, method)
		} else {
			p.errorf(p.offset(), "expected a method in the trait, found %s", describe(p.tok()))
			p.syncStatement()
		}
		p.skipTerminators()
		if p.index == before {
			p.next()
		}
	}

	p.expect(token.RBrace)
	return decl
}

// parseImplDecl parses `impl Trait for Type { ... }`, and the bare
// `impl Trait for Type` that asks for the derived implementation.
func (p *Parser) parseImplDecl() *ast.ImplDecl {
	decl := &ast.ImplDecl{Offset: p.offset()}
	p.expect(token.Impl)
	decl.Trait = p.parseBound()
	p.expect(token.For)
	decl.For = p.parseType()

	owner, ownerParams := p.implOwner(decl.For)

	if !p.at(token.LBrace) {
		p.expectTerminator()
		return decl
	}
	decl.Body = true
	p.next()
	p.skipTerminators()

	for !p.at(token.RBrace) && !p.atEOF() {
		before := p.index
		if p.at(token.Fn) || p.at(token.Async) || p.atFuncModifier() {
			method := p.parseFuncDecl()
			if method.Owner != "" {
				p.errorf(method.Offset, "a method in an impl is written without an owner: fn %s(...)", method.Name)
			}
			method.Impl = decl
			method.Owner, method.OwnerParams = owner, ownerParams
			decl.Methods = append(decl.Methods, method)
		} else {
			p.errorf(p.offset(), "expected a method in the impl, found %s", describe(p.tok()))
			p.syncStatement()
		}
		p.skipTerminators()
		if p.index == before {
			p.next()
		}
	}

	p.expect(token.RBrace)
	return decl
}

// implOwner reads the type an impl is for as the owner its methods are
// attached to: the type's name, and the parameter names it is applied to.
func (p *Parser) implOwner(t ast.Type) (string, []ast.TypeParam) {
	named, ok := t.(*ast.NamedType)
	if !ok || len(named.Path.Segments) != 1 {
		p.errorf(t.Pos(), "an impl is for a type declared in this module, written by its name")
		return "<error>", nil
	}
	segment := named.Path.Segments[0]
	var params []ast.TypeParam
	for _, arg := range segment.Args {
		argNamed, ok := arg.(*ast.NamedType)
		if !ok || len(argNamed.Path.Segments) != 1 || len(argNamed.Path.Segments[0].Args) > 0 {
			p.errorf(arg.Pos(), "an impl names the type's own parameters, as in impl Eq for Pair<T>")
			continue
		}
		params = append(params, ast.TypeParam{Offset: arg.Pos(), Name: argNamed.Path.Segments[0].Name})
	}
	return segment.Name, params
}

// parseParams parses a parenthesized parameter list. A leading `self` or
// `*self` becomes the receiver rather than an ordinary parameter.
func (p *Parser) parseParams() (*ast.Receiver, []ast.Param) {
	p.expect(token.LParen)

	var recv *ast.Receiver
	var params []ast.Param

	p.skipTerminators()
	first := true
	for !p.at(token.RParen) && !p.atEOF() {
		if first && p.isReceiver() {
			recv = p.parseReceiver()
		} else {
			params = append(params, p.parseParam())
		}
		first = false

		p.skipTerminators()
		if !p.accept(token.Comma) {
			break
		}
		p.skipTerminators()
	}

	p.expect(token.RParen)
	return recv, params
}

func (p *Parser) isReceiver() bool {
	if p.at(token.Self) {
		return true
	}
	return p.at(token.Star) && p.peekAt(1).Kind == token.Self
}

func (p *Parser) parseReceiver() *ast.Receiver {
	recv := &ast.Receiver{Offset: p.offset()}
	recv.Pointer = p.accept(token.Star)
	p.expect(token.Self)
	return recv
}

func (p *Parser) parseParam() ast.Param {
	param := ast.Param{Offset: p.offset()}
	param.Name, _ = p.expectIdent()
	p.expect(token.Colon)
	param.Variadic = p.accept(token.Ellipsis)
	param.Type = p.parseType()

	if p.accept(token.Assign) {
		p.withStructLits(true, func() {
			param.Default = p.parseExpr()
		})
	}
	return param
}

// parseResults parses the `: T` or `: (T, error)` return annotation. An
// absent annotation means the function returns nothing.
func (p *Parser) parseResults() []ast.Type {
	if !p.accept(token.Colon) {
		return nil
	}

	return p.parseResultList()
}

// parseResultList parses the type list after a result colon, shared by
// declarations, function types and closures.
func (p *Parser) parseResultList() []ast.Type {
	// A parenthesized list here is a multi-value return rather than a
	// tuple-typed single value.
	if p.at(token.LParen) {
		offset := p.offset()
		p.next()
		var results []ast.Type
		for !p.at(token.RParen) && !p.atEOF() {
			results = append(results, p.parseType())
			if !p.accept(token.Comma) {
				break
			}
		}
		p.expect(token.RParen)

		// A `?` after the parens says they were grouping, because a
		// result *list* has nothing to be null. It is the one way to
		// write a nullable code pointer or function type — whose own
		// spelling ends in a result the marker would otherwise bind to
		// — and it settles the parenthesized tuple the same way.
		if p.at(token.Question) && len(results) > 0 {
			var typ ast.Type = results[0]
			if len(results) != 1 {
				typ = &ast.TupleType{Offset: offset, Elements: results}
			}
			for p.at(token.Question) {
				typ = &ast.OptionalType{Offset: p.offset(), Elem: typ}
				p.next()
			}
			return []ast.Type{typ}
		}
		return results
	}

	return []ast.Type{p.parseType()}
}

// parseEffectRow parses the effect annotation that follows a signature:
// `!{}`, `!{ io, &xs }`, `!e`, or `!{ io, ..e }`. An entry written `&xs`
// is a borrow rather than an effect, naming the parameter the result views.
func (p *Parser) parseEffectRow() *ast.EffectRow {
	if !p.at(token.Bang) {
		return nil
	}

	row := &ast.EffectRow{Offset: p.offset()}
	p.next()

	// A bare `!e` is a row variable on its own.
	if !p.at(token.LBrace) {
		row.Var, _ = p.expectIdent()
		return row
	}

	p.next()
	for !p.at(token.RBrace) && !p.atEOF() {
		if p.accept(token.DotDot) {
			row.Var, _ = p.expectIdent()
			break
		}
		p.parseEffectEntry(row)

		if !p.accept(token.Comma) {
			break
		}
	}
	p.expect(token.RBrace)
	return row
}

// parseEffectEntry reads one entry of a row: `&xs` borrows a parameter,
// anything else names an effect.
func (p *Parser) parseEffectEntry(row *ast.EffectRow) {
	if p.at(token.Amp) {
		offset := p.offset()
		p.next()
		// `self` is a keyword, and naming a method's receiver is exactly
		// what it is for here.
		if p.accept(token.Self) {
			row.Borrows = append(row.Borrows, ast.Borrow{Offset: offset, Name: "self"})
			return
		}
		name, _ := p.expectIdent()
		row.Borrows = append(row.Borrows, ast.Borrow{Offset: offset, Name: name})
		return
	}
	name, _ := p.expectIdent()
	row.Names = append(row.Names, name)
}

// parseStructDecl parses `struct Name<T> { field: T ... }`.
func (p *Parser) parseStructDecl() *ast.StructDecl {
	decl := &ast.StructDecl{Offset: p.offset()}
	p.expect(token.Struct)
	decl.Name, _ = p.expectIdent()
	decl.TypeParams = p.parseTypeParams()

	p.expect(token.LBrace)
	p.skipTerminators()

	for !p.at(token.RBrace) && !p.atEOF() {
		field := ast.Field{Offset: p.offset()}
		field.Exported = p.accept(token.Export)
		field.Name, _ = p.expectIdent()
		p.expect(token.Colon)
		field.Type = p.parseType()
		decl.Fields = append(decl.Fields, field)

		// Fields are newline separated; a comma is accepted as well.
		p.accept(token.Comma)
		p.skipTerminators()
	}

	p.expect(token.RBrace)
	return decl
}

// parseUnionDecl parses `union Name { member: T ... }`, which only a
// foreign module's generated header contains. It takes no type
// parameters: a union is C's, and C has no generic ones.
func (p *Parser) parseUnionDecl() *ast.UnionDecl {
	decl := &ast.UnionDecl{Offset: p.offset()}
	p.next() // the contextual word `union`
	decl.Name, _ = p.expectIdent()

	p.expect(token.LBrace)
	p.skipTerminators()

	for !p.at(token.RBrace) && !p.atEOF() {
		member := ast.Field{Offset: p.offset()}
		member.Exported = p.accept(token.Export)
		member.Name, _ = p.expectIdent()
		p.expect(token.Colon)
		member.Type = p.parseType()
		decl.Members = append(decl.Members, member)

		p.accept(token.Comma)
		p.skipTerminators()
	}

	p.expect(token.RBrace)
	return decl
}

// parseEnumDecl parses `enum Name<T> { Variant, Variant(T) }`.
func (p *Parser) parseEnumDecl() *ast.EnumDecl {
	decl := &ast.EnumDecl{Offset: p.offset()}
	p.expect(token.Enum)
	decl.Name, _ = p.expectIdent()
	decl.TypeParams = p.parseTypeParams()

	p.expect(token.LBrace)
	p.skipTerminators()

	for !p.at(token.RBrace) && !p.atEOF() {
		variant := ast.EnumVariant{Offset: p.offset()}
		variant.Name, _ = p.expectIdent()

		if p.accept(token.LParen) {
			for !p.at(token.RParen) && !p.atEOF() {
				variant.Payload = append(variant.Payload, p.parseType())
				if !p.accept(token.Comma) {
					break
				}
			}
			p.expect(token.RParen)
		}
		decl.Variants = append(decl.Variants, variant)

		p.accept(token.Comma)
		p.skipTerminators()
	}

	p.expect(token.RBrace)
	return decl
}

// parseVarDecl parses `var name: T = value` and `const NAME = value`, with
// the `value, err = f()` form for multi-value returns.
func (p *Parser) parseVarDecl() *ast.VarDecl {
	decl := &ast.VarDecl{Offset: p.offset()}
	decl.Const = p.at(token.Const)
	p.next()

	decl.Names = append(decl.Names, p.parseBinding())
	for p.accept(token.Comma) {
		decl.Names = append(decl.Names, p.parseBinding())
	}

	if p.accept(token.Colon) {
		decl.Type = p.parseType()
	}

	if p.accept(token.Assign) {
		p.withStructLits(true, func() {
			decl.Value = p.parseExpr()
		})
	} else if decl.Type == nil {
		p.errorf(decl.Offset, "declaration needs either a type annotation or an initializer")
	} else if decl.Const {
		p.errorf(decl.Offset, "const declaration needs an initializer")
	}

	return decl
}

func (p *Parser) parseBinding() ast.Binding {
	binding := ast.Binding{Offset: p.offset()}
	if p.at(token.Underscore) {
		binding.Blank = true
		binding.Name = "_"
		p.next()
		return binding
	}
	binding.Name, _ = p.expectIdent()
	return binding
}
