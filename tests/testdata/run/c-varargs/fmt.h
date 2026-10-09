/* A printf, which is what varargs is for.
 *
 * Every name here is a scalar, because a scalar is what Gyro can import.
 * The variadic functions are internal: a `...` says nothing about what a
 * call passes, so nothing on the other side of a boundary could check
 * one — which is also true of C, where a variadic prototype is a promise
 * the compiler cannot keep.
 *
 * The rendered text stays in a buffer here and is read back a byte at a
 * time, so this library needs no allocator and no way to hand out a
 * string. */

/* Formatting calls. Each runs one render and answers its length. */
int render_mixed(void);
int render_signs(void);
int render_percent(void);
int render_nothing(void);

/* One byte of whatever was rendered last. */
int rendered_at(int index);

/* A variadic sum, called with three different argument counts, so the
 * count is per call and not per function. */
int sum_of_none(void);
int sum_of_two(int a, int b);
int sum_of_five(void);

/* The vprintf pattern: a va_list handed to another function, which
 * advances the cursor the caller declared. */
int forwarded(int a, int b);

/* va_copy: two walks over one call's arguments. */
int walked_twice(int x);

/* The default argument promotions. A char becomes an int and a float
 * becomes a double before either reaches the callee. */
int promoted(void);

/* A `void *` through a `...`, which is where the two features meet: the
 * pack is already an array of erased addresses, so an erased address
 * travels in it like anything else. */
int through_a_pointer(void);
