package cc

import (
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// `void *` and the casts that go with it.
//
// A void pointer is an address with the target's type erased, and in
// METALS that is `anyptr`: one word, comparable, and with nothing to
// load through it until a cast puts a type back. The cases below are
// mostly about the second half — C already forbids everything the
// erasure takes away, so the interesting question is what it takes to
// get a type back on, and what a mistake looks like when it does not.

func TestVoidPointerRoundTrip(t *testing.T) {
	answers(t, `
int result(void) {
    int n = 42;
    void *erased = &n;
    int *back = erased;
    return *back;
}
`, 42)
}

// A cast to `void *` is written and a cast back is not: C converts a
// void pointer to an object pointer with nothing written, which is the
// one asymmetry it keeps against C++.
func TestConversionsBothWays(t *testing.T) {
	answers(t, `
struct point { int x; int y; };

int result(void) {
    struct point p;
    void *erased;
    struct point *back;

    p.x = 40;
    p.y = 2;
    erased = (void *)&p;
    back = erased;
    return back->x + back->y;
}
`, 42)
}

// The userdata pattern, which is what the type exists for: a callback is
// handed an address whose type nothing between the two ends knows.
func TestVoidPointerThroughACallback(t *testing.T) {
	answers(t, `
typedef int (*visitor)(void *data);

static int read_int(void *data) { return *(int *)data; }

static int apply(visitor fn, void *data) { return fn(data); }

int result(void) {
    int n = 42;
    return apply(read_int, &n);
}
`, 42)
}

// Writing through a restored pointer reaches the object itself, which is
// the difference between a pointer and a copy.
func TestWritesThroughARestoredPointer(t *testing.T) {
	answers(t, `
int result(void) {
    int n = 0;
    void *erased = &n;
    int *back = erased;
    *back = 42;
    return n;
}
`, 42)
}

// Erasing changes what may be done with an address and nothing about the
// address, so identity survives it in both directions.
func TestErasurePreservesIdentity(t *testing.T) {
	answers(t, `
int result(void) {
    int a = 1;
    int b = 2;
    void *pa = &a;
    void *pa2 = &a;
    void *pb = &b;

    if (pa != pa2) {
        return 0;
    }
    if (pa == pb) {
        return 0;
    }
    return 42;
}
`, 42)
}

// C compares a `void *` against a pointer to an object without a cast,
// and the object pointer is the one that converts.
func TestComparesAgainstATypedPointer(t *testing.T) {
	answers(t, `
int result(void) {
    int n = 1;
    int *typed = &n;
    void *erased = &n;

    if (erased != typed) {
        return 0;
    }
    if (typed != erased) {
        return 0;
    }
    return 42;
}
`, 42)
}

// A null void pointer is a null, and `(void *)0` is still the null
// pointer constant it was before the type had a spelling.
func TestNullVoidPointer(t *testing.T) {
	answers(t, `
int result(void) {
    void *none = 0;
    void *also = (void *)0;
    int n = 1;
    void *some = &n;

    if (none != 0) {
        return 0;
    }
    if (also != none) {
        return 0;
    }
    if (some == 0) {
        return 0;
    }
    if (none) {
        return 0;
    }
    return 42;
}
`, 42)
}

// Erasing a type does not change what a pointer costs, which is the
// whole reason `anyptr` is a word and not a box.
func TestVoidPointerIsOneWord(t *testing.T) {
	answers(t, `
struct handle {
    int kind;
    void *data;
};

int result(void) {
    if (sizeof(void *) != 8) {
        return 0;
    }
    if (sizeof(struct handle) != 16) {
        return 0;
    }
    return 42;
}
`, 42)
}

// An array of erased pointers is C's `void *[]`: one array, targets of
// several types, each restored to what it was.
func TestArrayOfVoidPointers(t *testing.T) {
	answers(t, `
struct point { int x; int y; };

int result(void) {
    int n = 20;
    struct point p;
    void *slots[3];
    int *first;
    struct point *second;

    p.x = 12;
    p.y = 10;
    slots[0] = &n;
    slots[1] = &p;
    slots[2] = 0;

    if (slots[2] != 0) {
        return 0;
    }
    first = slots[0];
    second = slots[1];
    return *first + second->x + second->y;
}
`, 42)
}

func TestVoidPointerLowersToAnAnyPtr(t *testing.T) {
	module, err := Compile("case.c", `
void *identity(void *p) { return p; }
int result(void) { return 42; }
`, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var fn *metals.Func
	for _, candidate := range module.Funcs {
		if candidate.Name == "identity" {
			fn = candidate
		}
	}
	if fn == nil {
		t.Fatalf("the module declares %v, none of them identity", funcNames(module.Funcs))
	}
	want := &metals.Opt{Elem: &metals.AnyPtr{}}
	if len(fn.Params) != 1 || !metals.TypesEqual(fn.Params[0].Type, want) {
		t.Errorf("identity is %s, want its parameter to be %s", fn.Sig(), want)
	}
	if !metals.TypesEqual(fn.Result, want) {
		t.Errorf("identity returns %s, want %s", fn.Result, want)
	}
}

func funcNames(funcs []*metals.Func) []string {
	out := make([]string, len(funcs))
	for i, fn := range funcs {
		out[i] = fn.Name
	}
	return out
}

// What C already refuses, refused here for C's own reason: a `void *`
// says nothing about what is at the address, so every operation that
// needs the target's size or type has no answer.
func TestRefusesWhatAVoidPointerCannotDo(t *testing.T) {
	refused(t, `int result(void) { int n = 1; void *p = &n; return *p; }`,
		"cannot dereference a void *")
	refused(t, `int result(void) { int n = 1; void *p = &n; return *(p + 1); }`,
		"no element size to step by")
	refused(t, `int result(void) { int n = 1; void *p = &n; return p[0]; }`,
		"no element size to step by")
	refused(t, `int result(void) { int n = 1; void *p = &n; p++; return 0; }`,
		"no element size to step by")
	refused(t, `int result(void) { int n = 1; void *a = &n; void *b = &n; return (int)(a - b); }`,
		"no difference in elements")
	refused(t, `int result(void) { int n = 1; void *a = &n; void *b = &n; return a < b; }`,
		"count elements neither of them has")
	refused(t, `int result(void) { void *p = 0; return (int)sizeof(*p); }`,
		"cannot dereference a void *")
	refused(t, `struct s { int x; }; int result(void) { void *p = 0; return ((struct s *)p)->x + (int)p; }`,
		"cannot convert a void * to a int")
}

// A pointer to a function and a pointer to an object are two types here,
// and converting between them is a guarantee about a machine rather than
// about a program. METALS keeps them apart precisely so that a producer
// cannot quietly assume otherwise.
func TestRefusesFunctionPointerLaundering(t *testing.T) {
	refused(t, `
int f(int x) { return x; }
int result(void) { void *p = (void *)f; return 0; }
`, "different types here")
	refused(t, `
int result(void) { void *p = 0; int (*fp)(int) = p; return 0; }
`, "different types here")
}
