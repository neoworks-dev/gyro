#ifndef VM_H
#define VM_H

#define STACK 16

enum Op {
    OP_HALT = 0,
    OP_PUSH,
    OP_DUP,
    OP_ADD,
    OP_MUL,
    OP_EQ,
    OP_NE
};

int run(void);
int depth(void);
int at(int index);
int digit_value(int c);
int scan(void);

#endif /* VM_H */
