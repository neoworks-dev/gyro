package lsp

import (
	"sort"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/types"
)

// Inlay hints show what the source leaves implicit: the inferred type of a
// binding, and the parameter a positional argument fills. They come from
// sema's recordings, so they are offered only while the buffer checks. The
// last good analysis is not a fallback here the way it is for completion:
// its offsets belong to text the editor has already edited, and a hint in
// the wrong column is worse than no hint.

func (s *Server) handleInlayHint(req *request) error {
	var params inlayHintParams
	if ok, err := decode(s, req, &params); !ok {
		return err
	}

	doc := s.documentFor(params.TextDocument.URI)
	if doc == nil || doc.isMetals() || doc.analysis == nil || doc.analysis.info == nil {
		return s.conn.respond(req.ID, []inlayHint{})
	}

	a := doc.analysis
	collector := &hintCollector{
		text:    a.text,
		info:    a.info,
		current: view{calls: a.info.Calls, locals: a.info.Locals},
		start:   offsetFor(a.text, params.Range.Start),
		end:     offsetFor(a.text, params.Range.End),
	}
	for _, decl := range a.program.Root.Tree.Decls {
		collector.collectDecl(decl)
	}
	if collector.hints == nil {
		collector.hints = []inlayHint{}
	}
	// The walk finishes an expression before naming the argument it fills,
	// so the hints come out of order. Editors are happier with them sorted.
	sort.SliceStable(collector.hints, func(i, j int) bool {
		return before(collector.hints[i].Position, collector.hints[j].Position)
	})
	return s.conn.respond(req.ID, collector.hints)
}

// view is the set of recordings the hints read: the shared Info maps, or
// one instantiation's own inside a generic body, where the shared maps hold
// nothing.
type view struct {
	calls  map[*ast.CallExpr]*sema.CallInfo
	locals map[ast.Node]map[string]types.Type
}

type hintCollector struct {
	text string
	info *sema.Info
	// start and end bound the range the client asked about; the walk still
	// covers the file, since a hint's position is not where its node starts.
	start, end int
	current    view
	hints      []inlayHint
}

func (c *hintCollector) add(offset int, label string, kind int) {
	if offset < c.start || offset > c.end {
		return
	}
	c.hints = append(c.hints, inlayHint{
		Position: positionFor(c.text, offset),
		Label:    label,
		Kind:     kind,
		// A type hint reads as part of the name it follows; a parameter
		// name needs air before the argument.
		PaddingRight: kind == inlayKindParameter,
	})
}

func (c *hintCollector) addTypeHint(offset int, typ types.Type) {
	c.add(offset, ": "+typ.String(), inlayKindType)
}

// localType is the declared type of a binding, looked up the way sema
// recorded it: under the node that introduced it.
func (c *hintCollector) localType(owner ast.Node, name string) (types.Type, bool) {
	byName, ok := c.current.locals[owner]
	if !ok {
		return nil, false
	}
	typ, ok := byName[name]
	if !ok {
		return nil, false
	}
	return types.Default(typ), true
}

// ---------------------------------------------------------------------------
// Declarations
// ---------------------------------------------------------------------------

func (c *hintCollector) collectDecl(decl ast.Decl) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		c.collectFunc(d)
	case *ast.VarDecl:
		c.collectVarDecl(d)
	}
}

// collectFunc walks a body under the recordings that apply to it. A generic
// body has none of its own: sema checks it once per instantiation and files
// the results separately, so hints appear only when a single instantiation
// pins the types unambiguously.
func (c *hintCollector) collectFunc(decl *ast.FuncDecl) {
	if decl.Body == nil {
		return
	}
	outer := c.current
	defer func() { c.current = outer }()

	if isGeneric(decl) {
		only, ok := soleInstance(c.info, decl)
		if !ok {
			return
		}
		c.current = view{calls: only.Calls, locals: only.Locals}
	}

	for _, param := range decl.Params {
		c.collectExpr(param.Default)
	}
	c.collectBlock(decl.Body)
}

func isGeneric(decl *ast.FuncDecl) bool {
	return len(decl.TypeParams) > 0 || len(decl.OwnerParams) > 0
}

func soleInstance(info *sema.Info, decl *ast.FuncDecl) (*sema.FuncInstance, bool) {
	byKey := info.Instances[decl]
	if len(byKey) != 1 {
		return nil, false
	}
	for _, only := range byKey {
		return only, true
	}
	return nil, false
}

func (c *hintCollector) collectVarDecl(decl *ast.VarDecl) {
	c.collectExpr(decl.Value)
	if decl.Type != nil {
		return
	}
	for _, name := range decl.Names {
		if name.Blank {
			continue
		}
		typ, ok := c.localType(decl, name.Name)
		if !ok {
			continue
		}
		c.addTypeHint(name.Offset+len(name.Name), typ)
	}
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

func (c *hintCollector) collectBlock(block *ast.Block) {
	if block == nil {
		return
	}
	for _, stmt := range block.Stmts {
		c.collectStmt(stmt)
	}
}

func (c *hintCollector) collectStmt(stmt ast.Stmt) {
	switch s := stmt.(type) {
	case nil:
		return
	case *ast.VarDecl:
		c.collectVarDecl(s)
	case *ast.Block:
		c.collectBlock(s)
	case *ast.ExprStmt:
		c.collectExpr(s.X)
	case *ast.AssignStmt:
		for _, lhs := range s.Lhs {
			c.collectExpr(lhs)
		}
		c.collectExpr(s.Rhs)
	case *ast.ReturnStmt:
		for _, result := range s.Results {
			c.collectExpr(result)
		}
	case *ast.IfStmt:
		c.collectExpr(s.Cond)
		c.collectBlock(s.Then)
		if s.Else != nil {
			c.collectStmt(s.Else)
		}
	case *ast.WhileStmt:
		c.collectExpr(s.Cond)
		c.collectBlock(s.Body)
	case *ast.ForStmt:
		if s.Init != nil {
			c.collectStmt(s.Init)
		}
		c.collectExpr(s.Cond)
		if s.Post != nil {
			c.collectStmt(s.Post)
		}
		c.collectBlock(s.Body)
	case *ast.ForInStmt:
		c.collectForIn(s)
	case *ast.DeferStmt:
		c.collectExpr(s.Call)
	case *ast.WithStmt:
		c.collectExpr(s.Allocator)
	case *ast.SendStmt:
		c.collectExpr(s.Chan)
		c.collectExpr(s.Value)
	case *ast.SelectStmt:
		for _, arm := range s.Arms {
			c.collectStmt(arm.Comm)
			c.collectStmt(arm.Body)
		}
		if s.Else != nil {
			c.collectStmt(s.Else)
		}
	}
}

func (c *hintCollector) collectForIn(stmt *ast.ForInStmt) {
	c.collectExpr(stmt.Iterable)
	c.collectBlock(stmt.Body)

	typ, ok := c.localType(stmt, stmt.Name)
	if !ok {
		return
	}
	// The AST records where the statement starts, not where its bound name
	// sits, so the name is located by the same scan the index uses.
	start, end := findName(c.text, stmt.Offset, stmt.Name)
	if end-start != len(stmt.Name) {
		return
	}
	c.addTypeHint(end, typ)
}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

func (c *hintCollector) collectExpr(expr ast.Expr) {
	switch e := expr.(type) {
	case nil:
		return
	case *ast.CallExpr:
		c.collectCall(e)
	case *ast.ClosureExpr:
		c.collectClosure(e)
	case *ast.MatchExpr:
		c.collectMatch(e)
	case *ast.StringLit:
		for _, part := range e.Parts {
			c.collectExpr(part.Expr)
		}
	case *ast.ArrayLit:
		for _, element := range e.Elements {
			c.collectExpr(element)
		}
	case *ast.TupleLit:
		for _, element := range e.Elements {
			c.collectExpr(element)
		}
	case *ast.StructLit:
		for _, field := range e.Fields {
			c.collectExpr(field.Value)
		}
	case *ast.UnaryExpr:
		c.collectExpr(e.X)
	case *ast.BinaryExpr:
		c.collectExpr(e.X)
		c.collectExpr(e.Y)
	case *ast.SelectorExpr:
		c.collectExpr(e.X)
	case *ast.TupleIndexExpr:
		c.collectExpr(e.X)
	case *ast.IndexExpr:
		c.collectExpr(e.X)
		c.collectExpr(e.Index)
	case *ast.RangeExpr:
		c.collectExpr(e.Lo)
		c.collectExpr(e.Hi)
	case *ast.BlockExpr:
		c.collectBlock(e.Block)
	case *ast.AwaitExpr:
		c.collectExpr(e.X)
	case *ast.RecvExpr:
		c.collectExpr(e.Chan)
	case *ast.ChanLit:
		c.collectExpr(e.Cap)
	case *ast.CastExpr:
		c.collectExpr(e.X)
	}
}

func (c *hintCollector) collectCall(call *ast.CallExpr) {
	c.collectExpr(call.Fun)
	for _, arg := range call.TypeArgs {
		c.collectExpr(arg.Expr)
	}

	params := c.paramsOf(call)
	for i, arg := range call.Args {
		c.collectExpr(arg.Value)
		if i < len(params) {
			c.addParameterHint(params[i], arg)
		}
	}
}

// paramsOf returns the declared parameters a call's positional arguments
// fill, or nil when there is no declaration to read them from: the built-in
// method surface, calls through a function value, enum construction.
func (c *hintCollector) paramsOf(call *ast.CallExpr) []ast.Param {
	resolved, ok := c.current.calls[call]
	if !ok || resolved.Method == nil || resolved.Method.Decl == nil {
		return nil
	}
	params := resolved.Method.Decl.Params
	if len(call.Args) > len(params) {
		// The lists do not line up, so position i names nothing reliable.
		// Naming the wrong parameter is worse than naming none.
		return nil
	}
	return params
}

func (c *hintCollector) addParameterHint(param ast.Param, arg ast.Arg) {
	if arg.Name != "" || arg.Value == nil {
		return
	}
	if param.Name == "" || param.Name == "_" {
		return
	}
	if echoesName(arg.Value, param.Name) {
		return
	}
	c.add(exprStart(arg.Value), param.Name+":", inlayKindParameter)
}

// exprStart is where an expression begins in the source. A node's own
// Offset is its operator rather than its extent — the `.` of `xs.len()`,
// the `+` of `a + b` — so a hint that belongs in front of the whole
// expression walks down its left spine.
func exprStart(expr ast.Expr) int {
	switch e := expr.(type) {
	case *ast.CallExpr:
		return leftmost(e.Offset, e.Fun)
	case *ast.SelectorExpr:
		return leftmost(e.Offset, e.X)
	case *ast.IndexExpr:
		return leftmost(e.Offset, e.X)
	case *ast.TupleIndexExpr:
		return leftmost(e.Offset, e.X)
	case *ast.BinaryExpr:
		return leftmost(e.Offset, e.X)
	case *ast.CastExpr:
		return leftmost(e.Offset, e.X)
	case *ast.RangeExpr:
		return leftmost(e.Offset, e.Lo)
	}
	return expr.Pos()
}

func leftmost(offset int, left ast.Expr) int {
	if left == nil {
		return offset
	}
	return min(offset, exprStart(left))
}

// echoesName reports whether the argument already spells the parameter's
// name at the call site, where the hint would only repeat what is there.
func echoesName(value ast.Expr, name string) bool {
	switch v := value.(type) {
	case *ast.Ident:
		return v.Name == name
	case *ast.SelectorExpr:
		return v.Name == name
	}
	return false
}

func (c *hintCollector) collectClosure(closure *ast.ClosureExpr) {
	for _, param := range closure.Params {
		c.collectExpr(param.Default)
		if param.Type != nil {
			continue
		}
		if typ, ok := c.localType(closure, param.Name); ok {
			c.addTypeHint(param.Offset+len(param.Name), typ)
		}
	}
	c.collectBlock(closure.Body)
	c.collectExpr(closure.BodyExpr)
}

func (c *hintCollector) collectMatch(match *ast.MatchExpr) {
	c.collectExpr(match.Subject)
	for _, arm := range match.Arms {
		c.collectPattern(arm.Pattern)
		c.collectStmt(arm.Body)
	}
}

func (c *hintCollector) collectPattern(pattern ast.Pattern) {
	switch p := pattern.(type) {
	case *ast.BindingPattern:
		if typ, ok := c.localType(p, p.Name); ok {
			c.addTypeHint(p.Offset+len(p.Name), typ)
		}
	case *ast.VariantPattern:
		for _, payload := range p.Payload {
			c.collectPattern(payload)
		}
	case *ast.TypeTestPattern:
		for _, payload := range p.Payload {
			c.collectPattern(payload)
		}
	}
}
