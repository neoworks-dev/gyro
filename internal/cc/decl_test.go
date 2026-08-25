package cc

import (
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// The declaration grammar: typedef, enum, several declarators to one
// type, and sizeof. Between them they are what makes the first ten lines
// of a real C file parse.

func TestTypedef(t *testing.T) {
	answers(t, `
typedef int Count;
typedef Count *CountPtr;

int result(void) {
    Count n = 40;
    CountPtr p = &n;
    *p = *p + 2;
    return n;
}
`, 42)
}

// `typedef struct { ... } Point;` is the idiom a tagless struct exists
// for, and the METALS record it produces is named after the typedef
// rather than after nothing.
func TestTypedefNamesAnAnonymousStruct(t *testing.T) {
	module := compiled(t, `
typedef struct { int x; int y; } Point;

int result(void) {
    Point p;
    p.x = 40;
    p.y = 2;
    return p.x + p.y;
}
`)
	found := false
	for _, decl := range module.Types {
		if decl.Name == "Point" {
			found = true
		}
	}
	if !found {
		t.Errorf("no %%Point record; the module declares %v", typeNames(module.Types))
	}
	answers(t, `
typedef struct { int x; int y; } Point;

int result(void) {
    Point p;
    p.x = 40;
    p.y = 2;
    return p.x + p.y;
}
`, 42)
}

// A tag and a typedef of the same name are what C's `typedef struct Node
// Node;` produces, and they have to end up as one record rather than
// two.
func TestTypedefOfATaggedStruct(t *testing.T) {
	answers(t, `
typedef struct Node Node;

struct Node {
    int value;
    Node *next;
};

int result(void) {
    Node tail;
    Node head;
    tail.value = 2;
    tail.next = 0;
    head.value = 40;
    head.next = &tail;
    return head.value + head.next->value;
}
`, 42)
}

func TestEnum(t *testing.T) {
	answers(t, `
enum Color { RED, GREEN, BLUE };
enum Flag { LOW = 8, HIGH = LOW << 2, NEXT };

int result(void) {
    return RED + GREEN + BLUE + LOW + HIGH + NEXT;
}
`, 0+1+2+8+32+33)

	// The enumeration is an int, so a variable of the enum type is one.
	answers(t, `
enum Color { RED, GREEN, BLUE };

int result(void) {
    enum Color c = BLUE;
    if (c == BLUE) {
        return 42;
    }
    return 0;
}
`, 42)
}

// A local of the same name shadows an enumeration constant, which is why
// the constants are resolved during lowering and not during the parse.
func TestALocalShadowsAnEnumerator(t *testing.T) {
	answers(t, `
enum E { N = 7 };

int result(void) {
    int N = 42;
    return N;
}
`, 42)
}

func TestSeveralDeclaratorsPerDeclaration(t *testing.T) {
	answers(t, `
int result(void) {
    int a = 1, b = 2, c;
    c = a + b;

    int x = 10, *p = &x;
    return c + *p + 29;
}
`, 42)
}

func TestSizeof(t *testing.T) {
	answers(t, `
struct Pair { int a; int b; };

int result(void) {
    int total = 0;
    total = total + (int)sizeof(char);
    total = total + (int)sizeof(short);
    total = total + (int)sizeof(int);
    total = total + (int)sizeof(long);
    total = total + (int)sizeof(double);
    total = total + (int)sizeof(struct Pair);
    return total;
}
`, 1+2+4+8+8+8)

	// The expression form measures a type and evaluates nothing, which
	// is what makes `sizeof *p` legal on a null p.
	answers(t, `
int bump(void);

int result(void) {
    int *p = 0;
    return (int)sizeof *p + (int)sizeof(1 + 1);
}
`, 8)
}

func TestDeclarationRefusals(t *testing.T) {
	refused(t, `union U { int a; int b; }; int result(void) { return 0; }`, "a union has no METALS type")
	refused(t, `int result(void) { return sizeof(void); }`, "sizeof(void) has no answer")
	refused(t, `enum E { A }; int result(void) { A = 1; return 0; }`, "enumeration constant")
	refused(t, `typedef int T; typedef long T; int result(void) { return 0; }`, "typedef'd twice")
}

func typeNames(decls []*metals.TypeDecl) []string {
	out := make([]string, len(decls))
	for i, decl := range decls {
		out[i] = decl.Name
	}
	return out
}
