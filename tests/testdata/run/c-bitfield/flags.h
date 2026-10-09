/* Bit-fields, the way C packs a header or a hardware register.
 *
 * A bit-field's place is measured in bits, and METALS has no member
 * measured in bits — deliberately, because a format whose records were
 * bit-addressed would be a format every consumer had to shift. So a run
 * of adjacent bit-fields becomes one member of bytes, and each of C's
 * members remembers which bits of that run are its own.
 *
 * The packing rule is x86-64's, so the sizes here are the ones a C
 * compiler answers for the same declarations: a bit-field starts at the
 * next free bit and may not cross a boundary of its own declared type's
 * size, and the struct's alignment takes in those declared types even
 * though what is stored is bytes. */

/* An IPv4 header's first word, which is where most people meet the
 * feature: two four-bit fields in one byte, then a byte, then a short. */
struct ip_head {
    unsigned version:4;
    unsigned ihl:4;
    unsigned char tos;
    unsigned short length;
};

/* The straddle rule: the second field would cross the 32-bit boundary
 * its declared type sets, so it starts at the next one and the struct is
 * eight bytes rather than five. */
struct wide {
    unsigned first:30;
    unsigned second:10;
};

/* A signed bit-field extends its sign, an unsigned one does not — which
 * is the difference that makes four bits of ones -1 in the first and 15
 * in the second. */
struct signs {
    int low:4;
    unsigned high:4;
};

/* A run beside ordinary members, so the run's bytes are exactly the ones
 * it uses and the member after it lands right behind them. */
struct mixed {
    unsigned tag:3;
    unsigned kind:5;
    char name[6];
    int count;
};

int size_of_head(void);
int size_of_wide(void);
int size_of_signs(void);
int size_of_mixed(void);

int head_version(void);
int head_ihl(void);
int head_tos(void);
int head_length(void);

/* Writing one field leaves its neighbours where they were, which is the
 * whole of what a read-modify-write has to get right. */
int neighbours_survive(void);

int wide_round_trip(void);
int signed_reads_back(void);
int unsigned_reads_back(void);

/* What does not fit is dropped, which is C's conversion into a
 * bit-field, and the increments wrap at the field's width rather than at
 * int's. */
int truncates_on_write(void);
int wraps_at_its_width(void);

/* A braced initializer writes the members in order, and what it leaves
 * out is zero. */
int initialized_in_order(void);
int file_scope_initializer(void);

/* Every member of a union begins where the union does, so its
 * bit-fields overlap rather than pack. */
int union_members_overlap(void);
