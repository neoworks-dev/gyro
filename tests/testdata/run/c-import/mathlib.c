// C compiled into METALS directly, and imported by the Gyro program
// next to it.
//
// Nothing here is written for Gyro and nothing is marshalled. An `int`
// is an i32 because that is what an int is, a `long` is an i64, and an
// `int *` is a pointer into the same heap the caller allocated from —
// which is why divmod below can write its answers through one. That is
// the difference between this and @extern: a C library reached through
// the FFI cannot take an out-parameter at all.
//
// It is also the difference between this and internal/wasm. The same
// file through the bridge arrives with every one of these signatures
// erased to i32 and every struct flattened into a byte region, which
// runs correctly and cannot be imported as anything.

int add(int a, int b) { return a + b; }

int clamp(int v, int lo, int hi) {
    if (v < lo) { return lo; }
    if (v > hi) { return hi; }
    return v;
}

long fib(int n) {
    long a = 0;
    long b = 1;
    for (int i = 0; i < n; i++) {
        long next = a + b;
        a = b;
        b = next;
    }
    return a;
}

int gcd(int a, int b) {
    while (b != 0) {
        int t = a % b;
        a = b;
        b = t;
    }
    return a;
}

// A struct the module keeps to itself: Gyro never sees this type, and a
// pointer to it is an ordinary ptr<%Stats> the whole time.
struct Stats {
    int count;
    long total;
};

static void observe(struct Stats *s, int value) {
    s->count = s->count + 1;
    s->total = s->total + value;
}

long mean_of_range(int lo, int hi) {
    struct Stats s;
    s.count = 0;
    s.total = 0;
    for (int v = lo; v <= hi; v++) {
        observe(&s, v);
    }
    if (s.count == 0) { return 0; }
    return s.total / s.count;
}

// Two out-parameters, written through pointers the caller owns.
void divmod(int total, int by, int *quotient, int *remainder) {
    *quotient = total / by;
    *remainder = total % by;
}

double hypot_squared(double x, double y) { return x * x + y * y; }

// C computes in int and narrows on the way back into a char, so this
// wraps at eight bits and not at thirty-two.
int char_wraps(void) {
    char a = 100;
    char b = 100;
    char sum = a + b;
    return sum;
}
