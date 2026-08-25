package loader

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/cc"
	"github.com/gyro-lang/gyro/internal/lisp"
	"github.com/gyro-lang/gyro/internal/metals"
)

// A foreign module is one another language produced. It reaches the rest
// of the compiler as a Gyro module whose declarations have no bodies —
// the same shape a runtime intrinsic already has — so name resolution and
// type checking need to know nothing about the producer. What the
// checker sees is generated from the module's METALS exports, which is
// the only thing about a foreign module that is knowable.
//
// The compiled METALS is kept on the Module: lowering emits an import for
// it, and the driver hands it to the linker beside the Gyro program.

// foreignProducer names the compiler for a path's extension, or "" when
// the path is ordinary Gyro.
func foreignProducer(path string) string {
	switch filepath.Ext(path) {
	case ".lisp", ".scm":
		return "lisp"
	case ".c":
		return "cc"
	}
	return ""
}

// compileForeign runs the producer for a path and returns its module.
//
// A `.c` reaches internal/cc here and internal/wasm when it is the whole
// program: an imported C file is a library whose declared types are what
// Gyro imports, and only the direct front end still has them — by the
// time clang has made wasm of it, a `struct Point *` is a `u32`. The
// bridge keeps the root, where a C program wants the bundled libc that
// the direct front end has no preprocessor to include.
func compileForeign(path, source string) (*metals.Module, error) {
	var module *metals.Module
	var err error

	switch foreignProducer(path) {
	case "lisp":
		module, err = lisp.Compile(path, source)
	case "cc":
		module, err = cc.Compile(path, source, nil)
	default:
		return nil, fmt.Errorf("%s: no compiler for this kind of module", path)
	}
	if err != nil {
		return nil, err
	}

	// A producer is not trusted just because it lives in this
	// repository; the format's whole premise is that none of them are.
	if err := metals.Verify(module); err != nil {
		return nil, fmt.Errorf("%s does not verify: %v", filepath.Base(path), err)
	}
	return module, nil
}

// header renders a foreign module's exports as Gyro declarations without
// bodies. Only the exports are visible: whatever else the module defines
// is its own business, exactly as for a Gyro module.
func header(module *metals.Module) (string, error) {
	var out strings.Builder
	fmt.Fprintf(&out, "// Generated from the exports of %s.\n", module.Name)

	for _, fn := range module.Funcs {
		if !fn.Export || fn.Name == "main" {
			continue
		}
		if !isPlainIdent(fn.Name) {
			return "", fmt.Errorf("%s exports @%s, which is not a name Gyro can call", module.Name, fn.Name)
		}

		// An export Gyro has no way to spell is left out rather than
		// made an error. A C library is not written for this importer:
		// one helper taking a `va_list` or returning a shape a header
		// cannot redeclare should not make the other forty
		// unimportable. Naming a skipped export is still an error,
		// reported where it is imported, which is where it means
		// something.
		params := make([]string, len(fn.Params))
		spellable := true
		for i, param := range fn.Params {
			spelling, err := gyroType(param.Type)
			if err != nil {
				spellable = false
				break
			}
			params[i] = fmt.Sprintf("a%d: %s", i, spelling)
		}
		if !spellable {
			continue
		}

		result := ""
		if fn.Result != nil {
			spelling, err := gyroType(fn.Result)
			if err != nil {
				continue
			}
			result = ": " + spelling
		}

		prefix := ""
		if fn.Async {
			prefix = "async "
		}
		fmt.Fprintf(&out, "export %sfn %s(%s)%s\n", prefix, fn.Name, strings.Join(params, ", "), result)
	}
	return out.String(), nil
}

// gyroType spells a METALS type the way Gyro writes it. The types that
// have no spelling are the ones that cannot cross a boundary anyway, plus
// the module-local shapes a header has no way to redeclare yet.
func gyroType(t metals.Type) (string, error) {
	switch t := t.(type) {
	case *metals.Prim:
		if t.Kind == metals.Str {
			return "string", nil
		}
		return t.String(), nil
	case *metals.Any:
		return "any", nil
	case *metals.SliceOf:
		elem, err := gyroType(t.Elem)
		if err != nil {
			return "", err
		}
		return elem + "[]", nil
	case *metals.Opt:
		elem, err := gyroType(t.Elem)
		if err != nil {
			return "", err
		}
		return elem + "?", nil
	case *metals.Ptr:
		// `?` binds to the outermost type in Gyro, so an opt<ptr<T>> —
		// which is what a C pointer is — spells as *T? and not *(T?).
		elem, err := gyroType(t.Elem)
		if err != nil {
			return "", err
		}
		return "*" + elem, nil
	case *metals.TupleOf:
		parts := make([]string, len(t.Elems))
		for i, elem := range t.Elems {
			spelling, err := gyroType(elem)
			if err != nil {
				return "", err
			}
			parts[i] = spelling
		}
		return "(" + strings.Join(parts, ", ") + ")", nil
	case *metals.Named:
		return "", fmt.Errorf("%s is declared inside its own module, and a header cannot redeclare it yet", t)
	}
	return "", fmt.Errorf("%s has no Gyro spelling", t)
}

func isPlainIdent(name string) bool {
	if name == "" {
		return false
	}
	for i, ch := range name {
		letter := ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
		if letter || (i > 0 && ch >= '0' && ch <= '9') {
			continue
		}
		return false
	}
	return true
}
