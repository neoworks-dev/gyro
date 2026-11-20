package sema

import (
	"fmt"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// Ownership. A value of an owning type (types.Owning) has exactly one
// owner, and putting it anywhere that keeps it moves it: a declaration, an
// assignment, a return, a field or element of a literal, an enum payload.
// The variable it moved out of holds nothing afterwards, which definite
// assignment already knows how to say: a move takes the variable out of the
// assigned set, and assigning it again puts it back.
//
// Everything else a value is handed to borrows it, so the checks here are
// about the positions that take.

// owningLocal makes a local of an owning type tracked by definite
// assignment, so a move can leave it unassigned.
func (c *checker) owningLocal(sym *symbol, assigned bool) {
	if sym == nil || sym.typ == nil || !types.Owning(sym.typ) {
		return
	}
	sym.needsAssign = true
	if assigned {
		c.flow.assigned[sym] = true
	}
}

// consume records that e's value is taken by whatever it is being put
// into. A variable moves; a field, an element or a borrowed parameter
// cannot be moved out of; anything else is a fresh value, which lowering
// must not destroy as a temporary.
func (c *checker) consume(e ast.Expr) {
	t := c.recordedType(e)
	if t == nil || !types.Owning(t) {
		return
	}
	c.program.info.Consumed[e] = true
	switch e := e.(type) {
	case *ast.Ident:
		c.moveName(e)
	case *ast.SelfExpr:
		c.cannotMove(e.Offset, "self", "it is borrowed by this method")
	case *ast.SelectorExpr:
		c.cannotMove(e.Offset, "a field", "the value holding it would be left without it")
	case *ast.IndexExpr:
		c.cannotMove(e.Offset, "an element", "the container would be left without it")
	case *ast.TupleIndexExpr:
		c.cannotMove(e.Offset, "a tuple element", "the tuple would be left without it")
	case *ast.UnaryExpr:
		if e.Op == token.Star {
			c.cannotMove(e.Offset, "a pointer", "the pointer does not own what it points at")
		}
	case *ast.MatchExpr:
		for _, arm := range e.Arms {
			c.consumeTail(arm.Body)
		}
	case *ast.BlockExpr:
		c.consumeTail(e.Block)
	}
}

// consumeTail takes the value a match arm or a block produces. The arm has
// already been checked on its own path, so a variable named here cannot
// be marked moved on that path any more.
func (c *checker) consumeTail(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.ExprStmt:
		if t := c.recordedType(s.X); t == nil || !types.Owning(t) {
			return
		}
		if isPlace(s.X) {
			c.errorHintf(s.X.Pos(), []string{
				"move it into a local before the match: let value = ...",
			}, "a match arm cannot move an owning value out of a variable")
			return
		}
		c.consume(s.X)
	case *ast.Block:
		if len(s.Stmts) > 0 {
			c.consumeTail(s.Stmts[len(s.Stmts)-1])
		}
	}
}

func isPlace(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident, *ast.SelfExpr, *ast.SelectorExpr, *ast.IndexExpr, *ast.TupleIndexExpr:
		return true
	case *ast.UnaryExpr:
		return e.Op == token.Star
	}
	return false
}

// moveName moves a variable's value out. Only a local this function owns
// can give its value away.
func (c *checker) moveName(e *ast.Ident) {
	sym := c.scope.lookup(e.Name)
	if sym == nil || sym.kind != symValue {
		return
	}
	switch {
	case sym.param && !sym.take:
		c.cannotMove(e.Offset, e.Name, "a parameter is borrowed from the caller")
		return
	case !sym.needsAssign:
		// A module-level value lives as long as the program; nothing
		// local may take it.
		c.cannotMove(e.Offset, e.Name, "it is declared at module level")
		return
	case c.deferDepth > 0:
		c.errorHintf(e.Offset, []string{
			"a deferred call runs at block exit, after every other use of " + e.Name,
		}, "a deferred call cannot move %s", e.Name)
		return
	}
	delete(c.flow.assigned, sym)
	c.flow.moved[sym] = e.Offset
}

func (c *checker) cannotMove(offset int, what, why string) {
	c.errorHintf(offset, []string{
		why,
		"copying an owning value would destroy it twice",
	}, "cannot move %s", what)
}

// reportMoved reports the use of a variable whose value moved out on some
// path to here.
func (c *checker) reportMoved(sym *symbol, offset int, name string) {
	moved := c.file.Position(c.flow.moved[sym])
	c.errorHintf(offset, []string{
		fmt.Sprintf("it was moved on line %d", moved.Line),
		"assign it again before using it, or clone it before the move",
	}, "%s is used after it was moved", name)
}

// checkLoopMoves reports a variable from outside a loop that the body
// moved and did not assign again before the next iteration, which would
// find it empty. Doing this at the back edge is what lets the analysis
// stay a single pass: a body that hands every such variable back leaves
// the next iteration where the first one started.
func (c *checker) checkLoopMoves(entry flow, backEdge flow) {
	if !backEdge.reachable {
		return
	}
	for sym := range entry.assigned {
		if backEdge.assigned[sym] {
			continue
		}
		offset, moved := backEdge.moved[sym]
		if !moved {
			continue
		}
		c.errorHintf(offset, []string{
			"the next iteration would find it empty",
			"assign " + sym.name + " again before the end of the loop body",
		}, "%s is moved inside a loop", sym.name)
		// Once is enough: the next back edge would say the same.
		backEdge.assigned[sym] = true
	}
}

// rejectOwning reports an owning type somewhere ownership is not followed
// yet. Refusing it is what keeps the rest sound: each of these would copy
// the value without moving it.
func (c *checker) rejectOwning(offset int, t types.Type, where string) bool {
	if t == nil || !types.Owning(t) {
		return false
	}
	c.errorHintf(offset, []string{
		"ownership is not followed through " + where + " yet",
	}, "%s is owning and cannot be used in %s", t, where)
	return true
}
