/* A C file calling the C library with prototypes it wrote itself. Nothing
 * here defines malloc, memcpy, sqrt, strlen or qsort: each is an undefined
 * symbol the program resolves against libc and libm at link time. */

double sqrt(double x);
double floor(double x);
void *malloc(unsigned long size);
void free(void *block);
void *memcpy(void *to, const void *from, unsigned long size);
unsigned long strlen(const char *text);
void qsort(void *base, unsigned long count, unsigned long size,
           int (*compare)(const void *, const void *));

/* Squares 0..n-1 into one block, copies them into another with memcpy and
 * adds up the copy. */
int copied_sum(int n) {
    int *a = malloc(sizeof(int) * n);
    int *b = malloc(sizeof(int) * n);
    if (!a || !b) return -1;
    for (int i = 0; i < n; i++) a[i] = i * i;
    memcpy(b, a, sizeof(int) * n);
    int sum = 0;
    for (int i = 0; i < n; i++) sum += b[i];
    free(a);
    free(b);
    return sum;
}

double hypotenuse(double a, double b) { return sqrt(a * a + b * b); }

int whole_part(double x) { return (int)floor(x); }

/* strlen counts bytes up to the NUL of a literal that lives in this module. */
int greeting_length(void) { return (int)strlen("grüße"); }

static int descending(const void *l, const void *r) {
    int a = *(const int *)l, b = *(const int *)r;
    return (a < b) - (a > b);
}

/* qsort calls back into a function this file defines. */
int sorted_first(int n) {
    int *xs = malloc(sizeof(int) * n);
    for (int i = 0; i < n; i++) xs[i] = (i * 7) % n;
    qsort(xs, n, sizeof(int), descending);
    int first = xs[0];
    free(xs);
    return first;
}
