package lower

import (
	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/types"
)

// Module-level `var` is the one thing in Gyro that outlives every frame.
// It lowers to METALS storage, which is zero-initialized; the initializer
// runs in a synthesized function called before main's own body, in
// declaration order across modules in dependency order.
//
// `const` does not come through here. A const has no identity to share,
// so it stays a getter — see moduleValue.

// moduleGlobal is one lowered module-level var.
type moduleGlobal struct {
	name string
	decl *ast.VarDecl
	// binding is the declared name, which is the var's own name; a
	// destructuring module-level var does not parse.
	binding string
	typ     types.Type
}

const globalInitName = "__init_globals"

// collectGlobals registers storage for every module-level var, in the
// order the program declares them. Doing it up front rather than on first
// use is what makes the initialization order the source's rather than the
// order the lowering happened to reach them in.
func (l *lowerer) collectGlobals() {
	for _, module := range l.prog.Modules {
		for _, decl := range module.Tree.Decls {
			varDecl, ok := decl.(*ast.VarDecl)
			if !ok || varDecl.Const {
				continue
			}
			l.registerGlobal(varDecl)
		}
	}
}

func (l *lowerer) registerGlobal(decl *ast.VarDecl) {
	for _, binding := range decl.Names {
		if binding.Blank {
			continue
		}
		name := binding.Name
		declared := l.info.Locals[decl][name]
		if declared == nil {
			l.fail("module-level var %s has no recorded type", name)
		}
		qualified := l.globalName(decl, name)
		l.globals = append(l.globals, moduleGlobal{
			name:    qualified,
			decl:    decl,
			binding: name,
			typ:     declared,
		})
		l.globalOf[decl] = append(l.globalOf[decl], len(l.globals)-1)
		l.module.Globals = append(l.module.Globals, &metals.Global{
			Name: qualified,
			Type: l.metalsType(declared),
		})
	}
}

func (l *lowerer) globalName(decl *ast.VarDecl, name string) string {
	if module := l.moduleOf[decl]; module != nil {
		if tag := l.tags[module.Path]; tag != "" {
			return tag + "::" + name
		}
	}
	return name
}

// globalPlace reads and writes module storage through the ordinary
// pointer place, so assignment, compound assignment and &x all work
// without knowing a global is involved.
func (f *funcLowerer) globalPlace(decl *ast.VarDecl, name string) *place {
	for _, index := range f.l.globalOf[decl] {
		global := f.l.globals[index]
		if global.binding != name {
			continue
		}
		ptr := f.newReg(name + ".p")
		f.emit(&metals.GlobalPtr{Dst: ptr, Name: global.name})
		return f.pointerPlace(ptr, global.typ)
	}
	f.l.fail("@%s: no storage for the module-level var %s", f.fn.Name, name)
	return nil
}

// emitGlobalInit builds the initializer function and names it as the
// module's. It runs after the rest is lowered, so an initializer may
// call anything the program defines.
func (l *lowerer) emitGlobalInit() {
	if len(l.globals) == 0 {
		return
	}

	f := &funcLowerer{
		l:        l,
		fn:       &metals.Func{Name: globalInitName},
		regCount: map[string]int{},
	}
	f.addrTaken = map[string]bool{}
	f.blocks = []*[]metals.Instr{&f.fn.Body}
	f.pushScope()

	for _, global := range l.globals {
		if global.decl.Value == nil {
			continue
		}
		value := f.lowerExpr(global.decl.Value, global.typ)
		f.globalPlace(global.decl, global.binding).store(value)
	}
	f.emit(&metals.Return{})
	f.popScopeNoDefers()

	l.module.Funcs = append(l.module.Funcs, f.fn)
	l.drain()

	// The call is the linker's to place: a program is several modules
	// often enough, and every one of them wants its storage written
	// before main in the order they depend on each other.
	l.module.Init = globalInitName
}
