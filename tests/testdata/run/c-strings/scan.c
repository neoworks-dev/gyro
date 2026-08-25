/* A word scanner over string literals held in module storage.
 *
 * Nothing here is written for this compiler. The keyword table is an
 * array of pointers to string literals, which is the shape every C
 * lookup table has; the text is a `const char *` to another; and the
 * walk is `const char *p` stepping a byte at a time until it finds the
 * terminator C put there.
 *
 * The two kinds of file-scope initializer are both in it. `scratch` and
 * the literals are storage a compiler can write out as bytes. `keywords`
 * and `text` hold addresses, which no byte image can carry, so they are
 * written before main by whatever the compiler arranges to run first. */

#include "scan.h"

static const char *keywords[] = {
    "if",
    "else",
    "while",
    "return",
    "for",
};

static const char *text = "for i return while stop if else done";

static char scratch[SCRATCH];

static int length(const char *s)
{
    const char *p;

    p = s;
    while (*p != '\0') {
        p++;
    }
    return (int)(p - s);
}

/* same compares n bytes of a against b, and insists a ends there. */
static int same(const char *a, const char *b, int n)
{
    int i;

    if (length(a) != n) {
        return 0;
    }
    for (i = 0; i < n; i++) {
        if (a[i] != b[i]) {
            return 0;
        }
    }
    return 1;
}

/* start_of returns the address the index'th word begins at, or NULL when
 * the text has fewer than that many. */
static const char *start_of(int index)
{
    const char *p;
    int seen;

    p = text;
    seen = 0;
    while (*p != '\0') {
        while (*p == ' ') {
            p++;
        }
        if (*p == '\0') {
            break;
        }
        if (seen == index) {
            return p;
        }
        seen++;
        while (*p != ' ' && *p != '\0') {
            p++;
        }
    }
    return NULL;
}

static int word_length(const char *p)
{
    const char *q;

    q = p;
    while (*q != ' ' && *q != '\0') {
        q++;
    }
    return (int)(q - p);
}

int keyword_count(void)
{
    return (int)(sizeof(keywords) / sizeof(keywords[0]));
}

int token_count(void)
{
    int n;

    n = 0;
    while (start_of(n) != NULL) {
        n++;
    }
    return n;
}

int token_length(int index)
{
    const char *p;

    p = start_of(index);
    if (p == NULL) {
        return -1;
    }
    return word_length(p);
}

int token_char(int index, int at)
{
    const char *p;

    p = start_of(index);
    if (p == NULL || at >= word_length(p)) {
        return -1;
    }
    return p[at];
}

int token_id(int index)
{
    const char *p;
    int n;
    int i;

    p = start_of(index);
    if (p == NULL) {
        return -1;
    }
    n = word_length(p);
    for (i = 0; i < keyword_count(); i++) {
        if (same(keywords[i], p, n)) {
            return i + 1;
        }
    }
    return KW_NONE;
}

/* reverse_token writes a word backwards into the scratch buffer, which
 * is file-scope storage that starts zeroed. */
int reverse_token(int index)
{
    const char *p;
    int n;
    int i;

    p = start_of(index);
    if (p == NULL) {
        return -1;
    }
    n = word_length(p);
    if (n >= SCRATCH) {
        return -1;
    }
    for (i = 0; i < n; i++) {
        scratch[i] = p[n - 1 - i];
    }
    scratch[n] = '\0';
    return n;
}

int scratch_char(int at)
{
    return scratch[at];
}

/* FNV-1a over the text, in the unsigned arithmetic C says wraps. */
int checksum(void)
{
    unsigned int h;
    const char *p;

    h = 2166136261u;
    for (p = text; *p != '\0'; p++) {
        h = h ^ (unsigned int)(unsigned char)*p;
        h = h * 16777619u;
    }
    return (int)(h & 0x7fffffffu);
}

/* A pointer is one machine word, and a C program is entitled to say so.
 * It is only true here because a nullable pointer is laid out inside the
 * pointer rather than beside it. */
int pointer_width(void)
{
    return (int)sizeof(const char *);
}
