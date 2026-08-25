// A C program with a standard library: printf, malloc, string.h, the
// parsers, qsort and bsearch.
//
// None of it is linked from the host. internal/clibc is compiled in
// beside this file, and underneath it there are three imports and no
// more — write_byte, read_byte and exit, all taking scalars. It cannot
// be fd_write, because that takes a pointer into the guest's memory and
// a linear address is a u32; handing one to the runtime would make it a
// pointer at the boundary, which is the thing the sandbox rests on not
// doing. So stdio buffers on this side and drains a byte at a time.
//
// The golden is what C says these calls produce. It was checked against
// glibc as well, which is the useful cross-check for a library whose
// whole job is to behave like someone else's.

#include <ctype.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int cmp_int(const void *a, const void *b) {
    int x = *(const int *)a, y = *(const int *)b;
    return x < y ? -1 : (x > y ? 1 : 0);
}

int main(void) {
    // Conversions, flags, widths and precisions.
    printf("%d %05d %+d % d %x %X %#x %o\n", 42, 42, 42, 42, 255, 255, 255, 8);
    printf("[%6d][%-6d][%.4d][%6.4d]\n", 7, 7, 7, 7);
    printf("[%s][%10s][%-10s][%.3s]\n", "gyro", "right", "left", "truncate");
    printf("%c%c %lld %llu %zu\n", 'o', 'k', -9223372036854775807LL - 1,
           18446744073709551615ULL, (size_t)4096);
    printf("%*d|%.*f|%%\n", 5, 3, 2, 3.14159);

    // Floats, including the ties that separate half-to-even from
    // half-away-from-zero.
    printf("%.1f %.1f %.1f %.1f\n", 0.25, 0.35, 1.5, 2.5);
    printf("%f %.2f %.0f\n", 3.14159, 2.71828, 0.5);
    printf("%e %.2e %g %g %g\n", 1234.5678, 1234.5678, 100000.0, 1000000.0, 0.0001);
    printf("%f %f %f\n", 1.0 / 0.0, -1.0 / 0.0, 0.0);

    // The allocator, over memory.grow.
    char *text = malloc(64);
    strcpy(text, "malloc, then ");
    strcat(text, "strcat");
    puts(text);

    int *zeroed = calloc(16, sizeof(int));
    int all_zero = 1;
    for (int i = 0; i < 16; i++) {
        if (zeroed[i] != 0) all_zero = 0;
    }
    printf("calloc zeroed: %d\n", all_zero);

    text = realloc(text, 256);
    printf("realloc kept: %s\n", text);
    free(text);
    free(zeroed);

    // Enough allocation traffic to make the free list split, coalesce
    // and reuse rather than only ever growing.
    long total = 0;
    for (int round = 0; round < 200; round++) {
        void *a = malloc(24), *b = malloc(1000), *c = malloc(7);
        total += (a != NULL) + (b != NULL) + (c != NULL);
        free(b);
        free(a);
        free(c);
    }
    printf("allocations: %ld\n", total);

    // string.h
    printf("%s %s %s\n", strstr("hello world", "wor"), strchr("a,b", ','),
           strrchr("a.b.c", '.'));
    printf("%d %d %d %zu\n", strcmp("abc", "abd") < 0, strncmp("abc", "abd", 2),
           memcmp("ab", "ab", 2), strlen("gyro"));

    // Parsers.
    char *end;
    printf("%ld %ld %ld %ld\n", strtol("  -42rest", &end, 10), strtol("0x1f", NULL, 0),
           strtol("777", NULL, 8), strtol("z", NULL, 10));
    printf("end=%s\n", end);
    printf("%llu %d\n", strtoull("18446744073709551615", NULL, 10), atoi("123"));
    printf("%.3f %.3f\n", strtod("2.5e3", NULL), atof("-0.125"));

    // snprintf, including the truncating case whose return value is the
    // length the output would have had.
    char buf[16];
    int wanted = snprintf(buf, sizeof buf, "%s-%d", "abcdefgh", 12345);
    printf("%s (%d)\n", buf, wanted);
    wanted = snprintf(buf, 5, "%s", "truncated");
    printf("%s (%d)\n", buf, wanted);

    // qsort and bsearch.
    int values[] = {5, 3, 9, 1, 7, 3, 8, 2, 6, 0, 4};
    size_t count = sizeof values / sizeof values[0];
    qsort(values, count, sizeof values[0], cmp_int);
    for (size_t i = 0; i < count; i++) printf("%d", values[i]);
    printf("\n");

    int key = 7;
    int *found = bsearch(&key, values, count, sizeof values[0], cmp_int);
    printf("found %d\n", found ? *found : -1);

    // ctype answers nonzero rather than 1, so the corpus asks the
    // question C actually promises to answer.
    printf("%c%c %d %d %d\n", toupper('q'), tolower('Q'), isdigit('5') != 0,
           isalpha('5') != 0, isspace(' ') != 0);

    return 0;
}
