/* A heterogeneous store, the way C writes one: a tag says what an
 * address means and a `void *` carries the address without saying.
 *
 * Everything this header declares is a scalar, because a scalar is what
 * Gyro can import. The `void *` machinery is entirely inside store.c,
 * which is also where it is in a real C library — a caller is handed a
 * handle and an accessor, not the layout. */

enum kind {
    KIND_NONE,
    KIND_INT,
    KIND_POINT
};

struct point {
    int x;
    int y;
};

/* One slot's tag, and its contents read back at the type the tag says.
 * Reading a slot at the wrong type is what the tag exists to prevent. */
int cell_kind(int slot);
int cell_int(int slot);
int cell_x(int slot);
int cell_y(int slot);

/* A slot with no address in it. C spells the absent pointer as a null,
 * and a null void pointer is still one word. */
int cell_is_null(int slot);

/* Two erasures of one address are one address; two of different
 * addresses are not. Casting away a type does not move anything. */
int same_address(int a, int b);

/* The callback pattern the whole thing exists for: a visitor that is
 * handed a `void *` it did not create and a tag telling it what to make
 * of it. */
int sum_via_callback(void);

/* The round trip out and back: an address goes out as a `void *`, comes
 * back as the type it started as, and reads what was written. */
int round_trip(int value);

/* sizeof(void *) is a pointer's size and nothing about void. */
int void_pointer_size(void);
