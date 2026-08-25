package wasm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// The corpus covers what a translated program computes. These cover what
// it cannot: the decoder's own edges, and the errors that exist to stop a
// module translating into something that looks complete and is not.

func TestUnsignedLEB(t *testing.T) {
	cases := []struct {
		bytes []byte
		want  uint32
	}{
		{[]byte{0x00}, 0},
		{[]byte{0x7F}, 127},
		{[]byte{0x80, 0x01}, 128},
		{[]byte{0xE5, 0x8E, 0x26}, 624485},
		{[]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x0F}, 4294967295},
	}
	for _, c := range cases {
		r := &reader{src: c.bytes}
		got, err := r.u32()
		if err != nil {
			t.Errorf("u32(%v): %v", c.bytes, err)
			continue
		}
		if got != c.want {
			t.Errorf("u32(%v) = %d, want %d", c.bytes, got, c.want)
		}
		if !r.eof() {
			t.Errorf("u32(%v) left %d byte(s) unread", c.bytes, len(c.bytes)-r.pos)
		}
	}

	// A sixth group cannot belong to a 32-bit value, so it is a malformed
	// encoding rather than a large number.
	r := &reader{src: []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x01}}
	if _, err := r.u32(); err == nil {
		t.Error("a six-group unsigned LEB128 should not decode")
	}
}

func TestSignedLEB(t *testing.T) {
	cases := []struct {
		bytes []byte
		want  int64
	}{
		{[]byte{0x00}, 0},
		{[]byte{0x01}, 1},
		{[]byte{0x7F}, -1},
		{[]byte{0x7E}, -2},
		{[]byte{0x80, 0x01}, 128},
		{[]byte{0x80, 0x7F}, -128},
		{[]byte{0xC0, 0xBB, 0x78}, -123456},
	}
	for _, c := range cases {
		r := &reader{src: c.bytes}
		got, err := r.i64()
		if err != nil {
			t.Errorf("i64(%v): %v", c.bytes, err)
			continue
		}
		if got != c.want {
			t.Errorf("i64(%v) = %d, want %d", c.bytes, got, c.want)
		}
	}
}

func TestRejectsNonWasm(t *testing.T) {
	for _, input := range [][]byte{
		{},
		[]byte("not a wasm module at all"),
		{0x00, 0x61, 0x73, 0x6D, 0x09, 0x00, 0x00, 0x00}, // wrong version
	} {
		if _, err := decode(input); err == nil {
			t.Errorf("decode(%q) should have failed", input)
		}
	}
}

// A value type the bridge does not translate has to be named, not
// skipped: a module carrying one would otherwise lose part of itself
// silently.
func TestRejectsUntranslatedValueTypes(t *testing.T) {
	for _, c := range []struct{ b, want string }{
		{"\x7B", "SIMD"},
		{"\x70", "reference types"},
		{"\x6F", "reference types"},
	} {
		r := &reader{src: []byte(c.b)}
		_, err := r.valType()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("valType(%q) = %v, want a mention of %q", c.b, err, c.want)
		}
	}
}

func TestModuleName(t *testing.T) {
	for input, want := range map[string]string{
		"fib.c":            "wasm:fib",
		"a/b/thing.wasm":   "wasm:thing",
		"/tmp/no-suffix":   "wasm:no-suffix",
		"dir.d/nested.c":   "wasm:nested",
		"UPPER.C":          "wasm:UPPER",
		"many.dots.here.c": "wasm:many.dots.here",
	} {
		if got := ModuleName(input); got != want {
			t.Errorf("ModuleName(%q) = %q, want %q", input, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Through clang
// ---------------------------------------------------------------------------

// compileSource writes a C file to a temporary directory and runs the
// whole bridge over it.
func compileSource(t *testing.T, source string) (*metals.Module, error) {
	t.Helper()
	if err := Toolchain(); err != nil {
		t.Skipf("skipping: %v", err)
	}
	path := filepath.Join(t.TempDir(), "case.c")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return CompileC(path)
}

// Everything this bridge produces has to survive the verifier: it is a
// producer like any other, and the format trusts none of them.
func TestTranslatedModuleVerifies(t *testing.T) {
	module, err := compileSource(t, `
__attribute__((import_module("gyro:rt"), import_name("println<i64>")))
extern void put(long long);

static int values[16];

int main(void) {
	for (int i = 0; i < 16; i++) values[i] = i * 3;
	long long total = 0;
	for (int i = 0; i < 16; i++) total += values[i];
	put(total);
	return 0;
}
`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatalf("does not verify: %v\n%s", err, metals.Print(module))
	}

	if metals.Scheme(module.Name) != "wasm" {
		t.Errorf("module %q does not carry the wasm scheme", module.Name)
	}
	if len(module.Memories) != 1 {
		t.Fatalf("expected one linear memory, got %d", len(module.Memories))
	}

	// The synthesized entry is what makes the module runnable: a wasm
	// module has exports and a convention, not a main.
	var main *metals.Func
	for _, fn := range module.Funcs {
		if fn.Name == "main" {
			main = fn
		}
	}
	if main == nil || !main.Export {
		t.Fatal("the translated module has no exported @main")
	}
	if len(main.Params) != 0 {
		t.Errorf("@main takes %d parameter(s); a METALS entry takes none", len(main.Params))
	}
}

// The printed form has to reparse, because a producer that emits text
// the parser cannot read is not producing METALS.
func TestTranslatedModuleRoundTrips(t *testing.T) {
	module, err := compileSource(t, `
__attribute__((import_module("gyro:rt"), import_name("println<f64>")))
extern void put(double);

static const char text[] = "round trip";

int main(void) {
	double d = 0.5;
	for (int i = 0; i < 3; i++) d = d * 1.5 + text[i];
	put(d);
	return 0;
}
`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	printed := metals.Print(module)
	again, err := metals.Parse(printed)
	if err != nil {
		t.Fatalf("the printed module does not reparse: %v\n%s", err, printed)
	}
	if reprinted := metals.Print(again); reprinted != printed {
		t.Error("the printed module is not canonical")
	}
	// A data segment with arbitrary bytes is the interesting part of the
	// round trip, since it is the only place the text form carries binary.
	if len(again.Data) != len(module.Data) {
		t.Errorf("%d data segment(s) survived of %d", len(again.Data), len(module.Data))
	}
}

// An import with no module scheme is the one error a C author is likely
// to hit, so it says what to write rather than what went wrong.
func TestImportWithoutSchemeIsRefused(t *testing.T) {
	_, err := compileSource(t, `
extern void put(long long);
int main(void) { put(1); return 0; }
`)
	if err == nil {
		t.Fatal("an import from no module should have been refused")
	}
	if !strings.Contains(err.Error(), "import_module") {
		t.Errorf("error %q does not say how to declare the import", err)
	}
}

// A module with no main cannot become a program, and saying so beats
// producing one that no consumer will run.
func TestMissingMainIsRefused(t *testing.T) {
	_, err := compileSource(t, `int helper(int x) { return x + 1; }`)
	if err == nil || !strings.Contains(err.Error(), "exports no main") {
		t.Errorf("error %v does not report the missing main", err)
	}
}
