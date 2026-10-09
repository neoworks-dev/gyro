/* The tagged union every C message queue is built on: a discriminant, and
 * a family of structs that begin with it. Gyro does not declare a union,
 * so the type is C's and arrives through the import. The function exists
 * to export the types; the case itself drives them from Gyro. */

struct KeyEvent {
    int kind;
    int code;
};

struct MouseEvent {
    int kind;
    int x;
    int y;
};

union Event {
    int kind;
    struct KeyEvent key;
    struct MouseEvent mouse;
};

struct Queued {
    int at;
    union Event event;
};

int event_kind(const union Event *e) { return e->kind; }
int queued_at(const struct Queued *q) { return q->at; }
