package nasm

import (
	"sort"
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Register allocation.
//
// Every METALS register still owns a frame slot; the allocator hands the
// hottest scalars a machine register to live in instead of that slot.
// Only the callee-saved registers are handed out, and that is what keeps
// the allocation one decision per METALS register rather than one per
// program point: a value in rbx or r12..r15 survives every call the
// frame makes, so no instruction has to spill around a call and no
// instruction has to reload afterwards.
//
// A METALS register is a candidate when its type is a single
// integer-class word — aggregates live inline in memory by the layout
// rules, and a float has no callee-saved home to move into — and when no
// emitter ever needs its address. Two candidates interfere when one is
// written while the other is live; the interference graph is coloured
// greedily, hottest first, and whatever does not fit keeps its slot.

var allocRegs = []string{"rbx", "r12", "r13", "r14", "r15"}

// minWeight is the mention count below which a machine register does not
// pay for itself: a home saves roughly one memory access per mention and
// costs the two the prologue and epilogue spend saving the callee-saved
// register.
const minWeight = 3

// A mention inside a loop counts for more, since it runs more often.
const (
	loopFactor = 10
	maxFactor  = 1000000
)

// allocation is the result: where each homed METALS register lives, and
// the machine registers the frame therefore has to save and restore.
type allocation struct {
	homes map[string]string
	saved []string
}

func (g *generator) allocate(fn *metals.Func, types map[string]metals.Type, folded map[string]int64) allocation {
	empty := allocation{homes: map[string]string{}}
	candidates := g.candidates(fn, types, folded)
	if len(candidates) == 0 {
		return empty
	}

	scan := &liveScan{
		g:      g,
		live:   candidates,
		graph:  map[string]regSet{},
		labels: map[string]regSet{},
	}
	entry := scan.body(fn.Body, regSet{})

	// The prologue writes every parameter, so no two of them can share a
	// machine register, and each of them interferes with whatever the
	// body still expects to find in the others.
	var params []string
	for _, param := range fn.Params {
		if candidates[param.Name] {
			params = append(params, param.Name)
		}
	}
	for _, param := range params {
		for other := range entry {
			scan.addEdge(param, other)
		}
		for _, other := range params {
			scan.addEdge(param, other)
		}
	}

	weights := map[string]int{}
	g.countWeights(fn.Body, 1, weights)

	order := make([]string, 0, len(candidates))
	for name := range candidates {
		if weights[name] >= minWeight {
			order = append(order, name)
		}
	}
	if len(order) == 0 {
		return empty
	}
	sort.Slice(order, func(a, b int) bool {
		if weights[order[a]] != weights[order[b]] {
			return weights[order[a]] > weights[order[b]]
		}
		return order[a] < order[b]
	})

	homes := map[string]string{}
	inUse := map[string]bool{}
	for _, name := range order {
		taken := map[string]bool{}
		for neighbour := range scan.graph[name] {
			if home, ok := homes[neighbour]; ok {
				taken[home] = true
			}
		}
		for _, machine := range allocRegs {
			if !taken[machine] {
				homes[name] = machine
				inUse[machine] = true
				break
			}
		}
	}

	var saved []string
	for _, machine := range allocRegs {
		if inUse[machine] {
			saved = append(saved, machine)
		}
	}
	return allocation{homes: homes, saved: saved}
}

// candidates collects the registers a machine register could hold.
func (g *generator) candidates(fn *metals.Func, types map[string]metals.Type, folded map[string]int64) regSet {
	blocked := regSet{}
	walkInstrs(fn.Body, func(instr metals.Instr) {
		for _, name := range g.rolesOf(instr).mem {
			blocked.add(name)
		}
	})

	out := regSet{}
	for name, typ := range types {
		if _, isConst := folded[name]; isConst || blocked[name] {
			continue
		}
		// One integer word as a register carries it: a narrow scalar is
		// widened there, so an i32 is as good a candidate as an i64.
		if g.slotOf(typ) != wordSize || g.classOf(typ) != classInt {
			continue
		}
		out.add(name)
	}
	return out
}

// ---------------------------------------------------------------------------
// Constant folding
// ---------------------------------------------------------------------------

// immediates finds the registers one small integer constant defines and
// nothing else writes. Their value rides in the instructions that read
// it, so the constant costs no slot, no machine register and no store —
// and the register it would have taken goes to a real value instead.
func (g *generator) immediates(fn *metals.Func) map[string]int64 {
	writes := map[string]int{}
	blocked := regSet{}
	folded := map[string]int64{}

	walkInstrs(fn.Body, func(instr metals.Instr) {
		roles := g.rolesOf(instr)
		for _, name := range roles.defs {
			if name != "" {
				writes[name]++
			}
		}
		for _, name := range roles.mem {
			blocked.add(name)
		}
		if c, ok := instr.(*metals.Const); ok {
			if value, ok := immValue(c); ok {
				folded[c.Dst] = value
			}
		}
	})
	for _, param := range fn.Params {
		delete(folded, param.Name)
	}
	for name := range folded {
		if writes[name] != 1 || blocked[name] {
			delete(folded, name)
		}
	}

	// A copy of a constant, and a conversion of one that provably keeps
	// its value, are constants too. The lowering writes both around
	// every literal, so without this almost nothing folds.
	settled := false
	for !settled {
		settled = true
		walkInstrs(fn.Body, func(instr metals.Instr) {
			dst, source, ok := forwardedConst(g, instr, folded)
			if !ok {
				return
			}
			if _, done := folded[dst]; done {
				return
			}
			if writes[dst] != 1 || blocked[dst] {
				return
			}
			folded[dst] = source
			settled = false
		})
	}
	for _, param := range fn.Params {
		delete(folded, param.Name)
	}
	return folded
}

// forwardedConst reports the constant an instruction passes through
// unchanged.
func forwardedConst(g *generator, instr metals.Instr, folded map[string]int64) (dst string, value int64, ok bool) {
	switch i := instr.(type) {
	case *metals.Copy:
		value, ok = folded[i.X]
		return i.Dst, value, ok
	case *metals.Conv:
		value, ok = folded[i.X]
		if !ok {
			return "", 0, false
		}
		target, isPrim := i.To.(*metals.Prim)
		if !isPrim {
			return "", 0, false
		}
		kind, isInt := intKinds[target.Kind]
		// A conversion the value does not survive keeps its check.
		if !isInt || !fitsWidth(value, kind) {
			return "", 0, false
		}
		return i.Dst, value, true
	}
	return "", 0, false
}

func fitsWidth(value int64, kind intKindInfo) bool {
	if kind.signed {
		if kind.width >= 64 {
			return true
		}
		bound := int64(1) << (kind.width - 1)
		return value >= -bound && value <= bound-1
	}
	if value < 0 {
		return false
	}
	return kind.width >= 64 || uint64(value) <= uintMaxOf(kind.width)
}

// immValue reads the constants an x86 instruction can carry: an integer
// that fits the sign-extended 32 bits every arithmetic immediate is.
// Strings and floats have no immediate form, and null makes an
// aggregate.
func immValue(c *metals.Const) (int64, bool) {
	if c.Null {
		return 0, false
	}
	prim, ok := c.Type.(*metals.Prim)
	if !ok {
		return 0, false
	}
	var value int64
	switch prim.Kind {
	case metals.Str, metals.F32, metals.F64:
		return 0, false
	case metals.Bool:
		if c.Value == "true" {
			value = 1
		}
	case metals.Rune:
		decoded, _, _, err := strconv.UnquoteChar(c.Value[1:len(c.Value)-1], '\'')
		if err != nil {
			return 0, false
		}
		value = int64(decoded)
	default:
		parsed, err := strconv.ParseInt(c.Value, 0, 64)
		if err != nil {
			return 0, false
		}
		value = parsed
	}
	if value < -2147483648 || value > 2147483647 {
		return 0, false
	}
	return value, true
}

// countWeights scores every mention, a mention inside a loop counting
// for a whole iteration count more than one outside it.
func (g *generator) countWeights(body []metals.Instr, factor int, weights map[string]int) {
	for _, instr := range body {
		roles := g.rolesOf(instr)
		for _, name := range roles.defs {
			weights[name] += factor
		}
		for _, name := range roles.uses {
			weights[name] += factor
		}
		switch i := instr.(type) {
		case *metals.If:
			g.countWeights(i.Then, factor, weights)
			g.countWeights(i.Else, factor, weights)
		case *metals.Block:
			g.countWeights(i.Body, factor, weights)
		case *metals.Loop:
			inner := factor * loopFactor
			if inner > maxFactor {
				inner = maxFactor
			}
			g.countWeights(i.Body, inner, weights)
		case *metals.Switch:
			for _, c := range i.Cases {
				g.countWeights(c.Body, factor, weights)
			}
			g.countWeights(i.Default, factor, weights)
		case *metals.Select:
			for _, arm := range i.Arms {
				g.countWeights(arm.Body, factor, weights)
			}
			g.countWeights(i.Default, factor, weights)
		}
	}
}

// ---------------------------------------------------------------------------
// Register roles
// ---------------------------------------------------------------------------

// roles is what one instruction does to the registers it names: which it
// writes, which it reads, and which it needs to have an address, which
// only a frame slot has. Structured instructions report their own
// operands only; the walkers descend into the bodies themselves.
type roles struct {
	defs []string
	uses []string
	mem  []string
}

func (g *generator) rolesOf(instr metals.Instr) roles {
	switch i := instr.(type) {
	case *metals.Const:
		return roles{defs: []string{i.Dst}}
	case *metals.Copy:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.Bin:
		return roles{defs: []string{i.Dst}, uses: []string{i.A, i.B}}
	case *metals.Un:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.Conv:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.Trunc:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.Bitcast:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.MemLoad:
		return roles{defs: []string{i.Dst}, uses: []string{i.Addr}}
	case *metals.MemStore:
		return roles{uses: []string{i.Addr, i.Val}}
	case *metals.MemSize:
		return roles{defs: []string{i.Dst}}
	case *metals.MemGrow:
		return roles{defs: []string{i.Dst}, uses: []string{i.Pages}}
	case *metals.MemCopy:
		return roles{uses: []string{i.To, i.From, i.Len}}
	case *metals.MemFill:
		return roles{uses: []string{i.To, i.Byte, i.Len}}
	case *metals.Slot:
		return roles{defs: []string{i.Dst}}
	case *metals.GlobalPtr:
		return roles{defs: []string{i.Dst}}
	case *metals.New:
		return roles{defs: []string{i.Dst}}
	case *metals.Free:
		return roles{uses: []string{i.X}}
	case *metals.Load:
		return roles{defs: []string{i.Dst}, uses: []string{i.Ptr}}
	case *metals.Store:
		return roles{uses: []string{i.Ptr, i.Val}}
	case *metals.FieldPtr:
		return roles{defs: []string{i.Dst}, uses: []string{i.Ptr}}
	case *metals.ElemPtr:
		return roles{defs: []string{i.Dst}, uses: []string{i.Ptr}}
	case *metals.PtrOffset:
		return roles{defs: []string{i.Dst}, uses: []string{i.Ptr, i.N}}
	case *metals.PtrDiff:
		return roles{defs: []string{i.Dst}, uses: []string{i.A, i.B}}
	case *metals.PtrErase:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.PtrCast:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.PtrAddr:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.PtrFrom:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.MakeRecord:
		return roles{defs: []string{i.Dst}, uses: i.Args}
	case *metals.FieldGet:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.FieldSet:
		return roles{uses: []string{i.X, i.Val}}
	case *metals.MakeVariant:
		return roles{defs: []string{i.Dst}, uses: i.Args}
	case *metals.VariantTag:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.VariantField:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.MakeTuple:
		return roles{defs: []string{i.Dst}, uses: i.Args}
	case *metals.TupleGet:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.MakeArray:
		return roles{defs: []string{i.Dst}, uses: i.Args}
	case *metals.Slice:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}, mem: []string{i.X}}
	case *metals.Loc:
		return roles{}
	case *metals.SliceFrom:
		return roles{defs: []string{i.Dst}, uses: []string{i.Ptr, i.Len}}
	case *metals.Syscall:
		return roles{defs: []string{i.Dst}, uses: append([]string{i.Number}, i.Args...)}
	case *metals.SliceSub:
		// A slice is two words, so neither operand nor result is a
		// candidate for a machine register in the first place; only the
		// bounds are ordinary values.
		return roles{defs: []string{i.Dst}, uses: []string{i.X, i.Lo, i.Hi}}
	case *metals.IndexGet:
		return roles{defs: []string{i.Dst}, uses: []string{i.X, i.Index}}
	case *metals.IndexSet:
		return roles{uses: []string{i.X, i.Index, i.Val}}
	case *metals.IndexPtr:
		// The base is read for its value, not its address: the array is
		// where the pointer says it is, so a home is fine for it.
		return roles{defs: []string{i.Dst}, uses: []string{i.Ptr, i.Index}}
	case *metals.Len:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.StrConcat:
		return roles{defs: []string{i.Dst}, uses: []string{i.A, i.B}}
	case *metals.Call:
		return g.callRoles(i)
	case *metals.FnRef:
		return roles{defs: []string{i.Dst}}
	case *metals.CodeRef:
		return roles{defs: []string{i.Dst}}
	case *metals.MakeClosure:
		return roles{defs: []string{i.Dst}, uses: []string{i.Env}}
	case *metals.CallIndirect:
		return roles{defs: []string{i.Dst}, uses: append([]string{i.Fn}, i.Args...)}
	case *metals.Return:
		return roles{uses: []string{i.Val}}
	case *metals.Abort:
		return roles{}
	case *metals.If:
		return roles{uses: []string{i.Cond}}
	case *metals.Block, *metals.Loop:
		return roles{}
	case *metals.Br:
		return roles{}
	case *metals.BrIf:
		return roles{uses: []string{i.Cond}}
	case *metals.Switch:
		return roles{uses: []string{i.X}}
	case *metals.ChanNew:
		return roles{defs: []string{i.Dst}, uses: []string{i.Cap}}
	case *metals.Send:
		// The runtime takes the element by pointer.
		return roles{uses: []string{i.Ch, i.Val}, mem: []string{i.Val}}
	case *metals.Recv:
		return roles{defs: []string{i.Dst}, uses: []string{i.Ch}, mem: []string{i.Dst}}
	case *metals.ChanClose:
		return roles{uses: []string{i.Ch}}
	case *metals.Await:
		// gy_future_get fills the destination through a pointer.
		return roles{defs: []string{i.Dst}, uses: []string{i.X}, mem: []string{i.Dst}}
	case *metals.Select:
		return g.selectRoles(i)
	case *metals.OptWrap:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	case *metals.OptUnwrap:
		return roles{defs: []string{i.Dst}, uses: []string{i.X}}
	}
	// Anything the backend does not implement keeps every register it
	// names in memory, so a wrong home can never outlive the failure the
	// emitter is about to raise.
	return g.unknownRoles(instr)
}

func (g *generator) unknownRoles(instr metals.Instr) roles {
	var named []string
	switch i := instr.(type) {
	case *metals.AnyWrap:
		named = []string{i.Dst, i.X}
	case *metals.AnyUnwrap:
		named = []string{i.Dst, i.X}
	case *metals.AnyIs:
		named = []string{i.Dst, i.X}
	case *metals.AnyCall:
		named = append([]string{i.Dst, i.X}, i.Args...)
	case *metals.AnyGet:
		named = []string{i.Dst, i.X}
	case *metals.AnySet:
		named = []string{i.X, i.Val}
	default:
		g.fail("the native backend does not implement %T yet", instr)
	}
	return roles{defs: named, uses: named, mem: named}
}

func (g *generator) callRoles(i *metals.Call) roles {
	r := roles{defs: []string{i.Dst}, uses: i.Args}
	if g.funcs[i.Callee] != nil {
		return r
	}
	sig := g.imports[i.Callee]
	if sig == nil || sig.Async {
		// An unknown callee fails in the emitter; an async import
		// marshals through a heap buffer and takes no slot addresses.
		return r
	}
	plan := g.nativePlanFor(i.Callee, sig)
	if plan.sret {
		r.mem = append(r.mem, i.Dst)
	}
	for _, arg := range plan.args {
		if arg.pointer {
			r.mem = append(r.mem, i.Args[arg.index])
		}
	}
	return r
}

func (g *generator) selectRoles(i *metals.Select) roles {
	var r roles
	for _, arm := range i.Arms {
		r.uses = append(r.uses, arm.Ch)
		if arm.Send {
			r.uses = append(r.uses, arm.Val)
			r.mem = append(r.mem, arm.Val)
		} else {
			r.defs = append(r.defs, arm.Dst)
			r.mem = append(r.mem, arm.Dst)
		}
	}
	return r
}

// ---------------------------------------------------------------------------
// Liveness
// ---------------------------------------------------------------------------

type regSet map[string]bool

func (s regSet) add(name string) {
	if name != "" {
		s[name] = true
	}
}

func (s regSet) remove(name string) { delete(s, name) }

func (s regSet) clone() regSet {
	out := make(regSet, len(s))
	for name := range s {
		out[name] = true
	}
	return out
}

// union returns a fresh set; neither operand is disturbed.
func (s regSet) union(other regSet) regSet {
	out := s.clone()
	for name := range other {
		out[name] = true
	}
	return out
}

func (s regSet) subsetOf(other regSet) bool {
	for name := range s {
		if !other[name] {
			return false
		}
	}
	return true
}

// liveScan walks a body backwards, threading the set of candidates live
// after each point and recording an interference edge whenever a live
// register is written.
type liveScan struct {
	g      *generator
	live   regSet            // the candidates; every other register is ignored
	graph  map[string]regSet // candidate -> the candidates it interferes with
	labels map[string]regSet // METALS label -> live set at its branch target
}

func (s *liveScan) addEdge(a, b string) {
	if a == b || !s.live[a] || !s.live[b] {
		return
	}
	if s.graph[a] == nil {
		s.graph[a] = regSet{}
	}
	if s.graph[b] == nil {
		s.graph[b] = regSet{}
	}
	s.graph[a][b] = true
	s.graph[b][a] = true
}

func (s *liveScan) body(instrs []metals.Instr, live regSet) regSet {
	for index := len(instrs) - 1; index >= 0; index-- {
		live = s.instr(instrs[index], live)
	}
	return live
}

func (s *liveScan) instr(instr metals.Instr, live regSet) regSet {
	switch i := instr.(type) {
	case *metals.If:
		out := s.body(i.Then, live.clone()).union(s.body(i.Else, live.clone()))
		return s.transfer(roles{uses: []string{i.Cond}}, out)

	case *metals.Block:
		// `br $label` leaves the block, so its target is what is live
		// after the whole construct.
		return s.scoped(i.Label, live.clone(), func() regSet {
			return s.body(i.Body, live.clone())
		})

	case *metals.Loop:
		// `br $label` re-enters the top, so the target's live set is the
		// body's own live-in — a fixpoint, reached from below.
		var out regSet
		s.scoped(i.Label, regSet{}, func() regSet {
			for {
				out = s.body(i.Body, live.clone())
				if out.subsetOf(s.labels[i.Label]) {
					return out
				}
				s.labels[i.Label] = s.labels[i.Label].union(out)
			}
		})
		return out

	case *metals.Br:
		return s.target(i.Label).clone()

	case *metals.BrIf:
		out := live.clone().union(s.target(i.Label))
		return s.transfer(roles{uses: []string{i.Cond}}, out)

	case *metals.Switch:
		out := regSet{}
		for _, c := range i.Cases {
			out = out.union(s.body(c.Body, live.clone()))
		}
		if len(i.Default) > 0 {
			out = out.union(s.body(i.Default, live.clone()))
		} else {
			out = out.union(live)
		}
		return s.transfer(roles{uses: []string{i.X}}, out)

	case *metals.Select:
		return s.selectInstr(i, live)

	case *metals.Return:
		return s.transfer(roles{uses: []string{i.Val}}, regSet{})

	case *metals.Abort:
		return regSet{}
	}
	return s.transfer(s.g.rolesOf(instr), live)
}

// scoped installs a label's branch target for the duration of one body,
// restoring whatever the name meant outside it.
func (s *liveScan) scoped(label string, target regSet, run func() regSet) regSet {
	saved, had := s.labels[label]
	s.labels[label] = target
	out := run()
	if had {
		s.labels[label] = saved
	} else {
		delete(s.labels, label)
	}
	return out
}

func (s *liveScan) target(label string) regSet {
	set, ok := s.labels[label]
	if !ok {
		s.g.fail("br to unknown label $%s", label)
	}
	return set
}

// selectInstr models the poll: an arm's body runs after every arm has
// been tried, and without a default the whole poll retries, so every
// channel and every sent value stays live across all of it.
func (s *liveScan) selectInstr(i *metals.Select, live regSet) regSet {
	out := regSet{}
	for _, arm := range i.Arms {
		out = out.union(s.body(arm.Body, live.clone()))
	}
	if len(i.Default) > 0 {
		out = out.union(s.body(i.Default, live.clone()))
	}
	for _, arm := range i.Arms {
		out.add(arm.Ch)
		if arm.Send {
			out.add(arm.Val)
		}
	}
	for _, arm := range i.Arms {
		if !arm.Send {
			s.define(arm.Dst, out)
		}
	}
	return out
}

// transfer applies one instruction to the set live after it and returns
// the set live before it.
func (s *liveScan) transfer(r roles, live regSet) regSet {
	out := live.clone()
	for _, name := range r.defs {
		s.define(name, out)
	}
	for _, name := range r.uses {
		if s.live[name] {
			out.add(name)
		}
	}
	return out
}

// define records that writing name clobbers everything live, then kills
// it. A register that is both read and written by one instruction is
// added back by transfer, which is what the emitters do: every operand
// is read before the destination is written.
func (s *liveScan) define(name string, live regSet) {
	if !s.live[name] {
		return
	}
	for other := range live {
		s.addEdge(name, other)
	}
	live.remove(name)
}
