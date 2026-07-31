package parser

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
)

func (p *Parser) parseBlock() *ast.Block {
	block := &ast.Block{Offset: p.offset()}
	p.expect(token.LBrace)

	// A block body is always unambiguous, so struct literals are legal
	// again even when the enclosing header disabled them.
	p.withStructLits(true, func() {
		p.skipTerminators()
		for !p.at(token.RBrace) && !p.atEOF() {
			before := p.index

			stmt := p.parseStmt()
			if stmt != nil {
				block.Stmts = append(block.Stmts, stmt)
			}
			p.skipTerminators()

			if p.index == before {
				p.next()
			}
		}
	})

	p.expect(token.RBrace)
	return block
}

func (p *Parser) parseStmt() ast.Stmt {
	switch p.kind() {
	case token.Var, token.Const:
		decl := p.parseVarDecl()
		p.expectTerminator()
		return decl
	case token.Return:
		return p.parseReturn()
	case token.If:
		return p.parseIf()
	case token.While:
		return p.parseWhile()
	case token.For:
		return p.parseFor()
	case token.Defer:
		return p.parseDefer()
	case token.Select:
		return p.parseSelect()
	case token.Break, token.Continue:
		stmt := &ast.BranchStmt{Offset: p.offset(), Kind: p.kind()}
		p.next()
		p.expectTerminator()
		return stmt
	case token.LBrace:
		return p.parseBlock()
	}

	return p.parseSimpleStmt(true)
}

// parseSimpleStmt parses an expression, assignment or compound assignment.
// terminate is false inside a `for` header, where the clause is ended by a
// semicolon or brace that the caller consumes.
func (p *Parser) parseSimpleStmt(terminate bool) ast.Stmt {
	offset := p.offset()

	// Stop above comparison precedence so that the operators which only
	// matter at statement level are still unconsumed: `<-` would otherwise
	// be split into a comparison, and `>>=` would be eaten as a `>`.
	first := p.parseBinary(precShift)

	// `ch <- value` only ever appears as a statement, which is what makes
	// the token unambiguous here but a comparison inside an expression.
	if p.at(token.LeftArrow) {
		stmt := &ast.SendStmt{Offset: offset, Chan: first}
		p.next()
		p.withStructLits(true, func() {
			stmt.Value = p.parseExpr()
		})
		if terminate {
			p.expectTerminator()
		}
		return stmt
	}

	lhs := []ast.Expr{first}
	for p.accept(token.Comma) {
		lhs = append(lhs, p.parseBinary(precShift))
	}

	if op, ok := p.assignOp(); ok {
		stmt := &ast.AssignStmt{Offset: offset, Op: op, Lhs: lhs}
		p.withStructLits(true, func() {
			stmt.Rhs = p.parseExpr()
		})
		if terminate {
			p.expectTerminator()
		}
		return stmt
	}

	if len(lhs) > 1 {
		p.errorf(offset, "expected an assignment after a comma-separated target list")
	}

	// Not a send and not an assignment, so finish parsing the expression at
	// the precedence levels that were held back above.
	expr := p.parseBinaryFrom(first, precRange)
	if terminate {
		p.expectTerminator()
	}
	return &ast.ExprStmt{X: expr}
}

// assignOp consumes an assignment operator when one is present. `>>=` is
// spelled as adjacent `>` and `>=` tokens because the lexer never merges
// consecutive `>`, so that nested generics can close cleanly.
func (p *Parser) assignOp() (token.Kind, bool) {
	switch p.kind() {
	case token.Assign, token.PlusEq, token.MinusEq, token.StarEq, token.SlashEq,
		token.PercentEq, token.AmpEq, token.PipeEq, token.CaretEq, token.ShlEq:
		op := p.kind()
		p.next()
		return op, true
	case token.Gt:
		if p.peekAt(1).Kind == token.GtEq && p.adjacent(0, 1) {
			p.next()
			p.next()
			return token.ShrEq, true
		}
	}
	return token.Invalid, false
}

// adjacent reports whether the tokens at the two offsets sit next to each
// other in the source with no space between them.
func (p *Parser) adjacent(i, j int) bool {
	left, right := p.peekAt(i), p.peekAt(j)
	return left.Offset+len(left.Kind.String()) == right.Offset
}

func (p *Parser) parseReturn() ast.Stmt {
	stmt := &ast.ReturnStmt{Offset: p.offset()}
	p.expect(token.Return)

	if !p.at(token.Terminator) && !p.at(token.RBrace) && !p.atEOF() {
		stmt.Results = append(stmt.Results, p.parseExpr())
		for p.accept(token.Comma) {
			stmt.Results = append(stmt.Results, p.parseExpr())
		}
	}

	p.expectTerminator()
	return stmt
}

func (p *Parser) parseDefer() ast.Stmt {
	stmt := &ast.DeferStmt{Offset: p.offset()}
	p.expect(token.Defer)
	stmt.Call = p.parseExpr()

	if _, isCall := stmt.Call.(*ast.CallExpr); !isCall {
		p.errorf(stmt.Offset, "defer needs a function call")
	}

	p.expectTerminator()
	return stmt
}

// parseIf parses `if cond { ... }` with an optional `elif` / `else` tail.
// `elif` is desugared into a nested if, so it leaves no trace in the tree.
func (p *Parser) parseIf() ast.Stmt {
	stmt := &ast.IfStmt{Offset: p.offset()}
	p.next() // `if` or `elif`

	p.withStructLits(false, func() {
		stmt.Cond = p.parseExpr()
	})
	stmt.Then = p.parseBlock()

	// A terminator was synthesized after the closing brace; an else on the
	// next line is still part of this statement.
	saved := p.index
	p.skipTerminators()

	switch p.kind() {
	case token.Elif:
		stmt.Else = p.parseIf()
	case token.Else:
		p.next()
		// `else if` is not accepted: the chained form is spelled `elif`, so
		// there is exactly one way to write it.
		if p.at(token.If) {
			p.errorf(p.offset(), "use `elif` to chain conditions; `else if` is not valid Gyro")
			stmt.Else = p.parseIf()
			return stmt
		}
		stmt.Else = p.parseBlock()
		p.expectTerminator()
	default:
		p.index = saved
		p.expectTerminator()
	}

	return stmt
}

func (p *Parser) parseWhile() ast.Stmt {
	stmt := &ast.WhileStmt{Offset: p.offset()}
	p.expect(token.While)

	p.withStructLits(false, func() {
		stmt.Cond = p.parseExpr()
	})
	stmt.Body = p.parseBlock()
	p.expectTerminator()
	return stmt
}

// parseSelect parses `select { comm: body, ... }`. Arms use the same
// `comm: body` shape as match arms, and a binding arm is written with an
// ordinary `var` declaration rather than a dedicated short form.
func (p *Parser) parseSelect() ast.Stmt {
	stmt := &ast.SelectStmt{Offset: p.offset()}
	p.expect(token.Select)
	p.expect(token.LBrace)

	p.withStructLits(true, func() {
		p.skipTerminators()
		for !p.at(token.RBrace) && !p.atEOF() {
			before := p.index

			if p.at(token.Else) {
				p.next()
				p.expect(token.Colon)
				stmt.Else = p.parseArmBody()
			} else {
				arm := ast.SelectArm{Offset: p.offset()}
				arm.Comm = p.parseSelectComm()
				p.expect(token.Colon)
				arm.Body = p.parseArmBody()
				stmt.Arms = append(stmt.Arms, arm)
			}

			p.skipTerminators()
			p.accept(token.Comma)
			p.skipTerminators()

			if p.index == before {
				p.next()
			}
		}
	})

	p.expect(token.RBrace)
	p.expectTerminator()
	return stmt
}

// parseSelectComm parses the channel operation on the left of a select arm.
func (p *Parser) parseSelectComm() ast.Stmt {
	if p.at(token.Var) || p.at(token.Const) {
		return p.parseVarDecl()
	}
	return p.parseSimpleStmt(false)
}

// parseArmBody parses the right-hand side of a match or select arm: a block,
// a control-flow statement, or a single expression.
//
// An arm is delimited by the comma that separates it from the next one, so a
// `return` here yields at most one value. Returning several needs a block.
func (p *Parser) parseArmBody() ast.Stmt {
	switch p.kind() {
	case token.LBrace:
		return p.parseBlock()

	case token.Return:
		stmt := &ast.ReturnStmt{Offset: p.offset()}
		p.next()
		if !p.atArmEnd() {
			stmt.Results = append(stmt.Results, p.parseExpr())
		}
		return stmt

	case token.Break, token.Continue:
		stmt := &ast.BranchStmt{Offset: p.offset(), Kind: p.kind()}
		p.next()
		return stmt
	}

	return &ast.ExprStmt{X: p.parseExpr()}
}

// atArmEnd reports whether the current token closes an arm body.
func (p *Parser) atArmEnd() bool {
	switch p.kind() {
	case token.Comma, token.RBrace, token.Terminator, token.EOF:
		return true
	}
	return false
}

// parseFor parses both the C-style `for init; cond; post { ... }` and the
// iterator form `for name in iterable { ... }`.
func (p *Parser) parseFor() ast.Stmt {
	offset := p.offset()
	p.expect(token.For)

	if p.at(token.Ident) && p.peekAt(1).Kind == token.In {
		stmt := &ast.ForInStmt{Offset: offset}
		stmt.Name, _ = p.expectIdent()
		p.expect(token.In)
		p.withStructLits(false, func() {
			stmt.Iterable = p.parseExpr()
		})
		stmt.Body = p.parseBlock()
		p.expectTerminator()
		return stmt
	}

	stmt := &ast.ForStmt{Offset: offset}
	p.withStructLits(false, func() {
		stmt.Init = p.parseForClause()
		p.expect(token.Terminator)

		if !p.at(token.Terminator) {
			stmt.Cond = p.parseExpr()
		}
		p.expect(token.Terminator)

		if !p.at(token.LBrace) {
			stmt.Post = p.parseForClause()
		}
	})

	stmt.Body = p.parseBlock()
	p.expectTerminator()
	return stmt
}

// parseForClause parses the init or post clause of a C-style for header,
// neither of which is terminated by a newline.
func (p *Parser) parseForClause() ast.Stmt {
	switch p.kind() {
	case token.Terminator, token.LBrace:
		return nil
	case token.Var, token.Const:
		return p.parseVarDecl()
	default:
		return p.parseSimpleStmt(false)
	}
}
