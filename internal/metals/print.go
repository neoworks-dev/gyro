package metals

import (
	"fmt"
	"strconv"
	"strings"
)

// Print renders a module in the canonical text form docs/metals.md
// specifies. Parse reads it back; the round trip is exact.
func Print(m *Module) string {
	p := &printer{}
	p.printf("module %s", strconv.Quote(m.Name))
	p.line("")

	for _, imp := range m.Imports {
		p.printf("import %s {", strconv.Quote(imp.Module))
		p.indent++
		for _, fn := range imp.Funcs {
			prefix := "fn @"
			if fn.Sig.Async {
				prefix = "async fn @"
			}
			p.line(prefix + fn.Name + sigSuffix(fn.Sig))
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

	for _, global := range m.Globals {
		p.printf("global @%s: %s = %s", global.Name, global.Type, global.Value)
	}
	if len(m.Globals) > 0 {
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
	if decl.Record != nil {
		fields := make([]string, len(decl.Record.Fields))
		for i, field := range decl.Record.Fields {
			fields[i] = field.Name + ": " + field.Type.String()
		}
		p.printf("type %%%s = record { %s }", decl.Name, strings.Join(fields, ", "))
		return
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
	p.printf("type %%%s = enum { %s }", decl.Name, strings.Join(variants, ", "))
}

func (p *printer) function(fn *Func) {
	header := ""
	if fn.Export {
		header += "export "
	}
	if fn.Async {
		header += "async "
	}
	params := make([]string, len(fn.Params))
	for i, param := range fn.Params {
		params[i] = "%" + param.Name + ": " + param.Type.String()
	}
	header += "fn @" + fn.Name + "(" + strings.Join(params, ", ") + ")"
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
		parts[i] = "%" + name
	}
	return strings.Join(parts, ", ")
}

func (p *printer) instr(instr Instr) {
	switch i := instr.(type) {
	case *Const:
		if i.Null {
			p.printf("%%%s = const.null %s", i.Dst, i.Type)
			return
		}
		p.printf("%%%s = const.%s %s", i.Dst, i.Type, i.Value)
	case *Copy:
		p.printf("%%%s = copy %%%s", i.Dst, i.X)
	case *Bin:
		p.printf("%%%s = %s %%%s, %%%s", i.Dst, i.Op, i.A, i.B)
	case *Un:
		p.printf("%%%s = %s %%%s", i.Dst, i.Op, i.X)
	case *Conv:
		p.printf("%%%s = conv %%%s -> %s", i.Dst, i.X, i.To)
	case *Slot:
		p.printf("%%%s = slot %s", i.Dst, i.Type)
	case *New:
		p.printf("%%%s = new %s", i.Dst, i.Type)
	case *Free:
		p.printf("free %%%s", i.X)
	case *Load:
		p.printf("%%%s = load %%%s", i.Dst, i.Ptr)
	case *Store:
		p.printf("store %%%s, %%%s", i.Ptr, i.Val)
	case *FieldPtr:
		p.printf("%%%s = field.ptr %%%s, %d", i.Dst, i.Ptr, i.Index)
	case *MakeRecord:
		p.printf("%%%s = record %%%s(%s)", i.Dst, i.TypeName, regs(i.Args))
	case *FieldGet:
		p.printf("%%%s = field.get %%%s, %d", i.Dst, i.X, i.Index)
	case *FieldSet:
		p.printf("field.set %%%s, %d, %%%s", i.X, i.Index, i.Val)
	case *MakeVariant:
		text := fmt.Sprintf("%%%s = variant %%%s, %d", i.Dst, i.TypeName, i.Variant)
		if len(i.Args) > 0 {
			text += "(" + regs(i.Args) + ")"
		}
		p.line(text)
	case *VariantTag:
		p.printf("%%%s = variant.tag %%%s", i.Dst, i.X)
	case *VariantField:
		p.printf("%%%s = variant.field %%%s, %d, %d", i.Dst, i.X, i.Variant, i.Index)
	case *MakeTuple:
		p.printf("%%%s = tuple(%s)", i.Dst, regs(i.Args))
	case *TupleGet:
		p.printf("%%%s = tuple.get %%%s, %d", i.Dst, i.X, i.Index)
	case *MakeArray:
		p.printf("%%%s = array<%s>(%s)", i.Dst, i.Elem, regs(i.Args))
	case *Slice:
		p.printf("%%%s = slice.of %%%s", i.Dst, i.X)
	case *IndexGet:
		p.printf("%%%s = index.get %%%s, %%%s", i.Dst, i.X, i.Index)
	case *IndexSet:
		p.printf("index.set %%%s, %%%s, %%%s", i.X, i.Index, i.Val)
	case *Len:
		p.printf("%%%s = len %%%s", i.Dst, i.X)
	case *StrConcat:
		p.printf("%%%s = str.concat %%%s, %%%s", i.Dst, i.A, i.B)
	case *Call:
		if i.Dst == "" {
			p.printf("call @%s(%s)", i.Callee, regs(i.Args))
			return
		}
		p.printf("%%%s = call @%s(%s)", i.Dst, i.Callee, regs(i.Args))
	case *FnRef:
		p.printf("%%%s = fn.ref @%s", i.Dst, i.Callee)
	case *MakeClosure:
		p.printf("%%%s = closure @%s, %%%s", i.Dst, i.Callee, i.Env)
	case *CallIndirect:
		if i.Dst == "" {
			p.printf("call.indirect %%%s(%s)", i.Fn, regs(i.Args))
			return
		}
		p.printf("%%%s = call.indirect %%%s(%s)", i.Dst, i.Fn, regs(i.Args))
	case *Return:
		if i.Val == "" {
			p.line("return")
			return
		}
		p.printf("return %%%s", i.Val)
	case *Abort:
		p.printf("abort %s", strconv.Quote(i.Message))
	case *If:
		p.printf("if %%%s {", i.Cond)
		p.nested(i.Then)
		if len(i.Else) > 0 {
			p.line("} else {")
			p.nested(i.Else)
		}
		p.line("}")
	case *Block:
		p.printf("block $%s {", i.Label)
		p.nested(i.Body)
		p.line("}")
	case *Loop:
		p.printf("loop $%s {", i.Label)
		p.nested(i.Body)
		p.line("}")
	case *Br:
		p.printf("br $%s", i.Label)
	case *BrIf:
		p.printf("br_if %%%s, $%s", i.Cond, i.Label)
	case *Switch:
		p.printf("switch %%%s {", i.X)
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
	case *ChanNew:
		if i.Cap == "" {
			p.printf("%%%s = chan.new<%s>", i.Dst, i.Elem)
			return
		}
		p.printf("%%%s = chan.new<%s> %%%s", i.Dst, i.Elem, i.Cap)
	case *Send:
		p.printf("send %%%s, %%%s", i.Ch, i.Val)
	case *Recv:
		p.printf("%%%s = recv %%%s", i.Dst, i.Ch)
	case *ChanClose:
		p.printf("chan.close %%%s", i.Ch)
	case *Await:
		p.printf("%%%s = await %%%s", i.Dst, i.X)
	case *Select:
		p.line("select {")
		p.indent++
		for _, arm := range i.Arms {
			if arm.Send {
				p.printf("send %%%s, %%%s {", arm.Ch, arm.Val)
			} else {
				p.printf("recv %%%s = %%%s {", arm.Dst, arm.Ch)
			}
			p.nested(arm.Body)
			p.line("}")
		}
		if len(i.Default) > 0 {
			p.line("default {")
			p.nested(i.Default)
			p.line("}")
		}
		p.indent--
		p.line("}")
	case *DynWrap:
		p.printf("%%%s = dyn.wrap %%%s", i.Dst, i.X)
	case *DynUnwrap:
		p.printf("%%%s = dyn.unwrap %%%s -> %s", i.Dst, i.X, i.To)
	case *DynCall:
		p.printf("%%%s = dyn.call %%%s(%s)", i.Dst, i.X, regs(i.Args))
	case *DynGet:
		p.printf("%%%s = dyn.get %%%s, %s", i.Dst, i.X, strconv.Quote(i.Name))
	case *DynSet:
		p.printf("dyn.set %%%s, %s, %%%s", i.X, strconv.Quote(i.Name), i.Val)
	}
}

func (p *printer) nested(body []Instr) {
	p.indent++
	p.body(body)
	p.indent--
}
