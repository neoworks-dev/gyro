/* The driver for the C this case was ported from. bbc.h beside it is
 * Code Monkey King's `src/bbc/perft_test/bbc.c` from
 * github.com/maksimKorzh/chess_programming, copied in byte for byte —
 * only the extension differs, because the harness compiles every .c in
 * the directory and that file carries its own main(), which the include
 * below renames out of the way.
 *
 * bbc.c is GPL-3 (the upstream engine ships the licence as bbc/LICENCE).
 * It is compiled here only into this standalone benchmark binary, whose
 * output is compared against the Gyro port's; nothing of it is linked
 * into the Gyro compiler or its runtime.
 *
 * Everything here mirrors main.gyro line for line: the same six
 * positions, the same depth, table initialization outside the timed
 * region, the same two lines of output. The comparison only means
 * something because the three programs print the same bytes, and the
 * harness fails the case when they do not.
 */

#define main bbc_main
#include "bbc.h"
#undef main

#include <stdio.h>
#include <time.h>

#define START "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1 "
#define KIWIPETE "r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1 "
#define POSITION_3 "8/2p5/3p4/KP5r/1R3p1k/8/4P1P1/8 w - - 0 1 "
#define POSITION_4 "r3k2r/Pppp1ppp/1b3nbN/nP6/BBP1P3/q4N2/Pp1P2PP/R2Q1RK1 w kq - 0 1 "
#define POSITION_5 "rnbq1k1r/pp1Pbppp/2p5/8/2B5/8/PPP1NnPP/RNBQK2R w KQ - 1 8 "
#define POSITION_6 "r4rk1/1pp1qppp/p1np1n2/2b1p1B1/2B1P1b1/P1NP1N2/1PP1QPPP/R4RK1 w - - 0 10 "

#define DEPTH 3

static long long monotonic(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (long long)ts.tv_sec * 1000000000LL + (long long)ts.tv_nsec;
}

static long count(char *fen)
{
	parse_fen(fen);
	nodes = 0;
	perft_driver(DEPTH);
	return nodes;
}

int main(void)
{
	init_all();

	long long start = monotonic();
	long total = 0;
	total += count(START);
	total += count(KIWIPETE);
	total += count(POSITION_3);
	total += count(POSITION_4);
	total += count(POSITION_5);
	total += count(POSITION_6);
	long long stop = monotonic();

	printf("%ld\n", total);
	fprintf(stderr, "gyro:bench %lld\n", stop - start);
	return 0;
}
