package cc

// The headers this front end ships, because they describe the compiler
// rather than a library.
//
// There is no libc here — a C program calls what the system installed,
// and its headers are the system's — so the bundled set is only what a C
// compiler has to supply about itself: <stdarg.h>, <stddef.h>,
// <stdbool.h>, <limits.h> and <float.h>. Nothing in them is a function,
// and the builtins <stdarg.h> is written over are the front end's own.
// The compiler's own copies are written over macros only that compiler
// predefines, which is why they are not read; see system.go.

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

var bundled = map[string]string{
	"stdarg.h":  stdargH,
	"stddef.h":  stddefH,
	"stdbool.h": stdboolH,
	"limits.h":  limitsH,
	"float.h":   floatH,
}

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
typedef struct __va_list_tag __gnuc_va_list[1];

#define va_start(ap, last) __builtin_va_start(ap)
#define va_arg(ap, type)   __builtin_va_arg(ap, type)
#define va_end(ap)         ((void)0)
#define va_copy(dst, src)  ((dst)[0] = (src)[0])

#endif
`

// stddef.h. The system's headers ask for one name at a time with
// `__need_size_t` and its siblings and then include this, so each
// definition answers either to its own request or to a plain include.
// size_t is what an LP64 target says it is, which is the layout rule's
// word as an unsigned.
const stddefH = `#if !defined(__need_size_t) && !defined(__need_ptrdiff_t) && !defined(__need_wchar_t) && !defined(__need_NULL) && !defined(__need_offsetof) && !defined(__need_wint_t) && !defined(__need_max_align_t)
#define __GYRO_STDDEF_ALL
#endif

#if defined(__GYRO_STDDEF_ALL) || defined(__need_size_t)
#ifndef __size_t_defined_by_gyro
#define __size_t_defined_by_gyro
typedef unsigned long size_t;
#endif
#endif

#if defined(__GYRO_STDDEF_ALL) || defined(__need_ptrdiff_t)
#ifndef __ptrdiff_t_defined_by_gyro
#define __ptrdiff_t_defined_by_gyro
typedef long ptrdiff_t;
#endif
#endif

#if defined(__GYRO_STDDEF_ALL) || defined(__need_wchar_t)
#ifndef __wchar_t_defined_by_gyro
#define __wchar_t_defined_by_gyro
typedef int wchar_t;
#endif
#endif

#if defined(__GYRO_STDDEF_ALL) || defined(__need_wint_t)
#ifndef __wint_t_defined_by_gyro
#define __wint_t_defined_by_gyro
typedef unsigned int wint_t;
#endif
#endif

#if defined(__GYRO_STDDEF_ALL) || defined(__need_NULL)
#undef NULL
#define NULL ((void *)0)
#endif

#if defined(__GYRO_STDDEF_ALL) || defined(__need_offsetof)
#undef offsetof
#define offsetof(type, member) ((size_t)&((type *)0)->member)
#endif

#undef __need_size_t
#undef __need_ptrdiff_t
#undef __need_wchar_t
#undef __need_wint_t
#undef __need_NULL
#undef __need_offsetof
#undef __need_max_align_t
#undef __GYRO_STDDEF_ALL
`

// stdbool.h. _Bool is the language's own and the three names are macros,
// as the standard has them.
const stdboolH = `#ifndef __GYRO_STDBOOL_H
#define __GYRO_STDBOOL_H

#define bool _Bool
#define true 1
#define false 0
#define __bool_true_false_are_defined 1

#endif
`

// limits.h for the target the layout rule describes: LP64, char signed.
const limitsH = `#ifndef __GYRO_LIMITS_H
#define __GYRO_LIMITS_H

#define CHAR_BIT 8
#define SCHAR_MIN (-128)
#define SCHAR_MAX 127
#define UCHAR_MAX 255
#define CHAR_MIN SCHAR_MIN
#define CHAR_MAX SCHAR_MAX
#define SHRT_MIN (-32768)
#define SHRT_MAX 32767
#define USHRT_MAX 65535
#define INT_MIN (-INT_MAX - 1)
#define INT_MAX 2147483647
#define UINT_MAX 4294967295U
#define LONG_MIN (-LONG_MAX - 1L)
#define LONG_MAX 9223372036854775807L
#define ULONG_MAX 18446744073709551615UL
#define LLONG_MIN (-LLONG_MAX - 1LL)
#define LLONG_MAX 9223372036854775807LL
#define ULLONG_MAX 18446744073709551615ULL

#include_next <limits.h>

#endif
`

// float.h, the parts that are constants.
const floatH = `#ifndef __GYRO_FLOAT_H
#define __GYRO_FLOAT_H

#define FLT_RADIX 2
#define FLT_MANT_DIG 24
#define DBL_MANT_DIG 53
#define FLT_DIG 6
#define DBL_DIG 15
#define FLT_MIN_EXP (-125)
#define DBL_MIN_EXP (-1021)
#define FLT_MAX_EXP 128
#define DBL_MAX_EXP 1024
#define FLT_MAX 3.40282346638528859812e+38F
#define DBL_MAX 1.79769313486231570815e+308
#define FLT_MIN 1.17549435082228750797e-38F
#define DBL_MIN 2.22507385850720138309e-308
#define FLT_EPSILON 1.19209289550781250000e-7F
#define DBL_EPSILON 2.22044604925031308085e-16

#endif
`
