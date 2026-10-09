package lsp

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/loader"
)

// The AST records where each node starts but not where names sit inside
// declarations, so the index walks every tree once and records anchors:
// exact source spans that positional requests can hit. Value anchors carry
// the expression for Info lookups; declaration anchors carry the declaring
// node.

// anchor is one name occurrence in the root module.
type anchor struct {
	start, end int
	name       string
	// expr is set for a name in value or type position: the node to look
	// up in Info.Types and Info.Refs.
	expr ast.Expr
	// decl is set when the anchor is itself a declaration's name.
	decl ast.Node
	// localOwner keys Info.Locals for a binding introduced here.
	localOwner ast.Node
}

// site is where a declaration's name lives, for go-to-definition targets.
type site struct {
	module     *loader.Module
	start, end int
}

// binding is a name completion can offer, valid inside a region of the
// root file.
type binding struct {
	name  string
	kind  int
	owner ast.Node
	// visibleFrom is the declaration offset; a binding is offered only
	// after it. regionStart/regionEnd bound the enclosing declaration.
	visibleFrom, regionStart, regionEnd int
}

// topDecl is one top-level declaration of the root module, with its
// approximate extent: from its first token to the start of the next
// declaration.
type topDecl struct {
	node       ast.Decl
	start, end int
}

type index struct {
	program   *loader.Program
	src       string
	anchors   []anchor
	declSites map[ast.Node]site
	bindings  []binding
	topLevel  []topDecl
}

func buildIndex(program *loader.Program) *index {
	idx := &index{
		program:   program,
		src:       program.Root.File.Src,
		declSites: map[ast.Node]site{},
	}

	for _, module := range program.Modules {
		idx.indexDeclSites(module)
	}
	idx.indexRoot()

	sort.SliceStable(idx.anchors, func(i, j int) bool {
		return idx.anchors[i].start < idx.anchors[j].start
	})
	return idx
}

// anchorAt finds the innermost anchor containing the byte offset.
func (idx *index) anchorAt(offset int) *anchor {
	found := -1
	for i := range idx.anchors {
		if idx.anchors[i].start > offset {
			break
		}
		if offset < idx.anchors[i].end {
			found = i
		}
	}
	if found < 0 {
		return nil
	}
	return &idx.anchors[found]
}

// ---------------------------------------------------------------------------
// Declaration sites, across every module
// ---------------------------------------------------------------------------

func (idx *index) indexDeclSites(module *loader.Module) {
	src := module.File.Src
	for _, decl := range module.Tree.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			start, end := findName(src, d.Offset, d.Name)
			idx.declSites[d] = site{module, start, end}
		case *ast.StructDecl:
			start, end := findName(src, d.Offset, d.Name)
			idx.declSites[d] = site{module, start, end}
		case *ast.EnumDecl:
			start, end := findName(src, d.Offset, d.Name)
			idx.declSites[d] = site{module, start, end}
		case *ast.VarDecl:
			if len(d.Names) > 0 {
				name := d.Names[0]
				idx.declSites[d] = site{module, name.Offset, name.Offset + len(name.Name)}
			}
		}
	}
	for _, imp := range module.Tree.Imports {
		idx.declSites[imp] = site{module, imp.Offset, imp.Offset + len("import")}
	}
}

// findName locates a declaration's name as a whole word after its first
// token, so `fn Stack::push` anchors on the right identifier even though
// the AST only stores the declaration's start.
func findName(src string, from int, name string) (int, int) {
	if name == "" {
		return from, from + 1
	}
	for at := from; at < len(src); {
		found := strings.Index(src[at:], name)
		if found < 0 {
			break
		}
		start := at + found
		end := start + len(name)
		beforeOK := start == 0 || !isIdentifierPart(runeBefore(src, start))
		afterOK := end >= len(src) || !isIdentifierPart(runeAt(src, end))
		if beforeOK && afterOK {
			return start, end
		}
		at = start + len(name)
	}
	return from, from + 1
}

func runeAt(src string, offset int) rune {
	r, _ := utf8.DecodeRuneInString(src[offset:])
	return r
}

func runeBefore(src string, offset int) rune {
	r, _ := utf8.DecodeLastRuneInString(src[:offset])
	return r
}

// ---------------------------------------------------------------------------
// Root module walk
// ---------------------------------------------------------------------------

func (idx *index) indexRoot() {
	tree := idx.program.Root.Tree

	for _, imp := range tree.Imports {
		for _, name := range imp.Names {
			idx.anchors = append(idx.anchors, anchor{
				start: name.Offset, end: name.Offset + len(name.Name),
				name: name.Name, decl: imp,
			})
			bound := name.Name
			if name.Alias != "" {
				bound = name.Alias
			}
			idx.addModuleBinding(bound, completionKindModule, imp)
		}
		if imp.Wildcard != "" {
			idx.addModuleBinding(imp.Wildcard, completionKindModule, imp)
		}
	}

	// Top-level extents: each declaration reaches to the start of the next.
	for i, decl := range tree.Decls {
		end := len(idx.src)
		if i+1 < len(tree.Decls) {
			end = tree.Decls[i+1].Pos()
		}
		idx.topLevel = append(idx.topLevel, topDecl{node: decl, start: decl.Pos(), end: end})
	}

	for i := range idx.topLevel {
		idx.indexDecl(idx.topLevel[i].node, idx.topLevel[i].start, idx.topLevel[i].end)
	}
}

// addModuleBinding registers a name visible everywhere in the file.
func (idx *index) addModuleBinding(name string, kind int, owner ast.Node) {
	idx.bindings = append(idx.bindings, binding{
		name: name, kind: kind, owner: owner,
		visibleFrom: 0, regionStart: 0, regionEnd: len(idx.src),
	})
}

func (idx *index) indexDecl(decl ast.Decl, regionStart, regionEnd int) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		start, end := findName(idx.src, d.Offset, d.Name)
		idx.anchors = append(idx.anchors, anchor{start: start, end: end, name: d.Name, decl: d})
		idx.addModuleBinding(d.Name, completionKindFunction, d)
		for _, param := range d.Params {
			idx.anchors = append(idx.anchors, anchor{
				start: param.Offset, end: param.Offset + len(param.Name),
				name: param.Name, decl: d, localOwner: d,
			})
			idx.bindings = append(idx.bindings, binding{
				name: param.Name, kind: completionKindVariable, owner: d,
				visibleFrom: param.Offset, regionStart: regionStart, regionEnd: regionEnd,
			})
			idx.indexType(param.Type)
			idx.indexExpr(param.Default, d, regionStart, regionEnd)
		}
		for _, result := range d.Results {
			idx.indexType(result)
		}
		if d.Body != nil {
			idx.indexBlock(d.Body, d, regionStart, regionEnd)
		}

	case *ast.StructDecl:
		start, end := findName(idx.src, d.Offset, d.Name)
		idx.anchors = append(idx.anchors, anchor{start: start, end: end, name: d.Name, decl: d})
		idx.addModuleBinding(d.Name, completionKindStruct, d)
		for _, field := range d.Fields {
			idx.anchors = append(idx.anchors, anchor{
				start: field.Offset + fieldNameShift(idx.src, field),
				end:   field.Offset + fieldNameShift(idx.src, field) + len(field.Name),
				name:  field.Name, decl: d,
			})
			idx.indexType(field.Type)
		}

	case *ast.EnumDecl:
		start, end := findName(idx.src, d.Offset, d.Name)
		idx.anchors = append(idx.anchors, anchor{start: start, end: end, name: d.Name, decl: d})
		idx.addModuleBinding(d.Name, completionKindEnum, d)
		for _, variant := range d.Variants {
			idx.anchors = append(idx.anchors, anchor{
				start: variant.Offset, end: variant.Offset + len(variant.Name),
				name: variant.Name, decl: d,
			})
			for _, payload := range variant.Payload {
				idx.indexType(payload)
			}
		}

	case *ast.VarDecl:
		idx.indexVarDecl(d, nil, regionStart, regionEnd, true)

	case *ast.ExportDecl:
		// Export lists name module-level declarations that are anchored at
		// their definitions.
	}
}

// fieldNameShift accounts for the `export` prefix a struct field may
// carry: Field.Offset is where the field starts, not where its name does.
func fieldNameShift(src string, field ast.Field) int {
	rest := src[field.Offset:]
	if strings.HasPrefix(rest, "export") {
		if shifted := strings.Index(rest, field.Name); shifted > 0 {
			return shifted
		}
	}
	return 0
}

// indexVarDecl anchors the bound names. owner keys Info.Locals: the
// VarDecl itself for locals, and module-level declarations record no
// Locals entry but still anchor and bind.
func (idx *index) indexVarDecl(d *ast.VarDecl, enclosing ast.Node, regionStart, regionEnd int, topLevel bool) {
	kind := completionKindVariable
	if d.Const {
		kind = completionKindConstant
	}
	if _, known := idx.declSites[d]; !known && len(d.Names) > 0 {
		first := d.Names[0]
		idx.declSites[d] = site{idx.program.Root, first.Offset, first.Offset + len(first.Name)}
	}
	for _, name := range d.Names {
		if name.Blank {
			continue
		}
		idx.anchors = append(idx.anchors, anchor{
			start: name.Offset, end: name.Offset + len(name.Name),
			name: name.Name, decl: d, localOwner: d,
		})
		if topLevel {
			idx.addModuleBinding(name.Name, kind, d)
		} else {
			idx.bindings = append(idx.bindings, binding{
				name: name.Name, kind: kind, owner: d,
				visibleFrom: name.Offset, regionStart: regionStart, regionEnd: regionEnd,
			})
		}
	}
	idx.indexType(d.Type)
	idx.indexExpr(d.Value, enclosing, regionStart, regionEnd)
}

// ---------------------------------------------------------------------------
// Statements and expressions
// ---------------------------------------------------------------------------

func (idx *index) indexBlock(block *ast.Block, enclosing ast.Node, regionStart, regionEnd int) {
	if block == nil {
		return
	}
	for _, stmt := range block.Stmts {
		idx.indexStmt(stmt, enclosing, regionStart, regionEnd)
	}
}

func (idx *index) indexStmt(stmt ast.Stmt, enclosing ast.Node, regionStart, regionEnd int) {
	switch s := stmt.(type) {
	case *ast.VarDecl:
		idx.indexVarDecl(s, enclosing, regionStart, regionEnd, false)
	case *ast.Block:
		idx.indexBlock(s, enclosing, regionStart, regionEnd)
	case *ast.ExprStmt:
		idx.indexExpr(s.X, enclosing, regionStart, regionEnd)
	case *ast.AssignStmt:
		for _, lhs := range s.Lhs {
			idx.indexExpr(lhs, enclosing, regionStart, regionEnd)
		}
		idx.indexExpr(s.Rhs, enclosing, regionStart, regionEnd)
	case *ast.ReturnStmt:
		for _, result := range s.Results {
			idx.indexExpr(result, enclosing, regionStart, regionEnd)
		}
	case *ast.IfStmt:
		idx.indexExpr(s.Cond, enclosing, regionStart, regionEnd)
		idx.indexBlock(s.Then, enclosing, regionStart, regionEnd)
		if s.Else != nil {
			idx.indexStmt(s.Else, enclosing, regionStart, regionEnd)
		}
	case *ast.WhileStmt:
		idx.indexExpr(s.Cond, enclosing, regionStart, regionEnd)
		idx.indexBlock(s.Body, enclosing, regionStart, regionEnd)
	case *ast.ForStmt:
		if s.Init != nil {
			idx.indexStmt(s.Init, enclosing, regionStart, regionEnd)
		}
		idx.indexExpr(s.Cond, enclosing, regionStart, regionEnd)
		if s.Post != nil {
			idx.indexStmt(s.Post, enclosing, regionStart, regionEnd)
		}
		idx.indexBlock(s.Body, enclosing, regionStart, regionEnd)
	case *ast.ForInStmt:
		nameStart := nameOffsetAfter(idx.src, s.Offset, s.Name)
		idx.declSites[s] = site{idx.program.Root, nameStart, nameStart + len(s.Name)}
		idx.anchors = append(idx.anchors, anchor{
			start: nameStart, end: nameStart + len(s.Name),
			name: s.Name, decl: s, localOwner: s,
		})
		idx.bindings = append(idx.bindings, binding{
			name: s.Name, kind: completionKindVariable, owner: s,
			visibleFrom: s.Offset, regionStart: regionStart, regionEnd: regionEnd,
		})
		idx.indexExpr(s.Iterable, enclosing, regionStart, regionEnd)
		idx.indexBlock(s.Body, enclosing, regionStart, regionEnd)
	case *ast.DeferStmt:
		idx.indexExpr(s.Call, enclosing, regionStart, regionEnd)
	case *ast.SendStmt:
		idx.indexExpr(s.Chan, enclosing, regionStart, regionEnd)
		idx.indexExpr(s.Value, enclosing, regionStart, regionEnd)
	case *ast.SelectStmt:
		for _, arm := range s.Arms {
			idx.indexStmt(arm.Comm, enclosing, regionStart, regionEnd)
			idx.indexStmt(arm.Body, enclosing, regionStart, regionEnd)
		}
		if s.Else != nil {
			idx.indexStmt(s.Else, enclosing, regionStart, regionEnd)
		}
	case *ast.BranchStmt:
		// Nothing to anchor.
	}
}

// nameOffsetAfter finds a bound name written shortly after a keyword, such
// as the `value` of `for value in xs`.
func nameOffsetAfter(src string, from int, name string) int {
	start, _ := findName(src, from, name)
	return start
}

func (idx *index) indexExpr(expr ast.Expr, enclosing ast.Node, regionStart, regionEnd int) {
	switch e := expr.(type) {
	case nil:
		return
	case *ast.Ident:
		if e.Name != "_" && e.Name != "<error>" {
			idx.anchors = append(idx.anchors, anchor{
				start: e.Offset, end: e.Offset + len(e.Name), name: e.Name, expr: e,
			})
		}
	case *ast.SelfExpr:
		idx.anchors = append(idx.anchors, anchor{
			start: e.Offset, end: e.Offset + len("self"), name: "self", expr: e,
		})
	case *ast.Path:
		idx.indexPath(e)
	case *ast.SelectorExpr:
		idx.indexExpr(e.X, enclosing, regionStart, regionEnd)
		start, end := findName(idx.src, e.Offset, e.Name)
		idx.anchors = append(idx.anchors, anchor{start: start, end: end, name: e.Name, expr: e})
	case *ast.TupleIndexExpr:
		idx.indexExpr(e.X, enclosing, regionStart, regionEnd)
	case *ast.BasicLit:
		// Nothing to anchor.
	case *ast.StringLit:
		for _, part := range e.Parts {
			idx.indexExpr(part.Expr, enclosing, regionStart, regionEnd)
		}
	case *ast.ArrayLit:
		for _, element := range e.Elements {
			idx.indexExpr(element, enclosing, regionStart, regionEnd)
		}
	case *ast.TupleLit:
		for _, element := range e.Elements {
			idx.indexExpr(element, enclosing, regionStart, regionEnd)
		}
	case *ast.StructLit:
		idx.indexType(e.Type)
		for _, field := range e.Fields {
			idx.indexExpr(field.Value, enclosing, regionStart, regionEnd)
		}
	case *ast.UnaryExpr:
		idx.indexExpr(e.X, enclosing, regionStart, regionEnd)
	case *ast.BinaryExpr:
		idx.indexExpr(e.X, enclosing, regionStart, regionEnd)
		idx.indexExpr(e.Y, enclosing, regionStart, regionEnd)
	case *ast.CallExpr:
		idx.indexExpr(e.Fun, enclosing, regionStart, regionEnd)
		for _, arg := range e.TypeArgs {
			idx.indexType(arg.Type)
			idx.indexExpr(arg.Expr, enclosing, regionStart, regionEnd)
		}
		for _, arg := range e.Args {
			idx.indexExpr(arg.Value, enclosing, regionStart, regionEnd)
		}
	case *ast.IndexExpr:
		idx.indexExpr(e.X, enclosing, regionStart, regionEnd)
		idx.indexExpr(e.Index, enclosing, regionStart, regionEnd)
	case *ast.RangeExpr:
		idx.indexExpr(e.Lo, enclosing, regionStart, regionEnd)
		idx.indexExpr(e.Hi, enclosing, regionStart, regionEnd)
	case *ast.MatchExpr:
		idx.indexExpr(e.Subject, enclosing, regionStart, regionEnd)
		for _, arm := range e.Arms {
			idx.indexPattern(arm.Pattern, regionStart, regionEnd)
			idx.indexStmt(arm.Body, enclosing, regionStart, regionEnd)
		}
	case *ast.BlockExpr:
		idx.indexBlock(e.Block, enclosing, regionStart, regionEnd)
	case *ast.AwaitExpr:
		idx.indexExpr(e.X, enclosing, regionStart, regionEnd)
	case *ast.RecvExpr:
		idx.indexExpr(e.Chan, enclosing, regionStart, regionEnd)
	case *ast.ChanLit:
		idx.indexType(e.Elem)
		idx.indexExpr(e.Cap, enclosing, regionStart, regionEnd)
	case *ast.ClosureExpr:
		idx.declSites[e] = site{idx.program.Root, e.Offset, e.Offset + len("fn")}
		for _, param := range e.Params {
			idx.anchors = append(idx.anchors, anchor{
				start: param.Offset, end: param.Offset + len(param.Name),
				name: param.Name, decl: e, localOwner: e,
			})
			idx.bindings = append(idx.bindings, binding{
				name: param.Name, kind: completionKindVariable, owner: e,
				visibleFrom: param.Offset, regionStart: regionStart, regionEnd: regionEnd,
			})
			idx.indexType(param.Type)
			idx.indexExpr(param.Default, enclosing, regionStart, regionEnd)
		}
		for _, result := range e.Results {
			idx.indexType(result)
		}
		idx.indexBlock(e.Body, e, regionStart, regionEnd)
		idx.indexExpr(e.BodyExpr, e, regionStart, regionEnd)
	case *ast.CastExpr:
		idx.indexExpr(e.X, enclosing, regionStart, regionEnd)
		idx.indexType(e.Type)
	}
}

func (idx *index) indexPattern(pattern ast.Pattern, regionStart, regionEnd int) {
	switch p := pattern.(type) {
	case *ast.BindingPattern:
		idx.declSites[p] = site{idx.program.Root, p.Offset, p.Offset + len(p.Name)}
		idx.anchors = append(idx.anchors, anchor{
			start: p.Offset, end: p.Offset + len(p.Name),
			name: p.Name, decl: p, localOwner: p,
		})
		idx.bindings = append(idx.bindings, binding{
			name: p.Name, kind: completionKindVariable, owner: p,
			visibleFrom: p.Offset, regionStart: regionStart, regionEnd: regionEnd,
		})
	case *ast.VariantPattern:
		idx.indexPath(p.Path)
		for _, payload := range p.Payload {
			idx.indexPattern(payload, regionStart, regionEnd)
		}
	case *ast.TypeTestPattern:
		idx.indexType(p.Type)
		for _, payload := range p.Payload {
			idx.indexPattern(payload, regionStart, regionEnd)
		}
	case *ast.WildcardPattern, *ast.LiteralPattern:
		// Nothing to anchor.
	}
}

// indexPath anchors every segment of a path on the path node itself, which
// is what Info.Refs is keyed by.
func (idx *index) indexPath(path *ast.Path) {
	if path == nil {
		return
	}
	for _, segment := range path.Segments {
		idx.anchors = append(idx.anchors, anchor{
			start: segment.Offset, end: segment.Offset + len(segment.Name),
			name: segment.Name, expr: path,
		})
		for _, arg := range segment.Args {
			idx.indexType(arg)
		}
	}
}

func (idx *index) indexType(typ ast.Type) {
	switch t := typ.(type) {
	case nil:
		return
	case *ast.NamedType:
		idx.indexPath(t.Path)
	case *ast.PointerType:
		idx.indexType(t.Elem)
	case *ast.ArrayType:
		idx.indexType(t.Elem)
	case *ast.TupleType:
		for _, element := range t.Elements {
			idx.indexType(element)
		}
	case *ast.FuncType:
		for _, param := range t.Params {
			idx.indexType(param)
		}
		for _, result := range t.Results {
			idx.indexType(result)
		}
	case *ast.CodeType:
		for _, param := range t.Params {
			idx.indexType(param)
		}
		for _, result := range t.Results {
			idx.indexType(result)
		}
	case *ast.OptionalType:
		idx.indexType(t.Elem)
	case *ast.ChanType:
		idx.indexType(t.Elem)
	}
}
