/* Where a bit-field's bits actually land, read out as bytes.
 *
 * The values every tier agrees on are in c-bitfield/. This is the other
 * half: that the bytes are the ones a C compiler would have written, in
 * the places it would have written them. Nothing but reading the storage
 * as bytes can say that, and reading a struct's storage as bytes is type
 * punning — which the two native tiers do because they have bytes, and
 * the evaluator refuses because it has values instead.
 *
 * So this case runs where it can be run, and says why it cannot be run
 * where it cannot. Each number below was taken from a C compiler on this
 * target before it was written here.
 *
 * Bit zero of a run is the least significant bit of its first byte,
 * which is what x86-64 means by a bit-field. */

struct pair {
    unsigned low:4;
    unsigned high:4;
};

/* The run is one byte, so the char after it is at offset one and not at
 * offset four. */
struct after {
    unsigned flag:1;
    char c;
};

/* The second field would cross the boundary its declared type sets, so
 * it starts at bit 32 — its first byte is four, not one. */
struct straddling {
    char c;
    unsigned wide:30;
};

/* A zero-width field closes the run it is in, and the next one opens at
 * the following boundary of its declared type. */
struct gapped {
    unsigned a:1;
    unsigned :0;
    unsigned b:1;
};

static void wipe(unsigned char *p, int n) {
    int i;
    for (i = 0; i < n; i++) {
        p[i] = 0;
    }
}

/* Two four-bit fields share one byte, low one first: 10 and 2 make
 * 0x2A. */
int packed_byte(void) {
    struct pair v;
    unsigned char *p = (unsigned char *)&v;
    wipe(p, (int)sizeof v);
    v.low = 10;
    v.high = 2;
    return (int)p[0];
}

int member_after_a_run(void) {
    struct after v;
    unsigned char *p = (unsigned char *)&v;
    wipe(p, (int)sizeof v);
    v.c = 42;
    return (int)p[1];
}

int straddle_moves_to_the_next_unit(void) {
    struct straddling v;
    unsigned char *p = (unsigned char *)&v;
    wipe(p, (int)sizeof v);
    v.wide = 1;
    if (p[1] != 0) { return 1; }
    if (p[4] != 1) { return 2; }
    return 42;
}

int zero_width_opens_a_new_unit(void) {
    struct gapped v;
    unsigned char *p = (unsigned char *)&v;
    wipe(p, (int)sizeof v);
    v.a = 1;
    v.b = 1;
    if (p[0] != 1) { return 1; }
    if (p[1] != 0) { return 2; }
    if (p[4] != 1) { return 3; }
    return 42;
}

/* Writing one field clears its own bits and no others, which is what
 * the read-modify-write is for. */
int writing_clears_only_its_own(void) {
    struct pair v;
    unsigned char *p = (unsigned char *)&v;
    wipe(p, (int)sizeof v);
    v.low = 15;
    v.high = 15;
    if (p[0] != 0xFF) { return 1; }
    v.low = 0;
    if (p[0] != 0xF0) { return 2; }
    v.high = 3;
    if (p[0] != 0x30) { return 3; }
    return 42;
}
