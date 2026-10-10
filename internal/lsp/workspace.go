package lsp

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
)

// workspaceFile is the analysis of a file that is not open, valid while
// the file keeps the modification time it was read at.
type workspaceFile struct {
	modified time.Time
	analysis *analysis
}

// workspaceRoot is the directory a search beyond the open documents
// covers: the nearest one above path that holds .git or gyro.toml, which
// are the markers an editor finds the server's root by. Without either
// it is the file's own directory.
func workspaceRoot(path string) string {
	start := filepath.Dir(path)
	for dir := start; ; {
		for _, marker := range []string{".git", "gyro.toml"} {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// unopenedFiles lists the .gyro files under root that no document has
// open and whose text mentions name, analysed or taken from the cache.
// Hidden directories, worktrees among them, and node_modules are skipped.
func (s *Server) unopenedFiles(root, name string) map[string]*analysis {
	open := map[string]bool{}
	for _, doc := range s.documents {
		open[doc.path] = true
	}
	if s.workspace == nil {
		s.workspace = map[string]*workspaceFile{}
	}

	found := map[string]*analysis{}
	filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".gyro") || open[path] {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if cached, ok := s.workspace[path]; ok && cached.modified.Equal(info.ModTime()) {
			if cached.analysis != nil && containsWord(cached.analysis.text, name) {
				found[path] = cached.analysis
			}
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(source)
		if !containsWord(text, name) {
			// The text is the cheap filter, so a file that does not
			// mention the name is not worth analysing or remembering.
			return nil
		}
		var bag diag.Bag
		analyzed, err := s.check(path, text, &bag)
		if err != nil {
			analyzed = nil
		}
		s.workspace[path] = &workspaceFile{modified: info.ModTime(), analysis: analyzed}
		if analyzed != nil {
			found[path] = analyzed
		}
		return nil
	})
	return found
}

// containsWord reports whether name occurs in text as a whole identifier.
func containsWord(text, name string) bool {
	for at := 0; at < len(text); {
		found := strings.Index(text[at:], name)
		if found < 0 {
			return false
		}
		start := at + found
		end := start + len(name)
		if (start == 0 || !isIdentifierPart(runeBefore(text, start))) &&
			(end >= len(text) || !isIdentifierPart(runeAt(text, end))) {
			return true
		}
		at = end
	}
	return false
}

// visibleElsewhere reports whether other modules can name the declaration
// a reference resolved to: a module-level declaration, or a member of
// one. A local, a parameter or a closure's binding is seen only by its own
// file.
func visibleElsewhere(found site, target ast.Node, name string) bool {
	switch decl := target.(type) {
	case *ast.FuncDecl:
		// A parameter resolves to its function, under its own name.
		return name == decl.Name
	case *ast.StructDecl, *ast.EnumDecl, *ast.TraitDecl:
		return true
	case *ast.VarDecl:
		for _, top := range found.module.Tree.Decls {
			if top == decl {
				return true
			}
		}
	}
	return false
}
