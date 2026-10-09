#include "table.h"

int add(int a, int b) { return a + b; }
int sub(int a, int b) { return a - b; }
int mul(int a, int b) { return a * b; }

/* File-scope initializers whose values are addresses. An address is not
 * known until the program is laid out, so these are not static bytes:
 * they are stores the module's initializer runs before main. */
static struct op ops[] = {
    { 1, add },
    { 2, sub },
    { 3, mul },
};

static binary_fn preferred = mul;

int entries(void) { return (int)(sizeof(ops) / sizeof(ops[0])); }
int entry_size(void) { return (int)sizeof(struct op); }
int callback_size(void) { return (int)sizeof(binary_fn); }

int dispatch(int code, int a, int b) {
    int i;
    for (i = 0; i < entries(); i++) {
        if (ops[i].code == code) {
            /* Called straight out of the struct field it is stored in. */
            return ops[i].apply(a, b);
        }
    }
    return -1;
}

static int less_than(int a, int b) { return a < b; }
static int greater_than(int a, int b) { return a > b; }

/* A function returning a function pointer, written without the typedef
 * so the declarator has to be read the hard way. */
static int (*chooser(int descending))(int, int) {
    if (descending) {
        return greater_than;
    }
    return less_than;
}

static void sort(int *xs, int n, binary_fn before) {
    int i, j;
    for (i = 1; i < n; i++) {
        j = i;
        /* `(*before)(...)` is the older spelling of `before(...)`, and
         * C defines them to be the same call. */
        while (j > 0 && (*before)(xs[j], xs[j - 1])) {
            int swap = xs[j];
            xs[j] = xs[j - 1];
            xs[j - 1] = swap;
            j--;
        }
    }
}

int sorted_at(int descending, int i) {
    int xs[6];
    xs[0] = 41;
    xs[1] = 7;
    xs[2] = 19;
    xs[3] = 3;
    xs[4] = 28;
    xs[5] = 12;
    sort(xs, 6, chooser(descending));
    return xs[i];
}

static int call_through(binary_fn f, int a, int b, int fallback) {
    if (f == 0) {
        return fallback;
    }
    return f(a, b);
}

int call_or(int use_null, int a, int b, int fallback) {
    binary_fn f = add;
    if (use_null) {
        f = 0;
    }
    return call_through(f, a, b, fallback);
}

int identity_holds(void) {
    binary_fn one = add;
    binary_fn two = &add;
    if (one != two) {
        return 0;
    }
    if (one == sub) {
        return 0;
    }
    /* Dereferencing a function pointer gives the function back, which
     * decays to the same pointer again. */
    if ((*one) != one) {
        return 0;
    }
    return preferred == mul;
}

/* A pointer with static storage: block-scoped name, module-scoped
 * lifetime, so the walk survives the call that made it. */
int stepped(int x) {
    static int (*next)(int, int) = add;
    int out = next(x, 2);
    if (next == add) {
        next = mul;
    } else if (next == mul) {
        next = sub;
    } else {
        next = add;
    }
    return out;
}
