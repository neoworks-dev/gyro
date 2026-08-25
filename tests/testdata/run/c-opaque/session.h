/* An opaque handle, which is how a C library hands out something it
 * will not describe.
 *
 * `struct session` is declared here and defined nowhere in the whole
 * program — not in this header, not in session.c, not anywhere. A
 * caller may hold a `struct session *`, pass it, compare it, store it
 * and hand it back. It may not dereference one, reach a member of one,
 * step one, subtract two or ask what one is the size of, because
 * nothing anywhere says what a session is.
 *
 * In METALS that is `ptr<%session>` where `%session` is an opaque type
 * declaration: a name with no shape. Erasing it to `anyptr` instead
 * would run just as well and would throw away the point of the header —
 * a session and a ticket are different types, and staying different is
 * the only thing the caller gets in exchange for not being told
 * anything. */
struct session;

/* A second one, so that "two opaque types" is a real state and not an
 * accident of there being one. Nothing defines a ticket either. */
struct ticket;

/* Everything this header exports is a scalar, because a scalar is what
 * Gyro imports. The handles never leave C — which is also true of the
 * real thing: a caller gets a handle and an accessor, not the layout. */

/* Opens a session and answers the slot it was filed under. */
int session_open(int seed);

/* Reads through a handle, by putting back the type the implementation
 * took off when it handed the address out. */
int session_next(int slot);
int session_total(int slot);

/* Two handles are equal when they are one address. Nothing here knows
 * what is at either. */
int session_same(int a, int b);

/* A slot nothing was ever filed under holds the absent handle, which C
 * spells as a null and METALS as an `opt<ptr<%session>>` with no
 * pointer in it. */
int session_absent(int slot);

/* sizeof a handle is a pointer's size. It is the one measurement an
 * incomplete type still answers, because it is not a measurement of the
 * type — it is of the address. */
int session_handle_size(void);

/* Out through a `void *` and back. Both erasures are total and neither
 * moves anything, so the address that comes back is the one that left. */
int session_round_trip(int slot);

/* A ticket for a session: the same address under the other name. */
int ticket_issue(int slot);
int ticket_matches(int slot);

/* Walking an array of handles, testing each against the absent one —
 * everything a program can do with addresses it knows nothing about. */
int session_open_count(void);
void session_close(int slot);
