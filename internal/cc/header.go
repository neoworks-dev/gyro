package cc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Reading a header.
//
// A header is how C says what a library is, and importing one is reading
// it the way a C compiler would: preprocess it, parse every declaration,
// and keep what a program asked for by name. Everything else in a header
// — and a header like <SDL3/SDL.h> declares two thousand names — is
// parsed, so that the names a request mentions mean what the header says
// they mean, and then dropped.
//
// Nothing here is compiled. A function in a header is a declaration, and
// the definition lives in a library the program is linked against; what
// the request produces is the type declarations those functions mention,
// the signatures of the functions themselves, and the constants. The
// functions become imports from the native library (see lowerer.natives),
// which is the same mechanism a `.c` file's undefined symbols use.

// HeaderRequest is one import of a header.
type HeaderRequest struct {
	// Module is the METALS name the header's types are declared under.
	Module string
	// Include is the text of the `#include` that reads it, brackets or
	// quotes included: `<SDL3/SDL.h>`.
	Include string
	// Names are the identifiers the import lists.
	Names []string
	// Options carry the include path, the definitions and the library.
	Options *Options
}

// Header is what a request found.
type Header struct {
	// Module declares the types the request needs and imports the
	// functions from the native library. It has no function of its own.
	Module *metals.Module
	// Funcs, Consts and Types are the requested names, by kind, in the
	// order they were asked for.
	Funcs  []HeaderFunc
	Consts []HeaderConst
	Types  []string
	// Problems maps a requested name to why it cannot cross. A name the
	// header does not declare is among them.
	Problems map[string]string
	// Skipped is what the header declared and this front end could not
	// read, for a diagnostic to point at when a name is missing.
	Skipped []string
}

// HeaderFunc is a function a header declares.
type HeaderFunc struct {
	Name string
	Sig  *metals.FnType
}

// HeaderConst is an integer constant a header declares, as an enumerator
// or a macro. The type is the one C gives the expression.
type HeaderConst struct {
	Name     string
	Type     *metals.Prim
	Value    int64
	Unsigned bool
}

// ReadHeader reads a header and answers the names a request asked for.
func ReadHeader(req HeaderRequest) (*Header, error) {
	opt := &Options{}
	if req.Options != nil {
		*opt = *req.Options
	}
	opt.Declarations = true

	file := "<" + req.Module + ">"
	toks, pp, err := preprocessKeeping(file, "#include "+req.Include+"\n", opt)
	if err != nil {
		return nil, err
	}
	u, err := parse(file, toks, true)
	if err != nil {
		return nil, err
	}

	h := &Header{Problems: map[string]string{}}
	for _, s := range u.skipped {
		h.Skipped = append(h.Skipped, fmt.Sprintf("%s:%d: %s", s.pos.file, s.pos.line, s.reason))
	}

	r := &headerReader{unit: u, pp: pp, header: h, wanted: map[string]*ctype{}, seen: map[string]bool{}}
	r.index()
	for _, name := range req.Names {
		if r.seen[name] {
			continue
		}
		r.seen[name] = true
		r.request(name)
	}

	module, err := r.module(req.Module, opt.library())
	if err != nil {
		return nil, err
	}
	h.Module = module
	return h, nil
}

type headerReader struct {
	unit   *unit
	pp     *preprocessor
	header *Header
	seen   map[string]bool

	funcs map[string]*funcDecl
	// wanted is the aggregates the module has to declare, by tag.
	wanted map[string]*ctype
	// natives are the functions to import.
	natives []HeaderFunc
}

// index finds each function's declaration. A header may declare a name
// more than once, and the first one that is not a skipped inline body is
// the one a call is checked against.
func (r *headerReader) index() {
	r.funcs = map[string]*funcDecl{}
	for _, fn := range r.unit.funcs {
		if _, taken := r.funcs[fn.name]; !taken {
			r.funcs[fn.name] = fn
		}
	}
}

func (r *headerReader) problem(name, format string, args ...any) {
	r.header.Problems[name] = fmt.Sprintf(format, args...)
}

// request answers one name. C has one namespace for functions, objects,
// typedef names and enumerators, and a second for struct, union and enum
// tags, so the order here is the order a name is looked for in.
func (r *headerReader) request(name string) {
	if fn, ok := r.funcs[name]; ok {
		r.function(name, fn)
		return
	}
	if typ, ok := r.unit.typedefs[name]; ok {
		r.typedef(name, typ)
		return
	}
	if value, ok := r.unit.enums[name]; ok {
		r.header.Consts = append(r.header.Consts, HeaderConst{
			Name: name, Type: metals.TInt32, Value: value,
		})
		return
	}
	if _, ok := r.unit.externs[name]; ok {
		r.problem(name, "is a variable the library owns, and a variable has no address a program can name")
		return
	}
	for _, g := range r.unit.globals {
		if g.name == name {
			r.problem(name, "is a variable, and a header import brings in functions, types and constants")
			return
		}
	}
	if m := r.pp.macros[name]; m != nil {
		r.macro(name, m)
		return
	}
	for _, s := range r.unit.structs {
		if s.tag == name {
			r.aggregate(name, s)
			return
		}
	}
	r.problem(name, "is not declared by this header")
}

func (r *headerReader) function(name string, fn *funcDecl) {
	switch {
	case fn.inline:
		r.problem(name, "is defined in the header as an inline function, and a header's bodies are not compiled")
		return
	case fn.static:
		r.problem(name, "is static, so no library exports it")
		return
	case fn.variadic:
		r.problem(name, "is variadic, and a native callee reads its extra arguments from registers and the stack this call cannot lay out")
		return
	case fn.label != "" && fn.label != name:
		r.problem(name, "is declared under the assembler label %q, which a call by this name would not reach", fn.label)
		return
	}
	sig, err := nativeSignature(fn)
	if err != nil {
		r.problem(name, "%s", err)
		return
	}
	for _, p := range fn.params {
		r.reach(p.typ)
	}
	r.reach(fn.result)
	r.natives = append(r.natives, HeaderFunc{Name: name, Sig: sig})
	r.header.Funcs = append(r.header.Funcs, HeaderFunc{Name: name, Sig: sig})
}

// nativeSignature is a prototype as METALS types it, or the reason it has
// none. The reason is a type in it that a native callee cannot be handed.
func nativeSignature(fn *funcDecl) (sig *metals.FnType, err error) {
	defer func() {
		if r := recover(); r != nil {
			caught, ok := r.(failure)
			if !ok {
				panic(r)
			}
			sig, err = nil, fmt.Errorf("%s", strings.TrimPrefix(caught.err.Error(), ":0:0: "))
		}
	}()
	for _, p := range fn.params {
		if mentionsVaList(p.typ, map[*ctype]bool{}) {
			return nil, fmt.Errorf("takes a va_list, whose layout is this front end's own and not the one a native callee reads")
		}
	}
	code := fn.typ().metalsType().(*metals.CodeType)
	return &metals.FnType{Params: code.Params, Result: code.Result}, nil
}

func mentionsVaList(t *ctype, seen map[*ctype]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	switch t.kind {
	case kPtr, kArray:
		return mentionsVaList(t.elem, seen)
	case kStruct, kUnion:
		return t.tag == "__va_list_tag"
	case kFunc:
		for _, p := range t.params {
			if mentionsVaList(p.typ, seen) {
				return true
			}
		}
		return mentionsVaList(t.result, seen)
	}
	return false
}

// reach records every aggregate a type depends on, because the module
// that declares a function's signature has to declare them too.
func (r *headerReader) reach(t *ctype) {
	if t == nil {
		return
	}
	switch t.kind {
	case kPtr, kArray:
		r.reach(t.elem)
	case kFunc:
		for _, p := range t.params {
			r.reach(p.typ)
		}
		r.reach(t.result)
	case kStruct, kUnion:
		if r.wanted[t.tag] != nil {
			return
		}
		r.wanted[t.tag] = t
		for _, f := range t.fields {
			r.reach(f.typ)
		}
	}
}

func (r *headerReader) typedef(name string, typ *ctype) {
	if !typ.isMembered() {
		r.problem(name, "is a typedef for %s, which Gyro has no name for; write %s where it is wanted", typ, typ)
		return
	}
	if typ.tag != name {
		r.problem(name, "is a typedef for %s %s, and it is imported under that tag", typ.keyword(), typ.tag)
		return
	}
	r.aggregate(name, typ)
}

func (r *headerReader) aggregate(name string, typ *ctype) {
	r.reach(typ)
	r.header.Types = append(r.header.Types, typ.tag)
}

// macro evaluates an object-like macro that is an integer constant
// expression. Anything else a macro can be — a string, a float, a
// statement, a call — is not a constant a program can import, and says
// so.
func (r *headerReader) macro(name string, m *macro) {
	if m.function {
		r.problem(name, "is a function-like macro, which is not a name a program can import")
		return
	}
	if m.dynamic != nil || len(m.body) == 0 {
		r.problem(name, "is a macro with no value")
		return
	}
	expanded, err := r.pp.expandAll(m.body)
	if err != nil {
		r.problem(name, "%s", err)
		return
	}
	for i := range expanded {
		if expanded[i].kind == tokIdent && keywords[expanded[i].text] {
			expanded[i].kind = tokKeyword
		}
	}
	value, typ, err := r.evaluate(expanded)
	if err != nil {
		r.problem(name, "is a macro that is not an integer constant: %s", err)
		return
	}
	prim := constPrim(typ)
	if prim == nil {
		r.problem(name, "is a macro of type %s, which Gyro has no integer type for", typ)
		return
	}
	r.header.Consts = append(r.header.Consts, HeaderConst{
		Name: name, Type: prim, Value: value, Unsigned: typ.isUnsigned(),
	})
}

// evaluate folds tokens as a C constant expression, with the type C
// gives the result.
func (r *headerReader) evaluate(toks []token) (value int64, typ *ctype, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			caught, ok := rec.(failure)
			if !ok {
				panic(rec)
			}
			err = fmt.Errorf("%s", strings.TrimPrefix(caught.err.Error(), ":0:0: "))
		}
	}()
	eof := token{kind: tokEOF}
	if len(toks) > 0 {
		eof.pos = toks[0].pos
	}
	p := &parser{
		toks:     append(append([]token(nil), toks...), eof),
		typedefs: r.unit.typedefs,
		structs:  map[string]*ctype{},
		enums:    r.unit.enums,
		enumTags: map[string]bool{},
	}
	for _, s := range r.unit.structs {
		p.structs[s.tag] = s
	}
	e := p.conditional()
	if !p.at("") && p.peek().kind != tokEOF {
		return 0, nil, fmt.Errorf("%s is left over", describe(p.peek()))
	}
	return foldTyped(e, r.unit.enums)
}

// constPrim is the METALS integer type of a constant.
func constPrim(t *ctype) *metals.Prim {
	switch t.kind {
	case kChar:
		return metals.TInt8
	case kUChar:
		return metals.TUint8
	case kShort:
		return metals.TInt16
	case kUShort:
		return metals.TUint16
	case kInt:
		return metals.TInt32
	case kUInt:
		return metals.TUint32
	case kLong:
		return metals.TInt64
	case kULong:
		return metals.TUint64
	}
	return nil
}

// module builds the METALS the request needs: the aggregates, laid out
// the way C lays them out, and the import of each function.
func (r *headerReader) module(name, library string) (out *metals.Module, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			caught, ok := rec.(failure)
			if !ok {
				panic(rec)
			}
			out, err = nil, caught.err
		}
	}()

	// A request that reached an aggregate no module can declare fails
	// that name and not the import. The aggregates are tried one at a
	// time for the reason a function's signature is: so the one that
	// cannot cross is the one that is blamed.
	r.dropUnrepresentable()

	for tag, t := range r.wanted {
		if why := r.unrepresentable(t); why != "" {
			delete(r.wanted, tag)
		}
	}

	l := &lowerer{
		out:     &metals.Module{Name: name},
		records: map[string]*ctype{},
		strings: map[string]string{},
		statics: map[string]bool{},
		natives: map[string]*metals.FnType{},
	}
	var structs []*ctype
	for _, s := range r.unit.structs {
		if r.wanted[s.tag] == s {
			structs = append(structs, s)
		}
	}
	l.declareRecords(name, []*translation{{path: name, unit: &unit{structs: structs}}})

	if len(r.natives) > 0 {
		imp := &metals.Import{Module: metals.ExternModule(library)}
		sort.Slice(r.natives, func(i, j int) bool { return r.natives[i].Name < r.natives[j].Name })
		for _, fn := range r.natives {
			imp.Funcs = append(imp.Funcs, metals.ImportedFunc{Name: fn.Name, Sig: fn.Sig})
		}
		l.out.Imports = append(l.out.Imports, imp)
	}
	return l.out, nil
}

// unrepresentable answers why METALS cannot declare a type, or "".
func (r *headerReader) unrepresentable(t *ctype) string {
	return unrepresentable(t, map[*ctype]bool{})
}

func unrepresentable(t *ctype, seen map[*ctype]bool) string {
	if t == nil || seen[t] {
		return ""
	}
	seen[t] = true
	switch t.kind {
	case kLongDouble:
		return "has a long double in it, which METALS has no primitive for"
	case kPtr, kArray:
		return unrepresentable(t.elem, seen)
	case kFunc:
		for _, p := range t.params {
			if why := unrepresentable(p.typ, seen); why != "" {
				return why
			}
		}
		return unrepresentable(t.result, seen)
	case kStruct, kUnion:
		for _, f := range t.fields {
			if why := unrepresentable(f.typ, seen); why != "" {
				return why
			}
		}
	}
	return ""
}

// dropUnrepresentable removes every requested thing that depends on a
// type METALS cannot declare, and says why.
func (r *headerReader) dropUnrepresentable() {
	kept := r.natives[:0]
	for _, fn := range r.natives {
		decl := r.funcs[fn.Name]
		why := ""
		for _, p := range decl.params {
			if why = r.unrepresentable(p.typ); why != "" {
				break
			}
		}
		if why == "" {
			why = r.unrepresentable(decl.result)
		}
		if why != "" {
			r.problem(fn.Name, "%s", why)
			continue
		}
		kept = append(kept, fn)
	}
	r.natives = kept
	funcs := r.header.Funcs[:0]
	for _, fn := range r.header.Funcs {
		if _, dropped := r.header.Problems[fn.Name]; !dropped {
			funcs = append(funcs, fn)
		}
	}
	r.header.Funcs = funcs

	types := r.header.Types[:0]
	for _, tag := range r.header.Types {
		if why := r.unrepresentable(r.wanted[tag]); why != "" {
			r.problem(tag, "%s", why)
			continue
		}
		types = append(types, tag)
	}
	r.header.Types = types
}
