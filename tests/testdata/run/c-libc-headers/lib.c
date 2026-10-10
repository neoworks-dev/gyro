/* A C file that includes the system's own headers. They are read the way a
 * C compiler reads them, so malloc, strlen, sqrt and toupper arrive with
 * the signatures libc declares and are resolved when the program links. */

#include <stdlib.h>
#include <string.h>
#include <math.h>
#include <stdint.h>
#include <stdbool.h>
#include <errno.h>
#include <ctype.h>

int64_t checksum(const char *text) {
    size_t n = strlen(text);
    int64_t sum = 0;
    for (size_t i = 0; i < n; i++) sum += toupper((unsigned char)text[i]);
    return sum;
}

bool is_square(int n) { double r = sqrt((double)n); return (int)r * (int)r == n; }

int *make(int n) { int *a = malloc(sizeof(int) * n); for (int i = 0; i < n; i++) a[i] = i; return a; }
void release(int *a) { free(a); }
