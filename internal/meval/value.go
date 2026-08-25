// Package meval is a tree-walking evaluator for verified METALS modules.
// It exists as the oracle for the Gyro-to-METALS lowering: tests run a
// program through the interpreter and through lower+eval and diff the
// output, and the native backend will later be measured against it too.
package meval

import (
	"strconv"
	"strings"
	"sync"

	"github.com/gyro-lang/gyro/internal/metals"
)

// value is a runtime METALS value.
type value interface{}

type (
	intVal struct {
		v    int64
		kind metals.PrimKind // i8..i64 and rune
	}
	uintVal struct {
		v    uint64
		kind metals.PrimKind // u8..u64 and usize
	}
	floatVal struct {
		v    float64
		kind metals.PrimKind
	}
	boolVal bool
	strVal  string

	// nullVal inhabits opt<T>; any non-null value in an opt slot is the
	// value itself, since opts do not nest.
	nullVal struct{}
	// uninitVal is what new produces and a closed channel yields.
	uninitVal struct{}
	// freedVal poisons a released cell.
	freedVal struct{}

	// staleVal is storage a union no longer holds: the member it names
	// was overwritten by a member the two share no initial sequence
	// with. Reading one is what C leaves undefined, and this carries the
	// sentence to say so with.
	staleVal struct{ why string }

	recordVal struct {
		typeName string
		fields   []value
	}

	// unionVal is a union's storage: which member is live, and its
	// value. C's union is bytes with nothing recording which member
	// wrote them, and this evaluator has no bytes — so it records the
	// member instead, which is strictly more than C keeps and exactly
	// what it takes to tell a defined read from an undefined one.
	//
	// cell is one element rather than a bare value so that a pointer to
	// the live member is an ordinary pointer into it, and everything
	// downstream — load, store, elem.ptr, another field.ptr — works
	// without knowing a union was involved.
	unionVal struct {
		typeName string
		// member is which one holds the value, or -1 for storage nothing
		// has written yet.
		member int
		cell   []value
	}
	enumVal struct {
		typeName string
		tag      int
		payload  []value
	}
	tupleVal []value

	arrayVal struct{ elems []value }
	sliceVal struct{ elems []value } // shares its backing: the borrow

	// ptrVal is a pointer: the object's cells and which one it points
	// at. It is not one *value because pointer arithmetic exists — the
	// cell after this one has to be nameable — and it is not a raw
	// address because the evaluator has no memory to address. The
	// backing slice is the extent, which is exactly the object C says
	// arithmetic stays inside; index may reach len, which is the one
	// past the end C allows a program to form.
	ptrVal struct {
		cells []value
		index int

		// owner and member are set on a pointer to a union member that
		// is not the one the union currently holds. The cells are that
		// member's storage read out of what is there — the common
		// initial sequence, where C defines one — so a read through it
		// is answered without disturbing the member that is live.
		//
		// A write is a different question: writing a member is what
		// makes it live, so a store through such a pointer installs
		// these cells as the union's, and so does reaching further in
		// with elem.ptr or another field.ptr, because treating storage
		// as a member's sub-object is treating it as that member.
		owner  *value
		member int
	}
	chanVal chan value

	// fnVal is a function value: a bare reference or a closure with its
	// bound environment.
	fnVal struct {
		name   string
		env    value
		hasEnv bool
	}

	// codeVal is the address of a function and nothing else, which is
	// what `code.ref` produces. It is a separate value from fnVal
	// because it is a separate type: one word rather than two, no
	// environment to bind, and comparable — C compares two function
	// pointers, and what it compares is which function they name.
	codeVal struct{ name string }

	futureVal struct {
		ch     chan value
		once   sync.Once
		result value
	}

	// handleVal is an opaque runtime object: Array, Mutex, guard, file.
	handleVal struct{ obj any }

	// anyVal is the dynamic escape hatch: the static type a value was
	// wrapped at, plus the value. any.is and any.unwrap test against the
	// recorded type, which is why an any can be received from a producer
	// this module knows nothing about.
	//
	// The payload is a shared cell, mirroring the native representation
	// — two words, the second pointing at the value — so any.set through
	// one reference is visible through every other. Gyro cannot observe
	// the sharing: its `any` has no mutation form.
	anyVal struct {
		typ  metals.Type
		cell *value
	}

	arrayObj struct {
		elems []value
		freed bool
	}
	mutexObj struct {
		mu  sync.Mutex
		val value
	}
	guardObj struct {
		mutex *mutexObj
		done  bool
	}
	fileObj struct {
		name   string
		closed bool
	}
)

func (f *futureVal) await() value {
	f.once.Do(func() {
		f.result = <-f.ch
	})
	return f.result
}

// samePointer is pointer equality: the same object, at the same
// element. Two pointers into one object compare by index, which is what
// makes a walk's `p == end` terminate.
func samePointer(a, b ptrVal) bool {
	// Two pointers into one union are one address, whichever members
	// they name — sharing storage is the whole of what a union is. So
	// is a pointer to the union itself, which is where its first member
	// begins.
	if a.owner != nil || b.owner != nil {
		return unionAddress(a) == unionAddress(b)
	}
	if a.index != b.index || len(a.cells) != len(b.cells) {
		return false
	}
	if len(a.cells) == 0 {
		return true
	}
	return &a.cells[0] == &b.cells[0]
}

// unionAddress is the cell a pointer to a union or to one of its
// members names, which is the same cell either way.
func unionAddress(p ptrVal) *value {
	if p.owner != nil {
		return p.owner
	}
	if p.index < 0 || p.index >= len(p.cells) {
		return nil
	}
	return &p.cells[p.index]
}

func isNull(v value) bool {
	_, ok := v.(nullVal)
	return ok
}

// copyValue is assignment semantics: records, enums, tuples and arrays
// copy; slices, pointers, channels, handles and functions share.
func copyValue(v value) value {
	switch v := v.(type) {
	case recordVal:
		fields := make([]value, len(v.fields))
		for i, field := range v.fields {
			fields[i] = copyValue(field)
		}
		return recordVal{typeName: v.typeName, fields: fields}
	case enumVal:
		payload := make([]value, len(v.payload))
		for i, p := range v.payload {
			payload[i] = copyValue(p)
		}
		return enumVal{typeName: v.typeName, tag: v.tag, payload: payload}
	case unionVal:
		// The cell is copied and not shared: a union assigns like every
		// other aggregate, and the copy's members are its own.
		return unionVal{
			typeName: v.typeName,
			member:   v.member,
			cell:     []value{copyValue(v.cell[0])},
		}
	case tupleVal:
		elems := make([]value, len(v))
		for i, e := range v {
			elems[i] = copyValue(e)
		}
		return tupleVal(elems)
	case arrayVal:
		elems := make([]value, len(v.elems))
		for i, e := range v.elems {
			elems[i] = copyValue(e)
		}
		return arrayVal{elems: elems}
	}
	return v
}

// ---------------------------------------------------------------------------
// Equality
// ---------------------------------------------------------------------------

func valuesEqual(a, b value) bool {
	if isNull(a) || isNull(b) {
		return isNull(a) && isNull(b)
	}
	switch a := a.(type) {
	case intVal:
		b, ok := b.(intVal)
		return ok && a.v == b.v
	case uintVal:
		b, ok := b.(uintVal)
		return ok && a.v == b.v
	case floatVal:
		b, ok := b.(floatVal)
		return ok && a.v == b.v
	case boolVal:
		b, ok := b.(boolVal)
		return ok && a == b
	case strVal:
		b, ok := b.(strVal)
		return ok && a == b
	case ptrVal:
		b, ok := b.(ptrVal)
		return ok && samePointer(a, b)
	case handleVal:
		b, ok := b.(handleVal)
		return ok && a.obj == b.obj
	case codeVal:
		b, ok := b.(codeVal)
		return ok && a.name == b.name
	case anyVal:
		// Two anys are equal when they hold the same type and equal
		// values: an i64 1 and an f64 1.0 are different values, and
		// comparing them answers false rather than aborting.
		b, ok := b.(anyVal)
		return ok && metals.TypesEqual(a.typ, b.typ) && valuesEqual(*a.cell, *b.cell)
	case tupleVal:
		b, ok := b.(tupleVal)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !valuesEqual(a[i], b[i]) {
				return false
			}
		}
		return true
	case recordVal:
		b, ok := b.(recordVal)
		if !ok || a.typeName != b.typeName || len(a.fields) != len(b.fields) {
			return false
		}
		for i := range a.fields {
			if !valuesEqual(a.fields[i], b.fields[i]) {
				return false
			}
		}
		return true
	case arrayVal:
		// A fixed-size array is a value, so it compares elementwise. A
		// slice does not reach here: Gyro refuses == on a view.
		b, ok := b.(arrayVal)
		if !ok || len(a.elems) != len(b.elems) {
			return false
		}
		for i := range a.elems {
			if !valuesEqual(a.elems[i], b.elems[i]) {
				return false
			}
		}
		return true
	case enumVal:
		b, ok := b.(enumVal)
		if !ok || a.typeName != b.typeName || a.tag != b.tag {
			return false
		}
		for i := range a.payload {
			if !valuesEqual(a.payload[i], b.payload[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Display
// ---------------------------------------------------------------------------

// display renders a value the way println shows it: strings raw at the
// top level, everything else via repr. This mirrors the interpreter.
func (m *machine) display(v value) string {
	// An any is transparent here too, so displaying one that holds a
	// string prints the text rather than a quoted literal.
	if box, ok := v.(anyVal); ok {
		return m.display(*box.cell)
	}
	if s, ok := v.(strVal); ok {
		return string(s)
	}
	return m.repr(v)
}

func (m *machine) repr(v value) string {
	switch v := v.(type) {
	case intVal:
		if v.kind == metals.Rune {
			return string(rune(v.v))
		}
		return strconv.FormatInt(v.v, 10)
	case uintVal:
		return strconv.FormatUint(v.v, 10)
	case floatVal:
		return strconv.FormatFloat(v.v, 'g', -1, 64)
	case boolVal:
		return strconv.FormatBool(bool(v))
	case strVal:
		return strconv.Quote(string(v))
	case nullVal:
		return "null"
	case uninitVal:
		return "<uninitialized>"
	case freedVal:
		return "<freed>"
	case tupleVal:
		parts := make([]string, len(v))
		for i, e := range v {
			parts[i] = m.repr(e)
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case recordVal:
		return m.recordRepr(v)
	case enumVal:
		return m.enumRepr(v)
	case ptrVal:
		if v.index < 0 || v.index >= len(v.cells) {
			return "&<past the end>"
		}
		return "&" + m.repr(v.cells[v.index])
	case sliceVal:
		return m.elemsRepr(v.elems)
	case arrayVal:
		return m.elemsRepr(v.elems)
	case chanVal:
		return "chan"
	case fnVal:
		return "fn"
	case codeVal:
		return "code"
	case *futureVal:
		return "awaitable"
	case handleVal:
		return m.handleRepr(v)
	case anyVal:
		// An any shows what it holds. The wrapper is a representation
		// choice, not part of the value the program printed.
		return m.repr(*v.cell)
	}
	return "?"
}

func (m *machine) elemsRepr(elems []value) string {
	parts := make([]string, len(elems))
	for i, e := range elems {
		parts[i] = m.repr(e)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (m *machine) recordRepr(v recordVal) string {
	decl := m.types[v.typeName]
	parts := make([]string, len(v.fields))
	for i, field := range v.fields {
		name := "?"
		if decl != nil && decl.Record != nil && i < len(decl.Record.Fields) {
			name = decl.Record.Fields[i].Name
		}
		parts[i] = name + ": " + m.repr(field)
	}
	return v.typeName + "{ " + strings.Join(parts, ", ") + " }"
}

func (m *machine) enumRepr(v enumVal) string {
	variant := "?"
	if decl := m.types[v.typeName]; decl != nil && decl.Enum != nil {
		if v.tag >= 0 && v.tag < len(decl.Enum.Variants) {
			variant = decl.Enum.Variants[v.tag].Name
		}
	}
	if len(v.payload) == 0 {
		return v.typeName + "::" + variant
	}
	parts := make([]string, len(v.payload))
	for i, p := range v.payload {
		parts[i] = m.repr(p)
	}
	return v.typeName + "::" + variant + "(" + strings.Join(parts, ", ") + ")"
}

func (m *machine) handleRepr(v handleVal) string {
	switch obj := v.obj.(type) {
	case *arrayObj:
		return "Array" + m.elemsRepr(obj.elems)
	case *mutexObj:
		return "Mutex"
	case *guardObj:
		return "MutexGuard"
	case *fileObj:
		return "File(" + obj.name + ")"
	}
	return "handle"
}
