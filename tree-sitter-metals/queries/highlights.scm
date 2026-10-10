; Highlights for METALS, written for nvim-treesitter's capture names.

(identifier) @variable

(mnemonic) @function.builtin

(register) @variable

(type_name) @type

(global_name) @function

(label) @label

(primitive_type) @type.builtin

(type_constructor) @type.builtin

(field_declaration
  name: (identifier) @variable.member)

(variant_declaration
  name: (identifier) @constant)

(module_declaration
  name: (string) @string.special)

(import_declaration
  module: (string) @string.special)

(function_declaration
  name: (global_name) @function)

(imported_function
  name: (global_name) @function)

(instruction
  destination: (register) @variable)

(number) @number

(string) @string

(char) @character

(comment) @comment

[
  "module"
  "import"
  "type"
  "global"
  "thread_local"
  "init"
  "memory"
  "data"
  "export"
  "async"
  "fn"
  "code"
  "record"
  "enum"
] @keyword

[
  "if"
  "else"
  "switch"
  "case"
  "default"
  "select"
  "recv"
  "send"
] @keyword.conditional

[
  "block"
  "loop"
] @keyword.repeat

[
  "->"
  "="
] @operator

[
  "("
  ")"
  "{"
  "}"
  "<"
  ">"
] @punctuation.bracket

[
  ","
  ":"
] @punctuation.delimiter
