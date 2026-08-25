package loader

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/cc"
	"github.com/gyro-lang/gyro/internal/lisp"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/token"
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
//
// A named type an export mentions is declared here too, as a Gyro struct
// with the same members in the same order. It is a declaration of the
// *same* type and not a lookalike: lowering imports the METALS type this
// text was generated from rather than rebuilding one from the text, so
// there is a single %Point in the linked program. The struct here is
// what lets Gyro's checker see the fields; the identity is the METALS
// module's. See metals.Import.Types.
func header(module *metals.Module) (string, error) {
	h := &headerGen{
		types:     module.TypeEnv(),
		spellable: map[string]bool{},
		wanted:    map[string]bool{},
	}
	for _, imp := range module.Imports {
		h.order = append(h.order, imp.Types...)
	}
	h.order = append(h.order, module.Types...)
	h.resolve()

	var decls strings.Builder
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
			spelling, err := h.spell(param.Type)
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
			spelling, err := h.spell(fn.Result)
			if err != nil {
				continue
			}
			result = ": " + spelling
		}

		prefix := ""
		if fn.Async {
			prefix = "async "
		}
		fmt.Fprintf(&decls, "export %sfn %s(%s)%s\n", prefix, fn.Name, strings.Join(params, ", "), result)
	}

	var out strings.Builder
	fmt.Fprintf(&out, "// Generated from the exports of %s.\n", module.Name)
	for _, decl := range h.order {
		if !h.wanted[decl.Name] {
			continue
		}
		out.WriteString(h.declare(decl))
	}
	out.WriteString(decls.String())
	return out.String(), nil
}

// headerGen decides which of a module's named types Gyro can be shown,
// and which of those an export actually mentions.
type headerGen struct {
	types map[string]*metals.TypeDecl
	// order is every declaration, in the order the module wrote them, so
	// the generated text is the same on every run.
	order []*metals.TypeDecl
	// spellable is the fixpoint: a type is spellable when Gyro can write
	// its name, its members' names and every member's type — and a member
	// of an unspellable type makes it unspellable too.
	spellable map[string]bool
	// wanted narrows that to the types an emitted declaration mentions.
	// A module's internal shapes are not this importer's business.
	wanted map[string]bool
}

// resolve computes the spellable set. It starts by believing everything
// and takes belief away, which is what makes a recursive type — a linked
// list holding a pointer to itself — come out spellable rather than
// looping.
func (h *headerGen) resolve() {
	for name := range h.types {
		h.spellable[name] = true
	}
	for changed := true; changed; {
		changed = false
		for name, decl := range h.types {
			if h.spellable[name] && !h.declarable(decl) {
				h.spellable[name] = false
				changed = true
			}
		}
	}
	// Spelling a type is also how one gets marked for declaration, and
	// the passes above spelled types they went on to disbelieve. Only
	// what an export mentions counts, and nothing has been exported yet.
	clear(h.wanted)
}

// declarable reports whether one declaration can be written as a Gyro
// struct, assuming the current belief about every other type.
func (h *headerGen) declarable(decl *metals.TypeDecl) bool {
	if !isGyroName(decl.Name) {
		return false
	}
	if decl.Opaque {
		return true
	}
	// A union has no Gyro spelling and an enum's is not this one: Gyro's
	// enum is a tagged sum, and a producer's %enum carries payloads that
	// a struct declaration cannot say.
	if decl.Record == nil {
		return false
	}
	for _, field := range decl.Record.Fields {
		if !isGyroName(field.Name) {
			return false
		}
		if _, err := h.spell(field.Type); err != nil {
			return false
		}
	}
	return true
}

func (h *headerGen) declare(decl *metals.TypeDecl) string {
	if decl.Opaque {
		// A name with no shape becomes a fieldless struct, which is the
		// spelling Gyro already uses for a handle it may pass around and
		// not look inside.
		return fmt.Sprintf("export struct %s {}\n", decl.Name)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "export struct %s {\n", decl.Name)
	for _, field := range decl.Record.Fields {
		spelling, err := h.spell(field.Type)
		if err != nil {
			// Unreachable: nothing is wanted that resolve did not first
			// find spellable, member by member. Left as a member Gyro
			// will refuse rather than as text that silently means
			// something else.
			spelling = "<" + err.Error() + ">"
		}
		// Every member is exported. A C struct has no private members,
		// and a producer that wants one keeps the type out of its
		// exports rather than hiding a field of it.
		fmt.Fprintf(&out, "  export %s: %s\n", field.Name, spelling)
	}
	out.WriteString("}\n")
	return out.String()
}

// spell writes a METALS type the way Gyro writes it, and records the
// named types the spelling depends on. The types that have no spelling
// are the ones that cannot cross a boundary anyway, plus the shapes Gyro
// has no declaration for.
func (h *headerGen) spell(t metals.Type) (string, error) {
	switch t := t.(type) {
	case *metals.Prim:
		if t.Kind == metals.Str {
			return "string", nil
		}
		return t.String(), nil
	case *metals.Any:
		return "any", nil
	case *metals.AnyPtr:
		return "anyptr", nil
	case *metals.SliceOf:
		elem, err := h.spell(t.Elem)
		if err != nil {
			return "", err
		}
		return elem + "[]", nil
	case *metals.Opt:
		elem, err := h.spell(t.Elem)
		if err != nil {
			return "", err
		}
		// `?` binds to the outermost type, and a spelling that ends in
		// its own result type would swallow it: `code(i32): i32?` is a
		// code pointer returning a nullable, not a nullable code
		// pointer. Parentheses are how Gyro says which, the same way C
		// says it with the declarator's.
		if endsInAType(t.Elem) {
			return "(" + elem + ")?", nil
		}
		return elem + "?", nil
	case *metals.Ptr:
		// `?` binds to the outermost type in Gyro, so an opt<ptr<T>> —
		// which is what a C pointer is — spells as *T? and not *(T?).
		elem, err := h.spell(t.Elem)
		if err != nil {
			return "", err
		}
		return "*" + elem, nil
	case *metals.TupleOf:
		parts := make([]string, len(t.Elems))
		for i, elem := range t.Elems {
			spelling, err := h.spell(elem)
			if err != nil {
				return "", err
			}
			parts[i] = spelling
		}
		return "(" + strings.Join(parts, ", ") + ")", nil
	case *metals.CodeType:
		params := make([]string, len(t.Params))
		for i, param := range t.Params {
			spelling, err := h.spell(param)
			if err != nil {
				return "", err
			}
			params[i] = spelling
		}
		out := "code(" + strings.Join(params, ", ") + ")"
		if t.Result != nil {
			spelling, err := h.spell(t.Result)
			if err != nil {
				return "", err
			}
			out += ": " + spelling
		}
		return out, nil
	case *metals.Named:
		if !h.spellable[t.Name] {
			return "", fmt.Errorf("%s has no Gyro declaration", t)
		}
		h.want(t.Name)
		return t.Name, nil
	}
	return "", fmt.Errorf("%s has no Gyro spelling", t)
}

// endsInAType reports whether a Gyro spelling finishes with a nested
// type, so that a `?` written after it would attach to that instead of
// to the whole thing.
func endsInAType(t metals.Type) bool {
	switch t := t.(type) {
	case *metals.CodeType:
		return t.Result != nil
	case *metals.FnType:
		return t.Result != nil
	}
	return false
}

// want marks a type for declaration, and everything its members name
// along with it.
func (h *headerGen) want(name string) {
	if h.wanted[name] {
		return
	}
	h.wanted[name] = true
	decl := h.types[name]
	if decl == nil {
		return
	}
	for _, field := range decl.Members() {
		h.spell(field.Type)
	}
}

// isGyroName reports whether a producer's name is one Gyro can write:
// an identifier, and not one of its keywords.
func isGyroName(name string) bool {
	return isPlainIdent(name) && token.Lookup(name) == token.Ident
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
