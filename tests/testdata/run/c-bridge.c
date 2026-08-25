// C, compiled to wasm32 by clang and translated into METALS.
//
// This case is the reason linear memory exists in the IR. Nothing here
// was written for Gyro: it is ordinary freestanding C, and every part of
// it that a typed IR cannot express — pointer arithmetic over a static
// array, a struct read through a pointer, a function pointer called
// through a table, a string in .rodata — arrives as offsets into one
// flat region and lands on the same answers the evaluator and the C
// backend both produce.
//
// The boundary is declared in the C source rather than mapped by the
// bridge: import_module names the METALS module and import_name names
// the function in it, so what crosses is what this file says crosses.


__attribute__((import_module("gyro:rt"), import_name("println<i64>")))
extern void put(long long v);

__attribute__((import_module("gyro:rt"), import_name("println<f64>")))
extern void putf(double v);

typedef struct {
    int x;
    int y;
} Point;

static int squares[64];
static const char text[] = "hello";
static long long counter;

static int sum_points(const Point *p, int n) {
    int total = 0;
    for (int i = 0; i < n; i++) {
        total += p[i].x * p[i].y;
    }
    return total;
}

static int add(int a, int b) { return a + b; }
static int mul(int a, int b) { return a * b; }

// A call through a pointer becomes call_indirect, which the bridge turns
// into a switch over the element section — so the table is resolved at
// translation time and an index it never held aborts.
static int apply(int (*f)(int, int), int a, int b) { return f(a, b); }

static unsigned udiv(unsigned a, unsigned b) { return a / b; }
static int sdiv(int a, int b) { return a / b; }

static long long collatz(long long n) {
    long long steps = 0;
    while (n != 1) {
        n = (n & 1) ? 3 * n + 1 : n / 2;
        steps++;
    }
    return steps;
}

int main(void) {
    // A loop over module storage: every element is a memory access.
    for (int i = 0; i < 64; i++) {
        squares[i] = i * i;
    }
    long long total = 0;
    for (int i = 0; i < 64; i++) {
        total += squares[i];
    }
    put(total);

    // Structs by value on the stack, read back through a pointer.
    Point ps[3] = {{1, 2}, {3, 4}, {5, 6}};
    put(sum_points(ps, 3));

    // Indirect calls.
    put(apply(add, 20, 22));
    put(apply(mul, 6, 7));

    // Signedness is the operation's in wasm and the type's in METALS.
    put(udiv(4000000000u, 7u));
    put(sdiv(-100, 7));
    put((int)-1 >> 1);
    put((int)((unsigned)-1 >> 28));
    put(-7 % 3);

    // Static read-only data, addressed as bytes.
    put(text[0]);
    put(text[4]);

    // A dense switch is a br_table.
    long long acc = 0;
    for (int i = 0; i < 6; i++) {
        switch (i) {
        case 0: acc += 1; break;
        case 1: acc += 10; break;
        case 2: acc += 100; break;
        case 3: acc += 1000; break;
        default: acc += 10000; break;
        }
    }
    put(acc);

    // Module storage that outlives a call.
    for (int i = 0; i < 10; i++) {
        counter += i;
    }
    put(counter);

    // 64-bit arithmetic and a loop whose trip count is not known.
    put(collatz(27));

    // The bit-counting instructions, which METALS has no opcode for and
    // the bridge synthesizes as ordinary loops.
    put(__builtin_clz(1u));
    put(__builtin_ctz(0x8000u));
    put(__builtin_popcountll(0xF0F0F0F0F0F0F0F0ull));

    // A bulk copy inside the memory.
    int copy[8];
    __builtin_memcpy(copy, squares + 4, sizeof copy);
    put(copy[0] + copy[7]);

    // Floats, including a conversion clang emits in its saturating form.
    double d = 1.5;
    for (int i = 0; i < 4; i++) {
        d = d * 2.0 + 0.25;
    }
    putf(d);
    put((long long)(d * 100.0));
    put((long long)(1.0 / 0.0 > 0.0 ? 1 : 0));

    return 0;
}
