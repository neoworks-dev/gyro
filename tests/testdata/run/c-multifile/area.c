/* The entry file: the one Gyro names. Everything else in this directory
 * arrives because something here, or something that arrived because of
 * something here, needed a name it did not define. */

#include "shape.h"

/* A helper this file keeps to itself. geometry.c has one with the same
 * name and a different meaning, and both are correct — that is what
 * `static` is for, and why the two become separate symbols. */
static int helper(int x) { return x * 10; }

int area(int w, int h) {
    struct box b;
    b.w = w;
    b.h = h;
    return b.w * b.h;
}

int describe(int w, int h) {
    /* perimeter lives in geometry.c. This file has its prototype and no
     * idea where the body is, which is exactly as much as C ever knows. */
    return area(w, h) + perimeter(w, h) + helper(1);
}
