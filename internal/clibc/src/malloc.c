/* malloc over memory.grow.
 *
 * A first-fit free list with splitting and coalescing, which is the
 * smallest allocator that does not degrade on the alloc/free/alloc
 * pattern real programs have. It is not dlmalloc and does not try to be:
 * there are no size classes and no bins, so a workload with many small
 * allocations of varied sizes will walk the list. That is the trade
 * taken for something readable enough to be obviously correct.
 *
 * The heap starts at __heap_base, which wasm-ld places after the static
 * data, and grows a page at a time. Nothing is ever returned to the
 * memory: memory.grow only grows. */

#include <stdlib.h>
#include <string.h>

extern unsigned char __heap_base;

#define PAGE_SIZE 65536
#define ALIGN 8

typedef struct block {
    size_t size; /* payload bytes, a multiple of ALIGN */
    struct block *next;
    int free;
} block;

static block *heap_head = NULL;
static unsigned char *heap_end = NULL;   /* first byte past the heap */
static unsigned char *heap_break = NULL; /* first unused byte */

static size_t round_up(size_t n) { return (n + (ALIGN - 1)) & ~(size_t)(ALIGN - 1); }

/* grow_by extends the break by at least n bytes, growing the memory in
 * whole pages when the current one runs out. */
static void *grow_by(size_t n) {
    if (heap_break == NULL) {
        heap_break = &__heap_base;
        /* memory.size is in pages and counts everything, static data
         * included, so the end of the memory is where the heap can run
         * to before it has to grow. */
        heap_end = (unsigned char *)(__builtin_wasm_memory_size(0) * (size_t)PAGE_SIZE);
        if (heap_break > heap_end) heap_end = heap_break;
    }

    if ((size_t)(heap_end - heap_break) < n) {
        size_t needed = n - (size_t)(heap_end - heap_break);
        size_t pages = (needed + PAGE_SIZE - 1) / PAGE_SIZE;
        if (__builtin_wasm_memory_grow(0, pages) == (unsigned long)-1) {
            return NULL;
        }
        heap_end += pages * (size_t)PAGE_SIZE;
    }

    void *out = heap_break;
    heap_break += n;
    return out;
}

/* split cuts a block down to want bytes when the tail is big enough to
 * be a block of its own. */
static void split(block *b, size_t want) {
    if (b->size < want + sizeof(block) + ALIGN) return;
    block *tail = (block *)((unsigned char *)(b + 1) + want);
    tail->size = b->size - want - sizeof(block);
    tail->free = 1;
    tail->next = b->next;
    b->size = want;
    b->next = tail;
}

void *malloc(size_t size) {
    if (size == 0) size = 1;
    size = round_up(size);

    block *previous = NULL;
    for (block *b = heap_head; b != NULL; previous = b, b = b->next) {
        if (b->free && b->size >= size) {
            split(b, size);
            b->free = 0;
            return b + 1;
        }
    }

    block *fresh = grow_by(sizeof(block) + size);
    if (fresh == NULL) return NULL;
    fresh->size = size;
    fresh->free = 0;
    fresh->next = NULL;
    if (previous == NULL) {
        heap_head = fresh;
    } else {
        previous->next = fresh;
    }
    return fresh + 1;
}

/* coalesce merges every run of adjacent free blocks. It runs on free
 * rather than on malloc so that a program which frees in a loop does not
 * accumulate a list of unmergeable fragments. */
static void coalesce(void) {
    for (block *b = heap_head; b != NULL && b->next != NULL;) {
        block *next = b->next;
        int adjacent = (unsigned char *)(b + 1) + b->size == (unsigned char *)next;
        if (b->free && next->free && adjacent) {
            b->size += sizeof(block) + next->size;
            b->next = next->next;
            continue; /* try to absorb the one after it too */
        }
        b = next;
    }
}

void free(void *p) {
    if (p == NULL) return;
    block *b = (block *)p - 1;
    b->free = 1;
    coalesce();
}

void *calloc(size_t count, size_t size) {
    size_t total = count * size;
    /* The multiply is 32-bit here, so an overflow would hand back a
     * block far smaller than asked for. */
    if (count != 0 && total / count != size) return NULL;
    void *out = malloc(total);
    if (out) memset(out, 0, total);
    return out;
}

void *realloc(void *p, size_t size) {
    if (p == NULL) return malloc(size);
    if (size == 0) {
        free(p);
        return NULL;
    }

    block *b = (block *)p - 1;
    size_t want = round_up(size);
    if (b->size >= want) {
        split(b, want);
        return p;
    }

    void *out = malloc(size);
    if (out == NULL) return NULL;
    memcpy(out, p, b->size);
    free(p);
    return out;
}
