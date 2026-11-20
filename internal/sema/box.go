package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/types"
)

// A Box<T> is looked through. A field or a method asked of one is the
// boxed value's, and `&box` points at that value, the way a pointer's
// field is its target's. The box itself has nothing to say outside
// stdlib: its fields are private and it has no method a program calls.

// BoxedType is the T of a Box<T>, or nil for any other type.
func BoxedType(t types.Type) types.Type {
	strct, ok := t.(*types.Struct)
	if !ok || len(strct.Args) != 1 {
		return nil
	}
	origin := strct
	if strct.Origin != nil {
		origin = strct.Origin
	}
	if module, bundled := stdlib.NameOf(origin.Module); !bundled || module != "stdlib" || origin.Name != "Box" {
		return nil
	}
	return strct.Args[0]
}

// addressOf is the type of `&x` for an x of type operand. A box has no
// address worth taking, so `&box` is a pointer to the value inside it, and
// `&box` on a box that may be absent is a pointer that may be null.
func (c *checker) addressOf(x ast.Expr, operand types.Type) types.Type {
	if c.modulePath != stdlib.Path("stdlib") {
		if optional, ok := operand.(*types.Optional); ok {
			if elem := BoxedType(optional.Elem); elem != nil {
				c.program.info.Boxed[x] = true
				return &types.Optional{Elem: &types.Pointer{Elem: elem}}
			}
		}
	}
	return &types.Pointer{Elem: c.throughBox(x, operand)}
}

// throughBox is the type a field or method of base is looked up on: the
// value inside when base is a box, and base itself otherwise. A box that is
// looked through is recorded for lowering, which has to load the pointer
// inside it. The module that declares Box sees the box itself, since that
// is where its own fields are used.
func (c *checker) throughBox(e ast.Expr, base types.Type) types.Type {
	elem := BoxedType(base)
	if elem == nil || c.modulePath == stdlib.Path("stdlib") {
		return base
	}
	c.program.info.Boxed[e] = true
	return elem
}
