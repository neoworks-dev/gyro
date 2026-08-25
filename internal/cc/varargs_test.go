package cc

import (
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Varargs.
//
// METALS has no variadic call. The caller spills each argument after the
// last named one into a cell of its own and passes an array of erased
// addresses plus a count; `va_arg` walks it. So most of these cases are
// about the walk — what a callee reads, in what order, and what happens
// when it asks for one too many.

func TestVarargsSum(t *testing.T) {
	answers(t, `
#include <stdarg.h>

static int total(int n, ...) {
    va_list ap;
    int sum = 0;
    int i;

    va_start(ap, n);
    for (i = 0; i < n; i = i + 1) {
        sum = sum + va_arg(ap, int);
    }
    va_end(ap);
    return sum;
}

int result(void) { return total(3, 20, 12, 10); }
`, 42)
}

// One array of erased addresses carries all three, which is the reason
// for the indirection: METALS will not let a pointer and a number share
// a slot, and addresses are all one type.
func TestVarargsMixedTypes(t *testing.T) {
	answers(t, `
#include <stdarg.h>

static int read(const char *spec, ...) {
    va_list ap;
    int total = 0;
    const char *p;

    va_start(ap, spec);
    for (p = spec; *p != 0; p = p + 1) {
        if (*p == 'i') {
            total = total + va_arg(ap, int);
        } else if (*p == 'd') {
            total = total + (int)va_arg(ap, double);
        } else if (*p == 'p') {
            total = total + *va_arg(ap, int *);
        }
    }
    va_end(ap);
    return total;
}

int result(void) {
    int boxed = 12;
    return read("idp", 20, 10.5, &boxed);
}
`, 42)
}

// The vprintf pattern. A va_list is an array of one, so naming it decays
// to a pointer and the callee advances the cursor its caller declared.
func TestVarargsForwardAVaList(t *testing.T) {
	answers(t, `
#include <stdarg.h>

static int add_two(va_list ap) {
    int a = va_arg(ap, int);
    int b = va_arg(ap, int);
    return a + b;
}

static int outer(int base, ...) {
    va_list ap;
    int n;

    va_start(ap, base);
    n = add_two(ap);
    va_end(ap);
    return base + n;
}

int result(void) { return outer(0, 20, 22); }
`, 42)
}

func TestVarargsVaCopyWalksTwice(t *testing.T) {
	answers(t, `
#include <stdarg.h>

static int twice(int n, ...) {
    va_list ap;
    va_list again;
    int a;
    int b;

    va_start(ap, n);
    va_copy(again, ap);
    a = va_arg(ap, int);
    b = va_arg(again, int);
    va_end(again);
    va_end(ap);
    return a + b;
}

int result(void) { return twice(1, 21); }
`, 42)
}

// C's default argument promotions: everything narrower than int becomes
// one, and a float becomes a double. The callee is written against the
// result, which is why `va_arg(ap, char)` is undefined in C too.
func TestVarargsDefaultPromotions(t *testing.T) {
	answers(t, `
#include <stdarg.h>

static int take(int n, ...) {
    va_list ap;
    int whole;
    double fractional;

    va_start(ap, n);
    whole = va_arg(ap, int);
    fractional = va_arg(ap, double);
    va_end(ap);
    return whole + (int)fractional;
}

int result(void) {
    char c = 40;
    float f = 2.5f;
    return take(2, c, f);
}
`, 42)
}

// The count is per call. The same function is handed nothing, and then
// four things.
func TestVarargsCountIsPerCall(t *testing.T) {
	answers(t, `
#include <stdarg.h>

static int total(int n, ...) {
    va_list ap;
    int sum = 0;
    int i;

    va_start(ap, n);
    for (i = 0; i < n; i = i + 1) {
        sum = sum + va_arg(ap, int);
    }
    va_end(ap);
    return sum;
}

int result(void) { return total(0) + total(4, 10, 11, 10, 11); }
`, 42)
}

// A pointer to a variadic function is a code pointer like any other. The
// two hidden parameters are part of the signature, so the type says what
// a call through it has to build.
func TestVarargsThroughAFunctionPointer(t *testing.T) {
	answers(t, `
#include <stdarg.h>

static int with(int base, ...) {
    va_list ap;
    int n;

    va_start(ap, base);
    n = va_arg(ap, int);
    va_end(ap);
    return base + n;
}

int result(void) {
    int (*fp)(int, ...) = with;
    return fp(20, 22);
}
`, 42)
}

// Where varargs and `void *` meet. The pack is already an array of
// erased addresses, so an erased address travels in one like anything
// else.
func TestVarargsCarriesAVoidPointer(t *testing.T) {
	answers(t, `
#include <stdarg.h>

static int read(int n, ...) {
    va_list ap;
    void *erased;
    int *back;

    va_start(ap, n);
    erased = va_arg(ap, void *);
    va_end(ap);
    back = erased;
    return *back;
}

int result(void) {
    int boxed = 42;
    return read(1, (void *)&boxed);
}
`, 42)
}

// The hidden parameters are part of the signature, and their names are
// not in C's identifier space, so nothing a translation unit declares can
// collide with one.
func TestVariadicSignatureCarriesTheHiddenParameters(t *testing.T) {
	module, err := Compile("case.c", `
#include <stdarg.h>
int tally(int n, ...) { return n; }
int plain(int n) { return n; }
`, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var tally, plain *metals.Func
	for _, fn := range module.Funcs {
		switch fn.Name {
		case "tally":
			tally = fn
		case "plain":
			plain = fn
		}
	}
	if tally == nil || plain == nil {
		t.Fatalf("the module declares %v", funcNames(module.Funcs))
	}
	if len(plain.Params) != 1 {
		t.Errorf("plain is %s, and it declared one parameter", plain.Sig())
	}
	if len(tally.Params) != 3 {
		t.Fatalf("tally is %s, want its one parameter plus the two a `...` carries", tally.Sig())
	}
	for i, want := range []string{"opt<ptr<opt<anyptr>>>", "i64"} {
		if got := tally.Params[i+1].Type.String(); got != want {
			t.Errorf("tally's hidden parameter %d is %s, want %s", i, got, want)
		}
	}
	for _, param := range tally.Params[1:] {
		if !strings.Contains(param.Name, "$") {
			t.Errorf("%s is a name a C program could collide with", param.Name)
		}
	}
}

// A variadic export is left out of the generated Gyro header rather than
// making the whole module unimportable: a `...` says nothing about what
// a call passes, so nothing on the far side of a boundary could check
// one. That is the loader's rule; here it is enough that the module
// verifies with the export in it.
func TestVariadicExportVerifies(t *testing.T) {
	module, err := Compile("case.c", `
#include <stdarg.h>
int tally(int n, ...) { return n; }
`, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatalf("the module does not verify: %v\n%s", err, metals.Print(module))
	}
}

// Two declarations of one name have to agree, and a `...` is part of the
// agreement. C's linker compares nothing and would take this.
func TestRefusesDisagreeingAboutVariadic(t *testing.T) {
	files := map[string]string{
		"case.c":  `int tally(int n); int result(void) { return tally(1); }`,
		"other.c": "#include <stdarg.h>\nint tally(int n, ...) { return n; }",
	}
	refusedOpt(t, program(files), files["case.c"], "two different signatures")
}

func TestRefusesWhatVarargsCannotDo(t *testing.T) {
	// C requires a named parameter before the `...`, and va_start is why.
	refused(t, `int f(...); int result(void) { return 0; }`,
		"needs a named parameter before it")

	// A variadic call still checks the arguments it does declare.
	refused(t, `
int f(int a, int b, ...);
int result(void) { return f(1); }
`, "takes at least 2 argument(s)")

	// va_start in a function that has no `...` has nothing to start from.
	refused(t, `
#include <stdarg.h>
int result(void) { va_list ap; va_start(ap, result); return 0; }
`, "only meaningful in a function declared with `...`")

	// A narrow type is never what was passed, because the promotions ran
	// at the call. C calls this undefined; here it is refused where it is
	// written.
	refused(t, `
#include <stdarg.h>
static int f(int n, ...) { va_list ap; va_start(ap, n); return va_arg(ap, char); }
int result(void) { return f(1, 2); }
`, "so va_arg reads int where you wrote char")
	refused(t, `
#include <stdarg.h>
static double f(int n, ...) { va_list ap; va_start(ap, n); return va_arg(ap, float); }
int result(void) { return (int)f(1, 2.0); }
`, "so va_arg reads double where you wrote float")

	// The builtins want a va_list and say so.
	refused(t, `
#include <stdarg.h>
static int f(int n, ...) { int wrong; va_start(wrong, n); return 0; }
int result(void) { return f(1); }
`, "va_start wants a va_list")

	// <stdarg.h>'s struct is the compiler's, and this front end reads its
	// shape rather than assuming it.
	refused(t, `
struct __va_list_tag { int wrong; };
typedef struct __va_list_tag va_list[1];
static int f(int n, ...) { va_list ap; __builtin_va_start(ap); return 0; }
int result(void) { return f(1); }
`, "and this program declares it as something else")
}

// <stdarg.h> is the compiler's own, so it needs no include path. A
// quoted include is still the program's file, because taking that over
// would be the compiler outvoting the source.
func TestBundledHeaderIsAngledOnly(t *testing.T) {
	answers(t, `
#include <stdarg.h>
int result(void) { return (int)sizeof(va_list) - 8; }
`, 8)

	refused(t, "#include \"stdarg.h\"\nint result(void) { return 0; }",
		`cannot find "stdarg.h"`)
}
