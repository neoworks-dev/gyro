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

func TestShimIsEmptyWithoutForeignImports(t *testing.T) {
	module := &metals.Module{Name: "test"}
	if source, libraries := Shim(module); source != "" || libraries != nil {
		t.Errorf("Shim = (%q, %v), want empty", source, libraries)
	}
}
