package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/types"
)

// A string literal with a #{} in it is a Format: its pieces, recorded in
// the frame of the statement that wrote it. It borrows what it names, so
// the rule that keeps it from outliving that statement is that it is only
// ever an argument: bound, returned, stored or captured, it would carry the
// borrow past the end.

// noteFormat records stdlib's Format once that module is being checked.
func (c *checker) noteFormat(tree *ast.File) {
	if c.modulePath != stdlib.Path("stdlib") {
		return
	}
	for _, decl := range tree.Decls {
		if s, ok := decl.(*ast.StructDecl); ok && s.Name == "Format" {
			c.program.format = c.structTypes[s]
		}
	}
}

func (c *checker) isFormat(t types.Type) bool {
	strct, ok := t.(*types.Struct)
	return ok && c.program.format != nil && strct == c.program.format
}

// formatLiteral types a string literal with holes in it.
func (c *checker) formatLiteral(e *ast.StringLit) types.Type {
	for _, part := range e.Parts {
		if part.Expr != nil {
			c.expr(part.Expr)
		}
	}
	if c.program.format == nil {
		c.errorf(e.Offset, "string interpolation builds a Format, which stdlib declares; this program does not load stdlib")
		return types.Unknown
	}
	return c.program.format
}

// allowFormat marks the expressions of a call that may be a format value:
// each argument, and the receiver of a method.
func (c *checker) allowFormat(call *ast.CallExpr) {
	for _, arg := range call.Args {
		c.formatOK[arg.Value] = true
	}
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
		c.formatOK[selector.X] = true
	}
}

// checkFormatPosition rejects a format value anywhere but where one may be,
// and answers the type the expression has from here on.
func (c *checker) checkFormatPosition(e ast.Expr, t types.Type) types.Type {
	if !c.isFormat(t) || c.formatOK[e] {
		return t
	}
	c.errorHintf(e.Pos(), []string{
		"to get text, build it into an allocator: format(heap(), ...) from mem",
	}, "a format value can only be passed as an argument, because it cannot outlive the statement it is written in")
	return types.Unknown
}

// noFormat rejects a Format as a field, element or result type.
func (c *checker) noFormat(offset int, t types.Type) types.Type {
	if c.isFormat(t) {
		c.errorf(offset, "a Format cannot be a field, element or result type, because it cannot outlive the statement it is written in")
		return types.Unknown
	}
	return t
}
