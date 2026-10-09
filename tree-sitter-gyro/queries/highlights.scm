; Highlights for Gyro, written for nvim-treesitter's capture names.
; Later patterns win in Neovim, so the generic captures come first.

; --- identifiers -----------------------------------------------------------

(identifier) @variable

(type_identifier) @type

((type_identifier) @type.builtin
  (#any-of? @type.builtin
    "i8" "i16" "i32" "i64"
    "u8" "u16" "u32" "u64" "usize"
    "f32" "f64"
    "rune" "bool" "string" "void" "error" "any" "anyptr"))

(self) @variable.builtin

(blank_identifier) @variable.builtin

(parameter
  name: (identifier) @variable.parameter)

(closure_parameter
  name: (identifier) @variable.parameter)

(field_declaration
  name: (identifier) @variable.member)

(field_initializer
  name: (identifier) @variable.member)

(field_expression
  field: (identifier) @variable.member)

(tuple_index) @number

(enum_variant
  name: (identifier) @constant)

(variant_pattern
  (identifier) @constant)

(decorator
  (identifier) @attribute)

(effect_row
  (identifier) @attribute)

(capture_list
  name: (identifier) @variable)

; --- functions -------------------------------------------------------------

(function_declaration
  name: (identifier) @function)

(function_declaration
  owner: (type_identifier) @type)

(call_expression
  function: (identifier) @function.call)

(call_expression
  function: (field_expression
    field: (identifier) @function.method.call))

(call_expression
  function: (path_expression
    (identifier) @function.call .))

; --- literals --------------------------------------------------------------

(int_literal) @number

(float_literal) @number.float

(string_literal) @string

(escape_sequence) @string.escape

(char_literal) @character

(boolean_literal) @boolean

(null_literal) @constant.builtin

(interpolation
  "#{" @punctuation.special
  "}" @punctuation.special)

; --- comments --------------------------------------------------------------

(line_comment) @comment

(block_comment) @comment

; --- keywords --------------------------------------------------------------

"fn" @keyword.function

[
  "struct"
  "union"
  "enum"
] @keyword.type

[
  "var"
  "const"
] @keyword

"export" @keyword.modifier

"move" @keyword.modifier

[
  "import"
  "from"
] @keyword.import

"as" @keyword.operator

"return" @keyword.return

[
  "if"
  "elif"
  "else"
  "match"
  "select"
] @keyword.conditional

[
  "while"
  "for"
  "in"
] @keyword.repeat

(break_statement) @keyword

(continue_statement) @keyword

"defer" @keyword

"chan" @keyword.type

; `code` is contextual: this only fires inside a code pointer type, so a
; variable called code is still a variable.
(code_type "code" @keyword.type)

[
  "async"
  "await"
] @keyword.coroutine

; --- operators and punctuation ---------------------------------------------

[
  "+" "-" "*" "/" "%"
  "+%" "-%" "*%"
  "==" "!=" "<" "<=" ">" ">="
  "&&" "||" "!"
  "&" "|" "^" "~" "<<" ">>"
  "=" "+=" "-=" "*=" "/=" "%="
  "&=" "|=" "^=" "<<=" ">>="
  ".." "<-" "?"
] @operator

"::" @punctuation.delimiter

[
  "."
  ","
  ":"
  ";"
] @punctuation.delimiter

[
  "(" ")"
  "[" "]"
  "{" "}"
] @punctuation.bracket

"@" @attribute

(wildcard_pattern) @variable.builtin
