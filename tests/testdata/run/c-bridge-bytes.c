// The second C case, for what the first one leaves out: reading storage
// at a width other than the one that wrote it.
//
// `fnv` walks an array of ints one unsigned char at a time. There is no
// typed IR in which that is an operation — the object has one type and
// the access has another — and it is completely ordinary C. It works
// here because a linear memory has no types in it at all: `data` is an
// offset, and the width of a read belongs to the instruction.
//
// The rest is the pointer-shaped code a typed backend would also find
// awkward: a sort that walks two indices of one array, a struct taken
// and returned by value, and 64-bit unsigned shifts whose signedness is
// the operation's rather than the register's.

__attribute__((import_module("gyro:rt"), import_name("println<i64>")))
extern void put(long long v);

// A deterministic generator, so the case needs no input and the golden
// is a property of the program rather than of the machine.
static unsigned long long state = 88172645463325252ull;

static unsigned long long xorshift(void) {
    state ^= state << 13;
    state ^= state >> 7;
    state ^= state << 17;
    return state;
}

static int data[512];

static void isort(int *a, int n) {
    for (int i = 1; i < n; i++) {
        int v = a[i];
        int j = i - 1;
        while (j >= 0 && a[j] > v) {
            a[j + 1] = a[j];
            j--;
        }
        a[j + 1] = v;
    }
}

static unsigned fnv(const unsigned char *p, unsigned n) {
    unsigned h = 2166136261u;
    for (unsigned i = 0; i < n; i++) {
        h ^= p[i];
        h *= 16777619u;
    }
    return h;
}

typedef struct {
    int lo;
    int hi;
} Range;

static Range widen(Range r, int by) {
    r.lo -= by;
    r.hi += by;
    return r;
}

int main(void) {
    for (int i = 0; i < 512; i++) {
        data[i] = (int)(xorshift() % 10000);
    }
    isort(data, 512);

    long long check = 0;
    for (int i = 0; i < 512; i++) {
        check = check * 31 + data[i];
    }
    put(check);
    put(data[0]);
    put(data[511]);

    // Bytes of an int array, which is the point of the case.
    put((long long)fnv((const unsigned char *)data, sizeof data));

    Range r = widen((Range){3, 9}, 4);
    put(r.lo * 1000 + r.hi);

    long long bits = 0;
    for (int i = 0; i < 64; i++) {
        if ((state >> i) & 1) {
            bits++;
        }
    }
    put(bits);

    return 0;
}
