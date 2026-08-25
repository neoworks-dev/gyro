/* The driver for the C this case was ported from. md4c.c, md4c-html.c and
 * entity.c beside it are Martin Mitas' files (MIT), copied in unmodified
 * and driven the way md2html drives them with no flags: the CommonMark
 * dialect, no extensions, and MD_HTML_FLAG_SKIP_UTF8_BOM as the only
 * renderer flag — md2html's default minus MD_HTML_FLAG_DEBUG, which only
 * writes diagnostics to stderr.
 *
 * Everything here mirrors main.gyro line for line: the same document, the
 * same round count, the same FNV-1a accumulator over the rendered bytes,
 * the same two lines of output. The comparison only means something
 * because the two programs print the same bytes, and the harness fails the
 * case when they do not.
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "md4c-html.h"

#define ROUNDS 2000

static const char *DOCUMENT =
    "# md4c\n"
    "\n"
    "A *small* but **representative** document: it exercises every block\n"
    "and inline construct the CommonMark dialect has, so the hot region is\n"
    "the parser and not one branch of it.\n"
    "\n"
    "## Blocks\n"
    "\n"
    "> A block quote, with a [reference link][ref] inside it.\n"
    ">\n"
    "> - a tight list item\n"
    "> - another one\n"
    "\n"
    "1. an ordered item\n"
    "2. a second one\n"
    "\n"
    "   with a loose paragraph in it\n"
    "\n"
    "Setext heading\n"
    "==============\n"
    "\n"
    "    an indented code block\n"
    "    over two lines\n"
    "\n"
    "```c\n"
    "int main(void) { return 0; }\n"
    "```\n"
    "\n"
    "***\n"
    "\n"
    "## Inlines\n"
    "\n"
    "Text with `code spans`, <https://example.org/autolink>, an entity &amp;\n"
    "and &#x1F600;, an ![image](/img.png \"title\"), a hard break here  \n"
    "and a backslash escape \\*not emphasis\\*.\n"
    "\n"
    "<div class=\"raw\">\n"
    "  <p>raw HTML block</p>\n"
    "</div>\n"
    "\n"
    "Nested *emphasis with **strong** inside* and a [collapsed][] reference,\n"
    "plus a <span>raw span</span> and an <b>inline tag</b>.\n"
    "\n"
    "[ref]: /destination \"the title\"\n"
    "[collapsed]: /collapsed\n";

/* The rendered HTML, folded into the checksum as it arrives. */
typedef struct fold {
    unsigned long long acc;
    unsigned long long size;
} fold;

static void process_output(const MD_CHAR *text, MD_SIZE size, void *userdata) {
    fold *state = (fold *)userdata;
    MD_SIZE i;

    for (i = 0; i < size; i += 1) {
        state->acc = (state->acc ^ (unsigned long long)(unsigned char)text[i]) *
                     1099511628211ULL;
    }
    state->size += size;
}

static long long monotonic(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (long long)ts.tv_sec * 1000000000LL + (long long)ts.tv_nsec;
}

int main(void) {
    size_t length = strlen(DOCUMENT);
    long long round;

    long long start = monotonic();
    unsigned long long acc = 14695981039346656037ULL;
    for (round = 0; round < ROUNDS; round += 1) {
        fold state;
        state.acc = acc;
        state.size = 0;
        if (md_html(DOCUMENT, (MD_SIZE)length, process_output, &state, 0,
                    MD_HTML_FLAG_SKIP_UTF8_BOM) != 0) {
            fprintf(stderr, "md_html() failed\n");
            return 1;
        }
        acc = state.acc * 31 + state.size;
    }
    long long stop = monotonic();

    printf("%llu\n", acc);
    fprintf(stderr, "gyro:bench %lld\n", stop - start);
    return 0;
}
