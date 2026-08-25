#ifndef _ERRNO_H
#define _ERRNO_H

/* There are no syscalls to fail, so errno exists to be assignable and
 * is never set by anything here. */
extern int errno;

#define EDOM 33
#define ERANGE 34
#define EINVAL 22
#define ENOMEM 12

#endif
