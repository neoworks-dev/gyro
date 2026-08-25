// Package wasm compiles a linked WebAssembly module into METALS.
//
// It exists to answer a question about METALS before a C front end is
// written to depend on the answer. wasm32 and METALS are the same shape
// in the two places that matter: structured control flow — block, loop,
// br to an enclosing label, and nothing else — and a flat linear memory
// addressed by integers. So `clang --target=wasm32` produces, from real
// C, exactly the input a C front end would eventually produce itself,
// with LLVM having already done the one genuinely hard piece: turning C's
// goto and irreducible loops back into structured control flow.
//
// That makes this a bridge, not a destination. What it buys is a
// producer that exercises linear memory, the indirect-call table and the
// runtime import surface against programs nobody wrote by hand, and an
// oracle to diff a future front end's output against: the same C, two
// producers, one evaluator.
//
// The scheme is `wasm:`, which says what wrote the module. A C front end
// will have its own, because it will not be this.
package wasm

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// ModuleName is the METALS module name for a compiled wasm file.
func ModuleName(path string) string {
	base := filepath.Base(path)
	return "wasm:" + strings.TrimSuffix(base, filepath.Ext(base))
}

// Compile translates a linked wasm binary into a METALS module. The
// result still has to be verified: this package is a producer like any
// other, and the format trusts none of them.
func Compile(path string, binary []byte) (*metals.Module, error) {
	parsed, err := decode(binary)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	module, err := translate(ModuleName(path), parsed)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return module, nil
}

// ---------------------------------------------------------------------------
// The binary's own vocabulary
// ---------------------------------------------------------------------------

// valType is a wasm value type. The numbering is the binary format's.
type valType byte

const (
	valI32 valType = 0x7F
	valI64 valType = 0x7E
	valF32 valType = 0x7D
	valF64 valType = 0x7C
)

func (v valType) String() string {
	switch v {
	case valI32:
		return "i32"
	case valI64:
		return "i64"
	case valF32:
		return "f32"
	case valF64:
		return "f64"
	}
	return fmt.Sprintf("valtype(%#x)", byte(v))
}

// metalsType is the METALS type a wasm value lives in.
//
// A wasm i32 is a bag of 32 bits with no signedness of its own: the
// operation decides, which is why there is an i32.div_s and an
// i32.div_u. METALS types carry signedness, so one of the two has to be
// the register's type and the other has to be reached by bitcast. The
// signed one is chosen because it is the common case in compiled C and
// because it makes an i32 register print as the number C would print.
func (v valType) metalsType() metals.Type {
	switch v {
	case valI32:
		return metals.TInt32
	case valI64:
		return metals.TInt64
	case valF32:
		return metals.TF32
	case valF64:
		return metals.TF64
	}
	return nil
}

// funcType is one entry of the type section.
type funcType struct {
	Params  []valType
	Results []valType
}

func (f *funcType) String() string {
	params := make([]string, len(f.Params))
	for i, p := range f.Params {
		params[i] = p.String()
	}
	results := make([]string, len(f.Results))
	for i, r := range f.Results {
		results[i] = r.String()
	}
	return "(" + strings.Join(params, ", ") + ") -> (" + strings.Join(results, ", ") + ")"
}

// wimport is an imported function, memory or global. Only functions are
// supported; the rest are recorded so that index spaces stay right.
type wimport struct {
	Module string
	Name   string
	Kind   byte
	// TypeIndex is set for a function import.
	TypeIndex uint32
}

const (
	kindFunc   byte = 0x00
	kindTable  byte = 0x01
	kindMemory byte = 0x02
	kindGlobal byte = 0x03
)

type memType struct {
	Min uint32
	Max uint32 // 0 when the limit is absent
}

type globalDef struct {
	Type    valType
	Mutable bool
	// Init is the constant expression's value. Only the numeric constant
	// forms appear in a linked module's globals.
	Init int64
}

type export struct {
	Name  string
	Kind  byte
	Index uint32
}

// element is one entry of the element section: the function indices a
// table holds, which is what makes call_indirect resolvable statically.
type element struct {
	Offset uint32
	Funcs  []uint32
}

// code is one function body: its extra locals and its raw instructions.
type code struct {
	Locals []valType
	Body   []byte
}

type dataSeg struct {
	Offset uint32
	Bytes  []byte
}

// wmodule is a decoded wasm module, one field per section.
type wmodule struct {
	Types    []funcType
	Imports  []wimport
	Funcs    []uint32 // type index per locally defined function
	Memories []memType
	Globals  []globalDef
	Exports  []export
	Elements []element
	Code     []code
	Data     []dataSeg
	// Names maps a function index to the name the name section gave it,
	// which is what keeps the generated METALS readable.
	Names map[uint32]string
}

// importedFuncs counts the function imports, which occupy the low end of
// the function index space.
func (m *wmodule) importedFuncs() int {
	n := 0
	for _, imp := range m.Imports {
		if imp.Kind == kindFunc {
			n++
		}
	}
	return n
}

// typeOfFunc resolves a function index to its signature, across the
// import and definition halves of the index space.
func (m *wmodule) typeOfFunc(index uint32) (*funcType, error) {
	imported := 0
	for _, imp := range m.Imports {
		if imp.Kind != kindFunc {
			continue
		}
		if uint32(imported) == index {
			if int(imp.TypeIndex) >= len(m.Types) {
				return nil, fmt.Errorf("import %s.%s names type %d, of %d",
					imp.Module, imp.Name, imp.TypeIndex, len(m.Types))
			}
			return &m.Types[imp.TypeIndex], nil
		}
		imported++
	}
	local := int(index) - imported
	if local < 0 || local >= len(m.Funcs) {
		return nil, fmt.Errorf("function index %d is out of range", index)
	}
	typeIndex := m.Funcs[local]
	if int(typeIndex) >= len(m.Types) {
		return nil, fmt.Errorf("function %d names type %d, of %d", index, typeIndex, len(m.Types))
	}
	return &m.Types[typeIndex], nil
}
