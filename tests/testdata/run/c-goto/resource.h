/* goto, in the three shapes C actually uses it in.
 *
 * METALS has structured control flow and no jump: `br $L` leaves an
 * enclosing `block`, and `br $L` to a `loop` restarts it. That is
 * WebAssembly's shape and it is deliberate — a format with a jump is a
 * format whose consumers all need a control-flow graph.
 *
 * C's labels already nest, and the two directions want two different
 * wrappers. A label jumped to from earlier gets a `block` around
 * everything before it, so branching out of that block lands where the
 * label is. One jumped to from later gets a `loop` around everything
 * from it onwards, so branching to that loop restarts it — which lands
 * in the same place — and running off the end falls out.
 *
 * The three shapes below are what `goto` is written for, and all three
 * are a label in the goto's own statement list or an enclosing one. */

/* One exit, reached from several places, with the cleanup written once.
 * fail_at says which step fails; the answer encodes how far it got and
 * what it returned. */
int acquire_and_release(int fail_at);

/* How many times the cleanup ran, which is once per call however the
 * call ended. */
int releases(void);

/* A backward goto is a loop, and one written this way behaves like the
 * `while` it stands in for. */
int sum_by_backward_goto(int upto);

/* Breaking out of nested loops, which is the thing C has no keyword
 * for. */
int first_pair_multiplying_to(int product);

/* A label reached from both sides gets both wrappers, and each goto
 * takes the one that encloses it. */
int retry_until(int rounds);

/* Several labels in one list nest, later outside earlier, so a jump over
 * one label to the next reaches it. */
int skip_a_stage(int from);

/* A local declared before a label is still there after it, which is why
 * a function with labels reserves its storage where no branch can jump
 * past it. */
int locals_survive(void);
