package lower

import (
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/types"
)

// Derived hashing, synthesized the way derived printing is: one METALS
// function per composite type, which every tier below reaches as an
// ordinary call. The runtime answers for the scalars, where there is
// nothing to walk.
//
// hash() must agree with == , so it is derived over exactly what ==
// compares: a struct's fields, a tuple's elements, an enum's tag and
// payload. The numbers themselves have to match the interpreter byte for
// byte — it is the specification — so the fold is written out here rather
// than left to a backend: FNV's prime over splitmix64'd parts.

const (
	fnvOffset uint64 = 14695981039346656037
	fnvPrime  uint64 = 1099511628211
)

// hashWord is the splitmix64 finalizer, mirrored from the interpreter. It
// runs here only on values known at compile time, such as an enum tag.
func hashWord(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

func hashCombine(h, part uint64) uint64 { return (h ^ part) * fnvPrime }

// hashFor returns the METALS function that hashes t.
func (l *lowerer) hashFor(t types.Type) string {
	t = types.Default(t)
	if name, sig := runtimeHash(l, t); name != "" {
		l.ensureImport(name, sig)
		return name
	}

	key := t.String()
	name := "hash<" + key + ">"
	if l.hashes[key] {
		return name
	}
	// Registered before the body is built, so a type that reaches itself
	// finds the name rather than recursing forever.
	l.hashes[key] = true
	l.buildHash(name, t)
	return name
}

// runtimeHash names the import that hashes a scalar, with its signature.
// Every integer width goes to the same two entry points once the
// declaration fixes the parameter type; a float rides in a double, which
// is how f32 is already carried.
func runtimeHash(l *lowerer, t types.Type) (string, *metals.FnType) {
	basic, ok := t.(*types.Basic)
	if !ok {
		return "", nil
	}
	switch {
	case basic.Kind == types.BoolKind:
		return "", nil
	case types.IsNumeric(t), basic.Kind == types.RuneKind, basic.Kind == types.StringKind:
		lowered := l.metalsType(t)
		return "hash<" + lowered.String() + ">", &metals.FnType{
			Params: []metals.Type{lowered},
			Result: metals.TUint64,
		}
	}
	return "", nil
}

func (l *lowerer) buildHash(name string, t types.Type) {
	b := &hashBuilder{l: l, fn: &metals.Func{
		Name:   name,
		Params: []metals.Param{{Name: "v", Type: l.metalsType(t)}},
		Result: metals.TUint64,
	}}
	b.blocks = []*[]metals.Instr{&b.fn.Body}

	switch t := t.(type) {
	case *types.Basic:
		b.boolBody()
	case *types.Struct:
		b.structBody(t)
	case *types.Tuple:
		b.tupleBody(t)
	case *types.Enum:
		b.enumBody(t)
	default:
		l.fail("cannot hash %s", t.String())
	}

	l.module.Funcs = append(l.module.Funcs, b.fn)
}

// ---------------------------------------------------------------------------
// The builder
// ---------------------------------------------------------------------------

type hashBuilder struct {
	l      *lowerer
	fn     *metals.Func
	blocks []*[]metals.Instr
	regs   map[string]int
}

func (b *hashBuilder) emit(instr metals.Instr) {
	top := b.blocks[len(b.blocks)-1]
	*top = append(*top, instr)
}

func (b *hashBuilder) nested(fill func()) []metals.Instr {
	var body []metals.Instr
	b.blocks = append(b.blocks, &body)
	fill()
	b.blocks = b.blocks[:len(b.blocks)-1]
	return body
}

func (b *hashBuilder) reg(hint string) string {
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

func (b *hashBuilder) word(value uint64) string {
	dst := b.reg("k")
	b.emit(&metals.Const{Dst: dst, Type: metals.TUint64, Value: strconv.FormatUint(value, 10)})
	return dst
}

// mix folds one part into the accumulator: FNV's step over a whole word.
func (b *hashBuilder) mix(acc, part string) {
	b.emit(&metals.Bin{Dst: acc, Op: "bxor", A: acc, B: part})
	b.emit(&metals.Bin{Dst: acc, Op: "wmul", A: acc, B: b.word(fnvPrime)})
}

// part hashes one nested value, through the type's own hash where it
// declares one — the same precedence to_string has over derived printing.
func (b *hashBuilder) part(reg string, t types.Type) string {
	dst := b.reg("h")
	if method := hashMethodOf(t); method != nil {
		callee := b.l.ensureFunc(method.Decl, ownerBindings(method.Decl, t))
		b.emit(&metals.Call{Dst: dst, Callee: callee, Args: []string{reg}})
		return dst
	}
	b.emit(&metals.Call{Dst: dst, Callee: b.l.hashFor(t), Args: []string{reg}})
	return dst
}

// hashMethodOf finds a hash(): u64 the type declares itself, which stands
// in for the derived one.
func hashMethodOf(t types.Type) *types.Method {
	var method *types.Method
	switch t := t.(type) {
	case *types.Struct:
		if isOpaqueHandle(t) {
			return nil
		}
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		method = origin.Methods["hash"]
	case *types.Enum:
		origin := t
		if t.Origin != nil {
			origin = t.Origin
		}
		method = origin.Methods["hash"]
	}
	if method == nil || method.Decl == nil || method.Recv == types.NoRecv {
		return nil
	}
	if len(method.Sig.Params) != 0 || len(method.TypeParams) > 0 {
		return nil
	}
	if !types.Identical(method.Sig.Result, types.Uint64) {
		return nil
	}
	return method
}

// ---------------------------------------------------------------------------
// One body per shape
// ---------------------------------------------------------------------------

// boolBody is the one scalar the runtime does not answer for: two values,
// so two constants, and no call.
func (b *hashBuilder) boolBody() {
	b.emit(&metals.If{
		Cond: "v",
		Then: b.nested(func() { b.emit(&metals.Return{Val: b.word(hashWord(1))}) }),
	})
	b.emit(&metals.Return{Val: b.word(hashWord(0))})
}

func (b *hashBuilder) structBody(t *types.Struct) {
	acc := b.reg("acc")
	b.emit(&metals.Const{Dst: acc, Type: metals.TUint64, Value: strconv.FormatUint(fnvOffset, 10)})
	for index, field := range t.Fields {
		got := b.reg("f")
		b.emit(&metals.FieldGet{Dst: got, X: "v", Index: index})
		b.mix(acc, b.part(got, field.Type))
	}
	b.emit(&metals.Return{Val: acc})
}

func (b *hashBuilder) tupleBody(t *types.Tuple) {
	acc := b.reg("acc")
	b.emit(&metals.Const{Dst: acc, Type: metals.TUint64, Value: strconv.FormatUint(fnvOffset, 10)})
	for index, elem := range t.Elems {
		got := b.reg("e")
		b.emit(&metals.TupleGet{Dst: got, X: "v", Index: index})
		b.mix(acc, b.part(got, elem))
	}
	b.emit(&metals.Return{Val: acc})
}

// enumBody switches on the tag, like the derived representation does. The
// tag's own contribution is known here, so each arm starts from a constant
// rather than hashing the tag at run time.
func (b *hashBuilder) enumBody(t *types.Enum) {
	tag := b.reg("tag")
	b.emit(&metals.VariantTag{Dst: tag, X: "v"})

	cases := make([]metals.SwitchCase, 0, len(t.Variants))
	for index, variant := range t.Variants {
		body := b.nested(func() {
			acc := b.reg("acc")
			seed := hashCombine(fnvOffset, hashWord(uint64(index)))
			b.emit(&metals.Const{Dst: acc, Type: metals.TUint64, Value: strconv.FormatUint(seed, 10)})
			for at, payload := range variant.Payload {
				got := b.reg("p")
				b.emit(&metals.VariantField{Dst: got, X: "v", Variant: index, Index: at})
				b.mix(acc, b.part(got, payload))
			}
			b.emit(&metals.Return{Val: acc})
		})
		cases = append(cases, metals.SwitchCase{Value: index, Body: body})
	}

	b.emit(&metals.Switch{
		X:       tag,
		Cases:   cases,
		Default: b.nested(func() { b.emit(&metals.Abort{Message: "unknown variant"}) }),
	})
}
