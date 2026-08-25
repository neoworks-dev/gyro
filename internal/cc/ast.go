package cc

// The syntax tree. It carries no types: what an expression's type is
// falls out of lowering it, which is one pass rather than two and is
// enough while there is no diagnostic to report before code is emitted.

type unit struct {
	// structs is in declaration order, which is the order the METALS
	// module declares them in, so a printed module reads like its source.
	structs []*ctype
	globals []*globalDecl
	funcs   []*funcDecl
	// enums maps an enumeration constant to its value. They are not
	// resolved during the parse because a local variable of the same
	// name shadows one, and only lowering knows what is in scope.
	enums map[string]int64
}

type globalDecl struct {
	name string
	typ  *ctype
	init *initializer // nil when the declaration has none
	pos  position
}

// initializer is what follows `=` in a declaration. C's braced form
// writes an object's members rather than producing a value, so it is not
// an expression and does not lower like one; the two forms nest, which
// is what `{ {1, 2}, {3, 4} }` is.
type initializer struct {
	x    expr           // nil for a braced list
	list []*initializer // nil for an expression
	pos  position
}

func (i *initializer) braced() bool { return i.x == nil }

type funcDecl struct {
	name   string
	result *ctype
	params []param
	// body is nil for a prototype, which declares a name this unit may
	// call and another module has to supply.
	body   *blockStmt
	static bool
	pos    position
}

type param struct {
	name string
	typ  *ctype
}

type varDecl struct {
	name string
	typ  *ctype
	init *initializer // nil when the declaration has none
	// static means the name is block-scoped and the storage is not: it
	// is module storage under a name only this block can reach.
	static bool
	pos    position
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

type stmt interface{ isStmt() }

type (
	blockStmt struct{ stmts []stmt }

	// declStmt is one declaration, which in C may introduce more than
	// one name: `int a, *b, c = 3;` is a single statement.
	declStmt struct{ vars []*varDecl }

	exprStmt struct{ x expr }

	ifStmt struct {
		cond expr
		then stmt
		els  stmt // nil without an else
	}

	whileStmt struct {
		cond expr
		body stmt
	}

	doStmt struct {
		body stmt
		cond expr
	}

	forStmt struct {
		init stmt // nil, a declStmt or an exprStmt
		cond expr // nil means the loop runs forever
		post expr
		body stmt
	}

	// switchStmt holds its body flat rather than as a block, because
	// C's case labels are labels and not nesting: `case 2:` after a
	// statement continues the same list, which is what fallthrough is.
	switchStmt struct {
		x    expr
		body []stmt
		pos  position
	}

	// caseStmt and defaultStmt are labels standing in the body list.
	// Consecutive ones with nothing between them label the same group.
	caseStmt struct {
		value int64
		pos   position
	}
	defaultStmt struct{ pos position }

	returnStmt struct {
		x   expr // nil for a bare return
		pos position
	}

	breakStmt    struct{ pos position }
	continueStmt struct{ pos position }
	emptyStmt    struct{}
)

func (*blockStmt) isStmt()    {}
func (*declStmt) isStmt()     {}
func (*exprStmt) isStmt()     {}
func (*ifStmt) isStmt()       {}
func (*whileStmt) isStmt()    {}
func (*doStmt) isStmt()       {}
func (*forStmt) isStmt()      {}
func (*switchStmt) isStmt()   {}
func (*caseStmt) isStmt()     {}
func (*defaultStmt) isStmt()  {}
func (*returnStmt) isStmt()   {}
func (*breakStmt) isStmt()    {}
func (*continueStmt) isStmt() {}
func (*emptyStmt) isStmt()    {}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

type expr interface{ at() position }

type (
	intLit struct {
		val uint64
		typ *ctype
		pos position
	}

	floatLit struct {
		val float64
		typ *ctype
		pos position
	}

	// strLit is a string literal, with the terminator C appends already
	// in text. It is an array of char with static storage, not a value:
	// what an expression sees is that array, which decays to a pointer
	// everywhere except under sizeof and in `char buf[] = "..."`.
	strLit struct {
		text string
		pos  position
	}

	identExpr struct {
		name string
		pos  position
	}

	// callExpr's callee is an expression, not a name: C calls whatever
	// designates a function, and `(*table[i].handler)(x)` designates one
	// just as `f` does.
	callExpr struct {
		callee expr
		args   []expr
		pos    position
	}

	// unaryExpr covers `-`, `+`, `!`, `~`, `*` and `&`.
	unaryExpr struct {
		op  string
		x   expr
		pos position
	}

	// incExpr is ++ and --, prefix or postfix. They are one node because
	// they read, add and write back the same lvalue; only the value the
	// expression produces differs.
	incExpr struct {
		op      string // "++" or "--"
		x       expr
		postfix bool
		pos     position
	}

	binaryExpr struct {
		op   string
		a, b expr
		pos  position
	}

	assignExpr struct {
		// op is "=" or a compound assignment such as "+=".
		op     string
		target expr
		value  expr
		pos    position
	}

	condExpr struct {
		cond, then, els expr
		pos             position
	}

	memberExpr struct {
		x     expr
		name  string
		arrow bool
		pos   position
	}

	castExpr struct {
		typ *ctype
		x   expr
		pos position
	}

	// sizeofExpr is `sizeof(T)` when typ is set and `sizeof x` when x
	// is. The operand of the second form is never evaluated, which is
	// what lets `sizeof *p` be written where p is null.
	sizeofExpr struct {
		typ *ctype
		x   expr
		pos position
	}
)

func (e *intLit) at() position     { return e.pos }
func (e *floatLit) at() position   { return e.pos }
func (e *strLit) at() position     { return e.pos }
func (e *identExpr) at() position  { return e.pos }
func (e *callExpr) at() position   { return e.pos }
func (e *unaryExpr) at() position  { return e.pos }
func (e *incExpr) at() position    { return e.pos }
func (e *binaryExpr) at() position { return e.pos }
func (e *assignExpr) at() position { return e.pos }
func (e *condExpr) at() position   { return e.pos }
func (e *memberExpr) at() position { return e.pos }
func (e *castExpr) at() position   { return e.pos }
func (e *sizeofExpr) at() position { return e.pos }
