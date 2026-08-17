package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/types"
)

// Race warnings for `spawn`. A closure captures by copy, which is safe for a
// value and not for a reference: copying a `*T` copies the pointer, so both
// threads end up writing the same memory. Copying a `T[]` is the same story,
// a slice being a pointer and a length over storage it does not own.
//
// A channel is exempt: handing work between threads is what it is for. A
// `move` capture is exempt too, since the binding goes into the closure
// rather than being shared with it.
//
// This does not prove the absence of a race — a pointer reached through a
// struct field, or a second `spawn` over the same channel, is invisible here.
// It catches the direct case the language reference has always called out.

// captureSet collects the outer names a closure body reads.
type captureSet struct {
	depth int
	syms  []*symbol
	seen  map[*symbol]bool
}

// pushCaptures runs a closure body one level deeper, collecting the names it
// reaches out of its own scope for.
func (c *checker) pushCaptures(walk func()) []*symbol {
	c.closureDepth++
	c.captures = append(c.captures, &captureSet{depth: c.closureDepth, seen: map[*symbol]bool{}})

	walk()

	set := c.captures[len(c.captures)-1]
	c.captures = c.captures[:len(c.captures)-1]
	c.closureDepth--
	return set.syms
}

// noteCapture records a name used inside a closure but declared outside it.
// Every enclosing closure between the two captures it, since the value has
// to travel through each one.
func (c *checker) noteCapture(sym *symbol) {
	if sym == nil || sym.kind != symValue || sym.closureDepth >= c.closureDepth {
		return
	}
	for _, set := range c.captures {
		if set.depth <= sym.closureDepth || set.seen[sym] {
			continue
		}
		set.seen[sym] = true
		set.syms = append(set.syms, sym)
	}
}

// checkSpawnRace reports the references a spawned closure shares with the
// thread that spawned it.
func (c *checker) checkSpawnRace(call *ast.CallExpr) {
	if c.intrinsicCall(call) != "spawn" || len(call.Args) == 0 {
		return
	}
	closure, ok := call.Args[0].Value.(*ast.ClosureExpr)
	if !ok {
		return
	}

	moved := movedNames(closure)
	for _, sym := range c.closureCaptures[closure] {
		if moved[sym.name] {
			continue
		}
		shared := sharedReference(sym.typ)
		if shared == "" {
			continue
		}
		c.warnRace(call, sym.name, shared, sym.typ)
	}
}

func (c *checker) warnRace(call *ast.CallExpr, name, shared string, typ types.Type) {
	c.bag.Add(diag.Diagnostic{
		Severity: diag.SeverityWarning,
		Code:     diag.WSpawnRace,
		Pos:      c.file.Position(call.Offset),
		Message:  "the spawned closure captures " + name + ", so both threads reach the same " + shared,
		Help: []string{
			"a capture copies, and copying " + typ.String() + " copies the reference, not what it points at",
			"hand the value over instead: fn[move " + name + "]() takes the binding with it",
			"or share through a channel or a Mutex, which are built for it",
		},
	})
}

// sharedReference names what a captured type aliases, or "" when copying it
// shares nothing. A channel is left out on purpose: it is the sanctioned way
// to reach another thread.
func sharedReference(t types.Type) string {
	switch t := t.(type) {
	case *types.Pointer:
		return "memory"
	case *types.Slice:
		return "elements"
	case *types.Optional:
		return sharedReference(t.Elem)
	}
	return ""
}

func movedNames(closure *ast.ClosureExpr) map[string]bool {
	moved := map[string]bool{}
	for _, capture := range closure.Captures {
		moved[capture.Name] = true
	}
	return moved
}
