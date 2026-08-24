package meval

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gyro-lang/gyro/internal/metals"
)

// native dispatches a gyro:rt import by name. Monomorphized names such
// as println<i32> and Array<i32>::push dispatch on their base name; the
// type arguments only shaped the signature.
func (m *machine) native(name string, args []value) value {
	owner, method := splitNativeName(name)
	arg := func(i int) value {
		if i < len(args) {
			return args[i]
		}
		return uninitVal{}
	}

	switch owner {
	case "":
		return m.freeNative(method, arg)
	case "Array":
		return m.arrayNative(method, arg)
	case "Mutex":
		return m.mutexNative(method, arg)
	case "MutexGuard":
		return m.guardNative(method, arg)
	case "File":
		return m.fileNative(method, arg)
	case "string":
		switch method {
		case "compare":
			left := arg(0).(strVal)
			right := arg(1).(strVal)
			return intVal{v: int64(strings.Compare(string(left), string(right))), kind: metals.I32}
		case "runes":
			text := arg(0).(strVal)
			runes := []rune(string(text))
			elems := make([]value, len(runes))
			for i, r := range runes {
				elems[i] = intVal{v: int64(r), kind: metals.Rune}
			}
			return sliceVal{elems: elems}
		case "bytes":
			text := arg(0).(strVal)
			elems := make([]value, len(text))
			for i := 0; i < len(text); i++ {
				elems[i] = uintVal{v: uint64(text[i]), kind: metals.U8}
			}
			return sliceVal{elems: elems}
		}
	}
	m.throw("unknown runtime function @%s", name)
	return nil
}

// splitNativeName separates Array<i32>::push into (Array, push) and
// println<i32> into ("", println).
func splitNativeName(name string) (owner, method string) {
	method = name
	if i := strings.LastIndex(name, "::"); i >= 0 {
		owner, method = name[:i], name[i+2:]
	}
	if j := strings.IndexByte(owner, '<'); j >= 0 {
		owner = owner[:j]
	}
	if j := strings.IndexByte(method, '<'); j >= 0 {
		method = method[:j]
	}
	return owner, method
}

func (m *machine) freeNative(method string, arg func(int) value) value {
	switch method {
	case "print":
		fmt.Fprint(m.stdout, m.display(arg(0)))
		return nil
	case "println":
		fmt.Fprintln(m.stdout, m.display(arg(0)))
		return nil
	case "eprintln":
		fmt.Fprintln(m.stderr, m.display(arg(0)))
		return nil
	case "str":
		return strVal(m.repr(arg(0)))
	case "from_utf8":
		slice, ok := arg(0).(sliceVal)
		if !ok {
			m.throw("from_utf8 takes a u8 slice")
		}
		bytes := make([]byte, len(slice.elems))
		for i, elem := range slice.elems {
			switch value := elem.(type) {
			case uintVal:
				bytes[i] = byte(value.v)
			case intVal:
				bytes[i] = byte(value.v)
			default:
				m.throw("from_utf8 takes a u8 slice")
			}
		}
		return strVal(bytes)
	case "hash":
		// The import is named for its parameter type — hash<i32>,
		// hash<str> — but the value already carries its shape, so one
		// case answers for all of them.
		return uintVal{v: hashOf(m, arg(0)), kind: metals.U64}
	case "spawn":
		fn, ok := arg(0).(fnVal)
		if !ok {
			m.throw("spawn takes a closure")
		}
		go func() {
			defer func() {
				if failure := recover(); failure != nil {
					if abort, ok := failure.(*Abort); ok {
						fmt.Fprintln(m.stderr, abort.Error())
						return
					}
					fmt.Fprintln(m.stderr, "panic:", failure)
				}
			}()
			m.callFn(fn, nil)
		}()
		return nil
	case "to_i32":
		text, _ := arg(0).(strVal)
		parsed, err := strconv.ParseInt(strings.TrimSpace(string(text)), 10, 32)
		if err != nil {
			return tupleVal{intVal{kind: metals.I32}, m.nativeError(fmt.Sprintf("%q is not an i32", string(text)))}
		}
		return tupleVal{intVal{v: parsed, kind: metals.I32}, nullVal{}}
	case "to_f64":
		text, _ := arg(0).(strVal)
		parsed, err := strconv.ParseFloat(strings.TrimSpace(string(text)), 64)
		if err != nil {
			return tupleVal{floatVal{kind: metals.F64}, m.nativeError(fmt.Sprintf("%q is not an f64", string(text)))}
		}
		return tupleVal{floatVal{v: parsed, kind: metals.F64}, nullVal{}}
	case "open":
		name, _ := arg(0).(strVal)
		if _, err := os.Stat(string(name)); err != nil {
			return tupleVal{nullVal{}, m.nativeError(err.Error())}
		}
		return tupleVal{handleVal{obj: &fileObj{name: string(name)}}, nullVal{}}
	case "read_all":
		handle, ok := arg(0).(handleVal)
		if !ok {
			return tupleVal{strVal(""), m.nativeError("not an open file")}
		}
		file, ok := handle.obj.(*fileObj)
		if !ok {
			return tupleVal{strVal(""), m.nativeError("not an open file")}
		}
		data, err := os.ReadFile(file.name)
		if err != nil {
			return tupleVal{strVal(""), m.nativeError(err.Error())}
		}
		return tupleVal{strVal(data), nullVal{}}
	case "write_file":
		path, _ := arg(0).(strVal)
		text, _ := arg(1).(strVal)
		if err := os.WriteFile(string(path), []byte(text), 0o644); err != nil {
			return m.nativeError(err.Error())
		}
		return nullVal{}
	case "append_file":
		path, _ := arg(0).(strVal)
		text, _ := arg(1).(strVal)
		file, err := os.OpenFile(string(path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return m.nativeError(err.Error())
		}
		_, err = file.WriteString(string(text))
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return m.nativeError(err.Error())
		}
		return nullVal{}
	case "remove":
		path, _ := arg(0).(strVal)
		if err := os.Remove(string(path)); err != nil {
			return m.nativeError(err.Error())
		}
		return nullVal{}
	case "read_dir":
		path, _ := arg(0).(strVal)
		entries, err := os.ReadDir(string(path))
		if err != nil {
			return tupleVal{handleVal{obj: &arrayObj{}}, m.nativeError(err.Error())}
		}
		names := &arrayObj{}
		for _, entry := range entries {
			names.elems = append(names.elems, strVal(entry.Name()))
		}
		return tupleVal{handleVal{obj: names}, nullVal{}}
	case "exists":
		path, _ := arg(0).(strVal)
		_, err := os.Stat(string(path))
		return boolVal(err == nil)
	case "get":
		return tupleVal{strVal(""), m.nativeError("the interpreter has no network")}
	case "now":
		return intVal{v: time.Now().UnixMilli(), kind: metals.I64}
	case "monotonic":
		return intVal{v: time.Since(monotonicStart).Nanoseconds(), kind: metals.I64}
	case "sleep":
		millis, ok := arg(0).(intVal)
		if !ok {
			m.throw("sleep takes a millisecond count")
		}
		time.Sleep(time.Duration(millis.v) * time.Millisecond)
		return nil
	case "env":
		name, _ := arg(0).(strVal)
		value, found := os.LookupEnv(string(name))
		if !found {
			return nullVal{}
		}
		return strVal(value)
	case "read_stdin":
		data, err := io.ReadAll(m.stdin)
		if err != nil {
			m.throw("read_stdin: %v", err)
		}
		return strVal(data)
	case "exit":
		code, ok := arg(0).(intVal)
		if !ok {
			m.throw("exit takes an i32")
		}
		panic(&Exit{Code: int(code.v)})
	}
	m.throw("unknown runtime function %s", method)
	return nil
}

// monotonicStart anchors the arbitrary epoch: time.Since reads Go's
// monotonic clock, so the difference never steps with the wall clock.
var monotonicStart = time.Now()

// nativeError mints variant 0 of the closed %error sum: Native(str).
func (m *machine) nativeError(message string) value {
	return enumVal{typeName: "error", tag: 0, payload: []value{strVal(message)}}
}

func (m *machine) arrayNative(method string, arg func(int) value) value {
	if method == "new" || method == "with_capacity" {
		return handleVal{obj: &arrayObj{}}
	}
	handle, ok := arg(0).(handleVal)
	if !ok {
		m.throw("an Array method needs its Array")
	}
	array, ok := handle.obj.(*arrayObj)
	if !ok {
		m.throw("an Array method needs its Array")
	}
	if array.freed && method != "free" {
		m.throw("use after free of an Array")
	}
	switch method {
	case "push":
		array.elems = append(array.elems, copyValue(arg(1)))
		return nil
	case "pop":
		if len(array.elems) == 0 {
			return tupleVal{uninitVal{}, boolVal(false)}
		}
		last := array.elems[len(array.elems)-1]
		array.elems = array.elems[:len(array.elems)-1]
		return tupleVal{last, boolVal(true)}
	case "get":
		index := m.index(arg(1), len(array.elems))
		return copyValue(array.elems[index])
	case "set":
		index := m.index(arg(1), len(array.elems))
		array.elems[index] = copyValue(arg(2))
		return nil
	case "len":
		return uintVal{v: uint64(len(array.elems)), kind: metals.Usize}
	case "slice":
		return sliceVal{elems: array.elems}
	case "free":
		if array.freed {
			m.throw("double free of an Array")
		}
		array.freed = true
		return nil
	}
	m.throw("unknown Array method %s", method)
	return nil
}

func (m *machine) mutexNative(method string, arg func(int) value) value {
	switch method {
	case "new":
		return handleVal{obj: &mutexObj{val: copyValue(arg(0))}}
	case "lock":
		handle := arg(0).(handleVal)
		mutex := handle.obj.(*mutexObj)
		mutex.mu.Lock()
		return handleVal{obj: &guardObj{mutex: mutex}}
	}
	m.throw("unknown Mutex method %s", method)
	return nil
}

func (m *machine) guardNative(method string, arg func(int) value) value {
	handle := arg(0).(handleVal)
	guard := handle.obj.(*guardObj)
	if guard.done {
		m.throw("the guard was already unlocked")
	}
	switch method {
	case "get":
		return copyValue(guard.mutex.val)
	case "set":
		guard.mutex.val = copyValue(arg(1))
		return nil
	case "unlock":
		guard.done = true
		guard.mutex.mu.Unlock()
		return nil
	}
	m.throw("unknown MutexGuard method %s", method)
	return nil
}

func (m *machine) fileNative(method string, arg func(int) value) value {
	if method != "close" {
		m.throw("unknown File method %s", method)
	}
	if handle, ok := arg(0).(handleVal); ok {
		if file, ok := handle.obj.(*fileObj); ok {
			file.closed = true
		}
	}
	return nil
}
