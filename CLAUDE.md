# Gyro

Compiler for the Gyro language, written in Go.

- `docs/language.md` is the source of truth for the syntax. Read it before
  changing the parser.
- `docs/decisions.md` records what was decided and what was rejected. Read it
  before proposing a change to settled syntax, and update it whenever a new
  decision is made.

Gyro is a work in progress with no users. Nothing needs a migration path.
When a spelling changes, change it everywhere and update the docs — do not
leave a compatibility shim behind, and do not add a diagnostic whose only
job is to point from the old spelling at the new one. Same for syntax the
language never had: an error that names a construct borrowed from another
language is carrying weight it has not earned.

Delete removed things outright. No tombstone comments recording what used
to be there, no placeholder to hold a gap in a numbering. Git has the
history.

## Layout

```
main.go              CLI driver
internal/token/      token kinds and keywords
internal/lexer/      scanner, automatic statement termination
internal/ast/        syntax tree and dumper
internal/parser/     recursive descent, precedence climbing
internal/diag/       positions and diagnostics
internal/loader/     module graph resolution
internal/sema/       name/type checking; records an Info for the backends
internal/types/      the type representation
internal/interp/     tree-walking interpreter, the executable spec
internal/stdlib/     bundled declaration stubs (stdlib, mem, fs, net, ...)
internal/metals/     METALS IR: format, printer/parser, verifier
internal/lower/      Gyro -> METALS lowering
internal/meval/      METALS evaluator, the lowering's oracle
internal/nasm/       METALS -> x86-64 NASM + C runtime (gyro build)
internal/lsp/        language server (gyro lsp): diagnostics, hover, defs, completion
tree-sitter-gyro/    tree-sitter grammar for Gyro: external scanner, queries, corpus
tree-sitter-metals/  tree-sitter grammar for the METALS text format
examples/            reference programs, all must parse cleanly
```

`docs/tooling.md` covers the editor tooling: what the grammars and the
language server support, and how they wire into Neovim.

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
- **Prefer a library function over a keyword.** `spawn` was a keyword until
  closures made it unnecessary. Before adding grammar, check whether a
  closure-taking function would do.
- **`else` takes a block.** Chaining is spelled `elif`, one way only, so
  `else if` does not parse.
- **`move` and `copy` are contextual**, valid only inside a capture list.
  They must stay usable as ordinary identifiers.
- **Trait bounds join with `&`, not `+`.** Several bounds are an
  intersection. Do not "fix" this to match Rust.
- **`?` binds to the outermost type.** `*Node?` is a nullable pointer, not a
  pointer to a nullable value. `parseSuffixType` exists for this.
- **No default exports**, so no default imports either.
- **Type arguments live on path segments**, since `Array<i32>::new`
  parameterizes a non-final segment. Use `Path.Names()` and
  `Path.IsPlainName()` rather than indexing segments as strings.
- **Visibility is explicit.** `export` marks it. There is no capitalization
  rule; do not add one.
- **No `:=`.** Bindings are `var` or `const`, assignment is `=`.
- **`>>` is two `>` tokens.** The lexer must never merge them, or nested
  generics stop closing. The parser rejoins them for shifts.
- **`<-` is scanned greedily** and split back apart by the parser in
  binary-operator position. Sends are statements only.
- **A new diagnostic needs a code.** Each front-end package maps its
  message formats to a `diag.Code` in its own `codes.go`; the catalogue and
  the `gyro explain` text live in `internal/diag/codes.go`.
  `internal/diag/catalog_test.go` fails when a message has no entry, so a
  reworded message means updating the table.
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
