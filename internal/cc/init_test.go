package cc

import (
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// String literals and the storage they live in.

func TestStringLiteralIsAnArrayOfChar(t *testing.T) {
	answers(t, `int result(void) { return (int)sizeof("hello"); }`, 6)
	answers(t, `int result(void) { return "hello"[1]; }`, 'e')
	answers(t, `int result(void) { const char *s = "hi"; return s[0] + s[1]; }`, 'h'+'i')
}

func TestStringLiteralIsInterned(t *testing.T) {
	// Two identical literals are one array, which is why comparing their
	// addresses answers true.
	answers(t, `
int result(void) {
    const char *a = "same";
    const char *b = "same";
    return a == b;
}
`, 1)

	module := compiled(t, `
const char *first(void) { return "one"; }
const char *second(void) { return "two"; }
const char *again(void) { return "one"; }
int result(void) { return 0; }
`)
	images := 0
	for _, global := range module.Globals {
		if global.Bytes != nil {
			images++
		}
	}
	if images != 2 {
		t.Fatalf("expected 2 interned literals, found %d\n%s", images, metals.Print(module))
	}
}

func TestAdjacentLiteralsJoin(t *testing.T) {
	answers(t, `int result(void) { return (int)sizeof("ab" "cd"); }`, 5)
	answers(t, `int result(void) { return "ab" "cd"[3]; }`, 'd')
}

func TestEscapesReachTheImage(t *testing.T) {
	answers(t, `int result(void) { return "\t\n\\"[0] + "\t\n\\"[1] + "\t\n\\"[2]; }`, 9+10+92)
	answers(t, `int result(void) { return "\x41\102"[0] + "\x41\102"[1]; }`, 'A'+'B')
}

func TestCharArrayCopiesTheBytes(t *testing.T) {
	// `char buf[] = "hi"` is the one place a literal is not an address:
	// the bytes are copied into the array, so writing to it is allowed.
	answers(t, `
int result(void) {
    char buf[] = "hi";
    buf[0] = 'H';
    return buf[0] + buf[1] + (int)sizeof(buf);
}
`, 'H'+'i'+3)

	// A longer array than the string zeroes the rest.
	answers(t, `
int result(void) {
    char buf[6] = "ab";
    int i, total;
    total = 0;
    for (i = 0; i < 6; i++) {
        total = total * 2 + buf[i];
    }
    return total;
}
`, ((((('a'*2 + 'b') * 2) * 2) * 2) * 2))
}

// ---------------------------------------------------------------------------
// Initializers
// ---------------------------------------------------------------------------

func TestFileScopeConstantsBecomeAnImage(t *testing.T) {
	source := `
static int counter = 7;
static char tag = 'x';
static double ratio = 0.5;
static int limits[4] = {10, 20, 30};

int result(void) {
    return counter + tag + (int)(ratio * 4) + limits[0] + limits[1] + limits[2] + limits[3];
}
`
	answers(t, source, 7+'x'+2+10+20+30+0)

	// Nothing here names an address, so the module needs no init at all.
	module := compiled(t, source)
	if module.Init != "" {
		t.Fatalf("expected no init function, got @%s\n%s", module.Init, metals.Print(module))
	}
	for _, global := range module.Globals {
		if global.Bytes == nil {
			t.Fatalf("global @%s has no static image\n%s", global.Name, metals.Print(module))
		}
	}
}

func TestFileScopeAddressesRunBeforeMain(t *testing.T) {
	source := `
static int table[3] = {4, 5, 6};
static int *cursor = &table[1];
static const char *label = "gyro";

int result(void) {
    return *cursor + label[0] + (int)(label - label);
}
`
	answers(t, source, 5+'g')

	module := compiled(t, source)
	if module.Init == "" {
		t.Fatalf("an address initializer needs an init function\n%s", metals.Print(module))
	}
}

func TestArrayLengthComesFromTheInitializer(t *testing.T) {
	answers(t, `
static int weights[] = {2, 3, 5, 7};
int result(void) { return (int)sizeof(weights) / (int)sizeof(weights[0]); }
`, 4)
	answers(t, `
static char name[] = "abc";
int result(void) { return (int)sizeof(name); }
`, 4)
}

func TestNestedInitializers(t *testing.T) {
	answers(t, `
struct Point { int x; int y; };
struct Line { struct Point from; struct Point to; };

static struct Line edge = { {1, 2}, {30, 40} };

int result(void) {
    return edge.from.x + edge.from.y * 10 + edge.to.x + edge.to.y;
}
`, 1+20+30+40)

	answers(t, `
struct Point { int x; int y; };
int result(void) {
    struct Point grid[3] = { {1, 1}, {2, 4} };
    return grid[0].x + grid[1].y + grid[2].x + grid[2].y;
}
`, 1+4+0+0)
}

func TestLocalInitializersZeroWhatTheyOmit(t *testing.T) {
	answers(t, `
int result(void) {
    int values[5] = {9};
    int i, total;
    total = 0;
    for (i = 0; i < 5; i++) {
        total = total + values[i];
    }
    return total;
}
`, 9)

	answers(t, `
struct Big { int a; int b; int c; };
int result(void) {
    struct Big b = {7};
    return b.a * 100 + b.b * 10 + b.c;
}
`, 700)
}

func TestRefusesInitializersThatDoNotFit(t *testing.T) {
	refused(t, `static int a[2] = {1, 2, 3}; int result(void) { return 0; }`,
		"3 initializers for an array of 2")
	refused(t, `static char s[2] = "abc"; int result(void) { return 0; }`,
		"does not fit")
	refused(t, `static int x = {1, 2}; int result(void) { return 0; }`,
		"2 initializers for a int")
}

// A global whose type holds a pointer anywhere inside it cannot carry a
// static image, because a pointer has no value until the program is laid
// out. The front end never asks for one; the verifier is what says so.
func TestVerifierRefusesAPointerImage(t *testing.T) {
	module := &metals.Module{
		Name: "cc:case",
		Globals: []*metals.Global{{
			Name:  "p",
			Type:  &metals.Opt{Elem: &metals.Ptr{Elem: metals.TInt8}},
			Bytes: make([]byte, 8),
		}},
	}
	err := metals.Verify(module)
	if err == nil || !strings.Contains(err.Error(), "no value until the program is laid out") {
		t.Fatalf("expected a refusal naming the pointer, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// static locals
// ---------------------------------------------------------------------------

func TestStaticLocalKeepsItsValue(t *testing.T) {
	answers(t, `
static int next(void) {
    static int seen = 10;
    seen = seen + 1;
    return seen;
}
int result(void) { return next() * 100 + next() * 10 + next(); }
`, 11*100+12*10+13)
}

func TestStaticLocalsDoNotCollide(t *testing.T) {
	// Two functions each with a `static int count`, and one function with
	// two blocks that each declare one: four objects, four names.
	answers(t, `
static int a(void) { static int count; count++; return count; }
static int b(void) { static int count = 100; count++; return count; }
static int c(int which) {
    if (which) { static int count = 5; count++; return count; }
    { static int count = 50; count++; return count; }
}
int result(void) {
    a(); a();
    b();
    c(1); c(1);
    c(0);
    return a() * 1000 + b() * 100 + c(1) * 10 + c(0);
}
`, 3*1000+102*100+8*10+52)
}

func TestStaticLocalArrayAndString(t *testing.T) {
	answers(t, `
static int tally(int n) {
    static int seen[4];
    static const char *tag = "abcd";
    seen[n] = seen[n] + 1;
    return seen[n] * 10 + tag[n];
}
int result(void) {
    tally(0);
    return tally(0) + tally(3);
}
`, (20+'a')+(10+'d'))
}
