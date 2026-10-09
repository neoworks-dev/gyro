#include "resource.h"

static int held;
static int released;

static int open_step(int n) { held += n; return n; }
static void release_all(void) { held = 0; released++; }

int acquire_and_release(int fail_at) {
    int got = 0;
    int rc = 0;

    got += open_step(1);
    if (fail_at == 1) { rc = -1; goto done; }

    got += open_step(2);
    if (fail_at == 2) { rc = -2; goto done; }

    got += open_step(4);
    rc = got;

done:
    /* Runs on every path out, which is the whole reason the goto is
     * there rather than three copies of this. */
    release_all();
    return rc * 100 + got;
}

int releases(void) { return released; }

int sum_by_backward_goto(int upto) {
    int i = 0;
    int total = 0;

again:
    if (i > upto) { goto out; }
    total += i;
    i++;
    goto again;

out:
    return total;
}

int first_pair_multiplying_to(int product) {
    int i;
    int j;
    int answer = -1;

    for (i = 1; i < 20; i++) {
        for (j = i; j < 20; j++) {
            if (i * j == product) {
                answer = i * 100 + j;
                goto found;
            }
        }
    }

found:
    return answer;
}

int retry_until(int rounds) {
    int seen = 0;
    int value = 0;

    if (rounds > 0) { goto attempt; }
    return -1;

attempt:
    seen++;
    value = value * 2 + 1;
    if (seen < rounds) { goto attempt; }
    return value * 10 + seen;
}

int skip_a_stage(int from) {
    int trace = 0;

    if (from == 2) { goto second; }
    if (from == 3) { goto third; }

    trace = trace * 10 + 1;

second:
    trace = trace * 10 + 2;

third:
    trace = trace * 10 + 3;
    return trace;
}

int locals_survive(void) {
    int base = 40;
    int extra[2];
    extra[0] = 1;
    extra[1] = 1;

    if (base == 40) { goto here; }
    base = 0;
    extra[0] = 0;

here:
    return base + extra[0] + extra[1];
}
