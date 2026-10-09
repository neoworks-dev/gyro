package cc

// The headers this front end ships, because they describe the compiler
// rather than a library.
//
// There is no libc here — a C program compiled into a Gyro one calls
// what Gyro links, not what a system installed — so the bundled set is
// only what a C compiler has to supply about itself. <stdarg.h> is that:
// nothing in it is a function, and the two builtins it is written over
// are the front end's own.

// bundledHeader answers an angled `#include` from the set above, or
// reports that it is not one of them. A quoted include is never answered
// here: `"stdarg.h"` beside a program is that program's file, and taking
// it over would be the compiler outvoting the source.
func bundledHeader(path string, angled bool) (resolved, source string, ok bool) {
	if !angled {
		return "", "", false
	}
	text, found := bundled[path]
	return "<" + path + ">", text, found
}

var bundled = map[string]string{"stdarg.h": stdargH}

// stdarg.h. `va_list` is an array of one struct for the reason glibc's
// is: naming one decays to a pointer, so `vprintf(fmt, ap)` advances the
// cursor the caller declared rather than a copy of it, and `va_copy` has
// to say `[0]` on both sides because that is the one place C treats an
// array as an object.
//
// va_end does nothing and va_copy is a struct assignment, so neither
// needs a builtin — doing nothing and copying a struct are both things C
// can already say. The other two cannot be said: va_start reads the
// enclosing function's hidden parameters, and va_arg takes a type.
const stdargH = `#ifndef __GYRO_STDARG_H
#define __GYRO_STDARG_H

struct __va_list_tag {
    void **__next;
    long __left;
};

typedef struct __va_list_tag va_list[1];

#define va_start(ap, last) __builtin_va_start(ap)
#define va_arg(ap, type)   __builtin_va_arg(ap, type)
#define va_end(ap)         ((void)0)
#define va_copy(dst, src)  ((dst)[0] = (src)[0])

#endif
`
