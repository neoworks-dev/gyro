package loader

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/cc"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/parser"
)

// Importing a C header.
//
//	import safe { SDL_Init, SDL_Event } from "pkg:sdl3/SDL3/SDL.h"
//	import safe { sqrt } from "<math.h>"
//
// The specifier is the header, and the way to find it is part of the
// specifier. `pkg:<package>/<path>` names a pkg-config package and a path
// inside one of its include directories: the compiler flags come from
// `pkg-config --cflags <package>` and the library to link from
// `pkg-config --libs <package>`, so the program says what it depends on
// in the one place it uses it. `<path>` is a header on the system's
// include path, which is the C library's and the compiler's.
//
// What crosses is what the import names. A header like <SDL3/SDL.h>
// declares two thousand things, and a program that imports three of them
// gets those three and whatever their signatures mention, not a Gyro copy
// of the header.

// headerPrefix and angled are the two forms of header specifier.
const headerPrefix = "pkg:"

// isHeaderSpecifier reports whether an import specifier names a C header.
func isHeaderSpecifier(spec string) bool {
	return strings.HasPrefix(spec, headerPrefix) ||
		(strings.HasPrefix(spec, "<") && strings.HasSuffix(spec, ">"))
}

// headerImport is one header being imported by possibly several modules.
// The header is read once the whole graph is loaded, because what to read
// out of it is the union of what every importer names.
type headerImport struct {
	module *Module
	spec   string
	names  []string
	sites  map[string][]site
	// wildcard is where an import took the whole header, which no import
	// of one may.
	wildcard *site
	first    site
}

type site struct {
	from   *Module
	offset int
}

func (h *headerImport) add(name string, at site) {
	if _, known := h.sites[name]; !known {
		h.names = append(h.names, name)
	}
	h.sites[name] = append(h.sites[name], at)
}

// headerDependency files an import of a header, creating its module the
// first time any module names it.
func (s *loadState) headerDependency(from *Module, spec string, offset int, names []ast.ImportName, wildcard bool) {
	here := site{from: from, offset: offset}
	imp := s.headers[spec]
	if imp == nil {
		module := &Module{Path: spec, Deps: map[string]*Module{}}
		imp = &headerImport{module: module, spec: spec, sites: map[string][]site{}, first: here}
		s.headers[spec] = imp
		s.modules[spec] = module
		s.order = append(s.order, module)
		s.pending = append(s.pending, imp)
	}
	if wildcard && imp.wildcard == nil {
		imp.wildcard = &here
	}
	for _, name := range names {
		imp.add(name.Name, site{from: from, offset: name.Offset})
	}
	from.Deps[spec] = imp.module
}

// finishHeaders reads every header the graph imported.
func (s *loadState) finishHeaders() {
	for _, imp := range s.pending {
		s.readHeader(imp)
	}
	s.pending = nil
}

func (s *loadState) readHeader(imp *headerImport) {
	imp.module.File = diag.NewFile(imp.spec, "")
	imp.module.Tree = &ast.File{Name: imp.spec}
	s.bag.Track(imp.module.File)

	report := func(at site, code diag.Code, format string, args ...any) {
		s.bag.Add(diag.Diagnostic{
			Severity: diag.SeverityError,
			Code:     code,
			Pos:      at.from.File.Position(at.offset),
			Message:  fmt.Sprintf(format, args...),
		})
	}

	if imp.wildcard != nil {
		report(*imp.wildcard, diag.EHeaderName,
			"%s declares more than a program wants; name what is imported from a header: import { name } from %q", imp.spec, imp.spec)
		return
	}

	request, err := headerRequest(imp.spec, imp.names)
	if err != nil {
		report(imp.first, diag.EModuleUnreadable, "%v", err)
		return
	}
	header, err := cc.ReadHeader(request)
	if err != nil {
		report(imp.first, diag.EModuleUnreadable, "%v", err)
		return
	}
	if err := metals.Verify(header.Module); err != nil {
		report(imp.first, diag.EModuleUnreadable, "%s does not verify: %v", imp.spec, err)
		return
	}

	text, extra := headerText(header)
	problems := header.Problems
	for name, why := range extra {
		if _, known := problems[name]; !known {
			problems[name] = why
		}
	}
	names := make([]string, 0, len(problems))
	for name := range problems {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, at := range imp.sites[name] {
			report(at, diag.EHeaderName, "%s %s", name, problems[name])
		}
	}

	module := imp.module
	module.File = diag.NewFile(displayPath(imp.spec), text)
	s.bag.Track(module.File)
	module.Foreign = header.Module
	module.Tree = parser.ParseForeign(module.File, s.bag)
}

// headerRequest turns a specifier into the include text, the directories
// to search and the library its functions are linked from.
func headerRequest(spec string, names []string) (cc.HeaderRequest, error) {
	system, err := cc.SystemInclude()
	if err != nil {
		return cc.HeaderRequest{}, err
	}
	opt := &cc.Options{}

	var include string
	if strings.HasPrefix(spec, headerPrefix) {
		pkg, path, ok := strings.Cut(strings.TrimPrefix(spec, headerPrefix), "/")
		if !ok || pkg == "" || path == "" {
			return cc.HeaderRequest{}, fmt.Errorf("%q does not name a header inside a package: write pkg:<package>/<path>", spec)
		}
		dirs, defines, err := pkgConfigFlags(pkg)
		if err != nil {
			return cc.HeaderRequest{}, err
		}
		opt.Include = append(opt.Include, dirs...)
		opt.Define = append(opt.Define, defines...)
		opt.Package = pkg
		include = "<" + path + ">"
	} else {
		include = spec
	}
	opt.Include = append(opt.Include, system...)

	return cc.HeaderRequest{
		Module:  "cc:" + spec,
		Include: include,
		Names:   names,
		Options: opt,
	}, nil
}

// pkgConfigFlags asks pkg-config how to compile against a package. Only
// the flags a preprocessor reads matter here — the include directories
// and the definitions; the libraries are asked for when the program is
// linked, from the package name the native import carries.
func pkgConfigFlags(pkg string) (dirs, defines []string, err error) {
	if _, err := exec.LookPath("pkg-config"); err != nil {
		return nil, nil, fmt.Errorf("importing from package %s needs pkg-config, and it is not on the path", pkg)
	}
	out, err := exec.Command("pkg-config", "--cflags", pkg).CombinedOutput()
	if err != nil {
		return nil, nil, fmt.Errorf("pkg-config does not know package %s: %s", pkg, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	for i := 0; i < len(fields); i++ {
		flag := fields[i]
		switch {
		case flag == "-I" || flag == "-D":
			if i+1 < len(fields) {
				i++
				if flag == "-I" {
					dirs = append(dirs, fields[i])
				} else {
					defines = append(defines, fields[i])
				}
			}
		case strings.HasPrefix(flag, "-I"):
			dirs = append(dirs, strings.TrimPrefix(flag, "-I"))
		case strings.HasPrefix(flag, "-D"):
			defines = append(defines, strings.TrimPrefix(flag, "-D"))
		}
	}
	return dirs, defines, nil
}

// headerText renders what a header import found as Gyro declarations.
// The second result is the names that were found and have no spelling.
func headerText(header *cc.Header) (string, map[string]string) {
	module := header.Module
	h := &headerGen{
		types:     module.TypeEnv(),
		spellable: map[string]bool{},
		wanted:    map[string]bool{},
		order:     module.Types,
	}
	h.resolve()

	unspelled := map[string]string{}
	var decls strings.Builder
	for _, fn := range header.Funcs {
		text, err := h.function(fn.Name, fn.Sig.Params, fn.Sig.Result, false, false)
		if err != nil {
			unspelled[fn.Name] = "has no Gyro spelling: " + err.Error()
			continue
		}
		decls.WriteString(text)
	}
	for _, tag := range header.Types {
		if !h.spellable[tag] {
			unspelled[tag] = "has no Gyro spelling"
			continue
		}
		h.want(tag)
	}

	var out strings.Builder
	fmt.Fprintf(&out, "// Generated from %s.\n", module.Name)
	for _, decl := range h.order {
		if h.wanted[decl.Name] {
			out.WriteString(h.declare(decl))
		}
	}
	out.WriteString(decls.String())
	for _, c := range header.Consts {
		if c.Unsigned {
			fmt.Fprintf(&out, "export const %s: %s = %d\n", c.Name, c.Type, uint64(c.Value))
		} else if c.Value < 0 {
			fmt.Fprintf(&out, "export const %s: %s = 0 - %d\n", c.Name, c.Type, -c.Value)
		} else {
			fmt.Fprintf(&out, "export const %s: %s = %d\n", c.Name, c.Type, c.Value)
		}
	}
	return out.String(), unspelled
}
