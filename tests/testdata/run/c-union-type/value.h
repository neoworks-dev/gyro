union Value {
    long i;
    double f;
    unsigned char bytes[8];
};

struct Boxed {
    int kind;
    union Value value;
};

union Value make_int(long n);
long int_of(union Value v);
void store_double(union Value *out, double f);
struct Boxed boxed(int kind, long n);
int kind_of(const struct Boxed *b);
long value_size(void);
long boxed_size(void);
