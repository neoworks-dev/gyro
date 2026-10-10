package lower

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

// Operators on a type its author declared are calls: + is the type's add,
// unary - its neg, < its compare against zero, and == its eq. The checker
// has already seen to it that the type implements the trait, so lowering
// only has to find the method.

// operatorMethods names the method each overloadable operator calls.
var operatorMethods = map[token.Kind]string{
	token.Plus:    "add",
	token.Minus:   "sub",
	token.Star:    "mul",
	token.Slash:   "div",
	token.Percent: "rem",
}

// implementationOf finds the impl of a capability trait a type carries.
func implementationOf(t types.Type, kind string) *types.Impl {
	var impls []*types.Impl
	switch t := t.(type) {
	case *types.Struct:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		impls = origin.Impls
	case *types.Enum:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		impls = origin.Impls
	case *types.Union:
		impls = t.Impls
	}
	for _, impl := range impls {
		if impl.Trait.Kind == kind {
			return impl
		}
	}
	return nil
}

// callMethod calls a method of a declared type on already-evaluated
// operands. The receiver is passed by value: the operator traits all take
// self.
func (f *funcLowerer) callMethod(t types.Type, name string, args []string, result types.Type) string {
	method := methodOfType(t, name)
	if method == nil || method.Decl == nil {
		f.l.fail("@%s: %s has no method %s for the operator", f.fn.Name, t, name)
	}
	callee := f.l.ensureFunc(method.Decl, methodBindings(method, t))
	out := f.newReg(name)
	f.emit(&metals.Call{Dst: out, Callee: callee, Args: args})
	return out
}

// declaredBinary lowers an arithmetic operator on a declared type.
func (f *funcLowerer) declaredBinary(e *ast.BinaryExpr, t types.Type) string {
	name, ok := operatorMethods[e.Op]
	if !ok {
		f.l.fail("@%s: the operator %s is not defined on %s", f.fn.Name, e.Op, t)
	}
	left := f.lowerExpr(e.X, t)
	right := f.lowerExpr(e.Y, t)
	return f.callMethod(t, name, []string{left, right}, t)
}

// orderingByCompare lowers < <= > >= over a type ordered by its compare:
// the call's answer against zero.
func (f *funcLowerer) orderingByCompare(e *ast.BinaryExpr, t types.Type) string {
	left := f.lowerExpr(e.X, t)
	right := f.lowerExpr(e.Y, t)

	var answer string
	if types.IsString(t) {
		f.l.ensureImport("string::compare", &metals.FnType{
			Params: []metals.Type{metals.TStr, metals.TStr},
			Result: metals.TInt32,
		})
		answer = f.newReg("cmp")
		f.emit(&metals.Call{Dst: answer, Callee: "string::compare", Args: []string{left, right}})
	} else {
		answer = f.callMethod(t, "compare", []string{left, right}, types.Int32)
	}

	zero := f.newReg("zero")
	f.emit(&metals.Const{Dst: zero, Type: metals.TInt32, Value: "0"})
	out := f.newReg("cmp")
	f.emit(&metals.Bin{Dst: out, Op: orderOps[e.Op], A: answer, B: zero})
	return out
}

// primitiveOrdered reports the types the ordering instructions apply to
// directly.
func primitiveOrdered(t types.Type) bool {
	return types.IsOrdered(types.Default(t))
}

// isDeclared reports a named type whose operators are its own methods.
func isDeclared(t types.Type) bool {
	switch t.(type) {
	case *types.Struct, *types.Enum:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Equality
// ---------------------------------------------------------------------------

// customEq reports whether comparing two values of t has to call an eq its
// author wrote: because t has one, or because a part of t does and the
// derived comparison therefore cannot be the machine's word-for-word one.
func (l *lowerer) customEq(t types.Type) bool {
	return l.customEqSeen(t, map[string]bool{})
}

func (l *lowerer) customEqSeen(t types.Type, seen map[string]bool) bool {
	switch t := t.(type) {
	case *types.Struct:
		if isOpaqueHandle(t) {
			return false
		}
		if impl := implementationOf(t, "Eq"); impl != nil && !impl.Derived {
			return true
		}
		if seen[t.String()] {
			return false
		}
		seen[t.String()] = true
		for _, field := range t.Fields {
			if l.customEqSeen(field.Type, seen) {
				return true
			}
		}
	case *types.Enum:
		if impl := implementationOf(t, "Eq"); impl != nil && !impl.Derived {
			return true
		}
		if seen[t.String()] {
			return false
		}
		seen[t.String()] = true
		for _, variant := range t.Variants {
			for _, payload := range variant.Payload {
				if l.customEqSeen(payload, seen) {
					return true
				}
			}
		}
	case *types.Tuple:
		for _, elem := range t.Elems {
			if l.customEqSeen(elem, seen) {
				return true
			}
		}
	case *types.Optional:
		return l.customEqSeen(t.Elem, seen)
	case *types.Array:
		return l.customEqSeen(t.Elem, seen)
	}
	return false
}

// equalValues compares two registers of one type, calling the eq the type
// or one of its parts defines where there is one.
func (f *funcLowerer) equalValues(left, right string, t types.Type) string {
	if !f.l.customEq(t) {
		out := f.newReg("eq")
		f.emit(&metals.Bin{Dst: out, Op: "eq", A: left, B: right})
		return out
	}
	out := f.newReg("eq")
	f.emit(&metals.Call{Dst: out, Callee: f.l.eqFor(t), Args: []string{left, right}})
	return out
}

// eqFor returns the METALS function that compares two values of t,
// synthesizing it the way derived hashing and printing are: one function
// per type, written once here rather than in a backend.
func (l *lowerer) eqFor(t types.Type) string {
	// A type's own eq is the function.
	if impl := implementationOf(t, "Eq"); impl != nil && !impl.Derived {
		method := methodOfType(t, "eq")
		if method == nil || method.Decl == nil {
			l.fail("%s implements Eq by hand but has no eq", t)
		}
		return l.ensureFunc(method.Decl, methodBindings(method, t))
	}

	key := t.String()
	name := "eq<" + key + ">"
	if l.eqs[key] {
		return name
	}
	// Registered before the body is built, so a type that reaches itself
	// finds the name rather than recursing forever.
	l.eqs[key] = true

	lowered := l.metalsType(t)
	b := &eqBuilder{l: l, fn: &metals.Func{
		Name:   name,
		Params: []metals.Param{{Name: "a", Type: lowered}, {Name: "b", Type: lowered}},
		Result: metals.TBool,
	}}
	b.blocks = []*[]metals.Instr{&b.fn.Body}

	switch t := t.(type) {
	case *types.Struct:
		b.structBody(t)
	case *types.Tuple:
		b.tupleBody(t)
	case *types.Enum:
		b.enumBody(t)
	case *types.Optional:
		b.optionalBody(t)
	default:
		l.fail("== over %s, which has parts that implement Eq by hand, is not built yet", t)
	}

	l.module.Funcs = append(l.module.Funcs, b.fn)
	return name
}

type eqBuilder struct {
	l      *lowerer
	fn     *metals.Func
	blocks []*[]metals.Instr
	regs   map[string]int
}

func (b *eqBuilder) emit(instr metals.Instr) {
	top := b.blocks[len(b.blocks)-1]
	*top = append(*top, instr)
}

func (b *eqBuilder) nested(fill func()) []metals.Instr {
	var body []metals.Instr
	b.blocks = append(b.blocks, &body)
	fill()
	b.blocks = b.blocks[:len(b.blocks)-1]
	return body
}

func (b *eqBuilder) reg(hint string) string {
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

func (b *eqBuilder) boolean(value bool) string {
	out := b.reg("k")
	text := "false"
	if value {
		text = "true"
	}
	b.emit(&metals.Const{Dst: out, Type: metals.TBool, Value: text})
	return out
}

// returnIfDiffers compares one pair of parts and leaves the function with
// false when they differ.
func (b *eqBuilder) returnIfDiffers(left, right string, t types.Type) {
	same := b.reg("same")
	if b.l.customEq(t) {
		b.emit(&metals.Call{Dst: same, Callee: b.l.eqFor(t), Args: []string{left, right}})
	} else {
		b.emit(&metals.Bin{Dst: same, Op: "eq", A: left, B: right})
	}
	differs := b.reg("differs")
	b.emit(&metals.Un{Dst: differs, Op: "not", X: same})
	b.emit(&metals.If{Cond: differs, Then: b.nested(func() {
		b.emit(&metals.Return{Val: b.boolean(false)})
	})})
}

func (b *eqBuilder) structBody(t *types.Struct) {
	for index, field := range t.Fields {
		left, right := b.reg("l"), b.reg("r")
		b.emit(&metals.FieldGet{Dst: left, X: "a", Index: index})
		b.emit(&metals.FieldGet{Dst: right, X: "b", Index: index})
		b.returnIfDiffers(left, right, field.Type)
	}
	b.emit(&metals.Return{Val: b.boolean(true)})
}

func (b *eqBuilder) tupleBody(t *types.Tuple) {
	for index, elem := range t.Elems {
		left, right := b.reg("l"), b.reg("r")
		b.emit(&metals.TupleGet{Dst: left, X: "a", Index: index})
		b.emit(&metals.TupleGet{Dst: right, X: "b", Index: index})
		b.returnIfDiffers(left, right, elem)
	}
	b.emit(&metals.Return{Val: b.boolean(true)})
}

// enumBody compares the tags, then the payload of the variant they name.
func (b *eqBuilder) enumBody(t *types.Enum) {
	tagA, tagB := b.reg("tag"), b.reg("tag")
	b.emit(&metals.VariantTag{Dst: tagA, X: "a"})
	b.emit(&metals.VariantTag{Dst: tagB, X: "b"})
	same := b.reg("tags")
	b.emit(&metals.Bin{Dst: same, Op: "eq", A: tagA, B: tagB})
	differs := b.reg("differs")
	b.emit(&metals.Un{Dst: differs, Op: "not", X: same})
	b.emit(&metals.If{Cond: differs, Then: b.nested(func() {
		b.emit(&metals.Return{Val: b.boolean(false)})
	})})

	var cases []metals.SwitchCase
	for index, variant := range t.Variants {
		if len(variant.Payload) == 0 {
			continue
		}
		body := b.nested(func() {
			for at, payload := range variant.Payload {
				left, right := b.reg("l"), b.reg("r")
				b.emit(&metals.VariantField{Dst: left, X: "a", Variant: index, Index: at})
				b.emit(&metals.VariantField{Dst: right, X: "b", Variant: index, Index: at})
				b.returnIfDiffers(left, right, payload)
			}
		})
		cases = append(cases, metals.SwitchCase{Value: index, Body: body})
	}
	if len(cases) > 0 {
		b.emit(&metals.Switch{X: tagA, Cases: cases})
	}
	b.emit(&metals.Return{Val: b.boolean(true)})
}

// optionalBody: two nulls are equal, a null and a value are not, and two
// values compare as the type inside does.
func (b *eqBuilder) optionalBody(t *types.Optional) {
	null := b.reg("null")
	b.emit(&metals.Const{Dst: null, Type: b.l.metalsType(t), Null: true})
	aNull, bNull := b.reg("anull"), b.reg("bnull")
	b.emit(&metals.Bin{Dst: aNull, Op: "eq", A: "a", B: null})
	b.emit(&metals.Bin{Dst: bNull, Op: "eq", A: "b", B: null})

	either := b.reg("either")
	b.emit(&metals.Bin{Dst: either, Op: "ne", A: aNull, B: bNull})
	b.emit(&metals.If{Cond: either, Then: b.nested(func() {
		b.emit(&metals.Return{Val: b.boolean(false)})
	})})
	b.emit(&metals.If{Cond: aNull, Then: b.nested(func() {
		b.emit(&metals.Return{Val: b.boolean(true)})
	})})

	left, right := b.reg("l"), b.reg("r")
	b.emit(&metals.OptUnwrap{Dst: left, X: "a"})
	b.emit(&metals.OptUnwrap{Dst: right, X: "b"})
	b.returnIfDiffers(left, right, t.Elem)
	b.emit(&metals.Return{Val: b.boolean(true)})
}

// ---------------------------------------------------------------------------
// Indexing
// ---------------------------------------------------------------------------

// indexElemOf is the T of a declared type that implements Index<T>, or nil.
// The impl stores T over the type's own parameters; a generic type
// answers for the instance through its arguments.
func indexElemOf(t types.Type) types.Type {
	impl := implementationOf(t, "Index")
	if impl == nil || len(impl.Args) != 1 {
		return nil
	}
	var params []*types.TypeParam
	var args []types.Type
	switch t := t.(type) {
	case *types.Struct:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		params, args = origin.TypeParams, t.Args
	case *types.Enum:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		params, args = origin.TypeParams, t.Args
	}
	if param, ok := impl.Args[0].(*types.TypeParam); ok {
		for i, owner := range params {
			if owner == param && i < len(args) {
				return args[i]
			}
		}
	}
	return impl.Args[0]
}

// declaredElementPlace reaches an element of a declared type through the
// address its at method hands back, the way an Array's runtime at does.
func (f *funcLowerer) declaredElementPlace(e *ast.IndexExpr, base, elemType types.Type) *place {
	method := methodOfType(base, "at")
	if method == nil || method.Decl == nil {
		f.l.fail("@%s: %s is indexed but has no at method", f.fn.Name, base)
	}
	callee := f.l.ensureFunc(method.Decl, methodBindings(method, base))
	receiver := f.methodReceiver(e.X, method)
	index := f.lowerExpr(e.Index, types.USize)
	ptr := f.newReg("elem.p")
	f.emit(&metals.Call{Dst: ptr, Callee: callee, Args: []string{receiver, index}})
	return f.pointerPlace(ptr, elemType)
}
