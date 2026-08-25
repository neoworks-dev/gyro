package cc

import "testing"

// Arrays and pointer arithmetic. C's array is not a value: it is
// storage that decays to a pointer everywhere but a declaration and a
// sizeof, and `a[i]` is defined as `*(a + i)`. Both of those are what
// these check, because both are what a C program written by someone
// else will lean on in its first loop.

func TestArrayIndexing(t *testing.T) {
	answers(t, `
int result(void) {
    int a[5];
    int i;
    for (i = 0; i < 5; i++) {
        a[i] = i * i;
    }
    return a[0] + a[1] + a[2] + a[3] + a[4];
}
`, 0+1+4+9+16)

	// `a[i]` is `*(a + i)`, so `i[a]` is the same expression written the
	// other way round. It is a good test precisely because nobody would
	// write it: it only works if the desugaring is the definition.
	answers(t, `
int result(void) {
    int a[3];
    a[0] = 40;
    1[a] = 2;
    return a[0] + a[1];
}
`, 42)
}

func TestPointerWalksAnArray(t *testing.T) {
	answers(t, `
static int sum(int *p, int n) {
    int total = 0;
    int *end = p + n;
    while (p < end) {
        total = total + *p;
        p++;
    }
    return total;
}

int result(void) {
    int a[6];
    int i;
    for (i = 0; i < 6; i++) {
        a[i] = i + 1;
    }
    return sum(a, 6);
}
`, 21)
}

func TestPointerArithmetic(t *testing.T) {
	answers(t, `
int result(void) {
    int a[8];
    int i;
    for (i = 0; i < 8; i++) {
        a[i] = i * 10;
    }

    int *p = a + 5;
    int *q = p - 2;

    int total = 0;
    total = total + *p;          /* 50 */
    total = total + *q;          /* 30 */
    total = total + (int)(p - q);/*  2 */
    total = total + *--p;        /* 40 */
    total = total + *(q + 1);    /* 40 */
    total = total + (p > q);     /*  1 */
    total = total + (q >= a);    /*  1 */
    return total;
}
`, 50+30+2+40+40+1+1)
}

// One past the end is a pointer C lets a program form and compare, and
// it is what every idiomatic walk stops on.
func TestOnePastTheEnd(t *testing.T) {
	answers(t, `
int result(void) {
    int a[4];
    int *end = a + 4;
    int *p = a;
    int steps = 0;
    while (p != end) {
        steps++;
        p++;
    }
    return steps * 10 + (int)(end - a);
}
`, 44)
}

// An array parameter is a pointer, which is why the length has to be
// passed beside it and why sizeof inside the callee would be wrong.
func TestArrayParameterIsAPointer(t *testing.T) {
	answers(t, `
static void fill(int a[], int n, int with) {
    int i;
    for (i = 0; i < n; i++) {
        a[i] = with;
    }
}

int result(void) {
    int a[3];
    fill(a, 3, 14);
    return a[0] + a[1] + a[2];
}
`, 42)
}

func TestArraysOfStructs(t *testing.T) {
	answers(t, `
struct Point { int x; int y; };

int result(void) {
    struct Point pts[3];
    int i;
    for (i = 0; i < 3; i++) {
        pts[i].x = i;
        pts[i].y = i * 2;
    }

    struct Point *p = pts + 2;
    return pts[1].x + pts[1].y + p->x + p->y + 33;
}
`, 1+2+2+4+33)
}

func TestTwoDimensionalArrays(t *testing.T) {
	// `int g[2][3]` is two arrays of three, so `g[i]` is an array that
	// decays and `g[i][j]` steps twice.
	answers(t, `
int result(void) {
    int g[2][3];
    int i;
    int j;
    for (i = 0; i < 2; i++) {
        for (j = 0; j < 3; j++) {
            g[i][j] = i * 3 + j;
        }
    }
    return g[0][0] + g[0][2] + g[1][0] + g[1][2] + 32;
}
`, 0+2+3+5+32)
}

func TestSizeofArrays(t *testing.T) {
	answers(t, `
int result(void) {
    int a[10];
    char c[7];
    return (int)sizeof(a) + (int)sizeof(c) + (int)(sizeof(a) / sizeof(a[0]));
}
`, 40+7+10)
}

// An array in module storage is the same thing one frame up, which is
// what a C program's lookup tables are.
func TestGlobalArray(t *testing.T) {
	answers(t, `
static int table[4];

static void init(void) {
    int i;
    for (i = 0; i < 4; i++) {
        table[i] = (i + 1) * (i + 1);
    }
}

int result(void) {
    init();
    return table[0] + table[1] + table[2] + table[3] + 12;
}
`, 1+4+9+16+12)
}
