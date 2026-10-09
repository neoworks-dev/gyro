/* A C library written the way a C library is: it holds what the caller
 * gave it without being told what that is.
 *
 * `void *` is the only type C has for an address it will not describe,
 * and it is what every registry, callback table and generic container in
 * C is written over. In METALS that is `anyptr` — one word, comparable,
 * and with nothing to load through — and in Gyro it is the type of the
 * same name. Neither end converts anything: the address that goes in is
 * the address that comes out, because it never stopped being one. */

#ifndef STORE_H
#define STORE_H

struct Cell {
    int value;
};

/* Down to an address, and back up to the type the caller knew all
 * along. Both directions are total and neither moves anything. */
void *erase(struct Cell *c);
int read_through(void *p);

/* Holding what it was handed, which is what a registry does. */
void keep(int slot, void *p);
void *held(int slot);
int kept(void);

/* The absent address, which C spells as a null pointer and Gyro as the
 * null in `anyptr?`. */
void *nothing(void);

/* Two addresses are equal when they are one address; nothing here knows
 * what is at either. */
int same(void *a, void *b);

/* sizeof, which is the one measurement an address answers whatever it
 * points at. */
int address_size(void);

#endif
