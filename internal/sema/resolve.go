package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
)

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

// block checks a block in a fresh scope.
func (c *checker) block(block *ast.Block) {
	c.pushScope(func() {
		c.blockInPlace(block)
	})
}

// blockInPlace checks a block's statements in the current scope. Function
// bodies use it so parameters and locals share the function scope.
func (c *checker) blockInPlace(block *ast.Block) {
	for _, stmt := range block.Stmts {
		c.stmt(stmt)
	}
}

func (c *checker) stmt(stmt ast.Stmt) {
	switch s := stmt.(type) {
	case *ast.VarDecl:
		c.localVar(s)

	case *ast.Block:
		c.block(s)

	case *ast.ExprStmt:
		c.expr(s.X)

	case *ast.AssignStmt:
		for _, target := range s.Lhs {
			c.expr(target)
		}
		c.expr(s.Rhs)

	case *ast.ReturnStmt:
		for _, result := range s.Results {
			c.expr(result)
		}

	case *ast.IfStmt:
		c.expr(s.Cond)
		c.block(s.Then)
		if s.Else != nil {
			c.stmt(s.Else)
		}

	case *ast.WhileStmt:
		c.expr(s.Cond)
		c.block(s.Body)

	case *ast.ForStmt:
		// The init clause scopes over the condition, post and body.
		c.pushScope(func() {
			if s.Init != nil {
				c.stmt(s.Init)
			}
			if s.Cond != nil {
				c.expr(s.Cond)
			}
			if s.Post != nil {
				c.stmt(s.Post)
			}
			c.blockInPlace(s.Body)
		})

	case *ast.ForInStmt:
		c.expr(s.Iterable)
		c.pushScope(func() {
			c.declare(s.Name, s.Offset, symValue, s)
			c.blockInPlace(s.Body)
		})

	case *ast.DeferStmt:
		c.expr(s.Call)

	case *ast.SendStmt:
		c.expr(s.Chan)
		c.expr(s.Value)

	case *ast.SelectStmt:
		for _, arm := range s.Arms {
			// The arm's binding is visible to its own body and nowhere else.
			c.pushScope(func() {
				c.stmt(arm.Comm)
				c.stmt(arm.Body)
			})
		}
		if s.Else != nil {
			c.stmt(s.Else)
		}

	case *ast.BranchStmt:
		// Nothing to resolve.
	}
}

// localVar checks a local declaration. The initializer is resolved before
// the names are bound, so `var x = x` refers to an outer x or fails.
func (c *checker) localVar(decl *ast.VarDecl) {
	c.checkVarValue(decl)
	for _, binding := range decl.Names {
		c.declare(binding.Name, binding.Offset, symValue, decl)
	}
}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

func (c *checker) expr(expr ast.Expr) {
	switch e := expr.(type) {
	case *ast.Ident:
		c.resolveIdent(e)

	case *ast.Path:
		c.resolvePath(e)

	case *ast.SelfExpr:
		if !c.inMethod {
			c.errorf(e.Offset, "self is only available inside a method")
		}

	case *ast.BasicLit:
		// Nothing to resolve.

	case *ast.StringLit:
		for _, part := range e.Parts {
			if part.Expr != nil {
				c.expr(part.Expr)
			}
		}

	case *ast.ArrayLit:
		for _, element := range e.Elements {
			c.expr(element)
		}

	case *ast.TupleLit:
		for _, element := range e.Elements {
			c.expr(element)
		}

	case *ast.StructLit:
		c.checkType(e.Type)
		for _, field := range e.Fields {
			c.expr(field.Value)
		}

	case *ast.UnaryExpr:
		c.expr(e.X)

	case *ast.BinaryExpr:
		c.expr(e.X)
		c.expr(e.Y)

	case *ast.CallExpr:
		c.expr(e.Fun)
		for _, arg := range e.TypeArgs {
			if arg.Type != nil {
				c.checkType(arg.Type)
			} else {
				c.expr(arg.Expr)
			}
		}
		for _, arg := range e.Args {
			c.expr(arg.Value)
		}

	case *ast.SelectorExpr:
		// Only the operand resolves here; field and method names need the
		// type checker.
		c.expr(e.X)

	case *ast.TupleIndexExpr:
		c.expr(e.X)

	case *ast.IndexExpr:
		c.expr(e.X)
		c.expr(e.Index)

	case *ast.RangeExpr:
		c.expr(e.Lo)
		c.expr(e.Hi)

	case *ast.MatchExpr:
		c.expr(e.Subject)
		for _, arm := range e.Arms {
			c.pushScope(func() {
				c.pattern(arm.Pattern)
				c.stmt(arm.Body)
			})
		}

	case *ast.BlockExpr:
		c.block(e.Block)

	case *ast.AwaitExpr:
		c.expr(e.X)

	case *ast.RecvExpr:
		c.expr(e.Chan)

	case *ast.ChanLit:
		c.checkType(e.Elem)
		if e.Cap != nil {
			c.expr(e.Cap)
		}

	case *ast.ClosureExpr:
		c.closure(e)

	case *ast.CastExpr:
		c.expr(e.X)
		c.checkType(e.Type)
	}
}

func (c *checker) closure(closure *ast.ClosureExpr) {
	// A move capture takes an existing binding with it, so the name must
	// already resolve outside the closure.
	for _, capture := range closure.Captures {
		if c.scope.lookup(capture.Name) == nil {
			c.errorf(capture.Offset, "cannot move %s: no such name in scope", capture.Name)
		}
	}

	c.pushScope(func() {
		for _, param := range closure.Params {
			if param.Type != nil {
				c.checkType(param.Type)
			}
			if param.Default != nil {
				c.expr(param.Default)
			}
			c.declare(param.Name, param.Offset, symValue, closure)
		}
		for _, result := range closure.Results {
			c.checkType(result)
		}

		if closure.Body != nil {
			c.blockInPlace(closure.Body)
		}
		if closure.BodyExpr != nil {
			c.expr(closure.BodyExpr)
		}
	})
}

func (c *checker) resolveIdent(ident *ast.Ident) {
	// `_` discards and `<error>` marks parser recovery; neither is a name.
	if ident.Name == "_" || ident.Name == "<error>" {
		return
	}
	if c.scope.lookup(ident.Name) == nil {
		c.errorf(ident.Offset, "undeclared name %s; there is no universe scope, so it must be declared or imported", ident.Name)
	}
}

// resolvePath resolves the head of a `::` path. Later segments depend on
// what the head is, which only the type checker can fully decide; the one
// case checkable now is a two-segment path into a local enum.
func (c *checker) resolvePath(path *ast.Path) {
	head := path.Segments[0]
	sym := c.scope.lookup(head.Name)
	if sym == nil {
		c.errorf(head.Offset, "undeclared name %s; there is no universe scope, so it must be declared or imported", head.Name)
		return
	}

	for _, segment := range path.Segments {
		for _, arg := range segment.Args {
			c.checkType(arg)
		}
	}

	c.checkVariantSegment(path, sym)
}

// checkVariantSegment validates `Enum::Variant` when the head names an enum
// declared in this module.
func (c *checker) checkVariantSegment(path *ast.Path, head *symbol) {
	if len(path.Segments) != 2 {
		return
	}
	enum, ok := c.enums[path.Segments[0].Name]
	if !ok || head.kind != symType {
		return
	}

	name := path.Segments[1].Name
	for _, variant := range enum.Variants {
		if variant.Name == name {
			return
		}
	}
	c.errorf(path.Segments[1].Offset, "%s has no variant %s", enum.Name, name)
}

// ---------------------------------------------------------------------------
// Patterns
// ---------------------------------------------------------------------------

func (c *checker) pattern(pattern ast.Pattern) {
	switch p := pattern.(type) {
	case *ast.WildcardPattern:
		// Matches anything, binds nothing.

	case *ast.LiteralPattern:
		c.expr(p.Value)

	case *ast.BindingPattern:
		c.declare(p.Name, p.Offset, symValue, p)

	case *ast.VariantPattern:
		c.resolvePath(p.Path)
		for _, payload := range p.Payload {
			c.pattern(payload)
		}
	}
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

func (c *checker) checkType(typ ast.Type) {
	switch t := typ.(type) {
	case *ast.NamedType:
		c.checkNamedType(t)

	case *ast.PointerType:
		c.checkType(t.Elem)

	case *ast.OptionalType:
		c.checkType(t.Elem)

	case *ast.ArrayType:
		c.checkType(t.Elem)
		if t.Length != nil {
			c.expr(t.Length)
		}

	case *ast.TupleType:
		for _, element := range t.Elements {
			c.checkType(element)
		}

	case *ast.FuncType:
		for _, param := range t.Params {
			c.checkType(param)
		}
		for _, result := range t.Results {
			c.checkType(result)
		}

	case *ast.ChanType:
		c.checkType(t.Elem)
	}
}

func (c *checker) checkNamedType(typ *ast.NamedType) {
	head := typ.Path.Segments[0]

	if head.Name == "<error>" {
		return
	}
	if len(typ.Path.Segments) == 1 && builtinTypes[head.Name] {
		if len(head.Args) > 0 {
			c.errorf(head.Offset, "%s takes no type arguments", head.Name)
		}
		return
	}

	sym := c.scope.lookup(head.Name)
	if sym == nil {
		c.errorf(head.Offset, "undeclared type %s; there is no universe scope, so it must be declared or imported", head.Name)
		return
	}
	if sym.kind == symValue {
		c.errorf(head.Offset, "%s is a value, not a type", head.Name)
	}

	for _, segment := range typ.Path.Segments {
		for _, arg := range segment.Args {
			c.checkType(arg)
		}
	}
}
