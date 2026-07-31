package ast

import (
	"fmt"
	"strings"

	"github.com/gyro-lang/gyro/internal/token"
)

// Dump renders a node as an indented s-expression. It is the format used by
// `gyro ast` and by the parser's golden tests, so it is deliberately stable
// and free of source offsets.
func Dump(node Node) string {
	var printer printer
	printer.node(node)
	return printer.out.String()
}

type printer struct {
	out    strings.Builder
	indent int
}

func (p *printer) line(format string, args ...any) {
	p.out.WriteString(strings.Repeat("  ", p.indent))
	fmt.Fprintf(&p.out, format, args...)
	p.out.WriteByte('\n')
}

// child prints a labelled subtree, skipping nil nodes entirely.
func (p *printer) child(label string, node Node) {
	if node == nil || isNilNode(node) {
		return
	}
	p.line("%s:", label)
	p.indent++
	p.node(node)
	p.indent--
}

func (p *printer) group(label string, fn func()) {
	p.line("%s:", label)
	p.indent++
	fn()
	p.indent--
}

// isNilNode reports whether an interface holds a typed nil pointer, which
// happens whenever an optional field was never assigned.
func isNilNode(node Node) bool {
	switch value := node.(type) {
	case *Block:
		return value == nil
	case *Path:
		return value == nil
	}
	return false
}

func (p *printer) node(node Node) {
	switch n := node.(type) {
	case *File:
		p.line("File %s", n.Name)
		p.indent++
		for _, imp := range n.Imports {
			p.node(imp)
		}
		for _, decl := range n.Decls {
			p.node(decl)
		}
		p.indent--

	case *Import:
		p.line("Import %s from %q", bindingClause(n.Default, n.Wildcard, n.Names), n.Path)

	case *ExportDecl:
		clause := "{" + nameListString(n.Names) + "}"
		if n.Star {
			clause = "*"
		}
		if n.Path == "" {
			p.line("Export %s", clause)
			return
		}
		p.line("Export %s from %q", clause, n.Path)

	case *Decorator:
		p.line("Decorator @%s", pathString(n.Name))
		p.indent++
		p.args(n.Args)
		p.indent--

	case *FuncDecl:
		p.line("Func %s%s%s%s", exportMark(n.Exported), asyncMark(n.Async), n.Name, typeParamString(n.TypeParams))
		p.indent++
		p.decorators(n.Decorators)
		if n.Recv != nil {
			p.line("Recv: %sself", pointerMark(n.Recv.Pointer))
		}
		for _, param := range n.Params {
			p.group("Param "+param.Name, func() {
				p.child("Type", param.Type)
				p.child("Default", param.Default)
			})
		}
		for _, result := range n.Results {
			p.child("Result", result)
		}
		p.effects(n.Effects)
		p.child("Body", n.Body)
		p.indent--

	case *StructDecl:
		p.line("Struct %s%s%s", exportMark(n.Exported), n.Name, typeParamString(n.TypeParams))
		p.indent++
		p.decorators(n.Decorators)
		for _, field := range n.Fields {
			p.group("Field "+exportMark(field.Exported)+field.Name, func() {
				p.node(field.Type)
			})
		}
		p.indent--

	case *EnumDecl:
		p.line("Enum %s%s%s", exportMark(n.Exported), n.Name, typeParamString(n.TypeParams))
		p.indent++
		p.decorators(n.Decorators)
		for _, variant := range n.Variants {
			p.group("Variant "+variant.Name, func() {
				for _, payload := range variant.Payload {
					p.node(payload)
				}
			})
		}
		p.indent--

	case *ImplDecl:
		p.line("Impl%s", typeParamString(n.TypeParams))
		p.indent++
		p.child("Type", n.Type)
		for _, method := range n.Methods {
			p.node(method)
		}
		p.indent--

	case *VarDecl:
		p.line("%s %s%s", varKeyword(n.Const), exportMark(n.Exported), bindingString(n.Names))
		p.indent++
		p.decorators(n.Decorators)
		p.child("Type", n.Type)
		p.child("Value", n.Value)
		p.indent--

	case *Block:
		p.line("Block")
		p.indent++
		for _, stmt := range n.Stmts {
			p.node(stmt)
		}
		p.indent--

	case *ExprStmt:
		p.line("ExprStmt")
		p.indent++
		p.node(n.X)
		p.indent--

	case *AssignStmt:
		p.line("Assign %s", n.Op)
		p.indent++
		for _, target := range n.Lhs {
			p.child("Lhs", target)
		}
		p.child("Rhs", n.Rhs)
		p.indent--

	case *ReturnStmt:
		p.line("Return")
		p.indent++
		for _, result := range n.Results {
			p.node(result)
		}
		p.indent--

	case *IfStmt:
		p.line("If")
		p.indent++
		p.child("Cond", n.Cond)
		p.child("Then", n.Then)
		p.child("Else", n.Else)
		p.indent--

	case *WhileStmt:
		p.line("While")
		p.indent++
		p.child("Cond", n.Cond)
		p.child("Body", n.Body)
		p.indent--

	case *ForStmt:
		p.line("For")
		p.indent++
		p.child("Init", n.Init)
		p.child("Cond", n.Cond)
		p.child("Post", n.Post)
		p.child("Body", n.Body)
		p.indent--

	case *ForInStmt:
		p.line("ForIn %s", n.Name)
		p.indent++
		p.child("Iterable", n.Iterable)
		p.child("Body", n.Body)
		p.indent--

	case *BranchStmt:
		p.line("%s", n.Kind)

	case *SendStmt:
		p.line("Send")
		p.indent++
		p.child("Chan", n.Chan)
		p.child("Value", n.Value)
		p.indent--

	case *SelectStmt:
		p.line("Select")
		p.indent++
		for _, arm := range n.Arms {
			p.group("Arm", func() {
				p.child("Comm", arm.Comm)
				p.child("Body", arm.Body)
			})
		}
		p.child("Else", n.Else)
		p.indent--

	case *DeferStmt:
		p.line("Defer")
		p.indent++
		p.node(n.Call)
		p.indent--

	case *Ident:
		p.line("Ident %s", n.Name)

	case *Path:
		p.line("Path %s", pathString(n))

	case *SelfExpr:
		p.line("Self")

	case *BasicLit:
		p.line("Lit %s %s", n.Kind, quoteIfString(n))

	case *StringLit:
		p.line("StringLit")
		p.indent++
		for _, part := range n.Parts {
			if part.Expr != nil {
				p.child("Interp", part.Expr)
			} else {
				p.line("Text %q", part.Text)
			}
		}
		p.indent--

	case *ArrayLit:
		p.line("ArrayLit")
		p.indent++
		for _, element := range n.Elements {
			p.node(element)
		}
		p.indent--

	case *StructLit:
		p.line("StructLit%s", positionalMark(n.Positional))
		p.indent++
		p.child("Type", n.Type)
		for index, field := range n.Fields {
			p.child(fieldLabel(field, index), field.Value)
		}
		p.indent--

	case *UnaryExpr:
		p.line("Unary %s", n.Op)
		p.indent++
		p.node(n.X)
		p.indent--

	case *PostfixExpr:
		p.line("Postfix %s", n.Op)
		p.indent++
		p.node(n.X)
		p.indent--

	case *BinaryExpr:
		p.line("Binary %s", n.Op)
		p.indent++
		p.node(n.X)
		p.node(n.Y)
		p.indent--

	case *CallExpr:
		p.line("Call")
		p.indent++
		p.child("Fun", n.Fun)
		for _, arg := range n.TypeArgs {
			if arg.Type != nil {
				p.child("TypeArg", arg.Type)
			} else {
				p.child("TypeArg", arg.Expr)
			}
		}
		p.args(n.Args)
		p.indent--

	case *SelectorExpr:
		p.line("Selector .%s", n.Name)
		p.indent++
		p.node(n.X)
		p.indent--

	case *IndexExpr:
		p.line("Index")
		p.indent++
		p.node(n.X)
		p.child("Index", n.Index)
		p.indent--

	case *RangeExpr:
		p.line("Range")
		p.indent++
		p.node(n.Lo)
		p.node(n.Hi)
		p.indent--

	case *MatchExpr:
		p.line("Match")
		p.indent++
		p.child("Subject", n.Subject)
		for _, arm := range n.Arms {
			p.group("Arm", func() {
				p.child("Pattern", arm.Pattern)
				p.child("Body", arm.Body)
			})
		}
		p.indent--

	case *BlockExpr:
		p.node(n.Block)

	case *AwaitExpr:
		p.line("Await")
		p.indent++
		p.node(n.X)
		p.indent--

	case *RecvExpr:
		p.line("Recv")
		p.indent++
		p.node(n.Chan)
		p.indent--

	case *ChanLit:
		p.line("ChanLit")
		p.indent++
		p.child("Elem", n.Elem)
		p.child("Cap", n.Cap)
		p.indent--

	case *ClosureExpr:
		p.line("%s", strings.TrimSpace("Closure "+asyncMark(n.Async)+captureListString(n.Captures)))
		p.indent++
		for _, param := range n.Params {
			p.group("Param "+param.Name, func() {
				p.child("Type", param.Type)
				p.child("Default", param.Default)
			})
		}
		for _, result := range n.Results {
			p.child("Result", result)
		}
		p.effects(n.Effects)
		p.child("Body", n.Body)
		p.child("Body", n.BodyExpr)
		p.indent--

	case *CastExpr:
		p.line("Cast")
		p.indent++
		p.node(n.X)
		p.child("Type", n.Type)
		p.indent--

	case *WildcardPattern:
		p.line("Wildcard")

	case *LiteralPattern:
		p.line("LiteralPattern")
		p.indent++
		p.node(n.Value)
		p.indent--

	case *BindingPattern:
		p.line("BindingPattern %s", n.Name)

	case *VariantPattern:
		p.line("VariantPattern %s", pathString(n.Path))
		p.indent++
		for _, payload := range n.Payload {
			p.node(payload)
		}
		p.indent--

	case *NamedType:
		p.line("NamedType %s", pathString(n.Path))
		p.indent++
		for _, arg := range n.Args {
			p.node(arg)
		}
		p.indent--

	case *PointerType:
		p.line("PointerType")
		p.indent++
		p.node(n.Elem)
		p.indent--

	case *ArrayType:
		p.line("ArrayType")
		p.indent++
		p.node(n.Elem)
		p.child("Length", n.Length)
		p.indent--

	case *TupleType:
		p.line("TupleType")
		p.indent++
		for _, element := range n.Elements {
			p.node(element)
		}
		p.indent--

	case *FuncType:
		p.line("FuncType %s", asyncMark(n.Async))
		p.indent++
		for _, param := range n.Params {
			p.child("Param", param)
		}
		for _, result := range n.Results {
			p.child("Result", result)
		}
		p.effects(n.Effects)
		p.indent--

	case *ChanType:
		p.line("ChanType")
		p.indent++
		p.node(n.Elem)
		p.indent--

	default:
		p.line("<unknown %T>", node)
	}
}

// effects prints a declared effect row, if any.
func (p *printer) effects(row *EffectRow) {
	if row == nil {
		return
	}
	p.line("Effects: %s", effectRowString(row))
}

func effectRowString(row *EffectRow) string {
	if len(row.Names) == 0 && row.Var != "" {
		return "!" + row.Var
	}
	inner := strings.Join(row.Names, ", ")
	if row.Var != "" {
		if inner != "" {
			inner += ", "
		}
		inner += ".." + row.Var
	}
	return "!{" + inner + "}"
}

func positionalMark(positional bool) string {
	if positional {
		return " positional"
	}
	return ""
}

func fieldLabel(field FieldValue, index int) string {
	if field.Name == "" {
		return fmt.Sprintf("Field %d", index)
	}
	return "Field " + field.Name
}

func captureListString(captures []Capture) string {
	if len(captures) == 0 {
		return ""
	}
	rendered := make([]string, len(captures))
	for i, capture := range captures {
		rendered[i] = capture.Mode.String() + " " + capture.Name
	}
	return "[" + strings.Join(rendered, ", ") + "]"
}

func asyncMark(async bool) string {
	if async {
		return "async "
	}
	return ""
}

func (p *printer) decorators(decorators []*Decorator) {
	for _, decorator := range decorators {
		p.node(decorator)
	}
}

func (p *printer) args(args []Arg) {
	for _, arg := range args {
		label := "Arg"
		if arg.Name != "" {
			label = "Arg " + arg.Name
		}
		p.child(label, arg.Value)
	}
}

// bindingClause renders the part of an import between `import` and `from`.
func bindingClause(defaultName, wildcard string, names []ImportName) string {
	var parts []string
	if defaultName != "" {
		parts = append(parts, defaultName)
	}
	if wildcard != "" {
		parts = append(parts, "* as "+wildcard)
	}
	if len(names) > 0 {
		parts = append(parts, "{"+nameListString(names)+"}")
	}
	if len(parts) == 0 {
		return "<side effects>"
	}
	return strings.Join(parts, ", ")
}

func nameListString(names []ImportName) string {
	rendered := make([]string, len(names))
	for i, name := range names {
		rendered[i] = name.Name
		if name.Alias != "" {
			rendered[i] += " as " + name.Alias
		}
	}
	return strings.Join(rendered, ", ")
}

func pathString(path *Path) string {
	if path == nil {
		return ""
	}
	return strings.Join(path.Segments, "::")
}

func typeParamString(params []TypeParam) string {
	if len(params) == 0 {
		return ""
	}
	names := make([]string, len(params))
	for i, param := range params {
		names[i] = param.Name
	}
	return "<" + strings.Join(names, ", ") + ">"
}

func bindingString(bindings []Binding) string {
	names := make([]string, len(bindings))
	for i, binding := range bindings {
		names[i] = binding.Name
	}
	return strings.Join(names, ", ")
}

func exportMark(exported bool) string {
	if exported {
		return "export "
	}
	return ""
}

func pointerMark(pointer bool) string {
	if pointer {
		return "*"
	}
	return ""
}

func varKeyword(isConst bool) string {
	if isConst {
		return "Const"
	}
	return "Var"
}

func quoteIfString(lit *BasicLit) string {
	if lit.Kind == token.String || lit.Kind == token.Char {
		return fmt.Sprintf("%q", lit.Value)
	}
	return lit.Value
}
