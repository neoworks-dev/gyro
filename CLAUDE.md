# CLAUDE.md — Gyro

Gyro is a programming language with a compiler written in Go. Everything the compiler can read becomes METALS, and one thing consumes it: `internal/nasm`, which emits x86-64 assembly and links it against the C runtime in `internal/crt`.

**The implementation is not the specification.** The book in `docs/` is. A golden file is written by hand and says what a program prints; a compiler that disagrees with the golden is wrong. There is no flag that regenerates a golden from what the compiler happened to do — that is what stops the backend from redefining the language it implements, and since there is no second tier to disagree with it, the hand-written golden is the only check there is. Implement semantics once, at the highest shared layer possible: prefer synthesizing behavior during lowering over writing it into the backend and its runtime.

Gyro has no users. There are no migration paths, compatibility shims, deprecated aliases, tombstone comments, numbering placeholders, or diagnostics explaining obsolete or never-supported syntax. Replaced behavior is deleted outright — git keeps the history.

## Authoritative docs

Read the relevant docs before non-trivial changes:

- `docs/SUMMARY.md` — the book's table of contents; every chapter is listed there
- `docs/ch*.md` — syntax and semantics (source of truth), chapter by chapter
- `docs/decisions.md` — settled and rejected design decisions, with reasoning
- `docs/appendix-d-metals.md` — METALS IR specification
- `docs/appendix-e-native-backend.md` — the native backend and data-layout rules
- `docs/appendix-c-stdlib.md` — built-in method surface and standard library
- `docs/appendix-f-diagnostics.md` — diagnostic shape, notes and code ranges
- `docs/appendix-g-known-gaps.md` — what is not built yet
- `docs/ch16-02-editors.md` — tree-sitter grammars and language server
- `docs/agent-context.md` — detailed architecture, testing model, known gaps

`README.md` may be stale; verify its claims against the docs and the implementation.

When instructions conflict, the precedence is: explicit user request → the book's chapters → `decisions.md` → subsystem appendices (METALS, the native backend) → existing implementation and tests → this file. When the docs don't answer a question, don't invent syntax or semantics — ask.

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
internal/lower/      Gyro -> METALS lowering; METALS' first producer
internal/cc/         C -> METALS; the second producer, and what a .c import goes through
internal/nasm/       METALS -> x86-64 NASM, the one consumer
internal/crt/        the C runtime the backend links
internal/lsp/        language server
tree-sitter-gyro/    Gyro tree-sitter grammar and queries
tree-sitter-metals/  METALS tree-sitter grammar
examples/            reference programs
tests/               execution corpus
```

## Making language changes

When syntax, spelling, or semantics change:

1. Read the chapter that owns the topic (`docs/SUMMARY.md` indexes them) and the relevant `docs/decisions.md` entries first.
2. Update that chapter, then change the implementation everywhere.
3. Add parser coverage for syntax changes, add or extend a real program in `examples/`, and add execution or diagnostics corpus coverage as appropriate.
4. Record the new decision and its rejected alternatives in `docs/decisions.md`.
5. Update the tree-sitter grammars and tooling when affected.

## Testing

Prefer the Gyro corpora over new Go tests whenever a corpus can express the behavior.

**Execution corpus** (`tests/testdata/run/`). A single-file case is `NAME.gyro` + `NAME.out`; a multi-module case is a `NAME/` directory with `main.gyro` and `expected.out`. Every case is built and run. Supported directives: `//gyro:panic <fragment>`, `//gyro:unordered`, `//gyro:skip <why>`, `//gyro:args a b c`, `//gyro:stdin <line>`, `//gyro:exit <code>`. A known gap belongs in the corpus as a `//gyro:skip` with a reason, not only in prose. **Write the golden by hand.** Deciding what a program should print is the work; there is no `-update`, and pasting in what the compiler produced turns the corpus into a check that the compiler is deterministic.

A root may also be `NAME.metals` — IR written by hand, which is how the corpus reaches an instruction no lowering emits, linear memory among them. Directives there are spelled `;gyro:`, since `//` is not METALS' comment syntax.

**Diagnostics corpus** (`internal/sema/testdata/check/`). Every emitted diagnostic must be claimed by a fixture marker and every marker must match: `//~ E0301 does not fit u8` (`//~^` refers to the preceding line, one caret per line). Markers may match diagnostic codes and may claim `help:` or `note:` output. Unexpected diagnostics must fail tests; don't weaken fixtures to broad substring checks.

**Go tests** cover what corpora can't express cleanly: lexer/parser structure and recovery, parser progress, AST shape and precedence, loader graph behavior, METALS parsing/printing/verification, LSP protocol behavior, diagnostic rendering and catalogue, backend `Info`, source-position details, and focused helpers such as `editDistance`.

**Benchmark suite** (`tests/bench/`). Gyro programs that each isolate one codegen decision, measured against the C the case was ported from. A case is `NAME.gyro` or a `NAME/` directory with `main.gyro`; a directory case may add `reference.c` — a driver mirroring `main.gyro` — plus the upstream C files copied in unmodified, which the harness builds at `cc -O1 -fno-strict-aliasing` and reports as a `cc` column. Those flags are pinned rather than tuned: rebuilding the reference at another level turns every ratio into a comparison of optimization settings. Ported cases import their library out of `tests/testdata/run/`, so each port exists once. Each program times its own hot region and prints `gyro:bench <nanoseconds>` on stderr; both binaries in a case must agree on the stdout checksum. It is off during `go test ./...` — `make bench` runs it (`BENCH=<substring>`, `REPEAT=<n>`). It measures codegen, not correctness: a performance claim about codegen needs a case here, and the spread between cases is what says where the backend loses.

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

Bundled sources live under `internal/stdlib/src/`. A declaration without a body is a runtime intrinsic; one with a body is ordinary Gyro. Implement functionality in Gyro when it doesn't require runtime support — something with no interesting runtime requirement written into `rt.c` instead is a second language to maintain it in, for nothing. Extension methods are module-scope names, imported and exported like functions; they are not globally attached to a type, and must never become universe-scope behavior.

## METALS and the native backend

METALS is a whole-program IR (see `docs/appendix-d-metals.md`). The critical native layout rule: **storage is C's — a value in memory is exactly what `sizeof` says in C, at C's alignment, padded where C pads — while a register carries a scalar widened to a qword, and an `f32` as a double.** Loading a narrow scalar widens it, storing one narrows it, and what the runtime holds it holds in storage representation, so a narrow scalar reaches it through a staging cell rather than as the address of its register. The layout arithmetic is `metals.Layout`'s and belongs to no backend. When lowering stores a value in a mutable METALS register, the register name is not an immutable snapshot — deferred or delayed operations may need the captured value rather than the register's later contents.

METALS has two producers. `internal/lower` is Gyro's; `internal/cc` reads C directly and is what an `import` of a `.c` goes through. The second one exists to catch what the format only appears to guarantee because Gyro's lowering happens to satisfy it. Don't relax a verifier rule to make a producer compile — when a producer has broken a rule, the rule was tightened, not loosened. Several modules link into one via `metals.Link` before any consumer sees them; that is also the only pass that compares an importer's declared signature against the exporter's actual one.

The verifier is now the only thing that reads a module before the backend does, so a producer bug it doesn't catch reaches generated code. Tighten it rather than working around it downstream.

**Linear memory is not a second pointer.** A `memory` is a flat, bounds-checked byte region, and its addresses are `u32` integers — never `ptr<T>`. That is what keeps the boundary rules intact and a future sandbox real; a producer uses one model or the other, and no instruction takes both. `conv` (checked), `trunc` (wrapping) and `bitcast` (reinterpreting) are three different questions — don't answer one by weakening another. No producer emits a memory today: the wasm bridge that needed one is gone, and the corpus exercises the instructions from hand-written METALS. Keep it that way rather than deleting it — the model is what a C-shaped producer needs, and it is why the boundary rules exist.

**The floor of a sandboxed memory is scalars.** The bundled libc reached the outside through `write_byte`, `read_byte` and `exit`, and nothing else. A bulk import taking an address and a length would make a linear address a pointer at the boundary, which is the one thing the rule above forbids — if per-byte i/o ever costs too much, the answer is a bulk *instruction* between a memory and an `Array<u8>` handle, checked on both sides. Don't add the import.

**Float results round to their width.** An `f32` register holds a double, so every operation producing one has to round back through single precision or an `f32` multiply that should overflow to `+Inf` stays finite. `fnearest` is ties-to-even (not C's `round`) and `fmin`/`fmax` propagate NaN (not C's `fmin`/`fmax`) — don't "fix" either to match the C library.

A producer that can declare its types should. `any` is the open dynamic value for languages that genuinely cannot, and it is two words with an interned descriptor, not a heap box — don't make the boundary uniformly dynamic to save a declaration.

## Diagnostics and error recovery

Every new front-end diagnostic needs a `diag.Code`, mapped from its message format in the package's own `codes.go`. The catalogue and `gyro explain` text live in `internal/diag/codes.go`, and `internal/diag/catalog_test.go` verifies coverage — changing diagnostic wording means updating the mapping and catalogue. Don't suppress legitimate cascading diagnostics just because one malformed construct produces more than one error.

Error-recovery paths must still return structurally valid internal data. Never return malformed or partially initialized structures because a diagnostic was already emitted — downstream stages must not panic on invalid source.

## Git

Commit the worktree first if it's dirty, then write your changes. Short, to-the-point commit title; a body explaining the change when the title doesn't carry it; ask if you're unsure what to write. On a feature branch, only commit what that branch is for.

No trailers, ever: no `Co-Authored-By` on a commit, no "Generated with Claude Code" on a PR.

## Writing

Commits and issues carry only what matters. Say the thing, explain what a reader won't see for themselves, stop. No restating the diff, no summarising what you just said, no section that exists because the format seemed to want one.

Write issues and their comments the way you'd explain it to a colleague, in complete sentences.

- Start with a 2–3 sentence summary: what happened, why, and the fix.
- Use short headings, with short paragraphs of normal prose under them.
- Never join ideas with arrows, slashes, colons or dashes. Write "first X, then Y" instead of "X → Y", and "A and B" instead of "A / B".
- Use at most two code identifiers per sentence. Say what each one does the first time it appears.
- Use bullets only for genuinely separate items, and make each bullet a full sentence.
- Use tables only for numbers or timelines.
- Use one date format everywhere: 2026-10-07 18:28.
- Put error messages, paths and commands in code formatting, and long logs in a collapsed `<details>` block.
- Put side findings in a short "Out of scope" section at the end.

## How we work

One agent at a time, with me giving feedback as it goes. Keep each step small enough for me to read in one sitting, and stop to show me rather than piling up work I then have to catch up on.

`main` is what I've reviewed. It stays checked out in the repo root: never switch branches there.

Straight to `main` in the root, no branch: typos, one-liners, and anything that only touches how we work rather than the compiler — this file, `.claude/skills/`, editor config.

Everything else happens on a branch off `main`, in a worktree under `.worktrees/<branch>` (`git worktree add .worktrees/<branch> -b <branch> main`). Name it `<issue-number>-<slug>` when there is an issue (`12-const-match-pattern`), `<slug>` when there isn't. One branch is one thing; anything found along the way gets written down as an issue and stays off the branch, unless the branch can't finish without it.

Before starting on anything, check whether it is already half-built: `git branch` and `git worktree list` for the feature, and read what is on the branch. Sessions end mid-feature, and a branch is where that work is — starting again writes it a second time and loses whatever the first attempt learned. If a branch for it exists, continue on it.

No pull requests unless I ask for one. Review happens here, on the diff, before the merge.

## Issues

Issues on `neoworks-dev/gyro` carry goals across sessions. A session ends and its context goes with it; an issue is the only thing that carries a goal to the next one. So:

- Work that finishes in this session, with me here, needs no issue.
- Work that won't finish in one session gets one, however loosely defined it still is.
- Something concrete found along the way, that isn't what we're doing now, gets one instead of being done.

Write them as soon as the list exists, not once the work starts.

Write to GitHub as the bot: issues, comments and labels go through `gh bot` (`gh bot issue comment 12 --body …`), so they show as `neoworks-bot[bot]`, not as me. Plain `gh` is for reading only. The bot as author already says a model wrote it, so no "written by Claude" line in the text. If `gh bot` fails, say so rather than falling back to plain `gh`. It lives in `~/Documents/neoworks/gh-bot`.

Every issue gets a type, a priority, an effort and an area. Type, priority and effort are GitHub's own issue type and the organization's issue fields, never labels.

- **Type** — `Bug` something is broken, `Feature` something new, `Task` everything else.
- **Priority** — `Urgent` drop everything, `High` next up, `Medium` normal, `Low` nice to have.
- **Effort** — `Low` an hour or two, `Medium` about a day, `High` several days or needs splitting.

`gh bot` sets all three at once, by name: `gh bot issue create --type Bug --priority High --effort Low --label area:sema …`, and `gh bot issue edit 12 --priority Urgent` to change them later.

Area is a label, exactly one of:

- `area:syntax` — lexer, parser, AST, automatic statement termination
- `area:sema` — name and type checking, effect rows, and the safety analyses
- `area:lower` — Gyro to METALS lowering
- `area:metals` — the IR format, verifier and linker
- `area:cc` — the C front end and `.c` imports
- `area:backend` — `internal/nasm`, `internal/crt`, and codegen performance
- `area:stdlib` — the bundled standard library
- `area:tooling` — CLI, diagnostics rendering, language server, tree-sitter grammars
- `area:docs` — the book, when the change is to the specification itself

Two areas is fine when an issue genuinely spans them; three means split it.

## Done means verified

Work is done when it has been shown to work, not when the code is written. Shown means a corpus case or a Go test (see Testing) that fails without the change and passes with it, with a golden written by hand. `go test ./...`, `make check` and `make vet` pass on the branch either way, and a performance claim needs a `make bench` number.

Then hand it over and stop: what changed in a sentence or two, the evidence, and the branch. When it has an issue, the evidence also goes on the issue as a comment — that comment is what I read, so it's written for someone who wasn't in the session.

## Merging

I review the branch's diff, and it merges into `main` when I say so — never before, and never without evidence. Merge in the root with `git merge --no-ff <branch>`, so the branch stays one unit in the history; if it conflicts, resolve it in the merge commit. Push `main`. Then remove the worktree and delete the branch, and close its issue with `gh bot issue close <n>`.

## Project overrides of shared rules

- Go naming: follow Go conventions, including initialisms such as `ID`.
