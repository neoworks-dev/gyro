/* The driver for the C this case was ported from. jsmn.h beside it is
 * Serge Zaitsev's file (MIT), copied in unmodified and built in its
 * default configuration — no JSMN_STRICT, no JSMN_PARENT_LINKS — which
 * is what the port covers.
 *
 * Everything here mirrors main.gyro line for line: the same document,
 * the same pool size, the same round count, the same accumulator, the
 * same two lines of output. The comparison only means something because
 * the two programs print the same bytes, and the harness fails the case
 * when they do not.
 */

#include <stdio.h>
#include <string.h>
#include <time.h>

#define JSMN_STATIC
#include "jsmn.h"

#define POOL 128
#define ROUNDS 20000

static const char *DOCUMENT =
    "{ \"height\":10, \"layers\":[ { \"data\":[6,6], \"height\":10, "
    "\"name\":\"Calque de Tile 1\", \"opacity\":1, \"type\":\"tilelayer\", "
    "\"visible\":true, \"width\":10, \"x\":0, \"y\":0 }], "
    "\"orientation\":\"orthogonal\", \"properties\": { }, \"tileheight\":32, "
    "\"tilesets\":[ { \"firstgid\":1, \"image\":\"..\\/images\\/tiles.png\", "
    "\"imageheight\":64, \"imagewidth\":160, \"margin\":0, \"name\":\"Tiles\", "
    "\"properties\":{}, \"spacing\":0, \"tileheight\":32, \"tilewidth\":32 "
    "}], \"tilewidth\":32, \"version\":1, \"width\":10 }";

static long long monotonic(void) {
  struct timespec ts;
  clock_gettime(CLOCK_MONOTONIC, &ts);
  return (long long)ts.tv_sec * 1000000000LL + (long long)ts.tv_nsec;
}

int main(void) {
  size_t length = strlen(DOCUMENT);
  static jsmntok_t tokens[POOL];

  long long start = monotonic();
  unsigned long long acc = 0;
  for (long long round = 0; round < ROUNDS; round += 1) {
    jsmn_parser parser;
    jsmn_init(&parser);
    int result = jsmn_parse(&parser, DOCUMENT, length, tokens, POOL);
    acc = acc * 31 + (unsigned long long)result;
    for (int i = 0; i < result; i += 1) {
      jsmntok_t token = tokens[i];
      acc = acc * 31 + (unsigned long long)token.type * 7;
      acc = acc * 31 + (unsigned long long)token.start * 3 +
            (unsigned long long)token.end;
      acc = acc * 31 + (unsigned long long)token.size;
    }
  }
  long long stop = monotonic();

  printf("%llu\n", acc);
  fprintf(stderr, "gyro:bench %lld\n", stop - start);
  return 0;
}
