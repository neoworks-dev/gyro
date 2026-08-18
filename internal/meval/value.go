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

	recordVal struct {
		typeName string
		fields   []value
	}
	enumVal struct {
		typeName string
		tag      int
		payload  []value
	}
	tupleVal []value

	arrayVal struct{ elems []value }
	sliceVal struct{ elems []value } // shares its backing: the borrow

	ptrVal  struct{ cell *value }
	chanVal chan value

	// fnVal is a function value: a bare reference or a closure with its
	// bound environment.
	fnVal struct {
		name   string
		env    value
		hasEnv bool
	}

	futureVal struct {
		ch     chan value
		once   sync.Once
		result value
	}

	// handleVal is an opaque runtime object: Array, Mutex, guard, file.
	handleVal struct{ obj any }

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
		return ok && a.cell == b.cell
	case handleVal:
		b, ok := b.(handleVal)
		return ok && a.obj == b.obj
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
		return "&" + m.repr(*v.cell)
	case sliceVal:
		return m.elemsRepr(v.elems)
	case arrayVal:
		return m.elemsRepr(v.elems)
	case chanVal:
		return "chan"
	case fnVal:
		return "fn"
	case *futureVal:
		return "awaitable"
	case handleVal:
		return m.handleRepr(v)
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
