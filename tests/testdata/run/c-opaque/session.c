#include "session.h"

/* What a handle really addresses. Note that this does not define
 * `struct session` — it is a different tag, and the connection between
 * the two is a cast and nothing else. That is the arrangement a library
 * uses when it does not want the caller to have the declaration at all,
 * and it is why `struct session` stays incomplete even in the file that
 * implements it. */
struct guts {
    int seed;
    int calls;
};

#define SLOTS 4

static struct guts pool[SLOTS];
static struct session *live[SLOTS];
static struct ticket *issued[SLOTS];
static int used;

/* The two directions of the erasure. One takes the type off an address
 * this file understands; the other claims it back. */
static struct session *handle_of(struct guts *g) {
    return (struct session *)g;
}

static struct guts *guts_of(struct session *s) {
    return (struct guts *)s;
}

int session_open(int seed) {
    int slot = used++;
    struct guts *g = &pool[slot];
    g->seed = seed;
    g->calls = 0;
    live[slot] = handle_of(g);
    return slot;
}

int session_next(int slot) {
    struct guts *g = guts_of(live[slot]);
    g->calls++;
    return g->seed + g->calls;
}

int session_total(int slot) {
    struct guts *g = guts_of(live[slot]);
    return g->seed + g->calls;
}

int session_same(int a, int b) {
    return live[a] == live[b];
}

int session_absent(int slot) {
    return live[slot] == 0;
}

int session_handle_size(void) {
    return (int)sizeof(struct session *);
}

int session_round_trip(int slot) {
    /* A handle converts to a `void *` on its own, because that
     * direction claims nothing. Coming back is a claim, and C lets a
     * `void *` make it implicitly — which is the one asymmetry in the
     * pair. */
    void *raw = live[slot];
    struct session *back = raw;
    return back == live[slot];
}

int ticket_issue(int slot) {
    /* A session and a ticket are two types over one address. Neither
     * converts to the other without being asked, which is the whole
     * reason to declare two names nobody defines instead of using one
     * `void *` for both. */
    issued[slot] = (struct ticket *)live[slot];
    return issued[slot] != 0;
}

int ticket_matches(int slot) {
    return (struct session *)issued[slot] == live[slot];
}

int session_open_count(void) {
    int open = 0;
    int i;
    for (i = 0; i < SLOTS; i++) {
        if (live[i] != 0) {
            open++;
        }
    }
    return open;
}

void session_close(int slot) {
    live[slot] = 0;
    issued[slot] = 0;
}
