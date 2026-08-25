#ifndef SCAN_H
#define SCAN_H

#define SCRATCH 64
#define NULL ((void *)0)

enum Keyword {
    KW_NONE = 0,
    KW_IF,
    KW_ELSE,
    KW_WHILE,
    KW_RETURN,
    KW_FOR
};

int keyword_count(void);
int token_count(void);
int token_id(int index);
int token_length(int index);
int token_char(int index, int at);
int reverse_token(int index);
int scratch_char(int at);
int checksum(void);
int pointer_width(void);

#endif /* SCAN_H */
