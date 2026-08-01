package interp

import (
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/token"
	"github.com/gyro-lang/gyro/internal/types"
)

func (f *frame) eval(e ast.Expr, env *env) Value {
	switch e := e.(type) {
	case *ast.Ident:
		return f.evalIdent(e, env)

	case *ast.Path:
		return f.evalPath(e, env)

	case *ast.SelfExpr:
		cell := env.lookup("self")
		if cell == nil {
			f.in.panicf(f.module, e.Offset, "self outside a method")
		}
		return *cell

	case *ast.BasicLit:
		return f.evalLiteral(e)

	case *ast.StringLit:
		var out strings.Builder
		for _, part := range e.Parts {
			if part.Expr != nil {
				out.WriteString(f.in.display(f.eval(part.Expr, env)))
			} else {
				out.WriteString(part.Text)
			}
		}
		return StringVal(out.String())

	case *ast.ArrayLit:
		elems := make([]Value, len(e.Elements))
		for i, element := range e.Elements {
			elems[i] = copyValue(f.eval(element, env))
		}
		return ArrayVal{Elems: elems}

	case *ast.TupleLit:
		elems := make(TupleVal, len(e.Elements))
		for i, element := range e.Elements {
			elems[i] = copyValue(f.eval(element, env))
		}
		return elems

	case *ast.StructLit:
		return f.evalStructLit(e, env)

	case *ast.UnaryExpr:
		return f.evalUnary(e, env)

	case *ast.BinaryExpr:
		return f.evalBinary(e, env)

	case *ast.CallExpr:
		return f.prepareCall(e, env)()

	case *ast.SelectorExpr:
		return f.evalSelector(e, env)

	case *ast.TupleIndexExpr:
		tuple := f.eval(e.X, env).(TupleVal)
		return tuple[e.Index]

	case *ast.IndexExpr:
		return *f.indexCell(e, env)

	case *ast.MatchExpr:
		return f.evalMatch(e, env)

	case *ast.BlockExpr:
		out := f.execBlock(e.Block, env)
		if out.kind != flowNormal {
			panic(flowEscape{f: out})
		}
		return VoidVal{}

	case *ast.AwaitExpr:
		future, ok := f.eval(e.X, env).(*FutureVal)
		if !ok {
			f.in.panicf(f.module, e.Offset, "await needs an async value")
		}
		return future.await()

	case *ast.RecvExpr:
		return f.evalRecv(e, env)

	case *ast.ChanLit:
		capacity := 0
		if e.Cap != nil {
			capacity = int(toIndex(f.eval(e.Cap, env)))
		}
		return ChanVal(make(chan Value, capacity))

	case *ast.ClosureExpr:
		return f.evalClosure(e, env)

	case *ast.CastExpr:
		return f.evalCast(e, env)

	case *ast.RangeExpr:
		f.in.panicf(f.module, e.Offset, "a range is not a value")
	}
	f.in.panicf(f.module, e.Pos(), "unsupported expression")
	return nil
}

// evalRecv is a plain receive: the element, or an uninitialized value from
// a closed channel — a closed channel yields no value, not a zero value.
func (f *frame) evalRecv(e *ast.RecvExpr, env *env) Value {
	value, _ := f.recvPair(e, env)
	return value
}

// recvPair receives and reports whether the channel was still open, for
// the two-name binding form.
func (f *frame) recvPair(e *ast.RecvExpr, env *env) (Value, bool) {
	channel := f.channelOperand(e.Chan, e.Offset, env)
	value, open := <-channel
	if !open {
		value = UninitVal{}
	}
	return value, open
}

func (f *frame) evalLiteral(e *ast.BasicLit) Value {
	switch e.Kind {
	case token.Int:
		return parseIntLiteral(e.Value)
	case token.Float:
		return parseFloatLiteral(e.Value)
	case token.String:
		return StringVal(e.Value)
	case token.Char:
		runes := []rune(e.Value)
		if len(runes) == 0 {
			return IntVal{Kind: types.RuneKind}
		}
		return IntVal{V: int64(runes[0]), Kind: types.RuneKind}
	case token.True:
		return BoolVal(true)
	case token.False:
		return BoolVal(false)
	case token.Null:
		return NullVal{}
	}
	return UninitVal{}
}

// ---------------------------------------------------------------------------
// Names, cells, reads
// ---------------------------------------------------------------------------

func (f *frame) evalIdent(e *ast.Ident, env *env) Value {
	cell := f.identCell(e, env)
	value := *cell
	switch value.(type) {
	case UninitVal:
		f.in.panicf(f.module, e.Offset, "use of uninitialized value %s", e.Name)
	case FreedVal:
		f.in.panicf(f.module, e.Offset, "use after free of %s", e.Name)
	}
	return value
}

func (f *frame) identCell(e *ast.Ident, env *env) *Value {
	if cell := env.lookup(e.Name); cell != nil {
		return cell
	}
	entry := f.module.lookupBinding(e.Name)
	if entry == nil {
		f.in.panicf(f.module, e.Offset, "undefined name %s", e.Name)
	}
	if entry.cell != nil {
		return entry.cell
	}
	if entry.fn != nil {
		return cellOf(f.closureFor(entry.fn))
	}
	f.in.panicf(f.module, e.Offset, "%s is not a value", e.Name)
	return nil
}

func (f *frame) closureFor(def *funcDef) Value {
	return &ClosureVal{Def: def, Module: def.module, Async: def.decl != nil && def.decl.Async}
}

// evalCell resolves an assignable place to its storage cell.
func (f *frame) evalCell(e ast.Expr, env *env) *Value {
	switch e := e.(type) {
	case *ast.Ident:
		return f.identCell(e, env)

	case *ast.SelectorExpr:
		return f.fieldCell(e, env)

	case *ast.IndexExpr:
		return f.indexCell(e, env)

	case *ast.TupleIndexExpr:
		tuple := f.eval(e.X, env).(TupleVal)
		return &tuple[e.Index]

	case *ast.UnaryExpr:
		if e.Op == token.Star {
			pointer := f.pointerOperand(e.X, e.Offset, env)
			return pointer.Cell
		}
	}
	f.in.panicf(f.module, e.Pos(), "not an assignable place")
	return nil
}

func (f *frame) pointerOperand(e ast.Expr, offset int, env *env) PointerVal {
	value := f.eval(e, env)
	pointer, ok := value.(PointerVal)
	if !ok {
		f.in.panicf(f.module, offset, "dereference of a non-pointer")
	}
	if _, freed := (*pointer.Cell).(FreedVal); freed {
		f.in.panicf(f.module, offset, "use after free")
	}
	return pointer
}

// fieldCell finds the storage of base.name, auto-dereferencing a pointer
// base. Fields alias their struct through the shared slice backing.
func (f *frame) fieldCell(e *ast.SelectorExpr, env *env) *Value {
	base := f.baseForField(e, env)

	strct, ok := base.(StructVal)
	if !ok {
		f.in.panicf(f.module, e.Offset, "no field %s here", e.Name)
	}
	index := strct.Def.fieldIndex(e.Name)
	if index < 0 {
		f.in.panicf(f.module, e.Offset, "%s has no field %s", strct.Def.name, e.Name)
	}
	return &strct.Fields[index]
}

// baseForField evaluates the operand of a field access, keeping the
// aliasing: through a name it reads the stored value (whose Fields slice
// is shared), through a pointer it follows the cell.
func (f *frame) baseForField(e *ast.SelectorExpr, env *env) Value {
	base := f.eval(e.X, env)
	if pointer, ok := base.(PointerVal); ok {
		if _, freed := (*pointer.Cell).(FreedVal); freed {
			f.in.panicf(f.module, e.Offset, "use after free")
		}
		return *pointer.Cell
	}
	if isNull(base) {
		f.in.panicf(f.module, e.Offset, "null dereference")
	}
	return base
}

func (d *typeDef) fieldIndex(name string) int {
	if d.structDecl == nil {
		return -1
	}
	for i, field := range d.structDecl.Fields {
		if field.Name == name {
			return i
		}
	}
	return -1
}

func (f *frame) indexCell(e *ast.IndexExpr, env *env) *Value {
	base := f.eval(e.X, env)
	index := toIndex(f.eval(e.Index, env))

	var elems []Value
	switch base := base.(type) {
	case SliceVal:
		elems = base.Elems
	case ArrayVal:
		elems = base.Elems
	default:
		f.in.panicf(f.module, e.Offset, "value cannot be indexed")
	}

	if index < 0 || index >= int64(len(elems)) {
		f.in.panicf(f.module, e.Offset, "index %d out of range for length %d", index, len(elems))
	}
	return &elems[index]
}

// evalSelector reads a field. Method values do not exist; calls resolve
// methods through prepareCall.
func (f *frame) evalSelector(e *ast.SelectorExpr, env *env) Value {
	cell := f.fieldCell(e, env)
	if _, ok := (*cell).(UninitVal); ok {
		f.in.panicf(f.module, e.Offset, "use of uninitialized field %s", e.Name)
	}
	return *cell
}

// ---------------------------------------------------------------------------
// Literals of composite types
// ---------------------------------------------------------------------------

func (f *frame) evalStructLit(e *ast.StructLit, env *env) Value {
	def := f.resolveTypeDef(e.Type)
	if def == nil || def.structDecl == nil {
		f.in.panicf(f.module, e.Pos(), "not a struct type")
	}
	decl := def.structDecl

	fields := make([]Value, len(decl.Fields))
	for i := range fields {
		fields[i] = UninitVal{}
	}

	for i, field := range e.Fields {
		value := copyValue(f.eval(field.Value, env))
		index := i
		if !e.Positional {
			index = def.fieldIndex(field.Name)
		}
		if index >= 0 && index < len(fields) {
			fields[index] = f.coerceToAst(value, decl.Fields[index].Type)
		}
	}
	return StructVal{Def: def, Fields: fields}
}

// resolveTypeDef resolves a type expression to a struct or enum
// declaration, looking through namespaces; generic arguments are erased at
// runtime.
func (f *frame) resolveTypeDef(t ast.Type) *typeDef {
	named, ok := t.(*ast.NamedType)
	if !ok {
		return nil
	}
	segments := named.Path.Segments

	entry := f.module.lookupBinding(segments[0].Name)
	if entry == nil {
		return nil
	}
	if entry.ns != nil && len(segments) > 1 {
		if member, ok := entry.ns.exports[segments[1].Name]; ok {
			return member.typ
		}
		return nil
	}
	return entry.typ
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

func (f *frame) evalUnary(e *ast.UnaryExpr, env *env) Value {
	switch e.Op {
	case token.Amp:
		return PointerVal{Cell: f.evalCell(e.X, env)}
	case token.Star:
		pointer := f.pointerOperand(e.X, e.Offset, env)
		value := *pointer.Cell
		if _, ok := value.(UninitVal); ok {
			f.in.panicf(f.module, e.Offset, "use of uninitialized value")
		}
		return value
	}

	operand := f.eval(e.X, env)
	switch e.Op {
	case token.Minus:
		return f.negate(operand, e.Offset)
	case token.Bang:
		return BoolVal(!truthy(operand))
	case token.Tilde:
		return f.bitNot(operand, e.Offset)
	}
	f.in.panicf(f.module, e.Offset, "unsupported operator")
	return nil
}

func (f *frame) evalBinary(e *ast.BinaryExpr, env *env) Value {
	switch e.Op {
	case token.AndAnd:
		if !truthy(f.eval(e.X, env)) {
			return BoolVal(false)
		}
		return BoolVal(truthy(f.eval(e.Y, env)))
	case token.OrOr:
		if truthy(f.eval(e.X, env)) {
			return BoolVal(true)
		}
		return BoolVal(truthy(f.eval(e.Y, env)))
	}

	left := f.eval(e.X, env)
	right := f.eval(e.Y, env)
	return f.binaryValues(e.Op, left, right, e.Offset)
}

// ---------------------------------------------------------------------------
// Match
// ---------------------------------------------------------------------------

func (f *frame) evalMatch(e *ast.MatchExpr, env *env) Value {
	subject := f.eval(e.Subject, env)

	for _, arm := range e.Arms {
		scope := newEnv(env)
		if !f.patternMatches(arm.Pattern, subject, scope, env) {
			continue
		}

		if exprStmt, ok := arm.Body.(*ast.ExprStmt); ok {
			return f.eval(exprStmt.X, scope)
		}
		out := f.exec(arm.Body, scope)
		if out.kind != flowNormal {
			panic(flowEscape{f: out})
		}
		return VoidVal{}
	}

	f.in.panicf(f.module, e.Offset, "no match arm applies to %s", f.in.repr(subject))
	return nil
}

func (f *frame) patternMatches(p ast.Pattern, subject Value, scope *env, outer *env) bool {
	switch p := p.(type) {
	case *ast.WildcardPattern:
		return true

	case *ast.LiteralPattern:
		return valuesEqual(subject, f.eval(p.Value, outer))

	case *ast.BindingPattern:
		scope.bind(p.Name, copyValue(subject))
		return true

	case *ast.VariantPattern:
		return f.variantMatches(p, subject, scope)
	}
	return false
}

func (f *frame) variantMatches(p *ast.VariantPattern, subject Value, scope *env) bool {
	names := p.Path.Names()

	// Enum destructuring: the last two segments are Enum::Variant.
	if enum, ok := subject.(EnumVal); ok && len(names) >= 2 {
		if names[len(names)-1] != enum.Variant || names[len(names)-2] != enum.Def.name {
			return false
		}
		for i, payload := range p.Payload {
			if i >= len(enum.Payload) {
				return false
			}
			if !f.patternMatches(payload, enum.Payload[i], scope, scope) {
				return false
			}
		}
		return true
	}

	// Type test: a single name against a value whose concrete type is
	// dynamic — an error slot.
	if strct, ok := subject.(StructVal); ok && len(names) == 1 {
		if strct.Def.name != names[0] {
			return false
		}
		if len(p.Payload) == 1 {
			return f.patternMatches(p.Payload[0], subject, scope, scope)
		}
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Closures and casts
// ---------------------------------------------------------------------------

// evalClosure snapshots the locals: a closure captures by copy, so it sees
// the values as they are now and nothing that happens to them later.
func (f *frame) evalClosure(e *ast.ClosureExpr, env *env) Value {
	captured := newEnv(nil)
	flattenCopy(env, captured)
	return &ClosureVal{Decl: e, Env: captured, Module: f.module, Async: e.Async}
}

// flattenCopy copies the whole visible chain, outer frames first so inner
// shadows overwrite them.
func flattenCopy(from *env, into *env) {
	if from == nil {
		return
	}
	flattenCopy(from.parent, into)
	for name, cell := range from.names {
		into.bind(name, copyValue(*cell))
	}
}

func (f *frame) evalCast(e *ast.CastExpr, env *env) Value {
	value := f.eval(e.X, env)
	named, ok := e.Type.(*ast.NamedType)
	if !ok || !named.Path.IsPlainName() {
		return value
	}
	basic, ok := types.Builtin[named.Path.Segments[0].Name]
	if !ok {
		return value
	}
	return f.convert(value, basic.Kind, e.Offset)
}
