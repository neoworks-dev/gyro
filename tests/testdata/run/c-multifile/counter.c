/* Reached only through geometry.c: two links from the file Gyro named.
 *
 * Its state is file-scope static, so no other file can touch it and
 * every reader goes through the two functions below. */

#include "shape.h"

static int count;

int bump(void) {
    count = count + 1;
    return count;
}

int counter_value(void) { return count; }
