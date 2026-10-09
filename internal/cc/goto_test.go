package cc

import "testing"

// goto.
//
// METALS has structured control flow and no jump, so a goto has to
// become a branch out of, or back to the top of, something that encloses
// it. C's labels already nest, and the two directions want two different
// wrappers: a label jumped to from earlier gets a `block` around
// everything before it, and one jumped to from later gets a `loop`
// around everything from it onwards. Branching out of the first lands
// where the label is, and branching to the second restarts it, which
// lands in the same place.

// The pattern goto exists for: one exit, reached from several places,
// with the cleanup written once.
func TestForwardGotoToCleanup(t *testing.T) {
	answers(t, `
static int steps;

static int run(int fail_at) {
    int rc = 0;
    steps = 0;

    steps++;
    if (fail_at == 1) { rc = 1; goto done; }
    steps++;
    if (fail_at == 2) { rc = 2; goto done; }
    steps++;
    rc = 0;

done:
    /* The cleanup runs on every path, which is the whole point. */
    steps = steps * 10 + rc;
    return steps;
}

int result(void) {
    if (run(1) != 11) { return 1; }
    if (run(2) != 22) { return 2; }
    if (run(0) != 30) { return 3; }
    return 42;
}
`, 42)
}

// A backward goto is a loop, and running off the end of the region falls
// out of it the way the statements after the label would.
func TestBackwardGotoIsALoop(t *testing.T) {
	answers(t, `
int result(void) {
    int i = 0;
    int total = 0;

again:
    total += i;
    i++;
    if (i < 5) { goto again; }

    /* 0 + 1 + 2 + 3 + 4, and then the statements after the region. */
    return total + 32;
}
`, 42)
}

// A label reached from both sides gets both wrappers, and each goto
// takes the one that encloses it.
func TestGotoFromBothSides(t *testing.T) {
	answers(t, `
int result(void) {
    int n = 0;
    int rounds = 0;

    if (n == 0) { goto middle; }
    n = 100;

middle:
    rounds++;
    n += 20;
    if (rounds < 2) { goto middle; }

    return n + 2;
}
`, 42)
}

// Breaking out of nested loops, which is the other thing goto is for and
// the one C has no keyword for.
func TestGotoOutOfNestedLoops(t *testing.T) {
	answers(t, `
int result(void) {
    int i;
    int j;
    int found = 0;

    for (i = 0; i < 10; i++) {
        for (j = 0; j < 10; j++) {
            if (i * j == 42) {
                found = i * 100 + j;
                goto out;
            }
        }
    }
    found = -1;

out:
    if (found != 607) { return 1; }
    return 42;
}
`, 42)
}

// A goto from inside a loop back to a label outside it, which is a
// branch to a loop several levels up.
func TestGotoBackwardsOutOfALoop(t *testing.T) {
	answers(t, `
int result(void) {
    int passes = 0;
    int total = 0;
    int i;

restart:
    passes++;
    for (i = 0; i < 3; i++) {
        total += i;
        if (i == 1 && passes < 3) { goto restart; }
    }

    /* pass 1: 0+1, pass 2: 0+1, pass 3: 0+1+2. */
    if (total != 5) { return 1; }
    if (passes != 3) { return 2; }
    return 42;
}
`, 42)
}

// Several labels in one list nest, later outside earlier, so a goto from
// anywhere before a label reaches it.
func TestSeveralLabels(t *testing.T) {
	answers(t, `
int result(void) {
    int trace = 0;

    /* A jump over the first label to the second, which is what needs
     * the later label's block to enclose the earlier one's. */
    goto second;

first:
    trace = trace * 10 + 1;

second:
    trace = trace * 10 + 2;
    if (trace == 2) { goto third; }

    trace = trace * 10 + 9;

third:
    if (trace != 2) { return 1; }
    return 42;
}
`, 42)
}

// A label the program never jumps to costs nothing and changes nothing.
func TestUnusedLabel(t *testing.T) {
	answers(t, `
int result(void) {
    int n = 40;
unused:
    n += 2;
    return n;
}
`, 42)
}

// A local declared before a label is still there after it. The wrapper
// hides what is assigned inside it from the code that follows, which is
// why the storage is reserved at the top of a function that has labels.
func TestLocalsSurviveAWrapper(t *testing.T) {
	answers(t, `
int result(void) {
    int a = 40;
    int b[2];
    b[0] = 1;
    b[1] = 1;
    if (a == 40) { goto here; }
    a = 0;

here:
    return a + b[0] + b[1];
}
`, 42)
}

func TestGotoRefusals(t *testing.T) {
	refused(t, `int result(void) { goto nowhere; }`, "names no label in result")

	refused(t, `int result(void) { twice: twice: return 0; }`,
		"labels two statements in one function")

	// A label in one nested statement list reached from another is a
	// jump into a block, and no shape here expresses it.
	refused(t, `
int result(void) {
    { goto inner; }
    { inner: return 0; }
}
`, "jumps into a block")

	// A label that is not in a statement list has nothing to wrap.
	refused(t, `int result(void) { if (1) here: return 0; goto here; }`,
		"not in a block of its own")

	// A backward jump that reaches past a later forward label would put
	// the loop across the block's edge, which is the shape a relooper
	// is for. It is the one thing this refuses that is otherwise
	// ordinary C.
	refused(t, `
int result(void) {
    int n = 0;
early:
    n++;
    if (n > 5) { goto late; }
    n += 2;
late:
    if (n < 20) { goto early; }
    return 0;
}
`, "takes a control-flow graph")

	// A label inside a switch body is a case label's neighbour, and the
	// body is a multiway branch into a linear run rather than something
	// a branch can leave.
	refused(t, `
int result(void) {
    int n = 0;
    switch (n) {
    case 0:
        inside: n++;
        if (n < 3) { goto inside; }
    }
    return n;
}
`, "inside a switch body")

	// And one reached from outside the switch is a jump into a block,
	// which is the same refusal as any other list this branch is not in.
	refused(t, `
int result(void) {
    int n = 0;
    switch (n) {
    case 0:
        inside: return 1;
    }
    goto inside;
}
`, "jumps into a block")
}
