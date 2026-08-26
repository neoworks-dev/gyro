# 16.2. Editors

Gyro ships its editor support in this repository, versioned in lockstep with
the compiler: two tree-sitter grammars and a language server. Nothing here is
wired into any editor automatically; the pieces expose standard interfaces (a
tree-sitter parser directory, an LSP command) and the editor configuration is
yours.

```text
tree-sitter-gyro/     grammar, external scanner, queries, corpus tests
tree-sitter-metals/   the same for the METALS text format
internal/lsp/         the language server behind `gyro lsp`
```

## tree-sitter-gyro

`grammar.js` covers the full surface syntax. The parts a context-free
grammar cannot express live in `src/scanner.c`:

- **Automatic statement termination.** The scanner replicates the lexer's
  rule: a newline ends a statement when the parse state allows one to end
  (that is the grammar's encoding of the "last token can end a statement"
  set) and the next real token cannot only continue an expression. The
  continuation set is the lexer's, including the fine print: `<` continues
  unless it is `<-`, `as` continues as a word, comments are looked through
  ([chapter 3.5](ch03-05-lines-and-comments.md)).
- **Nested block comments**, which no regular token can match.

The grammar leans **permissive**: it exists for highlighting and structure,
and accepts a superset of what the compiler accepts — a struct literal in an
`if` condition parses here and is rejected by the compiler. The language
server is the authority on what compiles.

Queries under `queries/` target nvim-treesitter's capture conventions:
`highlights.scm`, `locals.scm`, `folds.scm`, `indents.scm`,
`injections.scm`.

## tree-sitter-metals

METALS is line-oriented, so this grammar needs no external scanner: newlines
are ordinary tokens and `;` comments are extras. Structured control flow
(`if`, `block`, `loop`, `switch`, `select`) has real rules; every other
instruction parses as destination, mnemonic, operands, which is robust to
new instructions being added to the IR. The three sigils map to distinct
node types (`register`, `global_name`, `label`), quoted forms like
`@"Pair<i32>::sum"` included.

## `gyro lsp`

`gyro lsp` speaks the Language Server Protocol over stdio. The protocol
layer is hand-rolled — the repository keeps zero Go dependencies — and the
language smarts are the compiler's own passes, run in-process on every
keystroke with open buffers overlaid over the file system.

For `.gyro` files:

- **Diagnostics**: parse and sema errors, live as you type, published for
  the open file. Warnings arrive as warnings. Each carries its code
  (`E0401`) in the LSP `code` field, and its notes — the suggested spelling
  or import under an undeclared name — follow the message on their own
  lines, since LSP has one message field.
- **Hover**: the checked type of the name under the cursor, and for
  functions the declared signature, sliced verbatim from the source.
- **Go to definition**: across files, including into imported modules
  (bundled stubs have no file and return nothing).
- **Document symbols**: functions (methods as `Type::name`), structs with
  fields, enums with variants, module-level bindings.
- **Inlay hints**: the inferred type after a `var`/`const` name, a
  `for ... in` loop variable, an unannotated closure parameter and a `match`
  binding; the parameter name before a positional argument. A hint is
  dropped when the argument already spells the parameter (`f(count)`), when
  the argument is written named, and when the callee has no declaration to
  read names from. Hints come from the current analysis only, never from the
  last good one: its offsets belong to text the editor has already changed.
- **Completion**: names in scope, struct fields and methods after `.`,
  variants and associated functions after `::`, exported names after a
  wildcard import's `namespace::`, keywords. Member completion resolves
  against the most recent analysis whose sema pass ran, so it keeps working
  while the buffer is mid-edit and does not parse.

For `.metals` files: diagnostics from the METALS parser (with line numbers)
and the verifier.

## Building the grammars

The Go side needs nothing beyond `make build`. The grammars regenerate with
the tree-sitter CLI, installed locally per grammar:

```sh
cd tree-sitter-gyro     # or tree-sitter-metals
npm install             # tree-sitter-cli, pinned in package.json
npx tree-sitter generate
npx tree-sitter test
```

The generated `src/parser.c` is committed, so consumers never need the CLI.

## Neovim

`editor/nvim/` holds a working setup, independent of nvim-treesitter's
install machinery:

```sh
editor/nvim/install.sh          # compile parsers into ~/.config/nvim/parser/,
                                # symlink queries into ~/.config/nvim/queries/
cp editor/nvim/gyro.lua ~/.config/nvim/lua/plugins/   # lazy.nvim spec
```

The install script compiles the committed `parser.c`/`scanner.c` with cc — no
tree-sitter CLI, no npm. `vim.treesitter` picks the parsers up from `parser/`
on the runtimepath. The spec registers the `gyro` and `metals` filetypes,
starts tree-sitter highlighting and folding, and runs the language server
with `bin/gyro lsp` (adjust `REPO_ROOT` inside if the repository lives
elsewhere). Inlay hints are off by default in Neovim, so the spec enables
them on attach and binds `<leader>ih` to toggle them.

After a grammar change: rerun `install.sh`. After a compiler or LSP change:
`make build`; the server restarts with the next editor session
(`:LspRestart` for the current one).

Without lazy.nvim, the spec's `config` function body is plain Lua — paste it
into `init.lua` and drop the surrounding spec table.

## Known limits (v1)

- Diagnostics are published for the open file only; an import's own errors
  surface when that file is opened.
- The AST records where nodes start but not where they end, so document
  symbol ranges extend to the next declaration, and declaration names are
  located by a bounded text scan.
- Scope completion approximates lexical scope by declaration order within
  the enclosing top-level declaration; it can offer a name from a sibling
  block.
- A generic body has no types of its own — sema records one set per
  instantiation — so inlay hints appear inside one only when a single
  instantiation pins them, and vanish as soon as there is a second.
- No rename, no find-references, no signature help yet.
