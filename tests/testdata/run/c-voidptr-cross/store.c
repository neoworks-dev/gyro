#include "store.h"

#define SLOTS 4

static void *kept_slots[SLOTS];
static int filled;

void *erase(struct Cell *c) { return (void *)c; }

int read_through(void *p) { return ((struct Cell *)p)->value; }

void keep(int slot, void *p) {
    if (slot < 0 || slot >= SLOTS) {
        return;
    }
    if (kept_slots[slot] == 0 && p != 0) {
        filled++;
    }
    kept_slots[slot] = p;
}

void *held(int slot) {
    if (slot < 0 || slot >= SLOTS) {
        return 0;
    }
    return kept_slots[slot];
}

int kept(void) { return filled; }

void *nothing(void) { return 0; }

int same(void *a, void *b) { return a == b; }

int address_size(void) { return (int)sizeof(void *); }
