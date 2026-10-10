package lsp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/types"
)

// indexFor returns the document's position index, building it on demand.
func indexFor(a *analysis) *index {
	if a == nil || a.program == nil {
		return nil
	}
	if a.idx == nil {
		a.idx = buildIndex(a.program)
	}
	return a.idx
}

// ---------------------------------------------------------------------------
// Hover
// ---------------------------------------------------------------------------

func (s *Server) handleHover(req *request) error {
	var params textDocumentPositionParams
	if ok, err := decode(s, req, &params); !ok {
		return err
	}

	doc := s.documentFor(params.TextDocument.URI)
	if doc == nil || doc.isMetals() || doc.analysis == nil {
		return s.conn.respond(req.ID, nil)
	}

	a := doc.analysis
	idx := indexFor(a)
	offset := offsetFor(a.text, params.Position)
	hit := idx.anchorAt(offset)
	if hit == nil {
		return s.conn.respond(req.ID, nil)
	}

	lines := s.hoverLines(a, hit)
	if len(lines) == 0 {
		return s.conn.respond(req.ID, nil)
	}

	span := rangeFor(a.text, hit.start, hit.end)
	return s.conn.respond(req.ID, hoverResult{
		Contents: markupContent{
			Kind:  "markdown",
			Value: "```gyro\n" + strings.Join(lines, "\n") + "\n```",
		},
		Range: &span,
	})
}

func (s *Server) hoverLines(a *analysis, hit *anchor) []string {
	var lines []string

	if typ := anchorType(a, hit); typ != nil {
		lines = append(lines, fmt.Sprintf("%s: %s", hit.name, typ.String()))
	}

	if described := describeDecl(a, hit); described != "" {
		if len(lines) == 0 || described != lines[0] {
			lines = append(lines, described)
		}
	}
	return lines
}

// anchorType finds the checked type of the name under the cursor.
func anchorType(a *analysis, hit *anchor) types.Type {
	if a.info == nil {
		return nil
	}
	if hit.expr != nil {
		if typ, ok := a.info.Types[hit.expr]; ok {
			return types.Default(typ)
		}
	}
	if hit.localOwner != nil {
		if byName, ok := a.info.Locals[hit.localOwner]; ok {
			if typ, ok := byName[hit.name]; ok {
				return types.Default(typ)
			}
		}
	}
	return nil
}

// declTarget resolves the declaration an anchor points at: the anchor's
// own declaration, what sema resolved the expression to, or for the
// method name of `value.method(...)` the method the call resolved to.
func declTarget(a *analysis, hit *anchor) ast.Node {
	if hit.expr != nil && a.info != nil {
		if decl, ok := a.info.Refs[hit.expr]; ok {
			return decl
		}
		if selector, ok := hit.expr.(*ast.SelectorExpr); ok {
			if call := indexFor(a).methodCalls[selector]; call != nil {
				if resolved := a.info.Calls[call]; resolved != nil && resolved.Method != nil && resolved.Method.Decl != nil {
					return resolved.Method.Decl
				}
			}
		}
	}
	return hit.decl
}

// describeDecl renders the declaration a hover should show: a function's
// signature sliced from its source, or a one-line summary.
func describeDecl(a *analysis, hit *anchor) string {
	target := declTarget(a, hit)
	if target == nil {
		return ""
	}
	idx := indexFor(a)

	switch decl := target.(type) {
	case *ast.FuncDecl:
		if declSite, ok := idx.declSites[decl]; ok {
			return signatureText(declSite.module, decl)
		}
	case *ast.StructDecl:
		return "struct " + decl.Name
	case *ast.EnumDecl:
		return "enum " + decl.Name
	case *ast.TraitDecl:
		return "trait " + decl.Name
	case *ast.Import:
		return fmt.Sprintf("import from %q", decl.Path)
	}
	return ""
}

// signatureText slices a function's declared signature out of its source,
// which reproduces exactly what the author wrote — effect rows included.
func signatureText(module *loader.Module, decl *ast.FuncDecl) string {
	src := module.File.Src
	end := len(src)
	if decl.Body != nil {
		end = decl.Body.Offset
	} else if lineEnd := strings.IndexByte(src[decl.Offset:], '\n'); lineEnd >= 0 {
		end = decl.Offset + lineEnd
	}
	text := strings.TrimSpace(src[decl.Offset:min(end, len(src))])
	return strings.Join(strings.Fields(text), " ")
}

// ---------------------------------------------------------------------------
// Definition
// ---------------------------------------------------------------------------

func (s *Server) handleDefinition(req *request) error {
	var params textDocumentPositionParams
	if ok, err := decode(s, req, &params); !ok {
		return err
	}

	doc := s.documentFor(params.TextDocument.URI)
	if doc == nil || doc.isMetals() || doc.analysis == nil {
		return s.conn.respond(req.ID, nil)
	}

	a := doc.analysis
	idx := indexFor(a)
	offset := offsetFor(a.text, params.Position)
	hit := idx.anchorAt(offset)
	if hit == nil {
		return s.conn.respond(req.ID, nil)
	}

	target := declTarget(a, hit)
	if target == nil {
		return s.conn.respond(req.ID, nil)
	}
	declSite, ok := idx.declSites[target]
	if !ok {
		return s.conn.respond(req.ID, nil)
	}
	declSite = refineSite(declSite, target, hit.name)

	if strings.HasPrefix(declSite.module.Path, "@") {
		// Bundled stdlib modules have no file to jump into.
		return s.conn.respond(req.ID, nil)
	}
	return s.conn.respond(req.ID, Location{
		URI:   uriFromPath(declSite.module.Path),
		Range: rangeFor(declSite.module.File.Src, declSite.start, declSite.end),
	})
}

// refineSite narrows a declaration site to the member that actually binds
// the name: sema records a parameter's declaration as its whole function,
// a destructured binding's as its whole var declaration, and a field or
// a variant as the struct or enum that declares it.
func refineSite(declSite site, target ast.Node, name string) site {
	switch decl := target.(type) {
	case *ast.FuncDecl:
		for _, param := range decl.Params {
			if param.Name == name {
				declSite.start = param.Offset
				declSite.end = param.Offset + len(name)
			}
		}
	case *ast.ClosureExpr:
		for _, param := range decl.Params {
			if param.Name == name {
				declSite.start = param.Offset
				declSite.end = param.Offset + len(name)
			}
		}
	case *ast.VarDecl:
		for _, bound := range decl.Names {
			if bound.Name == name {
				declSite.start = bound.Offset
				declSite.end = bound.Offset + len(name)
			}
		}
	case *ast.StructDecl:
		for _, field := range decl.Fields {
			if field.Name == name && name != decl.Name {
				declSite.start = field.Offset + fieldNameShift(declSite.module.File.Src, field)
				declSite.end = declSite.start + len(name)
			}
		}
	case *ast.EnumDecl:
		for _, variant := range decl.Variants {
			if variant.Name == name && name != decl.Name {
				declSite.start = variant.Offset
				declSite.end = variant.Offset + len(name)
			}
		}
	}
	return declSite
}

// ---------------------------------------------------------------------------
// References
// ---------------------------------------------------------------------------

// handleReferences finds every name that resolves to the same declaration
// as the one under the cursor. Each open document is analysed as its own
// program, so the AST nodes of one analysis mean nothing to another; what
// they share is the place a declaration is written, and that is what two
// names are compared by. Files that are not open are not searched.
func (s *Server) handleReferences(req *request) error {
	var params referenceParams
	if ok, err := decode(s, req, &params); !ok {
		return err
	}

	doc := s.documentFor(params.TextDocument.URI)
	if doc == nil || doc.isMetals() || doc.analysis == nil {
		return s.conn.respond(req.ID, nil)
	}
	hit := indexFor(doc.analysis).anchorAt(offsetFor(doc.analysis.text, params.Position))
	if hit == nil {
		return s.conn.respond(req.ID, nil)
	}
	want, ok := declarationSite(doc.analysis, hit)
	if !ok {
		return s.conn.respond(req.ID, nil)
	}

	uris := make([]string, 0, len(s.documents))
	for uri := range s.documents {
		uris = append(uris, uri)
	}
	sort.Strings(uris)

	locations := []Location{}
	for _, uri := range uris {
		open := s.documents[uri]
		if open.isMetals() || open.analysis == nil {
			continue
		}
		for i := range indexFor(open.analysis).anchors {
			anchor := &indexFor(open.analysis).anchors[i]
			if anchor.expr == nil && !params.Context.IncludeDeclaration {
				continue
			}
			if found, ok := declarationSite(open.analysis, anchor); ok && sameSite(found, want) {
				locations = append(locations, Location{
					URI:   uri,
					Range: rangeFor(open.analysis.text, anchor.start, anchor.end),
				})
			}
		}
	}
	return s.conn.respond(req.ID, locations)
}

// declarationSite is where the declaration a name resolves to is written.
func declarationSite(a *analysis, hit *anchor) (site, bool) {
	target := declTarget(a, hit)
	if target == nil {
		return site{}, false
	}
	declSite, ok := indexFor(a).declSites[target]
	if !ok {
		return site{}, false
	}
	return refineSite(declSite, target, hit.name), true
}

func sameSite(a, b site) bool {
	return a.module.Path == b.module.Path && a.start == b.start
}

// ---------------------------------------------------------------------------
// Document symbols
// ---------------------------------------------------------------------------

func (s *Server) handleDocumentSymbol(req *request) error {
	var params documentSymbolParams
	if ok, err := decode(s, req, &params); !ok {
		return err
	}

	doc := s.documentFor(params.TextDocument.URI)
	if doc == nil || doc.isMetals() || doc.analysis == nil {
		return s.conn.respond(req.ID, nil)
	}

	a := doc.analysis
	idx := indexFor(a)
	var symbols []documentSymbol
	for _, top := range idx.topLevel {
		if symbol, ok := symbolFor(a, top); ok {
			symbols = append(symbols, symbol)
		}
	}
	return s.conn.respond(req.ID, symbols)
}

func symbolFor(a *analysis, top topDecl) (documentSymbol, bool) {
	src := a.text
	full := rangeFor(src, top.start, top.end)

	switch decl := top.node.(type) {
	case *ast.FuncDecl:
		name := decl.Name
		if decl.Owner != "" {
			name = decl.Owner + "::" + decl.Name
		}
		kind := symbolKindFunction
		if decl.Owner != "" {
			kind = symbolKindMethod
		}
		start, end := findName(src, decl.Offset, decl.Name)
		return documentSymbol{
			Name: name, Kind: kind, Detail: "fn",
			Range: full, SelectionRange: rangeFor(src, start, end),
		}, true

	case *ast.StructDecl:
		start, end := findName(src, decl.Offset, decl.Name)
		symbol := documentSymbol{
			Name: decl.Name, Kind: symbolKindStruct, Detail: "struct",
			Range: full, SelectionRange: rangeFor(src, start, end),
		}
		for _, field := range decl.Fields {
			shift := fieldNameShift(src, field)
			symbol.Children = append(symbol.Children, documentSymbol{
				Name: field.Name, Kind: symbolKindField,
				Range:          rangeFor(src, field.Offset, field.Offset+shift+len(field.Name)),
				SelectionRange: rangeFor(src, field.Offset+shift, field.Offset+shift+len(field.Name)),
			})
		}
		return symbol, true

	case *ast.EnumDecl:
		start, end := findName(src, decl.Offset, decl.Name)
		symbol := documentSymbol{
			Name: decl.Name, Kind: symbolKindEnum, Detail: "enum",
			Range: full, SelectionRange: rangeFor(src, start, end),
		}
		for _, variant := range decl.Variants {
			symbol.Children = append(symbol.Children, documentSymbol{
				Name: variant.Name, Kind: symbolKindEnumCase,
				Range:          rangeFor(src, variant.Offset, variant.Offset+len(variant.Name)),
				SelectionRange: rangeFor(src, variant.Offset, variant.Offset+len(variant.Name)),
			})
		}
		return symbol, true

	case *ast.VarDecl:
		if len(decl.Names) == 0 {
			return documentSymbol{}, false
		}
		kind := symbolKindVariable
		detail := "var"
		if decl.Const {
			kind = symbolKindConstant
			detail = "const"
		}
		bound := decl.Names[0]
		return documentSymbol{
			Name: bound.Name, Kind: kind, Detail: detail,
			Range:          full,
			SelectionRange: rangeFor(src, bound.Offset, bound.Offset+len(bound.Name)),
		}, true
	}
	return documentSymbol{}, false
}
