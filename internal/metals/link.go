package metals

import (
	"fmt"
	"strings"
)

// Link merges a program's modules into the one module every consumer
// takes. It is the pass docs/metals.md leaves open: an importer declares
// the signatures it expects, an exporter has actual ones, and nothing
// until now compared them.
//
// modules arrives in dependency order — a module's dependencies before
// it, the root last — which is the order the loader already produces.
//
// Every symbol a non-root module owns is qualified with its module name,
// so two producers may both call a function `fib` and both declare a
// record `Pair` without colliding. Qualified names are exactly what the
// text format's quoted spelling exists for: `@"lisp:fib::fib"`.
//
// An import of another module in the program becomes a direct reference
// to that module's qualified symbol, checked against what the exporter
// actually declares. An import of anything else — `gyro:rt` and the
// runtime modules generally — survives into the output, deduplicated.
func Link(modules []*Module) (*Module, error) {
	if len(modules) == 0 {
		return nil, fmt.Errorf("link: a program needs at least one module")
	}
	if len(modules) == 1 {
		only := modules[0]
		if err := applyInits(only, []string{only.Init}); err != nil {
			return nil, err
		}
		return only, nil
	}

	l := &linker{
		byName:    map[string]*Module{},
		exports:   map[string]map[string]*Func{},
		typeDecls: map[string]map[string]*TypeDecl{},
	}
	for _, m := range modules {
		if _, dup := l.byName[m.Name]; dup {
			return nil, fmt.Errorf("link: module %q appears twice", m.Name)
		}
		l.byName[m.Name] = m

		byFunc := map[string]*Func{}
		for _, fn := range m.Funcs {
			if fn.Export {
				byFunc[fn.Name] = fn
			}
		}
		l.exports[m.Name] = byFunc

		byType := map[string]*TypeDecl{}
		for _, decl := range m.Types {
			byType[decl.Name] = decl
		}
		l.typeDecls[m.Name] = byType
	}

	root := modules[len(modules)-1]
	l.root = root
	out := &Module{Name: root.Name}
	seenImport := map[string]bool{}
	var inits []string

	for _, m := range modules {
		if err := l.check(m); err != nil {
			return nil, err
		}
	}
	for _, m := range modules {
		l.current = m
		l.rootModule = m == root

		for _, imp := range m.Imports {
			if _, internal := l.byName[imp.Module]; internal {
				continue
			}
			for _, fn := range imp.Funcs {
				key := imp.Module + "\x00" + fn.Name
				if seenImport[key] {
					continue
				}
				seenImport[key] = true
				out.addImport(imp.Module, fn)
			}
		}
		for _, decl := range m.Types {
			out.Types = append(out.Types, &TypeDecl{
				Name:   l.typeName(decl.Name),
				Record: l.record(decl.Record),
				Enum:   l.enum(decl.Enum),
			})
		}
		// Each module keeps its own linear memory: two C modules linked
		// together are two sandboxes, not one heap they can reach across.
		// That is the conservative reading, and the one the boundary rule
		// already implies — sharing storage is what a boundary crossing
		// exists not to do.
		for _, mem := range m.Memories {
			out.Memories = append(out.Memories, &Memory{
				Name: l.symbol(mem.Name),
				Min:  mem.Min,
				Max:  mem.Max,
			})
		}
		for _, seg := range m.Data {
			out.Data = append(out.Data, &DataSeg{
				Memory: l.symbol(seg.Memory),
				Offset: seg.Offset,
				Bytes:  seg.Bytes,
			})
		}
		for _, global := range m.Globals {
			out.Globals = append(out.Globals, &Global{
				Name:  l.symbol(global.Name),
				Type:  l.rewrite(global.Type),
				Bytes: global.Bytes,
			})
		}
		for _, fn := range m.Funcs {
			out.Funcs = append(out.Funcs, l.function(fn))
		}
		if m.Init != "" {
			inits = append(inits, l.symbol(m.Init))
		}
	}
	if err := applyInits(out, inits); err != nil {
		return nil, err
	}
	return out, nil
}

// applyInits is where a module's initializer becomes a call. The names
// arrive in dependency order, so an importer's storage is written after
// everything it imports has written its own, and every one of them
// before @main's first instruction.
//
// The linked module carries no Init of its own afterwards: the calls are
// in the program now, and a second link would emit them twice.
func applyInits(out *Module, names []string) error {
	out.Init = ""

	var calls []Instr
	for _, name := range names {
		if name == "" {
			continue
		}
		calls = append(calls, &Call{Callee: name})
	}
	if len(calls) == 0 {
		return nil
	}

	main := findFunc(out, "main")
	if main == nil {
		return fmt.Errorf("link: @%s initializes module storage, but the program has no @main to run it before",
			trimName(calls[0].(*Call).Callee))
	}
	main.Body = append(calls, main.Body...)
	return nil
}

// trimName is the unqualified half of a linked symbol, for a message
// that would otherwise name a module twice.
func trimName(symbol string) string {
	if at := strings.Index(symbol, "::"); at >= 0 {
		return symbol[at+2:]
	}
	return symbol
}

// check verifies a module's imports against what the program actually
// exports: the name must be there, exported, and its signature must match
// structurally. A mismatch here is the error the format had nowhere to
// report before.
func (l *linker) check(m *Module) error {
	for _, imp := range m.Imports {
		exporter, internal := l.byName[imp.Module]
		if !internal {
			continue
		}
		for _, want := range imp.Funcs {
			have, ok := l.exports[exporter.Name][want.Name]
			if !ok {
				if hidden := findFunc(exporter, want.Name); hidden != nil {
					return fmt.Errorf("link: %s imports @%s from %q, which declares it but does not export it",
						m.Name, want.Name, imp.Module)
				}
				return fmt.Errorf("link: %s imports @%s from %q, which does not declare it",
					m.Name, want.Name, imp.Module)
			}
			if !TypesEqual(want.Sig, have.Sig()) {
				return fmt.Errorf("link: %s imports @%s from %q as %s, but it is %s",
					m.Name, want.Name, imp.Module, want.Sig, have.Sig())
			}
			// An export obeys the boundary rules when something out of
			// another runtime imports it, and that is a question with two
			// sides — which is why it is asked here and not in Verify,
			// where a module can only see its own name.
			if !SameRuntime(m.Name, exporter.Name) {
				if err := checkBoundary(want.Name, have.Sig(), l.typeDecls[exporter.Name]); err != nil {
					return fmt.Errorf("link: %s imports @%s from %q: %w",
						m.Name, want.Name, imp.Module, err)
				}
			}
		}
	}
	return nil
}

func findFunc(m *Module, name string) *Func {
	for _, fn := range m.Funcs {
		if fn.Name == name {
			return fn
		}
	}
	return nil
}

type linker struct {
	byName  map[string]*Module
	exports map[string]map[string]*Func
	// typeDecls is each module's named types, which is where a %Named in
	// its signatures resolves and nowhere else.
	typeDecls map[string]map[string]*TypeDecl
	// root is the module whose names survive unqualified; current is the
	// one being rewritten.
	root       *Module
	current    *Module
	rootModule bool
}

// symbol qualifies a name the current module owns. The root keeps its
// names so that @main stays @main and a single-module program lowers to
// exactly what it did before linking existed.
func (l *linker) symbol(name string) string {
	if l.rootModule {
		return name
	}
	return l.current.Name + "::" + name
}

func (l *linker) typeName(name string) string { return l.symbol(name) }

// callee resolves a name the current module referenced. It is either an
// import of another module in the program — in which case it names that
// module's symbol — or one of the current module's own.
func (l *linker) callee(name string) string {
	for _, imp := range l.current.Imports {
		exporter, internal := l.byName[imp.Module]
		if !internal {
			continue
		}
		for _, fn := range imp.Funcs {
			if fn.Name != name {
				continue
			}
			if exporter == l.root {
				return fn.Name
			}
			return exporter.Name + "::" + fn.Name
		}
	}
	return l.symbol(name)
}

func (l *linker) function(fn *Func) *Func {
	params := make([]Param, len(fn.Params))
	for i, param := range fn.Params {
		params[i] = Param{Name: param.Name, Type: l.rewrite(param.Type)}
	}
	return &Func{
		Name: l.symbol(fn.Name),
		// Only the root's main is the program's entry point; an
		// exported main from a dependency is an ordinary function once
		// its name is qualified.
		Export: fn.Export,
		Async:  fn.Async,
		Params: params,
		Result: l.rewrite(fn.Result),
		Body:   l.body(fn.Body),
	}
}

func (l *linker) record(shape *RecordShape) *RecordShape {
	if shape == nil {
		return nil
	}
	fields := make([]FieldDecl, len(shape.Fields))
	for i, field := range shape.Fields {
		fields[i] = FieldDecl{Name: field.Name, Type: l.rewrite(field.Type)}
	}
	return &RecordShape{Fields: fields}
}

func (l *linker) enum(shape *EnumShape) *EnumShape {
	if shape == nil {
		return nil
	}
	variants := make([]VariantDecl, len(shape.Variants))
	for i, variant := range shape.Variants {
		payload := make([]Type, len(variant.Payload))
		for j, elem := range variant.Payload {
			payload[j] = l.rewrite(elem)
		}
		variants[i] = VariantDecl{Name: variant.Name, Payload: payload}
	}
	return &EnumShape{Variants: variants}
}

// rewrite renames the module-local types a type mentions. Everything else
// is structural and survives untouched.
func (l *linker) rewrite(t Type) Type {
	switch t := t.(type) {
	case nil:
		return nil
	case *Named:
		return &Named{Name: l.typeName(t.Name)}
	case *Ptr:
		return &Ptr{Elem: l.rewrite(t.Elem)}
	case *Opt:
		return &Opt{Elem: l.rewrite(t.Elem)}
	case *SliceOf:
		return &SliceOf{Elem: l.rewrite(t.Elem)}
	case *ArrayOf:
		return &ArrayOf{Elem: l.rewrite(t.Elem), Len: t.Len}
	case *ChanOf:
		return &ChanOf{Elem: l.rewrite(t.Elem)}
	case *FutureOf:
		return &FutureOf{Result: l.rewrite(t.Result)}
	case *TupleOf:
		elems := make([]Type, len(t.Elems))
		for i, elem := range t.Elems {
			elems[i] = l.rewrite(elem)
		}
		return &TupleOf{Elems: elems}
	case *FnType:
		params := make([]Type, len(t.Params))
		for i, param := range t.Params {
			params[i] = l.rewrite(param)
		}
		return &FnType{Params: params, Result: l.rewrite(t.Result), Async: t.Async}
	case *CodeType:
		params := make([]Type, len(t.Params))
		for i, param := range t.Params {
			params[i] = l.rewrite(param)
		}
		return &CodeType{Params: params, Result: l.rewrite(t.Result)}
	}
	return t
}

func (l *linker) body(body []Instr) []Instr {
	out := make([]Instr, len(body))
	for i, instr := range body {
		out[i] = l.instr(instr)
	}
	return out
}

// instr rebuilds one instruction with every name and type it carries
// rewritten. Instructions that name neither are returned as they are.
func (l *linker) instr(instr Instr) Instr {
	switch i := instr.(type) {
	case *Const:
		return &Const{Dst: i.Dst, Type: l.rewrite(i.Type), Value: i.Value, Null: i.Null}
	case *Conv:
		return &Conv{Dst: i.Dst, X: i.X, To: l.rewrite(i.To)}
	case *Trunc:
		return &Trunc{Dst: i.Dst, X: i.X, To: l.rewrite(i.To)}
	case *Bitcast:
		return &Bitcast{Dst: i.Dst, X: i.X, To: l.rewrite(i.To)}
	case *AnyUnwrap:
		return &AnyUnwrap{Dst: i.Dst, X: i.X, To: l.rewrite(i.To)}
	case *AnyIs:
		return &AnyIs{Dst: i.Dst, X: i.X, To: l.rewrite(i.To)}
	case *Slot:
		return &Slot{Dst: i.Dst, Type: l.rewrite(i.Type)}
	case *New:
		return &New{Dst: i.Dst, Type: l.rewrite(i.Type)}
	case *ChanNew:
		return &ChanNew{Dst: i.Dst, Elem: l.rewrite(i.Elem), Cap: i.Cap}
	case *MakeArray:
		return &MakeArray{Dst: i.Dst, Elem: l.rewrite(i.Elem), Args: i.Args}
	case *MakeRecord:
		return &MakeRecord{Dst: i.Dst, TypeName: l.typeName(i.TypeName), Args: i.Args}
	case *MakeVariant:
		return &MakeVariant{Dst: i.Dst, TypeName: l.typeName(i.TypeName), Variant: i.Variant, Args: i.Args}
	case *GlobalPtr:
		return &GlobalPtr{Dst: i.Dst, Name: l.symbol(i.Name)}
	case *MemLoad:
		return &MemLoad{Dst: i.Dst, Memory: l.symbol(i.Memory), Addr: i.Addr, Type: i.Type}
	case *MemStore:
		return &MemStore{Memory: l.symbol(i.Memory), Addr: i.Addr, Val: i.Val, Type: i.Type}
	case *MemSize:
		return &MemSize{Dst: i.Dst, Memory: l.symbol(i.Memory)}
	case *MemGrow:
		return &MemGrow{Dst: i.Dst, Memory: l.symbol(i.Memory), Pages: i.Pages}
	case *MemCopy:
		return &MemCopy{Memory: l.symbol(i.Memory), To: i.To, From: i.From, Len: i.Len}
	case *MemFill:
		return &MemFill{Memory: l.symbol(i.Memory), To: i.To, Byte: i.Byte, Len: i.Len}
	case *Call:
		return &Call{Dst: i.Dst, Callee: l.callee(i.Callee), Args: i.Args}
	case *FnRef:
		return &FnRef{Dst: i.Dst, Callee: l.callee(i.Callee)}
	case *CodeRef:
		return &CodeRef{Dst: i.Dst, Callee: l.callee(i.Callee)}
	case *MakeClosure:
		return &MakeClosure{Dst: i.Dst, Callee: l.callee(i.Callee), Env: i.Env}

	case *If:
		return &If{Cond: i.Cond, Then: l.body(i.Then), Else: l.body(i.Else)}
	case *Block:
		return &Block{Label: i.Label, Body: l.body(i.Body)}
	case *Loop:
		return &Loop{Label: i.Label, Body: l.body(i.Body)}
	case *Switch:
		cases := make([]SwitchCase, len(i.Cases))
		for n, c := range i.Cases {
			cases[n] = SwitchCase{Value: c.Value, Body: l.body(c.Body)}
		}
		return &Switch{X: i.X, Cases: cases, Default: l.body(i.Default)}
	case *Select:
		arms := make([]SelectArm, len(i.Arms))
		for n, arm := range i.Arms {
			arms[n] = SelectArm{Dst: arm.Dst, Ch: arm.Ch, Val: arm.Val, Send: arm.Send, Body: l.body(arm.Body)}
		}
		return &Select{Arms: arms, Default: l.body(i.Default)}
	}
	return instr
}

// addImport files one imported function under its module, reusing the
// group when the module is already there.
func (m *Module) addImport(module string, fn ImportedFunc) {
	for _, imp := range m.Imports {
		if imp.Module == module {
			imp.Funcs = append(imp.Funcs, fn)
			return
		}
	}
	m.Imports = append(m.Imports, &Import{Module: module, Funcs: []ImportedFunc{fn}})
}
