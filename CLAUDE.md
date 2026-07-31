# Gyro

Compiler for the Gyro language, written in Go. `docs/language.md` is the
source of truth for the syntax — read it before changing the parser.

## Layout

```
main.go              CLI driver
internal/token/      token kinds and keywords
internal/lexer/      scanner, automatic statement termination
internal/ast/        syntax tree and dumper
internal/parser/     recursive descent, precedence climbing
internal/diag/       positions and diagnostics
examples/            reference programs, all must parse cleanly
```

## Commands

```sh
make build   # -> bin/gyro
make test    # go test ./...
make check   # parse every example
make vet
```

## Working on the language

Gyro's design is still in flux, and much of it is not derivable from the code.
When a change touches syntax or semantics:

1. Update `docs/language.md` first.
2. Add a parser test in `internal/parser/parser_test.go`.
3. Add or extend an example in `examples/` so the syntax has a real user.

Do not reintroduce `src/gyro.peggy`. The Peggy grammar was abandoned and
deleted; it describes a different language from the one implemented here.

Ask before inventing syntax or semantics that `docs/language.md` does not
cover. Guessing produces code that has to be undone.

## Constraints that are easy to break

- **No universe scope.** If a name is not part of the formal syntax, it must
  be imported. `println` is imported; `ch.close()` and `xs.len()` are methods
  on built-in types. Do not add predeclared functions.
- **Visibility is explicit.** `export` marks it. There is no capitalization
  rule; do not add one.
- **No `:=`.** Bindings are `var` or `const`, assignment is `=`.
- **`>>` is two `>` tokens.** The lexer must never merge them, or nested
  generics stop closing. The parser rejoins them for shifts.
- **`<-` is scanned greedily** and split back apart by the parser in
  binary-operator position. Sends are statements only.
- **Parser progress.** Every loop over statements or declarations must
  guarantee forward progress on malformed input.
  `TestParserAlwaysTerminates` covers this.

## Go style

- Descriptive names. `recordID`, not `rid`.
- Guard clauses over nesting; more than two levels of indentation is a smell.
- Short functions with one responsibility.
- Comments explain why, never restate the code. No comment is better than an
  obvious one.
- Match the surrounding code when these rules conflict with it.

## Code Style

When editing or generating code, prioritize readability and maintainability over cleverness.

Rules:

- Preserve descriptive names. Do not shorten identifiers.
  - Good: `recordId`, `customerAccount`, `paymentMethod`
  - Bad: `rid`, `acct`, `pm`

- Use `camelCase` for variables, functions, parameters, and object fields unless the language, framework, or existing codebase requires another convention.
- Prefer explicit control flow over shorthand.
- Avoid `??`, ternary `?:`, and compact conditional expressions unless they clearly prevent a large amount of repetitive code without reducing readability.
- Do not deeply nest logic.
  - More than 2 indentation levels is too much.
  - Use guard clauses, early returns, helper functions, or extracted validation steps instead.

- Keep functions short and focused.
  - Split large functions into smaller functions instead of writing one huge function.
  - Each function should have one clear responsibility.

- Add comments only when the code is not immediately readable when skimming.
- Comments must be technical, concise, and useful.
  - Good: `// Normalize external IDs before database lookup.`
  - Bad: `// Now we loop through the items and do the thing.`

- Do not add obvious comments that restate the code.
- Prefer clearly named helper functions over long inline logic with comments.
- Do not change behavior, public APIs, data shapes, validation rules, or side effects unless explicitly asked.
- Match the surrounding code style when it conflicts with these rules.

## Git

Commit before large changes when the tree is dirty. Write a short imperative
subject and explain the reasoning in the body when it is not obvious from the
diff. State in the commit message that it was written by Claude, not a human.
