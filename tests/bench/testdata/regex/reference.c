/* The driver for the C this case was ported from. re.c and re.h beside
 * it are tiny-regex-c's files by Kim Bjerge / Rob Pike lineage, kept
 * unmodified.
 *
 * Everything here mirrors main.gyro line for line: the same pattern, the
 * same texts in the same order, the same round count, the same
 * accumulator, the same two lines of output. The comparison only means
 * something because the two programs print the same bytes, and the
 * harness fails the case when they do not.
 */

#include <stdio.h>
#include <time.h>

#include "re.h"

#define ROUNDS 5000

static const char *texts[] = {
	"%JxLLcVx8wxrjsj",
	"=KbvUQjsj",
	"^uDnoZjsj",
	"UzZbjsj",
	"zLa_FTEjsj",
	"\"mw3p8_Ojsj",
	"abcdefghijklmnopqrstuvwxyz0123456789",
	"the quick brown fox jumps over the lazy dog",
};

#define TEXT_COUNT ((int)(sizeof(texts) / sizeof(texts[0])))

static long long monotonic(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (long long)ts.tv_sec * 1000000000LL + (long long)ts.tv_nsec;
}

int main(void)
{
	re_t pattern = re_compile(".?\\w+jsj$");

	long long start = monotonic();
	unsigned long long acc = 0;
	for (long long round = 0; round < ROUNDS; round += 1) {
		for (int i = 0; i < TEXT_COUNT; ++i) {
			int length = 0;
			int offset = re_matchp(pattern, texts[i], &length);
			acc = acc * 31 + (unsigned long long)(offset + 1) * 7 +
			      (unsigned long long)length;
		}
	}
	long long stop = monotonic();

	printf("%llu\n", acc);
	fprintf(stderr, "gyro:bench %lld\n", stop - start);
	return 0;
}
