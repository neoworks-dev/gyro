package metals

import "fmt"

func (fv *funcVerifier) checkInstr(instr Instr, state *regState) error {
	switch i := instr.(type) {
	case *Const:
		if i.Null {
			if _, ok := i.Type.(*Opt); !ok {
				return fmt.Errorf("const.null wants an opt<...> type, not %s", i.Type)
			}
			return fv.def(state, i.Dst, i.Type)
		}
		prim, ok := i.Type.(*Prim)
		if !ok {
			return fmt.Errorf("const wants a primitive type, not %s", i.Type)
		}
		if err := checkConstValue(prim, i.Value); err != nil {
			return err
		}
		return fv.def(state, i.Dst, prim)

	case *Copy:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		return fv.def(state, i.Dst, typ)

	case *Bin:
		return fv.checkBin(i, state)

	case *Un:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		class, ok := UnOps[i.Op]
		if !ok {
			return fmt.Errorf("unknown operator %q", i.Op)
		}
		switch class {
		case "number":
			if !intKind(typ) && !floatKind(typ) {
				return fmt.Errorf("%s wants a number, not %s", i.Op, typ)
			}
		case "int":
			if !intKind(typ) {
				return fmt.Errorf("%s wants an integer, not %s", i.Op, typ)
			}
		case "float":
			if !floatKind(typ) {
				return fmt.Errorf("%s wants a float, not %s", i.Op, typ)
			}
		case "bool":
			if !isBool(typ) {
				return fmt.Errorf("%s wants a bool, not %s", i.Op, typ)
			}
		}
		return fv.def(state, i.Dst, typ)

	case *Conv:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if !convertibleKind(typ) || !convertibleKind(i.To) {
			return fmt.Errorf("conv converts numbers and rune; %s -> %s does not", typ, i.To)
		}
		return fv.def(state, i.Dst, i.To)

	case *Trunc:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if !intKind(typ) || !intKind(i.To) {
			return fmt.Errorf("trunc narrows integers; %s -> %s is not one", typ, i.To)
		}
		if PrimWidth(i.To) > PrimWidth(typ) {
			return fmt.Errorf("trunc %s -> %s widens; conv is the widening one", typ, i.To)
		}
		return fv.def(state, i.Dst, i.To)

	case *Bitcast:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if !numericKind(typ) || !numericKind(i.To) {
			return fmt.Errorf("bitcast reinterprets numbers; %s -> %s does not", typ, i.To)
		}
		if PrimWidth(typ) != PrimWidth(i.To) {
			return fmt.Errorf("bitcast %s -> %s changes width; only the same one reinterprets", typ, i.To)
		}
		return fv.def(state, i.Dst, i.To)

	case *MemLoad:
		if err := fv.memAddr(state, i.Memory, i.Addr, "mem.load"); err != nil {
			return err
		}
		if !MemAccess(i.Type) {
			return fmt.Errorf("mem.load cannot read a %s", i.Type)
		}
		return fv.def(state, i.Dst, i.Type)

	case *MemStore:
		if err := fv.memAddr(state, i.Memory, i.Addr, "mem.store"); err != nil {
			return err
		}
		if !MemAccess(i.Type) {
			return fmt.Errorf("mem.store cannot write a %s", i.Type)
		}
		valType, err := fv.use(state, i.Val)
		if err != nil {
			return err
		}
		if !TypesEqual(valType, i.Type) {
			return fmt.Errorf("mem.store.%s of a %s; trunc says which bits to keep", i.Type, valType)
		}
		return nil

	case *MemSize:
		if err := fv.memory(i.Memory); err != nil {
			return err
		}
		return fv.def(state, i.Dst, TUint32)

	case *MemGrow:
		if err := fv.memory(i.Memory); err != nil {
			return err
		}
		pages, err := fv.use(state, i.Pages)
		if err != nil {
			return err
		}
		if !isU32(pages) {
			return fmt.Errorf("mem.grow counts pages in u32, not %s", pages)
		}
		// i32, not u32: the result is the previous size or -1.
		return fv.def(state, i.Dst, TInt32)

	case *MemCopy:
		if err := fv.memAddr(state, i.Memory, i.To, "mem.copy"); err != nil {
			return err
		}
		return fv.memCounts(state, "mem.copy", i.From, i.Len)

	case *MemFill:
		if err := fv.memAddr(state, i.Memory, i.To, "mem.fill"); err != nil {
			return err
		}
		b, err := fv.use(state, i.Byte)
		if err != nil {
			return err
		}
		if !isU8(b) {
			return fmt.Errorf("mem.fill writes a u8, not %s", b)
		}
		return fv.memCounts(state, "mem.fill", i.Len)

	case *Slot:
		if err := fv.checkType(i.Type); err != nil {
			return fmt.Errorf("slot: %w", err)
		}
		return fv.def(state, i.Dst, &Ptr{Elem: i.Type})
	case *GlobalPtr:
		typ, ok := fv.globals[i.Name]
		if !ok {
			return fmt.Errorf("no global @%s", i.Name)
		}
		return fv.def(state, i.Dst, &Ptr{Elem: typ})
	case *New:
		if err := fv.checkType(i.Type); err != nil {
			return fmt.Errorf("new: %w", err)
		}
		return fv.def(state, i.Dst, &Ptr{Elem: i.Type})

	case *Free:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		pointer, ok := typ.(*Ptr)
		if !ok {
			return fmt.Errorf("free wants a pointer, not %s", typ)
		}
		return fv.sized(pointer.Elem, "free")

	case *Load:
		typ, err := fv.use(state, i.Ptr)
		if err != nil {
			return err
		}
		pointer, ok := typ.(*Ptr)
		if !ok {
			return fmt.Errorf("load wants a pointer, not %s", typ)
		}
		if err := fv.sized(pointer.Elem, "load"); err != nil {
			return err
		}
		return fv.def(state, i.Dst, pointer.Elem)

	case *Store:
		ptrType, err := fv.use(state, i.Ptr)
		if err != nil {
			return err
		}
		pointer, ok := ptrType.(*Ptr)
		if !ok {
			return fmt.Errorf("store wants a pointer, not %s", ptrType)
		}
		if err := fv.sized(pointer.Elem, "store"); err != nil {
			return err
		}
		valType, err := fv.use(state, i.Val)
		if err != nil {
			return err
		}
		if !TypesEqual(valType, pointer.Elem) {
			return fmt.Errorf("store of %s into %s", valType, ptrType)
		}
		return nil

	case *FieldPtr:
		typ, err := fv.use(state, i.Ptr)
		if err != nil {
			return err
		}
		pointer, ok := typ.(*Ptr)
		if !ok {
			return fmt.Errorf("field.ptr wants a pointer, not %s", typ)
		}
		// A union's members are reached the same way a record's are, and
		// the only difference is that they all sit at zero. That is the
		// layout's answer, not this one's.
		decl, err := fv.memberedDecl(pointer.Elem)
		if err != nil {
			return err
		}
		members := decl.Members()
		if i.Index < 0 || i.Index >= len(members) {
			return fmt.Errorf("field %d out of range", i.Index)
		}
		return fv.def(state, i.Dst, &Ptr{Elem: members[i.Index].Type})

	case *ElemPtr:
		typ, err := fv.use(state, i.Ptr)
		if err != nil {
			return err
		}
		pointer, ok := typ.(*Ptr)
		if !ok {
			return fmt.Errorf("elem.ptr wants a pointer, not %s", typ)
		}
		array, ok := pointer.Elem.(*ArrayOf)
		if !ok {
			return fmt.Errorf("elem.ptr wants a pointer to an array, not %s", typ)
		}
		return fv.def(state, i.Dst, &Ptr{Elem: array.Elem})

	case *PtrOffset:
		typ, err := fv.use(state, i.Ptr)
		if err != nil {
			return err
		}
		pointer, ok := typ.(*Ptr)
		if !ok {
			return fmt.Errorf("ptr.offset wants a pointer, not %s", typ)
		}
		if err := fv.sized(pointer.Elem, "ptr.offset"); err != nil {
			return err
		}
		count, err := fv.use(state, i.N)
		if err != nil {
			return err
		}
		if !intKind(count) {
			return fmt.Errorf("ptr.offset counts in an integer, not %s", count)
		}
		return fv.def(state, i.Dst, typ)

	case *PtrDiff:
		a, err := fv.use(state, i.A)
		if err != nil {
			return err
		}
		b, err := fv.use(state, i.B)
		if err != nil {
			return err
		}
		pointer, ok := a.(*Ptr)
		if !ok {
			return fmt.Errorf("ptr.diff wants two pointers, not %s", a)
		}
		if !TypesEqual(a, b) {
			return fmt.Errorf("ptr.diff subtracts two pointers of one type, not %s and %s", a, b)
		}
		if err := fv.sized(pointer.Elem, "ptr.diff"); err != nil {
			return err
		}
		return fv.def(state, i.Dst, TInt64)

	case *PtrErase:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		switch typ := typ.(type) {
		case *Ptr:
			return fv.def(state, i.Dst, &AnyPtr{})
		case *Opt:
			if _, ok := typ.Elem.(*Ptr); ok {
				return fv.def(state, i.Dst, &Opt{Elem: &AnyPtr{}})
			}
		}
		return fmt.Errorf("ptr.erase wants a ptr<T> or an opt<ptr<T>>, not %s", typ)

	case *PtrAddr:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if _, ok := typ.(*AnyPtr); ok {
			return fv.def(state, i.Dst, TUsize)
		}
		if opt, ok := typ.(*Opt); ok {
			if _, ok := opt.Elem.(*AnyPtr); ok {
				return fv.def(state, i.Dst, TUsize)
			}
		}
		return fmt.Errorf("ptr.addr wants an anyptr or an opt<anyptr>, not %s; ptr.erase a typed pointer first", typ)

	case *PtrFrom:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if !isUsize(typ) {
			return fmt.Errorf("ptr.from wants a usize, not %s", typ)
		}
		switch to := i.To.(type) {
		case *AnyPtr:
			fv.claimsAddress = true
			return fv.def(state, i.Dst, to)
		case *Opt:
			if _, ok := to.Elem.(*AnyPtr); ok {
				fv.claimsAddress = true
				return fv.def(state, i.Dst, to)
			}
		}
		return fmt.Errorf("ptr.from makes an anyptr or an opt<anyptr>, not %s; ptr.cast puts a type on it", i.To)

	case *AtomicLoad:
		elem, err := fv.atomicCell(state, i.Ptr, "atomic.load", false)
		if err != nil {
			return err
		}
		return fv.def(state, i.Dst, elem)

	case *AtomicStore:
		elem, err := fv.atomicCell(state, i.Ptr, "atomic.store", false)
		if err != nil {
			return err
		}
		return fv.atomicOperand(state, i.Val, elem, "atomic.store")

	case *AtomicRMW:
		op := "atomic." + i.Op
		elem, err := fv.atomicCell(state, i.Ptr, op, i.Op != "swap")
		if err != nil {
			return err
		}
		if err := fv.atomicOperand(state, i.Val, elem, op); err != nil {
			return err
		}
		return fv.def(state, i.Dst, elem)

	case *AtomicCas:
		elem, err := fv.atomicCell(state, i.Ptr, "atomic.cas", false)
		if err != nil {
			return err
		}
		if err := fv.atomicOperand(state, i.Expected, elem, "atomic.cas"); err != nil {
			return err
		}
		if err := fv.atomicOperand(state, i.New, elem, "atomic.cas"); err != nil {
			return err
		}
		return fv.def(state, i.Dst, elem)

	case *Fence:
		return nil

	case *PtrCast:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if err := fv.checkType(i.To); err != nil {
			return err
		}
		switch to := i.To.(type) {
		case *Ptr:
			if _, ok := typ.(*AnyPtr); !ok {
				return fmt.Errorf("ptr.cast to %s wants an anyptr, not %s", i.To, typ)
			}
			return fv.def(state, i.Dst, to)
		case *Opt:
			if _, ok := to.Elem.(*Ptr); !ok {
				break
			}
			opt, ok := typ.(*Opt)
			if !ok {
				return fmt.Errorf("ptr.cast to %s wants an opt<anyptr>, not %s", i.To, typ)
			}
			if _, ok := opt.Elem.(*AnyPtr); !ok {
				return fmt.Errorf("ptr.cast to %s wants an opt<anyptr>, not %s", i.To, typ)
			}
			return fv.def(state, i.Dst, to)
		}
		return fmt.Errorf("ptr.cast puts a target type back on an anyptr, and %s is not one", i.To)

	case *MakeRecord:
		shape, err := fv.recordShape(i.TypeName)
		if err != nil {
			return err
		}
		if len(i.Args) != len(shape.Fields) {
			return fmt.Errorf("%%%s has %d fields, the record sets %d", i.TypeName, len(shape.Fields), len(i.Args))
		}
		types, err := fv.uses(state, i.Args)
		if err != nil {
			return err
		}
		for index, typ := range types {
			if !TypesEqual(typ, shape.Fields[index].Type) {
				return fmt.Errorf("field %s wants %s, got %s", shape.Fields[index].Name, shape.Fields[index].Type, typ)
			}
		}
		return fv.def(state, i.Dst, &Named{Name: i.TypeName})

	case *FieldGet:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		shape, err := fv.namedRecordOf(typ)
		if err != nil {
			return err
		}
		if i.Index < 0 || i.Index >= len(shape.Fields) {
			return fmt.Errorf("field %d out of range", i.Index)
		}
		return fv.def(state, i.Dst, shape.Fields[i.Index].Type)

	case *FieldSet:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		shape, err := fv.namedRecordOf(typ)
		if err != nil {
			return err
		}
		if i.Index < 0 || i.Index >= len(shape.Fields) {
			return fmt.Errorf("field %d out of range", i.Index)
		}
		valType, err := fv.use(state, i.Val)
		if err != nil {
			return err
		}
		if !TypesEqual(valType, shape.Fields[i.Index].Type) {
			return fmt.Errorf("field %s wants %s, got %s", shape.Fields[i.Index].Name, shape.Fields[i.Index].Type, valType)
		}
		return nil

	case *MakeVariant:
		shape, err := fv.enumShape(i.TypeName)
		if err != nil {
			return err
		}
		if i.Variant < 0 || i.Variant >= len(shape.Variants) {
			return fmt.Errorf("variant %d out of range", i.Variant)
		}
		payload := shape.Variants[i.Variant].Payload
		if len(i.Args) != len(payload) {
			return fmt.Errorf("variant %s carries %d value(s), got %d", shape.Variants[i.Variant].Name, len(payload), len(i.Args))
		}
		types, err := fv.uses(state, i.Args)
		if err != nil {
			return err
		}
		for index, typ := range types {
			if !TypesEqual(typ, payload[index]) {
				return fmt.Errorf("payload %d wants %s, got %s", index, payload[index], typ)
			}
		}
		return fv.def(state, i.Dst, &Named{Name: i.TypeName})

	case *VariantTag:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if _, err := fv.namedEnumOf(typ); err != nil {
			return err
		}
		return fv.def(state, i.Dst, TInt32)

	case *VariantField:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		shape, err := fv.namedEnumOf(typ)
		if err != nil {
			return err
		}
		if i.Variant < 0 || i.Variant >= len(shape.Variants) {
			return fmt.Errorf("variant %d out of range", i.Variant)
		}
		payload := shape.Variants[i.Variant].Payload
		if i.Index < 0 || i.Index >= len(payload) {
			return fmt.Errorf("payload %d out of range", i.Index)
		}
		return fv.def(state, i.Dst, payload[i.Index])

	case *MakeTuple:
		types, err := fv.uses(state, i.Args)
		if err != nil {
			return err
		}
		return fv.def(state, i.Dst, &TupleOf{Elems: types})

	case *TupleGet:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		tuple, ok := typ.(*TupleOf)
		if !ok {
			return fmt.Errorf("tuple.get wants a tuple, not %s", typ)
		}
		if i.Index < 0 || i.Index >= len(tuple.Elems) {
			return fmt.Errorf("tuple element %d out of range", i.Index)
		}
		return fv.def(state, i.Dst, tuple.Elems[i.Index])

	case *MakeArray:
		types, err := fv.uses(state, i.Args)
		if err != nil {
			return err
		}
		for _, typ := range types {
			if !TypesEqual(typ, i.Elem) {
				return fmt.Errorf("array<%s> element is %s", i.Elem, typ)
			}
		}
		return fv.def(state, i.Dst, &ArrayOf{Elem: i.Elem, Len: int64(len(i.Args))})

	case *Slice:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		// An array's *address* borrows the storage that address names,
		// which is the only way to view an array a pointer reaches: the
		// value form would have to load it first, and a slice of a copy
		// is a view of storage nothing else can see. Same reasoning as
		// index.ptr, and the extent is static either way.
		if pointer, ok := typ.(*Ptr); ok {
			array, ok := pointer.Elem.(*ArrayOf)
			if !ok {
				return fmt.Errorf("slice.of wants a pointer to an array, not %s", typ)
			}
			return fv.def(state, i.Dst, &SliceOf{Elem: array.Elem})
		}
		array, ok := typ.(*ArrayOf)
		if !ok {
			return fmt.Errorf("slice.of wants an array or a pointer to one, not %s", typ)
		}
		return fv.def(state, i.Dst, &SliceOf{Elem: array.Elem})

	case *SliceFrom:
		typ, err := fv.use(state, i.Ptr)
		if err != nil {
			return err
		}
		pointer, ok := typ.(*Ptr)
		if !ok {
			return fmt.Errorf("slice.from wants a pointer to the first element, not %s", typ)
		}
		if _, isArray := pointer.Elem.(*ArrayOf); isArray {
			// A pointer to an array is slice.of's operand, and its
			// extent is the type's, not a run-time length.
			return fmt.Errorf("slice.from wants a pointer to an element, not to %s; slice.of views an array", pointer.Elem)
		}
		lenType, err := fv.use(state, i.Len)
		if err != nil {
			return err
		}
		if !isUsize(lenType) {
			return fmt.Errorf("slice.from length is usize, not %s", lenType)
		}
		return fv.def(state, i.Dst, &SliceOf{Elem: pointer.Elem})

	case *Syscall:
		numberType, err := fv.use(state, i.Number)
		if err != nil {
			return err
		}
		if !isUsize(numberType) {
			return fmt.Errorf("syscall number is usize, not %s", numberType)
		}
		if len(i.Args) > 6 {
			return fmt.Errorf("syscall takes at most six arguments, not %d", len(i.Args))
		}
		for _, arg := range i.Args {
			typ, err := fv.use(state, arg)
			if err != nil {
				return err
			}
			if !isSyscallWord(typ) {
				return fmt.Errorf("a syscall argument is one word, an integer, bool or pointer, not %s", typ)
			}
		}
		return fv.def(state, i.Dst, TIsize)

	case *SliceSub:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		slice, ok := typ.(*SliceOf)
		if !ok {
			// An array is sliced with slice.of first. A slice is what
			// carries the run-time extent this instruction narrows, and
			// widening the operand would give one instruction two
			// storage models for no gain.
			return fmt.Errorf("slice.sub wants a slice, not %s", typ)
		}
		for _, bound := range []string{i.Lo, i.Hi} {
			boundType, err := fv.use(state, bound)
			if err != nil {
				return err
			}
			if !isUsize(boundType) {
				return fmt.Errorf("slice.sub bounds are usize, not %s", boundType)
			}
		}
		return fv.def(state, i.Dst, &SliceOf{Elem: slice.Elem})

	case *IndexGet:
		elem, err := fv.checkIndex(state, i.X, i.Index)
		if err != nil {
			return err
		}
		return fv.def(state, i.Dst, elem)

	case *IndexSet:
		elem, err := fv.checkIndex(state, i.X, i.Index)
		if err != nil {
			return err
		}
		valType, err := fv.use(state, i.Val)
		if err != nil {
			return err
		}
		if !TypesEqual(valType, elem) {
			return fmt.Errorf("index.set of %s into elements of %s", valType, elem)
		}
		return nil

	case *IndexPtr:
		typ, err := fv.use(state, i.Ptr)
		if err != nil {
			return err
		}
		// One question — the checked address of element i — with two
		// places the extent can come from: the `N` in an array's type,
		// or the length word a slice carries. Both are checks; what
		// `ptr.offset` cannot do is check at all.
		var elem Type
		switch operand := typ.(type) {
		case *Ptr:
			array, ok := operand.Elem.(*ArrayOf)
			if !ok {
				return fmt.Errorf("index.ptr wants a pointer to an array, not %s", typ)
			}
			elem = array.Elem
		case *SliceOf:
			elem = operand.Elem
		default:
			return fmt.Errorf("index.ptr wants a pointer to an array or a slice, not %s", typ)
		}
		indexType, err := fv.use(state, i.Index)
		if err != nil {
			return err
		}
		if !isUsize(indexType) {
			return fmt.Errorf("indexes are usize, not %s", indexType)
		}
		return fv.def(state, i.Dst, &Ptr{Elem: elem})

	case *Len:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if elemOfIndexable(typ) == nil && !isStr(typ) {
			return fmt.Errorf("len wants a slice, array or str, not %s", typ)
		}
		return fv.def(state, i.Dst, TUsize)

	case *StrConcat:
		for _, name := range []string{i.A, i.B} {
			typ, err := fv.use(state, name)
			if err != nil {
				return err
			}
			if !isStr(typ) {
				return fmt.Errorf("str.concat wants str, not %s", typ)
			}
		}
		return fv.def(state, i.Dst, TStr)

	case *Call:
		sig, ok := fv.callables[i.Callee]
		if !ok {
			return fmt.Errorf("unknown function @%s", i.Callee)
		}
		return fv.checkCallShape(state, sig, i.Args, i.Dst, "@"+i.Callee)

	case *FnRef:
		sig, ok := fv.callables[i.Callee]
		if !ok {
			return fmt.Errorf("unknown function @%s", i.Callee)
		}
		return fv.def(state, i.Dst, sig)

	case *CodeRef:
		sig, ok := fv.callables[i.Callee]
		if !ok {
			return fmt.Errorf("unknown function @%s", i.Callee)
		}
		if sig.Async {
			return fmt.Errorf("code.ref @%s: an async function is started, not called, so its address is not a code pointer", i.Callee)
		}
		return fv.def(state, i.Dst, &CodeType{Params: sig.Params, Result: sig.Result})

	case *MakeClosure:
		sig, ok := fv.callables[i.Callee]
		if !ok {
			return fmt.Errorf("unknown function @%s", i.Callee)
		}
		if len(sig.Params) == 0 {
			return fmt.Errorf("closure over @%s: it has no environment parameter to bind", i.Callee)
		}
		envType, err := fv.use(state, i.Env)
		if err != nil {
			return err
		}
		// The environment is a pointer, never a value: a function value
		// has one word to carry its environment in. What it points at,
		// a frame or an allocator's block, is the producer's to keep
		// alive.
		if _, isPtr := envType.(*Ptr); !isPtr {
			return fmt.Errorf("closure environment is %s; it must be a ptr, since a function value has one word to carry it in", envType)
		}
		if !TypesEqual(envType, sig.Params[0]) {
			return fmt.Errorf("closure environment is %s, @%s wants %s", envType, i.Callee, sig.Params[0])
		}
		return fv.def(state, i.Dst, &FnType{Params: sig.Params[1:], Result: sig.Result, Async: sig.Async})

	case *CallIndirect:
		typ, err := fv.use(state, i.Fn)
		if err != nil {
			return err
		}
		switch sig := typ.(type) {
		case *FnType:
			return fv.checkCallShape(state, sig, i.Args, i.Dst, "the fn value")
		case *CodeType:
			shape := &FnType{Params: sig.Params, Result: sig.Result}
			return fv.checkCallShape(state, shape, i.Args, i.Dst, "the code pointer")
		}
		return fmt.Errorf("call.indirect wants a fn value or a code pointer, not %s", typ)

	case *Return:
		if fv.fn.Result == nil {
			if i.Val != "" {
				return fmt.Errorf("this function returns nothing")
			}
			state.unreachable = true
			return nil
		}
		if i.Val == "" {
			return fmt.Errorf("this function returns %s; the return gives nothing", fv.fn.Result)
		}
		typ, err := fv.use(state, i.Val)
		if err != nil {
			return err
		}
		if !TypesEqual(typ, fv.fn.Result) {
			return fmt.Errorf("return of %s from a function returning %s", typ, fv.fn.Result)
		}
		state.unreachable = true
		return nil

	case *Loc:
		if i.Line < 1 {
			return fmt.Errorf("loc lines count from 1, not %d", i.Line)
		}
		if i.File == "" {
			return fmt.Errorf("loc wants a file name")
		}
		return nil

	case *Abort:
		state.unreachable = true
		return nil

	case *If:
		condType, err := fv.use(state, i.Cond)
		if err != nil {
			return err
		}
		if !isBool(condType) {
			return fmt.Errorf("if wants a bool, not %s", condType)
		}
		thenState := state.clone()
		if err := fv.checkBody(i.Then, thenState); err != nil {
			return err
		}
		elseState := state.clone()
		if err := fv.checkBody(i.Else, elseState); err != nil {
			return err
		}
		// Only registers assigned in both branches survive the join; a
		// branch that cannot fall through does not constrain it.
		switch {
		case thenState.unreachable && elseState.unreachable:
			state.defined = thenState.defined
			state.unreachable = true
		case thenState.unreachable:
			state.defined = elseState.defined
		case elseState.unreachable:
			state.defined = thenState.defined
		default:
			thenState.intersect(elseState)
			state.defined = thenState.defined
		}
		return nil

	case *Block:
		// A br can leave from anywhere inside, so nothing assigned within
		// is definite afterwards.
		inner := state.clone()
		return fv.withLabel(i.Label, i.Body, inner)

	case *Loop:
		inner := state.clone()
		return fv.withLabel(i.Label, i.Body, inner)

	case *Br:
		if !fv.hasLabel(i.Label) {
			return fmt.Errorf("no enclosing label $%s", i.Label)
		}
		state.unreachable = true
		return nil

	case *BrIf:
		condType, err := fv.use(state, i.Cond)
		if err != nil {
			return err
		}
		if !isBool(condType) {
			return fmt.Errorf("br_if wants a bool, not %s", condType)
		}
		if !fv.hasLabel(i.Label) {
			return fmt.Errorf("no enclosing label $%s", i.Label)
		}
		return nil

	case *Switch:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if !intKind(typ) {
			return fmt.Errorf("switch wants an integer, not %s", typ)
		}
		seen := map[int]bool{}
		arms := []*regState{}
		for _, c := range i.Cases {
			if seen[c.Value] {
				return fmt.Errorf("case %d appears twice", c.Value)
			}
			seen[c.Value] = true
			caseState := state.clone()
			if err := fv.checkBody(c.Body, caseState); err != nil {
				return err
			}
			arms = append(arms, caseState)
		}
		defaultState := state.clone()
		if err := fv.checkBody(i.Default, defaultState); err != nil {
			return err
		}
		// Without a default the switch may fall through untouched, so the
		// entry state stands. With one, join the arms that can fall out.
		if len(i.Default) == 0 {
			return nil
		}
		arms = append(arms, defaultState)
		var joined *regState
		for _, arm := range arms {
			if arm.unreachable {
				continue
			}
			if joined == nil {
				joined = arm
			} else {
				joined.intersect(arm)
			}
		}
		if joined == nil {
			state.unreachable = true
			return nil
		}
		state.defined = joined.defined
		return nil

	case *ChanNew:
		if i.Cap != "" {
			capType, err := fv.use(state, i.Cap)
			if err != nil {
				return err
			}
			if !isUsize(capType) {
				return fmt.Errorf("a channel capacity is usize, not %s", capType)
			}
		}
		return fv.def(state, i.Dst, &ChanOf{Elem: i.Elem})

	case *Send:
		return fv.checkSend(state, i.Ch, i.Val)

	case *Recv:
		elem, err := fv.chanElem(state, i.Ch)
		if err != nil {
			return err
		}
		return fv.def(state, i.Dst, &TupleOf{Elems: []Type{elem, TBool}})

	case *ChanClose:
		_, err := fv.chanElem(state, i.Ch)
		return err

	case *Await:
		if !fv.fn.Async {
			return fmt.Errorf("await only works inside an async fn")
		}
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		future, ok := typ.(*FutureOf)
		if !ok {
			return fmt.Errorf("await wants a future, not %s", typ)
		}
		return fv.def(state, i.Dst, future.Result)

	case *Select:
		for _, arm := range i.Arms {
			armState := state.clone()
			if arm.Send {
				if err := fv.checkSend(armState, arm.Ch, arm.Val); err != nil {
					return err
				}
			} else {
				elem, err := fv.chanElem(armState, arm.Ch)
				if err != nil {
					return err
				}
				if err := fv.def(armState, arm.Dst, elem); err != nil {
					return err
				}
			}
			if err := fv.checkBody(arm.Body, armState); err != nil {
				return err
			}
		}
		defaultState := state.clone()
		return fv.checkBody(i.Default, defaultState)

	case *OptWrap:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if _, ok := typ.(*Opt); ok {
			return fmt.Errorf("opt.wrap of %s: optionals do not nest", typ)
		}
		return fv.def(state, i.Dst, &Opt{Elem: typ})

	case *OptUnwrap:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		optional, ok := typ.(*Opt)
		if !ok {
			return fmt.Errorf("opt.unwrap wants an opt<...>, not %s", typ)
		}
		return fv.def(state, i.Dst, optional.Elem)

	case *AnyWrap:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		// Never native: an `any` may cross *any* boundary, so what may go
		// into one is what the strictest boundary carries. A pointer that
		// a linked library could be handed is not a value a collector on
		// the far side of some other boundary could ever hold.
		if bad := boundaryOffender(typ, fv.types, map[string]bool{}, false); bad != "" {
			return fmt.Errorf("any.wrap of %s: %s cannot become any; any values may cross boundaries", typ, bad)
		}
		return fv.def(state, i.Dst, &Any{})

	case *AnyUnwrap:
		if err := fv.useAny(state, i.X, "any.unwrap"); err != nil {
			return err
		}
		if bad := boundaryOffender(i.To, fv.types, map[string]bool{}, false); bad != "" {
			return fmt.Errorf("any.unwrap to %s: %s cannot come out of an any", i.To, bad)
		}
		return fv.def(state, i.Dst, i.To)

	case *AnyIs:
		if err := fv.useAny(state, i.X, "any.is"); err != nil {
			return err
		}
		if bad := boundaryOffender(i.To, fv.types, map[string]bool{}, false); bad != "" {
			return fmt.Errorf("any.is %s: %s never goes into an any, so the test can never hold", i.To, bad)
		}
		return fv.def(state, i.Dst, TBool)

	case *AnyCall:
		if err := fv.useAny(state, i.X, "any.call"); err != nil {
			return err
		}
		for _, arg := range i.Args {
			if err := fv.useAny(state, arg, "any.call arguments"); err != nil {
				return err
			}
		}
		return fv.def(state, i.Dst, &Any{})

	case *AnyGet:
		if err := fv.useAny(state, i.X, "any.get"); err != nil {
			return err
		}
		return fv.def(state, i.Dst, &Any{})

	case *AnySet:
		if err := fv.useAny(state, i.X, "any.set"); err != nil {
			return err
		}
		return fv.useAny(state, i.Val, "any.set")
	}
	return fmt.Errorf("unhandled instruction %T", instr)
}

func (fv *funcVerifier) checkBin(i *Bin, state *regState) error {
	class, ok := BinOps[i.Op]
	if !ok {
		return fmt.Errorf("unknown operator %q", i.Op)
	}
	left, err := fv.use(state, i.A)
	if err != nil {
		return err
	}
	right, err := fv.use(state, i.B)
	if err != nil {
		return err
	}
	if !TypesEqual(left, right) {
		return fmt.Errorf("%s wants matching operands, got %s and %s", i.Op, left, right)
	}

	switch class {
	case "int":
		if !intKind(left) {
			return fmt.Errorf("%s wants integers, not %s", i.Op, left)
		}
		return fv.def(state, i.Dst, left)
	case "float":
		if !floatKind(left) {
			return fmt.Errorf("%s wants floats, not %s", i.Op, left)
		}
		return fv.def(state, i.Dst, left)
	case "ordered":
		if !orderedKind(left) {
			return fmt.Errorf("%s orders numbers and rune, not %s", i.Op, left)
		}
		return fv.def(state, i.Dst, TBool)
	default: // eq, ne
		if _, isChan := left.(*ChanOf); isChan {
			return fmt.Errorf("channels do not compare")
		}
		// Comparing two unions would have to compare storage, and
		// nothing records which member is live in either of them — C
		// does not define this and neither does the format. A record
		// with one inside is the same question one level down.
		if bad := unionInside(left, fv.types, map[string]bool{}); bad != "" {
			return fmt.Errorf("%s does not compare: nothing records which member %s holds", left, bad)
		}
		return fv.def(state, i.Dst, TBool)
	}
}

func (fv *funcVerifier) checkIndex(state *regState, base, index string) (Type, error) {
	baseType, err := fv.use(state, base)
	if err != nil {
		return nil, err
	}
	elem := elemOfIndexable(baseType)
	if elem == nil {
		return nil, fmt.Errorf("indexing wants a slice or array, not %s", baseType)
	}
	indexType, err := fv.use(state, index)
	if err != nil {
		return nil, err
	}
	if !isUsize(indexType) {
		return nil, fmt.Errorf("indexes are usize, not %s", indexType)
	}
	return elem, nil
}

func (fv *funcVerifier) chanElem(state *regState, name string) (Type, error) {
	typ, err := fv.use(state, name)
	if err != nil {
		return nil, err
	}
	channel, ok := typ.(*ChanOf)
	if !ok {
		return nil, fmt.Errorf("a channel operation wants chan<...>, not %s", typ)
	}
	return channel.Elem, nil
}

func (fv *funcVerifier) checkSend(state *regState, ch, val string) error {
	elem, err := fv.chanElem(state, ch)
	if err != nil {
		return err
	}
	valType, err := fv.use(state, val)
	if err != nil {
		return err
	}
	if !TypesEqual(valType, elem) {
		return fmt.Errorf("send of %s on chan<%s>", valType, elem)
	}
	return nil
}

func (fv *funcVerifier) useAny(state *regState, name, what string) error {
	typ, err := fv.use(state, name)
	if err != nil {
		return err
	}
	if _, ok := typ.(*Any); !ok {
		return fmt.Errorf("%s works on any values, not %s", what, typ)
	}
	return nil
}

// atomicCell checks the pointer an atomic operation goes through and
// answers what it points at. The cell is a word the hardware can update in
// one step: an integer of 8 to 64 bits, a bool, a ptr or an anyptr, or the
// nullable form of either, which is the same word. The
// arithmetic and bitwise operations are for integers alone, since adding to
// a pointer or a bool means nothing an atomic can promise.
func (fv *funcVerifier) atomicCell(state *regState, ptr, op string, integersOnly bool) (Type, error) {
	typ, err := fv.use(state, ptr)
	if err != nil {
		return nil, err
	}
	pointer, ok := typ.(*Ptr)
	if !ok {
		return nil, fmt.Errorf("%s wants a ptr<T>, not %s", op, typ)
	}
	if integersOnly {
		if !intKind(pointer.Elem) {
			return nil, fmt.Errorf("%s works on an integer, not %s", op, pointer.Elem)
		}
		return pointer.Elem, nil
	}
	switch elem := pointer.Elem.(type) {
	case *Ptr, *AnyPtr:
		return elem, nil
	case *Opt:
		// A nullable pointer is one word, which is all an atomic asks.
		if NicheOpt(elem) {
			return elem, nil
		}
	}
	if intKind(pointer.Elem) || isBool(pointer.Elem) {
		return pointer.Elem, nil
	}
	return nil, fmt.Errorf("%s works on an integer, a bool or a pointer, not %s", op, pointer.Elem)
}

// atomicOperand checks that a value is the type the cell holds.
func (fv *funcVerifier) atomicOperand(state *regState, reg string, elem Type, op string) error {
	typ, err := fv.use(state, reg)
	if err != nil {
		return err
	}
	if !TypesEqual(typ, elem) {
		return fmt.Errorf("%s of %s on a cell of %s", op, typ, elem)
	}
	return nil
}
