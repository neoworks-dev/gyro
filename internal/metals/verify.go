package metals

import (
	"fmt"
	"strconv"
)

// Verify type-checks a module with no trust in its producer: every
// register is assigned before use on all paths and keeps one type, every
// instruction's operands check, and boundary rules hold on exported and
// cross-language signatures. It returns the first violation found.
func Verify(m *Module) error {
	v := &verifier{module: m, types: map[string]*TypeDecl{}, callables: map[string]*FnType{}}

	for _, decl := range m.Types {
		if _, dup := v.types[decl.Name]; dup {
			return fmt.Errorf("type %%%s is declared twice", decl.Name)
		}
		v.types[decl.Name] = decl
	}
	for _, decl := range m.Types {
		if err := v.checkDeclShapes(decl); err != nil {
			return err
		}
	}

	scheme := Scheme(m.Name)
	for _, imp := range m.Imports {
		foreign := Scheme(imp.Module) != scheme
		for _, fn := range imp.Funcs {
			if _, dup := v.callables[fn.Name]; dup {
				return fmt.Errorf("@%s is declared twice", fn.Name)
			}
			v.callables[fn.Name] = fn.Sig
			if err := v.checkSigTypes(fn.Name, fn.Sig); err != nil {
				return err
			}
			if foreign {
				if err := v.checkBoundary(fn.Name, fn.Sig); err != nil {
					return err
				}
			}
		}
	}

	for _, fn := range m.Funcs {
		if _, dup := v.callables[fn.Name]; dup {
			return fmt.Errorf("@%s is declared twice", fn.Name)
		}
		v.callables[fn.Name] = fn.Sig()
	}

	for _, global := range m.Globals {
		if err := v.checkType(global.Type); err != nil {
			return fmt.Errorf("global @%s: %w", global.Name, err)
		}
	}

	for _, fn := range m.Funcs {
		if err := v.checkSigTypes(fn.Name, fn.Sig()); err != nil {
			return err
		}
		if fn.Export {
			if err := v.checkBoundary(fn.Name, fn.Sig()); err != nil {
				return err
			}
		}
		if err := v.checkFunc(fn); err != nil {
			return fmt.Errorf("fn @%s: %w", fn.Name, err)
		}
	}
	return nil
}

type verifier struct {
	module    *Module
	types     map[string]*TypeDecl
	callables map[string]*FnType
}

// ---------------------------------------------------------------------------
// Declarations and signatures
// ---------------------------------------------------------------------------

func (v *verifier) checkDeclShapes(decl *TypeDecl) error {
	if decl.Record != nil {
		for _, field := range decl.Record.Fields {
			if err := v.checkType(field.Type); err != nil {
				return fmt.Errorf("type %%%s, field %s: %w", decl.Name, field.Name, err)
			}
		}
		return nil
	}
	for _, variant := range decl.Enum.Variants {
		for _, typ := range variant.Payload {
			if err := v.checkType(typ); err != nil {
				return fmt.Errorf("type %%%s, variant %s: %w", decl.Name, variant.Name, err)
			}
		}
	}
	return nil
}

// checkType walks a type and requires every Named reference to resolve.
func (v *verifier) checkType(t Type) error {
	switch t := t.(type) {
	case *Named:
		if v.types[t.Name] == nil {
			return fmt.Errorf("unknown type %%%s", t.Name)
		}
	case *Ptr:
		return v.checkType(t.Elem)
	case *Opt:
		return v.checkType(t.Elem)
	case *SliceOf:
		return v.checkType(t.Elem)
	case *ArrayOf:
		return v.checkType(t.Elem)
	case *ChanOf:
		return v.checkType(t.Elem)
	case *FutureOf:
		return v.checkType(t.Result)
	case *TupleOf:
		for _, elem := range t.Elems {
			if err := v.checkType(elem); err != nil {
				return err
			}
		}
	case *FnType:
		for _, param := range t.Params {
			if err := v.checkType(param); err != nil {
				return err
			}
		}
		if t.Result != nil {
			return v.checkType(t.Result)
		}
	}
	return nil
}

func (v *verifier) checkSigTypes(name string, sig *FnType) error {
	if err := v.checkType(sig); err != nil {
		return fmt.Errorf("@%s: %w", name, err)
	}
	return nil
}

// checkBoundary enforces the copy rule on a boundary-crossing signature:
// no pointers, channels or futures anywhere in it.
func (v *verifier) checkBoundary(name string, sig *FnType) error {
	for _, param := range sig.Params {
		if bad := boundaryOffender(param); bad != "" {
			return fmt.Errorf("@%s crosses a language boundary; %s cannot: values copy at the boundary", name, bad)
		}
	}
	if sig.Result != nil {
		if bad := boundaryOffender(sig.Result); bad != "" {
			return fmt.Errorf("@%s crosses a language boundary; %s cannot: values copy at the boundary", name, bad)
		}
	}
	return nil
}

func boundaryOffender(t Type) string {
	switch t := t.(type) {
	case *Ptr:
		return t.String()
	case *ChanOf:
		return t.String()
	case *FutureOf:
		return t.String()
	case *Opt:
		return boundaryOffender(t.Elem)
	case *SliceOf:
		return boundaryOffender(t.Elem)
	case *ArrayOf:
		return boundaryOffender(t.Elem)
	case *TupleOf:
		for _, elem := range t.Elems {
			if bad := boundaryOffender(elem); bad != "" {
				return bad
			}
		}
	case *FnType:
		for _, param := range t.Params {
			if bad := boundaryOffender(param); bad != "" {
				return bad
			}
		}
		if t.Result != nil {
			return boundaryOffender(t.Result)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Function bodies
// ---------------------------------------------------------------------------

// regState tracks the one fixed type per register and which registers are
// definitely assigned on the current path.
type regState struct {
	types   map[string]Type
	defined map[string]bool
}

func (s *regState) clone() *regState {
	defined := make(map[string]bool, len(s.defined))
	for name := range s.defined {
		defined[name] = true
	}
	return &regState{types: s.types, defined: defined}
}

// intersect keeps only registers defined in both states.
func (s *regState) intersect(other *regState) {
	for name := range s.defined {
		if !other.defined[name] {
			delete(s.defined, name)
		}
	}
}

type funcVerifier struct {
	*verifier
	fn     *Func
	labels []string
}

func (v *verifier) checkFunc(fn *Func) error {
	state := &regState{types: map[string]Type{}, defined: map[string]bool{}}
	for _, param := range fn.Params {
		state.types[param.Name] = param.Type
		state.defined[param.Name] = true
	}
	fv := &funcVerifier{verifier: v, fn: fn}
	return fv.checkBody(fn.Body, state)
}

func (fv *funcVerifier) checkBody(body []Instr, state *regState) error {
	for _, instr := range body {
		if err := fv.checkInstr(instr, state); err != nil {
			return err
		}
	}
	return nil
}

func (fv *funcVerifier) use(state *regState, name string) (Type, error) {
	typ, known := state.types[name]
	if !known {
		return nil, fmt.Errorf("%%%s is never assigned", name)
	}
	if !state.defined[name] {
		return nil, fmt.Errorf("%%%s may not be assigned on this path", name)
	}
	return typ, nil
}

func (fv *funcVerifier) def(state *regState, name string, typ Type) error {
	if err := fv.checkType(typ); err != nil {
		return err
	}
	if existing, ok := state.types[name]; ok {
		if !TypesEqual(existing, typ) {
			return fmt.Errorf("%%%s is %s; it cannot become %s — a register's type is fixed at first assignment",
				name, existing, typ)
		}
	} else {
		state.types[name] = typ
	}
	state.defined[name] = true
	return nil
}

func (fv *funcVerifier) uses(state *regState, names []string) ([]Type, error) {
	out := make([]Type, len(names))
	for i, name := range names {
		typ, err := fv.use(state, name)
		if err != nil {
			return nil, err
		}
		out[i] = typ
	}
	return out, nil
}

func (fv *funcVerifier) checkCallShape(state *regState, sig *FnType, args []string, dst, what string) error {
	if len(args) != len(sig.Params) {
		return fmt.Errorf("%s takes %d argument(s), got %d", what, len(sig.Params), len(args))
	}
	types, err := fv.uses(state, args)
	if err != nil {
		return err
	}
	for i, typ := range types {
		if !TypesEqual(typ, sig.Params[i]) {
			return fmt.Errorf("%s argument %d is %s, not %s", what, i+1, typ, sig.Params[i])
		}
	}
	if dst == "" {
		return nil
	}
	if sig.Result == nil {
		return fmt.Errorf("%s returns nothing; there is no value to bind", what)
	}
	result := sig.Result
	if sig.Async {
		result = &FutureOf{Result: result}
	}
	return fv.def(state, dst, result)
}

func (fv *funcVerifier) hasLabel(label string) bool {
	for _, candidate := range fv.labels {
		if candidate == label {
			return true
		}
	}
	return false
}

func (fv *funcVerifier) withLabel(label string, body []Instr, state *regState) error {
	fv.labels = append(fv.labels, label)
	err := fv.checkBody(body, state)
	fv.labels = fv.labels[:len(fv.labels)-1]
	return err
}

func intKind(t Type) bool {
	prim, ok := t.(*Prim)
	if !ok {
		return false
	}
	switch prim.Kind {
	case I8, I16, I32, I64, U8, U16, U32, U64, Usize:
		return true
	}
	return false
}

func floatKind(t Type) bool {
	prim, ok := t.(*Prim)
	return ok && (prim.Kind == F32 || prim.Kind == F64)
}

func orderedKind(t Type) bool {
	if intKind(t) || floatKind(t) {
		return true
	}
	prim, ok := t.(*Prim)
	return ok && prim.Kind == Rune
}

func convertibleKind(t Type) bool {
	return orderedKind(t)
}

func isStr(t Type) bool {
	prim, ok := t.(*Prim)
	return ok && prim.Kind == Str
}

func isBool(t Type) bool {
	prim, ok := t.(*Prim)
	return ok && prim.Kind == Bool
}

func isUsize(t Type) bool {
	prim, ok := t.(*Prim)
	return ok && prim.Kind == Usize
}

func (fv *funcVerifier) recordShape(name string) (*RecordShape, error) {
	decl := fv.types[name]
	if decl == nil {
		return nil, fmt.Errorf("unknown type %%%s", name)
	}
	if decl.Record == nil {
		return nil, fmt.Errorf("%%%s is an enum, not a record", name)
	}
	return decl.Record, nil
}

func (fv *funcVerifier) enumShape(name string) (*EnumShape, error) {
	decl := fv.types[name]
	if decl == nil {
		return nil, fmt.Errorf("unknown type %%%s", name)
	}
	if decl.Enum == nil {
		return nil, fmt.Errorf("%%%s is a record, not an enum", name)
	}
	return decl.Enum, nil
}

func (fv *funcVerifier) namedRecordOf(t Type) (*RecordShape, error) {
	named, ok := t.(*Named)
	if !ok {
		return nil, fmt.Errorf("%s is not a record", t)
	}
	return fv.recordShape(named.Name)
}

func (fv *funcVerifier) namedEnumOf(t Type) (*EnumShape, error) {
	named, ok := t.(*Named)
	if !ok {
		return nil, fmt.Errorf("%s is not an enum", t)
	}
	return fv.enumShape(named.Name)
}

func elemOfIndexable(t Type) Type {
	switch t := t.(type) {
	case *SliceOf:
		return t.Elem
	case *ArrayOf:
		return t.Elem
	}
	return nil
}

// checkConstValue validates the literal against its primitive.
func checkConstValue(prim *Prim, value string) error {
	switch {
	case intKind(prim):
		if _, err := strconv.ParseInt(value, 0, 64); err != nil {
			if _, uerr := strconv.ParseUint(value, 0, 64); uerr != nil {
				return fmt.Errorf("%q is not an integer literal", value)
			}
		}
	case floatKind(prim):
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return fmt.Errorf("%q is not a float literal", value)
		}
	case prim.Kind == Bool:
		if value != "true" && value != "false" {
			return fmt.Errorf("%q is not a bool literal", value)
		}
	case prim.Kind == Rune:
		if len(value) < 3 || value[0] != '\'' {
			return fmt.Errorf("%q is not a rune literal", value)
		}
	case prim.Kind == Str:
		if len(value) < 2 || value[0] != '"' {
			return fmt.Errorf("%q is not a str literal", value)
		}
	}
	return nil
}
