package metals

import (
	"fmt"
	"strconv"
)

// Verify type-checks a module with no trust in its producer: every
// register is assigned before use on all paths and keeps one type, every
// instruction's operands check, and boundary rules hold on every import
// from another runtime. What a module *exports* is the linker's to check,
// because only it knows who imports the name and out of which runtime.
// It returns the first violation found.
func Verify(m *Module) error {
	_, err := verifyModule(m, false)
	return err
}

// RegisterTypes verifies the module and returns every function's
// register types, keyed by function name and then register name. The
// native backend sizes frame slots from this instead of re-deriving
// the type of every register.
func RegisterTypes(m *Module) (map[string]map[string]Type, error) {
	return verifyModule(m, true)
}

func verifyModule(m *Module, collect bool) (map[string]map[string]Type, error) {
	v := &verifier{
		module:    m,
		types:     map[string]*TypeDecl{},
		callables: map[string]*FnType{},
		globals:   map[string]Type{},
		memories:  map[string]*Memory{},
	}
	if collect {
		v.regTypes = map[string]map[string]Type{}
	}

	// An imported type is registered first and is checked like any other:
	// a module that repeats a shape has to be able to lay it out, and
	// whether the other module agrees is Link's question, not this one's.
	for _, imp := range m.Imports {
		for _, decl := range imp.Types {
			if _, dup := v.types[decl.Name]; dup {
				return nil, fmt.Errorf("type %%%s is imported twice", decl.Name)
			}
			v.types[decl.Name] = decl
		}
	}
	for _, decl := range m.Types {
		if _, dup := v.types[decl.Name]; dup {
			return nil, fmt.Errorf("type %%%s is declared twice", decl.Name)
		}
		v.types[decl.Name] = decl
	}
	for _, imp := range m.Imports {
		for _, decl := range imp.Types {
			if err := v.checkDeclShapes(decl); err != nil {
				return nil, err
			}
		}
	}
	for _, decl := range m.Types {
		if err := v.checkDeclShapes(decl); err != nil {
			return nil, err
		}
	}

	for _, imp := range m.Imports {
		foreign := !SameRuntime(m.Name, imp.Module)
		native := NativeLibrary(imp.Module)
		for _, fn := range imp.Funcs {
			if _, dup := v.callables[fn.Name]; dup {
				return nil, fmt.Errorf("@%s is declared twice", fn.Name)
			}
			v.callables[fn.Name] = fn.Sig
			if err := v.checkSigTypes(fn.Name, fn.Sig); err != nil {
				return nil, err
			}
			if foreign {
				if err := checkBoundary(fn.Name, fn.Sig, v.types, native); err != nil {
					return nil, err
				}
			}
		}
	}

	for _, fn := range m.Funcs {
		if _, dup := v.callables[fn.Name]; dup {
			return nil, fmt.Errorf("@%s is declared twice", fn.Name)
		}
		v.callables[fn.Name] = fn.Sig()
	}

	for _, mem := range m.Memories {
		if _, dup := v.memories[mem.Name]; dup {
			return nil, fmt.Errorf("memory @%s is declared twice", mem.Name)
		}
		if mem.Min < 0 || (mem.Max != 0 && mem.Max < mem.Min) {
			return nil, fmt.Errorf("memory @%s: %d pages minimum, %d maximum", mem.Name, mem.Min, mem.Max)
		}
		v.memories[mem.Name] = mem
	}
	for _, seg := range m.Data {
		mem, ok := v.memories[seg.Memory]
		if !ok {
			return nil, fmt.Errorf("data segment names no memory @%s", seg.Memory)
		}
		// A segment that does not fit the size its memory starts at would
		// have to grow it before anything ran, which is a producer bug
		// rather than a program that traps.
		end := seg.Offset + int64(len(seg.Bytes))
		if seg.Offset < 0 || end > mem.Min*PageSize {
			return nil, fmt.Errorf("data segment at %d..%d does not fit memory @%s's %d page(s)",
				seg.Offset, end, mem.Name, mem.Min)
		}
	}

	for _, global := range m.Globals {
		if err := v.checkType(global.Type); err != nil {
			return nil, fmt.Errorf("global @%s: %w", global.Name, err)
		}
		if _, dup := v.globals[global.Name]; dup {
			return nil, fmt.Errorf("global @%s is declared twice", global.Name)
		}
		v.globals[global.Name] = global.Type
	}
	if err := checkGlobalImages(m); err != nil {
		return nil, err
	}

	if m.Init != "" {
		// An initializer runs before anything has a value to pass it or a
		// place to put its answer, so it takes nothing and returns
		// nothing. It is also the module's own, not an import: a module
		// arranges its own storage.
		init := findFunc(m, m.Init)
		switch {
		case init == nil:
			return nil, fmt.Errorf("init names @%s, which this module does not declare", m.Init)
		case init.Async:
			return nil, fmt.Errorf("init @%s is async, and nothing exists yet to await it", m.Init)
		case len(init.Params) > 0 || init.Result != nil:
			return nil, fmt.Errorf("init @%s takes %s, and must take and return nothing",
				m.Init, init.Sig())
		}
	}

	for _, fn := range m.Funcs {
		if err := v.checkSigTypes(fn.Name, fn.Sig()); err != nil {
			return nil, err
		}
		if err := v.checkFunc(fn); err != nil {
			return nil, fmt.Errorf("fn @%s: %w", fn.Name, err)
		}
	}
	return v.regTypes, nil
}

// checkGlobalImages holds a global's static bytes to the storage they
// claim to be: exactly the size the layout table says, no more and no
// less, since a short image would leave the rest of the object to guess
// and a long one would write past it.
//
// A pointer has no static image. Its value is not known until the
// program is laid out, so a producer writes one in the module's Init
// instead — and a type that contains a pointer anywhere inside it cannot
// be spelled in bytes for the same reason.
func checkGlobalImages(m *Module) (err error) {
	var withImage []*Global
	for _, global := range m.Globals {
		if global.Bytes != nil {
			withImage = append(withImage, global)
		}
	}
	if len(withImage) == 0 {
		return nil
	}

	// The layout table reports a type it cannot lay out by calling out
	// and not returning; here that is this function's error.
	defer func() {
		switch caught := recover().(type) {
		case nil:
		case layoutFailure:
			err = caught.err
		default:
			panic(caught)
		}
	}()
	layout := NewLayout(m, func(format string, args ...any) {
		panic(layoutFailure{fmt.Errorf(format, args...)})
	})

	for _, global := range withImage {
		if bad := pointerInside(m, global.Type, map[string]bool{}); bad != nil {
			return fmt.Errorf("global @%s: %s holds a %s, which has no value until the program is laid out",
				global.Name, global.Type, bad)
		}
		if size := layout.Size(global.Type); len(global.Bytes) != size {
			return fmt.Errorf("global @%s: %d bytes of storage for a %s, which is %d",
				global.Name, len(global.Bytes), global.Type, size)
		}
	}
	return nil
}

type layoutFailure struct{ err error }

// pointerInside finds a type anywhere inside t whose value a static
// image cannot carry, and returns it, or nil when the whole of t is
// plain bytes.
func pointerInside(m *Module, t Type, seen map[string]bool) Type {
	switch t := t.(type) {
	case *Prim:
		// A str is a pointer to the runtime's own blob, whatever else it
		// looks like from here.
		if t.Kind == Str {
			return t
		}
		return nil
	case *Ptr, *AnyPtr, *ChanOf, *FutureOf, *Handle, *CodeType, *FnType, *SliceOf, *Any:
		return t
	case *Opt:
		return pointerInside(m, t.Elem, seen)
	case *ArrayOf:
		return pointerInside(m, t.Elem, seen)
	case *TupleOf:
		for _, elem := range t.Elems {
			if bad := pointerInside(m, elem, seen); bad != nil {
				return bad
			}
		}
		return nil
	case *Named:
		// A record that reaches itself does so through a pointer, which
		// has already been refused above; the guard is for the cyclic
		// declaration the layout table reports separately.
		if seen[t.Name] {
			return nil
		}
		seen[t.Name] = true
		defer delete(seen, t.Name)

		decl := findType(m, t.Name)
		if decl == nil || decl.Opaque {
			return t
		}
		if members := decl.Members(); decl.Enum == nil {
			for _, member := range members {
				if bad := pointerInside(m, member.Type, seen); bad != nil {
					return bad
				}
			}
			return nil
		}
		for _, variant := range decl.Enum.Variants {
			for _, payload := range variant.Payload {
				if bad := pointerInside(m, payload, seen); bad != nil {
					return bad
				}
			}
		}
		return nil
	}
	return t
}

// unionInside names a union reachable inside a type by value, or "". It
// is what equality asks: comparing storage nothing records the meaning
// of has no answer, and a record holding a union is that question one
// level down. A pointer to one is not — an address compares whatever is
// at the other end.
func unionInside(t Type, types map[string]*TypeDecl, seen map[string]bool) string {
	switch t := t.(type) {
	case *ArrayOf:
		return unionInside(t.Elem, types, seen)
	case *TupleOf:
		for _, elem := range t.Elems {
			if bad := unionInside(elem, types, seen); bad != "" {
				return bad
			}
		}
	case *Opt:
		return unionInside(t.Elem, types, seen)
	case *Named:
		decl := types[t.Name]
		if decl == nil || seen[t.Name] {
			return ""
		}
		seen[t.Name] = true
		if decl.Union != nil {
			return "%" + t.Name
		}
		for _, member := range decl.Members() {
			if bad := unionInside(member.Type, types, seen); bad != "" {
				return bad
			}
		}
		if decl.Enum != nil {
			for _, variant := range decl.Enum.Variants {
				for _, payload := range variant.Payload {
					if bad := unionInside(payload, types, seen); bad != "" {
						return bad
					}
				}
			}
		}
	}
	return ""
}

func findType(m *Module, name string) *TypeDecl {
	for _, decl := range m.Types {
		if decl.Name == name {
			return decl
		}
	}
	for _, imp := range m.Imports {
		for _, decl := range imp.Types {
			if decl.Name == name {
				return decl
			}
		}
	}
	return nil
}

type verifier struct {
	module    *Module
	types     map[string]*TypeDecl
	callables map[string]*FnType
	globals   map[string]Type
	memories  map[string]*Memory
	// regTypes collects each function's register types when non-nil;
	// clones share one types map per function, so after a body checks
	// out the map holds every register the function ever assigns.
	regTypes map[string]map[string]Type
}

// ---------------------------------------------------------------------------
// Declarations and signatures
// ---------------------------------------------------------------------------

func (v *verifier) checkDeclShapes(decl *TypeDecl) error {
	if decl.Opaque {
		if decl.Record != nil || decl.Union != nil || decl.Enum != nil {
			return fmt.Errorf("type %%%s is opaque and also has a shape", decl.Name)
		}
		return nil
	}
	// Nothing is aligned more than a word, which is what keeps an enum's
	// payload at a fixed offset and a frame's cells word-aligned.
	if n := decl.Align; n < 0 || n > Word || (n&(n-1)) != 0 {
		return fmt.Errorf("type %%%s is aligned to %d, and an alignment is a power of two up to %d",
			decl.Name, n, Word)
	}
	if decl.Union != nil {
		if len(decl.Union.Members) == 0 {
			return fmt.Errorf("type %%%s is a union of nothing, which has no storage to share", decl.Name)
		}
		for _, member := range decl.Union.Members {
			if err := v.checkType(member.Type); err != nil {
				return fmt.Errorf("type %%%s, member %s: %w", decl.Name, member.Name, err)
			}
		}
		return nil
	}
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

// checkType walks a type and requires every Named reference to resolve,
// and every one of them to have a shape — except directly under a
// pointer, which is the one position an opaque name is written in.
func (v *verifier) checkType(t Type) error {
	switch t := t.(type) {
	case *Named:
		decl := v.types[t.Name]
		if decl == nil {
			return fmt.Errorf("unknown type %%%s", t.Name)
		}
		if decl.Opaque {
			return fmt.Errorf("type %%%s is opaque; only a pointer to it has a layout", t.Name)
		}
	case *Ptr:
		// A pointer may address a type this module cannot see inside.
		// Nothing that would need the target's layout accepts one, so
		// letting the name through here costs no checking.
		if named, ok := t.Elem.(*Named); ok {
			if v.types[named.Name] == nil {
				return fmt.Errorf("unknown type %%%s", named.Name)
			}
			return nil
		}
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
	case *CodeType:
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

// checkBoundary enforces the copy rule on a signature crossing out of
// this runtime. The types are the declaring module's, since a %Named
// resolves there and nowhere else.
//
// `native` says the far side is a linked C library rather than another
// runtime — same process, same address space, same allocator — which is
// what decides whether an address may cross. See NativeLibrary. A
// channel and a future are refused either way: those are Gyro runtime
// objects with no C representation, which is a different reason than the
// heap.
//
// The verifier calls this on an import out of another runtime and the
// linker on the export it resolves to; both of them are the side that
// can see two runtimes at once.
func checkBoundary(name string, sig *FnType, types map[string]*TypeDecl, native bool) error {
	for _, param := range sig.Params {
		if bad := boundaryOffender(param, types, map[string]bool{}, native); bad != "" {
			return fmt.Errorf("@%s crosses a runtime boundary; %s cannot: values copy at the boundary", name, bad)
		}
	}
	if sig.Result != nil {
		if bad := boundaryOffender(sig.Result, types, map[string]bool{}, native); bad != "" {
			return fmt.Errorf("@%s crosses a runtime boundary; %s cannot: values copy at the boundary", name, bad)
		}
	}
	return nil
}

// boundaryOffender names the part of a type that cannot cross, or "". A
// record is walked rather than waved through: a struct crossing by value
// is its fields crossing by value, and one of them holding a pointer is
// the same shared mutable state a bare pointer would be — across a
// runtime boundary, where the far side has a heap of its own.
func boundaryOffender(t Type, types map[string]*TypeDecl, seen map[string]bool, native bool) string {
	switch t := t.(type) {
	case *Ptr:
		if native {
			// A linked library dereferences this address on this thread,
			// in this address space. Nothing is copied because there is
			// no second heap to copy into.
			return boundaryOffender(t.Elem, types, seen, native)
		}
		return t.String()
	case *AnyPtr:
		// Erasing the target's type does not make an address portable.
		// It is still this program's address space — which is exactly why
		// it crosses to a library sharing that address space and to
		// nothing else.
		if native {
			return ""
		}
		return t.String()
	case *ChanOf:
		return t.String()
	case *FutureOf:
		return t.String()
	case *CodeType:
		// An address of code means what a pointer means: something in
		// this program's address space. A linked library can call one; a
		// runtime with its own heap has no such address to call.
		if native {
			for _, param := range t.Params {
				if bad := boundaryOffender(param, types, seen, native); bad != "" {
					return bad
				}
			}
			if t.Result != nil {
				return boundaryOffender(t.Result, types, seen, native)
			}
			return ""
		}
		return t.String()
	case *Opt:
		return boundaryOffender(t.Elem, types, seen, native)
	case *SliceOf:
		return boundaryOffender(t.Elem, types, seen, native)
	case *ArrayOf:
		return boundaryOffender(t.Elem, types, seen, native)
	case *TupleOf:
		for _, elem := range t.Elems {
			if bad := boundaryOffender(elem, types, seen, native); bad != "" {
				return bad
			}
		}
	case *FnType:
		for _, param := range t.Params {
			if bad := boundaryOffender(param, types, seen, native); bad != "" {
				return bad
			}
		}
		if t.Result != nil {
			return boundaryOffender(t.Result, types, seen, native)
		}
	case *Named:
		decl := types[t.Name]
		if decl == nil || decl.Opaque || seen[t.Name] {
			return ""
		}
		seen[t.Name] = true
		if decl.Union != nil {
			// A union is storage with nothing recording which member is
			// live. Copying it across a boundary would hand the far side
			// a shape it has no way to read, so the copy rule refuses it
			// rather than making one up. A native library is not copied
			// to, and reads the member C's own way.
			if native {
				return ""
			}
			return "%" + t.Name + ", a union"
		}
		if decl.Record != nil {
			for _, field := range decl.Record.Fields {
				if bad := boundaryOffender(field.Type, types, seen, native); bad != "" {
					return bad
				}
			}
			return ""
		}
		for _, variant := range decl.Enum.Variants {
			for _, payload := range variant.Payload {
				if bad := boundaryOffender(payload, types, seen, native); bad != "" {
					return bad
				}
			}
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
	// unreachable marks the path after return, abort or br: nothing on
	// it runs, so its definedness does not constrain a join.
	unreachable bool
}

func (s *regState) clone() *regState {
	defined := make(map[string]bool, len(s.defined))
	for name := range s.defined {
		defined[name] = true
	}
	return &regState{types: s.types, defined: defined, unreachable: s.unreachable}
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
	// reachesMemory and claimsAddress are what the function did with
	// linear memory and with ptr.from. One function may not do both:
	// whichever comes first, the other would make an offset into a
	// sandboxed memory a place in the process. The rule is per function
	// rather than per module because linking merges modules and keeps
	// each one's memories, so a module is gone by the time the linked
	// program is verified again, and a function is not.
	reachesMemory, claimsAddress bool
}

func (v *verifier) checkFunc(fn *Func) error {
	state := &regState{types: map[string]Type{}, defined: map[string]bool{}}
	for _, param := range fn.Params {
		state.types[param.Name] = param.Type
		state.defined[param.Name] = true
	}
	fv := &funcVerifier{verifier: v, fn: fn}
	if err := fv.checkBody(fn.Body, state); err != nil {
		return err
	}
	if fv.reachesMemory && fv.claimsAddress {
		return fmt.Errorf("ptr.from and a linear memory in one function; a linear address is never a pointer")
	}
	if v.regTypes != nil {
		v.regTypes[fn.Name] = state.types
	}
	return nil
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
	if !state.defined[name] && !state.unreachable {
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
	case I8, I16, I32, I64, U8, U16, U32, U64, Usize, Isize:
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

// numericKind is what bitcast reinterprets between: the integers and the
// floats, but not rune or bool, whose valid values are a subset of their
// width's and so cannot be produced from arbitrary bits.
func numericKind(t Type) bool {
	return intKind(t) || floatKind(t)
}

func isStr(t Type) bool {
	prim, ok := t.(*Prim)
	return ok && prim.Kind == Str
}

func isBool(t Type) bool {
	prim, ok := t.(*Prim)
	return ok && prim.Kind == Bool
}

// isSyscallWord is a type the kernel can read from one register: an
// integer, a bool, or a pointer whose null, when it has one, is zero.
func isSyscallWord(t Type) bool {
	switch t := t.(type) {
	case *Prim:
		switch t.Kind {
		case I8, I16, I32, I64, Isize, U8, U16, U32, U64, Usize, Bool:
			return true
		}
	case *Ptr, *AnyPtr:
		return true
	case *Opt:
		switch t.Elem.(type) {
		case *Ptr, *AnyPtr:
			return true
		}
	}
	return false
}

func isUsize(t Type) bool {
	prim, ok := t.(*Prim)
	return ok && prim.Kind == Usize
}

// isU32 is the type of every linear-memory address, length and page
// count. One width for all of them is what keeps a producer from having
// to decide, and it is wasm32's.
func isU32(t Type) bool {
	prim, ok := t.(*Prim)
	return ok && prim.Kind == U32
}

func isU8(t Type) bool {
	prim, ok := t.(*Prim)
	return ok && prim.Kind == U8
}

// memory resolves a linear memory a instruction names.
func (fv *funcVerifier) memory(name string) error {
	if fv.memories[name] == nil {
		return fmt.Errorf("no memory @%s", name)
	}
	fv.reachesMemory = true
	return nil
}

// memAddr is the check every access shares: the memory resolves and the
// address is a u32.
func (fv *funcVerifier) memAddr(state *regState, memory, addr, what string) error {
	if err := fv.memory(memory); err != nil {
		return err
	}
	typ, err := fv.use(state, addr)
	if err != nil {
		return err
	}
	if !isU32(typ) {
		return fmt.Errorf("%s addresses memory with a u32, not %s", what, typ)
	}
	return nil
}

// memCounts requires every named register to be a u32, which is what the
// remaining address and length operands of mem.copy and mem.fill are.
func (fv *funcVerifier) memCounts(state *regState, what string, names ...string) error {
	for _, name := range names {
		typ, err := fv.use(state, name)
		if err != nil {
			return err
		}
		if !isU32(typ) {
			return fmt.Errorf("%s counts bytes in u32, not %s", what, typ)
		}
	}
	return nil
}

// sized refuses an opaque target where the layout of what a pointer
// addresses is what the instruction is made of. Reading, writing,
// stepping and subtracting all measure in the target's size, and an
// opaque type has none — which is exactly why C refuses the same four on
// a pointer to an incomplete type.
//
// Only a bare name has to be asked: an opaque type nested inside any
// other type is already refused by checkType, since a pointer's target
// is the one position it is allowed in.
func (fv *funcVerifier) sized(t Type, what string) error {
	named, ok := t.(*Named)
	if !ok {
		return nil
	}
	if decl := fv.types[named.Name]; decl != nil && decl.Opaque {
		return fmt.Errorf("%s needs %%%s's layout, and it is opaque", what, named.Name)
	}
	return nil
}

func (fv *funcVerifier) recordShape(name string) (*RecordShape, error) {
	decl := fv.types[name]
	if decl == nil {
		return nil, fmt.Errorf("unknown type %%%s", name)
	}
	if decl.Opaque {
		return nil, fmt.Errorf("%%%s is opaque, so it has no fields to reach", name)
	}
	if decl.Record == nil {
		if decl.Union != nil {
			return nil, fmt.Errorf("%%%s is a union, not a record", name)
		}
		return nil, fmt.Errorf("%%%s is an enum, not a record", name)
	}
	return decl.Record, nil
}

// memberedDecl is what field.ptr reaches into: a record or a union. The
// two answer the same question about names and types and different ones
// about offsets, which is the layout's business rather than this one's.
func (fv *funcVerifier) memberedDecl(t Type) (*TypeDecl, error) {
	named, ok := t.(*Named)
	if !ok {
		return nil, fmt.Errorf("%s has no members", t)
	}
	decl := fv.types[named.Name]
	if decl == nil {
		return nil, fmt.Errorf("unknown type %%%s", named.Name)
	}
	switch {
	case decl.Opaque:
		return nil, fmt.Errorf("%%%s is opaque, so it has no fields to reach", named.Name)
	case decl.Enum != nil:
		return nil, fmt.Errorf("%%%s is an enum, not a record or a union", named.Name)
	}
	return decl, nil
}

func (fv *funcVerifier) enumShape(name string) (*EnumShape, error) {
	decl := fv.types[name]
	if decl == nil {
		return nil, fmt.Errorf("unknown type %%%s", name)
	}
	if decl.Opaque {
		return nil, fmt.Errorf("%%%s is opaque, so it has no variants", name)
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
