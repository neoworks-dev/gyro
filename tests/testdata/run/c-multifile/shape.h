/* The header both .c files include. It declares; it defines nothing.
 *
 * That is C's whole arrangement: a header says what a function is, a .c
 * says what it does, and nothing anywhere says which .c. The list of
 * files is the build system's job in C, and here it is recovered from
 * the entry file — see internal/cc/resolve.go. */

#ifndef SHAPE_H
#define SHAPE_H

/* Both files parse this struct, because both include this header. The
 * program has one record: same tag and same shape is one declaration. */
struct box {
    int w;
    int h;
};

/* area is in area.c, perimeter and scaled_area are in geometry.c, and
 * bump and counter_value are in counter.c. Nothing below says so, which
 * is the point. */
int area(int w, int h);
int perimeter(int w, int h);
int describe(int w, int h);
int scaled_area(int w, int h, int factor);
int bump(void);
int counter_value(void);

#endif
