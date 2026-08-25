#include <stdarg.h>

/* A format string that promises more arguments than the call passed.
 * This is the oldest bug varargs has and the one C cannot report: the
 * callee walks off the end and reads whatever the stack held.
 *
 * The count travels with the arguments here, so it is an abort with a
 * message instead, and the same message on every tier. */
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

int too_few(void) {
    /* Says three, passes two. */
    return total(3, 20, 22);
}
