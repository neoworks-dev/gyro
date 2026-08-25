/* stdlib.h, less the allocator. */

#include <ctype.h>
#include <gyro.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

int errno = 0;

_Noreturn void exit(int code) {
    fflush(stdout);
    __gy_exit(code);
}

_Noreturn void abort(void) {
    fputs("abort\n", stderr);
    fflush(stdout);
    __gy_exit(134); /* what a SIGABRT death reports through a shell */
}

_Noreturn void __gy_assert_failed(const char *cond, const char *file, int line) {
    fprintf(stderr, "%s:%d: assertion failed: %s\n", file, line, cond);
    fflush(stdout);
    __gy_exit(134);
}

int abs(int v) { return v < 0 ? -v : v; }
long labs(long v) { return v < 0 ? -v : v; }
long long llabs(long long v) { return v < 0 ? -v : v; }

/* ------------------------------------------------------------------ */
/* Numeric parsing                                                     */
/* ------------------------------------------------------------------ */

static int digit_value(int c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'z') return c - 'a' + 10;
    if (c >= 'A' && c <= 'Z') return c - 'A' + 10;
    return -1;
}

/* parse_integer is strtoull's body, with the sign and the saturation
 * left to the callers so that each width reports its own limits. */
static unsigned long long parse_integer(const char *s, char **end, int base,
                                        int *negative, int *any) {
    const char *start = s;
    while (isspace((unsigned char)*s)) s++;

    *negative = 0;
    if (*s == '+' || *s == '-') {
        *negative = (*s == '-');
        s++;
    }

    if ((base == 0 || base == 16) && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') &&
        digit_value((unsigned char)s[2]) >= 0 && digit_value((unsigned char)s[2]) < 16) {
        s += 2;
        base = 16;
    } else if (base == 0) {
        base = (s[0] == '0') ? 8 : 10;
    }

    unsigned long long value = 0;
    *any = 0;
    for (;; s++) {
        int digit = digit_value((unsigned char)*s);
        if (digit < 0 || digit >= base) break;
        value = value * (unsigned)base + (unsigned)digit;
        *any = 1;
    }

    /* With no digits consumed the whole run, sign included, is not part
     * of the number. */
    if (end) *end = (char *)(*any ? s : start);
    return value;
}

long long strtoll(const char *s, char **end, int base) {
    int negative, any;
    unsigned long long v = parse_integer(s, end, base, &negative, &any);
    if (negative) {
        if (v > (unsigned long long)LLONG_MAX + 1) return LLONG_MIN;
        return -(long long)v;
    }
    if (v > (unsigned long long)LLONG_MAX) return LLONG_MAX;
    return (long long)v;
}

unsigned long long strtoull(const char *s, char **end, int base) {
    int negative, any;
    unsigned long long v = parse_integer(s, end, base, &negative, &any);
    return negative ? (unsigned long long)(-(long long)v) : v;
}

long strtol(const char *s, char **end, int base) {
    long long v = strtoll(s, end, base);
    if (v > LONG_MAX) return LONG_MAX;
    if (v < LONG_MIN) return LONG_MIN;
    return (long)v;
}

unsigned long strtoul(const char *s, char **end, int base) {
    return (unsigned long)strtoull(s, end, base);
}

int atoi(const char *s) { return (int)strtol(s, NULL, 10); }
long atol(const char *s) { return strtol(s, NULL, 10); }
long long atoll(const char *s) { return strtoll(s, NULL, 10); }

/* strtod handles the decimal forms and the exponent, which is what a
 * configuration file or a JSON document contains. It accumulates the
 * digits into a double as it goes, so the result can be a unit or two
 * off the nearest double for a long mantissa; correctly-rounded parsing
 * needs the same bignum an exact printer does, and neither is here yet. */
double strtod(const char *s, char **end) {
    const char *start = s;
    while (isspace((unsigned char)*s)) s++;

    int negative = 0;
    if (*s == '+' || *s == '-') {
        negative = (*s == '-');
        s++;
    }

    double value = 0.0;
    int any = 0;
    for (; isdigit((unsigned char)*s); s++) {
        value = value * 10.0 + (double)(*s - '0');
        any = 1;
    }
    if (*s == '.') {
        s++;
        double scale = 0.1;
        for (; isdigit((unsigned char)*s); s++) {
            value += (double)(*s - '0') * scale;
            scale *= 0.1;
            any = 1;
        }
    }
    if (!any) {
        if (end) *end = (char *)start;
        return 0.0;
    }

    if (*s == 'e' || *s == 'E') {
        const char *mark = s;
        s++;
        int exp_negative = 0;
        if (*s == '+' || *s == '-') {
            exp_negative = (*s == '-');
            s++;
        }
        if (isdigit((unsigned char)*s)) {
            int exponent = 0;
            for (; isdigit((unsigned char)*s); s++) {
                if (exponent < 10000) exponent = exponent * 10 + (*s - '0');
            }
            double factor = 1.0, base = 10.0;
            for (int e = exponent; e > 0; e >>= 1) {
                if (e & 1) factor *= base;
                base *= base;
            }
            value = exp_negative ? value / factor : value * factor;
        } else {
            s = mark; /* an `e` with no digits is not part of the number */
        }
    }

    if (end) *end = (char *)s;
    return negative ? -value : value;
}

double atof(const char *s) { return strtod(s, NULL); }

/* ------------------------------------------------------------------ */
/* Sorting and searching                                               */
/* ------------------------------------------------------------------ */

static void swap_bytes(unsigned char *a, unsigned char *b, size_t size) {
    for (size_t i = 0; i < size; i++) {
        unsigned char t = a[i];
        a[i] = b[i];
        b[i] = t;
    }
}

/* Shell sort with Ciura's gaps. qsort's contract says nothing about the
 * algorithm, and this needs no scratch allocation and no recursion,
 * which matters where the stack is a fixed region of linear memory. It
 * is not stable, which qsort never promised. */
void qsort(void *base, size_t count, size_t size,
           int (*compare)(const void *, const void *)) {
    static const size_t gaps[] = {701, 301, 132, 57, 23, 10, 4, 1};
    unsigned char *data = base;

    for (size_t g = 0; g < sizeof gaps / sizeof gaps[0]; g++) {
        size_t gap = gaps[g];
        if (gap >= count) continue;
        for (size_t i = gap; i < count; i++) {
            for (size_t j = i; j >= gap; j -= gap) {
                unsigned char *left = data + (j - gap) * size;
                unsigned char *right = data + j * size;
                if (compare(left, right) <= 0) break;
                swap_bytes(left, right, size);
            }
        }
    }
}

void *bsearch(const void *key, const void *base, size_t count, size_t size,
              int (*compare)(const void *, const void *)) {
    const unsigned char *data = base;
    size_t low = 0, high = count;
    while (low < high) {
        size_t mid = low + (high - low) / 2;
        int order = compare(key, data + mid * size);
        if (order == 0) return (void *)(data + mid * size);
        if (order < 0) {
            high = mid;
        } else {
            low = mid + 1;
        }
    }
    return NULL;
}

/* ------------------------------------------------------------------ */
/* rand                                                                */
/* ------------------------------------------------------------------ */

/* The LCG C99 prints as an example implementation, so a program that
 * seeds and expects a particular sequence gets the documented one. */
static unsigned long rand_state = 1;

void srand(unsigned seed) { rand_state = seed; }

int rand(void) {
    rand_state = rand_state * 1103515245u + 12345u;
    return (int)((rand_state / 65536u) % 32768u);
}
