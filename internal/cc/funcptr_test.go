package cc

import (
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Function pointers, and the declarator syntax that exists because of
// them. C reads a declarator as the expression that would use the thing
// it declares, so `int (*fp)(int)` says `(*fp)(int)` is an int — and
// every case below is a shape that cannot be parsed without reading it
// that way.

func TestCallThroughAPointer(t *testing.T) {
	answers(t, `
int twice(int x) { return x * 2; }

int result(void) {
    int (*fp)(int) = twice;
    return fp(21);
}
`, 42)
}

// `(*fp)(x)` is the older spelling, and C defines it to be the same
// call: the dereference gives a function designator, which decays right
// back to the pointer it came from.
func TestDereferenceSpellingIsTheSameCall(t *testing.T) {
	answers(t, `
int twice(int x) { return x * 2; }

int result(void) {
    int (*fp)(int) = &twice;
    return (*fp)(20) + (**fp)(1);
}
`, 42)
}

func TestFunctionPointerIsOneWord(t *testing.T) {
	answers(t, `
int f(int a, int b) { return a + b; }

int result(void) {
    int (*fp)(int, int) = f;
    if (sizeof(fp) != 8) {
        return 0;
    }
    if (sizeof(int (*)(int, int)) != 8) {
        return 0;
    }
    return 42;
}
`, 42)
}

// A struct of callbacks is the shape every driven C library has, and it
// is only C's struct if a callback occupies one word.
func TestStructOfCallbacksHasCLayout(t *testing.T) {
	answers(t, `
int add(int a, int b) { return a + b; }
int mul(int a, int b) { return a * b; }

struct ops {
    int (*first)(int, int);
    int (*second)(int, int);
    int tag;
};

int result(void) {
    struct ops o;
    if (sizeof(struct ops) != 24) {
        return 0;
    }
    o.first = add;
    o.second = mul;
    o.tag = 1;
    return o.first(o.second(4, 10), 2);
}
`, 42)
}

// An array of function pointers is a dispatch table, and `void (*t[3])()`
// is the declarator that says so: the `[3]` binds tighter than the `*`
// inside the parentheses.
func TestArrayOfFunctionPointers(t *testing.T) {
	answers(t, `
int add(int a, int b) { return a + b; }
int sub(int a, int b) { return a - b; }
int mul(int a, int b) { return a * b; }

int result(void) {
    int (*table[3])(int, int);
    table[0] = add;
    table[1] = sub;
    table[2] = mul;
    return table[2](table[0](3, 4), table[1](9, 3));
}
`, 42)
}

// A function returning a function pointer, in both spellings: the raw
// declarator and the typedef that exists to avoid reading it.
func TestFunctionReturningAFunctionPointer(t *testing.T) {
	answers(t, `
typedef int (*binary)(int, int);

int add(int a, int b) { return a + b; }
int mul(int a, int b) { return a * b; }

int (*raw(int which))(int, int) {
    if (which) { return mul; }
    return add;
}

binary named(int which) {
    if (which) { return mul; }
    return add;
}

int result(void) {
    return raw(1)(6, 7) + named(0)(0, 0);
}
`, 42)
}

// A function parameter is a pointer to a function, exactly as an array
// parameter is a pointer to an element. C says so, and this is the
// spelling that only makes sense if it does.
func TestFunctionParameterDecaysToAPointer(t *testing.T) {
	answers(t, `
int add(int a, int b) { return a + b; }

int apply(int f(int, int), int a, int b) { return f(a, b); }

int result(void) {
    return apply(add, 40, 2);
}
`, 42)
}

// Two names for one function are one address, and comparing them is
// what a C program does to find out which callback it holds.
func TestFunctionPointersCompareByAddress(t *testing.T) {
	answers(t, `
int add(int a, int b) { return a + b; }
int mul(int a, int b) { return a * b; }

int result(void) {
    int (*p)(int, int) = add;
    int (*q)(int, int) = &add;
    if (p != q) { return 0; }
    if (p == mul) { return 0; }
    if (!p) { return 0; }
    p = 0;
    if (p) { return 0; }
    if (p != 0) { return 0; }
    return 42;
}
`, 42)
}

// A file-scope function pointer holds an address, and an address is not
// a byte image: it is not known until the program is laid out. So the
// global carries no image and the module's initializer stores it.
func TestFileScopeCallbackIsWrittenByTheInitializer(t *testing.T) {
	module := compiled(t, `
int add(int a, int b) { return a + b; }

static int (*chosen)(int, int) = add;

int result(void) { return chosen(40, 2); }
`)
	if module.Init == "" {
		t.Fatal("the module names no init, so nothing stores the address")
	}
	for _, global := range module.Globals {
		if global.Name == "chosen" && global.Bytes != nil {
			t.Errorf("@chosen carries a byte image, which cannot hold an address")
		}
	}
	answers(t, `
int add(int a, int b) { return a + b; }

static int (*chosen)(int, int) = add;

int result(void) { return chosen(40, 2); }
`, 42)
}

// The IR type is the one-word one. A `fn` would be two words and would
// also carry an environment, which C has nowhere to put.
func TestPointerToFunctionLowersToACodePointer(t *testing.T) {
	module := compiled(t, `
int add(int a, int b) { return a + b; }
static int (*chosen)(int, int) = add;
int result(void) { return chosen(40, 2); }
`)
	var found metals.Type
	for _, global := range module.Globals {
		if global.Name == "chosen" {
			found = global.Type
		}
	}
	opt, ok := found.(*metals.Opt)
	if !ok {
		t.Fatalf("@chosen is %s, and a C pointer is nullable", found)
	}
	if _, ok := opt.Elem.(*metals.CodeType); !ok {
		t.Fatalf("@chosen holds %s, want a code pointer", opt.Elem)
	}
	if size := metals.NewLayout(module, func(string, ...any) {}).Size(found); size != 8 {
		t.Errorf("sizeof(@chosen) = %d, want 8", size)
	}
}

func TestRefusesWhatAFunctionIsNot(t *testing.T) {
	refused(t, `int f(void) { return 0; } int result(void) { f = 0; return 0; }`,
		"a function is not storage")
	refused(t, `int result(void) { int n = 1; return n(); }`,
		"is not something that can be called")
	refused(t, `struct S { int f(int); }; int result(void) { return 0; }`,
		"a pointer to one, not the function itself")
}
