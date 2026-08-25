/* Pulled in because area.c calls perimeter. It calls back into area.c
 * and on into counter.c, so the two files call each other and a third
 * arrives transitively.
 *
 * Mutual calls across files are ordinary C and have no dependency order.
 * As separate modules they would be an import cycle the loader refuses;
 * as one linkage unit they are two calls. */

#include "shape.h"

/* Same name as area.c's, different meaning. Neither file can see the
 * other's. */
static int helper(int x) { return x + 100; }

int perimeter(int w, int h) {
    struct box b;
    b.w = w;
    b.h = h;
    return 2 * (b.w + b.h);
}

int scaled_area(int w, int h, int factor) {
    bump();
    return area(w * factor, h * factor) + helper(0);
}
