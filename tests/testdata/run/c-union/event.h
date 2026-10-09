/* The shape every C event loop is built on, and the reason unions had
 * to exist here at all: a union of several event structs, each of which
 * begins with the same tag, plus a bare member for reading that tag out
 * of whichever one is there.
 *
 * SDL2's SDL_Event is exactly this. So is X11's XEvent, and Wayland's,
 * and every message queue anyone has written in C. It works because C
 * defines reading across members that share an initial sequence, and
 * that is the one cross-member read a program is entitled to.
 *
 * In METALS a union is a shape of its own: every member at offset zero,
 * the size the widest member's, the alignment the strictest. It is not a
 * record with coincident offsets, because writing one member unwrites
 * the others — which is a fact about reads and not about addresses. */

enum event_kind {
    EVENT_NONE,
    EVENT_KEY,
    EVENT_MOVE,
    EVENT_QUIT
};

struct key_event {
    int kind;
    int code;
    int pressed;
};

struct move_event {
    int kind;
    long x;
    long y;
};

union event {
    int kind;
    struct key_event key;
    struct move_event move;
};

/* Fills the queue with a fixed script and answers how many. */
int events_load(void);

/* The tag of one queued event, read through the bare member while one
 * of the structs is what was written. */
int event_kind(int at);

/* The rest of an event, read through the member its tag names. */
int event_code(int at);
int event_pressed(int at);
long event_x(int at);
long event_y(int at);

/* The dispatch a real loop is: one pass over the queue, switching on
 * the tag and reading each event at the member the tag says. */
long events_total(void);

/* One storage: a union is the widest member's size at the strictest
 * member's alignment, and not the sum of anything. */
int event_size(void);
int key_size(void);
int move_size(void);

/* Writing a member is what makes it the one that is there, and the
 * assignment copies the storage rather than sharing it. */
int reassign_round_trip(void);

/* A union is a member like any other, and it is an array element like
 * any other: the stride is one union. */
int stride_is_one_union(void);

/* A small union initialized at file scope, which writes the first
 * member and only the first. */
int first_member_initialized(void);
