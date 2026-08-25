#ifndef _STDIO_H
#define _STDIO_H

#include <stdarg.h>
#include <stddef.h>

#define EOF (-1)

/* There is no FILE, because there are no files: the floor is stdout,
 * stderr and stdin, and nothing here can open anything else. FILE is a
 * distinct incomplete type so that `FILE *` declarations compile, and
 * stdout/stderr are the only two values it ever has. */
typedef struct _GY_FILE FILE;

extern FILE *stdout;
extern FILE *stderr;
extern FILE *stdin;

int putchar(int c);
int puts(const char *s);
int getchar(void);

int fputc(int c, FILE *stream);
int fputs(const char *s, FILE *stream);
size_t fwrite(const void *data, size_t size, size_t count, FILE *stream);
int fflush(FILE *stream);

int printf(const char *format, ...);
int fprintf(FILE *stream, const char *format, ...);
int sprintf(char *out, const char *format, ...);
int snprintf(char *out, size_t limit, const char *format, ...);
int vprintf(const char *format, va_list args);
int vfprintf(FILE *stream, const char *format, va_list args);
int vsnprintf(char *out, size_t limit, const char *format, va_list args);

#endif
