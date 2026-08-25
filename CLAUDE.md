# CLAUDE.md — Gyro

Gyro is a programming language with a compiler written in Go. Everything the compiler can read becomes METALS, and the language runs through three execution tiers over that IR: the METALS evaluator (`internal/meval`) and two native backends over the same C runtime — x86-64 assembly via `internal/nasm` and generated C via `internal/cgen`.

**No tier is the specification.** `docs/language.md` is. A golden file is written by hand and says what a program prints; a tier that disagrees with the golden is wrong, and so is a tier that disagrees with another tier. There is no flag that regenerates a golden from what some tier happened to do — that is what stops a backend from redefining the language it implements. If tiers disagree, fix the divergence; never redefine behavior independently in a backend. Implement semantics once, at the highest shared layer possible: prefer synthesizing behavior during lowering over duplicating it in the evaluator and the native backend.

Gyro has no users. There are no migration paths, compatibility shims, deprecated aliases, tombstone comments, numbering placeholders, or diagnostics explaining obsolete or never-supported syntax. Replaced behavior is deleted outright — git keeps the history.

## Authoritative docs

Read the relevant docs before non-trivial changes:

- `docs/language.md` — syntax and semantics (source of truth)
- `docs/decisions.md` — settled and rejected design decisions, with reasoning
- `docs/metals.md` — METALS IR specification
- `docs/native.md` — native backend and data-layout rules
- `docs/tooling.md` — tree-sitter grammars and language server
- `docs/agent-context.md` — detailed architecture, testing model, known gaps

`README.md` may be stale; verify its claims against the docs and the implementation.

When instructions conflict, the precedence is: explicit user request → `language.md` → `decisions.md` → subsystem spec (`metals.md`, `native.md`, `tooling.md`) → existing implementation and tests → this file. When the docs don't answer a question, don't invent syntax or semantics — ask.

## Repository layout

```text
main.go              CLI driver
internal/token/      token kinds and keywords
internal/lexer/      scanner, automatic statement termination
internal/ast/        syntax tree and dumper
internal/parser/     recursive descent, precedence climbing
internal/diag/       positions and diagnostics
internal/loader/     module graph resolution
internal/sema/       name/type checking and safety analyses
internal/types/      type representation
internal/stdlib/     bundled Gyro standard-library sources
internal/metals/     METALS IR format, parser/printer, verifier, linker
internal/lisp/       a statically typed Scheme; METALS' second producer
internal/wasm/       wasm32 -> METALS; METALS' third producer, C's sandboxed route in
internal/clibc/      the C standard library the wasm bridge compiles in
internal/cc/         C -> METALS directly; what a Gyro program imports a .c through
internal/lower/      Gyro -> METALS lowering
internal/meval/      METALS evaluator
internal/nasm/       METALS -> x86-64 NASM
internal/cgen/       METALS -> C, the sibling backend
internal/crt/        the C runtime both native backends link
internal/lsp/        language server
tree-sitter-gyro/    Gyro tree-sitter grammar and queries
tree-sitter-metals/  METALS tree-sitter grammar
examples/            reference programs
tests/               execution corpus
```

## Making language changes

When syntax, spelling, or semantics change:

1. Read `docs/language.md` and the relevant `docs/decisions.md` entries first.
2. Update `docs/language.md`, then change the implementation everywhere.
3. Add parser coverage for syntax changes, add or extend a real program in `examples/`, and add execution or diagnostics corpus coverage as appropriate.
4. Record the new decision and its rejected alternatives in `docs/decisions.md`.
5. Update the tree-sitter grammars and tooling when affected.

## Testing

Prefer the Gyro corpora over new Go tests whenever a corpus can express the behavior.

**Execution corpus** (`tests/testdata/run/`). A single-file case is `NAME.gyro` + `NAME.out`; a multi-module case is a `NAME/` directory with `main.gyro` and `expected.out`. Every case runs through all three tiers (`metals`, `native`, `cnative`). Supported directives: `//gyro:panic <fragment>`, `//gyro:unordered`, `//gyro:skip <tier> <why>`, `//gyro:args a b c`, `//gyro:stdin <line>`, `//gyro:exit <code>`. A known tier gap belongs in the corpus as a `//gyro:skip` with a reason, not only in prose. **Write the golden by hand.** Deciding what a program should print is the work; there is no `-update`, and pasting in a tier's output defeats the point of running three of them. `//gyro:panic` is checked against every tier, so a message the tiers word differently is a divergence to fix, not a fragment to loosen.

A root may also be `NAME.metals`, `NAME.lisp` or `NAME.c` — IR written by hand, the Lisp front end, or C through the wasm bridge. Every root runs on the same three tiers, because every root becomes the same thing; only the producer in front differs, and directives are spelled `;gyro:` where `//` is not the comment syntax. A `.c` case skips itself when clang is not installed.

**Diagnostics corpus** (`internal/sema/testdata/check/`). Every emitted diagnostic must be claimed by a fixture marker and every marker must match: `//~ E0301 does not fit u8` (`//~^` refers to the preceding line, one caret per line). Markers may match diagnostic codes and may claim `help:` or `note:` output. Unexpected diagnostics must fail tests; don't weaken fixtures to broad substring checks.

**Go tests** cover what corpora can't express cleanly: lexer/parser structure and recovery, parser progress, AST shape and precedence, loader graph behavior, METALS parsing/printing/verification, LSP protocol behavior, diagnostic rendering and catalogue, backend `Info`, source-position details, and focused helpers such as `editDistance`.

**Benchmark suite** (`tests/bench/`). Gyro programs that each isolate one codegen decision, built by both native backends from the same lowered module. A case is `NAME.gyro` or a `NAME/` directory with `main.gyro`; a directory case may add `reference.c` — a driver mirroring `main.gyro` — plus the upstream C files copied in unmodified, which the harness builds with cgen's own flags and reports as a `cc` column. Ported cases import their library out of `tests/testdata/run/`, so each port exists once. Each program times its own hot region and prints `gyro:bench <nanoseconds>` on stderr; every binary in a case must agree on the stdout checksum. It is off during `go test ./...` — `make bench` runs it (`BENCH=<substring>`, `REPEAT=<n>`). It measures backends, not correctness: a performance claim about codegen needs a case here, and the spread between cases is what says where a backend loses.

Before considering a change complete, run `go test ./...`, `make check`, and `make vet`. Run `make grammar` whenever a tree-sitter grammar or the scanner changes. Don't claim tests passed unless they were run.

## Language invariants (easy to break)

**Names and syntax.** There is no universe scope: names outside the formal syntax must be imported, and there are no predeclared functions. Prefer a library function over a keyword when ordinary Gyro can express the feature. Bindings use `var` or `const`, assignment is `=` (there is no `:=`), `else` requires a block, and chaining uses `elif` — `else if` doesn't parse. Visibility is explicit via `export` (capitalization has no semantic meaning), with no default exports or imports. Trait bounds join with `&`, not `+`.

**Contextual keywords.** `move`, `copy`, and `from` are contextual identifiers, not lexer keywords. They must stay usable as ordinary identifiers outside their special grammar positions; don't add them to `internal/token` just because the parser recognizes them somewhere.

**Types and paths.** `?` binds to the outermost type: `*Node?` is a nullable pointer, not a pointer to a nullable `Node`. Type arguments belong to path segments (`Array<i32>::new`). Never treat paths as flat strings — prefer `Path.Names()` and `Path.IsPlainName()`.

**Lexer/parser interactions.** `>>` must remain two `>` tokens; the parser recombines them for shifts, and merging them in the lexer breaks nested generic closing. `<-` is scanned greedily and the parser may split it in binary-operator position; sends are statements only. Every parser loop over statements or declarations must guarantee forward progress on malformed input — preserve `TestParserAlwaysTerminates`.

## Safety semantics

The analyses in `internal/sema` — definite assignment, leak warnings, spawn race warnings, borrow provenance, and match exhaustiveness — are deliberate language properties, not optional linting. Never weaken them merely to make a program compile.

Borrow analysis tracks provenance, not lifetime. Its invariants: structs carry the origins of their fields (a local struct does not automatically mean frame storage); field-chain provenance roots at the base expression; generic borrow rows may become relevant only after instantiation; structs can be borrow sources, and only scalar values are rejected; `runes()` and `bytes()` copy and borrow nothing; returned `string` fields may borrow their containing struct. The flow analyses are intentionally gen-only/no-kill — don't introduce loop fixpoint machinery without revisiting that design.

## Standard library

Bundled sources live under `internal/stdlib/src/`. A declaration without a body is a runtime intrinsic; one with a body is ordinary Gyro. Implement functionality in Gyro when it doesn't require runtime support — an intrinsic must behave consistently across all three tiers. Extension methods are module-scope names, imported and exported like functions; they are not globally attached to a type, and must never become universe-scope behavior.

## METALS and the native backend

METALS is a whole-program IR (see `docs/metals.md`). The critical native layout rule: **storage is C's — a value in memory is exactly what `sizeof` says in C, at C's alignment, padded where C pads — while a register carries a scalar widened to a qword, and an `f32` as a double.** Loading a narrow scalar widens it, storing one narrows it, and what the runtime holds it holds in storage representation, so a narrow scalar reaches it through a staging cell rather than as the address of its register. The layout arithmetic is `metals.Layout`'s and belongs to no backend. When lowering stores a value in a mutable METALS register, the register name is not an immutable snapshot — deferred or delayed operations may need the captured value rather than the register's later contents.

METALS has more than one producer. `internal/lower` is Gyro's; `internal/lisp` is a statically typed Scheme, and `internal/wasm` reads what clang makes of C. They exist to catch what the format only appears to guarantee because Gyro's lowering happens to satisfy it. Don't relax a verifier rule to make a producer compile — the closure-environment rule was tightened, not loosened, when the second producer broke it, and the third got linear memory added rather than `ptr<T>` weakened. Several modules link into one via `metals.Link` before any consumer sees them; that is also the only pass that compares an importer's declared signature against the exporter's actual one.

**Linear memory is not a second pointer.** A `memory` is a flat, bounds-checked byte region, and its addresses are `u32` integers — never `ptr<T>`. That is what keeps the boundary rules intact and the sandbox real; a producer uses one model or the other, and no instruction takes both. `conv` (checked), `trunc` (wrapping) and `bitcast` (reinterpreting) are three different questions — don't answer one by weakening another.

**The C floor is scalars.** `internal/clibc` reaches the outside through `write_byte`, `read_byte` and `exit`, and nothing else. A bulk import taking an address and a length would make a linear address a pointer at the boundary, which is the one thing the rule above forbids — if per-byte i/o ever costs too much, the answer is a bulk *instruction* between a memory and an `Array<u8>` handle, checked on both sides. Don't add the import.

**Float results round to their width.** An `f32` register holds a double, so every operation producing one has to round back through single precision or an `f32` multiply that should overflow to `+Inf` stays finite. `fnearest` is ties-to-even (not C's `round`) and `fmin`/`fmax` propagate NaN (not C's `fmin`/`fmax`) — don't "fix" either to match the C library.

A producer that can declare its types should. `any` is the open dynamic value for languages that genuinely cannot, and it is two words with an interned descriptor, not a heap box — don't make the boundary uniformly dynamic to save a declaration.

## Diagnostics and error recovery

Every new front-end diagnostic needs a `diag.Code`, mapped from its message format in the package's own `codes.go`. The catalogue and `gyro explain` text live in `internal/diag/codes.go`, and `internal/diag/catalog_test.go` verifies coverage — changing diagnostic wording means updating the mapping and catalogue. Don't suppress legitimate cascading diagnostics just because one malformed construct produces more than one error.

Error-recovery paths must still return structurally valid internal data. Never return malformed or partially initialized structures because a diagnostic was already emitted — downstream stages must not panic on invalid source.

## Project overrides of shared rules

- Go naming: follow Go conventions, including initialisms such as `ID`.
