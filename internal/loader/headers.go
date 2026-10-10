package loader

import (
	"fmt"
	"os/exec"
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
// All the headers of one library are one translation unit, as they are to
// a C compiler: `<stdlib.h>` and `<arpa/inet.h>` are read together, so a
// struct they both mention is one struct, and so are the headers of one
// package. That is why the module an import gets is keyed by the library
// and not by the specifier — two specifiers of one library are two doors
// into one module. The cost is that a name is found if any header of the
// library declares it, whichever one the import said.
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
	// library is the pkg-config package, or "c" for the system's headers.
	library string
	// includes are the headers imported, in the order they were first named.
	includes []string
	names    []string
	sites    map[string][]site
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
func (s *loadState) headerDependency(from *Module, spec string, offset int, names []ast.ImportName) {
	here := site{from: from, offset: offset}
	library, include, err := splitHeader(spec)
	if err != nil {
		s.bag.Add(diag.Diagnostic{
			Severity: diag.SeverityError,
			Code:     diag.EModuleUnreadable,
			Pos:      from.File.Position(offset),
			Message:  err.Error(),
		})
		return
	}
	imp := s.headers[library]
	if imp == nil {
		module := &Module{Path: library, Deps: map[string]*Module{}}
		imp = &headerImport{module: module, library: library, sites: map[string][]site{}, first: here}
		s.headers[library] = imp
		s.modules[library] = module
		s.order = append(s.order, module)
		s.pending = append(s.pending, imp)
	}
	known := false
	for _, have := range imp.includes {
		known = known || have == include
	}
	if !known {
		imp.includes = append(imp.includes, include)
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
	imp.module.File = diag.NewFile(imp.library, "")
	imp.module.Tree = &ast.File{Name: imp.library}
	imp.module.Header = true
	s.bag.Track(imp.module.File)

	report := func(at site, code diag.Code, format string, args ...any) {
		s.bag.Add(diag.Diagnostic{
			Severity: diag.SeverityError,
			Code:     code,
			Pos:      at.from.File.Position(at.offset),
			Message:  fmt.Sprintf(format, args...),
		})
	}

	request, err := headerRequest(imp.library, imp.includes, imp.names)
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
		report(imp.first, diag.EModuleUnreadable, "%s does not verify: %v", imp.library, err)
		return
	}

	text, extra := headerText(header)
	problems := header.Problems
	for name, why := range extra {
		if _, known := problems[name]; !known {
			problems[name] = why
		}
	}
	module := imp.module
	module.File = diag.NewFile(imp.library, text)
	s.bag.Track(module.File)
	module.Foreign = header.Module
	module.Header = true
	module.Problems = problems
	module.Tree = parser.ParseForeign(module.File, s.bag)
}

// splitHeader says which library a specifier belongs to and what to
// `#include` to read it. The library of a package is the module the
// package's functions are linked from, and "c" is the system's.
func splitHeader(spec string) (library, include string, err error) {
	if !strings.HasPrefix(spec, headerPrefix) {
		return "c", spec, nil
	}
	pkg, path, ok := strings.Cut(strings.TrimPrefix(spec, headerPrefix), "/")
	if !ok || pkg == "" || path == "" {
		return "", "", fmt.Errorf("%q does not name a header inside a package: write pkg:<package>/<path>", spec)
	}
	return headerPrefix + pkg, "<" + path + ">", nil
}

// headerRequest builds the read of one library's headers: the include
// directories and definitions its package asks for, ahead of the
// system's, and the library its functions are linked from.
func headerRequest(library string, includes, names []string) (cc.HeaderRequest, error) {
	system, err := cc.SystemInclude()
	if err != nil {
		return cc.HeaderRequest{}, err
	}
	opt := &cc.Options{}

	if pkg, isPackage := strings.CutPrefix(library, headerPrefix); isPackage {
		dirs, defines, err := pkgConfigFlags(pkg)
		if err != nil {
			return cc.HeaderRequest{}, err
		}
		opt.Include = append(opt.Include, dirs...)
		opt.Define = append(opt.Define, defines...)
		opt.Package = pkg
	}
	opt.Include = append(opt.Include, system...)

	var source strings.Builder
	for _, include := range includes {
		fmt.Fprintf(&source, "#include %s\n", include)
	}
	return cc.HeaderRequest{
		Module:  "cc:" + library,
		Source:  source.String(),
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
