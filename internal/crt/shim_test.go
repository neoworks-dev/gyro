package crt

import (
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// The shim is the one place that spells C's real types, so what it
// generates is worth reading in a test: the corpus can only report that
// a program printed the right thing, and the point of the struct
// declarations is what they assert on a build that would otherwise be
// silently wrong.

func pixelModule() *metals.Module {
	return &metals.Module{
		Name: "test",
		Types: []*metals.TypeDecl{{
			Name: "Pixel",
			Record: &metals.RecordShape{Fields: []metals.FieldDecl{
				{Name: "r", Type: metals.TUint8},
				{Name: "depth", Type: metals.TF32},
				{Name: "id", Type: metals.TInt64},
				{Name: "lit", Type: metals.TBool},
			}},
		}},
		Imports: []*metals.Import{{
			Module: metals.ExternModule("gfx"),
			Funcs: []metals.ImportedFunc{{
				Name: "shade",
				Sig: &metals.FnType{
					Params: []metals.Type{&metals.Named{Name: "Pixel"}, metals.TInt32},
					Result: &metals.Named{Name: "Pixel"},
				},
			}},
		}},
	}
}

func TestShimDeclaresRecordLayout(t *testing.T) {
	source, libraries := Shim(pixelModule())

	if len(libraries) != 1 || libraries[0] != "gfx" {
		t.Fatalf("libraries = %v, want [gfx]", libraries)
	}

	// C's own layout for these members: 1, pad to 4, 4, pad to 8, 8, 1,
	// and the whole rounded to the widest member's alignment.
	want := []string{
		"typedef struct { uint8_t r; float depth; int64_t id; uint8_t lit; } gy_sPixel;",
		`_Static_assert(sizeof(gy_sPixel) == 24, "layout of Pixel");`,
		`_Static_assert(offsetof(gy_sPixel, r) == 0, "layout of Pixel");`,
		`_Static_assert(offsetof(gy_sPixel, depth) == 4, "layout of Pixel");`,
		`_Static_assert(offsetof(gy_sPixel, id) == 8, "layout of Pixel");`,
		`_Static_assert(offsetof(gy_sPixel, lit) == 16, "layout of Pixel");`,
	}
	for _, line := range want {
		if !strings.Contains(source, line) {
			t.Errorf("shim is missing:\n%s\ngot:\n%s", line, source)
		}
	}
}

func TestShimPassesRecordsByPointer(t *testing.T) {
	source, _ := Shim(pixelModule())

	// The real function takes and returns the struct by value; the
	// wrapper takes a pointer to the caller's copy and writes the result
	// through the hidden one, which is the shape both backends emit for
	// an aggregate.
	want := []string{
		`extern gy_sPixel gy_ffi_shade_target(gy_sPixel, int32_t) __asm__("shade");`,
		"void gy_ffi_shade(void *gy_out, void * a0, int64_t a1) {",
		"*(gy_sPixel *)gy_out = gy_ffi_shade_target(*(const gy_sPixel *)a0, (int32_t)a1);",
	}
	for _, line := range want {
		if !strings.Contains(source, line) {
			t.Errorf("shim is missing:\n%s\ngot:\n%s", line, source)
		}
	}

	declarations := ShimDeclarations(pixelModule())
	const prototype = "void gy_ffi_shade(void *, void *, int64_t);"
	if !strings.Contains(declarations, prototype) {
		t.Errorf("declarations = %q, want %q", declarations, prototype)
	}
}

// pointerModule is the qsort shape: an address, a callback, and a
// pointer to a record the wrapper therefore has to declare.
func pointerModule() *metals.Module {
	comparator := &metals.CodeType{
		Params: []metals.Type{&metals.AnyPtr{}, &metals.AnyPtr{}},
		Result: metals.TInt32,
	}
	return &metals.Module{
		Name: "test",
		Types: []*metals.TypeDecl{{
			Name: "Pixel",
			Record: &metals.RecordShape{Fields: []metals.FieldDecl{
				{Name: "r", Type: metals.TUint8},
			}},
		}},
		Imports: []*metals.Import{{
			Module: metals.ExternModule("c"),
			Funcs: []metals.ImportedFunc{
				{Name: "qsort", Sig: &metals.FnType{
					Params: []metals.Type{&metals.AnyPtr{}, metals.TUsize, metals.TUsize, comparator},
				}},
				{Name: "frexp", Sig: &metals.FnType{
					Params: []metals.Type{metals.TF64, &metals.Ptr{Elem: metals.TInt32}},
					Result: metals.TF64,
				}},
				{Name: "shade", Sig: &metals.FnType{
					Params: []metals.Type{&metals.Ptr{Elem: &metals.Named{Name: "Pixel"}}},
				}},
				{Name: "row", Sig: &metals.FnType{
					Params: []metals.Type{&metals.Ptr{Elem: &metals.ArrayOf{Elem: metals.TInt32, Len: 4}}},
				}},
			},
		}},
	}
}

// An address crosses a native boundary as itself, so the wrapper has to
// spell C's real pointer types — and a pointer type is a declarator
// rather than a base type with a star glued on. `int (*)[4]` is a
// pointer to an array and `int *[4]` is an array of pointers, and only
// the first is what a ptr<array<i32, 4>> means.
func TestShimSpellsPointerAndCallbackTypes(t *testing.T) {
	source, _ := Shim(pointerModule())

	want := []string{
		// The uniform shape a backend calls, and the real one C declared.
		`extern void gy_ffi_qsort_target(void *, uint64_t, uint64_t, int32_t (*)(void *, void *)) __asm__("qsort");`,
		"void gy_ffi_qsort(void * a0, int64_t a1, int64_t a2, gy_code a3) {",
		"gy_ffi_qsort_target((void *)a0, (uint64_t)a1, (uint64_t)a2, (int32_t (*)(void *, void *))a3);",

		// An out-parameter is the pointer C declared.
		`extern double gy_ffi_frexp_target(double, int32_t *) __asm__("frexp");`,
		"double gy_ffi_frexp(double a0, void * a1) {",

		// A pointer to a record still declares the record, so the layout
		// asserts cover an out-parameter's struct too.
		`extern void gy_ffi_shade_target(gy_sPixel *) __asm__("shade");`,
		`_Static_assert(sizeof(gy_sPixel) == 1, "layout of Pixel");`,

		// The declarator cases C reads inside out.
		`extern void gy_ffi_row_target(int32_t (*)[4]) __asm__("row");`,
	}
	for _, line := range want {
		if !strings.Contains(source, line) {
			t.Errorf("shim is missing:\n%s\ngot:\n%s", line, source)
		}
	}
}

// A union crossing to C is declared as a union, and the layout claim it
// makes is the one worth asserting: every member at offset zero, and the
// size the widest member's rounded to the strictest alignment. Declaring
// it as `void *` would have compiled and checked nothing.
func TestShimDeclaresAUnion(t *testing.T) {
	module := &metals.Module{
		Name: "test",
		Types: []*metals.TypeDecl{{
			Name: "Event",
			Union: &metals.UnionShape{Members: []metals.FieldDecl{
				{Name: "kind", Type: metals.TUint32},
				{Name: "storage", Type: &metals.ArrayOf{Elem: metals.TUint64, Len: 7}},
			}},
		}},
		Imports: []*metals.Import{{
			Module: metals.ExternModule("SDL2"),
			Funcs: []metals.ImportedFunc{{
				Name: "SDL_PollEvent",
				Sig: &metals.FnType{
					Params: []metals.Type{&metals.Ptr{Elem: &metals.Named{Name: "Event"}}},
					Result: metals.TInt32,
				},
			}},
		}},
	}

	source, _ := Shim(module)
	want := []string{
		"typedef union { uint32_t kind; uint64_t storage[7]; } gy_uEvent;",
		`_Static_assert(sizeof(gy_uEvent) == 56, "layout of Event");`,
		`_Static_assert(offsetof(gy_uEvent, kind) == 0, "layout of Event");`,
		`_Static_assert(offsetof(gy_uEvent, storage) == 0, "layout of Event");`,
		`extern int32_t gy_ffi_SDL_PollEvent_target(gy_uEvent *) __asm__("SDL_PollEvent");`,
		"int64_t gy_ffi_SDL_PollEvent(void * a0) {",
	}
	for _, line := range want {
		if !strings.Contains(source, line) {
			t.Errorf("shim is missing:\n%s\ngot:\n%s", line, source)
		}
	}
}

func TestShimIsEmptyWithoutForeignImports(t *testing.T) {
	module := &metals.Module{Name: "test"}
	if source, libraries := Shim(module); source != "" || libraries != nil {
		t.Errorf("Shim = (%q, %v), want empty", source, libraries)
	}
}
