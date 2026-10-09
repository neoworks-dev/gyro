#include <stdarg.h>

#include "fmt.h"

#define CAPACITY 64

static char out[CAPACITY];
static int out_len = 0;

static void put(char c) {
    if (out_len < CAPACITY) {
        out[out_len] = c;
        out_len = out_len + 1;
    }
}

static void put_str(const char *s) {
    while (*s != 0) {
        put(*s);
        s = s + 1;
    }
}

static void put_int(int n) {
    char digits[12];
    int i = 0;

    if (n < 0) {
        put('-');
        n = -n;
    }
    if (n == 0) {
        put('0');
        return;
    }
    while (n > 0) {
        digits[i] = (char)('0' + n % 10);
        i = i + 1;
        n = n / 10;
    }
    while (i > 0) {
        i = i - 1;
        put(digits[i]);
    }
}

/* The vprintf half: it takes the walk rather than the arguments, so the
 * cursor it advances is the one its caller declared. */
static void vrender(const char *spec, va_list ap) {
    const char *p = spec;

    while (*p != 0) {
        if (*p != '%') {
            put(*p);
            p = p + 1;
            continue;
        }
        p = p + 1;
        if (*p == 'd') {
            put_int(va_arg(ap, int));
        } else if (*p == 's') {
            put_str(va_arg(ap, const char *));
        } else if (*p == 'c') {
            /* A char argument arrived as an int, and va_arg has to ask
             * for what was passed rather than for what was written. */
            put((char)va_arg(ap, int));
        } else if (*p == '%') {
            put('%');
        } else {
            put('?');
        }
        p = p + 1;
    }
}

static int render(const char *spec, ...) {
    va_list ap;

    out_len = 0;
    va_start(ap, spec);
    vrender(spec, ap);
    va_end(ap);
    return out_len;
}

int render_mixed(void) {
    return render("%s has %d items, tag %c", "cart", 3, 'A');
}

int render_signs(void) {
    return render("%d and %d", -407, 0);
}

int render_percent(void) {
    return render("%d%% of %s, %z", 75, "it", 1);
}

int render_nothing(void) {
    return render("plain");
}

int rendered_at(int index) {
    return (int)out[index];
}

/* Not static, so C exports it — and a `...` says nothing about what a
 * call passes, so nothing on the far side of a boundary could check one.
 * The generated Gyro header leaves it out rather than refusing the
 * module: one unspellable helper should not make the other eleven
 * unimportable. */
int total(int n, ...) {
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

int sum_of_none(void) {
    return total(0);
}

int sum_of_two(int a, int b) {
    return total(2, a, b);
}

int sum_of_five(void) {
    return total(5, 1, 2, 3, 4, 5);
}

static int add_two(int unused, va_list ap) {
    int a = va_arg(ap, int);
    int b = va_arg(ap, int);
    return unused + a + b;
}

static int forward(int base, ...) {
    va_list ap;
    int n;

    va_start(ap, base);
    n = add_two(base, ap);
    va_end(ap);
    return n;
}

int forwarded(int a, int b) {
    return forward(0, a, b);
}

static int both_walks(int n, ...) {
    va_list ap;
    va_list again;
    int first;
    int second;

    va_start(ap, n);
    va_copy(again, ap);
    first = va_arg(ap, int);
    second = va_arg(again, int);
    va_end(again);
    va_end(ap);
    return first + second;
}

int walked_twice(int x) {
    return both_walks(1, x);
}

static int promotions(int n, ...) {
    va_list ap;
    int whole;
    double fractional;

    va_start(ap, n);
    whole = va_arg(ap, int);
    fractional = va_arg(ap, double);
    va_end(ap);
    return whole + (int)fractional;
}

int promoted(void) {
    char c = 40;
    float f = 2.75f;

    /* c is an int and f is a double by the time either is passed, which
     * is why the callee reads them at those types. */
    return promotions(2, c, f);
}

static int deref_erased(int n, ...) {
    va_list ap;
    void *erased;
    int *back;

    va_start(ap, n);
    erased = va_arg(ap, void *);
    va_end(ap);
    back = erased;
    return *back;
}

int through_a_pointer(void) {
    int boxed = 42;
    return deref_erased(1, (void *)&boxed);
}
