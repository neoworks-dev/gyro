package cc

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// C to METALS.
//
// Two mappings carry most of the weight and are worth stating before the
// code that implements them.
//
// **A C pointer is `opt<ptr<T>>`.** METALS' `ptr<T>` is never null and C's
// pointer is null half the time, so the nullable type is the honest one.
// It costs an `opt.unwrap` at every dereference and buys exactly what
// compiling C in rather than linking it is for: `*p` on a null pointer
// aborts with a message instead of being undefined. `&x` produces a
// `ptr<T>` and is wrapped; a null pointer constant is `const.null`.
//
// **Every local lives in a slot.** A C local's address may be taken, and
// deciding which ones escape is an analysis this front end does not need
// to have an opinion about yet: `slot` is a stack cell, so the version
// that always uses one is correct and the one that promotes to registers
// is an optimization the backends are better placed to do anyway.
//
// C's arithmetic wraps here — `wadd` and not `iadd`. Signed overflow is
// undefined in C, so either would be a defensible reading, but the wasm
// bridge translates the same program to the wrapping mnemonics, and
// docs/decisions.md holds the two producers to the same answers.

type lowerer struct {
	out     *metals.Module
	funcs   map[string]*funcDecl
	globals map[string]*ctype
	enums   map[string]int64
	// strings interns string literals: two identical ones are one array
	// in module storage, which is what every C compiler does.
	strings map[string]string
	// pending holds the globals whose initializers name an address, and
	// so become stores in the init function rather than a byte image. A
	// static local joins it while its function is being lowered, which is
	// why it lives here and not in a local variable.
	pending []*globalDecl
	// statics records the symbols static locals have taken, so two blocks
	// of one function may each declare the same name.
	statics map[string]bool
	// layout answers sizeof. The arithmetic is METALS' and belongs to no
	// producer: a second implementation here would agree with the
	// backends' until the first struct that pads, and then the two would
	// be measuring which one guessed.
	layout *metals.Layout
}

type failure struct{ err error }

func fail(pos position, format string, args ...any) {
	panic(failure{errorAt(pos, format, args...)})
}

func lower(name string, u *unit) (module *metals.Module, err error) {
	defer func() {
		if r := recover(); r != nil {
			caught, ok := r.(failure)
			if !ok {
				panic(r)
			}
			module, err = nil, caught.err
		}
	}()

	l := &lowerer{
		out:     &metals.Module{Name: name},
		funcs:   map[string]*funcDecl{},
		globals: map[string]*ctype{},
		strings: map[string]string{},
		statics: map[string]bool{},
		enums:   u.enums,
	}

	for _, s := range u.structs {
		if s.fields == nil {
			fail(position{file: name}, "struct %s is used but never defined", s.tag)
		}
		decl := &metals.TypeDecl{Name: s.tag, Record: &metals.RecordShape{}}
		for _, f := range s.fields {
			decl.Record.Fields = append(decl.Record.Fields,
				metals.FieldDecl{Name: f.name, Type: f.typ.metalsType()})
		}
		l.out.Types = append(l.out.Types, decl)
	}

	// The layout is built once the records are declared and before any
	// body is lowered, which is the order sizeof needs.
	l.layout = metals.NewLayout(l.out, func(format string, args ...any) {
		fail(position{file: name}, format, args...)
	})

	// A global with an initializer whose value is already known becomes
	// the bytes it starts with; one that names an address becomes stores
	// in the init function, which is emitted last so an initializer may
	// call anything the unit defines.
	for _, g := range u.globals {
		if _, dup := l.globals[g.name]; dup {
			fail(g.pos, "%s is declared twice", g.name)
		}
		l.declareGlobal(g)
		l.globals[g.name] = g.typ
	}

	for _, fn := range u.funcs {
		if seen, dup := l.funcs[fn.name]; dup {
			if seen.body != nil && fn.body != nil {
				fail(fn.pos, "%s is defined twice", fn.name)
			}
			if !sameSignature(seen, fn) {
				fail(fn.pos, "%s is declared with two different signatures", fn.name)
			}
			if fn.body != nil {
				l.funcs[fn.name] = fn
			}
			continue
		}
		l.funcs[fn.name] = fn
	}

	for _, fn := range u.funcs {
		if fn.body == nil {
			continue
		}
		l.out.Funcs = append(l.out.Funcs, l.function(fn))
	}
	l.emitInit(l.pending)
	return l.out, nil
}

// declareGlobal reserves module storage and decides how it starts: as
// the bytes an already-known value has, or as stores in the init
// function when the initializer names an address. g.name is the METALS
// symbol, which for a static local is not the name C wrote.
func (l *lowerer) declareGlobal(g *globalDecl) {
	if !g.typ.isComplete() {
		// `int t[] = {1, 2, 3}` takes its length from the initializer,
		// which is the one declaration C lets the right side size.
		g.typ = l.sizeFromInit(g)
	}

	global := &metals.Global{Name: g.name, Type: g.typ.metalsType()}
	if g.init != nil {
		if image, ok := l.staticImage(g.typ, g.init); ok {
			global.Bytes = image
		} else {
			l.pending = append(l.pending, g)
		}
	}
	l.out.Globals = append(l.out.Globals, global)
}

// staticSymbol names a static local's storage. C's own identifiers have
// no `$`, so nothing a translation unit declares can collide with one,
// and the counter separates two blocks of one function that each declare
// the same name.
func (l *lowerer) staticSymbol(fn, name string) string {
	base := fn + "$" + name
	if !l.statics[base] {
		l.statics[base] = true
		return base
	}
	for n := 1; ; n++ {
		candidate := base + "." + strconv.Itoa(n)
		if !l.statics[candidate] {
			l.statics[candidate] = true
			return candidate
		}
	}
}

// sizeFromInit gives an array declared without a length the one its
// initializer implies: the number of elements braced, or the length of
// the string including its terminator.
func (l *lowerer) sizeFromInit(g *globalDecl) *ctype {
	if !g.typ.isArray() || g.typ.length >= 0 || g.init == nil {
		fail(g.pos, "%s is %s, which has no size here", g.name, g.typ)
	}
	if g.init.braced() {
		return arrayOf(g.typ.elem, int64(len(g.init.list)))
	}
	if text, ok := g.init.x.(*strLit); ok && g.typ.elem.isCharacter() {
		return arrayOf(g.typ.elem, int64(len(text.text)))
	}
	fail(g.pos, "%s is %s, and this initializer does not say how long", g.name, g.typ)
	return nil
}

func sameSignature(a, b *funcDecl) bool {
	if !a.result.equals(b.result) || len(a.params) != len(b.params) {
		return false
	}
	for i := range a.params {
		if !a.params[i].typ.equals(b.params[i].typ) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// One function
// ---------------------------------------------------------------------------

// value is an expression's result: the register holding it and the C
// type it has. An lvalue's register holds a ptr<T> and its type is the
// object's, not the pointer's.
type value struct {
	reg string
	typ *ctype
}

type binding struct {
	// ptr is the register holding the ptr<T> to the variable's storage.
	ptr string
	typ *ctype
	// symbol names module storage when the variable lives there, which
	// is a file-scope declaration or a `static` local. It is not always
	// the C name: a static local's is qualified by the function it is
	// written in, because two of them may share one.
	symbol string
}

func (b *binding) global() bool { return b.symbol != "" }

type loop struct {
	brk  string
	cont string
}

type funcLowerer struct {
	l   *lowerer
	fn  *funcDecl
	out *metals.Func

	// blocks is the instruction stack: the innermost structured body is
	// on top, so emitting inside an if or a loop needs no threading.
	blocks [][]metals.Instr
	// scopes are the block-scoped names, innermost last.
	scopes []map[string]*binding
	loops  []loop

	names map[string]bool
	n     int
}

func (l *lowerer) function(fn *funcDecl) *metals.Func {
	f := &funcLowerer{
		l:     l,
		fn:    fn,
		out:   &metals.Func{Name: fn.name, Export: !fn.static},
		names: map[string]bool{},
	}
	f.blocks = [][]metals.Instr{nil}
	f.scopes = []map[string]*binding{{}}

	if !fn.result.isVoid() {
		f.out.Result = fn.result.metalsType()
	}

	// A parameter arrives in a register and C may take its address or
	// assign to it, so each one is copied into a cell of its own.
	for _, p := range fn.params {
		incoming := f.reserve(p.name + ".in")
		f.out.Params = append(f.out.Params, metals.Param{Name: incoming, Type: p.typ.metalsType()})
		cell := f.reserve(p.name)
		f.emit(&metals.Slot{Dst: cell, Type: p.typ.metalsType()})
		f.emit(&metals.Store{Ptr: cell, Val: incoming})
		f.scopes[0][p.name] = &binding{ptr: cell, typ: p.typ}
	}

	f.stmt(fn.body)

	// Falling off the end of a C function that returns something is
	// undefined, and an abort is the reading that says so in every tier
	// rather than handing back whatever a register held.
	if fn.result.isVoid() {
		f.emit(&metals.Return{})
	} else {
		f.emit(&metals.Abort{Message: "control reached the end of " + fn.name + " without a return"})
	}

	f.out.Body = f.blocks[0]
	return f.out
}

// ---------------------------------------------------------------------------
// Emission, names and scopes
// ---------------------------------------------------------------------------

func (f *funcLowerer) emit(i metals.Instr) {
	top := len(f.blocks) - 1
	f.blocks[top] = append(f.blocks[top], i)
}

func (f *funcLowerer) open() { f.blocks = append(f.blocks, nil) }
func (f *funcLowerer) close() []metals.Instr {
	top := len(f.blocks) - 1
	body := f.blocks[top]
	f.blocks = f.blocks[:top]
	return body
}

// reserve takes a register name, suffixing it when C's scoping has
// already used it. The suffix is visible on purpose: two `i`s in two
// blocks read as `%i` and `%"i.1"`, which is what they are.
func (f *funcLowerer) reserve(desired string) string {
	if !f.names[desired] {
		f.names[desired] = true
		return desired
	}
	for n := 1; ; n++ {
		candidate := desired + "." + strconv.Itoa(n)
		if !f.names[candidate] {
			f.names[candidate] = true
			return candidate
		}
	}
}

func (f *funcLowerer) temp() string {
	f.n++
	return f.reserve("t" + strconv.Itoa(f.n))
}

func (f *funcLowerer) label(kind string) string {
	f.n++
	return kind + strconv.Itoa(f.n)
}

func (f *funcLowerer) pushScope() { f.scopes = append(f.scopes, map[string]*binding{}) }
func (f *funcLowerer) popScope()  { f.scopes = f.scopes[:len(f.scopes)-1] }

func (f *funcLowerer) lookup(name string) *binding {
	for i := len(f.scopes) - 1; i >= 0; i-- {
		if b, ok := f.scopes[i][name]; ok {
			return b
		}
	}
	if typ, ok := f.l.globals[name]; ok {
		return &binding{typ: typ, symbol: name}
	}
	return nil
}

// address produces the ptr<T> a binding's storage is reached through. A
// global's is an instruction rather than a register held from the start,
// since global.ptr is cheap and holding one would pin it across a call.
func (f *funcLowerer) address(b *binding) string {
	if !b.global() {
		return b.ptr
	}
	reg := f.temp()
	f.emit(&metals.GlobalPtr{Dst: reg, Name: b.symbol})
	return reg
}

func (f *funcLowerer) constant(typ *ctype, text string) string {
	reg := f.temp()
	f.emit(&metals.Const{Dst: reg, Type: typ.metalsType(), Value: text})
	return reg
}

// zero is the constant a comparison against nothing needs: 0 for a
// number, a null for a pointer.
func (f *funcLowerer) zero(typ *ctype) string {
	if typ.isPointer() {
		reg := f.temp()
		f.emit(&metals.Const{Dst: reg, Type: typ.metalsType(), Null: true})
		return reg
	}
	if typ.isFloat() {
		return f.constant(typ, "0")
	}
	return f.constant(typ, "0")
}
