package cc

import "testing"

// Unions.
//
// A union is one storage with several readings of it, and METALS has a
// shape of its own for that rather than a record with coincident
// offsets: writing one member unwrites the others, which is a fact about
// reads and not about addresses. What the front end owes is C's own
// layout — the widest member's size at the strictest member's alignment
// — and C's own rule about reading across.

func TestUnionSharesStorage(t *testing.T) {
	answers(t, `
union pair { int a; int b; };

int result(void) {
    union pair p;
    p.a = 42;
    /* One storage, so the other member reads what this one wrote: two
     * ints at the same address are the same int. */
    return p.b;
}
`, 42)
}

// The size is the widest member's rounded to the strictest member's
// alignment, and never the sum of them.
func TestUnionSize(t *testing.T) {
	answers(t, `
union small { char c; int n; };
int result(void) { return (int)sizeof(union small); }
`, 4)

	answers(t, `
struct wide { int kind; long x; long y; };
union big { int kind; struct wide w; char pad[3]; };
int result(void) { return (int)sizeof(union big); }
`, 24)

	// A union inside a struct is a member like any other, so the struct
	// is 8 for the long and 24 for the union.
	answers(t, `
union body { long n; char text[20]; };
struct message { long id; union body body; };
int result(void) { return (int)sizeof(struct message); }
`, 32)
}

// The read every tagged union in C is built on: members that begin the
// same way read across, and that is the one cross-member read the
// language defines.
func TestCommonInitialSequence(t *testing.T) {
	answers(t, `
struct key { int kind; int code; };
struct move { int kind; long x; };
union event { int kind; struct key key; struct move move; };

int result(void) {
    union event e;

    e.key.kind = 1;
    e.key.code = 65;
    if (e.kind != 1) { return 1; }
    /* Reading the tag did not write, so the member that was there is
     * still there. */
    if (e.key.code != 65) { return 2; }

    e.move.kind = 2;
    e.move.x = 40;
    if (e.kind != 2) { return 3; }
    return (int)e.move.x + 2;
}
`, 42)
}

// Two members of one union are one address, which is what sharing
// storage means.
func TestUnionMembersAreOneAddress(t *testing.T) {
	answers(t, `
union pair { int a; long b; };

int result(void) {
    union pair p;
    void *first = &p.a;
    void *second = &p.b;
    void *whole = &p;
    if (first != second) { return 1; }
    if (first != whole) { return 2; }
    return 42;
}
`, 42)
}

// A union assigns like every other aggregate: the copy is storage of
// its own.
func TestUnionAssignmentCopies(t *testing.T) {
	answers(t, `
union box { int n; };

int result(void) {
    union box a;
    union box b;
    a.n = 42;
    b = a;
    b.n = 7;
    return a.n;
}
`, 42)
}

// A braced initializer writes the first member and only the first,
// which is all one storage has room for.
func TestUnionInitializesItsFirstMember(t *testing.T) {
	answers(t, `
union box { int n; long wide; };
static union box global = { 42 };

int result(void) {
    union box local = { 40 };
    return local.n + global.n - 40;
}
`, 42)
}

// A union is an array element like any other, and the stride is one
// union rather than one member.
func TestUnionArrayStride(t *testing.T) {
	answers(t, `
union cell { int n; long wide; };

int result(void) {
    union cell row[3];
    union cell *p = row;
    row[0].n = 1;
    row[1].n = 2;
    row[2].n = 39;
    return p->n + (p + 1)->n + (p + 2)->n;
}
`, 42)
}

// A pointer to a union is an ordinary pointer, and `->` reaches a
// member through one the way it reaches a field through a struct.
func TestUnionThroughAPointer(t *testing.T) {
	answers(t, `
union box { int n; };

static void fill(union box *b, int v) { b->n = v; }

int result(void) {
    union box b;
    fill(&b, 42);
    return b.n;
}
`, 42)
}

func TestUnionRefusals(t *testing.T) {
	// C keeps one namespace for struct, union and enum tags, so this is
	// not two types that happen to share a name — and it is also what
	// keeps one METALS name from meaning two things.
	refused(t, `struct P { int a; }; union P { int b; }; int result(void) { return 0; }`,
		"one tag namespace for both")

	// One storage holds one member, so one initializer is all there is
	// room for.
	refused(t, `union box { int a; int b; }; int result(void) { union box u = { 1, 2 }; return u.a; }`,
		"holds one member at a time")

	refused(t, `union box { int a; }; int result(void) { union box u; return u.missing; }`,
		"union box has no member missing")

	// An incomplete union is still incomplete: it has no size, which is
	// the same answer a struct gets.
	refused(t, `union box; int result(void) { union box u; return 0; }`, "has no size here")

	// A member needs storage, so a union cannot hold itself either.
	refused(t, `union box { union box inner; }; int result(void) { return 0; }`,
		"which is not complete here")
}
