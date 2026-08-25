package cc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding the other translation units.
//
// C files never include one another. A header declares what a function
// is; nothing anywhere says which `.c` defines it. Compilation is per
// file, and a linker joins the object files afterwards by matching
// undefined symbol names against defined ones across a flat global
// namespace — a list of files the language has no way to write down,
// which is why every C project has a build system whose main job is
// holding that list.
//
// Gyro imports a file rather than a list, so the list is recovered here:
// the entry file is compiled, whatever it needs and does not define is
// looked for in the `.c` files beside it, and each file that supplies one
// is pulled in and asked the same question. What comes out is the set a
// linker would have been handed, discovered instead of declared.
//
// The search is that one directory and no deeper. It is a rule that fits
// in a diagnostic, and a library laid out in subdirectories is imported
// through a file in each of them rather than by widening a net that would
// start matching names it was never pointed at.
//
// All of it becomes one METALS module, because a linkage unit is what
// METALS' module is. Two files that call each other are ordinary C and
// have no dependency order, so as separate modules they would be an
// import cycle the loader refuses; merged, they are two internal calls.
// The merge is also what makes the call *checked* — a cross-file call is
// verified against the definition it reaches, which a C linker matching
// bare names structurally cannot do.

// list returns the .c files in a directory. A caller with its own idea
// of where sources come from supplies Options.List, as it supplies Read.
func (o *Options) list(dir string) ([]string, error) {
	if o != nil && o.List != nil {
		return o.List(dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".c" {
			continue
		}
		out = append(out, filepath.Join(dir, entry.Name()))
	}
	return out, nil
}

// translation is one parsed .c file and the path it came from.
type translation struct {
	path string
	unit *unit
}

// gather compiles the entry file and then every sibling that supplies a
// name the growing set needs and does not have. The entry file is always
// first, so the module reads in the order a person would look for it.
func gather(path, source string, opt *Options) ([]*translation, error) {
	entry, err := parseUnit(path, source, opt)
	if err != nil {
		return nil, err
	}
	out := []*translation{entry}

	siblings, err := candidates(path, opt)
	if err != nil {
		return nil, err
	}

	// defined is what the set already supplies, wanted is what it still
	// asks for. Both grow as files arrive, which is why this loops until
	// a pass adds nothing rather than walking the list once.
	defined := map[string]bool{}
	for _, name := range provided(entry) {
		defined[name] = true
	}

	pulled := map[string]bool{path: true}
	for {
		var missing []string
		for _, t := range out {
			for _, name := range needed(t) {
				if !defined[name] {
					missing = append(missing, name)
				}
			}
		}
		sort.Strings(missing)

		grew := false
		for _, name := range missing {
			found := siblings[name]
			if found == "" || pulled[found] {
				continue
			}
			text, err := opt.read(found)
			if err != nil {
				return nil, fmt.Errorf("%s defines %s, and reading it failed: %v",
					filepath.Base(found), name, err)
			}
			next, err := parseUnit(found, text, opt)
			if err != nil {
				return nil, err
			}
			pulled[found] = true
			out = append(out, next)
			for _, supplied := range provided(next) {
				defined[supplied] = true
			}
			grew = true
		}
		if !grew {
			return out, nil
		}
	}
}

// parseUnit preprocesses and parses one file.
func parseUnit(path, source string, opt *Options) (*translation, error) {
	toks, err := preprocess(path, source, opt)
	if err != nil {
		return nil, err
	}
	parsed, err := parse(path, toks)
	if err != nil {
		return nil, err
	}
	return &translation{path: path, unit: parsed}, nil
}

// candidates maps every externally visible name a sibling file defines to
// the file that defines it. A file is parsed here only to be asked what
// it provides; the ones nothing needs are never lowered.
//
// Two siblings defining one name is C's duplicate symbol, and it is an
// error here even when nothing would have pulled either in — a resolver
// that silently picked the first one would make which file you get depend
// on directory order.
func candidates(entry string, opt *Options) (map[string]string, error) {
	paths, err := opt.list(filepath.Dir(entry))
	if err != nil {
		// A directory that cannot be listed is a directory with no
		// siblings in it. The entry file was read already, so this is
		// only a failure if something turns out to be missing, and that
		// is reported against the name rather than against the listing.
		return map[string]string{}, nil
	}
	sort.Strings(paths)

	out := map[string]string{}
	for _, path := range paths {
		if sameFile(path, entry) {
			continue
		}
		source, err := opt.read(path)
		if err != nil {
			continue
		}
		parsed, err := parseUnit(path, source, opt)
		if err != nil {
			// A sibling that does not compile is not an error until
			// something needs it. A directory may hold a file meant for
			// another target, and refusing to build because of it would
			// make every unrelated file in a folder a dependency.
			continue
		}
		for _, name := range provided(parsed) {
			if other, taken := out[name]; taken {
				return nil, fmt.Errorf("%s and %s both define %s, and a program has one of each name",
					filepath.Base(other), filepath.Base(path), name)
			}
			out[name] = path
		}
	}
	return out, nil
}

func sameFile(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// provided is what a translation unit offers the rest of the program:
// its non-static definitions. A `static` one has internal linkage, which
// is C's way of saying no other file may see it.
func provided(t *translation) []string {
	var out []string
	for _, fn := range t.unit.funcs {
		if fn.body != nil && !fn.static {
			out = append(out, fn.name)
		}
	}
	for _, g := range t.unit.globals {
		if !g.static {
			out = append(out, g.name)
		}
	}
	return out
}

// needed is what a translation unit uses and does not define. A header
// declares far more than any one file calls, so this is the names
// actually referenced rather than every prototype in scope — otherwise
// one shared header would pull in the whole directory.
func needed(t *translation) []string {
	defines := map[string]bool{}
	for _, fn := range t.unit.funcs {
		if fn.body != nil {
			defines[fn.name] = true
		}
	}
	for _, g := range t.unit.globals {
		defines[g.name] = true
	}

	seen := map[string]bool{}
	var out []string
	want := func(name string) {
		if name == "" || defines[name] || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	for _, fn := range t.unit.funcs {
		if fn.body != nil {
			walkStmt(fn.body, want)
		}
	}
	for _, g := range t.unit.globals {
		walkInit(g.init, want)
	}
	return out
}

// ---------------------------------------------------------------------------
// Walking a body for the names it mentions
// ---------------------------------------------------------------------------

func walkStmt(s stmt, want func(string)) {
	switch s := s.(type) {
	case *blockStmt:
		for _, inner := range s.stmts {
			walkStmt(inner, want)
		}
	case *declStmt:
		for _, v := range s.vars {
			walkInit(v.init, want)
		}
	case *exprStmt:
		walkExpr(s.x, want)
	case *ifStmt:
		walkExpr(s.cond, want)
		walkStmt(s.then, want)
		if s.els != nil {
			walkStmt(s.els, want)
		}
	case *whileStmt:
		walkExpr(s.cond, want)
		walkStmt(s.body, want)
	case *doStmt:
		walkStmt(s.body, want)
		walkExpr(s.cond, want)
	case *forStmt:
		if s.init != nil {
			walkStmt(s.init, want)
		}
		walkExpr(s.cond, want)
		walkExpr(s.post, want)
		walkStmt(s.body, want)
	case *switchStmt:
		walkExpr(s.x, want)
		for _, inner := range s.body {
			walkStmt(inner, want)
		}
	case *returnStmt:
		walkExpr(s.x, want)
	}
}

func walkInit(i *initializer, want func(string)) {
	if i == nil {
		return
	}
	walkExpr(i.x, want)
	for _, inner := range i.list {
		walkInit(inner, want)
	}
}

// walkExpr reports every name an expression mentions. A local variable's
// name reaches this too — nothing here knows what is in scope — which
// over-reports by exactly the names a sibling also happens to define at
// file scope, and those would be a collision to report either way.
func walkExpr(e expr, want func(string)) {
	switch e := e.(type) {
	case nil:
		return
	case *identExpr:
		want(e.name)
	case *callExpr:
		walkExpr(e.callee, want)
		for _, arg := range e.args {
			walkExpr(arg, want)
		}
	case *unaryExpr:
		walkExpr(e.x, want)
	case *incExpr:
		walkExpr(e.x, want)
	case *binaryExpr:
		walkExpr(e.a, want)
		walkExpr(e.b, want)
	case *assignExpr:
		walkExpr(e.target, want)
		walkExpr(e.value, want)
	case *condExpr:
		walkExpr(e.cond, want)
		walkExpr(e.then, want)
		walkExpr(e.els, want)
	case *memberExpr:
		walkExpr(e.x, want)
	case *castExpr:
		walkExpr(e.x, want)
	case *sizeofExpr:
		walkExpr(e.x, want)
	}
}

// unitName is the METALS symbol prefix a file's internal names take.
// `$` is not in C's identifier space, so a qualified name cannot collide
// with one a translation unit wrote — the same reason a static local is
// spelled `function$name`.
func unitName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
