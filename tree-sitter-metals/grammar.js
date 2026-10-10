/**
 * Tree-sitter grammar for METALS, the IR described in docs/metals.md and
 * implemented in internal/metals.
 *
 * The format is line-oriented: newlines end instructions and declarations,
 * `;` starts a comment, and the three sigils keep namespaces apart
 * (%register/%type, @function/@global, $label). Structured control flow
 * (if/block/loop/switch/select) is parsed with real rules; every other
 * instruction is a destination, a mnemonic and a run of operands, which is
 * exactly the structure highlighting needs and stays stable as instructions
 * are added.
 */

function commaSep1(rule) {
  return seq(rule, repeat(seq(',', rule)));
}

function commaSep(rule) {
  return optional(commaSep1(rule));
}

module.exports = grammar({
  name: 'metals',

  extras: $ => [
    /[ \t\r]+/,
    $.comment,
  ],

  word: $ => $.identifier,

  rules: {
    source_file: $ => repeat(choice(
      $.module_declaration,
      $.import_declaration,
      $.type_declaration,
      $.memory_declaration,
      $.data_declaration,
      $.global_declaration,
      $.init_declaration,
      $.function_declaration,
      $._newline,
    )),

    _newline: $ => '\n',

    _end: $ => prec.right(repeat1($._newline)),

    module_declaration: $ => seq(
      'module',
      field('name', $.string),
      $._end,
    ),

    import_declaration: $ => seq(
      'import',
      field('module', $.string),
      '{',
      $._end,
      // A type declaration here is the same text as a module's own, and
      // means the module reuses the exporter's type rather than
      // declaring a second one of the same shape. It carries its own
      // terminator.
      repeat(choice($.type_declaration, seq($.imported_function, $._end))),
      '}',
      $._end,
    ),

    imported_function: $ => seq(
      'fn',
      field('name', $.global_name),
      field('signature', $.signature),
    ),

    signature: $ => seq(
      '(',
      commaSep($._type),
      ')',
      optional(seq('->', field('result', $._type))),
    ),

    type_declaration: $ => seq(
      'type',
      field('name', alias($.register, $.type_name)),
      '=',
      field('body', choice($.record_shape, $.union_shape, $.enum_shape, $.opaque_shape)),
      $._end,
    ),

    // A union: several members over one storage, all beginning where
    // the union does.
    union_shape: $ => seq(
      'union',
      optional($.alignment),
      '{',
      commaSep($.field_declaration),
      '}',
    ),

    // A name with no shape: a type the module knows exists and knows
    // nothing else about, which only a pointer may address.
    opaque_shape: $ => 'opaque',

    record_shape: $ => seq(
      'record',
      optional($.alignment),
      '{',
      commaSep($.field_declaration),
      '}',
    ),

    // An alignment beyond what the members require, which is what a run
    // of C bit-fields needs: its storage is bytes and its alignment came
    // from declared types no longer in the member list.
    alignment: $ => seq('align', $.number),

    field_declaration: $ => seq(
      field('name', $.identifier),
      ':',
      field('type', $._type),
    ),

    enum_shape: $ => seq(
      'enum',
      '{',
      commaSep($.variant_declaration),
      '}',
    ),

    variant_declaration: $ => seq(
      field('name', $.identifier),
      optional(seq('(', commaSep($._type), ')')),
    ),

    // A linear memory: a flat, bounds-checked byte region, sized in
    // 64 KiB pages, that a producer with C's memory model compiles into.
    memory_declaration: $ => seq(
      'memory',
      field('name', $.global_name),
      field('minimum', $.number),
      optional(seq(',', field('maximum', $.number))),
      $._end,
    ),

    // A span of a linear memory, written before any function runs. This
    // is a global's missing initializer, one level down.
    data_declaration: $ => seq(
      'data',
      field('memory', $.global_name),
      ',',
      field('offset', $.number),
      ',',
      field('bytes', $.string),
      $._end,
    ),

    // Module storage, zero-initialized unless it carries the bytes it
    // starts with. A thread_local one is a cell per thread and carries
    // none. Those bytes are storage, not an initializer: an
    // expression would need an evaluation order, and this has none.
    global_declaration: $ => seq(
      optional('thread_local'),
      'global',
      field('name', $.global_name),
      ':',
      field('type', $._type),
      optional(seq('=', field('image', $.string))),
      $._end,
    ),

    // The function this module needs run before the program starts,
    // which the linker calls ahead of main.
    init_declaration: $ => seq(
      'init',
      field('name', $.global_name),
      $._end,
    ),

    function_declaration: $ => seq(
      optional('export'),
      'fn',
      field('name', $.global_name),
      '(',
      commaSep($.parameter),
      ')',
      optional(seq('->', field('result', $._type))),
      '{',
      $._end,
      repeat($._instruction),
      '}',
      $._end,
    ),

    parameter: $ => seq(
      field('name', $.register),
      ':',
      field('type', $._type),
    ),

    // -----------------------------------------------------------------------
    // Instructions
    // -----------------------------------------------------------------------

    _instruction: $ => choice(
      $.if_instruction,
      $.block_instruction,
      $.loop_instruction,
      $.switch_instruction,
      $.instruction,
      $._newline,
    ),

    _instruction_body: $ => repeat1($._instruction),

    if_instruction: $ => seq(
      'if',
      field('condition', $.register),
      '{',
      $._end,
      optional($._instruction_body),
      '}',
      optional(seq('else', '{', $._end, optional($._instruction_body), '}')),
      $._end,
    ),

    block_instruction: $ => seq(
      'block',
      field('label', $.label),
      '{',
      $._end,
      optional($._instruction_body),
      '}',
      $._end,
    ),

    loop_instruction: $ => seq(
      'loop',
      field('label', $.label),
      '{',
      $._end,
      optional($._instruction_body),
      '}',
      $._end,
    ),

    switch_instruction: $ => seq(
      'switch',
      field('subject', $.register),
      '{',
      $._end,
      repeat(choice($.switch_case, $.switch_default, $._newline)),
      '}',
      $._end,
    ),

    switch_case: $ => seq(
      'case',
      field('value', $.number),
      '{',
      $._end,
      optional($._instruction_body),
      '}',
      $._end,
    ),

    switch_default: $ => seq(
      'default',
      '{',
      $._end,
      optional($._instruction_body),
      '}',
      $._end,
    ),

    // A plain one-line instruction: an optional destination, the mnemonic,
    // then operands. The operand run is deliberately token-shaped rather
    // than fully structured; the verifier owns the semantics.
    instruction: $ => seq(
      optional(seq(field('destination', $.register), '=')),
      field('operation', alias($.identifier, $.mnemonic)),
      repeat($._operand),
      $._end,
    ),

    _operand: $ => choice(
      $.register,
      $.global_name,
      $.label,
      $.number,
      $.string,
      $.char,
      $.identifier,
      ',',
      '(',
      ')',
      '<',
      '>',
      '->',
      ':',
    ),

    // -----------------------------------------------------------------------
    // Types
    // -----------------------------------------------------------------------

    _type: $ => choice(
      $.primitive_type,
      alias($.register, $.type_name),
      $.generic_type,
      $.function_type,
      $.code_type,
    ),

    primitive_type: $ => choice(
      'i8', 'i16', 'i32', 'i64',
      'u8', 'u16', 'u32', 'u64', 'usize', 'isize',
      'f32', 'f64',
      'bool', 'rune', 'str',
      'any', 'anyptr', 'handle',
    ),

    generic_type: $ => seq(
      field('constructor', alias(
        choice('ptr', 'opt', 'slice', 'array', 'tuple'),
        $.type_constructor,
      )),
      '<',
      commaSep1(choice($._type, $.number)),
      '>',
    ),

    function_type: $ => seq(
      'fn',
      $.signature,
    ),

    // A code pointer: the address of a function, with no environment
    // beside it.
    code_type: $ => seq(
      'code',
      $.signature,
    ),

    // -----------------------------------------------------------------------
    // Terminals
    // -----------------------------------------------------------------------

    _literal: $ => choice($.number, $.string, $.char, $.identifier),

    register: $ => token(seq('%', choice(
      /[A-Za-z_][A-Za-z0-9_.]*/,
      /"([^"\\\n]|\\.)*"/,
    ))),

    global_name: $ => token(seq('@', choice(
      /[A-Za-z_][A-Za-z0-9_.]*/,
      /"([^"\\\n]|\\.)*"/,
    ))),

    label: $ => token(seq('$', choice(
      /[A-Za-z_][A-Za-z0-9_.]*/,
      /"([^"\\\n]|\\.)*"/,
    ))),

    string: $ => token(/"([^"\\\n]|\\.)*"/),

    char: $ => token(/'([^'\\\n]|\\.)*'/),

    // `inf`, `-inf` and `nan` are how a float constant that is not a
    // number is written. Unsigned, they also lex as identifiers, which
    // costs nothing here; the signed form needs this rule to exist at
    // all, since the leading `-` is not otherwise an operand.
    number: $ => token(/-?(0[xX][0-9a-fA-F]+|[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?|inf|nan)/),

    identifier: $ => /[A-Za-z_][A-Za-z0-9_.]*/,

    comment: $ => token(seq(';', /[^\n]*/)),
  },
});
