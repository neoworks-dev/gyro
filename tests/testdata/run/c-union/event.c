#include "event.h"

#define QUEUE 4

static union event queue[QUEUE];
static int filled;

/* A file-scope union initializer writes the first member and only the
 * first, because one storage has room for one of them. */
static union event origin = { EVENT_NONE };

int events_load(void) {
    union event *e;

    e = &queue[0];
    e->key.kind = EVENT_KEY;
    e->key.code = 65;
    e->key.pressed = 1;

    e = &queue[1];
    e->move.kind = EVENT_MOVE;
    e->move.x = 30;
    e->move.y = 12;

    e = &queue[2];
    e->key.kind = EVENT_KEY;
    e->key.code = 66;
    e->key.pressed = 0;

    e = &queue[3];
    e->kind = EVENT_QUIT;

    filled = QUEUE;
    return filled;
}

int event_kind(int at) {
    /* The read the whole arrangement exists for: `kind` is an int at the
     * front of the union, and every member begins with one. */
    return queue[at].kind;
}

int event_code(int at) {
    return queue[at].key.code;
}

int event_pressed(int at) {
    return queue[at].key.pressed;
}

long event_x(int at) {
    return queue[at].move.x;
}

long event_y(int at) {
    return queue[at].move.y;
}

long events_total(void) {
    long total = 0;
    int i;
    for (i = 0; i < filled; i++) {
        switch (queue[i].kind) {
        case EVENT_KEY:
            total += queue[i].key.code;
            break;
        case EVENT_MOVE:
            total += queue[i].move.x + queue[i].move.y;
            break;
        default:
            break;
        }
    }
    return total;
}

int event_size(void) {
    return (int)sizeof(union event);
}

int key_size(void) {
    return (int)sizeof(struct key_event);
}

int move_size(void) {
    return (int)sizeof(struct move_event);
}

int reassign_round_trip(void) {
    union event a;
    union event b;

    a.move.kind = EVENT_MOVE;
    a.move.x = 7;
    a.move.y = 8;

    /* The copy is storage of its own, so writing it leaves a alone. */
    b = a;
    b.move.x = 99;

    if (b.move.x != 99) { return 1; }
    if (a.move.x != 7) { return 2; }
    if (b.kind != EVENT_MOVE) { return 3; }
    return (int)(a.move.x + a.move.y);
}

int stride_is_one_union(void) {
    union event pair[2];
    union event *first = &pair[0];

    pair[0].key.kind = EVENT_KEY;
    pair[0].key.code = 3;
    pair[1].key.kind = EVENT_KEY;
    pair[1].key.code = 4;

    /* Stepping the pointer moves by one union, and the two elements are
     * separate storage rather than one shared with itself. */
    if ((first + 1)->key.code != 4) { return 1; }
    if (first->key.code != 3) { return 2; }
    return first->key.code + (first + 1)->key.code;
}

int first_member_initialized(void) {
    return origin.kind == EVENT_NONE;
}
