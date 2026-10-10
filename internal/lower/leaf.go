package lower

import (
	"fmt"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/stdlib"
	"github.com/gyro-lang/gyro/internal/types"
)

// The scalars at the bottom of a value — a number shown, a string compared,
// a word hashed — are answered by ordinary Gyro in stdlib. A call site asks
// for one under the name of the width it started from, `str<i8>` or
// `hash<u16>`; the function lowering defines under that name widens the
// argument to the one type the Gyro body takes and calls it. Every width
// shares a body, and nothing about a leaf is the runtime's.

// leaf describes how a name is answered: the stdlib function, the type the
// argument is widened to ("" for none), and whether the widened integer is
// then reinterpreted as unsigned.
type leaf struct {
	callee  string
	widen   metals.Type
	bitcast bool

	// A printing leaf is also told which stream it writes to and whether
	// a newline follows, as two constants after the value.
	stream  bool
	fd      string
	newline bool
}

// ensureLeaf defines the function a leaf call site names and reports
// whether the name was one.
func (l *lowerer) ensureLeaf(name string, sig *metals.FnType) bool {
	plan, ok := l.leafPlan(name, sig)
	if !ok {
		return false
	}
	if l.lowered[name] {
		return true
	}
	l.lowered[name] = true

	callee := l.ensureFunc(l.leafDecl(plan.callee), nil)
	fn := &metals.Func{Name: name, Result: sig.Result}
	for index, param := range sig.Params {
		fn.Params = append(fn.Params, metals.Param{Name: argName(index), Type: param})
	}

	args := make([]string, len(sig.Params))
	for index := range sig.Params {
		args[index] = argName(index)
	}
	if plan.widen != nil && len(sig.Params) == 1 && sig.Params[0].String() != plan.widen.String() {
		fn.Body = append(fn.Body, &metals.Conv{Dst: "wide", X: "a0", To: plan.widen})
		args[0] = "wide"
	}
	if plan.bitcast {
		fn.Body = append(fn.Body, &metals.Bitcast{Dst: "bits", X: args[0], To: metals.TUint64})
		args[0] = "bits"
	}
	if plan.stream {
		fd, newline := "fd", "newline"
		fn.Body = append(fn.Body,
			&metals.Const{Dst: fd, Type: metals.TInt32, Value: plan.fd},
			&metals.Const{Dst: newline, Type: metals.TBool, Value: fmt.Sprint(plan.newline)},
		)
		args = append(args, fd, newline)
	}
	call := &metals.Call{Callee: callee, Args: args}
	if sig.Result != nil {
		call.Dst = "r"
	}
	fn.Body = append(fn.Body, call)
	if sig.Result != nil {
		fn.Body = append(fn.Body, &metals.Return{Val: "r"})
	}
	l.module.Funcs = append(l.module.Funcs, fn)
	return true
}

func argName(index int) string { return "a" + string(rune('0'+index)) }

// printers are the four ways to write a leaf: to which descriptor, and
// whether a newline follows.
var printers = []struct {
	name    string
	fd      string
	newline bool
}{
	{"print", "1", false},
	{"println", "1", true},
	{"eprint", "2", false},
	{"eprintln", "2", true},
}

// leafPlan maps a call site's name and signature onto its stdlib function.
func (l *lowerer) leafPlan(name string, sig *metals.FnType) (leaf, bool) {
	switch {
	case name == "string::compare":
		return leaf{callee: "compare_text"}, true
	case strings.HasPrefix(name, "str<"):
		return scalarLeaf("str_", sig, false)
	case strings.HasPrefix(name, "hash<"):
		return scalarLeaf("hash_", sig, true)
	}
	for _, printer := range printers {
		if !strings.HasPrefix(name, printer.name+"<") {
			continue
		}
		plan, ok := scalarLeaf("put_", sig, false)
		if !ok || plan.callee == "put_str" {
			plan.callee = "put_text"
		}
		plan.stream, plan.fd, plan.newline = true, printer.fd, printer.newline
		return plan, ok
	}
	return leaf{}, false
}

// scalarLeaf picks the body for a scalar by its kind. Hashing an integer
// folds it to an unsigned word first, so a signed one is reinterpreted and
// both then go through the same finalizer.
func scalarLeaf(prefix string, sig *metals.FnType, hashing bool) (leaf, bool) {
	if len(sig.Params) != 1 {
		return leaf{}, false
	}
	prim, ok := sig.Params[0].(*metals.Prim)
	if !ok {
		return leaf{}, false
	}
	switch prim.Kind {
	case metals.I8, metals.I16, metals.I32, metals.I64, metals.Isize, metals.Rune:
		if hashing {
			return leaf{callee: "hash_word", widen: metals.TInt64, bitcast: true}, true
		}
		if prim.Kind == metals.Rune {
			return leaf{callee: prefix + "rune"}, true
		}
		return leaf{callee: prefix + "int", widen: metals.TInt64}, true
	case metals.U8, metals.U16, metals.U32, metals.U64, metals.Usize:
		if hashing {
			return leaf{callee: "hash_word", widen: metals.TUint64}, true
		}
		return leaf{callee: prefix + "uint", widen: metals.TUint64}, true
	case metals.F32, metals.F64:
		if hashing {
			return leaf{callee: "hash_float", widen: metals.TF64}, true
		}
		return leaf{callee: prefix + "float", widen: metals.TF64}, true
	case metals.Bool:
		if hashing {
			return leaf{}, false
		}
		return leaf{callee: prefix + "bool"}, true
	case metals.Str:
		if hashing {
			return leaf{callee: "hash_text"}, true
		}
		return leaf{callee: prefix + "str"}, true
	}
	return leaf{}, false
}

// warmLeaf asks for the function that shows a scalar now, while the set of
// types an any can hold is still open: the printers of an any are built
// after it closes, and a leaf lowered then could wrap something new.
func (l *lowerer) warmLeaf(t types.Type) {
	prim, ok := l.metalsType(t).(*metals.Prim)
	if !ok {
		return
	}
	l.ensureImport("str<"+t.String()+">", &metals.FnType{Params: []metals.Type{prim}, Result: metals.TStr})
}

// leafDecl finds a function that mem or stdlib declares, by name. The
// leaves for showing and comparing live in mem, which is loaded whenever
// anything is shown; the printing ones live in stdlib, which anything that
// prints has imported.
func (l *lowerer) leafDecl(name string) *ast.FuncDecl {
	for _, home := range []string{"mem", "stdlib"} {
		for _, module := range l.prog.Modules {
			if module.Path != stdlib.Path(home) {
				continue
			}
			for _, decl := range module.Tree.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Owner == "" && fn.Name == name {
					return fn
				}
			}
		}
	}
	l.fail("no leaf function %s", name)
	return nil
}
