// Package interp is a tree-walking interpreter over the checked syntax
// tree. It is the executable specification of Gyro's runtime semantics —
// overflow panics, block-exit defer, capture-by-copy — that the native
// backend will later be measured against.
package interp

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// Value is a runtime value. The concrete types below cover the whole
// language; error values are ordinary struct values sitting in an error?
// slot, and optionals hold either their value or Null.
type Value interface{}

type (
	// IntVal carries every signed integer, rune included. Kind drives the
	// width for overflow checks; UntypedInt adapts at its first use.
	IntVal struct {
		V    int64
		Kind types.BasicKind
	}
	// UintVal carries u8..u64 and usize.
	UintVal struct {
		V    uint64
		Kind types.BasicKind
	}
	FloatVal struct {
		V    float64
		Kind types.BasicKind
	}
	BoolVal   bool
	StringVal string
	NullVal   struct{}
	// UninitVal is what alloc produces and what a closed channel yields;
	// reading one is a panic. It is how "no zero values" behaves at
	// runtime until definite assignment lands statically.
	UninitVal struct{}
	// FreedVal replaces a cell released by free; any use panics.
	FreedVal struct{}

	TupleVal []Value

	StructVal struct {
		Def    *typeDef
		Fields []Value
	}
	EnumVal struct {
		Def     *typeDef
		Variant string
		Payload []Value
	}

	// PointerVal points at a cell: a variable, a field, an element or an
	// allocation.
	PointerVal struct {
		Cell *Value
	}

	// SliceVal borrows backing storage; ArrayVal owns a fixed-size block.
	// Both share Go slice aliasing, which is exactly the borrow semantics.
	SliceVal struct {
		Elems []Value
	}
	ArrayVal struct {
		Elems []Value
	}

	ChanVal chan Value

	ClosureVal struct {
		Decl *ast.ClosureExpr
		Env  *env
		// Module is the defining module: its scope is what the body sees.
		Module *moduleEnv
		// Def is set instead of Decl for a named function used as a value.
		Def   *funcDef
		Async bool
	}

	FutureVal struct {
		ch     chan Value
		once   sync.Once
		result Value
	}

	// ArrayObj is the stdlib Array<T>: an owned, growable buffer.
	ArrayObj struct {
		Elems []Value
		Freed bool
	}
	// StringObj is the stdlib String: an owned, growable byte buffer. The
	// bytes are UTF-8 and nothing validates that, the same way `string`
	// does not.
	StringObj struct {
		Bytes []byte
		Freed bool
	}
	MutexObj struct {
		mu    sync.Mutex
		value Value
	}
	GuardObj struct {
		mutex *MutexObj
		done  bool
	}
	FileObj struct {
		name   string
		closed bool
	}

	// NativeError is an error minted by the runtime; its message() is the
	// string it carries.
	NativeError struct {
		Message string
	}
)

func (f *FutureVal) await() Value {
	f.once.Do(func() {
		f.result = <-f.ch
	})
	return f.result
}

func isNull(v Value) bool {
	_, ok := v.(NullVal)
	return ok
}

// copyValue is assignment semantics: structs, tuples and fixed arrays copy;
// slices, pointers, channels and the reference-shaped natives share.
func copyValue(v Value) Value {
	switch v := v.(type) {
	case StructVal:
		fields := make([]Value, len(v.Fields))
		for i, field := range v.Fields {
			fields[i] = copyValue(field)
		}
		return StructVal{Def: v.Def, Fields: fields}
	case EnumVal:
		payload := make([]Value, len(v.Payload))
		for i, p := range v.Payload {
			payload[i] = copyValue(p)
		}
		return EnumVal{Def: v.Def, Variant: v.Variant, Payload: payload}
	case TupleVal:
		elems := make([]Value, len(v))
		for i, e := range v {
			elems[i] = copyValue(e)
		}
		return TupleVal(elems)
	case ArrayVal:
		elems := make([]Value, len(v.Elems))
		for i, e := range v.Elems {
			elems[i] = copyValue(e)
		}
		return ArrayVal{Elems: elems}
	}
	return v
}

// ---------------------------------------------------------------------------
// Printing
// ---------------------------------------------------------------------------

// display renders a value the way println and #{} show it: strings raw at
// the top level, quoted when nested; a to_string method wins over the
// derived representation.
func (in *interp) display(v Value) string {
	if s, ok := v.(StringVal); ok {
		return string(s)
	}
	return in.repr(v)
}

func (in *interp) repr(v Value) string {
	switch v := v.(type) {
	case IntVal:
		if v.Kind == types.RuneKind {
			return string(rune(v.V))
		}
		return strconv.FormatInt(v.V, 10)
	case UintVal:
		return strconv.FormatUint(v.V, 10)
	case FloatVal:
		return strconv.FormatFloat(v.V, 'g', -1, 64)
	case BoolVal:
		return strconv.FormatBool(bool(v))
	case StringVal:
		return strconv.Quote(string(v))
	case NullVal:
		return "null"
	case UninitVal:
		return "<uninitialized>"
	case TupleVal:
		parts := make([]string, len(v))
		for i, e := range v {
			parts[i] = in.repr(e)
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case StructVal:
		return in.structRepr(v)
	case EnumVal:
		if len(v.Payload) == 0 {
			return v.Def.name + "::" + v.Variant
		}
		parts := make([]string, len(v.Payload))
		for i, p := range v.Payload {
			parts[i] = in.repr(p)
		}
		return v.Def.name + "::" + v.Variant + "(" + strings.Join(parts, ", ") + ")"
	case PointerVal:
		return "&" + in.repr(*v.Cell)
	case SliceVal:
		return in.elemsRepr(v.Elems)
	case ArrayVal:
		return in.elemsRepr(v.Elems)
	case ChanVal:
		return "chan"
	case *ClosureVal:
		return "fn"
	case *FutureVal:
		return "awaitable"
	case *ArrayObj:
		return "Array" + in.elemsRepr(v.Elems)
	case *StringObj:
		return "String(" + strconv.Quote(string(v.Bytes)) + ")"
	case *MutexObj:
		return "Mutex"
	case *GuardObj:
		return "MutexGuard"
	case *FileObj:
		return "File(" + v.name + ")"
	case NativeError:
		return "error(" + strconv.Quote(v.Message) + ")"
	}
	return fmt.Sprintf("%v", v)
}

func (in *interp) elemsRepr(elems []Value) string {
	parts := make([]string, len(elems))
	for i, e := range elems {
		parts[i] = in.repr(e)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// structRepr is the compiler-derived representation, unless the struct
// declares its own to_string.
func (in *interp) structRepr(v StructVal) string {
	if def := v.Def.module.methodOf(v.Def.name, "to_string"); def != nil {
		receiver := Value(v)
		if def.decl.Recv != nil && def.decl.Recv.Pointer {
			receiver = PointerVal{Cell: cellOf(v)}
		}
		result := in.callFunction(def, receiver, nil)
		if s, ok := result.(StringVal); ok {
			return string(s)
		}
	}

	strct := v.Def.structDecl
	parts := make([]string, len(v.Fields))
	for i, field := range v.Fields {
		name := "?"
		if strct != nil && i < len(strct.Fields) {
			name = strct.Fields[i].Name
		}
		parts[i] = name + ": " + in.repr(field)
	}
	return v.Def.name + "{ " + strings.Join(parts, ", ") + " }"
}

func cellOf(v Value) *Value {
	cell := new(Value)
	*cell = v
	return cell
}

// ---------------------------------------------------------------------------
// Equality and comparison
// ---------------------------------------------------------------------------

// valuesEqual is the derived ==: primitives by value, pointers by
// identity, tuples, structs and enums field-wise.
func valuesEqual(a, b Value) bool {
	if isNull(a) || isNull(b) {
		return isNull(a) && isNull(b)
	}
	switch a := a.(type) {
	case IntVal:
		if b, ok := b.(IntVal); ok {
			return a.V == b.V
		}
		if b, ok := b.(UintVal); ok {
			return a.V >= 0 && uint64(a.V) == b.V
		}
	case UintVal:
		if b, ok := b.(UintVal); ok {
			return a.V == b.V
		}
		if b, ok := b.(IntVal); ok {
			return b.V >= 0 && a.V == uint64(b.V)
		}
	case FloatVal:
		if b, ok := b.(FloatVal); ok {
			return a.V == b.V
		}
		if b, ok := b.(IntVal); ok {
			return a.V == float64(b.V)
		}
	case BoolVal:
		b, ok := b.(BoolVal)
		return ok && a == b
	case StringVal:
		b, ok := b.(StringVal)
		return ok && a == b
	case PointerVal:
		b, ok := b.(PointerVal)
		return ok && a.Cell == b.Cell
	case TupleVal:
		b, ok := b.(TupleVal)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !valuesEqual(a[i], b[i]) {
				return false
			}
		}
		return true
	case StructVal:
		b, ok := b.(StructVal)
		if !ok || a.Def != b.Def || len(a.Fields) != len(b.Fields) {
			return false
		}
		for i := range a.Fields {
			if !valuesEqual(a.Fields[i], b.Fields[i]) {
				return false
			}
		}
		return true
	case EnumVal:
		b, ok := b.(EnumVal)
		if !ok || a.Def != b.Def || a.Variant != b.Variant {
			return false
		}
		for i := range a.Payload {
			if !valuesEqual(a.Payload[i], b.Payload[i]) {
				return false
			}
		}
		return true
	}
	// An untyped literal against a float, and other mixed pairs the
	// checker allowed, land in the specific cases above; anything else is
	// unequal.
	return false
}
