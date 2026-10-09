#include "filter.h"

static int data[6] = {3, 8, 1, 12, 7, 4};

int count_kept(keep_fn keep) {
    int i;
    int n = 0;
    for (i = 0; i < 6; i++) {
        if (keep(data[i])) {
            n++;
        }
    }
    return n;
}

int fold(fold_fn step, int start) {
    int i;
    int acc = start;
    for (i = 0; i < 6; i++) {
        acc = step(acc, data[i]);
    }
    return acc;
}

int fold_or(fold_fn step, int start, int fallback) {
    if (step == 0) {
        return fallback;
    }
    return fold(step, start);
}

int rule_size(void) { return (int)sizeof(struct Rule); }

int apply_rule(struct Rule r, int v) {
    if (r.keep == 0) {
        return -1;
    }
    return r.code * 100 + r.keep(v);
}

static int builtin_add(int a, int b) { return a + b; }

fold_fn adder(void) { return builtin_add; }

int round_trip(fold_fn step) { return step(40, 2); }
