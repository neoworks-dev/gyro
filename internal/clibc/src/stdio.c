/* stdio.h over the byte floor.
 *
 * There are no files: the floor is write_byte, read_byte and exit, so
 * the only streams are stdout, stderr and stdin. FILE exists as an
 * incomplete type with exactly two values so that `FILE *` declarations
 * and `fprintf(stderr, ...)` compile and mean what they say.
 *
 * stdout is buffered here rather than at the boundary, which is the
 * point of buffering it: the floor costs one call per byte, so a
 * program that formats a line and flushes it pays for the line and not
 * for each conversion. stderr is unbuffered, so a message written just
 * before an abort has already left. */

#include <ctype.h>
#include <gyro.h>
#include <limits.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

struct _GY_FILE {
    int fd;
    unsigned char *buffer;
    unsigned size;
    unsigned used;
};

#define OUT_BUFFER 4096
static unsigned char stdout_bytes[OUT_BUFFER];

static struct _GY_FILE file_stdout = {1, stdout_bytes, OUT_BUFFER, 0};
static struct _GY_FILE file_stderr = {2, 0, 0, 0};
static struct _GY_FILE file_stdin = {0, 0, 0, 0};

FILE *stdout = &file_stdout;
FILE *stderr = &file_stderr;
FILE *stdin = &file_stdin;

int fflush(FILE *stream) {
    if (stream == 0 || stream->buffer == 0) return 0;
    for (unsigned i = 0; i < stream->used; i++) {
        __gy_write_byte(stream->fd, stream->buffer[i]);
    }
    stream->used = 0;
    return 0;
}

int fputc(int c, FILE *stream) {
    if (stream == 0) stream = stdout;
    if (stream->buffer == 0) {
        __gy_write_byte(stream->fd, c & 0xff);
        return c & 0xff;
    }
    if (stream->used == stream->size) fflush(stream);
    stream->buffer[stream->used++] = (unsigned char)c;
    return c & 0xff;
}

int putchar(int c) { return fputc(c, stdout); }

int fputs(const char *s, FILE *stream) {
    for (; *s; s++) fputc((unsigned char)*s, stream);
    return 0;
}

/* puts appends a newline; fputs does not. */
int puts(const char *s) {
    fputs(s, stdout);
    fputc('\n', stdout);
    return 0;
}

size_t fwrite(const void *data, size_t size, size_t count, FILE *stream) {
    const unsigned char *bytes = data;
    size_t total = size * count;
    for (size_t i = 0; i < total; i++) fputc(bytes[i], stream);
    return count;
}

int getchar(void) { return __gy_read_byte(); }

/* ------------------------------------------------------------------ */
/* printf                                                              */
/* ------------------------------------------------------------------ */

/* A sink is either a stream or a caller's buffer. snprintf keeps
 * counting past the limit, because its return value is the length the
 * output would have had. */
typedef struct {
    FILE *stream;
    char *out;
    size_t limit;
    size_t written;
} sink;

static void put(sink *s, char c) {
    if (s->stream) {
        fputc((unsigned char)c, s->stream);
    } else if (s->written + 1 < s->limit) {
        s->out[s->written] = c;
    }
    s->written++;
}

static void put_repeat(sink *s, char c, int n) {
    for (int i = 0; i < n; i++) put(s, c);
}

typedef struct {
    int minus, plus, space, hash, zero;
    int width;
    int precision; /* -1 when absent */
} spec;

/* emit_padded writes one converted field: a prefix that must precede any
 * zero padding ("-", "0x"), then the body, honouring width and the left
 * and zero flags. */
static void emit_padded(sink *s, const spec *f, const char *prefix,
                        const char *body, int body_len, int zeros) {
    int prefix_len = (int)strlen(prefix);
    int total = prefix_len + zeros + body_len;
    int pad = f->width > total ? f->width - total : 0;

    if (!f->minus && !f->zero) put_repeat(s, ' ', pad);
    for (int i = 0; i < prefix_len; i++) put(s, prefix[i]);
    if (!f->minus && f->zero) put_repeat(s, '0', pad);
    put_repeat(s, '0', zeros);
    for (int i = 0; i < body_len; i++) put(s, body[i]);
    if (f->minus) put_repeat(s, ' ', pad);
}

static const char *digits_lower = "0123456789abcdef";
static const char *digits_upper = "0123456789ABCDEF";

/* render_unsigned writes the digits backwards into buf and returns where
 * they start, so the caller does not have to reverse anything. */
static char *render_unsigned(unsigned long long v, unsigned base, int upper,
                             char *buf, int cap, int *length) {
    const char *table = upper ? digits_upper : digits_lower;
    char *p = buf + cap;
    if (v == 0) *--p = '0';
    while (v != 0) {
        *--p = table[v % base];
        v /= base;
    }
    *length = (int)(buf + cap - p);
    return p;
}

static void integer_field(sink *s, spec *f, unsigned long long magnitude,
                          int negative, unsigned base, int upper) {
    char buf[24];
    int length;
    char *body = render_unsigned(magnitude, base, upper, buf, (int)sizeof buf, &length);

    /* A precision of 0 prints nothing at all for a zero value. */
    if (f->precision == 0 && magnitude == 0) length = 0;

    int zeros = 0;
    if (f->precision >= 0) {
        zeros = f->precision > length ? f->precision - length : 0;
        f->zero = 0; /* an explicit precision overrides the 0 flag */
    }

    char prefix[4] = {0};
    int at = 0;
    if (negative) {
        prefix[at++] = '-';
    } else if (f->plus) {
        prefix[at++] = '+';
    } else if (f->space) {
        prefix[at++] = ' ';
    }
    if (f->hash && base == 16 && magnitude != 0) {
        prefix[at++] = '0';
        prefix[at++] = upper ? 'X' : 'x';
    } else if (f->hash && base == 8 && magnitude != 0) {
        prefix[at++] = '0';
    }

    emit_padded(s, f, prefix, body, length, zeros);
}

static void string_field(sink *s, const spec *f, const char *value) {
    if (value == 0) value = "(null)";
    int length = (int)(f->precision >= 0 ? strnlen(value, (size_t)f->precision)
                                         : strlen(value));
    emit_padded(s, f, "", value, length, 0);
}

/* ------------------------------------------------------------------ */
/* Floating point                                                      */
/* ------------------------------------------------------------------ */

static double power_of_ten(int exponent) {
    double factor = 1.0, base = 10.0;
    int e = exponent < 0 ? -exponent : exponent;
    for (; e > 0; e >>= 1) {
        if (e & 1) factor *= base;
        base *= base;
    }
    return exponent < 0 ? 1.0 / factor : factor;
}

/* non_finite handles the two spellings that are not numbers, which are
 * the same at every precision and width. */
static int non_finite(sink *s, const spec *f, double v, int upper) {
    const char *body = 0;
    if (__builtin_isnan(v)) {
        body = upper ? "NAN" : "nan";
    } else if (__builtin_isinf(v)) {
        body = upper ? "INF" : "inf";
    } else {
        return 0;
    }

    char prefix[2] = {0};
    if (__builtin_signbit(v)) {
        prefix[0] = '-';
    } else if (f->plus) {
        prefix[0] = '+';
    } else if (f->space) {
        prefix[0] = ' ';
    }
    spec plain = *f;
    plain.zero = 0; /* nan is never zero-padded */
    emit_padded(s, &plain, prefix, body, 3, 0);
    return 1;
}

/* TWO_53 is where a double stops being able to hold every integer, and
 * so where scaling by the precision stops being exact. */
#define TWO_53 9007199254740992.0

static unsigned long long integer_power_of_ten(int exponent) {
    unsigned long long v = 1;
    for (int i = 0; i < exponent; i++) v *= 10;
    return v;
}

/* two_product returns a * b as an unevaluated sum: `product` is the
 * rounded result and `error` is exactly what the rounding threw away, so
 * product + error is the true value. Dekker's algorithm, which needs
 * only arithmetic — there is no fma opcode in wasm, and calling one
 * would mean a libm this library is supposed to be.
 *
 * This is what makes the tie test honest. Scaling 0.35 by ten rounds up
 * to exactly 3.5, so a rounder looking only at the product sees a tie
 * where the true value is below one; the error term says which side it
 * was on. */
static void two_product(double a, double b, double *product, double *error) {
    *product = a * b;
    const double split = 134217729.0; /* 2^27 + 1 */
    double a_hi = (split * a) - ((split * a) - a);
    double a_lo = a - a_hi;
    double b_hi = (split * b) - ((split * b) - b);
    double b_lo = b - b_hi;
    *error = ((a_hi * b_hi - *product) + a_hi * b_lo + a_lo * b_hi) + a_lo * b_lo;
}

/* fixed_field is %f.
 *
 * When |v| * 10^precision is below 2^53 the digits come out of exact
 * integer arithmetic and the rounding is half-to-even, which is what
 * printf specifies and what glibc does: `%.1f` of 0.25 is 0.2, and
 * `%.1f` of 0.35 is 0.3 because 0.35 as a double is a shade under the
 * tie. That covers everything a program prints for a person to read.
 *
 * Above 2^53 it falls back to scaling in floating point, and there it
 * can differ from glibc by one unit in the last digit — `%.6f` of a
 * value near 1e11 is where that starts. The exact algorithm for the
 * whole range exists in this repository already, as
 * tests/testdata/run/cjson/dtoa.gyro, a bignum expansion of the
 * significand; porting it here is the recorded next step, and until then
 * this is the honest limit. */
static void fixed_field(sink *s, spec *f, double v, int upper) {
    if (non_finite(s, f, v, upper)) return;

    int precision = f->precision < 0 ? 6 : f->precision;
    int negative = __builtin_signbit(v);
    double magnitude = __builtin_fabs(v);

    char body[512];
    int at = 0;

    /* 10^p is exactly representable up to p = 22, so below that the only
     * rounding in the scaling is the multiply's own — and two_product
     * hands that back exactly. */
    double scaled, scale_error;
    two_product(magnitude, power_of_ten(precision), &scaled, &scale_error);
    if (precision <= 19 && scaled < TWO_53) {
        double whole = __builtin_floor(scaled);
        double remainder = scaled - whole;
        if (remainder > 0.5) {
            whole += 1.0;
        } else if (remainder == 0.5) {
            /* The product landed on a tie. Where the true value actually
             * sat is what the error term says: below the tie rounds
             * down, above it rounds up, and only an exact tie goes to
             * the even neighbour. `whole` is below 2^53, so the
             * conversion reads its parity exactly. */
            if (scale_error > 0.0) {
                whole += 1.0;
            } else if (scale_error == 0.0 && ((unsigned long long)whole & 1ULL)) {
                whole += 1.0;
            }
        }

        unsigned long long units = (unsigned long long)whole;
        unsigned long long scale = integer_power_of_ten(precision);
        unsigned long long integral = units / scale;
        unsigned long long fraction = units % scale;

        char digits[32];
        int length;
        char *start = render_unsigned(integral, 10, 0, digits, (int)sizeof digits, &length);
        for (int i = 0; i < length; i++) body[at++] = start[i];

        if (precision > 0 || f->hash) {
            body[at++] = '.';
            /* The fraction is printed with leading zeros to the full
             * precision, which is what makes 1.005 print as 1.005 and
             * not as 1.5. */
            char frac_digits[24];
            int frac_len;
            char *frac_start = render_unsigned(fraction, 10, 0, frac_digits,
                                               (int)sizeof frac_digits, &frac_len);
            for (int i = frac_len; i < precision; i++) body[at++] = '0';
            for (int i = 0; i < frac_len && precision > 0; i++) body[at++] = frac_start[i];
        }
    } else {
        magnitude += 0.5 * power_of_ten(-precision);
        double integral = __builtin_floor(magnitude);
        double fraction = magnitude - integral;

        char digits[32];
        int length;
        char *start;
        if (integral < 18446744073709551616.0) {
            start = render_unsigned((unsigned long long)integral, 10, 0, digits,
                                    (int)sizeof digits, &length);
        } else {
            /* Past 2^64 the integer part has no exact 64-bit form.
             * Printing the digits it does have would be a lie about the
             * ones it does not, so this says so instead. */
            start = digits;
            length = 3;
            digits[0] = 'b';
            digits[1] = 'i';
            digits[2] = 'g';
        }
        for (int i = 0; i < length && at < (int)sizeof body; i++) body[at++] = start[i];

        if (precision > 0 || f->hash) {
            if (at < (int)sizeof body) body[at++] = '.';
            for (int i = 0; i < precision && at < (int)sizeof body - 1; i++) {
                fraction *= 10.0;
                int digit = (int)fraction;
                if (digit < 0) digit = 0;
                if (digit > 9) digit = 9;
                body[at++] = (char)('0' + digit);
                fraction -= (double)digit;
            }
        }
    }

    char prefix[2] = {0};
    if (negative) {
        prefix[0] = '-';
    } else if (f->plus) {
        prefix[0] = '+';
    } else if (f->space) {
        prefix[0] = ' ';
    }
    emit_padded(s, f, prefix, body, at, 0);
}

/* scientific_field is %e: normalize to one digit before the point, then
 * reuse the fixed path for the mantissa and append the exponent. */
static void scientific_field(sink *s, spec *f, double v, int upper) {
    if (non_finite(s, f, v, upper)) return;

    int precision = f->precision < 0 ? 6 : f->precision;
    int negative = __builtin_signbit(v);
    double magnitude = __builtin_fabs(v);

    int exponent = 0;
    if (magnitude != 0.0) {
        while (magnitude >= 10.0) { magnitude /= 10.0; exponent++; }
        while (magnitude < 1.0) { magnitude *= 10.0; exponent--; }
        /* Rounding the mantissa can carry it back to 10.0. */
        double rounded = magnitude + 0.5 * power_of_ten(-precision);
        if (rounded >= 10.0) { magnitude /= 10.0; exponent++; }
    }

    char body[512];
    int at = 0;
    double rounding = magnitude + 0.5 * power_of_ten(-precision);
    int lead = (int)rounding;
    if (lead > 9) lead = 9;
    body[at++] = (char)('0' + lead);
    double fraction = rounding - (double)lead;

    if (precision > 0 || f->hash) {
        body[at++] = '.';
        for (int i = 0; i < precision && at < (int)sizeof body - 8; i++) {
            fraction *= 10.0;
            int digit = (int)fraction;
            if (digit < 0) digit = 0;
            if (digit > 9) digit = 9;
            body[at++] = (char)('0' + digit);
            fraction -= (double)digit;
        }
    }

    body[at++] = upper ? 'E' : 'e';
    body[at++] = exponent < 0 ? '-' : '+';
    int e = exponent < 0 ? -exponent : exponent;
    /* C requires at least two exponent digits. */
    if (e >= 100) body[at++] = (char)('0' + e / 100);
    body[at++] = (char)('0' + (e / 10) % 10);
    body[at++] = (char)('0' + e % 10);

    char prefix[2] = {0};
    if (negative) {
        prefix[0] = '-';
    } else if (f->plus) {
        prefix[0] = '+';
    } else if (f->space) {
        prefix[0] = ' ';
    }
    emit_padded(s, f, prefix, body, at, 0);
}

/* general_field is %g: %e when the exponent is far from zero, %f
 * otherwise, and trailing zeros dropped unless # was given. */
static void general_field(sink *s, spec *f, double v, int upper) {
    if (non_finite(s, f, v, upper)) return;

    int precision = f->precision < 0 ? 6 : (f->precision == 0 ? 1 : f->precision);
    double magnitude = __builtin_fabs(v);

    int exponent = 0;
    if (magnitude != 0.0) {
        while (magnitude >= 10.0) { magnitude /= 10.0; exponent++; }
        while (magnitude < 1.0) { magnitude *= 10.0; exponent--; }
    }

    /* Render into a scratch buffer so the trailing zeros can be trimmed
     * before width padding decides anything. */
    char scratch[512];
    sink inner = {0, scratch, sizeof scratch, 0};
    spec bare = {0, f->plus, f->space, f->hash, 0, 0, 0};

    int use_scientific = exponent < -4 || exponent >= precision;
    if (use_scientific) {
        bare.precision = precision - 1;
        scientific_field(&inner, &bare, v, upper);
    } else {
        bare.precision = precision - 1 - exponent;
        fixed_field(&inner, &bare, v, upper);
    }
    size_t length = inner.written < sizeof scratch ? inner.written : sizeof scratch - 1;
    scratch[length] = '\0';

    if (!f->hash && memchr(scratch, '.', length) != 0) {
        /* Trim inside the mantissa, which for %e stops at the 'e'. */
        char *exp_at = strchr(scratch, upper ? 'E' : 'e');
        size_t mantissa_end = exp_at ? (size_t)(exp_at - scratch) : length;
        size_t trimmed = mantissa_end;
        while (trimmed > 0 && scratch[trimmed - 1] == '0') trimmed--;
        if (trimmed > 0 && scratch[trimmed - 1] == '.') trimmed--;
        if (exp_at) {
            memmove(scratch + trimmed, exp_at, length - mantissa_end);
            length = trimmed + (length - mantissa_end);
        } else {
            length = trimmed;
        }
    }

    spec plain = *f;
    plain.precision = -1;
    emit_padded(s, &plain, "", scratch, (int)length, 0);
}

/* ------------------------------------------------------------------ */
/* The format walk                                                     */
/* ------------------------------------------------------------------ */

enum length_kind { LEN_INT, LEN_CHAR, LEN_SHORT, LEN_LONG, LEN_LLONG, LEN_SIZE };

static int format(sink *s, const char *fmt, va_list args) {
    for (; *fmt; fmt++) {
        if (*fmt != '%') {
            put(s, *fmt);
            continue;
        }
        fmt++;
        if (*fmt == '%') {
            put(s, '%');
            continue;
        }

        spec f = {0, 0, 0, 0, 0, 0, -1};
        for (;; fmt++) {
            if (*fmt == '-') f.minus = 1;
            else if (*fmt == '+') f.plus = 1;
            else if (*fmt == ' ') f.space = 1;
            else if (*fmt == '#') f.hash = 1;
            else if (*fmt == '0') f.zero = 1;
            else break;
        }

        if (*fmt == '*') {
            f.width = va_arg(args, int);
            if (f.width < 0) { f.minus = 1; f.width = -f.width; }
            fmt++;
        } else {
            while (isdigit((unsigned char)*fmt)) f.width = f.width * 10 + (*fmt++ - '0');
        }

        if (*fmt == '.') {
            fmt++;
            f.precision = 0;
            if (*fmt == '*') {
                f.precision = va_arg(args, int);
                fmt++;
            } else {
                while (isdigit((unsigned char)*fmt)) {
                    f.precision = f.precision * 10 + (*fmt++ - '0');
                }
            }
        }

        enum length_kind length = LEN_INT;
        switch (*fmt) {
        case 'h':
            fmt++;
            length = LEN_SHORT;
            if (*fmt == 'h') { fmt++; length = LEN_CHAR; }
            break;
        case 'l':
            fmt++;
            length = LEN_LONG;
            if (*fmt == 'l') { fmt++; length = LEN_LLONG; }
            break;
        case 'z':
        case 't':
        case 'j':
            fmt++;
            length = LEN_SIZE;
            break;
        default:
            break;
        }

        switch (*fmt) {
        case 'd':
        case 'i': {
            long long v;
            switch (length) {
            case LEN_LLONG: v = va_arg(args, long long); break;
            case LEN_LONG: v = va_arg(args, long); break;
            case LEN_SIZE: v = (long long)va_arg(args, size_t); break;
            case LEN_SHORT: v = (short)va_arg(args, int); break;
            case LEN_CHAR: v = (signed char)va_arg(args, int); break;
            default: v = va_arg(args, int); break;
            }
            /* Negating LLONG_MIN would overflow, so the magnitude is
             * taken in unsigned arithmetic where it fits. */
            unsigned long long magnitude =
                v < 0 ? (unsigned long long)(-(v + 1)) + 1 : (unsigned long long)v;
            integer_field(s, &f, magnitude, v < 0, 10, 0);
            break;
        }
        case 'u':
        case 'o':
        case 'x':
        case 'X': {
            unsigned long long v;
            switch (length) {
            case LEN_LLONG: v = va_arg(args, unsigned long long); break;
            case LEN_LONG: v = va_arg(args, unsigned long); break;
            case LEN_SIZE: v = va_arg(args, size_t); break;
            case LEN_SHORT: v = (unsigned short)va_arg(args, unsigned); break;
            case LEN_CHAR: v = (unsigned char)va_arg(args, unsigned); break;
            default: v = va_arg(args, unsigned); break;
            }
            unsigned base = *fmt == 'o' ? 8 : (*fmt == 'u' ? 10 : 16);
            integer_field(s, &f, v, 0, base, *fmt == 'X');
            break;
        }
        case 'c': {
            char c = (char)va_arg(args, int);
            spec plain = f;
            plain.precision = -1;
            emit_padded(s, &plain, "", &c, 1, 0);
            break;
        }
        case 's':
            string_field(s, &f, va_arg(args, const char *));
            break;
        case 'p': {
            /* A pointer is a linear address, so it prints as one: an
             * offset into the memory, not a machine address. */
            unsigned long long v = (unsigned long long)(uintptr_t)va_arg(args, void *);
            spec plain = f;
            plain.hash = 1;
            plain.precision = -1;
            if (v == 0) {
                emit_padded(s, &f, "", "(nil)", 5, 0);
            } else {
                integer_field(s, &plain, v, 0, 16, 0);
            }
            break;
        }
        case 'f':
        case 'F':
            fixed_field(s, &f, va_arg(args, double), *fmt == 'F');
            break;
        case 'e':
        case 'E':
            scientific_field(s, &f, va_arg(args, double), *fmt == 'E');
            break;
        case 'g':
        case 'G':
            general_field(s, &f, va_arg(args, double), *fmt == 'G');
            break;
        case '\0':
            /* A format ending in % has nothing to convert; back up so
             * the loop's own increment does not run past the end. */
            fmt--;
            break;
        default:
            /* An unknown conversion prints as written, which is more
             * useful than dropping it. */
            put(s, '%');
            put(s, *fmt);
            break;
        }
    }
    return (int)s->written;
}

int vfprintf(FILE *stream, const char *fmt, va_list args) {
    sink s = {stream, 0, 0, 0};
    return format(&s, fmt, args);
}

int vprintf(const char *fmt, va_list args) { return vfprintf(stdout, fmt, args); }

int vsnprintf(char *out, size_t limit, const char *fmt, va_list args) {
    sink s = {0, out, limit, 0};
    int n = format(&s, fmt, args);
    if (limit > 0) {
        out[s.written < limit ? s.written : limit - 1] = '\0';
    }
    return n;
}

int printf(const char *fmt, ...) {
    va_list args;
    va_start(args, fmt);
    int n = vfprintf(stdout, fmt, args);
    va_end(args);
    return n;
}

int fprintf(FILE *stream, const char *fmt, ...) {
    va_list args;
    va_start(args, fmt);
    int n = vfprintf(stream, fmt, args);
    va_end(args);
    return n;
}

int snprintf(char *out, size_t limit, const char *fmt, ...) {
    va_list args;
    va_start(args, fmt);
    int n = vsnprintf(out, limit, fmt, args);
    va_end(args);
    return n;
}

int sprintf(char *out, const char *fmt, ...) {
    va_list args;
    va_start(args, fmt);
    int n = vsnprintf(out, (size_t)-1, fmt, args);
    va_end(args);
    return n;
}

/* ------------------------------------------------------------------ */
/* Leaving main                                                        */
/* ------------------------------------------------------------------ */

/* Returning from main is exit(status), which means stdout has to be
 * flushed. The synthesized METALS entry calls this after main when the
 * module exports it, which is how a buffered libc gets its last write
 * out without the bridge knowing there is a libc. */
__attribute__((export_name("__gy_finish"))) void __gy_finish(int status) {
    fflush(stdout);
    if (status != 0) __gy_exit(status);
}
