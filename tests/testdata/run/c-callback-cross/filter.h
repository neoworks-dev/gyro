/* A C library that lets its caller decide what happens.
 *
 * This is the shape every real C library has: the library owns the loop
 * and the caller owns the decision, and the decision arrives as a
 * function pointer. Until Gyro had a spelling for one, none of these
 * functions could be imported — a `fn` in Gyro is two words, a closure
 * and the environment it captured, and there is nowhere in a C
 * signature to put the second one.
 *
 * `code(A, B): C` is the one word: the address of a function and
 * nothing beside it. `&f` on a declared function is how you get one,
 * which is what C spells the same way. */

#ifndef FILTER_H
#define FILTER_H

typedef int (*keep_fn)(int);
typedef int (*fold_fn)(int, int);

/* The caller's decision, run over the library's own data. */
int count_kept(keep_fn keep);
int fold(fold_fn step, int start);

/* A callback that may be absent, which is a check and not a jump to
 * address zero. */
int fold_or(fold_fn step, int start, int fallback);

/* A table of them, so that "a struct holding a callback is the struct C
 * lays out" is a thing this case measures rather than assumes. */
struct Rule {
    int code;
    keep_fn keep;
};

int rule_size(void);
int apply_rule(struct Rule r, int v);

/* One of the library's own, handed back out. A code pointer crosses
 * both ways because it is one address either way. */
fold_fn adder(void);

/* And back in: the caller hands over what the library just gave it. */
int round_trip(fold_fn step);

#endif
