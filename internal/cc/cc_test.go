package cc

import (
	"io"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/meval"
)

// What a C program computes is checked by running it, not by reading the
// METALS it became: the evaluator is the tier closest to what the
// lowering emitted, so a wrong answer here is a lowering bug and not a
// backend's. Each case defines `int result(void)` and the harness
// synthesizes the @main that calls it, because a C main is the program's
// entry and these are not programs.

func compiled(t *testing.T, source string) *metals.Module {
	t.Helper()
	module, err := Compile("case.c", source, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatalf("the module does not verify: %v\n%s", err, metals.Print(module))
	}
	return module
}

// answers runs `result()` and fails unless it produced want. The
// comparison is inside the module, so a mismatch arrives as the abort
// the evaluator raises rather than as a value the harness has to reach
// in and read.
func answers(t *testing.T, source string, want int64) {
	t.Helper()
	answersOpt(t, nil, source, want)
}

func answersOpt(t *testing.T, opt *Options, source string, want int64) {
	t.Helper()
	module, err := Compile("case.c", source, opt)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatalf("the module does not verify: %v\n%s", err, metals.Print(module))
	}

	var result *metals.Func
	for _, fn := range module.Funcs {
		if fn.Name == "result" {
			result = fn
		}
	}
	if result == nil {
		t.Fatal("the case defines no result()")
	}
	if len(result.Params) != 0 || result.Result == nil {
		t.Fatalf("result() is %s, and the harness calls int result(void)", result.Sig())
	}

	module.Funcs = append(module.Funcs, &metals.Func{
		Name:   "main",
		Export: true,
		Body: []metals.Instr{
			&metals.Call{Dst: "got", Callee: "result"},
			&metals.Const{Dst: "want", Type: result.Result, Value: itoa(want)},
			&metals.Bin{Dst: "ok", Op: "eq", A: "got", B: "want"},
			&metals.Un{Dst: "bad", Op: "not", X: "ok"},
			&metals.If{
				Cond: "bad",
				Then: []metals.Instr{&metals.Abort{Message: "result() did not answer " + itoa(want)}},
			},
			&metals.Return{},
		},
	})
	if err := metals.Verify(module); err != nil {
		t.Fatalf("the harness module does not verify: %v\n%s", err, metals.Print(module))
	}

	// Linking is what turns the module's init into the call at the top of
	// main, so a case with file-scope initializers runs them.
	module, err = metals.Link([]*metals.Module{module})
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	if err := meval.Run(module, nil, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatalf("%v\n%s", err, metals.Print(module))
	}
}

func itoa(v int64) string {
	out := ""
	negative := v < 0
	if negative {
		v = -v
	}
	for {
		out = string(rune('0'+v%10)) + out
		v /= 10
		if v == 0 {
			break
		}
	}
	if negative {
		return "-" + out
	}
	return out
}

func refused(t *testing.T, source, fragment string) {
	t.Helper()
	refusedOpt(t, nil, source, fragment)
}

func refusedOpt(t *testing.T, opt *Options, source, fragment string) {
	t.Helper()
	module, err := Compile("case.c", source, opt)
	if err == nil {
		err = metals.Verify(module)
		if err == nil {
			t.Fatalf("expected %q, the program compiled:\n%s", fragment, metals.Print(module))
		}
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("expected an error containing %q, got %v", fragment, err)
	}
}

// ---------------------------------------------------------------------------
// What it computes
// ---------------------------------------------------------------------------

func TestArithmetic(t *testing.T) {
	answers(t, `int result(void) { return 6 * 7 - 2 + 8 / 4; }`, 42)
	answers(t, `int result(void) { return 17 % 5; }`, 2)
	answers(t, `int result(void) { return (1 << 5) | 9 | 1; }`, 41)
	answers(t, `int result(void) { return ~0 & 0xFF; }`, 255)
	answers(t, `int result(void) { return -7 / 2; }`, -3)
}

// C computes in int, so a char sum that overflows a char does not wrap
// at eight bits — it wraps at thirty-two, and only an assignment back
// into a char narrows it.
func TestIntegerPromotion(t *testing.T) {
	answers(t, `
int result(void) {
    char a = 100;
    char b = 100;
    return a + b;
}
`, 200)

	answers(t, `
int result(void) {
    char a = 100;
    char b = 100;
    char sum = a + b;
    return sum;
}
`, -56)
}

func TestUnsignedWraps(t *testing.T) {
	answers(t, `
int result(void) {
    unsigned int u = 0;
    u = u - 1;
    return (int)(u >> 28);
}
`, 15)

	// An unsigned operand pulls the signed one up to unsigned, which is
	// the conversion that makes this comparison false in C.
	answers(t, `
int result(void) {
    unsigned int u = 1;
    int i = -1;
    return i < u;
}
`, 0)
}

func TestShiftIsArithmeticOnSigned(t *testing.T) {
	answers(t, `int result(void) { return -16 >> 2; }`, -4)
	answers(t, `int result(void) { unsigned int u = 0xF0000000u; return (int)(u >> 28); }`, 15)
}

func TestControlFlow(t *testing.T) {
	answers(t, `
int result(void) {
    int total = 0;
    for (int i = 1; i <= 10; i++) {
        if (i % 3 == 0) { continue; }
        if (i == 9) { break; }
        total = total + i;
    }
    return total;
}
`, 37)

	answers(t, `
int result(void) {
    int n = 10;
    int fib = 0;
    int next = 1;
    while (n > 0) {
        int tmp = fib + next;
        fib = next;
        next = tmp;
        n--;
    }
    return fib;
}
`, 55)

	answers(t, `
int result(void) {
    int n = 0;
    do { n = n + 3; } while (n < 10);
    return n;
}
`, 12)
}

func TestShortCircuit(t *testing.T) {
	// The second operand must not run: dividing by zero aborts, so a
	// program that answers at all is a program that short-circuited.
	answers(t, `
int result(void) {
    int zero = 0;
    if (zero != 0 && 1 / zero) { return 1; }
    if (zero == 0 || 1 / zero) { return 2; }
    return 3;
}
`, 2)
}

func TestConditionalOperator(t *testing.T) {
	answers(t, `
int result(void) {
    int a = 3;
    return a > 2 ? a * 10 : a;
}
`, 30)
}

func TestRecursion(t *testing.T) {
	answers(t, `
static int fact(int n) { return n <= 1 ? 1 : n * fact(n - 1); }
int result(void) { return fact(7); }
`, 5040)
}

func TestFloats(t *testing.T) {
	answers(t, `
int result(void) {
    double x = 2.5;
    double y = 4.0;
    return (int)(x * y);
}
`, 10)

	// float is f32 and has to round back through single precision, or
	// the arithmetic below is silently done in double.
	answers(t, `
int result(void) {
    float x = 16777216.0;
    float y = x + 1.0;
    return x == y;
}
`, 1)
}

func TestStructs(t *testing.T) {
	answers(t, `
struct Point { int x; int y; };

static int area(struct Point p) { return p.x * p.y; }

int result(void) {
    struct Point p;
    p.x = 6;
    p.y = 7;
    return area(p);
}
`, 42)

	// A struct passed by value is copied, which is what METALS' record
	// already is; the callee's write must not reach the caller's.
	answers(t, `
struct Point { int x; int y; };

static int bump(struct Point p) { p.x = 100; return p.x; }

int result(void) {
    struct Point p;
    p.x = 1;
    p.y = 2;
    int inner = bump(p);
    return inner + p.x;
}
`, 101)
}

func TestPointers(t *testing.T) {
	answers(t, `
int result(void) {
    int n = 20;
    int *p = &n;
    *p = *p + 22;
    return n;
}
`, 42)

	answers(t, `
struct Node { int value; struct Node *next; };

int result(void) {
    struct Node tail;
    tail.value = 30;
    tail.next = 0;

    struct Node head;
    head.value = 12;
    head.next = &tail;

    int total = 0;
    struct Node *walk = &head;
    while (walk != 0) {
        total = total + walk->value;
        walk = walk->next;
    }
    return total;
}
`, 42)
}

// A null pointer in C is undefined to dereference and nothing catches
// it. Compiled into METALS it is an opt<ptr<T>>, so the unwrap aborts
// and says so — which is the difference between compiling a language in
// and linking it.
func TestNullDereferenceAborts(t *testing.T) {
	module := compiled(t, `
int deref(int *p) { return *p; }
int result(void) { return deref(0); }
`)
	module.Funcs = append(module.Funcs, &metals.Func{
		Name:   "main",
		Export: true,
		Body: []metals.Instr{
			&metals.Call{Dst: "r", Callee: "result"},
			&metals.Return{},
		},
	})
	err := meval.Run(module, nil, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("dereferencing a null pointer answered instead of aborting")
	}
	if !strings.Contains(err.Error(), "null") {
		t.Fatalf("expected an abort naming the null, got %v", err)
	}
}

func TestGlobals(t *testing.T) {
	answers(t, `
static int counter;

static void bump(int by) { counter = counter + by; }

int result(void) {
    counter = 0;
    bump(20);
    bump(22);
    return counter;
}
`, 42)
}

// ---------------------------------------------------------------------------
// What it refuses, and says why
// ---------------------------------------------------------------------------

func TestRefusesWhatIsNotLoweredYet(t *testing.T) {
	refused(t, `int result(void) { int a[4], b[4]; a = b; return 0; }`, "an array is storage")
	refused(t, `int result(void) { int a[]; return 0; }`, "has no size")
	refused(t, "#include <stdio.h>\nint result(void) { return 0; }", "cannot find <stdio.h>")
	refused(t, `int sum(int a, ...); int result(void) { return 0; }`, "variadic")
	refused(t, `int result() { return 0; }`, "write (void)")
	refused(t, `long double result(void) { return 0; }`, "not a type")
}

func TestRefusesBadPrograms(t *testing.T) {
	refused(t, `int result(void) { return missing; }`, "missing is not declared")
	refused(t, `int result(void) { return nope(); }`, "nope is not declared")
	refused(t, `void result(void) { return 1; }`, "returns void")
	refused(t, `int result(void) { return; }`, "carries nothing")
	refused(t, `struct P { int x; }; int result(void) { struct P p; return p.y; }`, "no field y")
	refused(t, `int result(void) { break; }`, "break is not inside a loop")
	refused(t, `int f(int a); int result(void) { return f(1, 2); }`, "takes 1 argument")
	refused(t, `struct P; int result(void) { struct P p; return 0; }`, "never defined")
}

func TestModuleNameCarriesTheScheme(t *testing.T) {
	module := compiled(t, `int result(void) { return 0; }`)
	if got := module.Name; got != "cc:case" {
		t.Errorf("module is %q, want cc:case", got)
	}
	if metals.Scheme(module.Name) != "cc" {
		t.Errorf("%q does not carry the cc scheme", module.Name)
	}
	// A pointer between a C module and a Gyro one is not a boundary
	// crossing; that is the rule this front end was waiting on.
	if !metals.SameRuntime(module.Name, "gyro:main") {
		t.Error("a cc module and a gyro module are not one runtime")
	}
}

// An exported C function is one without static, which is C's own rule
// for what leaves a translation unit.
func TestStaticIsNotExported(t *testing.T) {
	module := compiled(t, `
static int hidden(void) { return 1; }
int shown(void) { return hidden(); }
`)
	for _, fn := range module.Funcs {
		if fn.Name == "hidden" && fn.Export {
			t.Error("a static function was exported")
		}
		if fn.Name == "shown" && !fn.Export {
			t.Error("a non-static function was not exported")
		}
	}
}
