package cgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// frame emits one function. Every METALS register becomes one C local of
// its fixed type, declared at the top of the body: registers are not SSA,
// but their type is settled at the first assignment, which is exactly
// what a C local wants.
type frame struct {
	g      *generator
	fn     *metals.Func
	types  map[string]metals.Type
	areas  map[string]string // `slot` destination -> addressable local
	labels map[string]string // METALS label -> C label
	indent int
}

func (g *generator) genFunc(fn *metals.Func) {
	f := &frame{
		g:      g,
		fn:     fn,
		types:  g.regTypes[fn.Name],
		areas:  map[string]string{},
		labels: map[string]string{},
		indent: 1,
	}

	g.raw("\n/* fn @%s */", comment(fn.Name))
	g.raw("%s {", g.signature(fn))
	f.declareRegisters()
	f.declareAreas()
	f.body(fn.Body)

	// The lowering always terminates a body; this backstop only matters
	// when a function's last path falls through, and keeps C from
	// reaching the end of a value-returning function.
	if fn.Result != nil {
		f.line("{ %s fallthrough_result = {0}; return fallthrough_result; }", g.cType(fn.Result))
	}
	g.raw("}")
}

// declareRegisters declares every register the function uses, in sorted
// order so the output is deterministic. Parameters are already declared
// by the signature. Zeroing costs nothing the optimizer cannot see and
// keeps the generated file free of reads C would call undefined.
func (f *frame) declareRegisters() {
	params := map[string]bool{}
	for _, param := range f.fn.Params {
		params[param.Name] = true
	}
	for _, name := range sortedNames(f.types) {
		if params[name] {
			continue
		}
		f.line("%s %s = {0}; /* %%%s */", f.g.cType(f.types[name]), registerName(name), comment(name))
	}
}

// declareAreas gives every `slot` instruction an addressable local. The
// instruction itself re-zeroes it, because a slot inside a loop is a
// fresh cell on every pass.
func (f *frame) declareAreas() {
	walkInstrs(f.fn.Body, func(instr metals.Instr) {
		slot, ok := instr.(*metals.Slot)
		if !ok {
			return
		}
		if _, seen := f.areas[slot.Dst]; seen {
			return
		}
		name := fmt.Sprintf("sl_%d", len(f.areas))
		f.areas[slot.Dst] = name
		// A cell a pointer addresses holds storage, so it is declared with
		// the type a load through that pointer will read.
		f.line("%s %s = {0}; /* slot for %%%s */", f.g.memberType(slot.Type), name, comment(slot.Dst))
	})
}

// walkInstrs visits every instruction, structured bodies included.
func walkInstrs(body []metals.Instr, visit func(metals.Instr)) {
	for _, instr := range body {
		visit(instr)
		switch i := instr.(type) {
		case *metals.If:
			walkInstrs(i.Then, visit)
			walkInstrs(i.Else, visit)
		case *metals.Block:
			walkInstrs(i.Body, visit)
		case *metals.Loop:
			walkInstrs(i.Body, visit)
		case *metals.Switch:
			for _, c := range i.Cases {
				walkInstrs(c.Body, visit)
			}
			walkInstrs(i.Default, visit)
		case *metals.Select:
			for _, arm := range i.Arms {
				walkInstrs(arm.Body, visit)
			}
			walkInstrs(i.Default, visit)
		}
	}
}

// ---------------------------------------------------------------------------
// Emission
// ---------------------------------------------------------------------------

func (f *frame) line(format string, args ...any) {
	f.g.text.WriteString(strings.Repeat("\t", f.indent))
	fmt.Fprintf(&f.g.text, format, args...)
	f.g.text.WriteString("\n")
}

// label writes a C label. A label may end a compound statement, so it
// always carries an empty statement.
func (f *frame) label(name string) {
	f.g.text.WriteString(strings.Repeat("\t", f.indent-1))
	fmt.Fprintf(&f.g.text, "%s: ;\n", name)
}

func (f *frame) reg(name string) string { return registerName(name) }

func (f *frame) typeOf(reg string) metals.Type {
	t, ok := f.types[reg]
	if !ok {
		f.g.fail("@%s: register %%%s has no type", f.fn.Name, reg)
	}
	return t
}

func (f *frame) assign(dst, expr string) {
	f.line("%s = %s;", f.reg(dst), expr)
}

// ---------------------------------------------------------------------------
// Instructions
// ---------------------------------------------------------------------------

func (f *frame) body(instrs []metals.Instr) {
	for _, instr := range instrs {
		f.instr(instr)
	}
}

func (f *frame) instr(instr metals.Instr) {
	g := f.g
	switch i := instr.(type) {
	case *metals.Const:
		f.emitConst(i)

	case *metals.Copy:
		f.assign(i.Dst, f.reg(i.X))

	case *metals.Bin:
		f.emitBin(i)

	case *metals.Un:
		f.emitUn(i)

	case *metals.Conv:
		f.emitConv(i)

	case *metals.Trunc:
		f.emitTrunc(i)

	case *metals.Bitcast:
		f.emitBitcast(i)

	case *metals.Slot:
		area, ok := f.areas[i.Dst]
		if !ok {
			g.fail("@%s: slot %%%s has no storage", f.fn.Name, i.Dst)
		}
		f.line("memset(&%s, 0, %d);", area, g.sizeOf(i.Type))
		f.assign(i.Dst, "(void *)&"+area)

	case *metals.GlobalPtr:
		f.assign(i.Dst, "(void *)&"+g.globalSymbol(i.Name))

	case *metals.New:
		f.assign(i.Dst, fmt.Sprintf("gy_alloc(%d)", g.sizeOf(i.Type)))

	case *metals.Free:
		f.line("gy_free(%s);", f.reg(i.X))

	case *metals.Load:
		f.assign(i.Dst, f.deref(i.Ptr, f.typeOf(i.Dst)))

	case *metals.Store:
		f.line("%s = %s;", f.deref(i.Ptr, f.typeOf(i.Val)), f.reg(i.Val))

	case *metals.FieldPtr:
		record := f.recordBehindPtr(i.Ptr)
		if i.Index >= len(record.Fields) {
			g.fail("@%s: field.ptr %d past the record", f.fn.Name, i.Index)
		}
		named := f.typeOf(i.Ptr).(*metals.Ptr).Elem
		f.assign(i.Dst, fmt.Sprintf("(void *)&((%s *)%s)->f%d", g.cType(named), f.reg(i.Ptr), i.Index))

	case *metals.ElemPtr:
		// An array's storage starts at its first element, so the decay
		// is the same address with a different type over it.
		f.assign(i.Dst, f.reg(i.Ptr))

	case *metals.PtrOffset:
		size := g.sizeOf(f.typeOf(i.Ptr).(*metals.Ptr).Elem)
		f.assign(i.Dst, fmt.Sprintf("(void *)((char *)%s + (int64_t)%s * %d)",
			f.reg(i.Ptr), f.reg(i.N), size))

	case *metals.PtrDiff:
		size := g.sizeOf(f.typeOf(i.A).(*metals.Ptr).Elem)
		f.assign(i.Dst, fmt.Sprintf("(((char *)%s - (char *)%s) / %d)",
			f.reg(i.A), f.reg(i.B), size))

	case *metals.MakeRecord:
		f.emitMakeRecord(i)

	case *metals.FieldGet:
		f.assign(i.Dst, fmt.Sprintf("%s.f%d", f.reg(i.X), i.Index))

	case *metals.FieldSet:
		f.line("%s.f%d = %s;", f.reg(i.X), i.Index, f.reg(i.Val))

	case *metals.MakeVariant:
		f.emitMakeVariant(i)

	case *metals.VariantTag:
		f.assign(i.Dst, f.reg(i.X)+".tag")

	case *metals.VariantField:
		f.assign(i.Dst, fmt.Sprintf("%s.u.v%d.p%d", f.reg(i.X), i.Variant, i.Index))

	case *metals.MakeTuple:
		f.emitMakeTuple(i)

	case *metals.TupleGet:
		f.assign(i.Dst, fmt.Sprintf("%s.e%d", f.reg(i.X), i.Index))

	case *metals.MakeArray:
		f.emitMakeArray(i)

	case *metals.Slice:
		array, ok := f.typeOf(i.X).(*metals.ArrayOf)
		if !ok {
			g.fail("@%s: slice.of a non-array", f.fn.Name)
		}
		// The borrow is of the array register's own storage, exactly the
		// stack slot the asm backend hands out.
		f.assign(i.Dst, fmt.Sprintf("(gy_slice){ (void *)%s.v, %d }", f.reg(i.X), array.Len))

	case *metals.IndexGet:
		element := f.element(i.X, i.Index)
		f.assign(i.Dst, fmt.Sprintf("*(%s *)%s", g.memberType(f.typeOf(i.Dst)), element))

	case *metals.IndexSet:
		element := f.element(i.X, i.Index)
		f.line("*(%s *)%s = %s;", g.memberType(f.typeOf(i.Val)), element, f.reg(i.Val))

	case *metals.Len:
		f.emitLen(i)

	case *metals.StrConcat:
		f.assign(i.Dst, fmt.Sprintf("gy_str_concat(%s, %s)", f.reg(i.A), f.reg(i.B)))

	case *metals.AnyWrap:
		f.emitAnyWrap(i)

	case *metals.AnyUnwrap:
		f.emitAnyUnwrap(i)

	case *metals.AnyIs:
		f.emitAnyIs(i)

	case *metals.AnyGet:
		f.emitAnyGet(i)

	case *metals.AnySet:
		f.emitAnySet(i)

	case *metals.AnyCall:
		f.emitAnyCall(i)

	case *metals.Call:
		f.emitCall(i)

	case *metals.FnRef:
		if g.funcs[i.Callee] == nil {
			g.fail("@%s: fn.ref of the runtime function @%s is not supported", f.fn.Name, i.Callee)
		}
		f.assign(i.Dst, fmt.Sprintf("(gy_fn){ (void (*)(void))%s, 0 }", g.symbolFor(i.Callee)))

	case *metals.MakeClosure:
		if g.funcs[i.Callee] == nil {
			g.fail("@%s: closure over the runtime function @%s is not supported", f.fn.Name, i.Callee)
		}
		f.assign(i.Dst, fmt.Sprintf("(gy_fn){ (void (*)(void))%s, %s }", g.symbolFor(i.Callee), f.reg(i.Env)))

	case *metals.CallIndirect:
		f.emitCallIndirect(i)

	case *metals.Return:
		if i.Val == "" {
			f.line("return;")
		} else {
			f.line("return %s;", f.reg(i.Val))
		}

	case *metals.Abort:
		f.line("gy_abort(%s);", g.strConst(i.Message))

	case *metals.If:
		f.emitIf(i)

	case *metals.Block:
		end := g.newLabel()
		f.line("{")
		f.indent++
		f.withLabel(i.Label, end, i.Body)
		f.indent--
		f.line("}")
		f.label(end)

	case *metals.Loop:
		top := g.newLabel()
		f.label(top)
		f.line("{")
		f.indent++
		f.withLabel(i.Label, top, i.Body)
		f.indent--
		f.line("}")

	case *metals.Br:
		f.line("goto %s;", f.jumpTarget(i.Label))

	case *metals.BrIf:
		f.line("if (%s) goto %s;", f.reg(i.Cond), f.jumpTarget(i.Label))

	case *metals.Switch:
		f.emitSwitch(i)

	case *metals.ChanNew:
		capacity := "0"
		if i.Cap != "" {
			capacity = "(int64_t)" + f.reg(i.Cap)
		}
		f.assign(i.Dst, fmt.Sprintf("gy_chan_new(%s, %d)", capacity, g.sizeOf(i.Elem)))

	case *metals.Send:
		f.stagedOut(i.Val, func(address string) {
			f.line("gy_chan_send(%s, %s);", f.reg(i.Ch), address)
		})

	case *metals.Recv:
		// The element lands in the tuple's own first field, which already
		// holds storage, so nothing is staged here.
		tuple := f.tupleOf(i.Dst)
		f.line("memset(&%s.e0, 0, %d);", f.reg(i.Dst), g.sizeOf(tuple.Elems[0]))
		f.line("%s.e1 = gy_chan_recv(%s, (void *)&%s.e0);", f.reg(i.Dst), f.reg(i.Ch), f.reg(i.Dst))

	case *metals.ChanClose:
		f.line("gy_chan_close(%s);", f.reg(i.Ch))

	case *metals.Await:
		f.stagedIn(i.Dst, func(address string) {
			f.line("gy_future_get(%s, %s);", f.reg(i.X), address)
		})

	case *metals.Select:
		f.emitSelect(i)

	case *metals.OptWrap:
		if opt, ok := f.typeOf(i.Dst).(*metals.Opt); ok && metals.NicheOpt(opt) {
			f.assign(i.Dst, f.reg(i.X))
			break
		}
		f.line("%s.flag = 1;", f.reg(i.Dst))
		f.line("%s.value = %s;", f.reg(i.Dst), f.reg(i.X))

	case *metals.OptUnwrap:
		if opt, ok := f.typeOf(i.X).(*metals.Opt); ok && metals.NicheOpt(opt) {
			f.line("gy_want_value(%s != 0);", f.reg(i.X))
			f.assign(i.Dst, f.reg(i.X))
			break
		}
		f.line("gy_want_value(%s.flag);", f.reg(i.X))
		f.assign(i.Dst, f.reg(i.X)+".value")

	default:
		// The linear-memory instructions carry their own dispatch, so the
		// byte-level machinery stays in memory.go.
		if !f.emitMemory(instr) {
			g.fail("@%s: the C backend does not implement %T yet", f.fn.Name, instr)
		}
	}
}

// deref spells a read or write through a ptr register of the given type.
// The cell holds storage, so the cast is the member type and C's own
// conversion is the widening on the way out and the narrowing on the way
// in.
func (f *frame) deref(ptr string, t metals.Type) string {
	return fmt.Sprintf("*(%s *)%s", f.g.memberType(t), f.reg(ptr))
}

// ---------------------------------------------------------------------------
// Aggregate construction
// ---------------------------------------------------------------------------

func (f *frame) emitMakeRecord(i *metals.MakeRecord) {
	decl := f.g.types[i.TypeName]
	if decl == nil || decl.Record == nil {
		f.g.fail("@%s: record %%%s is not a record", f.fn.Name, i.TypeName)
	}
	if len(i.Args) == 0 {
		return // a record with no fields holds nothing to write
	}
	parts := make([]string, len(i.Args))
	for index, arg := range i.Args {
		parts[index] = f.reg(arg)
	}
	f.assign(i.Dst, fmt.Sprintf("(%s){ %s }", f.g.cType(&metals.Named{Name: i.TypeName}), strings.Join(parts, ", ")))
}

// emitMakeVariant writes the tag and the payload in one designated
// initializer, which zero-fills the rest of the payload area: two
// variants of one enum must never leave each other's bytes behind.
func (f *frame) emitMakeVariant(i *metals.MakeVariant) {
	decl := f.g.types[i.TypeName]
	if decl == nil || decl.Enum == nil {
		f.g.fail("@%s: variant %%%s is not an enum", f.fn.Name, i.TypeName)
	}
	name := f.g.cType(&metals.Named{Name: i.TypeName})
	if len(i.Args) == 0 {
		f.assign(i.Dst, fmt.Sprintf("(%s){ .tag = %d }", name, i.Variant))
		return
	}
	parts := make([]string, len(i.Args))
	for index, arg := range i.Args {
		parts[index] = f.reg(arg)
	}
	f.assign(i.Dst, fmt.Sprintf("(%s){ .tag = %d, .u.v%d = { %s } }",
		name, i.Variant, i.Variant, strings.Join(parts, ", ")))
}

func (f *frame) emitMakeTuple(i *metals.MakeTuple) {
	if len(i.Args) == 0 {
		return
	}
	parts := make([]string, len(i.Args))
	for index, arg := range i.Args {
		parts[index] = f.reg(arg)
	}
	f.assign(i.Dst, fmt.Sprintf("(%s){ %s }", f.g.cType(f.typeOf(i.Dst)), strings.Join(parts, ", ")))
}

func (f *frame) emitMakeArray(i *metals.MakeArray) {
	if len(i.Args) == 0 {
		return
	}
	parts := make([]string, len(i.Args))
	for index, arg := range i.Args {
		parts[index] = f.reg(arg)
	}
	f.assign(i.Dst, fmt.Sprintf("(%s){{ %s }}", f.g.cType(f.typeOf(i.Dst)), strings.Join(parts, ", ")))
}

// ---------------------------------------------------------------------------
// Indexing and lengths
// ---------------------------------------------------------------------------

// element bounds-checks an index and yields the address of the element.
func (f *frame) element(x, index string) string {
	g := f.g
	switch t := f.typeOf(x).(type) {
	case *metals.SliceOf:
		return fmt.Sprintf("gy_elem(%s.data, (int64_t)%s, %s.len, %d)",
			f.reg(x), f.reg(index), f.reg(x), g.sizeOf(t.Elem))
	case *metals.ArrayOf:
		return fmt.Sprintf("gy_elem((void *)%s.v, (int64_t)%s, %d, %d)",
			f.reg(x), f.reg(index), t.Len, g.sizeOf(t.Elem))
	default:
		g.fail("@%s: indexing a %s", f.fn.Name, t)
	}
	return ""
}

func (f *frame) emitLen(i *metals.Len) {
	switch t := f.typeOf(i.X).(type) {
	case *metals.SliceOf:
		f.assign(i.Dst, fmt.Sprintf("(uint64_t)%s.len", f.reg(i.X)))
	case *metals.ArrayOf:
		f.assign(i.Dst, strconv.FormatInt(t.Len, 10))
	case *metals.Prim:
		if t.Kind != metals.Str {
			f.g.fail("@%s: len of a %s", f.fn.Name, t)
		}
		f.assign(i.Dst, fmt.Sprintf("(uint64_t)%s->len", f.reg(i.X)))
	default:
		f.g.fail("@%s: len of a %s", f.fn.Name, t)
	}
}

// ---------------------------------------------------------------------------
// Control flow
// ---------------------------------------------------------------------------

// withLabel binds a METALS label to a C label for the duration of a body,
// restoring any label of the same name it shadows.
func (f *frame) withLabel(name, target string, body []metals.Instr) {
	saved, had := f.labels[name]
	f.labels[name] = target
	f.body(body)
	if had {
		f.labels[name] = saved
	} else {
		delete(f.labels, name)
	}
}

func (f *frame) jumpTarget(label string) string {
	target, ok := f.labels[label]
	if !ok {
		f.g.fail("@%s: br to unknown label $%s", f.fn.Name, label)
	}
	return target
}

func (f *frame) emitIf(i *metals.If) {
	f.line("if (%s) {", f.reg(i.Cond))
	f.indent++
	f.body(i.Then)
	f.indent--
	if len(i.Else) > 0 {
		f.line("} else {")
		f.indent++
		f.body(i.Else)
		f.indent--
	}
	f.line("}")
}

// emitSwitch maps straight onto C's switch. Nothing inside a case body
// ever emits a C loop or switch of its own — METALS control flow becomes
// labels and gotos — so no `break` can be captured by the wrong
// construct.
func (f *frame) emitSwitch(i *metals.Switch) {
	f.line("switch (%s) {", f.reg(i.X))
	for _, c := range i.Cases {
		f.line("case %d: {", c.Value)
		f.indent++
		f.body(c.Body)
		f.indent--
		f.line("} break;")
	}
	if len(i.Default) > 0 {
		f.line("default: {")
		f.indent++
		f.body(i.Default)
		f.indent--
		f.line("} break;")
	}
	f.line("}")
}

// emitSelect polls the arms in order, exactly as the native backend does,
// and sleeps briefly when none is ready and there is no default. The
// evaluator randomizes the order; docs/native.md records that as an
// accepted divergence.
func (f *frame) emitSelect(i *metals.Select) {
	g := f.g
	top := g.newLabel()
	end := g.newLabel()
	f.label(top)

	armLabels := make([]string, len(i.Arms))
	for index, arm := range i.Arms {
		armLabels[index] = g.newLabel()
		if arm.Send {
			f.stagedOut(arm.Val, func(address string) {
				f.line("if (gy_chan_try_send(%s, %s)) goto %s;",
					f.reg(arm.Ch), address, armLabels[index])
			})
			continue
		}
		// The value is read back out of staging whether or not the arm is
		// taken, which costs a copy on a miss and keeps the branch in one
		// place; a staged cell starts zeroed, so a miss leaves the same
		// nothing the memset does.
		taken := g.stageName()
		f.line("{")
		f.indent++
		f.line("int64_t %s;", taken)
		if !metals.Narrow(f.typeOf(arm.Dst)) {
			f.line("memset(&%s, 0, %d);", f.reg(arm.Dst), g.sizeOf(f.typeOf(arm.Dst)))
		}
		f.stagedIn(arm.Dst, func(address string) {
			f.line("%s = gy_chan_try_recv(%s, %s);", taken, f.reg(arm.Ch), address)
		})
		f.line("if (%s) goto %s;", taken, armLabels[index])
		f.indent--
		f.line("}")
	}
	if len(i.Default) > 0 {
		f.line("{")
		f.indent++
		f.body(i.Default)
		f.indent--
		f.line("}")
		f.line("goto %s;", end)
	} else {
		f.line("gy_select_pause();")
		f.line("goto %s;", top)
	}

	for index, arm := range i.Arms {
		f.label(armLabels[index])
		f.line("{")
		f.indent++
		f.body(arm.Body)
		f.indent--
		f.line("}")
		f.line("goto %s;", end)
	}
	f.label(end)
}

// ---------------------------------------------------------------------------
// Type lookups
// ---------------------------------------------------------------------------

func (f *frame) tupleOf(reg string) *metals.TupleOf {
	tuple, ok := f.typeOf(reg).(*metals.TupleOf)
	if !ok {
		f.g.fail("@%s: %%%s is not a tuple", f.fn.Name, reg)
	}
	return tuple
}

func (f *frame) recordBehindPtr(reg string) *metals.RecordShape {
	pointer, ok := f.typeOf(reg).(*metals.Ptr)
	if !ok {
		f.g.fail("@%s: %%%s is not a pointer", f.fn.Name, reg)
	}
	named, ok := pointer.Elem.(*metals.Named)
	if !ok {
		f.g.fail("@%s: %%%s does not point at a record", f.fn.Name, reg)
	}
	decl := f.g.types[named.Name]
	if decl == nil || decl.Record == nil {
		f.g.fail("@%s: %%%s does not point at a record", f.fn.Name, reg)
	}
	return decl.Record
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func (f *frame) emitConst(i *metals.Const) {
	g := f.g
	if i.Null {
		f.line("memset(&%s, 0, %d);", f.reg(i.Dst), g.sizeOf(f.typeOf(i.Dst)))
		return
	}
	prim, ok := i.Type.(*metals.Prim)
	if !ok {
		g.fail("@%s: constant of type %s", f.fn.Name, i.Type)
	}

	switch prim.Kind {
	case metals.Str:
		decoded, err := strconv.Unquote(i.Value)
		if err != nil {
			g.fail("@%s: bad string constant %s", f.fn.Name, i.Value)
		}
		f.assign(i.Dst, g.strConst(decoded))

	case metals.F32, metals.F64:
		parsed, err := strconv.ParseFloat(i.Value, 64)
		if err != nil {
			g.fail("@%s: bad float constant %s", f.fn.Name, i.Value)
		}
		if prim.Kind == metals.F32 {
			// A constant is an operation producing an f32 like any other,
			// so it rounds through single precision before the register
			// carries it as a double.
			parsed = float64(float32(parsed))
		}
		// The bit pattern travels, not the decimal: no round trip through
		// the C compiler's own parser can change the value.
		f.assign(i.Dst, fmt.Sprintf("gy_double_bits(0x%016xULL) /* %s */", floatBits(parsed), i.Value))

	case metals.Bool:
		value := "0"
		if i.Value == "true" {
			value = "1"
		}
		f.assign(i.Dst, value)

	case metals.Rune:
		decoded, _, _, err := strconv.UnquoteChar(i.Value[1:len(i.Value)-1], '\'')
		if err != nil {
			g.fail("@%s: bad rune constant %s", f.fn.Name, i.Value)
		}
		f.assign(i.Dst, signedLiteral(int64(decoded)))

	default:
		kind := intKinds[prim.Kind]
		if kind.signed {
			parsed, err := strconv.ParseInt(i.Value, 0, 64)
			if err != nil {
				g.fail("@%s: bad integer constant %s", f.fn.Name, i.Value)
			}
			f.assign(i.Dst, signedLiteral(parsed))
			return
		}
		parsed, err := strconv.ParseUint(i.Value, 0, 64)
		if err != nil {
			// A negative literal reaching an unsigned register is the
			// front end's business; carry the bits it asked for.
			signed, again := strconv.ParseInt(i.Value, 0, 64)
			if again != nil {
				g.fail("@%s: bad integer constant %s", f.fn.Name, i.Value)
			}
			parsed = uint64(signed)
		}
		f.assign(i.Dst, fmt.Sprintf("UINT64_C(%d)", parsed))
	}
}

// signedLiteral spells an int64 constant. The most negative one has no C
// literal of its own: -9223372036854775808 parses as a negation of a
// value that does not fit, so it is written as a subtraction.
func signedLiteral(value int64) string {
	if value == -9223372036854775808 {
		return "(-9223372036854775807LL - 1)"
	}
	return fmt.Sprintf("INT64_C(%d)", value)
}
