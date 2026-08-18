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
		switch i.Op {
		case "neg":
			if !intKind(typ) && !floatKind(typ) {
				return fmt.Errorf("neg wants a number, not %s", typ)
			}
		case "bnot":
			if !intKind(typ) {
				return fmt.Errorf("bnot wants an integer, not %s", typ)
			}
		case "not":
			if !isBool(typ) {
				return fmt.Errorf("not wants a bool, not %s", typ)
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

	case *Slot:
		return fv.def(state, i.Dst, &Ptr{Elem: i.Type})
	case *GlobalPtr:
		typ, ok := fv.globals[i.Name]
		if !ok {
			return fmt.Errorf("no global @%s", i.Name)
		}
		return fv.def(state, i.Dst, &Ptr{Elem: typ})
	case *New:
		return fv.def(state, i.Dst, &Ptr{Elem: i.Type})

	case *Free:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if _, ok := typ.(*Ptr); !ok {
			return fmt.Errorf("free wants a pointer, not %s", typ)
		}
		return nil

	case *Load:
		typ, err := fv.use(state, i.Ptr)
		if err != nil {
			return err
		}
		pointer, ok := typ.(*Ptr)
		if !ok {
			return fmt.Errorf("load wants a pointer, not %s", typ)
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
		shape, err := fv.namedRecordOf(pointer.Elem)
		if err != nil {
			return err
		}
		if i.Index < 0 || i.Index >= len(shape.Fields) {
			return fmt.Errorf("field %d out of range", i.Index)
		}
		return fv.def(state, i.Dst, &Ptr{Elem: shape.Fields[i.Index].Type})

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
		array, ok := typ.(*ArrayOf)
		if !ok {
			return fmt.Errorf("slice.of wants an array, not %s", typ)
		}
		return fv.def(state, i.Dst, &SliceOf{Elem: array.Elem})

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
		if !TypesEqual(envType, sig.Params[0]) {
			return fmt.Errorf("closure environment is %s, @%s wants %s", envType, i.Callee, sig.Params[0])
		}
		return fv.def(state, i.Dst, &FnType{Params: sig.Params[1:], Result: sig.Result, Async: sig.Async})

	case *CallIndirect:
		typ, err := fv.use(state, i.Fn)
		if err != nil {
			return err
		}
		sig, ok := typ.(*FnType)
		if !ok {
			return fmt.Errorf("call.indirect wants a fn value, not %s", typ)
		}
		return fv.checkCallShape(state, sig, i.Args, i.Dst, "the fn value")

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

	case *DynWrap:
		typ, err := fv.use(state, i.X)
		if err != nil {
			return err
		}
		if bad := boundaryOffender(typ); bad != "" {
			return fmt.Errorf("dyn.wrap of %s: %s cannot become dyn; dyn values may cross boundaries", typ, bad)
		}
		return fv.def(state, i.Dst, &Dyn{})

	case *DynUnwrap:
		if err := fv.useDyn(state, i.X, "dyn.unwrap"); err != nil {
			return err
		}
		if bad := boundaryOffender(i.To); bad != "" {
			return fmt.Errorf("dyn.unwrap to %s: %s cannot come out of a dyn", i.To, bad)
		}
		return fv.def(state, i.Dst, i.To)

	case *DynCall:
		if err := fv.useDyn(state, i.X, "dyn.call"); err != nil {
			return err
		}
		for _, arg := range i.Args {
			if err := fv.useDyn(state, arg, "dyn.call arguments"); err != nil {
				return err
			}
		}
		return fv.def(state, i.Dst, &Dyn{})

	case *DynGet:
		if err := fv.useDyn(state, i.X, "dyn.get"); err != nil {
			return err
		}
		return fv.def(state, i.Dst, &Dyn{})

	case *DynSet:
		if err := fv.useDyn(state, i.X, "dyn.set"); err != nil {
			return err
		}
		return fv.useDyn(state, i.Val, "dyn.set")
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

func (fv *funcVerifier) useDyn(state *regState, name, what string) error {
	typ, err := fv.use(state, name)
	if err != nil {
		return err
	}
	if _, ok := typ.(*Dyn); !ok {
		return fmt.Errorf("%s works on dyn values, not %s", what, typ)
	}
	return nil
}
