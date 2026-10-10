[
  (block)
  (field_declaration_list)
  (enum_variant_list)
  (trait_body)
  (impl_body)
  (struct_literal_body)
  (match_expression)
  (select_statement)
  (arguments)
  (parameter_list)
  (closure_parameter_list)
  (array_literal)
  (import_list)
] @indent.begin

[
  "}"
  ")"
  "]"
] @indent.branch

[
  (line_comment)
  (block_comment)
] @indent.auto
