package cc

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Storage that is written before main.
//
// C has two kinds of initializer and they lower to two different things.
// One is a value the compiler already knows — `static int limits[3] = {1,
// 2, 3}`, and every string literal — and that becomes the bytes the
// global starts with, which costs nothing at run time and nothing in the
// program's instructions. The other names an address: `static int *p =
// &table[2]` is a constant expression to C and still not a number until
// the program is laid out, so it becomes stores in the module's init
// function.
//
// The split is the linker's, not a preference. A byte image cannot hold
// a relocation, and METALS' Global.Bytes says so by refusing any type
// with a pointer inside it.

// initName is the function the module's file-scope initializers go into.
// The `$` keeps it out of C's identifier space, so it cannot collide
// with anything the translation unit declares.
const initName = "$init"

// stringData interns a string literal as module storage and returns the
// global's name. Two identical literals share one array, which C permits
// and every C compiler does.
func (l *lowerer) stringData(text string) string {
	if name, ok := l.strings[text]; ok {
		return name
	}
	name := fmt.Sprintf("$str.%d", len(l.strings))
	l.strings[text] = name
	l.out.Globals = append(l.out.Globals, &metals.Global{
		Name:  name,
		Type:  &metals.ArrayOf{Elem: metals.TInt8, Len: int64(len(text))},
		Bytes: []byte(text),
	})
	return name
}

// stringType is the type of the literal a global holds: an array of char
// as long as the text, terminator included.
func stringType(text string) *ctype {
	return arrayOf(typeChar, int64(len(text)))
}

// ---------------------------------------------------------------------------
// Static images
// ---------------------------------------------------------------------------

// staticImage renders an initializer as the bytes the object starts
// with, or reports that it cannot — which is C's own distinction between
// an arithmetic constant expression and an address constant. A false
// answer sends the whole initializer through the init function; mixing
// the two would let a program depend on which half ran when.
func (l *lowerer) staticImage(typ *ctype, init *initializer) ([]byte, bool) {
	image := make([]byte, l.layout.Size(typ.metalsType()))
	if !l.writeImage(image, typ, init) {
		return nil, false
	}
	return image, true
}

func (l *lowerer) writeImage(image []byte, typ *ctype, init *initializer) bool {
	if !init.braced() {
		return l.writeImageExpr(image, typ, init)
	}

	switch {
	case typ.isArray():
		if typ.length < 0 {
			return false
		}
		stride := l.layout.Size(typ.elem.metalsType())
		if int64(len(init.list)) > typ.length {
			fail(init.pos, "%d initializers for an array of %d", len(init.list), typ.length)
		}
		for i, element := range init.list {
			if !l.writeImage(image[i*stride:], typ.elem, element) {
				return false
			}
		}
		return true

	case typ.isStruct():
		if len(init.list) > len(typ.fields) {
			fail(init.pos, "%d initializers for a %s, which has %d fields",
				len(init.list), typ, len(typ.fields))
		}
		record := l.recordOf(typ, init.pos)
		for i, member := range init.list {
			if !l.writeImage(image[l.layout.FieldOffset(record, i):], typ.fields[i].typ, member) {
				return false
			}
		}
		return true
	}

	// `int x = { 5 }` is C, and means what `int x = 5` means.
	if len(init.list) == 0 {
		return true
	}
	if len(init.list) > 1 {
		fail(init.pos, "%d initializers for a %s", len(init.list), typ)
	}
	return l.writeImage(image, typ, init.list[0])
}

func (l *lowerer) writeImageExpr(image []byte, typ *ctype, init *initializer) bool {
	// `char message[] = "hello"` copies the bytes into the array rather
	// than pointing at them, which is the one place a string literal is
	// not an address.
	if text, ok := init.x.(*strLit); ok && typ.isArray() && typ.elem.isCharacter() {
		if int64(len(text.text)) > typ.length {
			fail(init.pos, "a %d byte string does not fit %s", len(text.text), typ)
		}
		copy(image, text.text)
		return true
	}
	if typ.isFloat() {
		value, ok := foldFloat(init.x, l.scope.enums)
		if !ok {
			return false
		}
		writeFloat(image, typ, value)
		return true
	}
	if !typ.isInteger() {
		return false
	}
	value, err := foldConstant(init.x, l.scope.enums)
	if err != nil {
		return false
	}
	writeInteger(image, l.layout.Size(typ.metalsType()), uint64(value))
	return true
}

func writeInteger(image []byte, size int, value uint64) {
	for i := 0; i < size; i++ {
		image[i] = byte(value >> (8 * i))
	}
}

func writeFloat(image []byte, typ *ctype, value float64) {
	if typ.kind == kFloat {
		binary.LittleEndian.PutUint32(image, math.Float32bits(float32(value)))
		return
	}
	binary.LittleEndian.PutUint64(image, math.Float64bits(value))
}

// foldFloat evaluates the constant expressions that reach a float: a
// literal, a sign in front of one, and a cast around it. Anything else
// is code, and the caller falls back to the init function.
func foldFloat(e expr, enums map[string]int64) (float64, bool) {
	switch e := e.(type) {
	case *floatLit:
		return e.val, true
	case *castExpr:
		return foldFloat(e.x, enums)
	case *unaryExpr:
		value, ok := foldFloat(e.x, enums)
		switch e.op {
		case "-":
			return -value, ok
		case "+":
			return value, ok
		}
		return 0, false
	}
	if value, err := foldConstant(e, enums); err == nil {
		return float64(value), true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// The init function
// ---------------------------------------------------------------------------

// emitInit builds the function that writes the file-scope storage a
// static image could not carry. It runs after every other function is
// lowered, so an initializer may call anything the unit defines.
func (l *lowerer) emitInit(pending []*globalDecl) {
	if len(pending) == 0 {
		return
	}

	f := &funcLowerer{
		l:     l,
		fn:    &funcDecl{name: initName, result: typeVoid},
		out:   &metals.Func{Name: initName},
		names: map[string]bool{},
	}
	f.blocks = [][]metals.Instr{nil}
	f.scopes = []map[string]*binding{{}}

	for _, global := range pending {
		ptr := f.temp()
		f.emit(&metals.GlobalPtr{Dst: ptr, Name: global.symbol})
		// Module storage is already zero, so only what the initializer
		// names has to be written.
		f.initialize(ptr, global.typ, global.init)
	}
	f.emit(&metals.Return{})

	f.out.Body = f.blocks[0]
	l.out.Funcs = append(l.out.Funcs, f.out)
	l.out.Init = initName
}

// initialize writes an initializer through a pointer to the object it
// initializes. It does not zero what the initializer leaves out: a
// caller that needs that — a local, whose storage is a fresh slot —
// zeroes first.
func (f *funcLowerer) initialize(ptr string, typ *ctype, init *initializer) {
	if !init.braced() {
		f.initializeExpr(ptr, typ, init)
		return
	}

	switch {
	case typ.isArray():
		if typ.length < 0 {
			fail(init.pos, "an array of unknown length has nowhere to put an initializer")
		}
		if int64(len(init.list)) > typ.length {
			fail(init.pos, "%d initializers for an array of %d", len(init.list), typ.length)
		}
		first := f.temp()
		f.emit(&metals.ElemPtr{Dst: first, Ptr: ptr})
		for i, element := range init.list {
			f.initialize(f.elementAt(first, i), typ.elem, element)
		}

	case typ.isStruct():
		if len(init.list) > len(typ.fields) {
			fail(init.pos, "%d initializers for a %s, which has %d fields",
				len(init.list), typ, len(typ.fields))
		}
		for i, member := range init.list {
			field := f.temp()
			f.emit(&metals.FieldPtr{Dst: field, Ptr: ptr, Index: i})
			f.initialize(field, typ.fields[i].typ, member)
		}

	case len(init.list) == 0:
		// `int x = {}` leaves the zero already there.

	case len(init.list) == 1:
		f.initialize(ptr, typ, init.list[0])

	default:
		fail(init.pos, "%d initializers for a %s", len(init.list), typ)
	}
}

func (f *funcLowerer) initializeExpr(ptr string, typ *ctype, init *initializer) {
	// `char message[] = "hello"` copies, so this is the one initializer
	// that writes an array without a brace in sight.
	if text, ok := init.x.(*strLit); ok && typ.isArray() && typ.elem.isCharacter() {
		if int64(len(text.text)) > typ.length {
			fail(init.pos, "a %d byte string does not fit %s", len(text.text), typ)
		}
		first := f.temp()
		f.emit(&metals.ElemPtr{Dst: first, Ptr: ptr})
		for i := 0; i < len(text.text); i++ {
			byteVal := f.constant(typ.elem, fmt.Sprintf("%d", int8(text.text[i])))
			f.emit(&metals.Store{Ptr: f.elementAt(first, i), Val: byteVal})
		}
		return
	}
	if typ.isArray() {
		fail(init.pos, "an array is initialized from a brace list or a string, not a %s", typ)
	}
	value := f.convert(f.exprAs(init.x, typ), typ, init.pos)
	f.emit(&metals.Store{Ptr: ptr, Val: value.reg})
}

// elementAt steps a decayed pointer to one element, and hands back the
// pointer itself for the first, which is where it already is.
func (f *funcLowerer) elementAt(first string, index int) string {
	if index == 0 {
		return first
	}
	count := f.constant(typeLong, fmt.Sprintf("%d", index))
	at := f.temp()
	f.emit(&metals.PtrOffset{Dst: at, Ptr: first, N: count})
	return at
}

// zeroObject writes zeros over the whole of an object. A local's storage
// is a fresh `slot`, which the native backends zero and the evaluator
// leaves unwritten on purpose — so a partial initializer at block scope
// says what the rest is rather than depending on the tier.
func (f *funcLowerer) zeroObject(ptr string, typ *ctype, pos position) {
	switch {
	case typ.isArray():
		if typ.length < 0 {
			fail(pos, "an array of unknown length has no storage to zero")
		}
		first := f.temp()
		f.emit(&metals.ElemPtr{Dst: first, Ptr: ptr})
		for i := int64(0); i < typ.length; i++ {
			f.zeroObject(f.elementAt(first, int(i)), typ.elem, pos)
		}

	case typ.isStruct():
		for i, field := range typ.fields {
			at := f.temp()
			f.emit(&metals.FieldPtr{Dst: at, Ptr: ptr, Index: i})
			f.zeroObject(at, field.typ, pos)
		}

	default:
		f.emit(&metals.Store{Ptr: ptr, Val: f.zero(typ)})
	}
}

// recordOf is the METALS record a struct type declares, which the layout
// table needs to answer a field's offset.
func (l *lowerer) recordOf(typ *ctype, pos position) *metals.RecordShape {
	for _, decl := range l.out.Types {
		if decl.Name == typ.tag {
			return decl.Record
		}
	}
	fail(pos, "struct %s has no layout here", typ.tag)
	return nil
}
