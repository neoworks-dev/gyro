// The float intrinsics through the bridge, and the single-precision
// rounding they sit on.
//
// wasm has abs, ceil, floor, trunc, nearest, sqrt, min, max and copysign
// as opcodes, so clang emits them inline and there is nothing to link;
// each is a METALS instruction for the same reason.
//
// min and max are absent here on purpose. C's fmin and fmax return the
// operand that is not NaN, where wasm's f64.min and f64.max propagate
// it, so clang emits a call rather than the opcode — correctly. Those
// two are pinned in float-ops.metals, where the instruction can be
// written directly, which is the honest place for an opcode C cannot
// ask for.
//
// Everything printed is an integer, a flag or a sign, so the golden
// follows from IEEE-754 rather than from what some tier happened to say.

__attribute__((import_module("gyro:rt"), import_name("println<i64>")))
extern void put(long long v);

// volatile keeps clang from folding the whole file at compile time,
// which would test its constant folder instead of the backend.
volatile double d_half = 0.5, d_1half = 1.5, d_2half = 2.5, d_3half = 3.5;
volatile double d_neghalf = -0.5, d_neg35 = -3.5, d_two = 2.0, d_nine = 9.0;
volatile double d_zero = 0.0, d_one = 1.0, d_negzero = -0.0;
volatile float f_big = 1e20f, f_quarter = 0.25f;

static int sign_of(double v) { return __builtin_signbit(v) ? 1 : 0; }

int main(void) {
    // sqrt of exact squares, so the answer is exact.
    put((long long)__builtin_sqrt(d_nine));
    put((long long)__builtin_sqrt(d_zero));
    put(__builtin_isnan(__builtin_sqrt(-d_one)) ? 1 : 0);

    put((long long)__builtin_fabs(d_neg35 * d_two)); // |-7.0|

    // The four roundings of -3.5, which disagree with each other.
    put((long long)__builtin_ceil(d_neg35));         // -3
    put((long long)__builtin_floor(d_neg35));        // -4
    put((long long)__builtin_trunc(d_neg35));        // -3
    put((long long)__builtin_nearbyint(d_neg35));    // -4, ties to even

    // Ties to even, not away from zero: 0.5 -> 0, 1.5 -> 2, 2.5 -> 2,
    // 3.5 -> 4. C's round() would answer 1, 2, 3, 4.
    put((long long)__builtin_nearbyint(d_half));
    put((long long)__builtin_nearbyint(d_1half));
    put((long long)__builtin_nearbyint(d_2half));
    put((long long)__builtin_nearbyint(d_3half));

    // -0.5 rounds to -0.0, which is zero with the sign still on it.
    put((long long)__builtin_nearbyint(d_neghalf));
    put(sign_of(__builtin_nearbyint(d_neghalf)));

    // A NaN survives the roundings rather than becoming a number.
    double nan = d_zero / d_zero;
    put(__builtin_isnan(__builtin_ceil(nan)) ? 1 : 0);
    put(__builtin_isnan(__builtin_nearbyint(nan)) ? 1 : 0);

    // An infinity rounds to itself, sign kept.
    double infinity = d_one / d_zero;
    put(__builtin_isinf(__builtin_floor(infinity)) ? 1 : 0);
    put(sign_of(__builtin_trunc(-infinity)));

    // -0.0 keeps its sign through abs's neighbours.
    put(sign_of(__builtin_ceil(d_negzero)));
    put(sign_of(__builtin_fabs(d_negzero)));

    put((long long)__builtin_copysign(d_nine, -d_one));
    put(sign_of(__builtin_copysign(d_nine, d_negzero)));

    // Single precision, where the register is a double underneath. 1e20f
    // squared overflows the f32 range, so it is +Inf — and dividing that
    // by 1e20f is still +Inf. Computing at double precision instead would
    // give a finite 1e20 back, which is the bug this pins.
    float big = f_big * f_big;
    put(__builtin_isinf(big) ? 1 : 0);
    put(__builtin_isinf(big / f_big) ? 1 : 0);

    // f32 sqrt of an exact square is exact at either width, so this
    // checks the call and not the rounding.
    put((long long)(__builtin_sqrtf(f_quarter) * 4.0f)); // sqrt(0.25)=0.5
    return 0;
}
