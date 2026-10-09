/* The driver for the C this case was ported from. cJSON.c and cJSON.h
 * beside it are Dave Gamble's files (MIT), copied in unmodified.
 *
 * Everything here mirrors main.gyro line for line: the same document, the
 * same round count, the same fold over the tree, the same two lines of
 * output. The comparison only means something because the two programs
 * print the same bytes, and the harness fails the case when they do not.
 */

#include <stdio.h>
#include <string.h>
#include <time.h>

#include "cJSON.h"

#define ROUNDS 2000

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

static unsigned long long fold(unsigned long long accumulator,
                               const cJSON *item) {
  unsigned long long acc = accumulator;
  const cJSON *child = NULL;

  if (item == NULL) {
    return accumulator;
  }

  acc = acc * 31 + (unsigned long long)(item->type & 0xFF);
  if (item->string != NULL) {
    acc = acc * 31 + (unsigned long long)strlen(item->string);
  }
  if (cJSON_IsNumber(item)) {
    acc = acc * 31 + (unsigned long long)(unsigned int)item->valueint;
  }
  if (cJSON_IsString(item) && item->valuestring != NULL) {
    acc = acc * 31 + (unsigned long long)strlen(item->valuestring);
  }

  for (child = item->child; child != NULL; child = child->next) {
    acc = fold(acc, child);
  }
  return acc;
}

int main(void) {
  long long start = monotonic();
  unsigned long long acc = 0;
  long long round = 0;

  for (round = 0; round < ROUNDS; round += 1) {
    cJSON *root = cJSON_Parse(DOCUMENT);
    char *printed = cJSON_PrintUnformatted(root);
    if (printed != NULL) {
      acc = acc * 31 + (unsigned long long)strlen(printed);
      cJSON_free(printed);
    }
    acc = fold(acc, root);
    cJSON_Delete(root);
  }

  long long stop = monotonic();

  printf("%llu\n", acc);
  fprintf(stderr, "gyro:bench %lld\n", stop - start);
  return 0;
}
