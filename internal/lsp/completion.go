package lsp

import (
	"sort"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/types"
)

// Completion tolerates broken buffers: the identifier chain under the
// cursor is read from the current text, and its types are resolved against
// the most recent analysis whose sema pass ran. A buffer mid-edit — the
// whole reason completion is being asked for — rarely parses.

var keywordCompletions = []string{
	"var", "const", "fn", "struct", "enum", "trait", "impl", "return", "if", "elif", "else",
	"while", "for", "in", "match", "break", "continue", "defer", "async",
	"await", "chan", "select", "import", "from", "as", "export", "self",
	"true", "false", "null",
}

func (s *Server) handleCompletion(req *request) error {
	var params completionParams
	if ok, err := decode(s, req, &params); !ok {
		return err
	}

	doc := s.documentFor(params.TextDocument.URI)
	if doc == nil || doc.isMetals() {
		return s.conn.respond(req.ID, nil)
	}

	offset := offsetFor(doc.text, params.Position)
	prefixStart := offset
	for prefixStart > 0 && isIdentifierPart(runeBefore(doc.text, prefixStart)) {
		prefixStart--
	}

	var items []completionItem
	switch {
	case strings.HasSuffix(doc.text[:prefixStart], "."):
		items = s.completeMembers(doc, doc.text[:prefixStart-1], offset)
	case strings.HasSuffix(doc.text[:prefixStart], "::"):
		items = s.completeNamespace(doc, doc.text[:prefixStart-2])
	default:
		items = s.completeScope(doc, offset)
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	return s.conn.respond(req.ID, dedupeItems(items))
}

func dedupeItems(items []completionItem) []completionItem {
	seen := map[string]bool{}
	kept := items[:0]
	for _, item := range items {
		if seen[item.Label] {
			continue
		}
		seen[item.Label] = true
		kept = append(kept, item)
	}
	return kept
}

// ---------------------------------------------------------------------------
// Plain scope completion
// ---------------------------------------------------------------------------

func (s *Server) completeScope(doc *document, offset int) []completionItem {
	var items []completionItem
	for _, keyword := range keywordCompletions {
		items = append(items, completionItem{Label: keyword, Kind: completionKindKeyword})
	}

	a := doc.analysis
	if a == nil {
		return items
	}
	idx := indexFor(a)
	for _, bound := range idx.bindings {
		if offset < bound.regionStart || offset >= bound.regionEnd || offset < bound.visibleFrom {
			continue
		}
		item := completionItem{Label: bound.name, Kind: bound.kind}
		if a.info != nil {
			if byName, ok := a.info.Locals[bound.owner]; ok {
				if typ, ok := byName[bound.name]; ok {
					item.Detail = types.Default(typ).String()
				}
			}
		}
		if fn, ok := bound.owner.(*ast.FuncDecl); ok && bound.kind == completionKindFunction {
			if declSite, siteKnown := idx.declSites[fn]; siteKnown {
				item.Detail = signatureText(declSite.module, fn)
			}
		}
		items = append(items, item)
	}
	return items
}

// ---------------------------------------------------------------------------
// Member completion after `.`
// ---------------------------------------------------------------------------

// completeMembers offers the fields and methods of the expression before
// the dot. The chain is resolved by name against the last typed analysis.
func (s *Server) completeMembers(doc *document, before string, offset int) []completionItem {
	a := doc.typed
	if a == nil || a.info == nil {
		return nil
	}

	chain := trailingChain(before)
	if len(chain) == 0 {
		return nil
	}

	typ := s.chainType(a, chain, offset)
	if typ == nil {
		return nil
	}
	return memberItems(typ)
}

// trailingChain extracts the `a.b.c` identifier chain ending the text.
func trailingChain(text string) []string {
	end := len(text)
	var chain []string
	for {
		start := end
		for start > 0 && isIdentifierPart(runeBefore(text, start)) {
			start--
		}
		if start == end {
			return nil
		}
		chain = append([]string{text[start:end]}, chain...)
		if start > 0 && text[start-1] == '.' {
			end = start - 1
			continue
		}
		return chain
	}
}

// chainType types an identifier chain: the head by name lookup near the
// cursor, each following segment as a struct field.
func (s *Server) chainType(a *analysis, chain []string, offset int) types.Type {
	typ := s.headType(a, chain[0], offset)
	for _, segment := range chain[1:] {
		if typ == nil {
			return nil
		}
		typ = fieldType(typ, segment)
	}
	return typ
}

// headType finds the type of a bare name: the nearest local binding with
// that name, falling back to nothing. Owners are ranked by how close
// before the cursor they start, which approximates lexical scope well
// enough for completion.
func (s *Server) headType(a *analysis, name string, offset int) types.Type {
	bestOffset := -1
	var best types.Type
	for owner, byName := range a.info.Locals {
		typ, ok := byName[name]
		if !ok {
			continue
		}
		ownerOffset := owner.Pos()
		if ownerOffset <= offset && ownerOffset > bestOffset {
			bestOffset = ownerOffset
			best = typ
		}
	}
	return types.Default(best)
}

// fieldType resolves a struct field by name, looking through pointers and
// optionals the way member access does.
func fieldType(typ types.Type, name string) types.Type {
	switch t := unwrapped(typ).(type) {
	case *types.Struct:
		for _, field := range t.Fields {
			if field.Name == name {
				return field.Type
			}
		}
	case *types.Union:
		if member := t.MemberNamed(name); member != nil {
			return member.Type
		}
	}
	return nil
}

func unwrapped(typ types.Type) types.Type {
	for {
		switch t := typ.(type) {
		case *types.Pointer:
			typ = t.Elem
		case *types.Optional:
			typ = t.Elem
		default:
			return typ
		}
	}
}

// memberItems lists what `.` can reach on a value of the given type: its
// fields, its methods, and the built-in method surface.
func memberItems(typ types.Type) []completionItem {
	var items []completionItem

	switch t := unwrapped(typ).(type) {
	case *types.Struct:
		for _, field := range t.Fields {
			items = append(items, completionItem{
				Label: field.Name, Kind: completionKindField, Detail: field.Type.String(),
			})
		}
		items = append(items, methodItems(t.Methods)...)
	case *types.Union:
		for _, member := range t.Members {
			items = append(items, completionItem{
				Label: member.Name, Kind: completionKindField, Detail: member.Type.String(),
			})
		}
		items = append(items, methodItems(t.Methods)...)
	case *types.Enum:
		items = append(items, methodItems(t.Methods)...)
	case *types.Slice, *types.Array:
		items = append(items, completionItem{Label: "len", Kind: completionKindMethod, Detail: "fn(): usize"})
	case *types.Chan:
		items = append(items, completionItem{Label: "close", Kind: completionKindMethod, Detail: "fn()"})
	case *types.Basic:
		items = append(items, basicMemberItems(t)...)
	}
	return items
}

func methodItems(methods map[string]*types.Method) []completionItem {
	var items []completionItem
	for name, method := range methods {
		if method.Recv == types.NoRecv {
			continue
		}
		items = append(items, completionItem{
			Label: name, Kind: completionKindMethod, Detail: method.Sig.String(),
		})
	}
	return items
}

func basicMemberItems(basic *types.Basic) []completionItem {
	switch {
	case basic.Kind == types.StringKind:
		return []completionItem{
			{Label: "len", Kind: completionKindMethod, Detail: "fn(): usize"},
		}
	case types.IsNumeric(basic) || basic.Kind == types.RuneKind:
		return []completionItem{
			{Label: "compare", Kind: completionKindMethod, Detail: "fn(other): i32"},
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Namespace completion after `::`
// ---------------------------------------------------------------------------

// completeNamespace offers what lives behind `Name::`: enum variants,
// a type's associated functions and methods, or a wildcard import's
// exported names.
func (s *Server) completeNamespace(doc *document, before string) []completionItem {
	a := doc.typed
	if a == nil {
		a = doc.analysis
	}
	if a == nil {
		return nil
	}

	chain := trailingChain(before)
	if len(chain) == 0 {
		return nil
	}
	name := chain[len(chain)-1]

	var items []completionItem
	for _, module := range a.program.Modules {
		for _, decl := range module.Tree.Decls {
			switch d := decl.(type) {
			case *ast.EnumDecl:
				if d.Name != name {
					continue
				}
				for _, variant := range d.Variants {
					items = append(items, completionItem{
						Label: variant.Name, Kind: completionKindEnumCase,
					})
				}
				if a.info != nil {
					if enum, ok := a.info.Enums[d]; ok {
						items = append(items, allMethodItems(enum.Methods)...)
					}
				}
			case *ast.StructDecl:
				if d.Name != name {
					continue
				}
				if a.info != nil {
					if strct, ok := a.info.Structs[d]; ok {
						items = append(items, allMethodItems(strct.Methods)...)
					}
				}
			}
		}
	}

	items = append(items, s.wildcardExports(a, name)...)
	return items
}

func allMethodItems(methods map[string]*types.Method) []completionItem {
	var items []completionItem
	for name, method := range methods {
		kind := completionKindMethod
		if method.Recv == types.NoRecv {
			kind = completionKindFunction
		}
		items = append(items, completionItem{
			Label: name, Kind: kind, Detail: method.Sig.String(),
		})
	}
	return items
}

// wildcardExports lists the exported declarations of the module behind an
// `import * as name`.
func (s *Server) wildcardExports(a *analysis, name string) []completionItem {
	root := a.program.Root
	var items []completionItem
	for _, imp := range root.Tree.Imports {
		if imp.Wildcard != name {
			continue
		}
		target := root.Deps[imp.Path]
		if target == nil {
			continue
		}
		for _, decl := range target.Tree.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Exported && d.Owner == "" {
					items = append(items, completionItem{Label: d.Name, Kind: completionKindFunction})
				}
			case *ast.StructDecl:
				if d.Exported {
					items = append(items, completionItem{Label: d.Name, Kind: completionKindStruct})
				}
			case *ast.EnumDecl:
				if d.Exported {
					items = append(items, completionItem{Label: d.Name, Kind: completionKindEnum})
				}
			case *ast.VarDecl:
				if d.Exported && len(d.Names) > 0 {
					items = append(items, completionItem{Label: d.Names[0].Name, Kind: completionKindConstant})
				}
			}
		}
	}
	return items
}
