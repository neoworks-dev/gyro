package meval

import (
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Run executes a verified module's exported main. Runtime failures —
// overflow, null unwraps, index errors, explicit aborts — come back as
// an *Abort error after printing nothing; the caller owns reporting.
//
// args is the command line as main would see it, args[0] included; it is
// ignored when main takes no parameters.
func Run(module *metals.Module, args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	m := &machine{
		module:  module,
		args:    args,
		funcs:   map[string]*metals.Func{},
		imports: map[string]*metals.FnType{},
		types:   map[string]*metals.TypeDecl{},
		globals: map[string]*value{},
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
	}
	for _, fn := range module.Funcs {
		m.funcs[fn.Name] = fn
	}
	for _, imp := range module.Imports {
		for _, fn := range imp.Funcs {
			m.imports[fn.Name] = fn.Sig
		}
	}
	for _, decl := range module.Types {
		m.types[decl.Name] = decl
	}
	// Module storage is live for the whole run and starts uninitialized,
	// so reading one before its initializer ran aborts the same way an
	// unwritten local does.
	for _, global := range module.Globals {
		m.globals[global.Name] = m.freshCell(global.Type).(ptrVal).cell
	}

	main := m.funcs["main"]
	if main == nil || !main.Export {
		return fmt.Errorf("meval: the module exports no main")
	}
	defer func() {
		failure := recover()
		if failure == nil {
			return
		}
		if exit, ok := failure.(*Exit); ok {
			err = exit
			return
		}
		if abort, ok := failure.(*Abort); ok {
			err = abort
			return
		}
		panic(failure)
	}()
	result := m.call(main.Name, m.entryArgs(main))
	if future, ok := result.(*futureVal); ok {
		future.await()
	}
	return nil
}

// entryArgs supplies main's arguments, or none when it declares none.
func (m *machine) entryArgs(main *metals.Func) []value {
	if len(main.Params) == 0 {
		return nil
	}
	elems := make([]value, len(m.args))
	for i, arg := range m.args {
		elems[i] = strVal(arg)
	}
	return []value{sliceVal{elems: elems}}
}

// Abort is a METALS abort: the runtime equivalent of a Gyro panic.
type Abort struct{ Message string }

func (a *Abort) Error() string { return "panic: " + a.Message }

// Exit is returned by Run when the program called os.exit: a
// termination, not a failure. The caller owns the process exit code.
type Exit struct{ Code int }

func (e *Exit) Error() string { return fmt.Sprintf("exit %d", e.Code) }

type machine struct {
	module  *metals.Module
	funcs   map[string]*metals.Func
	imports map[string]*metals.FnType
	types   map[string]*metals.TypeDecl
	globals map[string]*value
	args    []string
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
}

func (m *machine) throw(format string, args ...any) {
	panic(&Abort{Message: fmt.Sprintf(format, args...)})
}

// ---------------------------------------------------------------------------
// Calls
// ---------------------------------------------------------------------------

// call invokes a function or import by name: synchronously when it is
// plain, as an eagerly-started future when it is async.
func (m *machine) call(name string, args []value) value {
	if sig, ok := m.imports[name]; ok {
		if sig.Async {
			return m.future(func() value { return m.native(name, args) })
		}
		return m.native(name, args)
	}
	fn := m.funcs[name]
	if fn == nil {
		m.throw("unknown function @%s", name)
	}
	if fn.Async {
		return m.future(func() value { return m.run(fn, args) })
	}
	return m.run(fn, args)
}

func (m *machine) future(compute func() value) value {
	fut := &futureVal{ch: make(chan value, 1)}
	go func() {
		defer func() {
			if failure := recover(); failure != nil {
				if abort, ok := failure.(*Abort); ok {
					fmt.Fprintln(m.stderr, abort.Error())
				} else {
					fmt.Fprintln(m.stderr, "panic:", failure)
				}
				fut.ch <- uninitVal{}
			}
		}()
		fut.ch <- compute()
	}()
	return fut
}

func (m *machine) run(fn *metals.Func, args []value) value {
	regs := map[string]value{}
	for i, param := range fn.Params {
		if i < len(args) {
			regs[param.Name] = copyValue(args[i])
		}
	}
	sig := m.exec(fn.Body, regs)
	if ret, ok := sig.(sigReturn); ok {
		return ret.v
	}
	return nil
}

// callFn invokes a function value, prepending its environment.
func (m *machine) callFn(fn fnVal, args []value) value {
	if fn.hasEnv {
		args = append([]value{fn.env}, args...)
	}
	return m.call(fn.name, args)
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

type signal interface{}

type sigReturn struct{ v value }
type sigBr struct{ label string }

func (m *machine) exec(body []metals.Instr, regs map[string]value) signal {
	for _, instr := range body {
		if sig := m.step(instr, regs); sig != nil {
			return sig
		}
	}
	return nil
}

func (m *machine) get(regs map[string]value, name string) value {
	v, ok := regs[name]
	if !ok {
		m.throw("register %%%s read before assignment", name)
	}
	return v
}

func (m *machine) step(instr metals.Instr, regs map[string]value) signal {
	switch i := instr.(type) {
	case *metals.Const:
		regs[i.Dst] = m.constValue(i)

	case *metals.Copy:
		regs[i.Dst] = copyValue(m.get(regs, i.X))

	case *metals.Bin:
		regs[i.Dst] = m.binary(i.Op, m.get(regs, i.A), m.get(regs, i.B))

	case *metals.Un:
		regs[i.Dst] = m.unary(i.Op, m.get(regs, i.X))

	case *metals.Conv:
		regs[i.Dst] = m.convert(m.get(regs, i.X), i.To)

	case *metals.Slot:
		regs[i.Dst] = m.freshCell(i.Type)
	case *metals.GlobalPtr:
		cell, ok := m.globals[i.Name]
		if !ok {
			m.throw("no global @%s", i.Name)
		}
		regs[i.Dst] = ptrVal{cell: cell}
	case *metals.New:
		regs[i.Dst] = m.freshCell(i.Type)

	case *metals.Free:
		ptr := m.get(regs, i.X).(ptrVal)
		if _, freed := (*ptr.cell).(freedVal); freed {
			m.throw("double free")
		}
		*ptr.cell = freedVal{}

	case *metals.Load:
		ptr := m.get(regs, i.Ptr).(ptrVal)
		switch (*ptr.cell).(type) {
		case freedVal:
			m.throw("use after free")
		case uninitVal:
			m.throw("read of uninitialized memory")
		}
		regs[i.Dst] = copyValue(*ptr.cell)

	case *metals.Store:
		ptr := m.get(regs, i.Ptr).(ptrVal)
		if _, freed := (*ptr.cell).(freedVal); freed {
			m.throw("use after free")
		}
		*ptr.cell = copyValue(m.get(regs, i.Val))

	case *metals.FieldPtr:
		ptr := m.get(regs, i.Ptr).(ptrVal)
		record, ok := (*ptr.cell).(recordVal)
		if !ok {
			if _, freed := (*ptr.cell).(freedVal); freed {
				m.throw("use after free")
			}
			m.throw("field.ptr into a non-record cell")
		}
		regs[i.Dst] = ptrVal{cell: &record.fields[i.Index]}

	case *metals.MakeRecord:
		fields := make([]value, len(i.Args))
		for index, arg := range i.Args {
			fields[index] = copyValue(m.get(regs, arg))
		}
		regs[i.Dst] = recordVal{typeName: i.TypeName, fields: fields}

	case *metals.FieldGet:
		record := m.get(regs, i.X).(recordVal)
		field := record.fields[i.Index]
		if _, uninit := field.(uninitVal); uninit {
			m.throw("read of an unassigned field")
		}
		regs[i.Dst] = copyValue(field)

	case *metals.FieldSet:
		record := m.get(regs, i.X).(recordVal)
		record.fields[i.Index] = copyValue(m.get(regs, i.Val))

	case *metals.MakeVariant:
		payload := make([]value, len(i.Args))
		for index, arg := range i.Args {
			payload[index] = copyValue(m.get(regs, arg))
		}
		regs[i.Dst] = enumVal{typeName: i.TypeName, tag: i.Variant, payload: payload}

	case *metals.VariantTag:
		regs[i.Dst] = intVal{v: int64(m.get(regs, i.X).(enumVal).tag), kind: metals.I32}

	case *metals.VariantField:
		enum := m.get(regs, i.X).(enumVal)
		if enum.tag != i.Variant {
			m.throw("variant.field on the wrong variant")
		}
		regs[i.Dst] = copyValue(enum.payload[i.Index])

	case *metals.MakeTuple:
		elems := make([]value, len(i.Args))
		for index, arg := range i.Args {
			elems[index] = copyValue(m.get(regs, arg))
		}
		regs[i.Dst] = tupleVal(elems)

	case *metals.TupleGet:
		regs[i.Dst] = copyValue(m.get(regs, i.X).(tupleVal)[i.Index])

	case *metals.MakeArray:
		elems := make([]value, len(i.Args))
		for index, arg := range i.Args {
			elems[index] = copyValue(m.get(regs, arg))
		}
		regs[i.Dst] = arrayVal{elems: elems}

	case *metals.Slice:
		regs[i.Dst] = sliceVal{elems: m.get(regs, i.X).(arrayVal).elems}

	case *metals.IndexGet:
		elems := m.indexable(m.get(regs, i.X))
		index := m.index(m.get(regs, i.Index), len(elems))
		regs[i.Dst] = copyValue(elems[index])

	case *metals.IndexSet:
		elems := m.indexable(m.get(regs, i.X))
		index := m.index(m.get(regs, i.Index), len(elems))
		elems[index] = copyValue(m.get(regs, i.Val))

	case *metals.Len:
		switch v := m.get(regs, i.X).(type) {
		case sliceVal:
			regs[i.Dst] = uintVal{v: uint64(len(v.elems)), kind: metals.Usize}
		case arrayVal:
			regs[i.Dst] = uintVal{v: uint64(len(v.elems)), kind: metals.Usize}
		case strVal:
			regs[i.Dst] = uintVal{v: uint64(len(v)), kind: metals.Usize}
		default:
			m.throw("len of a non-sequence")
		}

	case *metals.StrConcat:
		regs[i.Dst] = m.get(regs, i.A).(strVal) + m.get(regs, i.B).(strVal)

	case *metals.Call:
		args := make([]value, len(i.Args))
		for index, arg := range i.Args {
			args[index] = m.get(regs, arg)
		}
		result := m.call(i.Callee, args)
		if i.Dst != "" {
			regs[i.Dst] = result
		}

	case *metals.FnRef:
		regs[i.Dst] = fnVal{name: i.Callee}

	case *metals.MakeClosure:
		regs[i.Dst] = fnVal{name: i.Callee, env: m.get(regs, i.Env), hasEnv: true}

	case *metals.CallIndirect:
		fn, ok := m.get(regs, i.Fn).(fnVal)
		if !ok {
			m.throw("call.indirect of a non-function")
		}
		args := make([]value, len(i.Args))
		for index, arg := range i.Args {
			args[index] = m.get(regs, arg)
		}
		result := m.callFn(fn, args)
		if i.Dst != "" {
			regs[i.Dst] = result
		}

	case *metals.Return:
		if i.Val == "" {
			return sigReturn{}
		}
		return sigReturn{v: copyValue(m.get(regs, i.Val))}

	case *metals.Abort:
		m.throw("%s", i.Message)

	case *metals.If:
		cond := m.get(regs, i.Cond).(boolVal)
		if cond {
			return m.exec(i.Then, regs)
		}
		return m.exec(i.Else, regs)

	case *metals.Block:
		sig := m.exec(i.Body, regs)
		if br, ok := sig.(sigBr); ok && br.label == i.Label {
			return nil
		}
		return sig

	case *metals.Loop:
		for {
			sig := m.exec(i.Body, regs)
			if br, ok := sig.(sigBr); ok && br.label == i.Label {
				continue
			}
			return sig
		}

	case *metals.Br:
		return sigBr{label: i.Label}

	case *metals.BrIf:
		if m.get(regs, i.Cond).(boolVal) {
			return sigBr{label: i.Label}
		}

	case *metals.Switch:
		selector := m.intOf(m.get(regs, i.X))
		for _, c := range i.Cases {
			if int64(c.Value) == selector {
				return m.exec(c.Body, regs)
			}
		}
		return m.exec(i.Default, regs)

	case *metals.ChanNew:
		capacity := 0
		if i.Cap != "" {
			capacity = int(m.intOf(m.get(regs, i.Cap)))
		}
		regs[i.Dst] = chanVal(make(chan value, capacity))

	case *metals.Send:
		m.send(m.get(regs, i.Ch).(chanVal), copyValue(m.get(regs, i.Val)))

	case *metals.Recv:
		element, open := <-m.get(regs, i.Ch).(chanVal)
		if !open {
			element = uninitVal{}
		}
		regs[i.Dst] = tupleVal{element, boolVal(open)}

	case *metals.ChanClose:
		m.closeChan(m.get(regs, i.Ch).(chanVal))

	case *metals.Await:
		future, ok := m.get(regs, i.X).(*futureVal)
		if !ok {
			m.throw("await of a non-future")
		}
		regs[i.Dst] = copyValue(future.await())

	case *metals.Select:
		return m.selectStep(i, regs)

	case *metals.OptWrap:
		regs[i.Dst] = copyValue(m.get(regs, i.X))

	case *metals.OptUnwrap:
		v := m.get(regs, i.X)
		if isNull(v) {
			m.throw("unwrap of a null value")
		}
		regs[i.Dst] = copyValue(v)

	default:
		m.throw("the evaluator does not implement %T yet", instr)
	}
	return nil
}

func (m *machine) send(ch chanVal, v value) {
	defer func() {
		if recover() != nil {
			m.throw("send on a closed channel")
		}
	}()
	ch <- v
}

func (m *machine) closeChan(ch chanVal) {
	defer func() {
		if recover() != nil {
			m.throw("close of a closed channel")
		}
	}()
	close(ch)
}

func (m *machine) selectStep(sel *metals.Select, regs map[string]value) signal {
	cases := make([]reflect.SelectCase, 0, len(sel.Arms)+1)
	for _, arm := range sel.Arms {
		ch := m.get(regs, arm.Ch).(chanVal)
		if arm.Send {
			cases = append(cases, reflect.SelectCase{
				Dir:  reflect.SelectSend,
				Chan: reflect.ValueOf((chan value)(ch)),
				Send: reflect.ValueOf(copyValue(m.get(regs, arm.Val))),
			})
			continue
		}
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf((chan value)(ch)),
		})
	}
	hasDefault := len(sel.Default) > 0
	if hasDefault {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectDefault})
	}

	chosen, received, open := reflect.Select(cases)
	if hasDefault && chosen == len(sel.Arms) {
		return m.exec(sel.Default, regs)
	}
	arm := sel.Arms[chosen]
	if !arm.Send {
		element := value(uninitVal{})
		if open {
			element = received.Interface()
		}
		regs[arm.Dst] = element
	}
	return m.exec(arm.Body, regs)
}

// freshCell allocates an uninitialized cell; a record type materializes
// with unassigned fields so field.ptr can reach into it.
func (m *machine) freshCell(t metals.Type) value {
	cell := new(value)
	*cell = uninitVal{}
	if named, ok := t.(*metals.Named); ok {
		if decl := m.types[named.Name]; decl != nil && decl.Record != nil {
			fields := make([]value, len(decl.Record.Fields))
			for i := range fields {
				fields[i] = uninitVal{}
			}
			*cell = recordVal{typeName: named.Name, fields: fields}
		}
	}
	return ptrVal{cell: cell}
}

func (m *machine) indexable(v value) []value {
	switch v := v.(type) {
	case sliceVal:
		return v.elems
	case arrayVal:
		return v.elems
	}
	m.throw("indexing a non-sequence")
	return nil
}

func (m *machine) index(v value, length int) int {
	index := m.intOf(v)
	if index < 0 || index >= int64(length) {
		m.throw("index %d out of range for length %d", index, length)
	}
	return int(index)
}

func (m *machine) intOf(v value) int64 {
	switch v := v.(type) {
	case intVal:
		return v.v
	case uintVal:
		return int64(v.v)
	}
	m.throw("expected an integer")
	return 0
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func (m *machine) constValue(i *metals.Const) value {
	if i.Null {
		return nullVal{}
	}
	prim, ok := i.Type.(*metals.Prim)
	if !ok {
		m.throw("const of a non-primitive")
	}
	switch prim.Kind {
	case metals.I8, metals.I16, metals.I32, metals.I64:
		parsed, err := strconv.ParseInt(i.Value, 0, 64)
		if err != nil {
			m.throw("bad integer constant %q", i.Value)
		}
		return intVal{v: parsed, kind: prim.Kind}
	case metals.U8, metals.U16, metals.U32, metals.U64, metals.Usize:
		parsed, err := strconv.ParseUint(i.Value, 0, 64)
		if err != nil {
			m.throw("bad integer constant %q", i.Value)
		}
		return uintVal{v: parsed, kind: prim.Kind}
	case metals.F32, metals.F64:
		parsed, err := strconv.ParseFloat(i.Value, 64)
		if err != nil {
			m.throw("bad float constant %q", i.Value)
		}
		return floatVal{v: parsed, kind: prim.Kind}
	case metals.Bool:
		return boolVal(i.Value == "true")
	case metals.Rune:
		if len(i.Value) < 2 {
			m.throw("bad rune constant %q", i.Value)
		}
		decoded, _, _, err := strconv.UnquoteChar(i.Value[1:len(i.Value)-1], '\'')
		if err != nil {
			m.throw("bad rune constant %q", i.Value)
		}
		return intVal{v: int64(decoded), kind: metals.Rune}
	case metals.Str:
		decoded, err := strconv.Unquote(i.Value)
		if err != nil {
			m.throw("bad string constant %q", i.Value)
		}
		return strVal(decoded)
	}
	m.throw("bad constant")
	return nil
}

// ---------------------------------------------------------------------------
// Arithmetic
// ---------------------------------------------------------------------------

var intBounds = map[metals.PrimKind][2]int64{
	metals.I8:   {math.MinInt8, math.MaxInt8},
	metals.I16:  {math.MinInt16, math.MaxInt16},
	metals.I32:  {math.MinInt32, math.MaxInt32},
	metals.I64:  {math.MinInt64, math.MaxInt64},
	metals.Rune: {math.MinInt32, math.MaxInt32},
}

var uintMax = map[metals.PrimKind]uint64{
	metals.U8:    math.MaxUint8,
	metals.U16:   math.MaxUint16,
	metals.U32:   math.MaxUint32,
	metals.U64:   math.MaxUint64,
	metals.Usize: math.MaxUint64,
}

func (m *machine) binary(op string, a, b value) value {
	switch op {
	case "eq":
		return boolVal(valuesEqual(a, b))
	case "ne":
		return boolVal(!valuesEqual(a, b))
	case "lt", "le", "gt", "ge":
		return m.ordered(op, a, b)
	}

	switch left := a.(type) {
	case intVal:
		right, ok := b.(intVal)
		if !ok {
			m.throw("mixed operands for %s", op)
		}
		return m.intBinary(op, left, right)
	case uintVal:
		right, ok := b.(uintVal)
		if !ok {
			m.throw("mixed operands for %s", op)
		}
		return m.uintBinary(op, left, right)
	case floatVal:
		right, ok := b.(floatVal)
		if !ok {
			m.throw("mixed operands for %s", op)
		}
		return m.floatBinary(op, left, right)
	}
	m.throw("bad operands for %s", op)
	return nil
}

func (m *machine) ordered(op string, a, b value) value {
	var cmp int
	switch left := a.(type) {
	case intVal:
		right := b.(intVal)
		cmp = compareInt(left.v, right.v)
	case uintVal:
		right := b.(uintVal)
		cmp = compareUint(left.v, right.v)
	case floatVal:
		right := b.(floatVal)
		switch {
		case left.v < right.v:
			cmp = -1
		case left.v > right.v:
			cmp = 1
		}
	default:
		m.throw("ordering non-numeric values")
	}
	switch op {
	case "lt":
		return boolVal(cmp < 0)
	case "le":
		return boolVal(cmp <= 0)
	case "gt":
		return boolVal(cmp > 0)
	default:
		return boolVal(cmp >= 0)
	}
}

func compareInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func compareUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (m *machine) intBinary(op string, a, b intVal) value {
	bounds := intBounds[a.kind]
	fit := func(v int64) value {
		if v < bounds[0] || v > bounds[1] {
			m.throw("integer overflow")
		}
		return intVal{v: v, kind: a.kind}
	}
	wrap := func(v int64) value {
		width := intWidth(a.kind)
		masked := uint64(v) & widthMask(width)
		return intVal{v: signExtend(masked, width), kind: a.kind}
	}
	switch op {
	case "iadd":
		sum := a.v + b.v
		if (a.v > 0 && b.v > 0 && sum < a.v) || (a.v < 0 && b.v < 0 && sum > a.v) {
			m.throw("integer overflow")
		}
		return fit(sum)
	case "isub":
		return fit(a.v - b.v)
	case "imul":
		if a.v != 0 && b.v != 0 {
			product := a.v * b.v
			if product/a.v != b.v {
				m.throw("integer overflow")
			}
			return fit(product)
		}
		return fit(0)
	case "idiv":
		if b.v == 0 {
			m.throw("division by zero")
		}
		if a.v == bounds[0] && b.v == -1 {
			m.throw("integer overflow")
		}
		return fit(a.v / b.v)
	case "irem":
		if b.v == 0 {
			m.throw("division by zero")
		}
		return fit(a.v % b.v)
	case "wadd":
		return wrap(a.v + b.v)
	case "wsub":
		return wrap(a.v - b.v)
	case "wmul":
		return wrap(a.v * b.v)
	case "band":
		return intVal{v: a.v & b.v, kind: a.kind}
	case "bor":
		return intVal{v: a.v | b.v, kind: a.kind}
	case "bxor":
		return intVal{v: a.v ^ b.v, kind: a.kind}
	case "shl":
		return wrap(a.v << clampShift(b.v))
	case "shr":
		return intVal{v: a.v >> clampShift(b.v), kind: a.kind}
	}
	m.throw("unknown integer operator %s", op)
	return nil
}

func (m *machine) uintBinary(op string, a, b uintVal) value {
	max := uintMax[a.kind]
	fit := func(v uint64) value {
		if v > max {
			m.throw("integer overflow")
		}
		return uintVal{v: v, kind: a.kind}
	}
	wrap := func(v uint64) value {
		return uintVal{v: v & max, kind: a.kind}
	}
	switch op {
	case "iadd":
		sum := a.v + b.v
		if sum < a.v {
			m.throw("integer overflow")
		}
		return fit(sum)
	case "isub":
		if b.v > a.v {
			m.throw("integer overflow")
		}
		return fit(a.v - b.v)
	case "imul":
		if a.v != 0 {
			product := a.v * b.v
			if product/a.v != b.v {
				m.throw("integer overflow")
			}
			return fit(product)
		}
		return fit(0)
	case "idiv":
		if b.v == 0 {
			m.throw("division by zero")
		}
		return fit(a.v / b.v)
	case "irem":
		if b.v == 0 {
			m.throw("division by zero")
		}
		return fit(a.v % b.v)
	case "wadd":
		return wrap(a.v + b.v)
	case "wsub":
		return wrap(a.v - b.v)
	case "wmul":
		return wrap(a.v * b.v)
	case "band":
		return uintVal{v: a.v & b.v, kind: a.kind}
	case "bor":
		return uintVal{v: a.v | b.v, kind: a.kind}
	case "bxor":
		return uintVal{v: a.v ^ b.v, kind: a.kind}
	case "shl":
		return wrap(a.v << clampShift(int64(b.v)))
	case "shr":
		return uintVal{v: a.v >> clampShift(int64(b.v)), kind: a.kind}
	}
	m.throw("unknown integer operator %s", op)
	return nil
}

func (m *machine) floatBinary(op string, a, b floatVal) value {
	switch op {
	case "fadd":
		return floatVal{v: a.v + b.v, kind: a.kind}
	case "fsub":
		return floatVal{v: a.v - b.v, kind: a.kind}
	case "fmul":
		return floatVal{v: a.v * b.v, kind: a.kind}
	case "fdiv":
		return floatVal{v: a.v / b.v, kind: a.kind}
	}
	m.throw("unknown float operator %s", op)
	return nil
}

func clampShift(count int64) uint {
	if count < 0 {
		return 64
	}
	if count > 64 {
		return 64
	}
	return uint(count)
}

func intWidth(kind metals.PrimKind) uint {
	switch kind {
	case metals.I8:
		return 8
	case metals.I16:
		return 16
	case metals.I32, metals.Rune:
		return 32
	}
	return 64
}

func widthMask(width uint) uint64 {
	if width >= 64 {
		return math.MaxUint64
	}
	return (uint64(1) << width) - 1
}

func signExtend(v uint64, width uint) int64 {
	if width >= 64 {
		return int64(v)
	}
	sign := uint64(1) << (width - 1)
	if v&sign != 0 {
		return int64(v | ^widthMask(width))
	}
	return int64(v)
}

func (m *machine) unary(op string, v value) value {
	switch op {
	case "not":
		return boolVal(!v.(boolVal))
	case "neg":
		switch v := v.(type) {
		case intVal:
			return m.intBinary("isub", intVal{v: 0, kind: v.kind}, v)
		case floatVal:
			return floatVal{v: -v.v, kind: v.kind}
		}
	case "bnot":
		switch v := v.(type) {
		case intVal:
			return intVal{v: ^v.v, kind: v.kind}
		case uintVal:
			return uintVal{v: (^v.v) & uintMax[v.kind], kind: v.kind}
		}
	}
	m.throw("bad operand for %s", op)
	return nil
}

// ---------------------------------------------------------------------------
// Conversions
// ---------------------------------------------------------------------------

// convert is `as`: widening succeeds, a narrowing that loses the value
// aborts, floats truncate toward zero first.
func (m *machine) convert(v value, to metals.Type) value {
	prim, ok := to.(*metals.Prim)
	if !ok {
		m.throw("conv to a non-primitive")
	}

	switch prim.Kind {
	case metals.F32, metals.F64:
		switch v := v.(type) {
		case intVal:
			return floatVal{v: float64(v.v), kind: prim.Kind}
		case uintVal:
			return floatVal{v: float64(v.v), kind: prim.Kind}
		case floatVal:
			if prim.Kind == metals.F32 {
				return floatVal{v: float64(float32(v.v)), kind: prim.Kind}
			}
			return floatVal{v: v.v, kind: prim.Kind}
		}
		m.throw("cannot convert to %s", prim)
	}

	// An integer or rune target: get the source onto the number line.
	var negative bool
	var magnitude uint64
	switch v := v.(type) {
	case intVal:
		if v.v < 0 {
			negative = true
			magnitude = uint64(-v.v)
		} else {
			magnitude = uint64(v.v)
		}
	case uintVal:
		magnitude = v.v
	case floatVal:
		truncated := math.Trunc(v.v)
		if truncated != v.v {
			// Truncation toward zero is the defined behavior, not a loss.
		}
		if truncated < 0 {
			negative = true
			magnitude = uint64(-truncated)
		} else {
			magnitude = uint64(truncated)
		}
	default:
		m.throw("cannot convert this value")
	}

	if bounds, ok := intBounds[prim.Kind]; ok {
		var signed int64
		if negative {
			if magnitude > uint64(math.MaxInt64) {
				m.throw("conversion loses the value")
			}
			signed = -int64(magnitude)
		} else {
			if magnitude > uint64(math.MaxInt64) {
				m.throw("conversion loses the value")
			}
			signed = int64(magnitude)
		}
		if signed < bounds[0] || signed > bounds[1] {
			m.throw("conversion loses the value")
		}
		return intVal{v: signed, kind: prim.Kind}
	}
	if max, ok := uintMax[prim.Kind]; ok {
		if negative && magnitude != 0 {
			m.throw("conversion loses the value")
		}
		if magnitude > max {
			m.throw("conversion loses the value")
		}
		return uintVal{v: magnitude, kind: prim.Kind}
	}
	m.throw("cannot convert to %s", prim)
	return nil
}
