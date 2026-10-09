// Package ast defines the Gyro syntax tree.
//
// Every node carries the byte offset of its first token. Offsets are
// resolved to line/column pairs through the diag.File that produced them,
// so nodes stay small and position handling lives in one place.
package ast

import "github.com/gyro-lang/gyro/internal/token"

// Node is implemented by every syntax tree node.
type Node interface {
	// Pos returns the byte offset of the node's first token.
	Pos() int
}

// Decl is a top-level or associated declaration.
type Decl interface {
	Node
	declNode()
}

// Stmt is a statement.
type Stmt interface {
	Node
	stmtNode()
}

// Expr is an expression.
type Expr interface {
	Node
	exprNode()
}

// Type is a type expression.
type Type interface {
	Node
	typeNode()
}

// ---------------------------------------------------------------------------
// File
// ---------------------------------------------------------------------------

// File is one parsed source file.
type File struct {
	Name    string
	Imports []*Import
	Decls   []Decl
}

func (f *File) Pos() int { return 0 }

// Import is one of:
//
//	import { a, b as c } from "path"
//	import * as name from "path"
//	import "path"
//
// There is no default-import form, because there are no default exports: a
// name is spelled the same in the module that defines it and in every module
// that uses it.
type Import struct {
	Offset int
	// Names lists the bound names for a braced import. It is empty for the
	// other forms.
	Names []ImportName
	// Wildcard is the local name for `import * as name`, or "" otherwise.
	Wildcard string
	Path     string
}

// ImportName is a single `name` or `name as alias` in a braced import.
type ImportName struct {
	Offset int
	Name   string
	Alias  string
}

func (i *Import) Pos() int { return i.Offset }

// ---------------------------------------------------------------------------
// Declarations
// ---------------------------------------------------------------------------

// ExportDecl is a standalone export list rather than an `export` modifier on
// a declaration:
//
//	export { a, b as c }
//	export { a } from "./other"
//	export * from "./other"
//
// Visibility in Gyro is always explicit. A declaration is module-private
// unless it is exported, and nothing about a name's spelling changes that.
type ExportDecl struct {
	Offset int
	Names  []ImportName
	// Star marks `export * from "path"`.
	Star bool
	// Path is the re-export source, or "" when exporting local names.
	Path string
}

func (e *ExportDecl) Pos() int  { return e.Offset }
func (e *ExportDecl) declNode() {}

// Decorator is `@name` or `@name(args)` attached to a declaration.
type Decorator struct {
	Offset int
	Name   *Path
	Args   []Arg
	// HasParens distinguishes `@gpu` from `@gpu()`.
	HasParens bool
}

func (d *Decorator) Pos() int { return d.Offset }

// FindDecorator returns the decorator spelled `@name`, or nil.
func FindDecorator(list []*Decorator, name string) *Decorator {
	for _, decorator := range list {
		if decorator.Name.IsPlainName() && decorator.Name.Segments[0].Name == name {
			return decorator
		}
	}
	return nil
}

// Arg is a call argument, optionally named as in `@concurrent(threads = 8)`.
type Arg struct {
	Offset int
	Name   string
	Value  Expr
	// Spread marks the trailing `...` that forwards an existing slice as a
	// whole variadic pack rather than as one element of it.
	Spread bool
}

// EffectRow is the `!{ io, &xs }` annotation on a signature.
//
// A declared row is a complete statement: sema checks the body against the
// concrete effects it names and against its borrow entries. An absent row
// means "infer", which is the normal case for module-private functions.
type EffectRow struct {
	Offset int
	// Names are the concrete effects listed in the row.
	Names []string
	// Borrows are the parameters the result is a view into, written `&xs`.
	// Empty means the result views nothing, which is what makes returning a
	// slice of a local an error.
	Borrows []Borrow
	// Var is the trailing row variable from `!e` or `!{ io, ..e }`, which is
	// how an effect-polymorphic signature says "whatever the callee does".
	Var string
}

// Borrow is one `&name` entry in an effect row.
type Borrow struct {
	Offset int
	Name   string
}

// FuncDecl is a function, or a method when Owner names the type it belongs
// to: `fn Stack::push(*self, v: i32)`.
type FuncDecl struct {
	Offset     int
	Decorators []*Decorator
	Exported   bool
	// Async marks an `async fn`, whose calls produce a value that must be
	// awaited.
	Async bool
	// Owner is the type qualifier of a method — `Stack` in `fn Stack::push`.
	// It is "" for a free function.
	Owner string
	// OwnerParams holds the owning type's parameters, as in `fn Pair<T>::swap`.
	OwnerParams []TypeParam
	Name        string
	// TypeParams holds the function's own generics, the `<T>` in `fn max<T>`.
	TypeParams []TypeParam
	// Recv is the `self` receiver of a method, nil for associated and free
	// functions.
	Recv   *Receiver
	Params []Param
	// Results is the return type list. A single type is the common case;
	// multiple types are the `(T, error?)` tuple form used for error
	// returns. An empty list means the function returns nothing.
	Results []Type
	// Effects is the declared effect row, or nil when the row is left to be
	// inferred.
	Effects *EffectRow
	Body    *Block
}

func (f *FuncDecl) Pos() int  { return f.Offset }
func (f *FuncDecl) declNode() {}

// Receiver is the `self` or `*self` parameter of a method.
type Receiver struct {
	Offset int
	// Pointer is true for `*self`, meaning the method may mutate its type.
	Pointer bool
}

// Param is one declared function parameter.
type Param struct {
	Offset int
	Name   string
	Type   Type
	// Default is the expression after `=`, or nil.
	Default Expr
	// Variadic marks `name: ...T`, which collects the remaining arguments.
	// Type is the element type T; the parameter itself is a T[] slice.
	Variadic bool
}

// TypeParam is one generic parameter. It carries no bounds: Gyro generics
// are duck-typed, so the body's usage is the requirement and it is checked
// at instantiation.
type TypeParam struct {
	Offset int
	Name   string
}

// StructDecl declares an aggregate type. Structs hold data only; methods are
// free-standing FuncDecls whose Owner names the struct.
type StructDecl struct {
	Offset     int
	Decorators []*Decorator
	Exported   bool
	Name       string
	TypeParams []TypeParam
	Fields     []Field
}

func (s *StructDecl) Pos() int  { return s.Offset }
func (s *StructDecl) declNode() {}

// UnionDecl declares one storage with several names for it. Every member
// begins where the union does, so writing one unwrites the others, and
// nothing records which of them is live — that is C's union, and it is
// what Gyro declares one for.
//
// It is a separate node rather than a flag on StructDecl because the
// difference is not where the members sit but what a read means, and a
// pass handed a StructDecl would have no reason to ask.
type UnionDecl struct {
	Offset     int
	Decorators []*Decorator
	Exported   bool
	Name       string
	Members    []Field
}

func (u *UnionDecl) Pos() int  { return u.Offset }
func (u *UnionDecl) declNode() {}

// Field is one struct field, or one union member.
type Field struct {
	Offset   int
	Exported bool
	Name     string
	Type     Type
}

// EnumDecl declares a sum type. Variants may carry positional payloads, as in
// `Circle(f64)` and `Rect(f64, f64)`.
type EnumDecl struct {
	Offset     int
	Decorators []*Decorator
	Exported   bool
	Name       string
	TypeParams []TypeParam
	Variants   []EnumVariant
}

func (e *EnumDecl) Pos() int  { return e.Offset }
func (e *EnumDecl) declNode() {}

// EnumVariant is one case of an enum.
type EnumVariant struct {
	Offset  int
	Name    string
	Payload []Type
}

// VarDecl is a `var` or `const` binding. It appears both at file scope and
// as a statement, so it satisfies both Decl and Stmt.
type VarDecl struct {
	Offset     int
	Decorators []*Decorator
	Exported   bool
	// Const marks a `const` binding, which must have a compile-time value.
	Const bool
	// Names holds the bound names. More than one appears in the
	// destructuring form used for error returns: `var value, err = f()`.
	Names []Binding
	// Type is the shared annotation, or nil when the type is inferred.
	Type  Type
	Value Expr
}

func (v *VarDecl) Pos() int  { return v.Offset }
func (v *VarDecl) declNode() {}
func (v *VarDecl) stmtNode() {}

// Binding is one name on the left of a var declaration. Blank is true for
// the `_` discard, which suppresses the unused-value check.
type Binding struct {
	Offset int
	Name   string
	Blank  bool
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

// Block is a braced statement list.
type Block struct {
	Offset int
	Stmts  []Stmt
}

func (b *Block) Pos() int  { return b.Offset }
func (b *Block) stmtNode() {}

// ExprStmt is an expression evaluated for its effect.
type ExprStmt struct {
	X Expr
}

func (e *ExprStmt) Pos() int  { return e.X.Pos() }
func (e *ExprStmt) stmtNode() {}

// AssignStmt is `lhs = rhs` or a compound assignment such as `lhs += rhs`.
// Op is token.Assign for a plain assignment.
type AssignStmt struct {
	Offset int
	Op     token.Kind
	// Lhs may hold several targets for the `value, err = f()` form.
	Lhs []Expr
	Rhs Expr
}

func (a *AssignStmt) Pos() int  { return a.Offset }
func (a *AssignStmt) stmtNode() {}

// ReturnStmt returns zero or more values.
type ReturnStmt struct {
	Offset  int
	Results []Expr
}

func (r *ReturnStmt) Pos() int  { return r.Offset }
func (r *ReturnStmt) stmtNode() {}

// IfStmt is `if cond { ... }` with an optional else branch. `elif` is parsed
// into a nested IfStmt in Else, so it is sugar and leaves no trace here.
type IfStmt struct {
	Offset int
	Cond   Expr
	Then   *Block
	// Else is a *Block or a nested *IfStmt, or nil.
	Else Stmt
}

func (i *IfStmt) Pos() int  { return i.Offset }
func (i *IfStmt) stmtNode() {}

// WhileStmt is `while cond { ... }`.
type WhileStmt struct {
	Offset int
	Cond   Expr
	Body   *Block
}

func (w *WhileStmt) Pos() int  { return w.Offset }
func (w *WhileStmt) stmtNode() {}

// ForStmt is the C-style `for init; cond; post { ... }`. Any of the three
// clauses may be nil.
type ForStmt struct {
	Offset int
	Init   Stmt
	Cond   Expr
	Post   Stmt
	Body   *Block
}

func (f *ForStmt) Pos() int  { return f.Offset }
func (f *ForStmt) stmtNode() {}

// ForInStmt is `for name in iterable { ... }`.
type ForInStmt struct {
	Offset   int
	Name     string
	Iterable Expr
	Body     *Block
}

func (f *ForInStmt) Pos() int  { return f.Offset }
func (f *ForInStmt) stmtNode() {}

// BranchStmt is `break` or `continue`.
type BranchStmt struct {
	Offset int
	Kind   token.Kind
}

func (b *BranchStmt) Pos() int  { return b.Offset }
func (b *BranchStmt) stmtNode() {}

// DeferStmt schedules a call to run when the enclosing block exits.
// Deferred calls run in reverse order of registration.
type DeferStmt struct {
	Offset int
	Call   Expr
}

func (d *DeferStmt) Pos() int  { return d.Offset }
func (d *DeferStmt) stmtNode() {}

// SendStmt is `ch <- value`. Sending is always a statement, never an
// expression, which is what lets the parser tell `ch <- v` apart from the
// comparison `x < -1`.
type SendStmt struct {
	Offset int
	Chan   Expr
	Value  Expr
}

func (s *SendStmt) Pos() int  { return s.Offset }
func (s *SendStmt) stmtNode() {}

// SelectStmt waits on several channel operations and runs the arm whose
// operation becomes ready first. Arms use the same `comm: body` shape as
// match, and bindings are ordinary `var` declarations rather than a special
// short form.
type SelectStmt struct {
	Offset int
	Arms   []SelectArm
	// Else is the arm taken when no operation is ready, making the select
	// non-blocking. It is nil when the select should block.
	Else Stmt
}

// SelectArm is one `comm: body` case. Comm is a *SendStmt, a *VarDecl or an
// *AssignStmt whose value is a receive, or an *ExprStmt wrapping a bare
// receive.
type SelectArm struct {
	Offset int
	Comm   Stmt
	Body   Stmt
}

func (s *SelectStmt) Pos() int  { return s.Offset }
func (s *SelectStmt) stmtNode() {}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

// Ident is a bare name.
type Ident struct {
	Offset int
	Name   string
}

func (i *Ident) Pos() int  { return i.Offset }
func (i *Ident) exprNode() {}

// Path is a `::`-separated namespace path such as `ast::Literal`. A path
// with a single unparameterized segment is equivalent to an Ident.
//
// Any segment may carry type arguments, since both the namespace and the
// final name can be generic: `Array<i32>::new` parameterizes the first,
// `ast::cast<i32>` the last.
type Path struct {
	Offset   int
	Segments []PathSegment
}

// PathSegment is one `name` or `name<A, B>` component of a path.
type PathSegment struct {
	Offset int
	Name   string
	Args   []Type
}

func (p *Path) Pos() int  { return p.Offset }
func (p *Path) exprNode() {}

// Names returns the segment names without their type arguments.
func (p *Path) Names() []string {
	names := make([]string, len(p.Segments))
	for i, segment := range p.Segments {
		names[i] = segment.Name
	}
	return names
}

// IsPlainName reports whether the path is a single segment with no type
// arguments, meaning it can be treated as an ordinary identifier.
func (p *Path) IsPlainName() bool {
	return len(p.Segments) == 1 && len(p.Segments[0].Args) == 0
}

// SelfExpr is the `self` receiver inside a method.
type SelfExpr struct {
	Offset int
}

func (s *SelfExpr) Pos() int  { return s.Offset }
func (s *SelfExpr) exprNode() {}

// BasicLit is an int, float, string, char, bool or null literal.
type BasicLit struct {
	Offset int
	Kind   token.Kind
	// Value is the decoded value: escapes resolved for strings and chars,
	// digit separators stripped for numbers.
	Value string
}

func (b *BasicLit) Pos() int  { return b.Offset }
func (b *BasicLit) exprNode() {}

// StringLit is an interpolated string. Parts alternates literal text and
// embedded expressions; a plain string parses to BasicLit instead.
type StringLit struct {
	Offset int
	Parts  []StringPart
}

// StringPart is either literal text or an interpolated expression.
type StringPart struct {
	Offset int
	// Text is set when Expr is nil.
	Text string
	Expr Expr
}

func (s *StringLit) Pos() int  { return s.Offset }
func (s *StringLit) exprNode() {}

// ArrayLit is `[a, b, c]`.
type ArrayLit struct {
	Offset   int
	Elements []Expr
}

func (a *ArrayLit) Pos() int  { return a.Offset }
func (a *ArrayLit) exprNode() {}

// StructLit is `Point{ x: 1, y: 2 }` or, positionally, `Mutex{ 0 }`. A
// literal is either all-named or all-positional; mixing the two is rejected.
type StructLit struct {
	Offset int
	Type   Type
	Fields []FieldValue
	// Positional marks the `Mutex{ 0 }` form, where fields are matched to
	// the declaration order instead of by name.
	Positional bool
}

// FieldValue is one field in a struct literal. Name is empty in the
// positional form.
type FieldValue struct {
	Offset int
	Name   string
	Value  Expr
}

func (s *StructLit) Pos() int  { return s.Offset }
func (s *StructLit) exprNode() {}

// UnaryExpr is a prefix operator: `-x`, `!x`, `~x`, `&x` (address-of) and
// `*x` (dereference).
type UnaryExpr struct {
	Offset int
	Op     token.Kind
	X      Expr
}

func (u *UnaryExpr) Pos() int  { return u.Offset }
func (u *UnaryExpr) exprNode() {}

// BinaryExpr is an infix operator application.
type BinaryExpr struct {
	Offset int
	Op     token.Kind
	X, Y   Expr
}

func (b *BinaryExpr) Pos() int  { return b.Offset }
func (b *BinaryExpr) exprNode() {}

// CallExpr is `f(a, b)`. TypeArgs holds the angle-bracketed arguments in
// `max<i32>(3, 7)` and the launch configuration in `VectorAdd<1, 8>(...)`,
// so an element is either a type or a constant expression.
type CallExpr struct {
	Offset   int
	Fun      Expr
	TypeArgs []TypeArg
	Args     []Arg
}

// TypeArg is one angle-bracketed argument. Exactly one field is set.
type TypeArg struct {
	Type Type
	Expr Expr
}

func (c *CallExpr) Pos() int  { return c.Offset }
func (c *CallExpr) exprNode() {}

// SelectorExpr is `x.name` field or method access.
type SelectorExpr struct {
	Offset int
	X      Expr
	Name   string
	// NameOffset is where the member name starts, so a diagnostic about it
	// underlines the name rather than the dot.
	NameOffset int
}

func (s *SelectorExpr) Pos() int  { return s.Offset }
func (s *SelectorExpr) exprNode() {}

// IndexExpr is `x[i]`.
type IndexExpr struct {
	Offset int
	X      Expr
	Index  Expr
}

func (i *IndexExpr) Pos() int  { return i.Offset }
func (i *IndexExpr) exprNode() {}

// RangeExpr is the half-open `lo..hi`, used by `for x in 0..10` and by
// slice narrowing, `xs[lo..hi]`.
//
// **Only in an index position may an end be absent**, and only one of
// them: `xs[lo..]` runs to the length and `xs[..hi]` starts at zero, so
// the missing end is a bound the container already knows. A `for` header
// always has both, because a bare `..` there names no sequence — which
// is why a nil here is not a general hole in the node but a shape the
// index parser is the only producer of.
type RangeExpr struct {
	Offset int
	Lo, Hi Expr
}

func (r *RangeExpr) Pos() int  { return r.Offset }
func (r *RangeExpr) exprNode() {}

// TupleLit is `(a, b)`. One-element tuples do not exist, so a parenthesized
// single expression stays plain grouping and never reaches this node.
type TupleLit struct {
	Offset   int
	Elements []Expr
}

func (t *TupleLit) Pos() int  { return t.Offset }
func (t *TupleLit) exprNode() {}

// TupleIndexExpr is positional tuple access: `pair.0`.
type TupleIndexExpr struct {
	Offset int
	X      Expr
	Index  int
}

func (t *TupleIndexExpr) Pos() int  { return t.Offset }
func (t *TupleIndexExpr) exprNode() {}

// MatchExpr is `match subject { pattern: expr, ... }`. It is an expression,
// so it can be assigned; using it as a statement is also allowed.
type MatchExpr struct {
	Offset  int
	Subject Expr
	Arms    []MatchArm
}

// MatchArm is one `pattern: body` case.
type MatchArm struct {
	Offset  int
	Pattern Pattern
	// Body is the arm's value. It is a statement rather than an expression
	// so that an arm can `return`, `break` or open a block; an ordinary
	// value arm is an ExprStmt whose expression is the match's result.
	Body Stmt
}

func (m *MatchExpr) Pos() int  { return m.Offset }
func (m *MatchExpr) exprNode() {}

// BlockExpr lets a braced block appear where an expression is expected,
// which is how a match arm with several statements is represented.
type BlockExpr struct {
	Block *Block
}

func (b *BlockExpr) Pos() int  { return b.Block.Pos() }
func (b *BlockExpr) exprNode() {}

// AwaitExpr is `await x`. It binds like the other prefix operators, so
// `await f().field` awaits the whole selector and `(await f()).field` needs
// its parentheses.
type AwaitExpr struct {
	Offset int
	X      Expr
}

func (a *AwaitExpr) Pos() int  { return a.Offset }
func (a *AwaitExpr) exprNode() {}

// RecvExpr is the receive operator `<-ch`. Paired with a two-name binding it
// also reports whether the channel is still open: `var value, ok = <-ch`.
type RecvExpr struct {
	Offset int
	Chan   Expr
}

func (r *RecvExpr) Pos() int  { return r.Offset }
func (r *RecvExpr) exprNode() {}

// ChanLit constructs a channel: `chan<i32>(64)`. An absent capacity means an
// unbuffered channel.
type ChanLit struct {
	Offset int
	Elem   Type
	Cap    Expr
}

func (c *ChanLit) Pos() int  { return c.Offset }
func (c *ChanLit) exprNode() {}

// Capture is one `move name` entry in a closure's capture list. Copies are
// implicit and never listed, so every entry transfers ownership.
type Capture struct {
	Offset int
	Name   string
}

// ClosureExpr is an anonymous function: `fn(a: i32): i32 { return a * 2 }`.
//
// It is spelled with the same `fn` and `:` as a declaration, so there is one
// way to write a function and one return-type marker. Parameter and result
// types may be omitted where the expected type is known from context, and a
// body that is a single expression needs no braces.
//
// A closure copies the locals it uses implicitly. The capture list exists
// only for ownership transfer — `fn[move node]() { free(node) }` — which is
// the one case the leak analysis must see in the source.
type ClosureExpr struct {
	Offset   int
	Async    bool
	Captures []Capture
	// Params may have a nil Type, meaning the type is inferred from context.
	Params []Param
	// Results is empty when the closure returns nothing or when the result
	// type is left to inference.
	Results []Type
	Effects *EffectRow
	// Body is set for a braced closure, BodyExpr for an expression one.
	// Exactly one of the two is non-nil.
	Body     *Block
	BodyExpr Expr
}

func (c *ClosureExpr) Pos() int  { return c.Offset }
func (c *ClosureExpr) exprNode() {}

// CastExpr is `x as T`.
type CastExpr struct {
	Offset int
	X      Expr
	Type   Type
}

func (c *CastExpr) Pos() int  { return c.Offset }
func (c *CastExpr) exprNode() {}

// ---------------------------------------------------------------------------
// Patterns
// ---------------------------------------------------------------------------

// Pattern is a match pattern.
type Pattern interface {
	Node
	patternNode()
}

// WildcardPattern is `_`, matching anything without binding.
type WildcardPattern struct {
	Offset int
}

func (w *WildcardPattern) Pos() int     { return w.Offset }
func (w *WildcardPattern) patternNode() {}

// LiteralPattern matches a constant value.
type LiteralPattern struct {
	Value Expr
}

func (l *LiteralPattern) Pos() int     { return l.Value.Pos() }
func (l *LiteralPattern) patternNode() {}

// BindingPattern binds the subject to a name.
type BindingPattern struct {
	Offset int
	Name   string
}

func (b *BindingPattern) Pos() int     { return b.Offset }
func (b *BindingPattern) patternNode() {}

// VariantPattern is `Name(bindings)` in a pattern. It covers two readings
// that share one surface shape: destructuring an enum variant, as in
// `Shape::Circle(r)`, and a type test on a dynamically typed value, as in
// `RangeError(e)` where the subject is an `error`. Sema tells them apart by
// what the path resolves to.
type VariantPattern struct {
	Offset  int
	Path    *Path
	Payload []Pattern
}

func (v *VariantPattern) Pos() int     { return v.Offset }
func (v *VariantPattern) patternNode() {}

// TypeTestPattern is a type test on an `any` whose type no path can spell:
// `(i32, string)(pair)`, `i32[](xs)`, `*Node(node)`. A type that is a plain
// name stays a VariantPattern, since `Point(p)` and `Shape::Circle(r)` are
// the same syntax until the subject says which one it is.
type TypeTestPattern struct {
	Offset  int
	Type    Type
	Payload []Pattern
}

func (t *TypeTestPattern) Pos() int     { return t.Offset }
func (t *TypeTestPattern) patternNode() {}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// NamedType is a possibly namespaced type name such as `i32`,
// `ast::Literal` or `Result<string, error>`. Type arguments live on the path
// segment that carries them.
type NamedType struct {
	Offset int
	Path   *Path
}

// Args returns the type arguments on the final path segment, which is where
// a type's own parameters appear.
func (n *NamedType) Args() []Type {
	if n.Path == nil || len(n.Path.Segments) == 0 {
		return nil
	}
	return n.Path.Segments[len(n.Path.Segments)-1].Args
}

func (n *NamedType) Pos() int  { return n.Offset }
func (n *NamedType) typeNode() {}

// PointerType is `*T`.
type PointerType struct {
	Offset int
	Elem   Type
}

func (p *PointerType) Pos() int  { return p.Offset }
func (p *PointerType) typeNode() {}

// ArrayType is `T[N]` for a fixed-size array or `T[]` for a slice.
type ArrayType struct {
	Offset int
	Elem   Type
	// Length is nil for a slice.
	Length Expr
}

func (a *ArrayType) Pos() int  { return a.Offset }
func (a *ArrayType) typeNode() {}

// TupleType is `(A, B)`, a first-class type usable anywhere a type is:
// fields, variables, channel elements. Multi-value returns are the same
// tuple, spelled without the parentheses.
type TupleType struct {
	Offset   int
	Elements []Type
}

func (t *TupleType) Pos() int  { return t.Offset }
func (t *TupleType) typeNode() {}

// FuncType is `fn(A, B): C`, used for function-valued parameters.
type FuncType struct {
	Offset  int
	Async   bool
	Params  []Type
	Results []Type
	Effects *EffectRow
}

func (f *FuncType) Pos() int  { return f.Offset }
func (f *FuncType) typeNode() {}

// CodeType is `code(A, B): C`: the address of a function and nothing
// beside it, which is what a C function pointer is. `code` is a
// contextual identifier — only a `(` after it in a type position makes
// this — so a program may still call a variable `code`.
//
// It carries no `async` and no effect row. Neither would be a claim
// anyone checked: what is at the address may have been compiled by
// another language, and an address does not remember its effects.
type CodeType struct {
	Offset  int
	Params  []Type
	Results []Type
}

func (c *CodeType) Pos() int  { return c.Offset }
func (c *CodeType) typeNode() {}

// OptionalType is `T?`, the only type that may hold null.
//
// The marker binds to the whole type, so `*Node?` is a nullable pointer. A
// plain `*Node` can never be null, which is what lets the checker reject a
// dereference that has not been guarded.
type OptionalType struct {
	Offset int
	Elem   Type
}

func (o *OptionalType) Pos() int  { return o.Offset }
func (o *OptionalType) typeNode() {}

// ChanType is `chan<T>`. Channels are language-level rather than a stdlib
// generic so that `select` and the send and receive operators can be type
// checked directly.
type ChanType struct {
	Offset int
	Elem   Type
}

func (c *ChanType) Pos() int  { return c.Offset }
func (c *ChanType) typeNode() {}
