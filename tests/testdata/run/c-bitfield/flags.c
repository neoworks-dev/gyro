#include "flags.h"

static struct ip_head head = { 4, 5, 16, 1500 };

union packed {
    unsigned low:4;
    unsigned wide:12;
};

int size_of_head(void) { return (int)sizeof(struct ip_head); }
int size_of_wide(void) { return (int)sizeof(struct wide); }
int size_of_signs(void) { return (int)sizeof(struct signs); }
int size_of_mixed(void) { return (int)sizeof(struct mixed); }

int head_version(void) { return (int)head.version; }
int head_ihl(void) { return (int)head.ihl; }
int head_tos(void) { return (int)head.tos; }
int head_length(void) { return (int)head.length; }

int neighbours_survive(void) {
    struct mixed m;
    m.tag = 5;
    m.kind = 31;
    m.count = 7;
    m.name[0] = 'a';

    m.kind = 0;
    if (m.tag != 5) { return 1; }
    if (m.count != 7) { return 2; }
    if (m.name[0] != 'a') { return 3; }

    m.tag = 0;
    m.kind = 17;
    if (m.tag != 0) { return 4; }
    return (int)m.kind;
}

int wide_round_trip(void) {
    struct wide w;
    w.first = 1073741823u; /* 2^30 - 1 */
    w.second = 1023u;      /* 2^10 - 1 */
    if (w.first != 1073741823u) { return 1; }
    if (w.second != 1023u) { return 2; }
    w.second = 0;
    if (w.first != 1073741823u) { return 3; }
    return 42;
}

int signed_reads_back(void) {
    struct signs s;
    s.low = 15;
    s.high = 15;
    if (s.low != -1) { return 1; }
    s.low = -8;
    if (s.low != -8) { return 2; }
    s.low = 7;
    return (int)s.low;
}

int unsigned_reads_back(void) {
    struct signs s;
    s.low = 0;
    s.high = 15;
    return (int)s.high;
}

int truncates_on_write(void) {
    struct signs s;
    s.low = 0;
    s.high = 0x1F;
    return (int)s.high;
}

int wraps_at_its_width(void) {
    struct signs s;
    s.low = 0;
    s.high = 15;
    s.high++;
    if (s.high != 0) { return 1; }
    s.high += 3;
    return (int)s.high;
}

int initialized_in_order(void) {
    struct mixed m = { 6, 7 };
    if (m.tag != 6) { return 1; }
    if (m.kind != 7) { return 2; }
    /* What an initializer leaves out is zero. */
    if (m.count != 0) { return 3; }
    if (m.name[0] != 0) { return 4; }
    return 42;
}

int file_scope_initializer(void) {
    return head.version * 10 + head.ihl;
}

int union_members_overlap(void) {
    union packed p;
    p.wide = 0x2FA;
    if (p.low != 10) { return 1; }
    p.low = 12;
    if (p.wide != 0x2FC) { return 2; }
    return 42;
}
