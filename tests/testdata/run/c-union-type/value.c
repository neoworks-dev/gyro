/* A C union crossing into Gyro as a type rather than behind an
 * accessor. Until Gyro could declare a union, this header was
 * unspellable: `declarable` refused any type that was not a record, so a
 * library handing out a union could be called but its type could not be
 * named, and every member had to be read through a C function written
 * for the purpose.
 *
 * Now the generated header writes `export union Value { ... }` and the
 * Gyro side reads and writes members directly, on the same storage the C
 * side lays out. */

#include "value.h"

union Value make_int(long n) {
    union Value v;
    v.i = n;
    return v;
}

long int_of(union Value v) { return v.i; }

/* An out-parameter over a union: C writes the storage Gyro owns. */
void store_double(union Value *out, double f) { out->f = f; }

struct Boxed boxed(int kind, long n) {
    struct Boxed b;
    b.kind = kind;
    b.value.i = n;
    return b;
}

int kind_of(const struct Boxed *b) { return b->kind; }

/* The size is the widest member's at the strictest member's alignment,
 * and that is what Gyro lays out too — the assertion this case is really
 * making. */
long value_size(void) { return (long)sizeof(union Value); }
long boxed_size(void) { return (long)sizeof(struct Boxed); }
