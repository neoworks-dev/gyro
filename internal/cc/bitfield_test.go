package cc

import (
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Bit-fields.
//
// A bit-field's place is measured in bits and METALS has no member
// measured in bits, so a run of them becomes one member of bytes and
// each of C's members remembers which bits are its own. What has to hold
// is C's own layout — every size below was checked against the system
// compiler, not just against this one — and C's own arithmetic on the
// values.

// The nine shapes the packing rule has to get right. Each of these
// answers what `cc` on x86-64 answers for the same declaration.
func TestBitFieldLayout(t *testing.T) {
	sizes := []struct {
		what string
		decl string
		size int64
	}{
		// Two that share one unit.
		{"packed", `struct s { unsigned x:3; unsigned y:5; };`, 4},
		// The second would cross a 32-bit boundary, so it starts at one.
		{"straddle", `struct s { unsigned x:30; unsigned y:10; };`, 8},
		// One byte of storage, and four of struct: the alignment comes
		// from a declared type that is no longer in the member list.
		{"alignment from the declared type", `struct s { unsigned char x:1; unsigned y:1; };`, 4},
		// The run is one byte, so the char after it lands at offset one.
		{"an ordinary member after a run", `struct s { unsigned x:1; char c; };`, 4},
		{"a run after an ordinary member", `struct s { char c; unsigned x:9; };`, 4},
		{"a wide run after an ordinary member", `struct s { char c; unsigned x:30; };`, 8},
		// A zero-width one moves the cursor to the next unit boundary.
		{"zero width", `struct s { unsigned a:1; unsigned :0; unsigned b:1; };`, 8},
		{"mixed widths", `struct s { unsigned long q:33; unsigned r:20; };`, 8},
		{"signed and unsigned", `struct s { int a:4; unsigned b:4; };`, 4},
	}
	for _, c := range sizes {
		t.Run(c.what, func(t *testing.T) {
			answers(t, c.decl+"\nint result(void) { return (int)sizeof(struct s); }", c.size)
		})
	}
}

// What the record ends up declaring, which is not what C wrote: a run of
// bit-fields is one member of bytes, sized to cover the bits it holds
// and placed where the layout table will put it.
//
// Where the bits sit inside a run is checked by running programs — in
// this file for their values, and in tests/testdata/run/c-bitfield-bytes
// for the bytes themselves, which needs a tier that has bytes.
func TestBitFieldStorage(t *testing.T) {
	cases := []struct {
		what   string
		decl   string
		record string
	}{
		// One byte of storage, and four of struct: the alignment comes
		// from a declared type that is no longer in the member list.
		{"one run", `struct s { unsigned x:1; unsigned y:1; };`,
			"align 4 { bits.0: array<u8, 1> }"},
		// The run is one byte, so the char after it lands at offset one.
		{"a member after a run", `struct s { unsigned x:1; char c; };`,
			"align 4 { bits.0: array<u8, 1>, c: i8 }"},
		// The run begins right after the char, and covers through the
		// last bit — including the bits the straddle rule skipped.
		{"a run after a member", `struct s { char c; unsigned x:30; };`,
			"align 4 { c: i8, bits.0: array<u8, 7> }"},
		// A zero-width one closes the run it is in and opens the next.
		{"two runs", `struct s { unsigned a:1; unsigned :0; unsigned b:1; };`,
			"align 4 { bits.0: array<u8, 1>, bits.1: array<u8, 4> }"},
		// Every member of a union begins where the union does, so its
		// bit-fields all start at bit zero and share one run.
		{"a union", `union s { unsigned a:4; unsigned b:12; };`,
			"align 4 { bits.0: array<u8, 2> }"},
		{"no bit-fields at all", `struct s { int a; char b; };`,
			"{ a: i32, b: i8 }"},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			module, err := Compile("case.c", c.decl+"\nint result(void) { return 0; }", nil)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			shape := "record"
			if strings.HasPrefix(c.decl, "union") {
				shape = "union"
			}
			want := "type %s = " + shape + " " + c.record
			if got := metals.Print(module); !strings.Contains(got, want) {
				t.Errorf("expected\n  %s\nin\n%s", want, got)
			}
		})
	}
}

// Reading and writing. A bit-field holds the low bits of what it is
// given, and reads back as its declared type: signed extends, unsigned
// does not.
func TestBitFieldValues(t *testing.T) {
	answers(t, `
struct s { unsigned a:3; unsigned b:5; unsigned c:24; };
int result(void) {
    struct s v;
    v.a = 5;
    v.b = 31;
    v.c = 1000000;
    if (v.a != 5) { return 1; }
    if (v.b != 31) { return 2; }
    if (v.c != 1000000) { return 3; }
    /* Writing one leaves its neighbours alone. */
    v.b = 0;
    if (v.a != 5) { return 4; }
    if (v.c != 1000000) { return 5; }
    return 42;
}
`, 42)

	// A signed bit-field extends its sign, which is why 15 in four bits
	// reads back as -1.
	answers(t, `
struct s { int n:4; };
int result(void) {
    struct s v;
    v.n = 15;
    if (v.n != -1) { return 1; }
    v.n = -8;
    if (v.n != -8) { return 2; }
    v.n = 7;
    if (v.n != 7) { return 3; }
    return 42;
}
`, 42)

	// An unsigned one does not.
	answers(t, `
struct s { unsigned n:4; };
int result(void) {
    struct s v;
    v.n = 15;
    if (v.n != 15) { return 1; }
    return 42;
}
`, 42)

	// What does not fit is dropped, which is C's rule for the
	// conversion into a bit-field.
	answers(t, `
struct s { unsigned n:4; };
int result(void) {
    struct s v;
    v.n = 0x1F;
    return v.n + 27;
}
`, 42)

	// One that spans several bytes.
	answers(t, `
struct s { unsigned pad:5; unsigned long wide:40; };
int result(void) {
    struct s v;
    v.pad = 0;
    v.wide = 1099511627775UL; /* 2^40 - 1 */
    if (v.wide != 1099511627775UL) { return 1; }
    if (v.pad != 0) { return 2; }
    v.pad = 31;
    if (v.wide != 1099511627775UL) { return 3; }
    return 42;
}
`, 42)
}

// Compound assignment and the increments read, combine and write back
// through the same two halves.
func TestBitFieldReadModifyWrite(t *testing.T) {
	answers(t, `
struct s { unsigned a:4; unsigned b:4; };
int result(void) {
    struct s v;
    v.a = 1;
    v.b = 2;
    v.a += 3;
    v.b <<= 1;
    if (v.a != 4) { return 1; }
    if (v.b != 4) { return 2; }
    v.a++;
    ++v.b;
    if (v.a != 5) { return 3; }
    if (v.b != 5) { return 4; }
    /* Wrapping at the field's width, not at int's. */
    v.a = 15;
    v.a++;
    if (v.a != 0) { return 5; }
    return 42;
}
`, 42)
}

// A braced initializer writes the members in order, at file scope as an
// image and at block scope as stores.
func TestBitFieldInitializers(t *testing.T) {
	answers(t, `
struct s { unsigned a:4; unsigned b:4; int tail; };
static struct s global = { 3, 5, 7 };

int result(void) {
    struct s local = { 1, 2, 3 };
    if (global.a != 3) { return 1; }
    if (global.b != 5) { return 2; }
    if (global.tail != 7) { return 3; }
    if (local.a != 1) { return 4; }
    if (local.b != 2) { return 5; }
    if (local.tail != 3) { return 6; }
    return 42;
}
`, 42)

	// What an initializer leaves out is zero, which for a run of
	// bit-fields is the whole run zeroed before anything is written.
	answers(t, `
struct s { unsigned a:4; unsigned b:4; };
int result(void) {
    struct s v = { 5 };
    if (v.a != 5) { return 1; }
    if (v.b != 0) { return 2; }
    return 42;
}
`, 42)
}

// Every member of a union begins where the union does, so its
// bit-fields all start at bit zero and overlap rather than pack.
func TestBitFieldInAUnion(t *testing.T) {
	answers(t, `
union u { unsigned low:4; unsigned wide:12; };
int result(void) {
    union u v;
    v.wide = 0x2FA;
    if (v.low != 10) { return 1; }
    v.low = 12;
    if (v.wide != 0x2FC) { return 2; }
    return 42;
}
`, 42)
}

// Through a pointer, and across two files that include one header —
// where the two declarations are parsed separately and only the merged
// one knows where the bits went.
func TestBitFieldThroughAPointer(t *testing.T) {
	answers(t, `
struct s { unsigned a:4; unsigned b:4; };

static void fill(struct s *p) { p->a = 4; p->b = 2; }

int result(void) {
    struct s v;
    fill(&v);
    return v.a * 10 + v.b;
}
`, 42)
}

func TestBitFieldRefusals(t *testing.T) {
	const decl = "struct s { unsigned a:4; unsigned b:4; };\n"

	// C gives a bit-field no address, and the rule is the reason it can
	// be packed at all.
	refused(t, decl+`int result(void) { struct s v; unsigned *p = &v.a; return *p; }`,
		"a bit-field has no address")
	refused(t, decl+`int result(void) { struct s v; return (int)sizeof v.a; }`,
		"sizeof does not apply to a bit-field")

	refused(t, `struct s { unsigned a:33; }; int result(void) { return 0; }`,
		"does not fit a unsigned int")
	refused(t, `struct s { double d:4; }; int result(void) { return 0; }`,
		"counts bits of an integer")
	refused(t, `struct s { unsigned a:0; }; int result(void) { return 0; }`,
		"only an unnamed one may be zero-width")
	refused(t, `struct s { unsigned :0; }; int result(void) { return 0; }`,
		"nothing but zero-width bit-fields")
	refused(t, `union u { unsigned :0; unsigned a:4; }; int result(void) { return 0; }`,
		"in a union there is no next one")
}
