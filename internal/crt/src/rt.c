/* gyro:rt — the native runtime behind the gy_* imports the code
 * generator emits. Compiled and linked by `gyro build`; the contract
 * (layouts, message texts, formatting rules) is docs/native.md. The
 * formatting must match Go's strconv byte for byte, because the METALS
 * evaluator prints through it and every tier must agree. */

#define _GNU_SOURCE
#include <ctype.h>
#include <dirent.h>
#include <errno.h>
#include <inttypes.h>
#include <math.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

/* ------------------------------------------------------------------ */
/* Strings: immutable { len, bytes } headers.                         */
/* ------------------------------------------------------------------ */

typedef struct {
    int64_t len;
    char bytes[];
} gy_str;

static gy_str *gy_str_alloc(int64_t len) {
    gy_str *s = malloc(sizeof(gy_str) + (size_t)len);
    s->len = len;
    return s;
}

static gy_str *gy_str_from(const char *text, size_t len) {
    gy_str *s = gy_str_alloc((int64_t)len);
    memcpy(s->bytes, text, len);
    return s;
}

static gy_str *gy_str_c(const char *text) { return gy_str_from(text, strlen(text)); }

/* ------------------------------------------------------------------ */
/* Aborts. A panic on the main thread ends the process with status 1; */
/* on a spawned thread only that thread dies, like the evaluator's    */
/* goroutine recovery.                                                */
/* ------------------------------------------------------------------ */

typedef struct gy_future gy_future;
static void gy_future_fulfill(gy_future *fut);

static __thread int gy_on_worker_thread = 0;
static __thread gy_future *gy_current_future = NULL;

_Noreturn void gy_abort(const gy_str *message) {
    fflush(stdout);
    fprintf(stderr, "panic: %.*s\n", (int)message->len, message->bytes);
    fflush(stderr);
    if (!gy_on_worker_thread) {
        exit(1);
    }
    if (gy_current_future != NULL) {
        gy_future_fulfill(gy_current_future);
    }
    pthread_exit(NULL);
}

_Noreturn static void gy_abort_c(const char *message) { gy_abort(gy_str_c(message)); }

_Noreturn void gy_abort_bounds(int64_t index, int64_t length) {
    char buf[96];
    snprintf(buf, sizeof buf, "index %" PRId64 " out of range for length %" PRId64, index, length);
    gy_abort_c(buf);
}

/* Narrowing a slice fails differently from indexing one: there are two
 * bounds and they may be wrong with respect to each other as well as to
 * the length, so the message names all three. */
_Noreturn void gy_abort_slice(int64_t lo, int64_t hi, int64_t length) {
    char buf[128];
    snprintf(buf, sizeof buf, "slice %" PRId64 "..%" PRId64 " out of range for length %" PRId64,
             lo, hi, length);
    gy_abort_c(buf);
}

/* ------------------------------------------------------------------ */
/* Linear memory: the flat, bounds-checked byte region a producer with */
/* C's memory model compiles into. Both backends share this, and both  */
/* inline the access itself — what lives here is what must be one      */
/* implementation: the abort text, the initial allocation and growth.  */
/*                                                                     */
/* Representation is little-endian, unconditionally, matching the      */
/* evaluator. Growth reallocs and zeroes, so a grown page reads as     */
/* zero the way a fresh one does.                                      */
/* ------------------------------------------------------------------ */

#define GY_PAGE_SIZE 65536
/* A u32 address space holds this many pages; growing past it would */
/* produce storage no address could reach. */
#define GY_MAX_PAGES 65536

typedef struct {
    uint8_t *bytes;
    uint64_t size;  /* bytes */
    uint64_t max;   /* pages; 0 means only GY_MAX_PAGES applies */
} gy_memory;

_Noreturn void gy_abort_mem_bounds(uint64_t addr, uint64_t end, uint64_t size) {
    char buf[128];
    snprintf(buf, sizeof buf,
             "linear memory access out of bounds: %" PRIu64 "..%" PRIu64 " of %" PRIu64 " bytes",
             addr, end, size);
    gy_abort_c(buf);
}

void gy_mem_init(gy_memory *m, uint64_t pages, uint64_t max) {
    m->size = pages * GY_PAGE_SIZE;
    m->max = max;
    m->bytes = calloc(1, m->size ? m->size : 1);
    if (m->bytes == NULL) {
        gy_abort_c("cannot allocate linear memory");
    }
}

/* Both ends are checked before either moves, so an access that would run
 * off the end changes nothing at all. */
static void gy_mem_check(const gy_memory *m, uint64_t addr, uint64_t len) {
    if (addr + len > m->size) {
        gy_abort_mem_bounds(addr, addr + len, m->size);
    }
}

/* The bulk operations are calls in both backends rather than inlined
 * code: each is one memmove or memset, so there is nothing for a call to
 * get in the way of, and one implementation is one abort message. */
void gy_mem_copy(gy_memory *m, uint64_t to, uint64_t from, uint64_t len) {
    gy_mem_check(m, to, len);
    gy_mem_check(m, from, len);
    memmove(m->bytes + to, m->bytes + from, len);
}

void gy_mem_fill(gy_memory *m, uint64_t to, uint64_t byte, uint64_t len) {
    gy_mem_check(m, to, len);
    memset(m->bytes + to, (int)(byte & 0xff), len);
}

/* gy_mem_data lays a data segment down before the program runs. The
 * source is the generated module's own storage, not the memory, so this
 * is the one copy that crosses in. */
void gy_mem_data(gy_memory *m, uint64_t offset, const void *src, uint64_t len) {
    gy_mem_check(m, offset, len);
    memcpy(m->bytes + offset, src, len);
}

int64_t gy_mem_grow(gy_memory *m, uint64_t pages) {
    uint64_t before = m->size / GY_PAGE_SIZE;
    uint64_t after = before + pages;
    uint64_t limit = (m->max > 0 && m->max < GY_MAX_PAGES) ? m->max : GY_MAX_PAGES;
    if (after > limit) {
        return -1;
    }
    uint8_t *grown = realloc(m->bytes, after * GY_PAGE_SIZE ? after * GY_PAGE_SIZE : 1);
    if (grown == NULL) {
        return -1;
    }
    memset(grown + m->size, 0, after * GY_PAGE_SIZE - m->size);
    m->bytes = grown;
    m->size = after * GY_PAGE_SIZE;
    return (int64_t)before;
}

/* ------------------------------------------------------------------ */
/* Memory. A magic header lets free reject a pointer that does not    */
/* name a live block; nothing not explicitly freed is ever reclaimed  */
/* in v1.                                                             */
/* ------------------------------------------------------------------ */

#define GY_ALIVE 0xA11FEED5A11FEED5ULL
#define GY_FREED 0xDEADDEADDEADDEADULL

void *gy_alloc(int64_t size) {
    uint64_t *cell = calloc(1, (size_t)size + 16);
    cell[0] = GY_ALIVE;
    return cell + 2;
}

/* The poison is written before the block goes back to the C allocator,
 * which promptly reuses those bytes for its own free-list bookkeeping —
 * so a second free reads whatever libc left behind and cannot tell a
 * double free from a wild pointer. Both say the same thing here, and
 * the evaluator says it too, because a message the tiers cannot all
 * justify is a message the corpus cannot hold them to. The precision
 * comes back when gy_free poisons the payload and keeps the block
 * instead of releasing it, which is the queued fix that also gives the
 * native tiers use-after-free detection. */
void gy_free(void *p) {
    uint64_t *cell = (uint64_t *)p - 2;
    if (cell[0] != GY_ALIVE) {
        gy_abort_c("free of memory that is not allocated");
    }
    cell[0] = GY_FREED;
    free(cell);
}

/* ------------------------------------------------------------------ */
/* Formatting, Go style.                                              */
/* ------------------------------------------------------------------ */

/* gy_format_float reproduces strconv.FormatFloat(v, 'g', -1, 64):
 * shortest round-trip digits, %e form when the decimal exponent is
 * below -4 or 6 and above, exponent at least two digits. The upper
 * threshold is 6 and not the width of the digits, because Go decides the
 * form at precision 6 whenever the digits themselves are the shortest
 * round trip: 123456 prints plainly, 1234567 prints as 1.234567e+06. */
static void gy_format_float(double v, char *out, size_t cap) {
    if (isnan(v)) {
        snprintf(out, cap, "NaN");
        return;
    }
    if (isinf(v)) {
        snprintf(out, cap, v > 0 ? "+Inf" : "-Inf");
        return;
    }

    char trial[64];
    int precision = 1;
    for (; precision <= 17; precision++) {
        snprintf(trial, sizeof trial, "%.*e", precision - 1, v);
        if (strtod(trial, NULL) == v) {
            break;
        }
    }

    /* Pull the digits and decimal exponent out of d.dddde±XX. */
    char digits[32];
    int digitCount = 0;
    const char *p = trial;
    char *w = out;
    if (*p == '-') {
        *w++ = '-';
        p++;
    }
    for (; *p != 'e'; p++) {
        if (*p != '.') {
            digits[digitCount++] = *p;
        }
    }
    int exponent = atoi(p + 1); /* value = 0.digits * 10^(exponent+1) */
    while (digitCount > 1 && digits[digitCount - 1] == '0') {
        digitCount--;
    }

    if (exponent < -4 || exponent >= 6) {
        *w++ = digits[0];
        if (digitCount > 1) {
            *w++ = '.';
            memcpy(w, digits + 1, (size_t)digitCount - 1);
            w += digitCount - 1;
        }
        *w++ = 'e';
        *w++ = exponent < 0 ? '-' : '+';
        int magnitude = exponent < 0 ? -exponent : exponent;
        if (magnitude < 10) {
            *w++ = '0';
        }
        w += sprintf(w, "%d", magnitude);
        *w = 0;
        return;
    }

    int pointAt = exponent + 1; /* digits before the decimal point */
    if (pointAt <= 0) {
        *w++ = '0';
        *w++ = '.';
        for (int i = 0; i < -pointAt; i++) {
            *w++ = '0';
        }
        memcpy(w, digits, (size_t)digitCount);
        w += digitCount;
    } else if (pointAt >= digitCount) {
        memcpy(w, digits, (size_t)digitCount);
        w += digitCount;
        for (int i = digitCount; i < pointAt; i++) {
            *w++ = '0';
        }
    } else {
        memcpy(w, digits, (size_t)pointAt);
        w += pointAt;
        *w++ = '.';
        memcpy(w, digits + pointAt, (size_t)(digitCount - pointAt));
        w += digitCount - pointAt;
    }
    *w = 0;
}

/* UTF-8 encode one code point, Go semantics: invalid scalars become
 * U+FFFD. Returns the byte count. */
static int gy_encode_rune(int64_t r, char *out) {
    if (r < 0 || r > 0x10FFFF || (r >= 0xD800 && r <= 0xDFFF)) {
        r = 0xFFFD;
    }
    if (r < 0x80) {
        out[0] = (char)r;
        return 1;
    }
    if (r < 0x800) {
        out[0] = (char)(0xC0 | (r >> 6));
        out[1] = (char)(0x80 | (r & 0x3F));
        return 2;
    }
    if (r < 0x10000) {
        out[0] = (char)(0xE0 | (r >> 12));
        out[1] = (char)(0x80 | ((r >> 6) & 0x3F));
        out[2] = (char)(0x80 | (r & 0x3F));
        return 3;
    }
    out[0] = (char)(0xF0 | (r >> 18));
    out[1] = (char)(0x80 | ((r >> 12) & 0x3F));
    out[2] = (char)(0x80 | ((r >> 6) & 0x3F));
    out[3] = (char)(0x80 | (r & 0x3F));
    return 4;
}

/* gy_quote is Go's %q for the ASCII range: the escapes strconv.Quote
 * would produce, non-ASCII bytes passed through. */
static gy_str *gy_quote(const gy_str *s) {
    char *buf = malloc((size_t)s->len * 4 + 3);
    char *w = buf;
    *w++ = '"';
    for (int64_t i = 0; i < s->len; i++) {
        unsigned char c = (unsigned char)s->bytes[i];
        switch (c) {
        case '"': *w++ = '\\'; *w++ = '"'; break;
        case '\\': *w++ = '\\'; *w++ = '\\'; break;
        case '\n': *w++ = '\\'; *w++ = 'n'; break;
        case '\r': *w++ = '\\'; *w++ = 'r'; break;
        case '\t': *w++ = '\\'; *w++ = 't'; break;
        default:
            if (c < 0x20 || c == 0x7f) {
                w += sprintf(w, "\\x%02x", c);
            } else {
                *w++ = (char)c;
            }
        }
    }
    *w++ = '"';
    gy_str *out = gy_str_from(buf, (size_t)(w - buf));
    free(buf);
    return out;
}

/* ------------------------------------------------------------------ */
/* The print family and str<T>.                                       */
/* ------------------------------------------------------------------ */

static pthread_mutex_t gy_print_mutex = PTHREAD_MUTEX_INITIALIZER;

static void gy_write(FILE *stream, const char *bytes, size_t len, int newline) {
    pthread_mutex_lock(&gy_print_mutex);
    fwrite(bytes, 1, len, stream);
    if (newline) {
        fputc('\n', stream);
    }
    fflush(stream);
    pthread_mutex_unlock(&gy_print_mutex);
}

static void gy_print_dispatch(FILE *stream, int newline, const char *bytes, size_t len) {
    gy_write(stream, bytes, len, newline);
}

#define GY_PRINT_FAMILY(suffix, type, RENDER)                                  \
    static void gy_render_##suffix(type v, char *buf, size_t cap) { RENDER; }  \
    void gy_print_##suffix(type v) {                                           \
        char buf[64];                                                          \
        gy_render_##suffix(v, buf, sizeof buf);                                \
        gy_print_dispatch(stdout, 0, buf, strlen(buf));                        \
    }                                                                          \
    void gy_println_##suffix(type v) {                                         \
        char buf[64];                                                          \
        gy_render_##suffix(v, buf, sizeof buf);                                \
        gy_print_dispatch(stdout, 1, buf, strlen(buf));                        \
    }                                                                          \
    void gy_eprintln_##suffix(type v) {                                        \
        char buf[64];                                                          \
        gy_render_##suffix(v, buf, sizeof buf);                                \
        gy_print_dispatch(stderr, 1, buf, strlen(buf));                        \
    }                                                                          \
    gy_str *gy_str_##suffix(type v) {                                          \
        char buf[64];                                                          \
        gy_render_##suffix(v, buf, sizeof buf);                                \
        return gy_str_c(buf);                                                  \
    }

GY_PRINT_FAMILY(int, int64_t, snprintf(buf, cap, "%" PRId64, v))
GY_PRINT_FAMILY(uint, uint64_t, snprintf(buf, cap, "%" PRIu64, v))
GY_PRINT_FAMILY(float, double, gy_format_float(v, buf, cap))
GY_PRINT_FAMILY(bool, int64_t, snprintf(buf, cap, "%s", v ? "true" : "false"))
GY_PRINT_FAMILY(rune, int64_t, buf[gy_encode_rune(v, buf)] = 0)

void gy_print_str(const gy_str *s) { gy_print_dispatch(stdout, 0, s->bytes, (size_t)s->len); }
void gy_println_str(const gy_str *s) { gy_print_dispatch(stdout, 1, s->bytes, (size_t)s->len); }
void gy_eprintln_str(const gy_str *s) { gy_print_dispatch(stderr, 1, s->bytes, (size_t)s->len); }

/* A string prints raw but renders quoted: str<string> is only ever
   reached from inside a composite's representation. */
gy_str *gy_str_str(const gy_str *s) { return gy_quote(s); }

/* ------------------------------------------------------------------ */
/* String operations.                                                 */
/* ------------------------------------------------------------------ */

gy_str *gy_str_concat(const gy_str *a, const gy_str *b) {
    gy_str *out = gy_str_alloc(a->len + b->len);
    memcpy(out->bytes, a->bytes, (size_t)a->len);
    memcpy(out->bytes + a->len, b->bytes, (size_t)b->len);
    return out;
}

int64_t gy_str_eq(const gy_str *a, const gy_str *b) {
    return a->len == b->len && memcmp(a->bytes, b->bytes, (size_t)a->len) == 0;
}

/* Lexicographic by byte, -1/0/1, matching Go's strings.Compare: the
 * shorter string loses when it is a prefix of the longer one. */
int64_t gy_str_compare(const gy_str *a, const gy_str *b) {
    int64_t shared = a->len < b->len ? a->len : b->len;
    int order = memcmp(a->bytes, b->bytes, (size_t)shared);
    if (order != 0) {
        return order < 0 ? -1 : 1;
    }
    if (a->len == b->len) {
        return 0;
    }
    return a->len < b->len ? -1 : 1;
}

/* gy_str_bytes hands a string's UTF-8 out as a slice of u8, which is the
 * same bytes it already holds. It still copies: a string is immutable and
 * shared, and a slice is neither. gy_str_runes below decodes instead,
 * because a rune is four bytes and UTF-8 is not. */
void gy_str_bytes(void **out, const gy_str *s) {
    char *bytes = malloc((size_t)s->len + 1);
    memcpy(bytes, s->bytes, (size_t)s->len);
    out[0] = bytes;
    out[1] = (void *)s->len;
}

/* gy_from_utf8 is the other direction, and the bytes are the bytes. The
 * copy is what makes the result own what it holds. */
const gy_str *gy_from_utf8(const void **slice) {
    const char *elems = (const char *)slice[0];
    int64_t len = (int64_t)slice[1];
    gy_str *out = gy_str_alloc(len);
    memcpy(out->bytes, elems, (size_t)len);
    return out;
}

/* ------------------------------------------------------------------ */
/* The foreign boundary: marshalling for @extern.                     */
/* ------------------------------------------------------------------ */

/* One conversion is left here, and C's own representation forces it: a
 * Gyro string is length-prefixed where C wants a NUL, so it cannot be
 * lent out as it stands. The copy lives for the duration of the call and
 * gy_ffi_release ends it. A slice needs no copy at all — its elements are
 * already laid out the way C reads them — so gy_ffi_data lends the
 * pointer, and a foreign function that keeps it keeps a dangling one. */

/* The other direction, for a foreign function that returns text. The
 * bytes belong to the library and may be reused on its next call, so the
 * string Gyro sees is a copy; a null pointer becomes the empty string,
 * since a Gyro string is never absent. */
const gy_str *gy_ffi_from_cstr(const char *text) {
    return text ? gy_str_c(text) : gy_str_alloc(0);
}

void *gy_ffi_cstr(const gy_str *s) {
    char *out = malloc((size_t)s->len + 1);
    memcpy(out, s->bytes, (size_t)s->len);
    out[s->len] = '\0';
    return out;
}

/* The elements are the bytes C reads, so this is the slice's own data
 * pointer and nothing is copied or released. */
void *gy_ffi_data(const void **slice) { return (void *)slice[0]; }

void gy_ffi_release(void *buffer) { free(buffer); }

/* An opt<T> is { flag, payload } with flag 0 for null, and C's one
 * absent value is the null pointer, so the two convert both ways. */
void *gy_ffi_opt_handle(const void **opt) {
    return opt[0] ? (void *)opt[1] : NULL;
}

void gy_ffi_handle_opt(void *out, void *handle) {
    void **cell = out;
    cell[0] = (void *)(intptr_t)(handle != NULL);
    cell[1] = handle;
}

/* gy_str_runes decodes UTF-8 into a heap slice of code points, Go
 * semantics: an invalid sequence yields U+FFFD and consumes one byte. */
void gy_str_runes(void *out, const gy_str *s) {
    int32_t *runes = malloc((size_t)s->len * 4 + 4);
    int64_t count = 0;
    int64_t i = 0;
    while (i < s->len) {
        unsigned char c = (unsigned char)s->bytes[i];
        int64_t r = 0xFFFD;
        int size = 1;
        if (c < 0x80) {
            r = c;
        } else if ((c & 0xE0) == 0xC0 && i + 1 < s->len && (s->bytes[i + 1] & 0xC0) == 0x80) {
            r = ((int64_t)(c & 0x1F) << 6) | (s->bytes[i + 1] & 0x3F);
            size = 2;
            if (r < 0x80) { r = 0xFFFD; size = 1; }
        } else if ((c & 0xF0) == 0xE0 && i + 2 < s->len && (s->bytes[i + 1] & 0xC0) == 0x80 &&
                   (s->bytes[i + 2] & 0xC0) == 0x80) {
            r = ((int64_t)(c & 0x0F) << 12) | ((int64_t)(s->bytes[i + 1] & 0x3F) << 6) |
                (s->bytes[i + 2] & 0x3F);
            size = 3;
            if (r < 0x800 || (r >= 0xD800 && r <= 0xDFFF)) { r = 0xFFFD; size = 1; }
        } else if ((c & 0xF8) == 0xF0 && i + 3 < s->len && (s->bytes[i + 1] & 0xC0) == 0x80 &&
                   (s->bytes[i + 2] & 0xC0) == 0x80 && (s->bytes[i + 3] & 0xC0) == 0x80) {
            r = ((int64_t)(c & 0x07) << 18) | ((int64_t)(s->bytes[i + 1] & 0x3F) << 12) |
                ((int64_t)(s->bytes[i + 2] & 0x3F) << 6) | (s->bytes[i + 3] & 0x3F);
            size = 4;
            if (r < 0x10000 || r > 0x10FFFF) { r = 0xFFFD; size = 1; }
        }
        runes[count++] = (int32_t)r;
        i += size;
    }
    void **slice = out;
    slice[0] = runes;
    slice[1] = (void *)count;
}

/* ------------------------------------------------------------------ */
/* Derived hashing. The composites are folded by functions the         */
/* lowering synthesizes; these answer for the scalars underneath them, */
/* and must produce exactly what internal/interp/hash.go does.         */
/* ------------------------------------------------------------------ */

#define GY_FNV_OFFSET 14695981039346656037ULL
#define GY_FNV_PRIME 1099511628211ULL

/* The splitmix64 finalizer: an avalanche over one word, so small
 * integers do not land in neighbouring buckets. */
static uint64_t gy_hash_word(uint64_t x) {
    x ^= x >> 30;
    x *= 0xbf58476d1ce4e5b9ULL;
    x ^= x >> 27;
    x *= 0x94d049bb133111ebULL;
    x ^= x >> 31;
    return x;
}

uint64_t gy_hash_int(int64_t v) { return gy_hash_word((uint64_t)v); }

uint64_t gy_hash_uint(uint64_t v) { return gy_hash_word(v); }

/* -0.0 == 0.0, so the two have to hash alike. */
uint64_t gy_hash_float(double v) {
    if (v == 0.0) {
        v = 0.0;
    }
    uint64_t bits;
    memcpy(&bits, &v, sizeof bits);
    return gy_hash_word(bits);
}

uint64_t gy_hash_str(const gy_str *s) {
    uint64_t h = GY_FNV_OFFSET;
    for (int64_t i = 0; i < s->len; i++) {
        h = (h ^ (unsigned char)s->bytes[i]) * GY_FNV_PRIME;
    }
    return h;
}

/* ------------------------------------------------------------------ */
/* Conversions the code generator delegates.                          */
/* ------------------------------------------------------------------ */

double gy_uint_to_double(uint64_t v) { return (double)v; }

int64_t gy_double_to_int(double v, int64_t min, int64_t max) {
    double t = trunc(v);
    if (!(t >= -9223372036854775808.0 && t < 9223372036854775808.0)) {
        gy_abort_c("conversion loses the value");
    }
    int64_t out = (int64_t)t;
    if (out < min || out > max) {
        gy_abort_c("conversion loses the value");
    }
    return out;
}

uint64_t gy_double_to_uint(double v, uint64_t max) {
    double t = trunc(v);
    if (!(t >= 0.0 && t < 18446744073709551616.0)) {
        gy_abort_c("conversion loses the value");
    }
    uint64_t out = (uint64_t)t;
    if (out > max) {
        gy_abort_c("conversion loses the value");
    }
    return out;
}

/* ------------------------------------------------------------------ */
/* The byte floor a compiled-in language reaches i/o through. One byte */
/* at a time, because a linear address is a u32 and not a pointer:     */
/* handing the runtime one and telling it to read the bytes behind it  */
/* would make it a pointer at the boundary, which is the whole of what */
/* keeps the sandbox real. A libc buffers on its own side and drains   */
/* through these, so the call count is per byte of i/o and not per     */
/* formatted item.                                                     */
/* ------------------------------------------------------------------ */

void gy_write_byte(int64_t fd, int64_t byte) {
    fputc((int)(byte & 0xff), fd == 2 ? stderr : stdout);
}

int64_t gy_read_byte(void) {
    int c = fgetc(stdin);
    return c == EOF ? -1 : (int64_t)c;
}

/* ------------------------------------------------------------------ */
/* The float intrinsics whose C library spelling has the wrong        */
/* semantics. sqrt, fabs, ceil, floor, trunc and copysign mean in C   */
/* exactly what they mean in METALS, so both backends call libm for   */
/* those directly; these three do not, and live here so that the two  */
/* backends cannot answer them differently.                           */
/* ------------------------------------------------------------------ */

/* fmin and fmax in C return the operand that is not NaN, and say
 * nothing useful about the two zeros. METALS propagates NaN and orders
 * -0.0 below +0.0, which is what a program comparing bit patterns
 * needs and what wasm's f64.min and f64.max specify. */
double gy_f64_min(double a, double b) {
    if (a != a || b != b) return a + b; /* either NaN -> NaN */
    if (a == 0.0 && b == 0.0) return signbit(a) ? a : b;
    return a < b ? a : b;
}

double gy_f64_max(double a, double b) {
    if (a != a || b != b) return a + b;
    if (a == 0.0 && b == 0.0) return signbit(a) ? b : a;
    return a > b ? a : b;
}

/* roundToIntegralTiesToEven. C's round() is ties-away-from-zero and is
 * the wrong function; nearbyint honours the rounding mode, which is
 * round-to-nearest-even unless someone changed it, and nothing here
 * ever does. */
double gy_f64_nearest(double v) { return nearbyint(v); }

float gy_f32_min(float a, float b) {
    if (a != a || b != b) return a + b;
    if (a == 0.0f && b == 0.0f) return signbit(a) ? a : b;
    return a < b ? a : b;
}

float gy_f32_max(float a, float b) {
    if (a != a || b != b) return a + b;
    if (a == 0.0f && b == 0.0f) return signbit(a) ? b : a;
    return a > b ? a : b;
}

float gy_f32_nearest(float v) { return nearbyintf(v); }

/* ------------------------------------------------------------------ */
/* convert, fs, net. Result tuples are written into the caller's      */
/* cell: the error member is { flag, tag, payload... }                */
/* with tag 0, the closed error sum's Native(str) variant.            */
/* ------------------------------------------------------------------ */

static void gy_write_error(int64_t *err, gy_str *message) {
    err[0] = 1; /* opt flag: an error is present */
    err[1] = 0; /* variant 0: Native(str) */
    err[2] = (int64_t)message;
}

static gy_str *gy_trim_space(const gy_str *s) {
    int64_t from = 0;
    int64_t to = s->len;
    while (from < to && isspace((unsigned char)s->bytes[from])) from++;
    while (to > from && isspace((unsigned char)s->bytes[to - 1])) to--;
    return gy_str_from(s->bytes + from, (size_t)(to - from));
}

static gy_str *gy_concat3(const char *before, const gy_str *middle, const char *after) {
    gy_str *quoted = (gy_str *)middle;
    size_t beforeLen = strlen(before), afterLen = strlen(after);
    gy_str *out = gy_str_alloc((int64_t)(beforeLen + (size_t)quoted->len + afterLen));
    memcpy(out->bytes, before, beforeLen);
    memcpy(out->bytes + beforeLen, quoted->bytes, (size_t)quoted->len);
    memcpy(out->bytes + beforeLen + quoted->len, after, afterLen);
    return out;
}

/* out: tuple<i32, opt<error>> = { value, flag, tag, message } */
void gy_to_i32(int64_t *out, const gy_str *s) {
    gy_str *trimmed = gy_trim_space(s);
    char buf[64];
    int64_t parsed = 0;
    int ok = trimmed->len > 0 && trimmed->len < 63;
    if (ok) {
        memcpy(buf, trimmed->bytes, (size_t)trimmed->len);
        buf[trimmed->len] = 0;
        char *end = NULL;
        errno = 0;
        long long wide = strtoll(buf, &end, 10);
        ok = end == buf + trimmed->len && errno == 0 && wide >= INT32_MIN && wide <= INT32_MAX;
        parsed = (int64_t)wide;
    }
    if (!ok) {
        out[0] = 0;
        gy_write_error(out + 1, gy_concat3("", gy_quote(s), " is not an i32"));
        return;
    }
    out[0] = parsed;
    out[1] = 0;
}

/* out: tuple<f64, opt<error>> */
void gy_to_f64(int64_t *out, const gy_str *s) {
    gy_str *trimmed = gy_trim_space(s);
    char buf[128];
    double parsed = 0;
    int ok = trimmed->len > 0 && trimmed->len < 127;
    if (ok) {
        memcpy(buf, trimmed->bytes, (size_t)trimmed->len);
        buf[trimmed->len] = 0;
        char *end = NULL;
        errno = 0;
        parsed = strtod(buf, &end);
        ok = end == buf + trimmed->len && errno == 0;
    }
    if (!ok) {
        out[0] = 0;
        gy_write_error(out + 1, gy_concat3("", gy_quote(s), " is not an f64"));
        return;
    }
    memcpy(out, &parsed, 8);
    out[1] = 0;
}

/* Go's syscall error strings are strerror with a lowercase initial. */
static gy_str *gy_errno_str(int code) {
    char buf[128];
    snprintf(buf, sizeof buf, "%s", strerror(code));
    if (buf[0] >= 'A' && buf[0] <= 'Z') {
        buf[0] = (char)(buf[0] - 'A' + 'a');
    }
    return gy_str_c(buf);
}

typedef struct {
    gy_str *name;
    int closed;
} gy_file;

/* gy_cstr copies a str into a NUL-terminated buffer for libc paths. */
static char *gy_cstr(const gy_str *s) {
    char *out = malloc((size_t)s->len + 1);
    memcpy(out, s->bytes, (size_t)s->len);
    out[s->len] = 0;
    return out;
}

/* out: tuple<handle, opt<error>> = { handle, flag, tag, message } */
void gy_open(int64_t *out, const gy_str *path) {
    char *cpath = gy_cstr(path);
    struct stat info;
    if (stat(cpath, &info) != 0) {
        gy_str *reason = gy_errno_str(errno);
        gy_str *prefix = gy_concat3("stat ", path, ": ");
        out[0] = 0;
        gy_write_error(out + 1, gy_str_concat(prefix, reason));
        free(cpath);
        return;
    }
    free(cpath);
    gy_file *file = calloc(1, sizeof(gy_file));
    file->name = gy_str_from(path->bytes, (size_t)path->len);
    out[0] = (int64_t)file;
    out[1] = 0;
}

/* out: tuple<str, opt<error>> */
void gy_read_all(int64_t *out, gy_file *file) {
    char *cpath = gy_cstr(file->name);
    FILE *stream = fopen(cpath, "rb");
    if (stream == NULL) {
        gy_str *reason = gy_errno_str(errno);
        gy_str *prefix = gy_concat3("open ", file->name, ": ");
        out[0] = (int64_t)gy_str_c("");
        gy_write_error(out + 1, gy_str_concat(prefix, reason));
        free(cpath);
        return;
    }
    free(cpath);
    fseek(stream, 0, SEEK_END);
    long size = ftell(stream);
    fseek(stream, 0, SEEK_SET);
    gy_str *content = gy_str_alloc(size);
    if (size > 0 && fread(content->bytes, 1, (size_t)size, stream) != (size_t)size) {
        content->len = 0;
    }
    fclose(stream);
    out[0] = (int64_t)content;
    out[1] = 0;
}

void gy_file_close(gy_file *file) { file->closed = 1; }

/* gy_write_text opens, writes and closes in one call; the error strings
 * match Go's os package (open/write prefixes) byte for byte. */
static void gy_write_text(int64_t *out, const gy_str *path, const gy_str *text, const char *mode) {
    char *cpath = gy_cstr(path);
    FILE *stream = fopen(cpath, mode);
    free(cpath);
    if (stream == NULL) {
        gy_str *reason = gy_errno_str(errno);
        gy_str *prefix = gy_concat3("open ", path, ": ");
        gy_write_error(out, gy_str_concat(prefix, reason));
        return;
    }
    if (text->len > 0 && fwrite(text->bytes, 1, (size_t)text->len, stream) != (size_t)text->len) {
        gy_str *reason = gy_errno_str(errno);
        gy_str *prefix = gy_concat3("write ", path, ": ");
        gy_write_error(out, gy_str_concat(prefix, reason));
        fclose(stream);
        return;
    }
    fclose(stream);
    out[0] = 0;
}

/* out: opt<error> = { flag, tag, message } */
void gy_write_file(int64_t *out, const gy_str *path, const gy_str *text) {
    gy_write_text(out, path, text, "wb");
}

/* out: opt<error> */
void gy_append_file(int64_t *out, const gy_str *path, const gy_str *text) {
    gy_write_text(out, path, text, "ab");
}

/* out: opt<error> */
void gy_remove(int64_t *out, const gy_str *path) {
    char *cpath = gy_cstr(path);
    if (remove(cpath) != 0) {
        gy_str *reason = gy_errno_str(errno);
        gy_str *prefix = gy_concat3("remove ", path, ": ");
        gy_write_error(out, gy_str_concat(prefix, reason));
        free(cpath);
        return;
    }
    free(cpath);
    out[0] = 0;
}

int64_t gy_exists(const gy_str *path) {
    char *cpath = gy_cstr(path);
    struct stat info;
    int64_t found = stat(cpath, &info) == 0;
    free(cpath);
    return found;
}

/* ------------------------------------------------------------------ */
/* os: the environment, stdin, and the exit code.                     */
/* ------------------------------------------------------------------ */

/* out: opt<str> = { flag, value } — flag 0 when the variable is unset. */
void gy_env(int64_t *out, const gy_str *name) {
    char *cname = malloc((size_t)name->len + 1);
    memcpy(cname, name->bytes, (size_t)name->len);
    cname[name->len] = 0;
    const char *value = getenv(cname);
    free(cname);
    if (value == NULL) {
        out[0] = 0;
        out[1] = 0;
        return;
    }
    out[0] = 1;
    out[1] = (int64_t)gy_str_c(value);
}

/* Reads stdin to end of file. One shot: a second call finds the stream
 * drained and returns "". */
gy_str *gy_read_stdin(void) {
    size_t cap = 4096;
    size_t len = 0;
    char *buf = malloc(cap);
    size_t got;
    while ((got = fread(buf + len, 1, cap - len, stdin)) > 0) {
        len += got;
        if (len == cap) {
            cap *= 2;
            buf = realloc(buf, cap);
        }
    }
    gy_str *out = gy_str_from(buf, len);
    free(buf);
    return out;
}

_Noreturn void gy_exit(int64_t code) {
    fflush(stdout);
    fflush(stderr);
    exit((int)code);
}

/* out: tuple<str, opt<error>> */
void gy_get(int64_t *out, const gy_str *url) {
    (void)url;
    out[0] = (int64_t)gy_str_c("");
    gy_write_error(out + 1, gy_str_c("this runtime has no network"));
}

/* ------------------------------------------------------------------ */
/* Clocks. now() dates events on the wall clock; monotonic() measures */
/* them; sleep blocks only the calling thread — spawn runs pthreads,  */
/* so the rest of the program keeps going.                            */
/* ------------------------------------------------------------------ */

int64_t gy_now(void) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

int64_t gy_monotonic(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000000000 + ts.tv_nsec;
}

void gy_sleep(int64_t millis) {
    if (millis <= 0) {
        return;
    }
    struct timespec left = {millis / 1000, (millis % 1000) * 1000000};
    while (nanosleep(&left, &left) != 0 && errno == EINTR) {
    }
}

/* ------------------------------------------------------------------ */
/* Array<T>: a growable buffer handle. The element size arrives with  */
/* each element-touching call, statically known at the call site.     */
/* ------------------------------------------------------------------ */

typedef struct {
    char *data;
    int64_t elemSize;
    int64_t count;
    int64_t capacity;
    int freed;
} gy_array;

void *gy_array_new(void) { return calloc(1, sizeof(gy_array)); }

static void gy_array_check(gy_array *a) {
    if (a->freed) {
        gy_abort_c("use after free of an Array");
    }
}

void gy_array_push(gy_array *a, const void *elem, int64_t elemSize) {
    gy_array_check(a);
    a->elemSize = elemSize;
    if (a->count == a->capacity) {
        a->capacity = a->capacity == 0 ? 8 : a->capacity * 2;
        a->data = realloc(a->data, (size_t)(a->capacity * elemSize));
    }
    memcpy(a->data + a->count * elemSize, elem, (size_t)elemSize);
    a->count++;
}

/* out: tuple<T, bool> = { element, flag }. The flag sits at the element's
 * own width, which is where a C struct of the two would put a one-byte
 * member, and it is one byte wide: writing a word there would run past
 * the tuple. */
void gy_array_pop(char *out, gy_array *a, int64_t elemSize) {
    gy_array_check(a);
    if (a->count == 0) {
        memset(out, 0, (size_t)elemSize + 1);
        return;
    }
    a->count--;
    memcpy(out, a->data + a->count * elemSize, (size_t)elemSize);
    out[elemSize] = 1;
}

void gy_array_get(void *out, gy_array *a, int64_t index, int64_t elemSize) {
    gy_array_check(a);
    if (index < 0 || index >= a->count) {
        gy_abort_bounds(index, a->count);
    }
    memcpy(out, a->data + index * elemSize, (size_t)elemSize);
}

void gy_array_set(gy_array *a, int64_t index, const void *elem, int64_t elemSize) {
    gy_array_check(a);
    if (index < 0 || index >= a->count) {
        gy_abort_bounds(index, a->count);
    }
    memcpy(a->data + index * elemSize, elem, (size_t)elemSize);
}

/* The element's own address rather than a copy of it. Bounds-checked
 * the way get and set are, and the reason an index costs one call: the
 * caller loads or stores through the address where the element lies. */
void *gy_array_at(gy_array *a, int64_t index, int64_t elemSize) {
    gy_array_check(a);
    if (index < 0 || index >= a->count) {
        gy_abort_bounds(index, a->count);
    }
    return a->data + index * elemSize;
}

int64_t gy_array_len(gy_array *a) {
    gy_array_check(a);
    return a->count;
}

void gy_array_slice(void **out, gy_array *a) {
    gy_array_check(a);
    out[0] = a->data;
    out[1] = (void *)a->count;
}

void gy_array_free(gy_array *a) {
    if (a->freed) {
        gy_abort_c("double free of an Array");
    }
    a->freed = 1;
    free(a->data);
    a->data = NULL;
}

/* fs::read_dir lives below the Array block because its result is one.  */

/* gy_name_compare orders names bytewise, the way Go sorts a listing.   */
static int gy_name_compare(const void *a, const void *b) {
    gy_str *const *left = a;
    gy_str *const *right = b;
    int64_t shorter = (*left)->len < (*right)->len ? (*left)->len : (*right)->len;
    int order = memcmp((*left)->bytes, (*right)->bytes, (size_t)shorter);
    if (order != 0) {
        return order;
    }
    return ((*left)->len > (*right)->len) - ((*left)->len < (*right)->len);
}

/* out: tuple<handle, opt<error>> = { handle, flag, tag, message } */
void gy_read_dir(int64_t *out, const gy_str *path) {
    char *cpath = gy_cstr(path);
    DIR *dir = opendir(cpath);
    free(cpath);
    if (dir == NULL) {
        gy_str *reason = gy_errno_str(errno);
        gy_str *prefix = gy_concat3("open ", path, ": ");
        out[0] = (int64_t)gy_array_new();
        gy_write_error(out + 1, gy_str_concat(prefix, reason));
        return;
    }

    size_t count = 0, capacity = 0;
    gy_str **names = NULL;
    struct dirent *entry;
    while ((entry = readdir(dir)) != NULL) {
        if (strcmp(entry->d_name, ".") == 0 || strcmp(entry->d_name, "..") == 0) {
            continue;
        }
        if (count == capacity) {
            capacity = capacity == 0 ? 16 : capacity * 2;
            names = realloc(names, capacity * sizeof(gy_str *));
        }
        names[count++] = gy_str_c(entry->d_name);
    }
    closedir(dir);
    if (count > 0) {
        qsort(names, count, sizeof(gy_str *), gy_name_compare);
    }

    gy_array *entries = gy_array_new();
    for (size_t i = 0; i < count; i++) {
        gy_array_push(entries, &names[i], 8);
    }
    free(names);
    out[0] = (int64_t)entries;
    out[1] = 0;
}

/* ------------------------------------------------------------------ */
/* Mutex<T> and its guard.                                            */
/* ------------------------------------------------------------------ */

typedef struct {
    pthread_mutex_t mutex;
    char *value;
    int64_t size;
} gy_mutex;

typedef struct {
    gy_mutex *mutex;
    int done;
} gy_guard;

void *gy_mutex_new(const void *init, int64_t size) {
    gy_mutex *m = calloc(1, sizeof(gy_mutex));
    pthread_mutex_init(&m->mutex, NULL);
    m->size = size;
    m->value = malloc((size_t)size);
    memcpy(m->value, init, (size_t)size);
    return m;
}

void *gy_mutex_lock(gy_mutex *m) {
    pthread_mutex_lock(&m->mutex);
    gy_guard *guard = calloc(1, sizeof(gy_guard));
    guard->mutex = m;
    return guard;
}

static void gy_guard_check(gy_guard *guard) {
    if (guard->done) {
        gy_abort_c("the guard was already unlocked");
    }
}

void gy_guard_get(void *out, gy_guard *guard) {
    gy_guard_check(guard);
    memcpy(out, guard->mutex->value, (size_t)guard->mutex->size);
}

void gy_guard_set(gy_guard *guard, const void *value) {
    gy_guard_check(guard);
    memcpy(guard->mutex->value, value, (size_t)guard->mutex->size);
}

void gy_guard_unlock(gy_guard *guard) {
    gy_guard_check(guard);
    guard->done = 1;
    pthread_mutex_unlock(&guard->mutex->mutex);
}

/* ------------------------------------------------------------------ */
/* Channels: a mutex/condvar ring buffer; capacity 0 is a true        */
/* rendezvous — the sender blocks until a receiver takes the element. */
/* ------------------------------------------------------------------ */

typedef struct {
    pthread_mutex_t mutex;
    pthread_cond_t senders;
    pthread_cond_t receivers;
    char *buffer;
    int64_t elemSize;
    int64_t capacity;
    int64_t count;
    int64_t head;
    int closed;
    int handoffFull; /* rendezvous slot state */
    int receiversWaiting;
    int64_t placedSeq; /* rendezvous tickets: which handoff was taken */
    int64_t takenSeq;
} gy_chan;

void *gy_chan_new(int64_t capacity, int64_t elemSize) {
    gy_chan *ch = calloc(1, sizeof(gy_chan));
    pthread_mutex_init(&ch->mutex, NULL);
    pthread_cond_init(&ch->senders, NULL);
    pthread_cond_init(&ch->receivers, NULL);
    ch->elemSize = elemSize;
    ch->capacity = capacity;
    ch->buffer = malloc((size_t)((capacity > 0 ? capacity : 1) * elemSize));
    return ch;
}

void gy_chan_send(gy_chan *ch, const void *elem) {
    pthread_mutex_lock(&ch->mutex);
    if (ch->capacity > 0) {
        while (ch->count == ch->capacity && !ch->closed) {
            pthread_cond_wait(&ch->senders, &ch->mutex);
        }
        if (ch->closed) {
            pthread_mutex_unlock(&ch->mutex);
            gy_abort_c("send on a closed channel");
        }
        int64_t at = (ch->head + ch->count) % ch->capacity;
        memcpy(ch->buffer + at * ch->elemSize, elem, (size_t)ch->elemSize);
        ch->count++;
        pthread_cond_signal(&ch->receivers);
        pthread_mutex_unlock(&ch->mutex);
        return;
    }

    while (ch->handoffFull && !ch->closed) {
        pthread_cond_wait(&ch->senders, &ch->mutex);
    }
    if (ch->closed) {
        pthread_mutex_unlock(&ch->mutex);
        gy_abort_c("send on a closed channel");
    }
    memcpy(ch->buffer, elem, (size_t)ch->elemSize);
    ch->handoffFull = 1;
    int64_t ticket = ++ch->placedSeq;
    pthread_cond_broadcast(&ch->receivers);
    while (ch->takenSeq < ticket && !ch->closed) {
        pthread_cond_wait(&ch->senders, &ch->mutex);
    }
    if (ch->takenSeq < ticket) {
        pthread_mutex_unlock(&ch->mutex);
        gy_abort_c("send on a closed channel");
    }
    pthread_mutex_unlock(&ch->mutex);
}

/* Returns the open flag: 1 with the element written, 0 when the
 * channel is closed and drained (the caller pre-zeroed the cell). */
int64_t gy_chan_recv(gy_chan *ch, void *out) {
    pthread_mutex_lock(&ch->mutex);
    if (ch->capacity > 0) {
        while (ch->count == 0 && !ch->closed) {
            pthread_cond_wait(&ch->receivers, &ch->mutex);
        }
        if (ch->count == 0) {
            pthread_mutex_unlock(&ch->mutex);
            return 0;
        }
        memcpy(out, ch->buffer + ch->head * ch->elemSize, (size_t)ch->elemSize);
        ch->head = (ch->head + 1) % ch->capacity;
        ch->count--;
        pthread_cond_signal(&ch->senders);
        pthread_mutex_unlock(&ch->mutex);
        return 1;
    }

    ch->receiversWaiting++;
    while (!ch->handoffFull && !ch->closed) {
        pthread_cond_wait(&ch->receivers, &ch->mutex);
    }
    ch->receiversWaiting--;
    if (!ch->handoffFull) {
        pthread_mutex_unlock(&ch->mutex);
        return 0;
    }
    memcpy(out, ch->buffer, (size_t)ch->elemSize);
    ch->handoffFull = 0;
    ch->takenSeq++;
    pthread_cond_broadcast(&ch->senders);
    pthread_mutex_unlock(&ch->mutex);
    return 1;
}

void gy_chan_close(gy_chan *ch) {
    pthread_mutex_lock(&ch->mutex);
    if (ch->closed) {
        pthread_mutex_unlock(&ch->mutex);
        gy_abort_c("close of a closed channel");
    }
    ch->closed = 1;
    pthread_cond_broadcast(&ch->senders);
    pthread_cond_broadcast(&ch->receivers);
    pthread_mutex_unlock(&ch->mutex);
}

int64_t gy_chan_try_send(gy_chan *ch, const void *elem) {
    pthread_mutex_lock(&ch->mutex);
    if (ch->closed) {
        pthread_mutex_unlock(&ch->mutex);
        gy_abort_c("send on a closed channel");
    }
    if (ch->capacity > 0) {
        if (ch->count == ch->capacity) {
            pthread_mutex_unlock(&ch->mutex);
            return 0;
        }
        int64_t at = (ch->head + ch->count) % ch->capacity;
        memcpy(ch->buffer + at * ch->elemSize, elem, (size_t)ch->elemSize);
        ch->count++;
        pthread_cond_signal(&ch->receivers);
        pthread_mutex_unlock(&ch->mutex);
        return 1;
    }
    if (ch->receiversWaiting == 0 || ch->handoffFull) {
        pthread_mutex_unlock(&ch->mutex);
        return 0;
    }
    memcpy(ch->buffer, elem, (size_t)ch->elemSize);
    ch->handoffFull = 1;
    ch->placedSeq++;
    pthread_cond_broadcast(&ch->receivers);
    pthread_mutex_unlock(&ch->mutex);
    return 1;
}

int64_t gy_chan_try_recv(gy_chan *ch, void *out) {
    pthread_mutex_lock(&ch->mutex);
    if (ch->capacity > 0) {
        if (ch->count > 0) {
            memcpy(out, ch->buffer + ch->head * ch->elemSize, (size_t)ch->elemSize);
            ch->head = (ch->head + 1) % ch->capacity;
            ch->count--;
            pthread_cond_signal(&ch->senders);
            pthread_mutex_unlock(&ch->mutex);
            return 1;
        }
        int64_t fired = ch->closed;
        pthread_mutex_unlock(&ch->mutex);
        return fired;
    }
    if (ch->handoffFull) {
        memcpy(out, ch->buffer, (size_t)ch->elemSize);
        ch->handoffFull = 0;
        ch->takenSeq++;
        pthread_cond_broadcast(&ch->senders);
        pthread_mutex_unlock(&ch->mutex);
        return 1;
    }
    int64_t fired = ch->closed;
    pthread_mutex_unlock(&ch->mutex);
    return fired;
}

void gy_select_pause(void) { usleep(200); }

/* ------------------------------------------------------------------ */
/* spawn and futures.                                                 */
/* ------------------------------------------------------------------ */

typedef struct {
    void (*bare)(void);
    void (*withEnv)(void *);
    void *env;
} gy_spawn_arg;

static void *gy_spawn_main(void *raw) {
    gy_spawn_arg arg = *(gy_spawn_arg *)raw;
    free(raw);
    gy_on_worker_thread = 1;
    if (arg.env != NULL) {
        arg.withEnv(arg.env);
    } else {
        arg.bare();
    }
    return NULL;
}

/* fnval points at a { code, env } function value. */
void gy_spawn(void **fnval) {
    gy_spawn_arg *arg = calloc(1, sizeof(gy_spawn_arg));
    arg->bare = (void (*)(void))fnval[0];
    arg->withEnv = (void (*)(void *))fnval[0];
    arg->env = fnval[1];
    pthread_t thread;
    pthread_attr_t attrs;
    pthread_attr_init(&attrs);
    pthread_attr_setdetachstate(&attrs, PTHREAD_CREATE_DETACHED);
    pthread_create(&thread, &attrs, gy_spawn_main, arg);
    pthread_attr_destroy(&attrs);
}

struct gy_future {
    pthread_mutex_t mutex;
    pthread_cond_t cond;
    int done;
    int64_t size;
    char *result;
};

static void gy_future_fulfill(gy_future *fut) {
    pthread_mutex_lock(&fut->mutex);
    fut->done = 1;
    pthread_cond_broadcast(&fut->cond);
    pthread_mutex_unlock(&fut->mutex);
}

typedef struct {
    void (*thunk)(void *, void *);
    void *args;
    gy_future *fut;
} gy_future_arg;

static void *gy_future_main(void *raw) {
    gy_future_arg arg = *(gy_future_arg *)raw;
    free(raw);
    gy_on_worker_thread = 1;
    gy_current_future = arg.fut;
    arg.thunk(arg.args, arg.fut->result);
    gy_current_future = NULL;
    gy_future_fulfill(arg.fut);
    return NULL;
}

void *gy_future_start(void (*thunk)(void *, void *), void *args, int64_t resultSize) {
    gy_future *fut = calloc(1, sizeof(gy_future));
    pthread_mutex_init(&fut->mutex, NULL);
    pthread_cond_init(&fut->cond, NULL);
    fut->size = resultSize;
    fut->result = calloc(1, (size_t)(resultSize > 0 ? resultSize : 8));

    gy_future_arg *arg = calloc(1, sizeof(gy_future_arg));
    arg->thunk = thunk;
    arg->args = args;
    arg->fut = fut;
    pthread_t thread;
    pthread_attr_t attrs;
    pthread_attr_init(&attrs);
    pthread_attr_setdetachstate(&attrs, PTHREAD_CREATE_DETACHED);
    pthread_create(&thread, &attrs, gy_future_main, arg);
    pthread_attr_destroy(&attrs);
    return fut;
}

void gy_future_get(gy_future *fut, void *out) {
    pthread_mutex_lock(&fut->mutex);
    while (!fut->done) {
        pthread_cond_wait(&fut->cond, &fut->mutex);
    }
    pthread_mutex_unlock(&fut->mutex);
    memcpy(out, fut->result, (size_t)fut->size);
}

/* ------------------------------------------------------------------ */
/* any: the open dynamic value.                                       */
/*                                                                    */
/* Two words — a descriptor and a payload. A scalar rides in the      */
/* payload, so wrapping one allocates nothing and testing one is a    */
/* pointer comparison; that is what keeps `any` from costing what an  */
/* interpreter's value costs. Anything wider is heap storage the      */
/* payload points at, and so is every record whatever its width, so   */
/* that any.set through one reference is visible through the rest.    */
/*                                                                    */
/* Descriptors are emitted one per distinct type by the code          */
/* generator and never duplicated, so identity is structural equality */
/* and `any.is` is `a->type == b`.                                    */
/* ------------------------------------------------------------------ */

typedef struct gy_type gy_type;
typedef struct { void (*code)(void); void *env; } gy_fn;
typedef struct { const gy_type *type; uint64_t payload; } gy_any;

/* A field of a record an any holds. type is NULL when the field is
 * itself an any, which needs no boxing on the way out. */
typedef struct {
    const char *name;
    int64_t offset;
    const gy_type *type;
} gy_field;

struct gy_type {
    const char *name;   /* the METALS spelling, for messages */
    int64_t size;       /* bytes of the wrapped value */
    int64_t boxed;      /* 1 when payload points at heap storage */
    const gy_field *fields;
    int64_t nfields;
    /* apply unwraps arguments to the wrapped function's declared
     * parameter types, calls it and boxes the result. The code
     * generator emits one per function type it wraps. */
    void (*apply)(gy_any *out, gy_fn fn, const gy_any *args, int64_t n);
};

static _Noreturn void gy_any_fail(const char *what, const char *detail,
                                  const char *a, const char *b) {
    char buffer[512];
    if (b != NULL) {
        snprintf(buffer, sizeof buffer, "%s: %s%s, not %s", what, detail, a, b);
    } else {
        snprintf(buffer, sizeof buffer, "%s%s%s", what, detail, a);
    }
    gy_abort(gy_str_c(buffer));
}

gy_any gy_any_box(const gy_type *type, const void *value) {
    gy_any out;
    out.type = type;
    out.payload = 0;
    if (type->boxed) {
        void *cell = gy_alloc(type->size);
        memcpy(cell, value, (size_t)type->size);
        out.payload = (uint64_t)(uintptr_t)cell;
    } else if (type->size > 0) {
        memcpy(&out.payload, value, (size_t)type->size);
    }
    return out;
}

static void *gy_any_cell(const gy_any *v) {
    if (v->type->boxed) {
        return (void *)(uintptr_t)v->payload;
    }
    return (void *)&v->payload;
}

int64_t gy_any_is(const gy_any *v, const gy_type *want) {
    return v->type == want ? 1 : 0;
}

void gy_any_read(void *out, const gy_any *v, const gy_type *want) {
    if (v->type != want) {
        gy_any_fail("any.unwrap", "the any holds ", v->type->name, want->name);
    }
    if (want->size > 0) {
        memcpy(out, gy_any_cell(v), (size_t)want->size);
    }
}

static const gy_field *gy_any_field(const gy_any *v, const char *name, const char *what) {
    if (v->type->nfields == 0) {
        gy_any_fail(what, " on an any holding ", v->type->name, NULL);
    }
    for (int64_t i = 0; i < v->type->nfields; i++) {
        if (strcmp(v->type->fields[i].name, name) == 0) {
            return &v->type->fields[i];
        }
    }
    gy_any_fail(what, ": no field named ", name, NULL);
}

gy_any gy_any_get(const gy_any *v, const char *name) {
    const gy_field *field = gy_any_field(v, name, "any.get");
    char *base = (char *)gy_any_cell(v) + field->offset;
    if (field->type == NULL) {
        gy_any held;
        memcpy(&held, base, sizeof held);
        return held;
    }
    return gy_any_box(field->type, base);
}

void gy_any_set(const gy_any *v, const char *name, const gy_any *value) {
    const gy_field *field = gy_any_field(v, name, "any.set");
    char *base = (char *)gy_any_cell(v) + field->offset;
    if (field->type == NULL) {
        memcpy(base, value, sizeof *value);
        return;
    }
    if (value->type != field->type) {
        gy_any_fail("any.set", "the any holds ", value->type->name, field->type->name);
    }
    if (field->type->size > 0) {
        memcpy(base, gy_any_cell(value), (size_t)field->type->size);
    }
}

void gy_any_call(gy_any *out, const gy_any *v, const gy_any *args, int64_t n) {
    if (v->type->apply == NULL) {
        gy_any_fail("any.call", " on an any holding ", v->type->name, NULL);
    }
    gy_fn fn;
    memcpy(&fn, gy_any_cell(v), sizeof fn);
    v->type->apply(out, fn, args, n);
}

_Noreturn void gy_any_arity(const gy_type *type, int64_t got) {
    char buffer[256];
    snprintf(buffer, sizeof buffer, "any.call passes %" PRId64 " argument(s) to %s",
             got, type->name);
    gy_abort(gy_str_c(buffer));
}

/* ------------------------------------------------------------------ */
/* Entry point.                                                       */
/* ------------------------------------------------------------------ */

/* fn main may take the command line. The slice is always built and always
 * passed; a main that declares no parameters simply ignores the register,
 * which costs one argv walk and keeps the entry point one symbol.
 * argv[0] is included: a program may want the name it was invoked as. */
extern void gy_user_main(void *args);

int main(int argc, char **argv) {
    void **elems = malloc((size_t)argc * 8 + 8);
    for (int i = 0; i < argc; i++) {
        elems[i] = (void *)gy_str_c(argv[i]);
    }
    void *slice[2] = {elems, (void *)(int64_t)argc};

    gy_user_main(slice);
    fflush(stdout);
    fflush(stderr);
    return 0;
}
