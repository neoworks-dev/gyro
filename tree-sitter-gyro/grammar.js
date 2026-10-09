/**
 * Tree-sitter grammar for Gyro.
 *
 * The source of truth for the syntax is the book in docs/ in the gyro repo;
 * the reference implementation is internal/parser. Two things need context
 * the LR lexer cannot see, so they live in the external scanner
 * (src/scanner.c):
 *
 *   - the automatic statement terminator, replicating the newline rules of
 *     internal/lexer (end-of-statement token set encoded in the parse
 *     states, continuation tokens checked by lookahead);
 *   - nested block comments.
 *
 * The grammar is deliberately a little more permissive than the compiler:
 * it exists for highlighting and structure, and the language server is the
 * authority on what actually compiles.
 */

const PREC = {
  range: 1,
  or: 2,
  and: 3,
  bit_or: 4,
  bit_xor: 5,
  bit_and: 6,
  equality: 7,
  comparison: 8,
  shift: 9,
  additive: 10,
  multiplicative: 11,
  cast: 12,
  unary: 13,
  postfix: 14,
};

// Type suffixes bind tighter than the pointer prefix, and `?` binds to the
// whole type: `*Node?` is a nullable pointer, `*i32[]` points at a slice.
const TYPE_PREC = {
  optional: 1,
  pointer: 2,
  suffix: 3,
};

function commaSep1(rule) {
  return seq(rule, repeat(seq(',', rule)), optional(','));
}

function commaSep(rule) {
  return optional(commaSep1(rule));
}

module.exports = grammar({
  name: 'gyro',

  externals: $ => [
    $._automatic_terminator,
    $.block_comment,
  ],

  extras: $ => [
    /[ \t\r\n]+/,
    $.line_comment,
    $.block_comment,
  ],

  word: $ => $.identifier,

  supertypes: $ => [
    $._declaration,
    $._statement,
    $._expression,
    $._type,
    $._pattern,
  ],

  conflicts: $ => [
    [$.path_expression, $.named_type],
    [$._expression, $.path_expression, $.named_type],
    [$.unary_expression, $.binary_expression, $.call_expression],
    [$.receive_expression, $.binary_expression, $.call_expression],
    [$.await_expression, $.binary_expression, $.call_expression],
    [$.binary_expression, $.call_expression],
    [$._expression, $.named_type],
    [$.named_type],
    [$._expression, $.type_arguments],
    [$.closure_parameter_list, $.function_type],
    [$.closure_parameter, $.named_type],
    // `i32[](xs)` in a match arm: the path is read as a pattern until the
    // `[` says it was a slice type's element instead.
    [$.variant_pattern, $.named_type],
    [$.if_statement],
    [$.elif_clause],
  ],

  rules: {
    // -----------------------------------------------------------------------
    // File structure
    // -----------------------------------------------------------------------

    source_file: $ => seq(
      repeat($._terminator),
      repeat(seq(
        choice($.import_declaration, $._declaration),
        repeat($._terminator),
      )),
    ),

    _terminator: $ => choice(';', $._automatic_terminator),

    import_declaration: $ => seq(
      'import',
      choice(
        field('path', $.string_literal),
        seq($.import_list, 'from', field('path', $.string_literal)),
        seq('*', 'as', field('alias', $.identifier), 'from', field('path', $.string_literal)),
      ),
    ),

    import_list: $ => seq('{', commaSep($.import_name), '}'),

    import_name: $ => seq(
      field('name', $.identifier),
      optional(seq('as', field('alias', $.identifier))),
    ),

    export_declaration: $ => seq(
      'export',
      choice(
        seq('*', 'from', field('path', $.string_literal)),
        seq($.import_list, optional(seq('from', field('path', $.string_literal)))),
      ),
    ),

    // -----------------------------------------------------------------------
    // Declarations
    // -----------------------------------------------------------------------

    _declaration: $ => choice(
      $.function_declaration,
      $.struct_declaration,
      $.union_declaration,
      $.enum_declaration,
      $.variable_declaration,
      $.export_declaration,
    ),

    decorator: $ => seq(
      '@',
      field('name', seq($.identifier, repeat(seq('::', $.identifier)))),
      optional(field('arguments', $.arguments)),
    ),

    function_declaration: $ => seq(
      repeat($.decorator),
      optional('export'),
      optional('async'),
      'fn',
      choice($._function_owner, $._function_plain_name),
      field('parameters', $.parameter_list),
      optional(seq(':', field('result', $._type))),
      optional($.effect_row),
      optional(field('body', $.block)),
    ),

    _function_plain_name: $ => seq(
      field('name', $.identifier),
      optional(field('type_parameters', $.type_parameters)),
    ),

    _function_owner: $ => seq(
      field('owner', alias($.identifier, $.type_identifier)),
      optional(field('owner_type_parameters', $.type_parameters)),
      '::',
      field('name', $.identifier),
      optional(field('type_parameters', $.type_parameters)),
    ),

    type_parameters: $ => seq(
      '<',
      commaSep1(alias($.identifier, $.type_identifier)),
      '>',
    ),

    parameter_list: $ => seq(
      '(',
      commaSep(choice($.receiver, $.parameter)),
      ')',
    ),

    receiver: $ => seq(optional('*'), 'self'),

    // A `...` before the type makes this the variadic parameter: it
    // collects the remaining arguments, and the type written is the
    // element type, since the body sees a slice of it.
    parameter: $ => seq(
      field('name', $.identifier),
      ':',
      optional('...'),
      field('type', $._type),
      optional(seq('=', field('default', $._expression))),
    ),

    // The compiler consumes an effect row greedily after a signature, so a
    // closure body can never begin with `!`; the precedence mirrors that.
    effect_row: $ => prec(1, seq(
      '!',
      choice(
        field('row_variable', $.identifier),
        seq('{', commaSep(choice(
          $.borrow,
          $.identifier,
          seq('..', $.identifier),
        )), '}'),
      ),
    )),

    // `&xs` names the parameter the result is a view into.
    borrow: $ => seq('&', field('source', $.identifier)),

    struct_declaration: $ => seq(
      repeat($.decorator),
      optional('export'),
      'struct',
      field('name', alias($.identifier, $.type_identifier)),
      optional(field('type_parameters', $.type_parameters)),
      field('body', $.field_declaration_list),
    ),

    // One storage with several names for it. No type parameters: a union
    // is the size of its widest member, and a parameter would make that
    // depend on an argument.
    union_declaration: $ => seq(
      repeat($.decorator),
      optional('export'),
      'union',
      field('name', alias($.identifier, $.type_identifier)),
      field('body', $.field_declaration_list),
    ),

    field_declaration_list: $ => seq(
      '{',
      repeat(seq($.field_declaration, optional(choice(',', ';')))),
      '}',
    ),

    field_declaration: $ => seq(
      optional('export'),
      field('name', $.identifier),
      ':',
      field('type', $._type),
    ),

    enum_declaration: $ => seq(
      repeat($.decorator),
      optional('export'),
      'enum',
      field('name', alias($.identifier, $.type_identifier)),
      optional(field('type_parameters', $.type_parameters)),
      field('body', $.enum_variant_list),
    ),

    enum_variant_list: $ => seq(
      '{',
      repeat(seq($.enum_variant, optional(choice(',', ';')))),
      '}',
    ),

    enum_variant: $ => seq(
      field('name', $.identifier),
      optional(seq('(', commaSep($._type), ')')),
    ),

    // prec.right: in a select arm, the `:` after a binding is read as a type
    // annotation, not the arm's colon, matching the compiler's greedy parse.
    variable_declaration: $ => prec.right(seq(
      repeat($.decorator),
      optional('export'),
      choice('var', 'const'),
      field('name', $._binding),
      repeat(seq(',', field('name', $._binding))),
      optional(seq(':', field('type', $._type))),
      optional(seq('=', field('value', $._expression))),
    )),

    _binding: $ => choice($.identifier, $.blank_identifier),

    blank_identifier: $ => '_',

    // -----------------------------------------------------------------------
    // Statements
    // -----------------------------------------------------------------------

    block: $ => seq(
      '{',
      repeat($._terminator),
      optional(seq(
        $._statement,
        repeat(seq(repeat1($._terminator), $._statement)),
        repeat($._terminator),
      )),
      '}',
    ),

    _statement: $ => choice(
      $.variable_declaration,
      $.return_statement,
      $.if_statement,
      $.while_statement,
      $.for_statement,
      $.for_in_statement,
      $.defer_statement,
      $.with_statement,
      $.select_statement,
      $.break_statement,
      $.continue_statement,
      $.block,
      $.send_statement,
      $.assignment_statement,
      $.expression_statement,
    ),

    expression_statement: $ => $._expression,

    return_statement: $ => prec.right(seq(
      'return',
      optional(seq($._expression, repeat(seq(',', $._expression)))),
    )),

    break_statement: $ => 'break',

    continue_statement: $ => 'continue',

    defer_statement: $ => seq('defer', $._expression),

    with_statement: $ => seq('with', field('allocator', $._expression)),

    send_statement: $ => seq(
      field('channel', $._expression),
      '<-',
      field('value', $._expression),
    ),

    _expression_list_head: $ => $._expression,

    assignment_statement: $ => seq(
      field('left', $._expression_list_head),
      repeat(seq(',', field('left', $._expression_list_head))),
      field('operator', choice(
        '=', '+=', '-=', '*=', '/=', '%=', '&=', '|=', '^=', '<<=', '>>=',
      )),
      field('right', $._expression),
    ),

    // The terminator the lexer synthesizes after the closing brace is
    // consumed here when an `elif`/`else` follows on the next line, the way
    // the compiler's parser skips it before looking for the tail.
    if_statement: $ => seq(
      'if',
      field('condition', $._expression),
      field('consequence', $.block),
      optional(seq(repeat($._terminator), $._else_tail)),
    ),

    _else_tail: $ => choice(
      field('alternative', $.elif_clause),
      seq('else', field('alternative', $.block)),
    ),

    elif_clause: $ => seq(
      'elif',
      field('condition', $._expression),
      field('consequence', $.block),
      optional(seq(repeat($._terminator), $._else_tail)),
    ),

    while_statement: $ => seq(
      'while',
      field('condition', $._expression),
      field('body', $.block),
    ),

    for_statement: $ => seq(
      'for',
      optional(field('initializer', $._small_statement)),
      ';',
      optional(field('condition', $._expression)),
      ';',
      optional(field('update', $._small_statement)),
      field('body', $.block),
    ),

    _small_statement: $ => choice(
      $.variable_declaration,
      $.assignment_statement,
      $.send_statement,
      $.expression_statement,
    ),

    for_in_statement: $ => seq(
      'for',
      field('name', $.identifier),
      'in',
      field('iterable', $._expression),
      field('body', $.block),
    ),

    select_statement: $ => seq(
      'select',
      '{',
      repeat($._terminator),
      optional(seq(
        $._select_arm,
        repeat(seq(repeat1($._terminator), $._select_arm)),
        repeat($._terminator),
      )),
      '}',
    ),

    _select_arm: $ => choice($.select_arm, $.select_else_arm),

    select_arm: $ => seq(
      field('communication', choice(
        $.variable_declaration,
        $.send_statement,
        $.assignment_statement,
        $.expression_statement,
      )),
      ':',
      field('body', $._arm_body),
    ),

    select_else_arm: $ => seq('else', ':', field('body', $._arm_body)),

    _arm_body: $ => choice(
      $.block,
      $.return_statement,
      $.break_statement,
      $.continue_statement,
      $._expression,
    ),

    // -----------------------------------------------------------------------
    // Expressions
    // -----------------------------------------------------------------------

    _expression: $ => choice(
      $.identifier,
      $.blank_identifier,
      $.self,
      $.int_literal,
      $.float_literal,
      $.string_literal,
      $.char_literal,
      $.boolean_literal,
      $.null_literal,
      $.array_literal,
      $.tuple_expression,
      $.parenthesized_expression,
      $.struct_literal,
      $.path_expression,
      $.unary_expression,
      $.receive_expression,
      $.await_expression,
      $.binary_expression,
      $.cast_expression,
      $.range_expression,
      $.call_expression,
      $.field_expression,
      $.index_expression,
      $.match_expression,
      $.chan_literal,
      $.closure_expression,
    ),

    self: $ => 'self',

    boolean_literal: $ => choice('true', 'false'),

    null_literal: $ => 'null',

    int_literal: $ => token(choice(
      /[0-9][0-9_]*/,
      /0[xX][0-9a-fA-F_]+/,
      /0[bB][01_]+/,
      /0[oO][0-7_]+/,
    )),

    float_literal: $ => token(choice(
      /[0-9][0-9_]*\.[0-9][0-9_]*([eE][+-]?[0-9_]+)?/,
      /[0-9][0-9_]*[eE][+-]?[0-9_]+/,
    )),

    string_literal: $ => seq(
      '"',
      repeat(choice(
        $.escape_sequence,
        $.interpolation,
        $.string_content,
        token.immediate('#'),
      )),
      token.immediate('"'),
    ),

    string_content: $ => token.immediate(prec(1, /[^"\\#]+/)),

    escape_sequence: $ => token.immediate(
      /\\(n|t|r|0|\\|'|"|#|x[0-9a-fA-F]{2}|u\{[0-9a-fA-F]+\})/,
    ),

    interpolation: $ => seq(
      token.immediate('#{'),
      $._expression,
      '}',
    ),

    char_literal: $ => token(seq(
      "'",
      choice(
        /[^'\\\n]/,
        /\\(n|t|r|0|\\|'|"|#|x[0-9a-fA-F]{2}|u\{[0-9a-fA-F]+\})/,
      ),
      "'",
    )),

    array_literal: $ => seq('[', commaSep($._expression), ']'),

    parenthesized_expression: $ => seq('(', $._expression, ')'),

    tuple_expression: $ => seq(
      '(',
      $._expression,
      ',',
      commaSep1($._expression),
      ')',
    ),

    // A struct literal is a name (possibly generic, possibly namespaced)
    // followed by a braced field list. It loses to a block when both parses
    // survive — the same rule the compiler applies in `if`/`for` headers.
    struct_literal: $ => prec.dynamic(-1, seq(
      field('type', $.named_type),
      field('body', $.struct_literal_body),
    )),

    struct_literal_body: $ => seq(
      '{',
      commaSep(choice($.field_initializer, $._expression)),
      '}',
    ),

    field_initializer: $ => seq(
      field('name', $.identifier),
      ':',
      field('value', $._expression),
    ),

    // `a::b::c` with optional type arguments on any segment, used as a value:
    // `Array<i32>::new`, `Shape::Circle`. A single plain name stays an
    // identifier.
    path_expression: $ => seq(
      $.identifier,
      optional($.type_arguments),
      repeat1(seq('::', $.identifier, optional($.type_arguments))),
    ),

    unary_expression: $ => prec.right(PREC.unary, seq(
      field('operator', choice('-', '!', '~', '&', '*')),
      field('operand', $._expression),
    )),

    receive_expression: $ => prec.right(PREC.unary, seq(
      '<-',
      field('channel', $._expression),
    )),

    await_expression: $ => prec.right(PREC.unary, seq(
      'await',
      field('operand', $._expression),
    )),

    binary_expression: $ => {
      const table = [
        [PREC.or, '||'],
        [PREC.and, '&&'],
        [PREC.bit_or, '|'],
        [PREC.bit_xor, '^'],
        [PREC.bit_and, '&'],
        [PREC.equality, choice('==', '!=')],
        [PREC.comparison, choice('<', '<=', '>', '>=')],
        [PREC.shift, choice('<<', '>>')],
        [PREC.additive, choice('+', '-', '+%', '-%')],
        [PREC.multiplicative, choice('*', '/', '%', '*%')],
      ];
      return choice(...table.map(([precedence, operator]) =>
        prec.left(precedence, seq(
          field('left', $._expression),
          field('operator', operator),
          field('right', $._expression),
        ))));
    },

    cast_expression: $ => prec.left(PREC.cast, seq(
      field('operand', $._expression),
      'as',
      field('type', $._type),
    )),

    range_expression: $ => prec.left(PREC.range, seq(
      field('start', $._expression),
      '..',
      field('end', $._expression),
    )),

    // A generic call `max<i32>(3, 7)` wins over the comparison reading when
    // the angle brackets close and a call follows, the same commitment
    // tryGenericCall makes in the compiler.
    call_expression: $ => prec(PREC.postfix, seq(
      field('function', $._expression),
      optional(field('type_arguments', prec.dynamic(2, $.type_arguments))),
      field('arguments', $.arguments),
    )),

    arguments: $ => seq(
      '(',
      commaSep(choice($.named_argument, $.spread_argument, $._expression)),
      ')',
    ),

    // `f(xs...)` forwards an existing slice as a whole variadic pack.
    spread_argument: $ => seq(
      field('value', $._expression),
      '...',
    ),

    named_argument: $ => seq(
      field('name', $.identifier),
      '=',
      field('value', $._expression),
    ),

    field_expression: $ => prec(PREC.postfix, seq(
      field('operand', $._expression),
      '.',
      field('field', choice($.identifier, alias($.int_literal, $.tuple_index))),
    )),

    index_expression: $ => prec(PREC.postfix, seq(
      field('operand', $._expression),
      '[',
      field('index', choice($._expression, $.open_range)),
      ']',
    )),

    // Narrowing a slice may leave one end off — `xs[lo..]` runs to the
    // length, `xs[..hi]` starts at zero — which only the bracket allows,
    // since a `for` header over a bare end names no sequence. Both ends
    // present is an ordinary range_expression.
    open_range: $ => choice(
      seq(field('start', $._expression), '..'),
      seq('..', field('end', $._expression)),
    ),

    match_expression: $ => seq(
      'match',
      field('subject', $._expression),
      '{',
      repeat($._terminator),
      optional(seq(
        $.match_arm,
        repeat(seq(repeat1($._terminator), $.match_arm)),
        repeat($._terminator),
      )),
      '}',
    ),

    match_arm: $ => seq(
      field('pattern', $._pattern),
      ':',
      field('value', $._arm_body),
    ),

    chan_literal: $ => seq(
      'chan',
      '<',
      field('element', $._type),
      '>',
      '(',
      optional(field('capacity', $._expression)),
      ')',
    ),

    closure_expression: $ => prec.right(seq(
      optional('async'),
      'fn',
      optional(field('captures', $.capture_list)),
      field('parameters', $.closure_parameter_list),
      optional(seq(':', field('result', $._type))),
      optional($.effect_row),
      field('body', choice($.block, $._expression)),
    )),

    capture_list: $ => seq(
      '[',
      commaSep(seq('move', field('name', $.identifier))),
      ']',
    ),

    closure_parameter_list: $ => seq(
      '(',
      commaSep($.closure_parameter),
      ')',
    ),

    closure_parameter: $ => seq(
      field('name', $.identifier),
      optional(seq(':', field('type', $._type))),
      optional(seq('=', field('default', $._expression))),
    ),

    // -----------------------------------------------------------------------
    // Patterns
    // -----------------------------------------------------------------------

    _pattern: $ => choice(
      $.wildcard_pattern,
      $.literal_pattern,
      $.variant_pattern,
      $.type_test_pattern,
      $.binding_pattern,
    ),

    wildcard_pattern: $ => '_',

    literal_pattern: $ => choice(
      seq(optional('-'), choice($.int_literal, $.float_literal)),
      $.string_literal,
      $.char_literal,
      $.boolean_literal,
      $.null_literal,
    ),

    binding_pattern: $ => $.identifier,

    // Either a qualified variant (`Shape::Circle(r)`), or a bare name with a
    // payload, which sema reads as a type test (`RangeError(e)`).
    variant_pattern: $ => choice(
      seq(
        field('type', $.identifier),
        field('payload', $.pattern_payload),
      ),
      seq(
        $.identifier,
        repeat1(seq('::', $.identifier)),
        optional(field('payload', $.pattern_payload)),
      ),
    ),

    // A type test on an `any` whose type no path can spell: a tuple, a
    // slice, an array or a pointer. A type that is a plain name stays a
    // variant_pattern, since the two are the same syntax until the
    // subject says which one it is.
    type_test_pattern: $ => seq(
      field('type', choice($.tuple_type, $.slice_type, $.array_type, $.pointer_type)),
      optional(field('payload', $.pattern_payload)),
    ),

    pattern_payload: $ => seq('(', commaSep($._pattern), ')'),

    // -----------------------------------------------------------------------
    // Types
    // -----------------------------------------------------------------------

    _type: $ => choice(
      $.named_type,
      $.pointer_type,
      $.slice_type,
      $.array_type,
      $.tuple_type,
      $.parenthesized_type,
      $.function_type,
      $.code_type,
      $.chan_type,
      $.optional_type,
    ),

    named_type: $ => seq(
      alias($.identifier, $.type_identifier),
      optional($.type_arguments),
      repeat(seq('::', alias($.identifier, $.type_identifier), optional($.type_arguments))),
    ),

    type_arguments: $ => seq(
      '<',
      commaSep1(choice($._type, $.int_literal)),
      '>',
    ),

    pointer_type: $ => prec(TYPE_PREC.pointer, seq('*', $._type)),

    optional_type: $ => prec.left(TYPE_PREC.optional, seq($._type, '?')),

    slice_type: $ => prec.left(TYPE_PREC.suffix, seq(
      field('element', $._type),
      '[', ']',
    )),

    array_type: $ => prec.left(TYPE_PREC.suffix, seq(
      field('element', $._type),
      '[',
      field('length', $._expression),
      ']',
    )),

    tuple_type: $ => seq(
      '(',
      $._type,
      ',',
      commaSep1($._type),
      ')',
    ),

    parenthesized_type: $ => seq('(', $._type, ')'),

    function_type: $ => prec.right(seq(
      optional('async'),
      'fn',
      '(',
      commaSep($._type),
      ')',
      optional(seq(':', $._type)),
      optional($.effect_row),
    )),

    // `code` is a contextual identifier — only a `(` after it makes a
    // code pointer type — so a program may still name something `code`.
    // No `async` and no effect row: an address does not remember either.
    code_type: $ => prec.right(seq(
      'code',
      '(',
      commaSep($._type),
      ')',
      optional(seq(':', $._type)),
    )),

    chan_type: $ => seq('chan', '<', field('element', $._type), '>'),

    // -----------------------------------------------------------------------
    // Terminals
    // -----------------------------------------------------------------------

    identifier: $ => /[_\p{XID_Start}][_\p{XID_Continue}]*/,

    line_comment: $ => token(seq('//', /[^\n]*/)),
  },
});
