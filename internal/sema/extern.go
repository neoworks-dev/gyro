package sema

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// @extern is the whole of Gyro's foreign-function interface. It says that
// a declaration is provided by a C library rather than by any Gyro the
// compiler can see, which the checker already had a shape for: a
// declaration with no body is a foreign function, and that is what the
// standard library's intrinsics are.
//
// What this file adds is the two things a bodyless declaration cannot
// answer on its own — which library, and under what symbol — plus the
// boundary check. The boundary is METALS' rule, not a new one: values copy
// across it, so nothing shared and mutable may appear in a signature. The
// verifier enforces that again on the lowered import; checking it here is
// what makes the diagnostic point at the declaration a person wrote.

// checkExternFunc validates an `@extern` function and records what
// lowering needs. It runs before the body check, so a declaration with a
// body is reported once here and then checked like any other.
func (c *checker) checkExternFunc(decl *ast.FuncDecl) {
	decorator := ast.FindDecorator(decl.Decorators, "extern")
	if decorator == nil {
		return
	}
	extern, ok := c.externArgs(decorator, decl.Name)
	if !ok {
		return
	}

	switch {
	case decl.Body != nil:
		c.errorf(decl.Offset, "@extern %s has a body; a foreign function is declared, not written", decl.Name)
		return
	case decl.Owner != "" || decl.Recv != nil:
		c.errorf(decl.Offset, "@extern %s is a method; C has no receiver to bind", decl.Name)
		return
	case len(decl.TypeParams) > 0 || len(decl.OwnerParams) > 0:
		c.errorf(decl.Offset, "@extern %s is generic; a library exports one symbol, not a family", decl.Name)
		return
	case decl.Async:
		c.errorf(decl.Offset, "@extern %s is async; a foreign call is an ordinary call", decl.Name)
		return
	case len(decl.Results) > 1:
		c.errorf(decl.Offset, "@extern %s returns several values; a C function returns one", decl.Name)
		return
	}
	if decl.Effects == nil {
		c.errorHintf(decl.Offset, []string{
			"there is no body to read, so an unstated row is assumed to do everything",
		}, "@extern %s states no effect row", decl.Name)
		return
	}

	for _, param := range decl.Params {
		if param.Variadic {
			c.errorf(decl.Offset, "@extern %s is variadic; C's varargs do not have one calling convention", decl.Name)
			return
		}
		c.checkCrossing(param.Offset, c.resolveType(param.Type), "parameter "+param.Name)
	}
	for _, result := range decl.Results {
		c.checkCrossing(result.Pos(), c.resolveType(result), "result")
	}

	c.program.info.Externs[decl] = extern
}

// checkExternStruct validates the fieldless struct that stands for an
// opaque C pointer, and marks the built type so lowering makes it a
// METALS handle rather than an empty record.
func (c *checker) checkExternStruct(decl *ast.StructDecl) {
	decorator := ast.FindDecorator(decl.Decorators, "extern")
	if decorator == nil {
		return
	}
	extern, ok := c.externArgs(decorator, decl.Name)
	if !ok {
		return
	}
	if len(decl.Fields) > 0 {
		c.errorHintf(decl.Offset, []string{
			"an @extern struct is a pointer the library hands back, with nothing readable in it",
		}, "@extern struct %s declares fields", decl.Name)
		return
	}
	if len(decl.TypeParams) > 0 {
		c.errorf(decl.Offset, "@extern struct %s is generic; an opaque pointer has no parameters", decl.Name)
		return
	}
	if built := c.structTypes[decl]; built != nil {
		built.Extern = extern.Library
	}
}

// externArgs reads `("SDL2")` or `("SDL2", symbol = "SDL_Delay")`. The
// library is required and positional; the symbol defaults to the
// declaration's own name.
func (c *checker) externArgs(decorator *ast.Decorator, declared string) (types.Extern, bool) {
	extern := types.Extern{Symbol: declared}
	if len(decorator.Args) == 0 || len(decorator.Args) > 2 {
		c.errorHintf(decorator.Offset, []string{
			`@extern("SDL2") or @extern("SDL2", symbol = "SDL_Delay")`,
		}, "@extern names the library it links from")
		return extern, false
	}

	library, ok := literalString(decorator.Args[0].Value)
	if !ok || decorator.Args[0].Name != "" || library == "" {
		c.errorf(decorator.Args[0].Offset, "@extern's library is a plain string, spelled as the linker wants it: \"SDL2\" links -lSDL2")
		return extern, false
	}
	extern.Library = library

	if len(decorator.Args) == 2 {
		symbol, ok := literalString(decorator.Args[1].Value)
		if decorator.Args[1].Name != "symbol" || !ok || !isCIdentifier(symbol) {
			c.errorf(decorator.Args[1].Offset, "@extern's second argument is symbol = \"the_c_name\"")
			return extern, false
		}
		extern.Symbol = symbol
	}
	return extern, true
}

// isCIdentifier holds a symbol to what a linker can resolve. Gyro's own
// identifiers are a subset, so this only ever rejects an explicit
// `symbol =`.
func isCIdentifier(name string) bool {
	for i, r := range name {
		digit := r >= '0' && r <= '9'
		letter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !letter && !(digit && i > 0) {
			return false
		}
	}
	return name != ""
}

// literalString reads a plain string literal. An interpolated one is a
// StringLit and never reaches here, which is the answer that wants: a
// library name is not computed.
func literalString(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.String {
		return "", false
	}
	return lit.Value, true
}

// checkCrossing rejects a type that cannot travel to C. What crosses is
// what C has a representation for: scalars, addresses, and aggregates
// laid out the way C lays them out. What does not is a Gyro runtime
// object, which C has no spelling for at all.
//
// A linked library is not a second heap — it runs on this thread over
// this allocator — so an address crosses as itself and an out-parameter
// is the pointer C declared. That is a narrower boundary than a runtime
// with a collector, and METALS draws the same line
// ([appendix D](appendix-d-metals.md)).
func (c *checker) checkCrossing(offset int, t types.Type, what string) {
	if bad := crossingOffender(t); bad != "" {
		c.errorHintf(offset, []string{
			"a foreign boundary carries what C can represent: primitives, string, a slice, a pointer, anyptr, a code pointer, a struct of members that themselves cross, and an @extern struct as an opaque pointer",
		}, "%s cannot cross to C: %s", what, bad)
	}
}

// crossingOffender names the part of a type that cannot cross, or "".
func crossingOffender(t types.Type) string {
	return offenderIn(t, map[*types.Struct]bool{}, false)
}

// offenderIn does the walking. inStruct says whether the type is a
// member of a struct crossing by value rather than an argument in its own
// right, which is a narrower question: a member is bytes C reads out of
// the struct, so `string` — a pointer to a length-prefixed header — is
// out even though a `string` argument is fine, since that one is copied
// into a C string for the call and a member could not be.
func offenderIn(t types.Type, seen map[*types.Struct]bool, inStruct bool) string {
	switch t := t.(type) {
	case *types.Basic:
		switch t.Kind {
		case types.Invalid:
			// Already reported as an unresolved type; saying it twice
			// would only bury the first diagnostic.
			return ""
		case types.ErrorKind:
			return "error is a Gyro value, not a C one"
		case types.AnyKind:
			return "any carries a Gyro type tag C cannot read"
		case types.AnyPtrKind:
			// It is still this program's address space — which is exactly
			// what a linked library shares, and `void *` is what half of
			// C's interfaces are written over.
			return ""
		case types.StringKind:
			if inStruct {
				return "a string field is a pointer to Gyro's own header, which C cannot read"
			}
		}
		return ""
	case *types.Struct:
		if t.Extern != "" {
			return "" // an opaque handle, which crosses as the pointer it is
		}
		if seen[t] {
			// A struct reaching itself has no by-value form in C either;
			// the field that does it is a pointer, and that is reported.
			return ""
		}
		seen[t] = true
		for _, field := range t.Fields {
			if bad := offenderIn(field.Type, seen, true); bad != "" {
				return bad
			}
		}
		return ""
	case *types.Enum:
		return t.Name + " is an enum, which is a tag and a payload C cannot read"
	case *types.Optional:
		// A null pointer is the one absence C has, so an opaque handle
		// may be nullable and nothing else may — and even that only where
		// a pointer is what crosses, not inside a struct, where Gyro
		// keeps a flag beside the payload.
		strct, ok := t.Elem.(*types.Struct)
		if ok && strct.Extern != "" && !inStruct {
			return ""
		}
		if inStruct {
			return "a nullable field is a flag and a payload, which is not a C member"
		}
		return "only an @extern struct may be nullable; null is C's null pointer and nothing else"
	case *types.Slice:
		if inStruct {
			return "a slice field is a pointer and a length, which is not a C member"
		}
		if bad := offenderIn(t.Elem, seen, true); bad != "" {
			return bad
		}
		if _, ok := t.Elem.(*types.Basic); !ok {
			return "a slice crosses as a pointer to its elements, which only works for scalars"
		}
		return ""
	case *types.Pointer:
		// A linked library dereferences this address on this thread, in
		// this address space, over the allocator the runtime itself
		// calls. There is no second heap to copy into, so an
		// out-parameter is the pointer C declared and nothing else.
		return offenderIn(t.Elem, seen, true)
	case *types.Chan:
		return "a channel is a Gyro runtime object"
	case *types.Future:
		return "a future is a Gyro runtime object"
	case *types.Func:
		return "a fn is code and a captured environment, and a C callback has one word for the two; pass &f, which is a code pointer"
	case *types.Code:
		// A code pointer is the address of a function this program
		// emitted, which is the same kind of address a `*T` is.
		for _, param := range t.Params {
			if bad := offenderIn(param, seen, false); bad != "" {
				return bad
			}
		}
		if t.Result != nil {
			return offenderIn(t.Result, seen, false)
		}
		return ""
	case *types.Tuple:
		return "a tuple is a Gyro layout; return one value or take an out-parameter"
	case *types.Array:
		// Inside a struct a fixed array is C's own `T v[N]`, so it is a
		// member like any other. On its own it is not what C takes.
		if inStruct {
			return offenderIn(t.Elem, seen, true)
		}
		return "a fixed-size array is a Gyro layout; pass a slice of it"
	}
	return ""
}
