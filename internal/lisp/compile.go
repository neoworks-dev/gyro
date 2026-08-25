package lisp

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/metals"
)

// ModuleName is the METALS module name for a Lisp file. The `lisp:`
// scheme is what makes a call from Gyro a boundary crossing, so the
// verifier applies the boundary rules to anything this module exports.
func ModuleName(path string) string {
	base := filepath.Base(path)
	return "lisp:" + strings.TrimSuffix(base, filepath.Ext(base))
}

// Compile reads a Lisp source file and produces its METALS module. The
// result still has to be verified: this package is a producer like any
// other, and the format trusts none of them.
func Compile(path, source string) (*metals.Module, error) {
	data, err := read(source)
	if err != nil {
		return nil, render(path, source, err)
	}

	c := &compiler{
		file:     diag.NewFile(path, source),
		module:   &metals.Module{Name: ModuleName(path)},
		sigs:     map[string]*metals.FnType{},
		declared: map[string]int{},
		defined:  map[string]bool{},
		exported: map[string]bool{},
		imported: map[string]bool{},
	}
	if err := c.run(data); err != nil {
		return nil, render(path, source, err)
	}
	return c.module, nil
}

type compiler struct {
	file   *diag.File
	module *metals.Module
	// sigs holds every top-level function's type, filled before any body
	// is compiled so that definitions may call each other in any order.
	sigs map[string]*metals.FnType
	// declared records where each (: name type) was written, so a
	// duplicate or an unused declaration can name its own position.
	declared map[string]int
	defined  map[string]bool
	exported map[string]bool
	imported map[string]bool
	// lambdas counts the lifted functions, so each gets its own name.
	lambdas int
	// defs are the function definitions in source order.
	defs []*definition
}

type definition struct {
	name   string
	params []*symbol
	body   []datum
	offset int
}

func (c *compiler) errf(offset int, format string, args ...any) error {
	return &posError{offset: offset, msg: fmt.Sprintf(format, args...)}
}

func (c *compiler) run(data []datum) error {
	if err := c.collect(data); err != nil {
		return err
	}
	for _, def := range c.defs {
		if err := c.lower(def); err != nil {
			return err
		}
	}
	for name, offset := range c.declared {
		if !c.defined[name] {
			return c.errf(offset, "%s is declared but never defined", name)
		}
	}
	for name := range c.exported {
		if !c.defined[name] {
			return c.errf(0, "%s is exported but never defined", name)
		}
	}
	return nil
}

// collect walks the top level once, so that every function's type is
// known before any body is compiled. Mutual recursion needs that, and a
// Lisp with no forward declarations would be a poor test of a format
// whose whole point is separate producers.
func (c *compiler) collect(data []datum) error {
	for _, top := range data {
		form, ok := top.(*list)
		if !ok || len(form.items) == 0 {
			return c.errf(top.pos(), "a top-level form is a list")
		}
		head, ok := form.items[0].(*symbol)
		if !ok {
			return c.errf(form.items[0].pos(), "a top-level form starts with a keyword")
		}

		switch head.name {
		case "export":
			for _, item := range form.items[1:] {
				name, ok := item.(*symbol)
				if !ok {
					return c.errf(item.pos(), "export takes names")
				}
				c.exported[name.name] = true
			}

		case ":":
			if err := c.declare(form); err != nil {
				return err
			}

		case "define":
			if err := c.define(form); err != nil {
				return err
			}

		default:
			return c.errf(head.offset, "unknown top-level form %s; expected export, : or define", head.name)
		}
	}
	return nil
}

func (c *compiler) declare(form *list) error {
	if len(form.items) != 3 {
		return c.errf(form.offset, "a declaration is (: name type)")
	}
	name, ok := form.items[1].(*symbol)
	if !ok {
		return c.errf(form.items[1].pos(), "a declaration names a function")
	}
	if where, dup := c.declared[name.name]; dup {
		return c.errf(name.offset, "%s is declared twice; the first is at %s",
			name.name, c.where(where))
	}

	typ, err := parseType(form.items[2])
	if err != nil {
		return err
	}
	sig, ok := typ.(*metals.FnType)
	if !ok {
		return c.errf(form.items[2].pos(),
			"%s is declared %s; only functions can be declared at the top level", name.name, typeName(typ))
	}
	c.declared[name.name] = name.offset
	c.sigs[name.name] = sig
	return nil
}

func (c *compiler) define(form *list) error {
	if len(form.items) < 3 {
		return c.errf(form.offset, "a definition is (define (name params...) body...)")
	}
	header, ok := form.items[1].(*list)
	if !ok {
		return c.errf(form.items[1].pos(),
			"only functions can be defined: a module-level value would need an initializer to run before main, which METALS leaves to the front end and this one does not do yet")
	}
	if len(header.items) == 0 {
		return c.errf(header.offset, "a definition needs a name")
	}

	name, ok := header.items[0].(*symbol)
	if !ok {
		return c.errf(header.items[0].pos(), "a definition needs a name")
	}
	if c.defined[name.name] {
		return c.errf(name.offset, "%s is defined twice", name.name)
	}
	sig, ok := c.sigs[name.name]
	if !ok {
		return c.errf(name.offset, "%s has no type declaration; write (: %s (-> ...)) above it",
			name.name, name.name)
	}

	params := make([]*symbol, 0, len(header.items)-1)
	for _, item := range header.items[1:] {
		param, ok := item.(*symbol)
		if !ok {
			return c.errf(item.pos(), "a parameter is a name")
		}
		params = append(params, param)
	}
	if len(params) != len(sig.Params) {
		return c.errf(header.offset, "%s is declared with %d parameter(s) but defined with %d",
			name.name, len(sig.Params), len(params))
	}

	c.defined[name.name] = true
	c.defs = append(c.defs, &definition{
		name:   name.name,
		params: params,
		body:   form.items[2:],
		offset: form.offset,
	})
	return nil
}

// render turns a positioned compile error into one that names the file
// and line, matching how the rest of the toolchain reports.
func render(path, source string, err error) error {
	var positioned *posError
	if !errors.As(err, &positioned) {
		return err
	}
	file := diag.NewFile(path, source)
	pos := file.Position(positioned.offset)
	return fmt.Errorf("%s:%d:%d: %s", file.Name, pos.Line, pos.Column, positioned.msg)
}

func (c *compiler) where(offset int) string {
	pos := c.file.Position(offset)
	return fmt.Sprintf("%d:%d", pos.Line, pos.Column)
}

// ---------------------------------------------------------------------------
// Lowering
// ---------------------------------------------------------------------------

func (c *compiler) lower(def *definition) error {
	sig := c.sigs[def.name]
	fn := &metals.Func{
		Name:   def.name,
		Export: c.exported[def.name],
		Result: sig.Result,
	}

	f := &fnScope{c: c, fn: fn}
	scope := newScope(nil)
	for i, param := range def.params {
		// A parameter is already a register, so it needs no binding
		// instruction; naming it after the source name keeps the IR
		// readable, and the suffix keeps it distinct from a later let.
		reg := f.gensym(param.name)
		fn.Params = append(fn.Params, metals.Param{Name: reg, Type: sig.Params[i]})
		scope.bind(param.name, reg, sig.Params[i])
	}

	var body []metals.Instr
	result, resultType, err := f.sequence(def.body, sig.Result, scope, &body)
	if err != nil {
		return err
	}
	if sig.Result == nil {
		body = append(body, &metals.Return{})
	} else {
		if !metals.TypesEqual(resultType, sig.Result) {
			return c.errf(def.body[len(def.body)-1].pos(),
				"%s returns %s but is declared to return %s",
				def.name, typeName(resultType), typeName(sig.Result))
		}
		body = append(body, &metals.Return{Val: result})
	}

	fn.Body = body
	c.module.Funcs = append(c.module.Funcs, fn)
	return nil
}

// fnScope carries what one function's compilation needs: the function
// being built and a counter for the register names.
type fnScope struct {
	c  *compiler
	fn *metals.Func
	n  int
}

func (f *fnScope) gensym(hint string) string {
	f.n++
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '_'
	}, hint)
	return fmt.Sprintf("%s.%d", clean, f.n)
}

// scope is a chain of lexical frames mapping a Lisp name to the register
// holding it and that register's type.
type scope struct {
	parent *scope
	names  map[string]binding
}

type binding struct {
	reg string
	typ metals.Type
	// captured marks a name a lambda copied out of its enclosing scope.
	// Assigning to one would write the copy and leave the original
	// alone, so set! refuses rather than doing that quietly.
	captured bool
}

func newScope(parent *scope) *scope {
	return &scope{parent: parent, names: map[string]binding{}}
}

func (s *scope) bind(name, reg string, typ metals.Type) {
	s.names[name] = binding{reg: reg, typ: typ}
}

func (s *scope) lookup(name string) (binding, bool) {
	for at := s; at != nil; at = at.parent {
		if b, ok := at.names[name]; ok {
			return b, true
		}
	}
	return binding{}, false
}

// sequence compiles a body: every form but the last for its effect, the
// last for its value. want is the type the last form must produce, or nil
// when the function returns nothing.
func (f *fnScope) sequence(forms []datum, want metals.Type, s *scope, body *[]metals.Instr) (string, metals.Type, error) {
	if len(forms) == 0 {
		return "", nil, f.c.errf(0, "an empty body produces no value")
	}

	forms, s, err := f.internalDefines(forms, s, body)
	if err != nil {
		return "", nil, err
	}
	if len(forms) == 0 {
		return "", nil, f.c.errf(0, "a body of nothing but definitions produces no value")
	}

	for _, form := range forms[:len(forms)-1] {
		if _, _, err := f.expr(form, nil, s, body); err != nil {
			return "", nil, err
		}
	}
	return f.expr(forms[len(forms)-1], want, s, body)
}

// internalDefines consumes the definitions a body may open with, the way
// Scheme allows, and returns what is left along with the scope they bind
// into. Each takes its type from a `(: name type)` beside it, so the
// standard spelling
//
//	(define (outer n)
//	  (: double (-> i64 i64))
//	  (define (double v) (* v n))
//	  (double 21))
//
// works without any annotation the reader of an ordinary Scheme would
// not recognise.
func (f *fnScope) internalDefines(forms []datum, s *scope, body *[]metals.Instr) ([]datum, *scope, error) {
	declared := map[string]*metals.FnType{}
	inner := s
	at := 0

	for ; at < len(forms); at++ {
		form, ok := forms[at].(*list)
		if !ok || len(form.items) == 0 {
			break
		}
		head, ok := form.items[0].(*symbol)
		if !ok || (head.name != "define" && head.name != ":") {
			break
		}

		if head.name == ":" {
			if len(form.items) != 3 {
				return nil, nil, f.c.errf(form.offset, "a declaration is (: name type)")
			}
			name, ok := form.items[1].(*symbol)
			if !ok {
				return nil, nil, f.c.errf(form.items[1].pos(), "a declaration names a function")
			}
			typ, err := parseType(form.items[2])
			if err != nil {
				return nil, nil, err
			}
			sig, ok := typ.(*metals.FnType)
			if !ok {
				return nil, nil, f.c.errf(form.items[2].pos(),
					"%s is declared %s; only functions can be declared inside a body", name.name, typeName(typ))
			}
			declared[name.name] = sig
			continue
		}

		if inner == s {
			inner = newScope(s)
		}
		if err := f.internalDefine(form, declared, inner, body); err != nil {
			return nil, nil, err
		}
	}
	return forms[at:], inner, nil
}

func (f *fnScope) internalDefine(form *list, declared map[string]*metals.FnType, s *scope, body *[]metals.Instr) error {
	if len(form.items) < 3 {
		return f.c.errf(form.offset, "a definition is (define (name params...) body...)")
	}
	header, ok := form.items[1].(*list)
	if !ok || len(header.items) == 0 {
		return f.c.errf(form.items[1].pos(),
			"only functions can be defined inside a body; bind a value with let")
	}
	name, ok := header.items[0].(*symbol)
	if !ok {
		return f.c.errf(header.items[0].pos(), "a definition needs a name")
	}

	sig, ok := declared[name.name]
	if !ok {
		return f.c.errf(name.offset, "%s has no type declaration; write (: %s (-> ...)) above it",
			name.name, name.name)
	}
	if len(header.items)-1 != len(sig.Params) {
		return f.c.errf(header.offset, "%s is declared with %d parameter(s) but defined with %d",
			name.name, len(sig.Params), len(header.items)-1)
	}

	names := make([]string, len(sig.Params))
	for i, item := range header.items[1:] {
		param, ok := item.(*symbol)
		if !ok {
			return f.c.errf(item.pos(), "a parameter is a name")
		}
		names[i] = param.name
	}

	// The name is bound only after the body is compiled, so a reference
	// to it from inside is caught here rather than capturing a register
	// that does not exist yet.
	value, typ, err := f.closureValue(names, sig.Params, form.items[2:], sig.Result, s, body)
	if err != nil {
		return err
	}
	if _, taken := s.names[name.name]; taken {
		return f.c.errf(name.offset, "%s is defined twice in this body", name.name)
	}
	s.bind(name.name, value, typ)
	return nil
}
