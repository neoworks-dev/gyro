package cc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The preprocessor.
//
// It is a separate language over the same tokens, and the reason this
// front end has to have its own rather than shelling out to clang's is
// the reason the front end exists at all: a translation unit that has
// already been expanded by someone else is fine, but a header is how C
// says what a library's types are, and `#include` is the only way to
// reach one. Without it, an imported `.c` can only be a file written for
// this compiler; with it, an imported `.c` is a C file.
//
// The expansion algorithm is Prosser's, the one every C compiler uses:
// each token carries the set of macros it came out of, and a macro will
// not expand inside its own output. That is what makes `#define f(x)
// f(x)` terminate, and it is why the hideset lives on the token rather
// than in a stack the expander walks.

// Options are the knobs a caller has over a C compilation. They are the
// command-line ones a C compiler has and nothing more: where to look for
// headers, and what to define before the first line.
type Options struct {
	// Include is the directory list `#include <...>` searches, in order.
	// A quoted include looks beside the including file first and then
	// searches this list, which is what every C compiler does.
	Include []string
	// Define is the initial macro set, spelled "NAME" or "NAME=body" the
	// way -D is.
	Define []string
	// Read reads an included file. Nil means the filesystem; a caller
	// that has its own idea of where sources come from supplies one.
	Read func(path string) (string, error)
	// List returns the `.c` files in a directory, which is how a
	// translation unit finds the ones it links against. Nil means the
	// filesystem, as for Read.
	List func(dir string) ([]string, error)
	// Package names the pkg-config package whose library a function the
	// program declares and nothing defines is resolved against. Empty
	// means the C library, which is always linked.
	Package string
}

func (o *Options) library() string {
	if o == nil || o.Package == "" {
		return "c"
	}
	return o.Package
}

func (o *Options) read(path string) (string, error) {
	if o != nil && o.Read != nil {
		return o.Read(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// maxIncludeDepth stops a header that includes itself without a guard
// from taking the process down with it.
const maxIncludeDepth = 200

type macro struct {
	name string
	// function distinguishes `#define M(x) ...` from `#define M (x)`,
	// which differ only by the space before the parenthesis.
	function bool
	params   []string
	variadic bool
	body     []token
	pos      position
	// dynamic computes the replacement for __FILE__ and __LINE__, whose
	// value is the invocation's and not the definition's.
	dynamic func(at token) []token
}

// condition is one frame of the `#if` stack. taken says some branch of
// this conditional has already run, which is what makes every later
// `#elif` false regardless of its expression.
type condition struct {
	active   bool
	taken    bool
	seenElse bool
	pos      position
}

type preprocessor struct {
	opt    *Options
	macros map[string]*macro
	// once is the set of files a `#pragma once` has closed, keyed by the
	// path they were resolved to.
	once map[string]bool
	// times counts how often each file has been included. A header is
	// legitimately read more than once — that is what an include guard
	// is for — so the runaway this catches is one that includes itself
	// without one, and the count is what tells the two apart.
	times map[string]int
	// pending is how #include hands a header's tokens back to the loop:
	// they are spliced in where the directive stood, so that a
	// conditional opened outside a header still governs what is inside
	// it, and so that a macro defined by one header is defined for the
	// next.
	pending []token
}

func preprocess(file, source string, opt *Options) ([]token, error) {
	pp := &preprocessor{
		opt:    opt,
		macros: map[string]*macro{},
		once:   map[string]bool{},
		times:  map[string]int{},
	}
	pp.predefine()
	if opt != nil {
		for _, d := range opt.Define {
			if err := pp.commandLineDefine(d); err != nil {
				return nil, err
			}
		}
	}

	toks, err := scan(file, source)
	if err != nil {
		return nil, err
	}
	out, err := pp.run(toks)
	if err != nil {
		return nil, err
	}
	// A macro may produce a keyword, and `##` may paste one together, so
	// what is a keyword is decided after expansion rather than during the
	// scan that ran before any of it existed.
	for i := range out {
		if out[i].kind == tokIdent && keywords[out[i].text] {
			out[i].kind = tokKeyword
		}
	}
	return append(out, token{kind: tokEOF, pos: position{file: file}}), nil
}

// predefine is the macro set a C program may assume. The target ones say
// what METALS' layout rule already says — LP64 on x86-64 — so a header
// that picks a type by them picks the one this front end lays out.
func (pp *preprocessor) predefine() {
	fixed := map[string]string{
		"__STDC__":                "1",
		"__STDC_VERSION__":        "199901L",
		"__STDC_HOSTED__":         "1",
		"__x86_64__":              "1",
		"__x86_64":                "1",
		"__linux__":               "1",
		"__unix__":                "1",
		"__ELF__":                 "1",
		"__LP64__":                "1",
		"_LP64":                   "1",
		"__CHAR_BIT__":            "8",
		"__SIZEOF_SHORT__":        "2",
		"__SIZEOF_INT__":          "4",
		"__SIZEOF_LONG__":         "8",
		"__SIZEOF_LONG_LONG__":    "8",
		"__SIZEOF_POINTER__":      "8",
		"__SIZEOF_FLOAT__":        "4",
		"__SIZEOF_DOUBLE__":       "8",
		"__BYTE_ORDER__":          "1234",
		"__ORDER_LITTLE_ENDIAN__": "1234",
		"__ORDER_BIG_ENDIAN__":    "4321",
		// The one a header can test to find out it is being read by this
		// compiler rather than by clang through the wasm bridge.
		"__gyro_cc__": "1",
	}
	for name, body := range fixed {
		toks, err := scan("<predefined>", body)
		if err != nil {
			panic("cc: a predefined macro does not scan: " + name)
		}
		pp.macros[name] = &macro{name: name, body: dropEOF(toks)}
	}

	pp.macros["__FILE__"] = &macro{name: "__FILE__", dynamic: func(at token) []token {
		return []token{{kind: tokStr, text: strconv.Quote(at.pos.file), sval: at.pos.file, pos: at.pos}}
	}}
	pp.macros["__LINE__"] = &macro{name: "__LINE__", dynamic: func(at token) []token {
		text := strconv.Itoa(at.pos.line)
		return []token{{kind: tokInt, text: text, ival: uint64(at.pos.line), pos: at.pos}}
	}}
}

func (pp *preprocessor) commandLineDefine(spec string) error {
	name, body, hasBody := strings.Cut(spec, "=")
	if !hasBody {
		body = "1"
	}
	toks, err := scan("<command line>", name+" "+body)
	if err != nil {
		return err
	}
	toks = dropEOF(toks)
	if len(toks) == 0 || toks[0].kind != tokIdent {
		return fmt.Errorf("-D%s does not define a name", spec)
	}
	pp.macros[toks[0].text] = &macro{name: toks[0].text, body: toks[1:]}
	return nil
}

func dropEOF(toks []token) []token {
	if n := len(toks); n > 0 && toks[n-1].kind == tokEOF {
		return toks[:n-1]
	}
	return toks
}

// ---------------------------------------------------------------------------
// The directive loop
// ---------------------------------------------------------------------------

func (pp *preprocessor) run(input []token) ([]token, error) {
	var out []token
	var conds []*condition

	active := func() bool {
		for _, c := range conds {
			if !c.active {
				return false
			}
		}
		return true
	}

	// The input is a queue with a front to push onto: an include or a
	// macro expansion puts its tokens ahead of what is left, and doing
	// that by copying the rest of the stream each time is quadratic in
	// the size of the headers, which is what a real libc is.
	q := &tokenQueue{buf: input}
	for {
		toks := q.rest()
		if len(toks) == 0 || toks[0].kind == tokEOF {
			break
		}
		tok := toks[0]

		if tok.bol && tok.is("#") {
			line, next := directiveLine(toks, 1)
			q.drop(next)
			var err error
			conds, err = pp.directive(tok, line, conds, active())
			if err != nil {
				return nil, err
			}
			if pp.pending != nil {
				q.pushFront(pp.pending)
				pp.pending = nil
			}
			continue
		}

		if !active() {
			q.drop(1)
			continue
		}

		if tok.kind == tokIdent {
			replacement, consumed, ok, err := pp.expandAt(toks, 0)
			if err != nil {
				return nil, err
			}
			if ok {
				q.drop(consumed)
				q.pushFront(replacement)
				continue
			}
		}
		out = append(out, tok)
		q.drop(1)
	}

	if len(conds) > 0 {
		return nil, errorAt(conds[len(conds)-1].pos, "this conditional is never closed")
	}
	return out, nil
}

// tokenQueue is the preprocessor's input: tokens come off the front, and
// an expansion or an included file goes back on it.
type tokenQueue struct {
	buf  []token
	head int
}

func (q *tokenQueue) rest() []token { return q.buf[q.head:] }
func (q *tokenQueue) drop(n int)    { q.head += n }

func (q *tokenQueue) pushFront(with []token) {
	if len(with) == 0 {
		return
	}
	if q.head < len(with) {
		remaining := q.buf[q.head:]
		room := len(with) + len(remaining)/2 + 1024
		grown := make([]token, room+len(remaining))
		copy(grown[room:], remaining)
		q.buf, q.head = grown, room
	}
	q.head -= len(with)
	copy(q.buf[q.head:], with)
}

// directiveLine takes the rest of a directive's logical line: every token
// up to the next one that begins a line.
func directiveLine(toks []token, i int) ([]token, int) {
	start := i
	for i < len(toks) && toks[i].kind != tokEOF && !toks[i].bol {
		i++
	}
	return toks[start:i], i
}

func splice(toks []token, at, n int, with []token) []token {
	out := make([]token, 0, len(toks)-n+len(with))
	out = append(out, toks[:at]...)
	out = append(out, with...)
	return append(out, toks[at+n:]...)
}

func (pp *preprocessor) directive(hash token, line []token, conds []*condition, active bool) ([]*condition, error) {
	// The null directive is a `#` on a line of its own, and it does
	// nothing at all.
	if len(line) == 0 {
		return conds, nil
	}
	name := line[0].text
	rest := line[1:]

	// The conditionals run whether or not this branch is active, because
	// they are what decides which branch is.
	switch name {
	case "if", "ifdef", "ifndef":
		frame := &condition{pos: line[0].pos}
		if !active {
			// A conditional inside a dead branch is dead whatever it
			// says, and its expression is never evaluated — which is what
			// lets `#ifdef X` guard a `#if X > 3` that would not parse.
			frame.active, frame.taken = false, true
			return append(conds, frame), nil
		}
		value, err := pp.conditionValue(name, line[0], rest)
		if err != nil {
			return nil, err
		}
		frame.active, frame.taken = value, value
		return append(conds, frame), nil

	case "elif", "else":
		if len(conds) == 0 {
			return nil, errorAt(line[0].pos, "#%s without a matching #if", name)
		}
		frame := conds[len(conds)-1]
		if frame.seenElse {
			return nil, errorAt(line[0].pos, "#%s comes after this conditional's #else", name)
		}
		if name == "else" {
			frame.seenElse = true
			if len(rest) > 0 {
				return nil, errorAt(rest[0].pos, "#else takes nothing after it")
			}
		}

		// Whether the enclosing conditionals are live is separate from
		// whether this branch is: an #elif inside a dead #if stays dead.
		outer := true
		for _, c := range conds[:len(conds)-1] {
			outer = outer && c.active
		}
		if !outer || frame.taken {
			frame.active = false
			if name == "elif" && frame.taken {
				return conds, nil
			}
			return conds, nil
		}
		if name == "else" {
			frame.active, frame.taken = true, true
			return conds, nil
		}
		value, err := pp.conditionValue("if", line[0], rest)
		if err != nil {
			return nil, err
		}
		frame.active, frame.taken = value, value
		return conds, nil

	case "endif":
		if len(conds) == 0 {
			return nil, errorAt(line[0].pos, "#endif without a matching #if")
		}
		return conds[:len(conds)-1], nil
	}

	if !active {
		return conds, nil
	}

	switch name {
	case "include":
		return conds, pp.include(line[0], rest)

	case "define":
		return conds, pp.define(rest)

	case "undef":
		if len(rest) == 0 || rest[0].kind != tokIdent {
			return nil, errorAt(line[0].pos, "#undef needs a name")
		}
		delete(pp.macros, rest[0].text)
		return conds, nil

	case "error":
		return nil, errorAt(line[0].pos, "#error %s", spell(rest))

	case "warning":
		// A warning this compiler cannot print anywhere useful is one it
		// should not stop for either.
		return conds, nil

	case "pragma":
		if len(rest) == 1 && rest[0].text == "once" {
			pp.once[line[0].pos.file] = true
		}
		return conds, nil

	case "line":
		// Line control renumbers a generated file's diagnostics. This
		// compiler reports the position it read, which is the one an
		// editor can open.
		return conds, nil
	}
	return nil, errorAt(line[0].pos, "#%s is not a directive this compiler knows", name)
}

// ---------------------------------------------------------------------------
// #include
// ---------------------------------------------------------------------------

func (pp *preprocessor) include(directive token, rest []token) error {
	path, angled, err := pp.includePath(directive, rest)
	if err != nil {
		return err
	}

	// A bundled header answers before the search does, and only for an
	// angled include: `"stdarg.h"` beside the program is the program's.
	resolved, source, ok := bundledHeader(path, angled)
	if !ok {
		var err error
		resolved, source, err = pp.find(directive.pos.file, path, angled)
		if err != nil {
			return err
		}
	}
	if pp.once[resolved] {
		return nil
	}

	pp.times[resolved]++
	if pp.times[resolved] > maxIncludeDepth {
		return errorAt(directive.pos, "%s has been included %d times; it is including itself without a guard", path, maxIncludeDepth)
	}

	toks, err := scan(resolved, source)
	if err != nil {
		return err
	}
	// The header's tokens are processed by the same loop that asked for
	// them, which is why they go back into the stream rather than through
	// a recursive run: a `#if` in the includer has to govern them.
	pp.pending = dropEOF(toks)
	return nil
}

// includePath reads what a `#include` names. A quoted form is one token,
// an angled one is however many tokens `<sys/types.h>` scans as, and
// either may arrive from a macro — which is why the tokens are expanded
// when they are neither.
func (pp *preprocessor) includePath(directive token, rest []token) (path string, angled bool, err error) {
	if len(rest) == 0 {
		return "", false, errorAt(directive.pos, "#include needs a file name")
	}
	if rest[0].kind == tokStr {
		return rest[0].sval, false, nil
	}
	if rest[0].is("<") {
		for i := 1; i < len(rest); i++ {
			if rest[i].is(">") {
				return spellTight(rest[1:i]), true, nil
			}
		}
		return "", false, errorAt(directive.pos, "this #include has no closing '>'")
	}

	expanded, err := pp.expandAll(rest)
	if err != nil {
		return "", false, err
	}
	if len(expanded) > 0 && (expanded[0].kind == tokStr || expanded[0].is("<")) {
		return pp.includePath(directive, expanded)
	}
	return "", false, errorAt(directive.pos, "#include names neither \"a file\" nor <a file>")
}

func (pp *preprocessor) find(from, path string, angled bool) (resolved, source string, err error) {
	var tried []string

	try := func(candidate string) (string, string, bool) {
		text, err := pp.opt.read(candidate)
		if err != nil {
			tried = append(tried, candidate)
			return "", "", false
		}
		return candidate, text, true
	}

	if filepath.IsAbs(path) {
		if resolved, source, ok := try(path); ok {
			return resolved, source, nil
		}
	} else {
		if !angled {
			if resolved, source, ok := try(filepath.Join(filepath.Dir(from), path)); ok {
				return resolved, source, nil
			}
		}
		if pp.opt != nil {
			for _, dir := range pp.opt.Include {
				if resolved, source, ok := try(filepath.Join(dir, path)); ok {
					return resolved, source, nil
				}
			}
		}
	}

	open, close := `"`, `"`
	if angled {
		open, close = "<", ">"
	}
	return "", "", fmt.Errorf("%s: cannot find %s%s%s; looked in %s",
		from, open, path, close, strings.Join(tried, ", "))
}

// ---------------------------------------------------------------------------
// #define
// ---------------------------------------------------------------------------

func (pp *preprocessor) define(rest []token) error {
	if len(rest) == 0 || rest[0].kind != tokIdent {
		if len(rest) == 0 {
			return fmt.Errorf("#define needs a name")
		}
		return errorAt(rest[0].pos, "#define needs a name, not %s", describe(rest[0]))
	}
	m := &macro{name: rest[0].text, pos: rest[0].pos}
	rest = rest[1:]

	// `#define M(x)` defines a function-like macro and `#define M (x)`
	// defines an object-like one whose body starts with a parenthesis.
	// The space is the only difference and this is the only place in C
	// where one means anything.
	if len(rest) > 0 && rest[0].is("(") && !rest[0].space {
		m.function = true
		i := 1
		for {
			if i >= len(rest) {
				return errorAt(m.pos, "this macro's parameter list is never closed")
			}
			if rest[i].is(")") {
				i++
				break
			}
			if len(m.params) > 0 || m.variadic {
				if !rest[i].is(",") {
					return errorAt(rest[i].pos, "expected ',' in a macro parameter list, found %s", describe(rest[i]))
				}
				i++
			}
			if i >= len(rest) {
				return errorAt(m.pos, "this macro's parameter list is never closed")
			}
			switch {
			case rest[i].is("..."):
				m.variadic = true
				m.params = append(m.params, "__VA_ARGS__")
				i++
			case rest[i].kind == tokIdent:
				name := rest[i].text
				i++
				// GNU's named variadic, `#define log(fmt, args...)`, which
				// enough real headers use to be worth the four lines.
				if i < len(rest) && rest[i].is("...") {
					m.variadic = true
					i++
				}
				m.params = append(m.params, name)
			default:
				return errorAt(rest[i].pos, "expected a parameter name, found %s", describe(rest[i]))
			}
			if m.variadic && i < len(rest) && !rest[i].is(")") {
				return errorAt(rest[i].pos, "a variadic parameter is the last one")
			}
		}
		rest = rest[i:]
	}

	// A copy: the line is a view into the input queue, and the queue
	// writes over what it has already consumed.
	m.body = append([]token(nil), rest...)
	if len(m.body) > 0 {
		if m.body[0].is("##") || m.body[len(m.body)-1].is("##") {
			return errorAt(m.pos, "## joins two tokens and has nothing on one side here")
		}
	}
	pp.macros[m.name] = m
	return nil
}

// ---------------------------------------------------------------------------
// Expansion
// ---------------------------------------------------------------------------

// expandAt expands the macro invocation at toks[i], if there is one. It
// reports how many tokens the invocation covered — one for an object-like
// macro, and through the closing parenthesis for a function-like one.
func (pp *preprocessor) expandAt(toks []token, i int) (replacement []token, consumed int, ok bool, err error) {
	name := toks[i].text
	if toks[i].hidden(name) {
		return nil, 0, false, nil
	}
	m := pp.macros[name]
	if m == nil {
		return nil, 0, false, nil
	}

	if m.dynamic != nil {
		return position1(m.dynamic(toks[i]), toks[i]), 1, true, nil
	}

	if !m.function {
		hide := withName(toks[i].hide, name)
		body, err := pp.subst(m, nil, hide, toks[i])
		if err != nil {
			return nil, 0, false, err
		}
		return position1(body, toks[i]), 1, true, nil
	}

	// A function-like macro's name without a following parenthesis is an
	// ordinary identifier, which is how `#define min(a,b) ...` leaves a
	// variable called min alone.
	open := i + 1
	if open >= len(toks) || !toks[open].is("(") {
		return nil, 0, false, nil
	}
	args, end, err := pp.arguments(m, toks, open)
	if err != nil {
		return nil, 0, false, err
	}

	// The hideset of the result is what the name and the closing
	// parenthesis agree on, plus the macro itself. Taking the
	// intersection is what lets a macro that was passed as an argument
	// expand again in a context where it is no longer hidden.
	hide := withName(intersect(toks[i].hide, toks[end].hide), name)
	body, err := pp.subst(m, args, hide, toks[i])
	if err != nil {
		return nil, 0, false, err
	}
	return position1(body, toks[i]), end - i + 1, true, nil
}

// arguments reads a function-like macro's actual arguments. It returns
// the index of the closing parenthesis.
func (pp *preprocessor) arguments(m *macro, toks []token, open int) (map[string][]token, int, error) {
	args := map[string][]token{}
	i := open + 1
	depth := 0
	var current []token
	var collected [][]token

	for {
		if i >= len(toks) || toks[i].kind == tokEOF {
			return nil, 0, errorAt(toks[open].pos, "this macro invocation is never closed")
		}
		tok := toks[i]
		switch {
		case tok.is("(") || tok.is("["):
			depth++
		case tok.is(")") && depth == 0:
			collected = append(collected, current)
			goto done
		case tok.is(")") || tok.is("]"):
			depth--
		case tok.is(",") && depth == 0:
			// A comma inside the variadic tail is part of the argument,
			// not a separator, once the named parameters are all filled.
			if !m.variadic || len(collected) < len(m.params)-1 {
				collected = append(collected, current)
				current = nil
				i++
				continue
			}
		}
		current = append(current, tok)
		i++
	}
done:

	// `f()` on a one-parameter macro passes one empty argument, and on a
	// zero-parameter macro passes none.
	if len(m.params) == 0 {
		if len(collected) == 1 && len(collected[0]) == 0 {
			collected = nil
		}
	}
	if m.variadic && len(collected) == len(m.params)-1 {
		collected = append(collected, nil)
	}
	if len(collected) != len(m.params) {
		return nil, 0, errorAt(toks[open].pos, "%s takes %d argument(s), and this invocation passes %d",
			m.name, len(m.params), len(collected))
	}
	for n, name := range m.params {
		args[name] = collected[n]
	}
	return args, i, nil
}

// subst walks a macro body and produces its replacement: parameters
// become arguments, `#` stringizes and `##` pastes.
func (pp *preprocessor) subst(m *macro, args map[string][]token, hide map[string]bool, at token) ([]token, error) {
	expandedArgs := map[string][]token{}
	for name, raw := range args {
		expanded, err := pp.expandAll(raw)
		if err != nil {
			return nil, err
		}
		expandedArgs[name] = expanded
	}
	isParam := func(t token) bool {
		if t.kind != tokIdent {
			return false
		}
		_, ok := args[t.text]
		return ok
	}

	var out []token
	body := m.body
	for i := 0; i < len(body); i++ {
		tok := body[i]

		// GNU's comma swallow: `, ## __VA_ARGS__` drops the comma when
		// the variadic tail is empty. Enough real headers write it that
		// refusing it would refuse them.
		if m.variadic && tok.is(",") && i+2 < len(body) && body[i+1].is("##") &&
			body[i+2].kind == tokIdent && body[i+2].text == "__VA_ARGS__" {
			if len(args["__VA_ARGS__"]) == 0 {
				i += 2
				continue
			}
			out = append(out, tok)
			out = append(out, expandedArgs["__VA_ARGS__"]...)
			i += 2
			continue
		}

		if tok.is("#") && i+1 < len(body) && isParam(body[i+1]) {
			out = append(out, stringize(args[body[i+1].text], tok.pos))
			i++
			continue
		}

		if tok.is("##") {
			if len(out) == 0 {
				return nil, errorAt(tok.pos, "## has nothing on its left")
			}
			i++
			var right []token
			if isParam(body[i]) {
				right = args[body[i].text]
			} else {
				right = body[i : i+1]
			}
			if len(right) == 0 {
				continue
			}
			pasted, err := paste(out[len(out)-1], right[0])
			if err != nil {
				return nil, err
			}
			out[len(out)-1] = pasted
			out = append(out, right[1:]...)
			continue
		}

		if isParam(tok) {
			// An argument next to `##` is substituted unexpanded, which is
			// what makes `#define cat(a, b) a ## b` paste the names it was
			// handed rather than what they expand to.
			raw := i+1 < len(body) && body[i+1].is("##")
			source := expandedArgs[tok.text]
			if raw {
				source = args[tok.text]
			}
			if len(source) == 0 && raw {
				// An empty argument leaves nothing for the `##` to join,
				// so the paste is dropped and whatever follows it is
				// substituted on its own by the next turn of this loop.
				i++
				continue
			}
			for n, arg := range source {
				if n == 0 {
					arg = spaceLike(arg, tok)
				}
				out = append(out, arg)
			}
			continue
		}

		out = append(out, tok)
	}

	for i := range out {
		out[i].hide = union(out[i].hide, hide)
		out[i].pos = at.pos
	}
	return out, nil
}

// expandAll runs the expander over a token list with no directives in
// it: a macro argument, or a `#if` expression.
func (pp *preprocessor) expandAll(toks []token) ([]token, error) {
	var out []token
	i := 0
	for i < len(toks) {
		if toks[i].kind == tokIdent {
			replacement, consumed, ok, err := pp.expandAt(toks, i)
			if err != nil {
				return nil, err
			}
			if ok {
				toks = splice(toks, i, consumed, replacement)
				continue
			}
		}
		out = append(out, toks[i])
		i++
	}
	return out, nil
}

// position1 gives a replacement the invocation's spacing. The first
// token inherits where the macro name stood so that `a M b` keeps its
// spaces; bol is cleared on all of them, because a `#` a macro produced
// is not a directive.
func position1(toks []token, at token) []token {
	for i := range toks {
		toks[i].bol = false
		if i == 0 {
			toks[i].space = at.space
		}
	}
	return toks
}

func spaceLike(t, like token) token {
	t.space = like.space
	return t
}

func stringize(arg []token, pos position) token {
	text := spell(arg)
	return token{kind: tokStr, text: strconv.Quote(text), sval: text, pos: pos}
}

func paste(left, right token) (token, error) {
	text := left.text + right.text
	toks, err := scan(left.pos.file, text)
	if err != nil {
		return token{}, errorAt(left.pos, "pasting %s and %s does not make a token", left.text, right.text)
	}
	toks = dropEOF(toks)
	if len(toks) != 1 {
		return token{}, errorAt(left.pos, "pasting %s and %s makes %d tokens, not one", left.text, right.text, len(toks))
	}
	out := toks[0]
	out.pos, out.space, out.hide = left.pos, left.space, intersect(left.hide, right.hide)
	return out, nil
}

// spell reconstructs source text from tokens, which is what `#` needs.
func spell(toks []token) string {
	var out strings.Builder
	for i, t := range toks {
		if i > 0 && t.space {
			out.WriteByte(' ')
		}
		out.WriteString(t.text)
	}
	return out.String()
}

// spellTight is the same without the spaces, which is what an angled
// include name needs: `<sys/types.h>` scans as five tokens and is one
// path.
func spellTight(toks []token) string {
	var out strings.Builder
	for _, t := range toks {
		out.WriteString(t.text)
	}
	return out.String()
}

func withName(hide map[string]bool, name string) map[string]bool {
	out := map[string]bool{name: true}
	for k := range hide {
		out[k] = true
	}
	return out
}

func union(a, b map[string]bool) map[string]bool {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

func intersect(a, b map[string]bool) map[string]bool {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	out := map[string]bool{}
	for k := range a {
		if b[k] {
			out[k] = true
		}
	}
	return out
}
