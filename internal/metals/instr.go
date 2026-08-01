package metals

// Instr is one METALS instruction. Simple instructions write at most one
// register; the structured ones (If, Block, Loop, Switch, Select) nest.
//
// Registers are referenced by name without the % sigil in these structs;
// the printer and parser add and strip it.
type Instr interface {
	isInstr()
}

// BinOps and UnOps are the operator mnemonics Bin and Un accept, mapped
// to their operand class for verification.
var BinOps = map[string]string{
	"iadd": "int", "isub": "int", "imul": "int", "idiv": "int", "irem": "int",
	"wadd": "int", "wsub": "int", "wmul": "int",
	"fadd": "float", "fsub": "float", "fmul": "float", "fdiv": "float",
	"band": "int", "bor": "int", "bxor": "int", "shl": "int", "shr": "int",
	"eq": "any", "ne": "any",
	"lt": "ordered", "le": "ordered", "gt": "ordered", "ge": "ordered",
}

var UnOps = map[string]string{
	"neg":  "number",
	"bnot": "int",
	"not":  "bool",
}

type (
	// Const is `%r = const.<prim> literal` or `%r = const.null opt<T>`.
	Const struct {
		Dst string
		// Type is the constant's type; for const.null, the optional type.
		Type Type
		// Value is the literal text: "42", "1.5", "true", `"text"`, "'a'".
		// Empty for null.
		Value string
		Null  bool
	}

	// Copy is `%r = copy %x`.
	Copy struct{ Dst, X string }

	// Bin is `%r = <op> %a, %b` for every two-operand mnemonic in BinOps.
	Bin struct {
		Dst, Op, A, B string
	}

	// Un is `%r = <op> %x` for neg, bnot, not.
	Un struct{ Dst, Op, X string }

	// Conv is `%r = conv %x -> T`.
	Conv struct {
		Dst, X string
		To     Type
	}

	// Slot is `%p = slot T`: a stack cell.
	Slot struct {
		Dst  string
		Type Type
	}
	// New is `%p = new T`: an uninitialized heap cell.
	New struct {
		Dst  string
		Type Type
	}
	Free  struct{ X string }
	Load  struct{ Dst, Ptr string }
	Store struct{ Ptr, Val string }
	// FieldPtr is `%q = field.ptr %p, i`: ptr<record> to ptr<field>.
	FieldPtr struct {
		Dst, Ptr string
		Index    int
	}

	// MakeRecord is `%r = record %Name(%a, %b)`, every field in order.
	MakeRecord struct {
		Dst, TypeName string
		Args          []string
	}
	FieldGet struct {
		Dst, X string
		Index  int
	}
	// FieldSet mutates the register's value in place.
	FieldSet struct {
		X     string
		Index int
		Val   string
	}

	// MakeVariant is `%e = variant %Name, i(%payload...)`.
	MakeVariant struct {
		Dst, TypeName string
		Variant       int
		Args          []string
	}
	VariantTag struct{ Dst, X string }
	// VariantField is `%v = variant.field %e, variant, index`.
	VariantField struct {
		Dst, X         string
		Variant, Index int
	}

	MakeTuple struct {
		Dst  string
		Args []string
	}
	TupleGet struct {
		Dst, X string
		Index  int
	}

	// MakeArray is `%a = array<T>(%x, %y)`.
	MakeArray struct {
		Dst  string
		Elem Type
		Args []string
	}
	// Slice is `%s = slice.of %a`: borrow the whole array.
	Slice    struct{ Dst, X string }
	IndexGet struct{ Dst, X, Index string }
	IndexSet struct{ X, Index, Val string }
	Len      struct{ Dst, X string }

	StrConcat struct{ Dst, A, B string }

	// Call is `%r = call @f(%a, %b)`; Dst empty discards a void result.
	Call struct {
		Dst, Callee string
		Args        []string
	}
	FnRef struct{ Dst, Callee string }
	// MakeClosure is `%c = closure @f, %env`: partially applies @f's
	// first parameter.
	MakeClosure  struct{ Dst, Callee, Env string }
	CallIndirect struct {
		Dst, Fn string
		Args    []string
	}

	Return struct{ Val string } // "" for a void return
	Abort  struct{ Message string }

	If struct {
		Cond string
		Then []Instr
		Else []Instr
	}
	Block struct {
		Label string
		Body  []Instr
	}
	Loop struct {
		Label string
		Body  []Instr
	}
	Br     struct{ Label string }
	BrIf   struct{ Cond, Label string }
	Switch struct {
		X       string
		Cases   []SwitchCase
		Default []Instr
	}

	ChanNew struct {
		Dst  string
		Elem Type
		Cap  string // "" for unbuffered
	}
	Send      struct{ Ch, Val string }
	Recv      struct{ Dst, Ch string } // -> tuple<T, bool>
	ChanClose struct{ Ch string }
	Await     struct{ Dst, X string }
	Select    struct {
		Arms    []SelectArm
		Default []Instr
	}

	DynWrap   struct{ Dst, X string }
	DynUnwrap struct {
		Dst, X string
		To     Type
	}
	DynCall struct {
		Dst, X string
		Args   []string
	}
	DynGet struct{ Dst, X, Name string }
	DynSet struct{ X, Name, Val string }
)

type SwitchCase struct {
	Value int
	Body  []Instr
}

// SelectArm is one arm of a select: a receive binding a register, or a
// send. Exactly one of Recv/Send applies: Recv arms set Dst.
type SelectArm struct {
	Dst  string // receive arms: the bound register
	Ch   string
	Val  string // send arms: the sent value
	Send bool
	Body []Instr
}

func (*Const) isInstr()        {}
func (*Copy) isInstr()         {}
func (*Bin) isInstr()          {}
func (*Un) isInstr()           {}
func (*Conv) isInstr()         {}
func (*Slot) isInstr()         {}
func (*New) isInstr()          {}
func (*Free) isInstr()         {}
func (*Load) isInstr()         {}
func (*Store) isInstr()        {}
func (*FieldPtr) isInstr()     {}
func (*MakeRecord) isInstr()   {}
func (*FieldGet) isInstr()     {}
func (*FieldSet) isInstr()     {}
func (*MakeVariant) isInstr()  {}
func (*VariantTag) isInstr()   {}
func (*VariantField) isInstr() {}
func (*MakeTuple) isInstr()    {}
func (*TupleGet) isInstr()     {}
func (*MakeArray) isInstr()    {}
func (*Slice) isInstr()        {}
func (*IndexGet) isInstr()     {}
func (*IndexSet) isInstr()     {}
func (*Len) isInstr()          {}
func (*StrConcat) isInstr()    {}
func (*Call) isInstr()         {}
func (*FnRef) isInstr()        {}
func (*MakeClosure) isInstr()  {}
func (*CallIndirect) isInstr() {}
func (*Return) isInstr()       {}
func (*Abort) isInstr()        {}
func (*If) isInstr()           {}
func (*Block) isInstr()        {}
func (*Loop) isInstr()         {}
func (*Br) isInstr()           {}
func (*BrIf) isInstr()         {}
func (*Switch) isInstr()       {}
func (*ChanNew) isInstr()      {}
func (*Send) isInstr()         {}
func (*Recv) isInstr()         {}
func (*ChanClose) isInstr()    {}
func (*Await) isInstr()        {}
func (*Select) isInstr()       {}
func (*DynWrap) isInstr()      {}
func (*DynUnwrap) isInstr()    {}
func (*DynCall) isInstr()      {}
func (*DynGet) isInstr()       {}
func (*DynSet) isInstr()       {}
