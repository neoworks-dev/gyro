/* The floor. Three imports, all scalars, and nothing else crosses.
 *
 * These are not part of C's interface; they are what the rest of this
 * libc is built on. A program may call them, but everything above is a
 * better way to say the same thing. */

#ifndef _GYRO_H
#define _GYRO_H

#define GY_IMPORT(name) \
    __attribute__((import_module("gyro:rt"), import_name(name)))

/* fd is 1 for stdout and 2 for stderr; anything else is stdout. */
GY_IMPORT("write_byte") void __gy_write_byte(int fd, int byte);

/* The next byte of standard input, or -1 at end of file. */
GY_IMPORT("read_byte") int __gy_read_byte(void);

GY_IMPORT("exit") _Noreturn void __gy_exit(int code);

#endif
