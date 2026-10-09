// External scanner for Gyro.
//
// Two tokens need context the generated lexer cannot see:
//
//   AUTOMATIC_TERMINATOR mirrors internal/lexer's needsTerminator: a newline
//   ends a statement when the parse state says one could end here (that is
//   the grammar's encoding of the "previous token could end a statement"
//   set) and the next real token is not one that can only continue an
//   expression.
//
//   BLOCK_COMMENT exists because /* ... */ nests, which a regular token
//   cannot express.
//
// The scanner is stateless, so serialization is empty.

#include "tree_sitter/parser.h"

enum TokenType {
  AUTOMATIC_TERMINATOR,
  BLOCK_COMMENT,
};

void *tree_sitter_gyro_external_scanner_create(void) { return NULL; }

void tree_sitter_gyro_external_scanner_destroy(void *payload) {}

unsigned tree_sitter_gyro_external_scanner_serialize(void *payload, char *buffer) {
  return 0;
}

void tree_sitter_gyro_external_scanner_deserialize(void *payload, const char *buffer,
                                                   unsigned length) {}

static void advance(TSLexer *lexer) { lexer->advance(lexer, false); }

static void skip_whitespace_char(TSLexer *lexer) { lexer->advance(lexer, true); }

static bool is_identifier_part(int32_t c) {
  return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
         (c >= '0' && c <= '9') || c > 127;
}

// consume_block_comment sits just past the opening "/*" and consumes the
// rest, honoring nesting. Returns at the character after the closing "*/",
// or at EOF for an unterminated comment.
static void consume_block_comment(TSLexer *lexer) {
  int depth = 1;
  while (depth > 0 && !lexer->eof(lexer)) {
    if (lexer->lookahead == '/') {
      advance(lexer);
      if (lexer->lookahead == '*') {
        depth++;
        advance(lexer);
      }
    } else if (lexer->lookahead == '*') {
      advance(lexer);
      if (lexer->lookahead == '/') {
        depth--;
        advance(lexer);
      }
    } else {
      advance(lexer);
    }
  }
}

// at_word reports whether the lookahead spells exactly the given word,
// consuming it. The caller has already committed to discarding this
// lookahead, so consumption is free.
static bool at_word(TSLexer *lexer, const char *word) {
  for (const char *c = word; *c != '\0'; c++) {
    if (lexer->lookahead != *c) {
      return false;
    }
    advance(lexer);
  }
  return !is_identifier_part(lexer->lookahead);
}

// continues_expression mirrors lexer.continuesExpression: the tokens that
// cannot begin a statement, so a newline before them does not terminate.
// `-`, `*` and `&` are absent on purpose (a statement can begin with them),
// and `<` continues unless it is `<-`.
static bool continues_expression(TSLexer *lexer) {
  switch (lexer->lookahead) {
    case '.':
    case ',':
    case ')':
    case ']':
    case '+':
    case '/':
    case '%':
    case '|':
    case '^':
    case '>':
    case '=':
      return true;
    case '<':
      advance(lexer);
      return lexer->lookahead != '-';
    case '!':
      advance(lexer);
      return lexer->lookahead == '=';
    case '&':
      advance(lexer);
      return lexer->lookahead == '&';
    case 'a':
      return at_word(lexer, "as");
    default:
      return false;
  }
}

// scan_terminator consumes horizontal whitespace and, at a newline, decides
// whether that newline ends a statement. The token is the newline itself;
// everything examined past it is lookahead beyond mark_end and is re-lexed
// afterwards, comments included.
static bool scan_terminator(TSLexer *lexer) {
  while (lexer->lookahead == ' ' || lexer->lookahead == '\t' || lexer->lookahead == '\r') {
    skip_whitespace_char(lexer);
  }
  if (lexer->lookahead != '\n') {
    return false;
  }
  advance(lexer);
  lexer->mark_end(lexer);

  // Look past further blank lines and comments for the first real token,
  // the way the compiler's lexer skips space and comments before deciding.
  for (;;) {
    if (lexer->lookahead == ' ' || lexer->lookahead == '\t' ||
        lexer->lookahead == '\r' || lexer->lookahead == '\n') {
      advance(lexer);
      continue;
    }
    if (lexer->lookahead == '/') {
      advance(lexer);
      if (lexer->lookahead == '/') {
        while (lexer->lookahead != '\n' && !lexer->eof(lexer)) {
          advance(lexer);
        }
        continue;
      }
      if (lexer->lookahead == '*') {
        advance(lexer);
        consume_block_comment(lexer);
        continue;
      }
      // A bare '/' is division, which continues the expression.
      return false;
    }
    break;
  }

  if (lexer->eof(lexer)) {
    lexer->result_symbol = AUTOMATIC_TERMINATOR;
    return true;
  }
  if (continues_expression(lexer)) {
    return false;
  }
  lexer->result_symbol = AUTOMATIC_TERMINATOR;
  return true;
}

bool tree_sitter_gyro_external_scanner_scan(void *payload, TSLexer *lexer,
                                            const bool *valid_symbols) {
  // Skip horizontal whitespace here: the generated lexer folds whitespace
  // extras into its own skip states and does not consult this scanner
  // again afterwards, so a "/*" after spaces would otherwise be missed.
  while (lexer->lookahead == ' ' || lexer->lookahead == '\t' || lexer->lookahead == '\r') {
    skip_whitespace_char(lexer);
  }

  if (lexer->lookahead == '/') {
    advance(lexer);
    if (lexer->lookahead != '*') {
      // Division or a line comment; the generated lexer handles both.
      return false;
    }
    advance(lexer);
    consume_block_comment(lexer);
    lexer->mark_end(lexer);
    lexer->result_symbol = BLOCK_COMMENT;
    return true;
  }

  if (valid_symbols[AUTOMATIC_TERMINATOR]) {
    return scan_terminator(lexer);
  }
  return false;
}
