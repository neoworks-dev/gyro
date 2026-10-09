/* A stack machine, which is what `switch` is for.
 *
 * The dispatch is a switch over an opcode with a default that traps, the
 * two comparison opcodes share a group by falling through, and the
 * machine's state is `static` — file scope for the stack, block scope
 * for the counter that survives every call to `step`. None of that is
 * written for this compiler; it is what a bytecode interpreter looks
 * like in C. */

#include "vm.h"

static long stack[STACK];
static int top;

/* The program: an opcode and its operand, in pairs. It computes
 *   (7 + 5) * 3, then compares that against 36 and leaves the flag. */
static const int program[] = {
    OP_PUSH, 7,
    OP_PUSH, 5,
    OP_ADD, 0,
    OP_PUSH, 3,
    OP_MUL, 0,
    OP_DUP, 0,
    OP_PUSH, 36,
    OP_EQ, 0,
    OP_HALT, 0
};

static void push(long value)
{
    if (top < STACK) {
        stack[top] = value;
        top++;
    }
}

static long pop(void)
{
    if (top == 0) {
        return 0;
    }
    top--;
    return stack[top];
}

/* step runs one instruction and returns 1 while the machine is running.
 * The counter is a static local: block scoped, and it outlives the
 * frame, which is the whole reason C spells it that way. */
static int step(int op, int operand)
{
    static int executed;
    long a;
    long b;

    executed++;

    switch (op) {
    case OP_PUSH:
        push(operand);
        break;

    case OP_DUP:
        a = pop();
        push(a);
        push(a);
        break;

    case OP_ADD:
        b = pop();
        a = pop();
        push(a + b);
        break;

    case OP_MUL:
        b = pop();
        a = pop();
        push(a * b);
        break;

    /* Two labels, one group: falling out of the first runs the second,
     * which is what C's fallthrough is. */
    case OP_EQ:
    case OP_NE:
        b = pop();
        a = pop();
        if (op == OP_EQ) {
            push(a == b);
        } else {
            push(a != b);
        }
        break;

    case OP_HALT:
        return 0;

    default:
        push(-999);
        return 0;
    }
    return executed;
}

int run(void)
{
    int at;
    int alive;

    top = 0;
    at = 0;
    alive = 1;
    while (alive) {
        alive = step(program[at], program[at + 1]);
        at = at + 2;
    }
    return at / 2;
}

int depth(void)
{
    return top;
}

int at(int index)
{
    if (index < 0 || index >= top) {
        return -1;
    }
    return (int)stack[index];
}

/* A switch whose control expression is a char, which promotes to the
 * int the case labels already are. */
int digit_value(int c)
{
    switch ((char)c) {
    case '0': return 0;
    case '1': return 1;
    case '2': return 2;
    case '3': return 3;
    case 'a': case 'A': return 10;
    case 'b': case 'B': return 11;
    }
    return -1;
}

/* A switch inside a loop: `break` leaves the switch, `continue` leaves
 * the iteration. */
int scan(void)
{
    static const char *text = "a1b2!3z";
    const char *p;
    int total;

    total = 0;
    for (p = text; *p != '\0'; p++) {
        switch (*p) {
        case '!':
            continue;
        case 'z':
            break;
        default:
            total = total * 10 + digit_value(*p);
            continue;
        }
        total = total + 1000;
    }
    return total;
}
