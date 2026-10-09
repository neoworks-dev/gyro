package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// Extension methods. `fn string::trim(self): string` attaches a method to
// a built-in type from ordinary Gyro, which is what lets the standard
// library spell an operation on a string the way the compiler's own
// surface does — `line.trim()` rather than `trim(line)`.
//
// A built-in type has no declaration to hang methods off, so an extension
// is an ordinary module-scope name under its method name. That is the
// whole visibility rule: it is callable where the name is in scope, and
// exported and imported like anything else. Two modules may both extend
// `string` with `trim`; importing both is the collision Gyro already
// reports, and importing one is unambiguous.
//
// The compiler's own surface — len, runes, bytes, close, compare,
// message — stays import-free. Those are part of the type rather than
// additions to it, and they win over an extension of the same name.

// builtinOwner resolves the type an extension method names, or nil when
// the name is not an extensible built-in. `void`, `error` and `any` are
// excluded: nothing has type void, error is a contract satisfied by
// whatever carries message() rather than a type to hang methods on, and an
// any's behaviour belongs to the type inside it — which a match names.
func builtinOwner(name string) types.Type {
	if name == "void" || name == "error" || name == "any" {
		return nil
	}
	if basic, ok := types.Builtin[name]; ok {
		return basic
	}
	return nil
}

// extendsBuiltin reports whether a declaration is an extension method.
func extendsBuiltin(decl *ast.FuncDecl) bool {
	return decl.Owner != "" && decl.Recv != nil && builtinOwner(decl.Owner) != nil
}

// extensionMethod finds an extension method for t named `name` among the
// names in scope. The declaration's own owner is what decides which type
// it applies to, so an extension of `i32` never answers for a string.
func (c *checker) extensionMethod(t types.Type, name string) *types.Method {
	sym := c.scope.lookup(name)
	if sym == nil || sym.kind != symValue {
		return nil
	}
	decl, ok := sym.decl.(*ast.FuncDecl)
	if !ok || !extendsBuiltin(decl) {
		return nil
	}
	// An untyped literal receiver takes its default type, the same way it
	// would in any other position: 4.double() is i32's.
	if !types.Identical(builtinOwner(decl.Owner), types.Default(t)) {
		return nil
	}
	// The signature was built by the declaring module's checker, which is
	// a different one whenever the extension was imported.
	if env := c.program.envs[decl]; env != nil {
		return env.funcSigs[decl]
	}
	return c.funcSigs[decl]
}
