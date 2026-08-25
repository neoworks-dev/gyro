/* An ordinary C translation unit: a header of its own, a file-scope
 * struct, a fixed array in module storage, and a quicksort that walks it
 * through pointers.
 *
 * Nothing here is written for this compiler. The point of the case is
 * that it does not have to be. */

#include "sortlib.h"

typedef struct {
    int values[CAPACITY];
    int n;
} Series;

static Series series;

void reset(void) {
    series.n = 0;
}

int count(void) {
    return series.n;
}

int push(int value) {
    if (series.n >= CAPACITY) {
        return 0;
    }
    series.values[series.n++] = value;
    return 1;
}

int at(int index) {
    if (index < 0 || index >= series.n) {
        return -1;
    }
    return series.values[index];
}

static void swap(int *a, int *b) {
    int t = *a;
    *a = *b;
    *b = t;
}

/* Lomuto partition: the pivot is the last element, and everything not
 * greater than it is moved to the front. */
static int partition(int *base, int n) {
    int pivot = base[n - 1];
    int i = 0;
    int j;

    for (j = 0; j < n - 1; j++) {
        if (base[j] <= pivot) {
            swap(base + i, base + j);
            i++;
        }
    }
    swap(base + i, base + n - 1);
    return i;
}

static void quicksort(int *base, int n) {
    if (n < 2) {
        return;
    }
    int p = partition(base, n);
    quicksort(base, p);
    quicksort(base + p + 1, n - p - 1);
}

static void reverse(int *base, int n) {
    if (n < 2) {
        return;
    }
    int *lo = base;
    int *hi = base + n - 1;

    while (lo < hi) {
        swap(lo, hi);
        lo++;
        hi--;
    }
}

void sort(int order) {
    quicksort(series.values, series.n);
    if (order == DESCENDING) {
        reverse(series.values, series.n);
    }
}

/* Binary search, which is only right on an ascending series. */
int find(int value) {
    int lo = 0;
    int hi = series.n - 1;

    while (lo <= hi) {
        int mid = lo + (hi - lo) / 2;
        int here = series.values[mid];
        if (here == value) {
            return mid;
        }
        if (here < value) {
            lo = mid + 1;
        } else {
            hi = mid - 1;
        }
    }
    return -1;
}

int median(void) {
    if (series.n == 0) {
        return 0;
    }
    return series.values[series.n / 2];
}

/* FNV-1a over the elements. Unsigned, so the multiply wraps at 32 bits
 * rather than overflowing — which is a thing C says and a thing the
 * three tiers have to agree about. */
u32 checksum(void) {
    u32 h = CHECKSUM_SEED;
    int i;

    for (i = 0; i < series.n; i++) {
        h = h ^ (u32)series.values[i];
        h = h * CHECKSUM_PRIME;
    }
    return h;
}
