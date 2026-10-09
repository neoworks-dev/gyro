/* The C side of the union fixture. A union reaches Gyro only through an
 * import, so the fixture's types are declared here. */

struct Pair {
    int x;
    int y;
};

union Value {
    long i;
    double f;
    unsigned char bytes[8];
    struct Pair pair;
    struct Pair *address;
    void *opaque;
    int (*callback)(int);
};

union Nested {
    union Value inner;
    unsigned long whole;
};

union Value make_value(long n) {
    union Value v;
    v.i = n;
    return v;
}

union Nested make_nested(unsigned long n) {
    union Nested v;
    v.whole = n;
    return v;
}

struct Pair make_pair(int x, int y) {
    struct Pair p;
    p.x = x;
    p.y = y;
    return p;
}
