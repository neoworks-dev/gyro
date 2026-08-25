// The other two of the three floor imports: read_byte and exit.
//
// getchar reads one byte and answers EOF as -1, and returning a status
// from main is exit(status) — which a buffered libc has to see, or its
// last write never leaves. The synthesized entry calls __gy_finish after
// main for exactly that, which is also what carries the status out.

//gyro:stdin gyro
//gyro:stdin lang
//gyro:exit 4

#include <ctype.h>
#include <stdio.h>

int main(void) {
    int lines = 0, bytes = 0, letters = 0;
    for (;;) {
        int c = getchar();
        if (c == EOF) break;
        bytes++;
        if (c == '\n') {
            lines++;
        } else {
            putchar(toupper(c));
        }
        if (isalpha(c)) letters++;
    }
    printf("\nlines=%d bytes=%d letters=%d\n", lines, bytes, letters);

    // This text is still in the buffer when main returns. It appears
    // only because __gy_finish flushes.
    printf("unflushed at return");
    return 4;
}
