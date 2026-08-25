#ifndef _ASSERT_H
#define _ASSERT_H

#ifdef NDEBUG
#define assert(cond) ((void)0)
#else
_Noreturn void __gy_assert_failed(const char *cond, const char *file, int line);
#define assert(cond) ((cond) ? (void)0 : __gy_assert_failed(#cond, __FILE__, __LINE__))
#endif

#endif
