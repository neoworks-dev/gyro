/* Pointers taken as numbers and numbers taken as pointers, the way real
 * C does it: a round trip through an integer, a tag kept in the low bit
 * of an aligned address, a field reached by adding its offset, and a
 * computed zero that is the null pointer. */

struct pair {
    long first;
    long second;
};

static struct pair the_pair = { 11, 22 };

int round_trip(void) {
    unsigned long n = (unsigned long)&the_pair;
    struct pair *back = (struct pair *)n;
    return back == &the_pair && back->second == 22;
}

/* A pair is 8-aligned, so the low bit of its address is free to say
 * something, and clearing it gives the pointer back. */
long tagged_second(void) {
    unsigned long tagged = (unsigned long)&the_pair | 1;
    int marked = (int)(tagged & 1);
    struct pair *p = (struct pair *)(tagged & ~1UL);
    return marked * 100 + p->second;
}

long second_by_offset(void) {
    unsigned long base = (unsigned long)&the_pair;
    long *second = (long *)(base + sizeof(long));
    *second = 33;
    return the_pair.second;
}

int zero_is_null(int zero) {
    char *p = (char *)zero;
    return p == 0;
}

/* The low 32 bits of an address survive a cast to a narrower int, and
 * an address that a long holds compares like the pointer did. */
int narrows_to_low_bits(void) {
    unsigned long n = (unsigned long)&the_pair;
    unsigned int low = (unsigned int)&the_pair;
    long signed_n = (long)&the_pair;
    return low == (unsigned int)n && (unsigned long)signed_n == n;
}
