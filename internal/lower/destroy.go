package lower

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/types"
)

// Destruction, synthesized like hashing: one METALS function per owning
// type, taking the address of the value to destroy. It runs the type's own
// destroy, if it implements Destroy, and then destroys every owning part
// in declaration order.
//
// A record is destroyed where it lies, so its destroy sees the storage its
// owner had. Every other shape is taken apart by value: each owning part
// is copied into a slot of its own and destroyed there, which is the same
// value at a different address, and nothing reads the original again.

// destroyFor returns the METALS function that destroys a value of t
// through a pointer to it.
func (l *lowerer) destroyFor(t types.Type) string {
	key := t.String()
	name := "destroy<" + key + ">"
	if l.destroys[key] {
		return name
	}
	// Registered before the body is built, so a type that reaches itself
	// finds the name rather than recursing forever.
	l.destroys[key] = true
	l.buildDestroy(name, t)
	return name
}

func (l *lowerer) buildDestroy(name string, t types.Type) {
	b := &destroyBuilder{l: l, fn: &metals.Func{
		Name:   name,
		Params: []metals.Param{{Name: "p", Type: &metals.Ptr{Elem: l.metalsType(t)}}},
	}}
	b.blocks = []*[]metals.Instr{&b.fn.Body}

	switch t := t.(type) {
	case *types.Struct:
		b.structBody(t)
	case *types.Enum:
		b.enumBody(t)
	case *types.Tuple:
		b.tupleBody(t)
	case *types.Array:
		b.arrayBody(t)
	case *types.Optional:
		b.optionalBody(t)
	default:
		l.fail("cannot destroy %s", t)
	}
	b.emit(&metals.Return{})

	l.module.Funcs = append(l.module.Funcs, b.fn)
}

type destroyBuilder struct {
	l      *lowerer
	fn     *metals.Func
	blocks []*[]metals.Instr
	regs   map[string]int
}

func (b *destroyBuilder) emit(instr metals.Instr) {
	top := b.blocks[len(b.blocks)-1]
	*top = append(*top, instr)
}

func (b *destroyBuilder) nested(fill func()) []metals.Instr {
	var body []metals.Instr
	b.blocks = append(b.blocks, &body)
	fill()
	b.blocks = b.blocks[:len(b.blocks)-1]
	return body
}

func (b *destroyBuilder) reg(hint string) string {
	if b.regs == nil {
		b.regs = map[string]int{}
	}
	n := b.regs[hint]
	b.regs[hint] = n + 1
	if n == 0 {
		return hint
	}
	return hint + "." + strconv.Itoa(n)
}

// own runs the type's Destroy on the value behind p.
func (b *destroyBuilder) own(t types.Type) {
	if !types.Destroys(t) {
		return
	}
	method := methodOfType(t, "destroy")
	if method == nil || method.Decl == nil {
		b.l.fail("%s implements Destroy without a destroy method", t)
	}
	callee := b.l.ensureFunc(method.Decl, methodBindings(method, t))
	b.emit(&metals.Call{Callee: callee, Args: []string{"p"}})
}

// at destroys the value behind ptr when its type is owning.
func (b *destroyBuilder) at(ptr string, t types.Type) {
	if !types.Owning(t) {
		return
	}
	b.emit(&metals.Call{Callee: b.l.destroyFor(t), Args: []string{ptr}})
}

// value destroys a value held in a register, from a slot it is copied to.
func (b *destroyBuilder) value(reg string, t types.Type) {
	if !types.Owning(t) {
		return
	}
	slot := b.reg("part")
	b.emit(&metals.Slot{Dst: slot, Type: b.l.metalsType(t)})
	b.emit(&metals.Store{Ptr: slot, Val: reg})
	b.at(slot, t)
}

func (b *destroyBuilder) structBody(t *types.Struct) {
	b.own(t)
	for index, field := range t.Fields {
		if !types.Owning(field.Type) {
			continue
		}
		ptr := b.reg("f")
		b.emit(&metals.FieldPtr{Dst: ptr, Ptr: "p", Index: index})
		b.at(ptr, field.Type)
	}
}

func (b *destroyBuilder) load() string {
	v := b.reg("v")
	b.emit(&metals.Load{Dst: v, Ptr: "p"})
	return v
}

func (b *destroyBuilder) enumBody(t *types.Enum) {
	b.own(t)
	v := b.load()
	tag := b.reg("tag")
	b.emit(&metals.VariantTag{Dst: tag, X: v})

	var cases []metals.SwitchCase
	for index, variant := range t.Variants {
		body := b.nested(func() {
			for at, payload := range variant.Payload {
				if !types.Owning(payload) {
					continue
				}
				got := b.reg("payload")
				b.emit(&metals.VariantField{Dst: got, X: v, Variant: index, Index: at})
				b.value(got, payload)
			}
		})
		if len(body) > 0 {
			cases = append(cases, metals.SwitchCase{Value: index, Body: body})
		}
	}
	if len(cases) > 0 {
		b.emit(&metals.Switch{X: tag, Cases: cases})
	}
}

func (b *destroyBuilder) tupleBody(t *types.Tuple) {
	v := b.load()
	for index, elem := range t.Elems {
		if !types.Owning(elem) {
			continue
		}
		got := b.reg("e")
		b.emit(&metals.TupleGet{Dst: got, X: v, Index: index})
		b.value(got, elem)
	}
}

func (b *destroyBuilder) arrayBody(t *types.Array) {
	for i := int64(0); i < t.Length; i++ {
		index := b.reg("i")
		b.emit(&metals.Const{Dst: index, Type: metals.TUsize, Value: strconv.FormatInt(i, 10)})
		ptr := b.reg("elem")
		b.emit(&metals.IndexPtr{Dst: ptr, Ptr: "p", Index: index})
		b.at(ptr, t.Elem)
	}
}

func (b *destroyBuilder) optionalBody(t *types.Optional) {
	v := b.load()
	empty := b.reg("none")
	b.emit(&metals.Const{Dst: empty, Type: b.l.metalsType(t), Null: true})
	present := b.reg("present")
	b.emit(&metals.Bin{Dst: present, Op: "ne", A: v, B: empty})
	b.emit(&metals.If{
		Cond: present,
		Then: b.nested(func() {
			inner := b.reg("some")
			b.emit(&metals.OptUnwrap{Dst: inner, X: v})
			b.value(inner, t.Elem)
		}),
	})
}

// ---------------------------------------------------------------------------
// Owners in a function
// ---------------------------------------------------------------------------

// A local declared with `let` owns its value. Its flag says whether it
// holds one: a move clears it and an assignment sets it, so the
// destruction registered with the scope runs only for a value that is
// still there. The destruction sits among the scope's defers, which is
// what interleaves the two in reverse order of registration.
//
// A parameter borrows, and nothing here touches it.

// own registers the destruction of a local just bound in the current
// scope. holds says whether the declaration gave it a value.
func (f *funcLowerer) own(name string, holds bool) {
	entry := f.lookup(name)
	if entry == nil || !types.Owning(entry.typ) {
		return
	}
	entry.alive = f.newReg(name + ".alive")
	f.setAlive(entry, holds)
	top := f.scopes[len(f.scopes)-1]
	top.defers = append(top.defers, func() { f.destroyLocal(entry) })
}

func (f *funcLowerer) setAlive(entry *local, holds bool) {
	f.emit(&metals.Const{Dst: entry.alive, Type: metals.TBool, Value: strconv.FormatBool(holds)})
}

func (f *funcLowerer) destroyLocal(entry *local) {
	f.emit(&metals.If{
		Cond: entry.alive,
		Then: f.nested(func() { f.destroyAt(entry.reg, entry.typ) }),
	})
}

func (f *funcLowerer) destroyAt(ptr string, t types.Type) {
	f.emit(&metals.Call{Callee: f.l.destroyFor(t), Args: []string{ptr}})
}

// destroyValue destroys a value held in a register, from a slot of its
// own.
func (f *funcLowerer) destroyValue(reg string, t types.Type) {
	slot := f.newReg("dead")
	f.emit(&metals.Slot{Dst: slot, Type: f.l.metalsType(t)})
	f.emit(&metals.Store{Ptr: slot, Val: reg})
	f.destroyAt(slot, t)
}

// moveOut clears the flag of a local whose value e takes.
func (f *funcLowerer) moveOut(e *ast.Ident, entry *local) {
	if entry.alive != "" && f.l.info.Consumed[e] {
		f.setAlive(entry, false)
	}
}

// storeOwned assigns over an owning place: the value it held is destroyed
// first, so it does not linger. A local that may hold nothing asks its
// flag, and is marked as holding one afterwards.
func (f *funcLowerer) storeOwned(lhs ast.Expr, target *place, value string) {
	if !types.Owning(target.typ) {
		target.store(value)
		return
	}
	if ident, ok := lhs.(*ast.Ident); ok {
		if entry := f.lookup(ident.Name); entry != nil && entry.alive != "" {
			f.destroyLocal(entry)
			target.store(value)
			f.setAlive(entry, true)
			return
		}
	}
	ptr := target.addr()
	if ptr == "" {
		f.l.fail("@%s: assigning an owning value to a place with no address", f.fn.Name)
	}
	f.destroyAt(ptr, target.typ)
	target.store(value)
}

// ---------------------------------------------------------------------------
// Temporaries
// ---------------------------------------------------------------------------

// A fresh owning value that nothing takes, such as the result of a call
// passed straight to a parameter that borrows it, is a temporary. It is
// destroyed at the end of the statement that made it, or of the branch it
// was made in, whichever is nearer, so a temporary made on only one path
// is destroyed on that path.

type temporary struct {
	slot string
	typ  types.Type
}

// keepTemporary spills a fresh owning value nothing takes, so that it can
// be destroyed when its statement ends.
func (f *funcLowerer) keepTemporary(e ast.Expr, reg string) {
	if !isFreshValue(e) || f.l.info.Consumed[e] {
		return
	}
	t := f.typeOfVoidOK(e)
	if t == nil || !types.Owning(t) {
		return
	}
	slot := f.newReg("temp")
	f.emit(&metals.Slot{Dst: slot, Type: f.l.metalsType(t)})
	f.emit(&metals.Store{Ptr: slot, Val: reg})
	f.temps = append(f.temps, temporary{slot: slot, typ: t})
}

func isFreshValue(e ast.Expr) bool {
	switch e.(type) {
	case *ast.CallExpr, *ast.StructLit, *ast.TupleLit, *ast.ArrayLit,
		*ast.MatchExpr, *ast.BlockExpr, *ast.AwaitExpr, *ast.RecvExpr:
		return true
	}
	return false
}

// destroyTemporaries destroys the temporaries made since mark, newest
// first, without forgetting them: an early exit destroys them on its own
// path and the path that falls through still has to.
func (f *funcLowerer) destroyTemporaries(mark int) {
	for i := len(f.temps) - 1; i >= mark; i-- {
		f.destroyAt(f.temps[i].slot, f.temps[i].typ)
	}
}

// endTemporaries destroys the temporaries made since mark at the end of
// the code that made them, unless that code already left.
func (f *funcLowerer) endTemporaries(mark int) {
	if !f.terminated() {
		f.destroyTemporaries(mark)
	}
	f.temps = f.temps[:mark]
}

// terminated reports whether the block being emitted ends in a jump, after
// which nothing runs.
func (f *funcLowerer) terminated() bool {
	body := *f.blocks[len(f.blocks)-1]
	if len(body) == 0 {
		return false
	}
	switch body[len(body)-1].(type) {
	case *metals.Return, *metals.Br:
		return true
	}
	return false
}
