#include "shapes.h"

struct Point point_at(int x, int y) {
    struct Point p;
    p.x = x;
    p.y = y;
    return p;
}

struct Line line_through(struct Point a, struct Point b) {
    struct Line l;
    l.a = a;
    l.b = b;
    return l;
}

int line_span(struct Line l) {
    return (l.b.x - l.a.x) + (l.b.y - l.a.y);
}

int point_size(void) { return (int)sizeof(struct Point); }
int line_size(void) { return (int)sizeof(struct Line); }

int point_x(struct Point *p) { return p->x; }

void point_shift(struct Point *p, int dx, int dy) {
    p->x += dx;
    p->y += dy;
}

void line_ends(struct Line *l, struct Point *first, struct Point *last) {
    *first = l->a;
    *last = l->b;
}

/* What an Arena actually is. The tag is different, so `struct Arena`
 * stays incomplete in this file too — the only thing connecting the two
 * is the cast, which is exactly the arrangement a real library uses. */
struct guts {
    int used;
    int cap;
};

static struct guts pool = {0, 64};

struct Arena *arena_open(void) { return (struct Arena *)&pool; }

int arena_take(struct Arena *a, int n) {
    struct guts *g = (struct guts *)a;
    if (g->used + n > g->cap) {
        return -1;
    }
    g->used += n;
    return g->used;
}

int arena_used(struct Arena *a) { return ((struct guts *)a)->used; }
