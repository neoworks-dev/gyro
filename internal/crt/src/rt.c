/* gyro:rt — what the generated code still asks the C runtime for. Compiled
 * and linked by `gyro build`. The standard library is Gyro now: this is the
 * floor under it that no METALS instruction reaches, and the one place that
 * starts a thread, with libc's pthreads. Spawning, channels, mutexes and
 * futures are Gyro over the thread instructions, the atomics and futex. */

#define _GNU_SOURCE
#include <ctype.h>
#include <errno.h>
#include <inttypes.h>
#include <math.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
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
/* on a spawned thread only that thread dies, which the abort hook    */
/* Gyro registers sees to.                                            */
/* ------------------------------------------------------------------ */

/* The Gyro output layer buffers standard output and registers what writes
 * it out here, the first time it prints. The program ending, a panic, and
 * a call into a C library are the points where the C runtime asks for it. */
void (*gy_flush_hook)(void) = NULL;

void gy_set_flush_hook(void (*hook)(void)) { gy_flush_hook = hook; }

/* What to do after a panic is printed, set by the Gyro runtime in mem. It
 * returns when the panic is the process's to end, and does not return when
 * it has ended a worker thread instead: which of the two is the state of the
 * thread, and that is Gyro's. */
static void (*gy_abort_hook)(void) = NULL;

void gy_set_abort_hook(void (*hook)(void)) { gy_abort_hook = hook; }

_Noreturn void gy_abort(const gy_str *message) {
    if (gy_flush_hook != NULL) {
        gy_flush_hook();
    }
    fprintf(stderr, "panic: %.*s\n", (int)message->len, message->bytes);
    if (gy_abort_hook != NULL) {
        gy_abort_hook();
    }
    exit(1);
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

/* The process heap as an allocator sees it: the floor mem's heap() wraps
 * into an Allocator, and what every other allocator bottoms out in. A
 * block is zero-filled, which is the allocator contract, and the C
 * allocator's alignment (16 on this ABI) covers every alignment a Gyro
 * type can ask for. Null answers a request that cannot be met; alloc<T>
 * turns that into a panic naming the size. */
void *gy_raw_alloc(int64_t size, int64_t align) {
    (void)align;
    return calloc(1, size > 0 ? (size_t)size : 1);
}

void gy_raw_free(void *p) { free(p); }

void *gy_raw_resize(void *p, int64_t oldSize, int64_t size, int64_t align) {
    (void)align;
    char *grown = realloc(p, size > 0 ? (size_t)size : 1);
    if (grown != NULL && size > oldSize) {
        memset(grown + oldSize, 0, (size_t)(size - oldSize));
    }
    return grown;
}

void gy_copy_bytes(void *to, const void *from, int64_t bytes) {
    memmove(to, from, (size_t)bytes);
}

/* A string is a { len, bytes } header followed by its bytes in one block,
 * and sealing a block as one is the one step Gyro cannot take itself: it
 * has no way to make a string of an address. */
const gy_str *gy_text_seal(void *block, int64_t len) {
    gy_str *s = block;
    s->len = len;
    return s;
}

/* panic ends the process with the message, the way an index out of range
 * does: the same abort, reached from Gyro. */
void gy_panic(const gy_str *message) { gy_abort(message); }

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

/* ------------------------------------------------------------------ */
/* The printing half of gyro:rt, for METALS that is not Gyro: a producer */
/* that cannot link Gyro's own stdlib prints through these. A Gyro      */
/* program never reaches them; it prints through stdlib's buffered      */
/* leaves, and these write what that buffer holds out first.            */
/* ------------------------------------------------------------------ */

/* Recursive, because a format value holds it across all its pieces and a
   piece's to_string may print. The Gyro output layer takes the same lock. */
static pthread_mutex_t gy_print_mutex = PTHREAD_RECURSIVE_MUTEX_INITIALIZER_NP;

void gy_lock_output(void) { pthread_mutex_lock(&gy_print_mutex); }
void gy_unlock_output(void) { pthread_mutex_unlock(&gy_print_mutex); }

static void gy_write(FILE *stream, const char *bytes, size_t len, int newline) {
    pthread_mutex_lock(&gy_print_mutex);
    if (gy_flush_hook != NULL) {
        gy_flush_hook();
    }
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
    void gy_eprint_##suffix(type v) {                                          \
        char buf[64];                                                          \
        gy_render_##suffix(v, buf, sizeof buf);                                \
        gy_print_dispatch(stderr, 0, buf, strlen(buf));                        \
    }                                                                          \
    void gy_eprintln_##suffix(type v) {                                        \
        char buf[64];                                                          \
        gy_render_##suffix(v, buf, sizeof buf);                                \
        gy_print_dispatch(stderr, 1, buf, strlen(buf));                        \
    }

GY_PRINT_FAMILY(int, int64_t, snprintf(buf, cap, "%" PRId64, v))
GY_PRINT_FAMILY(uint, uint64_t, snprintf(buf, cap, "%" PRIu64, v))
GY_PRINT_FAMILY(float, double, gy_format_float(v, buf, cap))
GY_PRINT_FAMILY(bool, int64_t, snprintf(buf, cap, "%s", v ? "true" : "false"))
GY_PRINT_FAMILY(rune, int64_t, buf[gy_encode_rune(v, buf)] = 0)

void gy_print_str(const gy_str *s) { gy_print_dispatch(stdout, 0, s->bytes, (size_t)s->len); }
void gy_println_str(const gy_str *s) { gy_print_dispatch(stdout, 1, s->bytes, (size_t)s->len); }
void gy_eprint_str(const gy_str *s) { gy_print_dispatch(stderr, 0, s->bytes, (size_t)s->len); }
void gy_eprintln_str(const gy_str *s) { gy_print_dispatch(stderr, 1, s->bytes, (size_t)s->len); }

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

/* gy_str_view is a slice over the string's
 * own bytes, for a function that only reads them. The view borrows the
 * string, which is immutable, so nothing can write through it. */
void gy_str_view(void **out, const gy_str *s) {
    out[0] = (void *)s->bytes;
    out[1] = (void *)s->len;
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
/* Threads: the one place that knows how one begins and ends.         */
/* ------------------------------------------------------------------ */

/* thread.start and thread.exit. The backend implements the METALS
 * instructions with libc's threads for as long as the program links libc
 * (#142). The stack the instruction offers is ignored: libc gives every
 * thread its own. The body is called through a pointer to a function
 * returning void *, which the generated code is ABI-compatible with; its
 * return value is ignored. */
uint64_t gy_thread_start(void (*body)(void *), void *data) {
    pthread_t thread;
    pthread_attr_t attrs;
    pthread_attr_init(&attrs);
    pthread_attr_setdetachstate(&attrs, PTHREAD_CREATE_DETACHED);
    int status = pthread_create(&thread, &attrs, (void *(*)(void *))body, data);
    pthread_attr_destroy(&attrs);
    if (status != 0) {
        gy_abort_c("could not start a thread");
    }
    return (uint64_t)thread;
}

_Noreturn void gy_thread_exit(void) { pthread_exit(NULL); }

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
    /* What a C library prints goes out as it is printed, so that it falls
     * between what Gyro prints in the order the program ran them. */
    setvbuf(stdout, NULL, _IONBF, 0);
    void **elems = malloc((size_t)argc * 8 + 8);
    for (int i = 0; i < argc; i++) {
        elems[i] = (void *)gy_str_c(argv[i]);
    }
    void *slice[2] = {elems, (void *)(int64_t)argc};

    gy_user_main(slice);
    if (gy_flush_hook != NULL) {
        gy_flush_hook();
    }
    return 0;
}
