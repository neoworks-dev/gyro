/* string.h. Straightforward, and deliberately so: clang lowers a
 * written-out copy loop to memory.copy where it can, so the clever
 * word-at-a-time versions would be undone anyway. */

#include <stdlib.h>
#include <string.h>

void *memcpy(void *dst, const void *src, size_t n) {
    unsigned char *d = dst;
    const unsigned char *s = src;
    for (size_t i = 0; i < n; i++) d[i] = s[i];
    return dst;
}

void *memmove(void *dst, const void *src, size_t n) {
    unsigned char *d = dst;
    const unsigned char *s = src;
    if (d == s || n == 0) return dst;
    if (d < s) {
        for (size_t i = 0; i < n; i++) d[i] = s[i];
    } else {
        for (size_t i = n; i > 0; i--) d[i - 1] = s[i - 1];
    }
    return dst;
}

void *memset(void *dst, int c, size_t n) {
    unsigned char *d = dst;
    for (size_t i = 0; i < n; i++) d[i] = (unsigned char)c;
    return dst;
}

int memcmp(const void *a, const void *b, size_t n) {
    const unsigned char *x = a, *y = b;
    for (size_t i = 0; i < n; i++) {
        if (x[i] != y[i]) return x[i] < y[i] ? -1 : 1;
    }
    return 0;
}

void *memchr(const void *s, int c, size_t n) {
    const unsigned char *p = s;
    for (size_t i = 0; i < n; i++) {
        if (p[i] == (unsigned char)c) return (void *)(p + i);
    }
    return NULL;
}

size_t strlen(const char *s) {
    size_t n = 0;
    while (s[n]) n++;
    return n;
}

size_t strnlen(const char *s, size_t n) {
    size_t i = 0;
    while (i < n && s[i]) i++;
    return i;
}

char *strcpy(char *dst, const char *src) {
    size_t i = 0;
    while ((dst[i] = src[i]) != '\0') i++;
    return dst;
}

/* strncpy pads with NULs to n and does not terminate a truncated copy,
 * which is the interface's oddity and not a bug here. */
char *strncpy(char *dst, const char *src, size_t n) {
    size_t i = 0;
    for (; i < n && src[i]; i++) dst[i] = src[i];
    for (; i < n; i++) dst[i] = '\0';
    return dst;
}

char *strcat(char *dst, const char *src) {
    strcpy(dst + strlen(dst), src);
    return dst;
}

char *strncat(char *dst, const char *src, size_t n) {
    char *end = dst + strlen(dst);
    size_t i = 0;
    for (; i < n && src[i]; i++) end[i] = src[i];
    end[i] = '\0';
    return dst;
}

int strcmp(const char *a, const char *b) {
    size_t i = 0;
    while (a[i] && a[i] == b[i]) i++;
    unsigned char x = (unsigned char)a[i], y = (unsigned char)b[i];
    if (x == y) return 0;
    return x < y ? -1 : 1;
}

int strncmp(const char *a, const char *b, size_t n) {
    for (size_t i = 0; i < n; i++) {
        unsigned char x = (unsigned char)a[i], y = (unsigned char)b[i];
        if (x != y) return x < y ? -1 : 1;
        if (x == '\0') return 0;
    }
    return 0;
}

/* strchr finds the terminator too, which callers rely on to reach the
 * end of a string. */
char *strchr(const char *s, int c) {
    char want = (char)c;
    for (;; s++) {
        if (*s == want) return (char *)s;
        if (*s == '\0') return NULL;
    }
}

char *strrchr(const char *s, int c) {
    char want = (char)c;
    const char *found = NULL;
    for (;; s++) {
        if (*s == want) found = s;
        if (*s == '\0') return (char *)found;
    }
}

char *strstr(const char *haystack, const char *needle) {
    if (*needle == '\0') return (char *)haystack;
    for (; *haystack; haystack++) {
        const char *h = haystack, *n = needle;
        while (*n && *h == *n) { h++; n++; }
        if (*n == '\0') return (char *)haystack;
    }
    return NULL;
}

static int in_set(char c, const char *set) {
    for (; *set; set++) {
        if (*set == c) return 1;
    }
    return 0;
}

size_t strspn(const char *s, const char *accept) {
    size_t n = 0;
    while (s[n] && in_set(s[n], accept)) n++;
    return n;
}

size_t strcspn(const char *s, const char *reject) {
    size_t n = 0;
    while (s[n] && !in_set(s[n], reject)) n++;
    return n;
}

char *strpbrk(const char *s, const char *accept) {
    for (; *s; s++) {
        if (in_set(*s, accept)) return (char *)s;
    }
    return NULL;
}

char *strdup(const char *s) {
    size_t n = strlen(s) + 1;
    char *out = malloc(n);
    if (out) memcpy(out, s, n);
    return out;
}
