#ifndef _MATH_H
#define _MATH_H

/* Every name here is a METALS instruction underneath: wasm has these as
 * opcodes, so clang emits the opcode and there is nothing to link. They
 * are written as calls to the builtins rather than as C so that the
 * -fno-builtin the bridge compiles with does not turn a call to `sqrt`
 * into a call to a `sqrt` that would then have to be a real function.
 *
 * fmin and fmax are the exception. C says they return the operand that
 * is not NaN; wasm's f64.min and f64.max propagate it. These follow C,
 * because this is a C library — a program that wants the wasm rule can
 * write the comparison itself. */

#define NAN __builtin_nanf("")
#define INFINITY __builtin_inff()
#define HUGE_VAL __builtin_inf()
#define HUGE_VALF __builtin_inff()

#define isnan(v) __builtin_isnan(v)
#define isinf(v) __builtin_isinf(v)
#define isfinite(v) __builtin_isfinite(v)
#define signbit(v) __builtin_signbit(v)

static inline double sqrt(double v) { return __builtin_sqrt(v); }
static inline double fabs(double v) { return __builtin_fabs(v); }
static inline double ceil(double v) { return __builtin_ceil(v); }
static inline double floor(double v) { return __builtin_floor(v); }
static inline double trunc(double v) { return __builtin_trunc(v); }
static inline double nearbyint(double v) { return __builtin_nearbyint(v); }
static inline double rint(double v) { return __builtin_rint(v); }
static inline double copysign(double x, double y) { return __builtin_copysign(x, y); }

static inline float sqrtf(float v) { return __builtin_sqrtf(v); }
static inline float fabsf(float v) { return __builtin_fabsf(v); }
static inline float ceilf(float v) { return __builtin_ceilf(v); }
static inline float floorf(float v) { return __builtin_floorf(v); }
static inline float truncf(float v) { return __builtin_truncf(v); }
static inline float nearbyintf(float v) { return __builtin_nearbyintf(v); }
static inline float copysignf(float x, float y) { return __builtin_copysignf(x, y); }

/* C's rule: a NaN operand loses. */
static inline double fmin(double x, double y) {
    if (__builtin_isnan(x)) return y;
    if (__builtin_isnan(y)) return x;
    return x < y ? x : y;
}

static inline double fmax(double x, double y) {
    if (__builtin_isnan(x)) return y;
    if (__builtin_isnan(y)) return x;
    return x > y ? x : y;
}

/* round is ties-away-from-zero, which is C's rule and not the one
 * `nearbyint` follows. It is written out because there is no opcode for
 * it: wasm's f64.nearest is ties-to-even. */
static inline double round(double v) {
    double away = __builtin_fabs(v) + 0.5;
    double whole = __builtin_floor(away);
    return __builtin_copysign(whole, v);
}

#endif
