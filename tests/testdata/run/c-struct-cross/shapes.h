/* A C library whose types are part of its interface.
 *
 * Everything a header like this exports is written in terms of its own
 * structs, and until now the Gyro side could call none of it: the
 * generated declarations had no way to say `struct Point`, so every
 * function mentioning one was left out. What was missing was not a
 * spelling but an identity — a Gyro struct of the same fields would
 * have been a second type with the same bytes, and passing one to
 * `line_span` would have been a lie the linker had no way to catch.
 *
 * So the declaration Gyro gets is a declaration of *this* type. The
 * generated header says `export struct Point { export x: i32, ... }`,
 * and lowering imports %Point out of this module instead of building
 * one from that text. There is one %Point in the linked program, and
 * the linker compares the two declarations of it before there is. */

#ifndef SHAPES_H
#define SHAPES_H

struct Point {
    int x;
    int y;
};

/* A struct holding structs, so that "the members come too" is a real
 * state: importing %Line without %Point would be a type nothing can
 * lay out. */
struct Line {
    struct Point a;
    struct Point b;
};

/* Declared here, defined nowhere in the program — the incomplete type a
 * library hands out when it will not say what it is holding. A caller
 * may hold one, pass it and compare it, and that is the whole contract.
 * It crosses as a fieldless Gyro struct, which is Gyro's own spelling
 * for a thing you may keep and not look inside. */
struct Arena;

struct Point point_at(int x, int y);
struct Line line_through(struct Point a, struct Point b);
int line_span(struct Line l);

/* sizeof on both sides of the boundary, which is the claim that the two
 * declarations are one layout and not two that happen to agree. */
int point_size(void);
int line_size(void);

/* A pointer to a caller's struct: C reading and writing storage Gyro
 * owns. There is no boundary here to copy across — one runtime, one
 * heap — so an out-parameter is an ordinary argument. */
int point_x(struct Point *p);
void point_shift(struct Point *p, int dx, int dy);
void line_ends(struct Line *l, struct Point *first, struct Point *last);

struct Arena *arena_open(void);
int arena_take(struct Arena *a, int n);
int arena_used(struct Arena *a);

#endif
