/* A C library that drives itself through function pointers: the shape
 * every real C library uses to let its caller decide what happens.
 *
 * The declarators here are the point as much as the code is. C reads a
 * declarator as the expression that would use the thing, which is why
 * `int (*chooser(int))(int, int)` is a function returning a pointer to a
 * function, and why the typedef spelling of the same type looks nothing
 * like it.
 *
 * Everything crossing back into Gyro is a scalar. A callback is an
 * address in this program, and what may cross a runtime boundary is
 * settled elsewhere; none of it is what this case is about. */

#ifndef TABLE_H
#define TABLE_H

/* A pointer to a function, written the way a header usually writes it. */
typedef int (*binary_fn)(int, int);

/* A dispatch table entry. The callback is one word, so this struct is
 * what C lays out and not a byte more. */
struct op {
    int code;
    int (*apply)(int, int);
};

int add(int a, int b);
int sub(int a, int b);
int mul(int a, int b);

/* sizeof over the table's own type, and sizeof over one entry: the
 * numbers that say a callback is one word. */
int entries(void);
int entry_size(void);
int callback_size(void);

/* Applies whatever the table holds for a code, or -1 for a code it has
 * no entry for. */
int dispatch(int code, int a, int b);

/* Sorts a private array through a comparison the caller picks, and
 * answers one of the sorted elements. */
int sorted_at(int descending, int i);

/* Calls through a pointer that may be null, which is a check and not a
 * jump to address zero. */
int call_or(int use_null, int a, int b, int fallback);

/* Two names for one function are one address; two functions are two. */
int identity_holds(void);

/* A pointer with static storage, advanced by each call. */
int stepped(int x);

#endif
