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
	"fmin": "float", "fmax": "float", "fcopysign": "float",
	"band": "int", "bor": "int", "bxor": "int", "shl": "int", "shr": "int",
	"eq": "any", "ne": "any",
	"lt": "ordered", "le": "ordered", "gt": "ordered", "ge": "ordered",
}

var UnOps = map[string]string{
	"neg":  "number",
	"bnot": "int",
	"not":  "bool",

	// The float intrinsics. They are instructions rather than runtime
	// imports because every one of them is a single machine instruction
	// or a handful, because a producer that has them as opcodes (wasm
	// does) would otherwise have to call out of the sandbox to round a
	// number, and because Gyro's own math module wants exactly these.
	"fabs": "float", "fsqrt": "float",
	"fceil": "float", "ffloor": "float", "ftrunc": "float", "fnearest": "float",
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

	// Conv is `%r = conv %x -> T`: value-preserving, and it aborts when
	// the value does not fit. This is Gyro's `as`.
	Conv struct {
		Dst, X string
		To     Type
	}

	// Trunc is `%r = trunc %x -> T`: integer-to-integer narrowing that
	// keeps the low bits and never aborts. Conv's wrapping sibling, and
	// what a C cast means.
	Trunc struct {
		Dst, X string
		To     Type
	}

	// Bitcast is `%r = bitcast %x -> T`: reinterpret the same bits as
	// another type of the same width. Never aborts. This is what makes a
	// signedness-per-operation language expressible — u32's `shr` over
	// bits an i32 register holds — and it is also the only way to spell
	// float/integer reinterpretation.
	Bitcast struct {
		Dst, X string
		To     Type
	}

	// Slot is `%p = slot T`: a stack cell.
	Slot struct {
		Dst  string
		Type Type
	}
	// GlobalPtr is `%p = global.ptr @name`: ptr<T> to module storage.
	// Reads and writes go through the ordinary load and store.
	GlobalPtr struct {
		Dst  string
		Name string
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

	// ElemPtr is `%q = elem.ptr %p`: ptr<array<T, N>> to the ptr<T> of
	// its first element. This is C's array-to-pointer decay, and it is
	// separate from ptr.offset because it is the only thing that changes
	// what a pointer points at rather than where.
	ElemPtr struct{ Dst, Ptr string }

	// PtrOffset is `%q = ptr.offset %p, %n`: the ptr<T> n elements along
	// from p, where n is a signed count and may be negative.
	//
	// This is pointer arithmetic, and it is unchecked on purpose. A
	// bounds test would need the object's extent at run time, which
	// means a pointer stops being a machine word — and C's pointers are
	// machine words, which is most of what compiling C in rather than
	// sandboxing it buys. What the format says instead is what C says:
	// the result is a pointer only while it stays inside the object p
	// points into, or one element past its end. The evaluator holds
	// producers to that; the native backends compute the address.
	PtrOffset struct{ Dst, Ptr, N string }

	// PtrDiff is `%n = ptr.diff %a, %b` -> i64: how many elements apart
	// two pointers into one object are. Undefined for pointers into
	// different objects, exactly as in C.
	PtrDiff struct{ Dst, A, B string }

	// PtrErase is `%a = ptr.erase %p`: a pointer with its target's type
	// taken away. It cannot fail and it moves nothing — an `anyptr` is
	// the same address the `ptr<T>` was. Optionality is carried through,
	// so an `opt<ptr<T>>` erases to an `opt<anyptr>` and a null stays a
	// null, because under the niche both are the same machine word and
	// making the producer branch to preserve that would be a lie about
	// what the instruction does.
	PtrErase struct{ Dst, X string }

	// PtrCast is `%p = ptr.cast %a -> ptr<T>`: the type put back. It is
	// the producer's assertion that the object at that address is a T,
	// the way ptr.offset staying inside its object is the producer's
	// assertion — the native backends take it at face value and the
	// evaluator, which knows what the object really is, aborts when the
	// two disagree. `opt<anyptr>` casts to `opt<ptr<T>>` for the reason
	// ptr.erase carries optionality.
	//
	// It is separate from ptr.erase because they answer different
	// questions: erasing is total and restoring is a claim. That is the
	// same division conv, trunc and bitcast already draw.
	PtrCast struct {
		Dst, X string
		To     Type
	}

	// PtrAddr is `%n = ptr.addr %a` -> usize: the address an erased
	// pointer holds, as a number. Like ptr.erase it cannot fail, and an
	// `opt<anyptr>` gives 0 for a null.
	PtrAddr struct{ Dst, X string }

	// PtrFrom is `%a = ptr.from %n -> anyptr`: a number taken as an
	// address. It is the producer's claim that something lives there,
	// the way ptr.cast is the claim about what, and nothing checks it.
	// The operand is a usize and never a linear memory's u32, and a
	// function that reaches a memory may not use it: a linear address is
	// an offset into its memory, not a place in the process (appendix D).
	// `-> opt<anyptr>` makes 0 a null, for C's `(T *)n`.
	PtrFrom struct {
		Dst, X string
		To     Type
	}

	// AtomicLoad is `%v = atomic.load %p`: the value a ptr<T> holds, read
	// so that no other thread's write is seen half done and every thread
	// sees the writes of every atomic operation in one order. T is an
	// integer, a bool, a ptr or an anyptr, and the address is aligned for
	// it. There is one ordering, sequentially consistent: every atomic
	// operation and fence blocks all reordering around it, so there is no
	// ordering operand to get wrong.
	AtomicLoad struct{ Dst, Ptr string }

	// AtomicStore is `atomic.store %p, %v`: the write that pairs with
	// AtomicLoad.
	AtomicStore struct{ Ptr, Val string }

	// AtomicRMW is `%old = atomic.<op> %p, %v` for op swap, add, sub, and,
	// or and xor: the value at p becomes v, or the sum, difference or
	// bitwise combination of it and v, in one indivisible step, and the
	// result is what it held before. add and sub wrap. swap takes the
	// integers, bool and pointers; the arithmetic and bitwise ones take
	// integers.
	AtomicRMW struct {
		Dst, Op, Ptr, Val string
	}

	// AtomicCas is `%old = atomic.cas %p, %expected, %new`: when the value
	// at p equals expected it becomes new, in one indivisible step, and
	// the result is what it held before. The exchange happened exactly
	// when the result equals expected.
	AtomicCas struct{ Dst, Ptr, Expected, New string }

	// Fence is `atomic.fence`: nothing before it is reordered with
	// anything after it, which a store followed by a load of another
	// address needs and neither operation gives on its own.
	Fence struct{}

	// MemLoad is `%v = mem.load.<T> @mem, %addr`. The suffix names both
	// the result type and the width read, so mem.load.u8 reads one byte
	// and mem.load.f64 reads eight. Addresses are u32 and the access is
	// bounds-checked.
	MemLoad struct {
		Dst, Memory, Addr string
		Type              *Prim
	}
	// MemStore is `mem.store.<T> @mem, %addr, %val`. The value's type
	// must be exactly the suffix's: a producer narrowing a wider register
	// says so with trunc, rather than having the width of a store decide
	// it silently.
	MemStore struct {
		Memory, Addr, Val string
		Type              *Prim
	}
	// MemSize is `%n = mem.size @mem` -> u32, the current size in pages.
	MemSize struct{ Dst, Memory string }
	// MemGrow is `%n = mem.grow @mem, %pages` -> i32: the size in pages
	// before the call, or -1 if it could not grow. New pages are zeroed.
	MemGrow struct{ Dst, Memory, Pages string }
	// MemCopy is `mem.copy @mem, %to, %from, %len`, overlap-safe.
	MemCopy struct{ Memory, To, From, Len string }
	// MemFill is `mem.fill @mem, %to, %byte, %len`; %byte is a u8.
	MemFill struct{ Memory, To, Byte, Len string }

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
	Slice struct{ Dst, X string }
	// SliceSub is `%s = slice.sub %x, %lo, %hi`: the half-open range of
	// a slice, sharing the same storage. Checked as `lo <= hi <= len`,
	// aborting otherwise.
	//
	// It is an instruction rather than arithmetic in a front end because
	// a slice's extent is a run-time value, so the check has to be made
	// where the length is — and because the abort text is one message
	// every tier has to word identically.
	SliceSub struct{ Dst, X, Lo, Hi string }
	// Loc is `loc "file" N`: the source position of the instructions
	// that follow it, until the next Loc. It computes nothing and
	// declares nothing; a consumer that has no use for positions skips
	// it, and a producer with no source to point at emits none. The
	// native backend turns it into the line table a debugger reads.
	Loc struct {
		File string
		Line int
	}
	// SliceFrom is `%s = slice.from %p, %n`: the slice of n elements that
	// start where the ptr<T> p points. Nothing can check that n elements
	// live there, which is why a producer reaches it only from code that
	// vouches for the memory.
	SliceFrom struct{ Dst, Ptr, Len string }
	// Syscall is `%r = syscall %n(%a, ...)`: Linux system call n with up
	// to six one-word arguments, answering the kernel's raw isize.
	Syscall struct {
		Dst, Number string
		Args        []string
	}
	IndexGet struct{ Dst, X, Index string }
	IndexSet struct{ X, Index, Val string }
	Len      struct{ Dst, X string }

	// IndexPtr is `%q = index.ptr %p, %i`: ptr<array<T, N>> to the ptr<T>
	// of element i, bounds-checked against N and aborting out of range
	// exactly as index.get does.
	//
	// It is the checked counterpart of elem.ptr composed with ptr.offset,
	// and it exists because those two are unchecked: reaching an element
	// through the array's address is the one case where the extent *is*
	// known statically, so the check costs a compare against a constant
	// rather than the run-time extent a pointer does not carry.
	//
	// It is what lets an element be read or written where it lies. Without
	// it, indexing an array a pointer reaches has to load the whole
	// aggregate into a register, index that copy, and store it back.
	IndexPtr struct{ Dst, Ptr, Index string }

	StrConcat struct{ Dst, A, B string }

	// Call is `%r = call @f(%a, %b)`; Dst empty discards a void result.
	Call struct {
		Dst, Callee string
		Args        []string
	}
	FnRef struct{ Dst, Callee string }
	// CodeRef is `%p = code.ref @f`: the address of @f as a one-word
	// `code(...)`, with no environment beside it. `fn.ref` answers the
	// same question with a `fn`, which is two words and can also hold a
	// closure; this one is what a C function pointer is.
	CodeRef struct{ Dst, Callee string }
	// MakeClosure is `%c = closure @f, %env`: partially applies @f's
	// first parameter.
	MakeClosure  struct{ Dst, Callee, Env string }
	CallIndirect struct {
		Dst, Fn string
		Args    []string
	}

	Return struct{ Val string } // "" for a void return
	Abort  struct{ Message string }

	// ThreadStart is `%tid = thread.start %stack, @body, %data`: a new
	// thread that runs @body(%data) and ends when it returns. It says
	// what happens and not how, so each backend does it its own
	// operating system's way.
	//
	// @body takes one anyptr and returns nothing, and %data is what it
	// is called with. The thread is detached: nothing waits for it, and
	// the result is only its identity, a usize that is never zero.
	// A thread that cannot be started aborts.
	//
	// %stack is an opt<anyptr>. A null leaves the stack to the backend. A
	// non-null one is the address just past the top of a block of memory
	// the caller owns, aligned to 16 bytes, for the thread to use as its
	// stack until it ends; a backend whose system library owns thread
	// stacks may ignore it, and a program that passes one cannot tell.
	// What the caller may rely on either way is that it must not use the
	// block until the thread has ended.
	ThreadStart struct {
		Dst, Stack, Body, Data string
	}

	// ThreadExit is `thread.exit`: the calling thread ends, as if @body
	// had returned from wherever it was. On the main thread it ends the
	// program's first thread only, and the process lives on while any
	// other thread does.
	ThreadExit struct{}

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

	// OptWrap is `%o = opt.wrap %v`: T -> opt<T>. Optionals do not nest.
	OptWrap struct{ Dst, X string }
	// OptUnwrap is `%v = opt.unwrap %o`: opt<T> -> T, aborts on null.
	// Null tests are eq/ne against a const.null of the same optional type.
	OptUnwrap struct{ Dst, X string }

	AnyWrap   struct{ Dst, X string }
	AnyUnwrap struct {
		Dst, X string
		To     Type
	}
	// AnyIs is `%b = dyn.is %d -> T`: the shape test dyn.unwrap aborts
	// on. Receiving a dyn from a foreign module is impossible without
	// it, since every other way to look inside one traps.
	AnyIs struct {
		Dst, X string
		To     Type
	}
	AnyCall struct {
		Dst, X string
		Args   []string
	}
	AnyGet struct{ Dst, X, Name string }
	AnySet struct{ X, Name, Val string }
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
func (*Trunc) isInstr()        {}
func (*Bitcast) isInstr()      {}
func (*Slot) isInstr()         {}
func (*GlobalPtr) isInstr()    {}
func (*New) isInstr()          {}
func (*Free) isInstr()         {}
func (*Load) isInstr()         {}
func (*Store) isInstr()        {}
func (*FieldPtr) isInstr()     {}
func (*ElemPtr) isInstr()      {}
func (*PtrOffset) isInstr()    {}
func (*PtrDiff) isInstr()      {}
func (*PtrErase) isInstr()     {}
func (*PtrCast) isInstr()      {}
func (*PtrAddr) isInstr()      {}
func (*PtrFrom) isInstr()      {}
func (*AtomicLoad) isInstr()   {}
func (*AtomicStore) isInstr()  {}
func (*AtomicRMW) isInstr()    {}
func (*AtomicCas) isInstr()    {}
func (*Fence) isInstr()        {}
func (*MemLoad) isInstr()      {}
func (*MemStore) isInstr()     {}
func (*MemSize) isInstr()      {}
func (*MemGrow) isInstr()      {}
func (*MemCopy) isInstr()      {}
func (*MemFill) isInstr()      {}
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
func (*SliceSub) isInstr()     {}
func (*SliceFrom) isInstr()    {}
func (*Syscall) isInstr()      {}
func (*Loc) isInstr()          {}
func (*IndexGet) isInstr()     {}
func (*IndexSet) isInstr()     {}
func (*IndexPtr) isInstr()     {}
func (*Len) isInstr()          {}
func (*StrConcat) isInstr()    {}
func (*Call) isInstr()         {}
func (*FnRef) isInstr()        {}
func (*CodeRef) isInstr()      {}
func (*MakeClosure) isInstr()  {}
func (*CallIndirect) isInstr() {}
func (*Return) isInstr()       {}
func (*Abort) isInstr()        {}
func (*ThreadStart) isInstr()  {}
func (*ThreadExit) isInstr()   {}
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
func (*OptWrap) isInstr()      {}
func (*OptUnwrap) isInstr()    {}
func (*AnyWrap) isInstr()      {}
func (*AnyUnwrap) isInstr()    {}
func (*AnyIs) isInstr()        {}
func (*AnyCall) isInstr()      {}
func (*AnyGet) isInstr()       {}
func (*AnySet) isInstr()       {}
