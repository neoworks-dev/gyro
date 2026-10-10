package metals

import (
	"fmt"
	"strconv"
	"strings"
)

// Print renders a module in the canonical text form docs/appendix-d-metals.md
// specifies. Parse reads it back; the round trip is exact.
func Print(m *Module) string {
	p := &printer{}
	p.printf("module %s", strconv.Quote(m.Name))
	p.line("")

	for _, imp := range m.Imports {
		p.printf("import %s {", strconv.Quote(imp.Module))
		p.indent++
		for _, decl := range imp.Types {
			p.typeDecl(decl)
		}
		for _, fn := range imp.Funcs {
			p.line("fn " + at(fn.Name) + sigSuffix(fn.Sig))
		}
		p.indent--
		p.line("}")
		p.line("")
	}

	for _, decl := range m.Types {
		p.typeDecl(decl)
	}
	if len(m.Types) > 0 {
		p.line("")
	}

	for _, mem := range m.Memories {
		if mem.Max > 0 {
			p.printf("memory %s %d, %d", at(mem.Name), mem.Min, mem.Max)
			continue
		}
		p.printf("memory %s %d", at(mem.Name), mem.Min)
	}
	for _, seg := range m.Data {
		p.printf("data %s, %d, %s", at(seg.Memory), seg.Offset, quoteBytes(seg.Bytes))
	}
	if len(m.Memories) > 0 {
		p.line("")
	}

	for _, global := range m.Globals {
		prefix := ""
		if global.ThreadLocal {
			prefix = "thread_local "
		}
		if global.Bytes != nil {
			p.printf("%sglobal %s: %s = %s", prefix, at(global.Name), global.Type, quoteBytes(global.Bytes))
			continue
		}
		p.printf("%sglobal %s: %s", prefix, at(global.Name), global.Type)
	}
	if len(m.Globals) > 0 {
		p.line("")
	}

	if m.Init != "" {
		p.printf("init %s", at(m.Init))
		p.line("")
	}

	for i, fn := range m.Funcs {
		if i > 0 {
			p.line("")
		}
		p.function(fn)
	}
	return p.out.String()
}

// quoteBytes renders a data segment's contents. Go's quoting escapes a
// byte that is not printable UTF-8 as \xNN and reads it back as the same
// byte, so the round trip is exact for arbitrary binary.
func quoteBytes(b []byte) string { return strconv.Quote(string(b)) }

// reg, at and lbl render a name behind its sigil, quoting rich names.
func reg(name string) string { return "%" + QuoteName(name) }
func at(name string) string  { return "@" + QuoteName(name) }
func lbl(name string) string { return "$" + QuoteName(name) }

// sigSuffix renders "(A, B) -> R"; the async marker is the caller's.
func sigSuffix(sig *FnType) string {
	parts := make([]string, len(sig.Params))
	for i, param := range sig.Params {
		parts[i] = param.String()
	}
	out := "(" + strings.Join(parts, ", ") + ")"
	if sig.Result != nil {
		out += " -> " + sig.Result.String()
	}
	return out
}

type printer struct {
	out    strings.Builder
	indent int
}

func (p *printer) line(text string) {
	if text != "" {
		p.out.WriteString(strings.Repeat("  ", p.indent))
	}
	p.out.WriteString(text)
	p.out.WriteByte('\n')
}

func (p *printer) printf(format string, args ...any) {
	p.line(fmt.Sprintf(format, args...))
}

func (p *printer) typeDecl(decl *TypeDecl) {
	p.printf("type %s = %s", reg(decl.Name), shapeText(decl))
}

// shapeText renders what a type declaration says after the `=`. It is
// the half two modules have to agree on when one imports the other's
// type, so a mismatch is reported in the same words the format is
// written in.
func shapeText(decl *TypeDecl) string {
	if decl.Opaque {
		return "opaque"
	}
	if decl.Record != nil || decl.Union != nil {
		shape := "record"
		if decl.Union != nil {
			shape = "union"
		}
		if decl.Align > 0 {
			shape += " align " + strconv.Itoa(decl.Align)
		}
		members := decl.Members()
		text := make([]string, len(members))
		for i, member := range members {
			text[i] = member.Name + ": " + member.Type.String()
		}
		return shape + " { " + strings.Join(text, ", ") + " }"
	}
	variants := make([]string, len(decl.Enum.Variants))
	for i, variant := range decl.Enum.Variants {
		text := variant.Name
		if len(variant.Payload) > 0 {
			payload := make([]string, len(variant.Payload))
			for j, typ := range variant.Payload {
				payload[j] = typ.String()
			}
			text += "(" + strings.Join(payload, ", ") + ")"
		}
		variants[i] = text
	}
	return "enum { " + strings.Join(variants, ", ") + " }"
}

func (p *printer) function(fn *Func) {
	header := ""
	if fn.Export {
		header += "export "
	}
	params := make([]string, len(fn.Params))
	for i, param := range fn.Params {
		params[i] = reg(param.Name) + ": " + param.Type.String()
	}
	header += "fn " + at(fn.Name) + "(" + strings.Join(params, ", ") + ")"
	if fn.Result != nil {
		header += " -> " + fn.Result.String()
	}
	p.line(header + " {")
	p.indent++
	p.body(fn.Body)
	p.indent--
	p.line("}")
}

func (p *printer) body(body []Instr) {
	for _, instr := range body {
		p.instr(instr)
	}
}

func regs(names []string) string {
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = reg(name)
	}
	return strings.Join(parts, ", ")
}

func (p *printer) instr(instr Instr) {
	switch i := instr.(type) {
	case *Const:
		if i.Null {
			p.printf("%s = const.null %s", reg(i.Dst), i.Type)
			return
		}
		p.printf("%s = const.%s %s", reg(i.Dst), i.Type, i.Value)
	case *Copy:
		p.printf("%s = copy %s", reg(i.Dst), reg(i.X))
	case *Bin:
		p.printf("%s = %s %s, %s", reg(i.Dst), i.Op, reg(i.A), reg(i.B))
	case *Un:
		p.printf("%s = %s %s", reg(i.Dst), i.Op, reg(i.X))
	case *Conv:
		p.printf("%s = conv %s -> %s", reg(i.Dst), reg(i.X), i.To)
	case *Trunc:
		p.printf("%s = trunc %s -> %s", reg(i.Dst), reg(i.X), i.To)
	case *Bitcast:
		p.printf("%s = bitcast %s -> %s", reg(i.Dst), reg(i.X), i.To)
	case *Slot:
		p.printf("%s = slot %s", reg(i.Dst), i.Type)
	case *GlobalPtr:
		p.printf("%s = global.ptr %s", reg(i.Dst), at(i.Name))
	case *New:
		p.printf("%s = new %s", reg(i.Dst), i.Type)
	case *Free:
		p.printf("free %s", reg(i.X))
	case *Load:
		p.printf("%s = load %s", reg(i.Dst), reg(i.Ptr))
	case *Store:
		p.printf("store %s, %s", reg(i.Ptr), reg(i.Val))
	case *FieldPtr:
		p.printf("%s = field.ptr %s, %d", reg(i.Dst), reg(i.Ptr), i.Index)
	case *ElemPtr:
		p.printf("%s = elem.ptr %s", reg(i.Dst), reg(i.Ptr))
	case *PtrOffset:
		p.printf("%s = ptr.offset %s, %s", reg(i.Dst), reg(i.Ptr), reg(i.N))
	case *PtrDiff:
		p.printf("%s = ptr.diff %s, %s", reg(i.Dst), reg(i.A), reg(i.B))
	case *PtrErase:
		p.printf("%s = ptr.erase %s", reg(i.Dst), reg(i.X))
	case *PtrCast:
		p.printf("%s = ptr.cast %s -> %s", reg(i.Dst), reg(i.X), i.To)
	case *PtrAddr:
		p.printf("%s = ptr.addr %s", reg(i.Dst), reg(i.X))
	case *PtrFrom:
		p.printf("%s = ptr.from %s -> %s", reg(i.Dst), reg(i.X), i.To)
	case *AtomicLoad:
		p.printf("%s = atomic.load %s", reg(i.Dst), reg(i.Ptr))
	case *AtomicStore:
		p.printf("atomic.store %s, %s", reg(i.Ptr), reg(i.Val))
	case *AtomicRMW:
		p.printf("%s = atomic.%s %s, %s", reg(i.Dst), i.Op, reg(i.Ptr), reg(i.Val))
	case *AtomicCas:
		p.printf("%s = atomic.cas %s, %s, %s", reg(i.Dst), reg(i.Ptr), reg(i.Expected), reg(i.New))
	case *Fence:
		p.printf("atomic.fence")
	case *MemLoad:
		p.printf("%s = mem.load.%s %s, %s", reg(i.Dst), i.Type, at(i.Memory), reg(i.Addr))
	case *MemStore:
		p.printf("mem.store.%s %s, %s, %s", i.Type, at(i.Memory), reg(i.Addr), reg(i.Val))
	case *MemSize:
		p.printf("%s = mem.size %s", reg(i.Dst), at(i.Memory))
	case *MemGrow:
		p.printf("%s = mem.grow %s, %s", reg(i.Dst), at(i.Memory), reg(i.Pages))
	case *MemCopy:
		p.printf("mem.copy %s, %s, %s, %s", at(i.Memory), reg(i.To), reg(i.From), reg(i.Len))
	case *MemFill:
		p.printf("mem.fill %s, %s, %s, %s", at(i.Memory), reg(i.To), reg(i.Byte), reg(i.Len))
	case *MakeRecord:
		p.printf("%s = record %s(%s)", reg(i.Dst), reg(i.TypeName), regs(i.Args))
	case *FieldGet:
		p.printf("%s = field.get %s, %d", reg(i.Dst), reg(i.X), i.Index)
	case *FieldSet:
		p.printf("field.set %s, %d, %s", reg(i.X), i.Index, reg(i.Val))
	case *MakeVariant:
		text := fmt.Sprintf("%s = variant %s, %d", reg(i.Dst), reg(i.TypeName), i.Variant)
		if len(i.Args) > 0 {
			text += "(" + regs(i.Args) + ")"
		}
		p.line(text)
	case *VariantTag:
		p.printf("%s = variant.tag %s", reg(i.Dst), reg(i.X))
	case *VariantField:
		p.printf("%s = variant.field %s, %d, %d", reg(i.Dst), reg(i.X), i.Variant, i.Index)
	case *MakeTuple:
		p.printf("%s = tuple(%s)", reg(i.Dst), regs(i.Args))
	case *TupleGet:
		p.printf("%s = tuple.get %s, %d", reg(i.Dst), reg(i.X), i.Index)
	case *MakeArray:
		p.printf("%s = array<%s>(%s)", reg(i.Dst), i.Elem, regs(i.Args))
	case *Slice:
		p.printf("%s = slice.of %s", reg(i.Dst), reg(i.X))
	case *Loc:
		p.printf("loc %s %d", strconv.Quote(i.File), i.Line)
	case *SliceFrom:
		p.printf("%s = slice.from %s, %s", reg(i.Dst), reg(i.Ptr), reg(i.Len))
	case *SliceSub:
		p.printf("%s = slice.sub %s, %s, %s", reg(i.Dst), reg(i.X), reg(i.Lo), reg(i.Hi))
	case *IndexGet:
		p.printf("%s = index.get %s, %s", reg(i.Dst), reg(i.X), reg(i.Index))
	case *IndexSet:
		p.printf("index.set %s, %s, %s", reg(i.X), reg(i.Index), reg(i.Val))
	case *IndexPtr:
		p.printf("%s = index.ptr %s, %s", reg(i.Dst), reg(i.Ptr), reg(i.Index))
	case *Len:
		p.printf("%s = len %s", reg(i.Dst), reg(i.X))
	case *StrConcat:
		p.printf("%s = str.concat %s, %s", reg(i.Dst), reg(i.A), reg(i.B))
	case *Syscall:
		p.printf("%s = syscall %s(%s)", reg(i.Dst), reg(i.Number), regs(i.Args))
	case *Call:
		if i.Dst == "" {
			p.printf("call %s(%s)", at(i.Callee), regs(i.Args))
			return
		}
		p.printf("%s = call %s(%s)", reg(i.Dst), at(i.Callee), regs(i.Args))
	case *FnRef:
		p.printf("%s = fn.ref %s", reg(i.Dst), at(i.Callee))
	case *CodeRef:
		p.printf("%s = code.ref %s", reg(i.Dst), at(i.Callee))
	case *MakeClosure:
		p.printf("%s = closure %s, %s", reg(i.Dst), at(i.Callee), reg(i.Env))
	case *CallIndirect:
		if i.Dst == "" {
			p.printf("call.indirect %s(%s)", reg(i.Fn), regs(i.Args))
			return
		}
		p.printf("%s = call.indirect %s(%s)", reg(i.Dst), reg(i.Fn), regs(i.Args))
	case *Return:
		if i.Val == "" {
			p.line("return")
			return
		}
		p.printf("return %s", reg(i.Val))
	case *Abort:
		p.printf("abort %s", strconv.Quote(i.Message))
	case *ThreadStart:
		p.printf("%s = thread.start %s, %s, %s", reg(i.Dst), reg(i.Stack), at(i.Body), reg(i.Data))
	case *ThreadExit:
		p.line("thread.exit")
	case *If:
		p.printf("if %s {", reg(i.Cond))
		p.nested(i.Then)
		if len(i.Else) > 0 {
			p.line("} else {")
			p.nested(i.Else)
		}
		p.line("}")
	case *Block:
		p.printf("block %s {", lbl(i.Label))
		p.nested(i.Body)
		p.line("}")
	case *Loop:
		p.printf("loop %s {", lbl(i.Label))
		p.nested(i.Body)
		p.line("}")
	case *Br:
		p.printf("br %s", lbl(i.Label))
	case *BrIf:
		p.printf("br_if %s, %s", reg(i.Cond), lbl(i.Label))
	case *Switch:
		p.printf("switch %s {", reg(i.X))
		p.indent++
		for _, c := range i.Cases {
			p.printf("case %d {", c.Value)
			p.nested(c.Body)
			p.line("}")
		}
		if len(i.Default) > 0 {
			p.line("default {")
			p.nested(i.Default)
			p.line("}")
		}
		p.indent--
		p.line("}")
	case *OptWrap:
		p.printf("%s = opt.wrap %s", reg(i.Dst), reg(i.X))
	case *OptUnwrap:
		p.printf("%s = opt.unwrap %s", reg(i.Dst), reg(i.X))
	case *AnyWrap:
		p.printf("%s = any.wrap %s", reg(i.Dst), reg(i.X))
	case *AnyUnwrap:
		p.printf("%s = any.unwrap %s -> %s", reg(i.Dst), reg(i.X), i.To)
	case *AnyIs:
		p.printf("%s = any.is %s -> %s", reg(i.Dst), reg(i.X), i.To)
	case *AnyCall:
		p.printf("%s = any.call %s(%s)", reg(i.Dst), reg(i.X), regs(i.Args))
	case *AnyGet:
		p.printf("%s = any.get %s, %s", reg(i.Dst), reg(i.X), strconv.Quote(i.Name))
	case *AnySet:
		p.printf("any.set %s, %s, %s", reg(i.X), strconv.Quote(i.Name), reg(i.Val))
	}
}

func (p *printer) nested(body []Instr) {
	p.indent++
	p.body(body)
	p.indent--
}
