/* What a C program believes about its own storage.
 *
 * A C pointer is an optional pointer in METALS, because METALS' pointer
 * is never null and C's is. The question this file asks is whether that
 * costs anything C can observe: sizeof, the stride between two struct
 * elements, and whether a zeroed page of pointers reads back as nulls.
 * All three answers are C's own, which is the point of the niche. */

struct Node {
    char tag;
    int *data;
};

typedef struct Node Node;

struct Pair {
    int *first;
    int *second;
};

static int storage[8];
static Node nodes[4];
static int *table[4];

int pointer_size(void)
{
    return (int)sizeof(int *);
}

int char_pointer_size(void)
{
    return (int)sizeof(char *);
}

int pair_size(void)
{
    return (int)sizeof(struct Pair);
}

/* A char in front of a pointer pads out to the pointer's alignment, so
 * this is 16 wherever C is C, and 24 if the pointer were two words. */
int node_size(void)
{
    return (int)sizeof(Node);
}

int node_stride(void)
{
    return (int)(sizeof(nodes) / 4);
}

/* Module storage starts zeroed, and with the niche a zeroed word is the
 * null pointer, which is what a C program compiled anywhere else already
 * assumes about a file-scope array of pointers. */
int table_is_clear(void)
{
    int i;
    for (i = 0; i < 4; i = i + 1) {
        if (table[i] != 0) {
            return 0;
        }
    }
    return 1;
}

int fill_table(void)
{
    int i;
    for (i = 0; i < 4; i = i + 1) {
        table[i] = &storage[i * 2];
    }
    return table[3] - table[0];
}

int table_entry(int i)
{
    return table[i] - &storage[0];
}

/* A pointer that walks a struct array proves the stride the layout table
 * computed is the stride the generated code steps by. */
int tag_walk(void)
{
    Node *p;
    Node *end;
    int i;
    int total;

    end = &nodes[4];

    i = 0;
    for (p = &nodes[0]; p != end; p++) {
        p->tag = (char)(i + 1);
        p->data = &storage[i];
        i = i + 1;
    }

    total = 0;
    for (p = &nodes[0]; p != end; p++) {
        total = total * 10 + p->tag;
    }
    return total;
}

int data_offsets(void)
{
    int i;
    int total;

    total = 0;
    for (i = 0; i < 4; i = i + 1) {
        total = total + (int)(nodes[i].data - &storage[0]);
    }
    return total;
}
