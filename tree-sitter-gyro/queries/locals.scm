(source_file) @local.scope

(function_declaration) @local.scope

(closure_expression) @local.scope

(block) @local.scope

(for_in_statement) @local.scope

(match_arm) @local.scope

(function_declaration
  name: (identifier) @local.definition.function)

(parameter
  name: (identifier) @local.definition.parameter)

(closure_parameter
  name: (identifier) @local.definition.parameter)

(variable_declaration
  name: (identifier) @local.definition.var)

(for_in_statement
  name: (identifier) @local.definition.var)

(import_name
  name: (identifier) @local.definition.import)

(binding_pattern) @local.definition.var

(identifier) @local.reference
