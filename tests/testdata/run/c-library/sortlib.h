/* A C header, read by internal/cc's own preprocessor.
 *
 * What crosses into Gyro is scalars, because a record declared inside a
 * module has no Gyro spelling yet — so the library owns its storage and
 * the interface is the one a stateful C library usually has anyway. */

#ifndef SORTLIB_H
#define SORTLIB_H

#define CAPACITY        64
#define CHECKSUM_SEED   2166136261u
#define CHECKSUM_PRIME  16777619u

typedef unsigned int u32;

enum Order { ASCENDING, DESCENDING };

void reset(void);
int  push(int value);
int  count(void);
int  at(int index);
void sort(int order);
int  find(int value);
int  median(void);
u32  checksum(void);

#endif
