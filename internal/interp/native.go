package interp

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// nativeCall is the context a native function runs in: the calling frame
// for panics and type resolution, the call's type arguments, and the
// evaluated arguments.
type nativeCall struct {
	frame    *frame
	offset   int
	typeArgs []ast.Type
	self     Value
	args     []Value
}

func (c *nativeCall) panicf(format string, args ...any) {
	c.frame.in.panicf(c.frame.module, c.offset, format, args...)
}

func (c *nativeCall) arg(i int) Value {
	if i < len(c.args) {
		return c.args[i]
	}
	return UninitVal{}
}

type nativeFunc func(c *nativeCall) Value

// nativeFor supplies the body of a declaration-only stdlib function.
// Instance methods on the runtime's own values (Array, Mutex, File, ...)
// dispatch dynamically in nativeMethodThunk instead.
func nativeFor(modulePath, owner, name string) nativeFunc {
	return natives[modulePath+"|"+owner+"|"+name]
}

var natives = map[string]nativeFunc{
	"gyro:stdlib||print":    nativePrint,
	"gyro:stdlib||println":  nativePrintln,
	"gyro:stdlib||eprintln": nativeEprintln,
	"gyro:stdlib||spawn":     nativeSpawn,
	"gyro:stdlib||from_utf8": nativeFromUTF8,

	"gyro:stdlib|Array|new":           nativeArrayNew,
	"gyro:stdlib|Array|with_capacity": nativeArrayNew,
	"gyro:stdlib|Mutex|new":           nativeMutexNew,

	"gyro:mem||alloc": nativeAlloc,
	"gyro:mem||free":  nativeFree,
	"gyro:mem||size":  nativeSize,

	"gyro:fs||open":        nativeOpen,
	"gyro:fs||read_all":    nativeReadAll,
	"gyro:fs||write_file":  nativeWriteFile,
	"gyro:fs||append_file": nativeAppendFile,
	"gyro:fs||remove":      nativeRemove,
	"gyro:fs||read_dir":    nativeReadDir,
	"gyro:fs||exists":      nativeExists,

	"gyro:net||get": nativeNetGet,

	"gyro:convert||to_i32": nativeToI32,
	"gyro:convert||to_f64": nativeToF64,

	"gyro:time||now":       nativeNow,
	"gyro:time||monotonic": nativeMonotonic,
	"gyro:time||sleep":     nativeSleep,
}

func nativePrint(c *nativeCall) Value {
	fmt.Fprint(c.frame.in.stdout, c.frame.in.display(c.arg(0)))
	return VoidVal{}
}

func nativePrintln(c *nativeCall) Value {
	fmt.Fprintln(c.frame.in.stdout, c.frame.in.display(c.arg(0)))
	return VoidVal{}
}

func nativeEprintln(c *nativeCall) Value {
	fmt.Fprintln(c.frame.in.stderr, c.frame.in.display(c.arg(0)))
	return VoidVal{}
}

func nativeSpawn(c *nativeCall) Value {
	closure, ok := c.arg(0).(*ClosureVal)
	if !ok {
		c.panicf("spawn takes a closure")
	}
	in := c.frame.in
	go func() {
		defer func() {
			if r := recover(); r != nil {
				in.abortFromGoroutine(r)
			}
		}()
		in.callClosure(closure, nil)
	}()
	return VoidVal{}
}

func nativeArrayNew(c *nativeCall) Value {
	return &ArrayObj{}
}

// nativeFromUTF8 reads a byte slice as text. It copies: a string owns its
// bytes, so the result does not depend on the slice staying alive.
func nativeFromUTF8(c *nativeCall) Value {
	slice, ok := c.arg(0).(SliceVal)
	if !ok {
		c.panicf("from_utf8 takes a u8 slice")
	}
	bytes := make([]byte, len(slice.Elems))
	for i, elem := range slice.Elems {
		// An untyped literal pushed into an Array<u8> keeps its signed
		// representation, so both integer shapes reach here.
		switch value := elem.(type) {
		case UintVal:
			bytes[i] = byte(value.V)
		case IntVal:
			bytes[i] = byte(value.V)
		default:
			c.panicf("from_utf8 takes a u8 slice")
		}
	}
	return StringVal(bytes)
}

func nativeMutexNew(c *nativeCall) Value {
	return &MutexObj{value: c.arg(0)}
}

// nativeAlloc returns a pointer to fresh, uninitialized storage. For a
// struct type the fields exist but hold no values yet — there are no zero
// values, and reading one before writing it is a panic.
func nativeAlloc(c *nativeCall) Value {
	cell := new(Value)
	*cell = Value(UninitVal{})

	if len(c.typeArgs) == 1 {
		if def := c.frame.resolveTypeDef(c.typeArgs[0]); def != nil && def.structDecl != nil {
			fields := make([]Value, len(def.structDecl.Fields))
			for i := range fields {
				fields[i] = UninitVal{}
			}
			*cell = StructVal{Def: def, Fields: fields}
		}
	}
	return PointerVal{Cell: cell}
}

func nativeFree(c *nativeCall) Value {
	pointer, ok := c.arg(0).(PointerVal)
	if !ok {
		c.panicf("free takes a pointer")
	}
	if _, freed := (*pointer.Cell).(FreedVal); freed {
		c.panicf("double free")
	}
	*pointer.Cell = FreedVal{}
	return VoidVal{}
}

func nativeSize(c *nativeCall) Value {
	return UintVal{V: 8, Kind: types.Usize}
}

func nativeOpen(c *nativeCall) Value {
	name, _ := c.arg(0).(StringVal)
	if _, err := os.Stat(string(name)); err != nil {
		return TupleVal{NullVal{}, NativeError{Message: err.Error()}}
	}
	return TupleVal{&FileObj{name: string(name)}, NullVal{}}
}

func nativeReadAll(c *nativeCall) Value {
	file, ok := c.arg(0).(*FileObj)
	if !ok {
		return TupleVal{StringVal(""), NativeError{Message: "not an open file"}}
	}
	data, err := os.ReadFile(file.name)
	if err != nil {
		return TupleVal{StringVal(""), NativeError{Message: err.Error()}}
	}
	return TupleVal{StringVal(data), NullVal{}}
}

func nativeWriteFile(c *nativeCall) Value {
	path, _ := c.arg(0).(StringVal)
	text, _ := c.arg(1).(StringVal)
	if err := os.WriteFile(string(path), []byte(text), 0o644); err != nil {
		return NativeError{Message: err.Error()}
	}
	return NullVal{}
}

func nativeAppendFile(c *nativeCall) Value {
	path, _ := c.arg(0).(StringVal)
	text, _ := c.arg(1).(StringVal)
	file, err := os.OpenFile(string(path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return NativeError{Message: err.Error()}
	}
	_, err = file.WriteString(string(text))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return NativeError{Message: err.Error()}
	}
	return NullVal{}
}

func nativeRemove(c *nativeCall) Value {
	path, _ := c.arg(0).(StringVal)
	if err := os.Remove(string(path)); err != nil {
		return NativeError{Message: err.Error()}
	}
	return NullVal{}
}

// nativeReadDir lists a directory's entry names. os.ReadDir sorts them,
// and the native runtime sorts the same way, so a listing is deterministic
// on every tier.
func nativeReadDir(c *nativeCall) Value {
	path, _ := c.arg(0).(StringVal)
	entries, err := os.ReadDir(string(path))
	if err != nil {
		return TupleVal{&ArrayObj{}, NativeError{Message: err.Error()}}
	}
	names := &ArrayObj{}
	for _, entry := range entries {
		names.Elems = append(names.Elems, StringVal(entry.Name()))
	}
	return TupleVal{names, NullVal{}}
}

func nativeExists(c *nativeCall) Value {
	path, _ := c.arg(0).(StringVal)
	_, err := os.Stat(string(path))
	return BoolVal(err == nil)
}

func nativeNetGet(c *nativeCall) Value {
	return TupleVal{StringVal(""), NativeError{Message: "the interpreter has no network"}}
}

// monotonicStart anchors the arbitrary epoch: time.Since reads Go's
// monotonic clock, so the difference never steps with the wall clock.
var monotonicStart = time.Now()

func nativeNow(c *nativeCall) Value {
	return IntVal{V: time.Now().UnixMilli(), Kind: types.I64}
}

func nativeMonotonic(c *nativeCall) Value {
	return IntVal{V: time.Since(monotonicStart).Nanoseconds(), Kind: types.I64}
}

func nativeSleep(c *nativeCall) Value {
	millis, ok := c.arg(0).(IntVal)
	if !ok {
		c.panicf("sleep takes a millisecond count")
	}
	time.Sleep(time.Duration(millis.V) * time.Millisecond)
	return VoidVal{}
}

func nativeToI32(c *nativeCall) Value {
	text, _ := c.arg(0).(StringVal)
	value, err := strconv.ParseInt(strings.TrimSpace(string(text)), 10, 32)
	if err != nil {
		return TupleVal{IntVal{Kind: types.I32}, NativeError{Message: fmt.Sprintf("%q is not an i32", string(text))}}
	}
	return TupleVal{IntVal{V: value, Kind: types.I32}, NullVal{}}
}

func nativeToF64(c *nativeCall) Value {
	text, _ := c.arg(0).(StringVal)
	value, err := strconv.ParseFloat(strings.TrimSpace(string(text)), 64)
	if err != nil {
		return TupleVal{FloatVal{Kind: types.F64}, NativeError{Message: fmt.Sprintf("%q is not an f64", string(text))}}
	}
	return TupleVal{FloatVal{V: value, Kind: types.F64}, NullVal{}}
}

// ---------------------------------------------------------------------------
// Methods on runtime and built-in values
// ---------------------------------------------------------------------------

// nativeMethodThunk dispatches methods on values the runtime owns — the
// stdlib objects and the built-in method surface (close, len, runes,
// compare, message). Returns nil when the receiver is not one of these.
func (f *frame) nativeMethodThunk(cell *Value, name string, args []Value, offset int) func() Value {
	recv := *cell
	call := &nativeCall{frame: f, offset: offset, self: recv, args: defaultedArgs(args)}

	// hash on a primitive is part of the type, so it answers before an
	// extension of the same name can. A composite's is derived later,
	// after any hash the type declares itself.
	if name == "hash" && isPrimitive(recv) {
		return f.hashThunk(recv, offset)
	}

	switch recv := recv.(type) {
	case *ArrayObj:
		return arrayMethod(recv, name, call)
	case *MutexObj:
		return mutexMethod(recv, name, call)
	case *GuardObj:
		return guardMethod(recv, name, call)
	case *FileObj:
		return fileMethod(recv, name, call)
	case ChanVal:
		if name == "close" {
			return func() Value { close(recv); return VoidVal{} }
		}
	case SliceVal:
		if name == "len" {
			return func() Value { return UintVal{V: uint64(len(recv.Elems)), Kind: types.Usize} }
		}
	case ArrayVal:
		if name == "len" {
			return func() Value { return UintVal{V: uint64(len(recv.Elems)), Kind: types.Usize} }
		}
	case StringVal:
		switch name {
		case "len":
			return func() Value { return UintVal{V: uint64(len(recv)), Kind: types.Usize} }
		case "bytes":
			return func() Value {
				elems := make([]Value, len(recv))
				for i := 0; i < len(recv); i++ {
					elems[i] = UintVal{V: uint64(recv[i]), Kind: types.U8}
				}
				return SliceVal{Elems: elems}
			}
		case "compare":
			return func() Value {
				other, _ := call.arg(0).(StringVal)
				return IntVal{V: int64(strings.Compare(string(recv), string(other))), Kind: types.I32}
			}
		case "runes":
			return func() Value {
				runes := []rune(string(recv))
				elems := make([]Value, len(runes))
				for i, r := range runes {
					elems[i] = IntVal{V: int64(r), Kind: types.RuneKind}
				}
				return SliceVal{Elems: elems}
			}
		}
	case IntVal, UintVal, FloatVal:
		if name == "compare" {
			return func() Value {
				left, right := adoptKinds(recv, call.arg(0))
				return IntVal{V: int64(f.compareNumeric(left, right, offset)), Kind: types.I32}
			}
		}
	case NativeError:
		if name == "message" {
			return func() Value { return StringVal(recv.Message) }
		}
	}
	return nil
}

func arrayMethod(a *ArrayObj, name string, c *nativeCall) func() Value {
	guard := func() {
		if a.Freed {
			c.panicf("use after free of an Array")
		}
	}
	switch name {
	case "push":
		return func() Value { guard(); a.Elems = append(a.Elems, copyValue(c.arg(0))); return VoidVal{} }
	case "pop":
		return func() Value {
			guard()
			if len(a.Elems) == 0 {
				return TupleVal{UninitVal{}, BoolVal(false)}
			}
			last := a.Elems[len(a.Elems)-1]
			a.Elems = a.Elems[:len(a.Elems)-1]
			return TupleVal{last, BoolVal(true)}
		}
	case "get":
		return func() Value {
			guard()
			index := toIndex(c.arg(0))
			if index < 0 || index >= int64(len(a.Elems)) {
				c.panicf("index %d out of range for length %d", index, len(a.Elems))
			}
			return copyValue(a.Elems[index])
		}
	case "set":
		return func() Value {
			guard()
			index := toIndex(c.arg(0))
			if index < 0 || index >= int64(len(a.Elems)) {
				c.panicf("index %d out of range for length %d", index, len(a.Elems))
			}
			a.Elems[index] = copyValue(c.arg(1))
			return VoidVal{}
		}
	case "len":
		return func() Value { guard(); return UintVal{V: uint64(len(a.Elems)), Kind: types.Usize} }
	case "slice":
		return func() Value { guard(); return SliceVal{Elems: a.Elems} }
	case "free":
		return func() Value {
			if a.Freed {
				c.panicf("double free of an Array")
			}
			a.Freed = true
			return VoidVal{}
		}
	}
	return nil
}

func mutexMethod(m *MutexObj, name string, c *nativeCall) func() Value {
	if name != "lock" {
		return nil
	}
	return func() Value {
		m.mu.Lock()
		return &GuardObj{mutex: m}
	}
}

func guardMethod(g *GuardObj, name string, c *nativeCall) func() Value {
	guard := func() {
		if g.done {
			c.panicf("the guard was already unlocked")
		}
	}
	switch name {
	case "get":
		return func() Value { guard(); return copyValue(g.mutex.value) }
	case "set":
		return func() Value { guard(); g.mutex.value = copyValue(c.arg(0)); return VoidVal{} }
	case "unlock":
		return func() Value {
			guard()
			g.done = true
			g.mutex.mu.Unlock()
			return VoidVal{}
		}
	}
	return nil
}

func fileMethod(file *FileObj, name string, c *nativeCall) func() Value {
	if name != "close" {
		return nil
	}
	return func() Value {
		file.closed = true
		return VoidVal{}
	}
}
