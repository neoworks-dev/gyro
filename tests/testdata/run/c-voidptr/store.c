#include "store.h"

/* The objects the store points at. Their types are known here and
 * nowhere else, which is the situation `void *` exists for. */
static int values[3] = { 10, 20, 30 };
static struct point origin = { 3, 4 };
static int scratch = 0;

struct cell {
    int kind;
    void *data;
};

static struct cell cells[4];
static int ready = 0;

static void setup(void) {
    if (ready) {
        return;
    }
    ready = 1;

    cells[0].kind = KIND_INT;
    cells[0].data = &values[0];

    cells[1].kind = KIND_INT;
    cells[1].data = &values[2];

    /* A different type entirely, in the same array of slots. */
    cells[2].kind = KIND_POINT;
    cells[2].data = &origin;

    cells[3].kind = KIND_NONE;
    cells[3].data = (void *)0;
}

int cell_kind(int slot) {
    setup();
    return cells[slot].kind;
}

int cell_int(int slot) {
    setup();
    /* The type goes back on where it was taken off, and only here. */
    return *(int *)cells[slot].data;
}

int cell_x(int slot) {
    struct point *p;
    setup();
    /* C converts a `void *` to an object pointer with no cast written,
     * which is the one asymmetry it keeps against C++. */
    p = cells[slot].data;
    return p->x;
}

int cell_y(int slot) {
    setup();
    return ((struct point *)cells[slot].data)->y;
}

int cell_is_null(int slot) {
    setup();
    return cells[slot].data == 0;
}

int same_address(int a, int b) {
    setup();
    return cells[a].data == cells[b].data;
}

typedef int (*visitor)(void *data, int kind);

/* A visitor is handed an address it did not create and a tag telling it
 * what to make of it. Nothing between the store and here knows the
 * types, which is the point. */
static int accumulate(void *data, int kind) {
    struct point *p;

    if (kind == KIND_INT) {
        return *(int *)data;
    }
    if (kind == KIND_POINT) {
        p = data;
        return p->x + p->y;
    }
    return 0;
}

static int each(visitor fn) {
    int total = 0;
    int i;

    setup();
    for (i = 0; i < 4; i = i + 1) {
        if (cells[i].data == 0) {
            continue;
        }
        total = total + fn(cells[i].data, cells[i].kind);
    }
    return total;
}

int sum_via_callback(void) {
    return each(accumulate);
}

int round_trip(int value) {
    void *erased;
    int *back;

    scratch = value;
    erased = &scratch;
    back = erased;
    *back = *back + 1;
    return scratch;
}

int void_pointer_size(void) {
    return (int)sizeof(void *);
}
