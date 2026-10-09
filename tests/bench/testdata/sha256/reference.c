/* The driver for the C this case was ported from. sha256.c and sha256.h
 * beside it are crypto-algorithms' files by Brad Conte
 * (brad AT bradconte.com), copied in unmodified.
 *
 * Everything here mirrors main.gyro line for line: the same buffer, the
 * same round count, the same accumulator, the same two lines of output.
 * The comparison only means something because the two programs print the
 * same bytes, and the harness fails the case when they do not.
 */

#include <stdio.h>
#include <time.h>

#include "sha256.h"

#define SIZE 4096
#define ROUNDS 1000

static long long monotonic(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (long long)ts.tv_sec * 1000000000LL + (long long)ts.tv_nsec;
}

int main(void)
{
	static BYTE data[SIZE];
	for (long long i = 0; i < SIZE; i += 1)
		data[i] = (BYTE)((i * 31 + 7) & 0xff);

	/* Each round seeds the next one from the digest of the last, so no
	 * compiler on either side can hoist the hash out of the loop. */
	long long start = monotonic();
	unsigned long long acc = 0;
	for (long long round = 0; round < ROUNDS; round += 1) {
		data[0] = (BYTE)(acc & 0xff);
		BYTE digest[SHA256_BLOCK_SIZE];
		SHA256_CTX ctx;
		sha256_init(&ctx);
		sha256_update(&ctx, data, SIZE);
		sha256_final(&ctx, digest);
		for (int j = 0; j < SHA256_BLOCK_SIZE; ++j)
			acc = acc * 31 + digest[j];
	}
	long long stop = monotonic();

	printf("%llu\n", acc);
	fprintf(stderr, "gyro:bench %lld\n", stop - start);
	return 0;
}
