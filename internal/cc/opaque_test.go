package cc

import "testing"

// Pointers to incomplete types.
//
// A tag no file in the program defines is C's incomplete type, and in
// METALS it is an opaque type declaration: a name with no shape. The
// interesting half is not what such a pointer can do — hold an address,
// compare, convert — but what it cannot, because that list is the entire
// content of the declaration. Every measurement of the object is
// refused, and refused here rather than by the verifier, so the message
// carries a position in the C source.

func TestOpaquePointerIsCarriedAndCompared(t *testing.T) {
	answers(t, `
struct session;

static int storage[2];

static struct session *first(void) { return (struct session *)&storage[0]; }
static struct session *second(void) { return (struct session *)&storage[1]; }

int result(void) {
    struct session *a = first();
    struct session *b = second();
    struct session *again = first();
    struct session *none = 0;

    if (a == b) { return 1; }
    if (a != again) { return 2; }
    if (none != 0) { return 3; }
    return 42;
}
`, 42)
}

// The declaration is what the module emits, and it emits it because
// nothing completed the tag. A tag one file leaves open and another
// closes is one complete type, which declareRecords already merged.
func TestOpaqueTagBecomesAnOpaqueDeclaration(t *testing.T) {
	module, err := Compile("case.c", `
struct session;
struct point { int x; };

static struct session *held;

int result(void) { held = 0; return (int)sizeof(struct point); }
`, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	found := map[string]bool{}
	for _, decl := range module.Types {
		found[decl.Name] = decl.Opaque
	}
	if opaque, ok := found["session"]; !ok || !opaque {
		t.Fatalf("struct session should be declared opaque, got %v", found)
	}
	if opaque, ok := found["point"]; !ok || opaque {
		t.Fatalf("struct point should be declared as a record, got %v", found)
	}
}

// The address is a word whatever is at the far end, which is the one
// measurement an incomplete type still answers — it is a measurement of
// the pointer and not of the type.
func TestSizeofAnOpaquePointer(t *testing.T) {
	answers(t, `
struct session;
int result(void) { return (int)sizeof(struct session *); }
`, 8)
}

// The four things C refuses on a pointer to an incomplete type, which
// are the four that measure the object.
func TestRefusesWhatNeedsTheLayout(t *testing.T) {
	const open = "struct session; struct session *p; struct session *q;\n"

	refused(t, open+`int result(void) { struct session s; return 0; }`, "has no size here")
	refused(t, open+`int result(void) { return (int)sizeof(struct session); }`, "has no size")
	refused(t, open+`int result(void) { struct session *r = p + 1; return r == q; }`, "no element size to step by")
	refused(t, open+`int result(void) { return (int)(p - q); }`, "no difference in elements")
	refused(t, open+`int result(void) { return p < q; }`, "count elements neither of them has")
	refused(t, open+`int result(void) { return p->field; }`, "never defined, so it has no field")
	refused(t, open+`int result(void) { *p; return 0; }`, "there is no object to read")
	refused(t, open+`struct session take(struct session s) { return s; } int result(void) { return 0; }`,
		"not complete here")
}

// A member needs storage, so its type has to be complete where it is
// written. That is also what rules out a struct holding itself, without
// the layout table having to notice the cycle.
func TestRefusesAnIncompleteMember(t *testing.T) {
	refused(t, `
struct session;
struct holder { struct session s; };
int result(void) { return 0; }
`, "field s is struct session, which is not complete here")

	refused(t, `
struct node { struct node self; };
int result(void) { return 0; }
`, "field self is struct node, which is not complete here")
}

// A pointer to a member is not affected: an address is a word whatever
// is at the far end, and that is what makes the forward declaration
// worth writing.
func TestAllowsAPointerMember(t *testing.T) {
	answers(t, `
struct session;

struct holder {
    struct session *s;
    int tag;
};

int result(void) {
    struct holder h;
    h.s = 0;
    h.tag = 42;
    if (h.s != 0) { return 1; }
    return h.tag;
}
`, 42)
}

// Two opaque types are two types. Neither converts to the other without
// being asked, which is the only thing a caller gets in exchange for
// being told nothing — and the reason to declare two names nobody
// defines instead of using one `void *` for both.
func TestOpaqueTypesStayDistinct(t *testing.T) {
	refused(t, `
struct session;
struct ticket;

int result(void) {
    struct session *s = 0;
    struct ticket *t = s;
    return t == 0;
}
`, "without a cast")

	answers(t, `
struct session;
struct ticket;

static int storage;

int result(void) {
    struct session *s = (struct session *)&storage;
    struct ticket *t = (struct ticket *)s;
    struct session *back = (struct session *)t;
    if (back != s) { return 1; }
    return 42;
}
`, 42)
}

// A `void *` in the middle of the two claims nothing on the way out and
// makes the claim again on the way back, which is how an opaque handle
// travels through a callback's userdata.
func TestOpaquePointerThroughVoid(t *testing.T) {
	answers(t, `
struct session;

static int storage = 42;

int result(void) {
    struct session *s = (struct session *)&storage;
    void *raw = s;
    struct session *back = raw;
    if (back != s) { return 1; }
    return *(int *)back;
}
`, 42)
}
