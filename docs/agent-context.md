# Gyro: orientation for an agent

Source material for a system prompt. Everything an agent needs to work on
this repository effectively, including the parts that are not derivable
from the code and the parts that are only learned by getting them wrong.

`CLAUDE.md` is the short operational version. This is the long one, with
reasoning attached.

### Which docs are authoritative

| file                                | status                                                 |
| ----------------------------------- | ------------------------------------------------------ |
| `docs/SUMMARY.md`                   | the book's table of contents; start here               |
| the `docs/ch*.md` chapters          | **source of truth** for syntax and semantics           |
| `docs/decisions.md`                 | what was decided and what was rejected, with reasoning |
| `docs/appendix-d-metals.md`         | the IR spec                                            |
| `docs/appendix-e-native-backend.md`| the native backend and its layout rules                |
| `docs/appendix-c-stdlib.md`         | built-in method surface and standard library           |
| `docs/appendix-g-known-gaps.md`     | what is not built yet                                  |
| `docs/ch16-02-editors.md`           | grammars and language server                           |
| `CLAUDE.md`                         | operational rules for an agent                         |
| `README.md`                         | stale in places; verify before trusting                |

---

## 1. What Gyro is

A compiler for the Gyro language, written in Go. ~31k lines of non-test
Go, ~3.8k lines of Go tests, plus two test corpora written in Gyro
itself.

**The goal that drives every design decision:** Gyro should be explicit
about side effects and unwanted control flow, and safe enough that a
machine can generate code in it and be caught when it is wrong. Not
"safe" as marketing — safe as in the compiler refuses the program.

That goal is why the language has:

- no zero values, so nothing is silently uninitialized
- no implicit conversions, anywhere, between any two typed values
- overflow that panics rather than wraps, with wrapping spelled `+%`
- exhaustive matches over enums
- definite assignment, flow-sensitively checked
- borrow provenance written into signatures
- leak and data-race warnings

It is a work in progress with **no users**. Nothing needs a migration
path. This matters constantly — see §7.

---

## 2. Pipeline

```
source
  → internal/lexer      scanner, automatic statement termination
  → internal/parser     recursive descent, precedence climbing
  → internal/loader     module graph, dependency order
  → internal/sema       name + type checking, all the safety analyses
  → internal/lower      Gyro → METALS IR
  → internal/metals     IR format, printer, parser, verifier
  → internal/nasm       METALS → x86-64 NASM
                        linked against internal/crt, the C runtime
```

Everything the compiler can read becomes METALS, and everything that
runs, runs from METALS. There is no second execution path.

### Package sizes, roughly

Counted without `_test.go`:

| package  | lines | what it is                                                    |
| -------- | ----- | ------------------------------------------------------------- |
| `cc`     | 7.6k  | C → METALS: preprocessor, parser, lowering                    |
| `sema`   | 8.2k  | the bulk of the compiler; every safety analysis lives here    |
| `metals` | 6.2k  | IR types, text format, verifier, linker                       |
| `lower`  | 5.8k  | Gyro → METALS, including monomorphization and closure lifting |
| `nasm`   | 5.0k  | x86-64 code generation                                        |
| `lsp`    | 2.5k  | language server, hand-rolled, zero dependencies               |
| `parser` | 2.3k  |                                                               |
| `ast`    | 1.7k  | tree + dumper                                                 |
| `crt`    | 1.6k  | `src/rt.c`, the runtime the backend links                     |

Also: `tree-sitter-gyro/` and `tree-sitter-metals/` (grammars, external
scanner, queries, corpus), `internal/stdlib/` (bundled `.gyro` sources
compiled into the binary), and `examples/` (10 reference programs).

### One backend, no oracle

This is the single most important architectural fact.

**The compiler does not define the answer.** The golden file does, and it
is written by hand. There is no flag that regenerates it from what the
compiler produced, because an implementation that can write its own exam
has stopped being a check on anything — the first version of this corpus
rewrote goldens from a tree-walking interpreter, and what that bought was
an implementation whose bugs became the specification until someone read
the file.

So a surprising output is not presumptively correct: read the book chapter
that covers the behaviour, decide what the program should print, and
record the decision in `docs/decisions.md` if the docs did not already
answer it.

**There used to be three tiers** — a METALS evaluator and two native
backends — and they had to agree byte for byte. That cross-check is gone.
What it caught was a wrong answer that looked plausible; nothing catches
that automatically now, so writing the golden *before* running the program
is no longer a discipline but the only check there is.

---

## 3. Testing model

**This is where most of the leverage is. Read this before writing a test.**

Two corpora written in Gyro carry most of the suite. Prefer adding to
them over writing another Go test.

### 3.1 Execution corpus — `tests/testdata/run/`

142 cases — 114 single files and 28 directories. One `.gyro` file beside
the `.out` it must print.

```
tests/testdata/run/tuples.gyro
tests/testdata/run/tuples.out
tests/testdata/run/cross-module/main.gyro    # multi-module case
tests/testdata/run/cross-module/util.gyro
tests/testdata/run/cross-module/expected.out
```

Every case is built and run, and held to the golden, which you write.

Directives, written as comments in the root file:

| directive                  | meaning                                                                                    |
| -------------------------- | ------------------------------------------------------------------------------------------ |
| `//gyro:panic <fragment>`  | must abort; the message must contain the fragment; the golden holds stdout before the panic |
| `//gyro:unordered`         | concurrent output; the program must succeed but output is not compared                     |
| `//gyro:skip <why>`        | a known gap, with the reason it is one                                                     |
| `//gyro:args a b c`        | main's arguments, after a fixed `argv[0]` of `program`                                     |
| `//gyro:stdin <line>`      | a line of standard input; repeatable                                                       |
| `//gyro:exit <code>`       | the exit code the program must produce                                                     |

**A gap belongs in the corpus as a skip with its reason attached, not in
prose.** A skip is a program that will fail the day the gap closes. There
are none right now: the cases that used to be skipped on the natives —
use-after-free, uninitialized reads, union punning — only ever passed on
the evaluator, and were deleted with it rather than kept as programs
nothing runs.

A root may also be `NAME.metals`, IR written by hand, which is how the
corpus reaches an instruction no lowering emits — linear memory, for
one. Directives there are spelled `;gyro:`.

### 3.2 Diagnostics corpus — `internal/sema/testdata/check/`

43 fixtures. A Gyro file that says what it expects of itself.

```gyro
var x: u8 = 300                //~ E0301 does not fit u8
return xs                      //~ E0609 which the row does not name
                               //~^ help: say so in the row: !{ &xs }
```

Rules:

- every diagnostic must be claimed by a `//~` marker **on its line**
- every marker must be satisfied
- a file with no markers must check clean
- `//~^` claims the line above, one line per caret
- a marker may pin the code (`E0301`) and may claim `help:` or `note:`
  instead of the message

Fixtures go through the **loader**, so imports resolve for real.

Why this shape: the old helpers matched a substring anywhere in the bag,
which asserts nothing about position. A diagnostic pointing at the wrong
line passed, and so did an unexpected second one.

### 3.3 What stays in Go

Only what neither corpus can express:

- `parser` — tree shapes, precedence, error recovery, `TestParserAlwaysTerminates`
- `lexer` — token kinds, automatic termination, keyword/non-keyword sets
- `metals` — text-format round trips, ~20 verifier rejection tests
- `lsp` — protocol behaviour
- `loader` — module graph, cycles, resolution
- `diag` — rendering, and the catalogue tests
- `lower` — METALS naming and record layout, nothing about output
- `sema` — `editDistance`, and the `Info` the backends read

### 3.4 Benchmark suite — `tests/bench/`

Gyro programs, each isolating one codegen decision, built by both native
backends from the same lowered module and compared:

| case            | what it isolates                                      |
| --------------- | ----------------------------------------------------- |
| `call-fib`      | call sequence: prologue, argument moves, return path  |
| `loop-index`    | loop induction and `index.get` bounds checks          |
| `arith-checked` | the overflow test on every add, subtract and multiply |
| `struct-array`  | aggregate copies in and out of `Array<T>`             |
| `string-concat` | allocation growth and the per-byte `String::push`     |
| `startup`       | baseline: process start, runtime init, exit           |
| `sha256`        | ported C: 32-bit arithmetic over a fixed buffer       |
| `regex`         | ported C: calls, branches and backtracking            |
| `jsmn`          | ported C: one read and a switch per input byte        |
| `cjson`         | ported C: one heap node per value, pointer-chased     |
| `md4c`          | ported C: ~90 functions behind a callback table       |
| `perft`         | ported C: recursion over u64 bitboards and big tables |

A case is `NAME.gyro` or a `NAME/` directory with `main.gyro` — the same
two shapes the execution corpus uses. A directory case may carry a C
**reference**: `reference.c`, a driver mirroring `main.gyro` line for
line, plus the upstream library files copied in unmodified. The harness
builds them at `cc -O1 -fno-strict-aliasing` and adds a `cc` column with
`nasm/cc` beside it, so the table answers what the language costs against
the C it replaces. The flags are pinned rather than tuned: a reference
rebuilt at a different level turns every ratio into a comparison of
optimization settings.
A ported case imports its library **out of the execution corpus** rather
than copying it, so there is one port per library: the one whose output
the corpus already compares against the C original's.

Each program times its own hot region with `time.monotonic` —
`clock_gettime` in the C references — and prints `gyro:bench
<nanoseconds>` on stderr, so setup, process start and runtime init stay
out of the number; process wall time and build time are reported in a
second table. Both binaries in a case must print the same checksum on
stdout or the case fails — comparing programs that computed different
things measures nothing, and for a port that agreement is the whole
claim.

The suite is off during `go test ./...` and runs from `make bench`
(`BENCH=<substring>` filters, `REPEAT=<n>` sets the run count; the
fastest run counts). Read the **spread**, not the average: a backend
behind by the same factor everywhere has one problem, a backend at 1.06x
on four cases and 2.25x on the fifth has a specific one.

The six ports spread widely against C — `regex` 6.6x, `perft` 11.9x,
`cjson` 13.8x, `jsmn` 16.6x, `sha256` 17.0x, `md4c` 29.0x, best of 3 on
2026-08-26 — and what separates them is how much work each does per
element access, since every one of those goes through a runtime call and
a bounds check while C reads a byte. These are worse than the numbers
this file used to carry because those were a second native backend's,
where the host compiler inlined and allocated registers; that backend is
deleted, and this is what the assembler costs.

### 3.5 Commands

```sh
make build     # -> bin/gyro
make test      # go test ./...
make bench     # the backend against the C, on tests/bench
make check     # parse every example
make vet
make grammar   # regenerate + test both tree-sitter grammars
```

Execution goldens are written by hand. There is no `-update`.

Before any commit: `go test ./...`, `make check`, `make vet`. If grammars
changed, `make grammar` too (46 and 8 parses respectively).

---

## 4. Language surface

The book's chapters are the source of truth — `docs/SUMMARY.md` indexes
them. Highlights an agent needs loaded:

### Syntax

- bindings are `var` and `const`; assignment is `=`; **there is no `:=`**
- `else` takes a block; chaining is `elif`, one way only — `else if` does
  not parse
- trait bounds join with `&`, not `+` (there are no traits, but the
  syntax is reserved and this has been "fixed" wrongly before)
- `?` binds to the **outermost** type: `*Node?` is a nullable pointer,
  not a pointer to a nullable
- no default exports, therefore no default imports
- type arguments live on **path segments**, since `Array<i32>::new`
  parameterizes a non-final segment; use `Path.Names()` and
  `Path.IsPlainName()`, never index segments as strings
- visibility is explicit with `export`; there is no capitalization rule
- `>>` is **two `>` tokens**; the lexer must never merge them or nested
  generics stop closing. The parser rejoins them for shifts.
- `<-` is scanned greedily and split apart by the parser in binary
  position. Sends are statements only.

### Contextual keywords

`move`, `copy` and `from` are **not tokens**. They are identifiers the
parser recognizes only in the one position each can occupy. They must
stay usable as ordinary names — `var from = 1` and `fn String::from(...)`
both parse.

This has bitten twice. If a word only means something in one place, it
belongs here, not in `internal/token`.

### No universe scope

If a name is not part of the formal syntax, it must be imported.
`println` is imported. `ch.close()`, `xs.len()` and `s.len()`
are methods on built-in types and need none — they are part of the type.

**Do not add predeclared functions.**

### Extension methods

`fn string::trim(self): string` attaches a method to a built-in type from
ordinary Gyro. Any module may write one.

The rule that keeps this out of universe scope: an extension is **not
attached to the type**. It is a module-scope name under its own method
name, exported and imported like a function, callable exactly where that
name is in scope. `line.trim(heap())` works because `trim` was imported.

That also disposes of coherence without traits: two modules may each
define `string::trim`; importing both is the duplicate-name error Gyro
already reports.

The compiler's own surface wins a name clash. `void` and `error` are not
extensible. An extension is still a method — calling one without a
receiver is an error.

### Errors

Errors are values. A fallible function returns `(T, error?)`. **There is
no propagation operator** — no `?`, no exceptions, no unwinding. `error`
is duck-typed: any type with `message(): string` is one.

Dropping an error is an error; discarding takes an explicit `var _, _ =`.

Programmer errors — bounds, divide by zero, null deref, overflow,
narrowing `as` — panic and abort. Not catchable, not returnable.

### The entry point

`fn main()` or `fn main(args: string[])`, either optionally `async`.
Anything else is E0523. `args[0]` is the invocation name.

---

## 5. The safety analyses (all in `internal/sema`)

These are the project's reason to exist. Each was built deliberately;
none should be weakened casually.

| analysis             | file         | what it does                                                             |
| -------------------- | ------------ | ------------------------------------------------------------------------ |
| definite assignment  | `assign.go`  | flow-sensitive; `var x: i32` tracked until every path to a read wrote it |
| leak warnings        | `leak.go`    | an `alloc<T>(heap())` reaching a block's end unfreed (W0701)                   |
| spawn races          | `race.go`    | a spawned closure capturing a `*T` or `T[]` (W0801)                      |
| borrows              | `borrow.go`  | provenance of views, written into effect rows                            |
| match exhaustiveness | `pattern.go` | enums must be covered or end in `_`                                      |

### The `flow` state

`assign.go` holds the piece sema otherwise lacks: a merging flow state.

```go
type flow struct {
    assigned  map[*symbol]bool
    settled   map[*symbol]bool   // leak analysis
    reachable bool
}
```

`mergeFlow` intersects; an unreachable branch does not constrain the
join. `branchFlow(walk)` runs a branch on a clone. `pushLoop` restores
entry state for a normal loop, and merges break states for `while true`.

The analyses are **gen-only, no-kill**, which is why loops need no
fixpoint iteration. Keep it that way.

### Borrows

What is tracked is **provenance, not lifetime**.

```go
type borrowOrigin struct {
    params []*symbol   // parameters of the enclosing function it views
    local  bool        // storage created in this frame
}
```

`local` is contagious on merge. A `T[]` or `string` result needs a `&`
entry naming where the view came from:

```gyro
fn first(xs: i32[]): i32[] !{ &xs } { return xs }
fn Stack::view(self): i32[] !{ &self } { return self.items }
```

Facts that took work to get right and are easy to break:

- a **struct carries its fields' origins**, so `View{ data: xs }` over a
  local array is frame storage while `Stack{ items: Array::new() }` is
  not. Do not treat local structs as frame storage wholesale.
- a **field chain roots at its base**: `self.items.slice()` views the
  receiver, not `items`.
- a **generic row is conditional**: `!{ &a, &b }` on `pick<T>` is
  load-bearing at `T = string` and vacuous at `T = i32`. View-ness is not
  judged on the generic declaration.
- a borrow **source** is anything holding storage — a struct counts.
  Only scalars are rejected.
- `runes()` and `bytes()` borrow **nothing**; both copy.
- `string` being a view means an accessor returning a string field
  borrows its struct. Real ergonomic cost, deliberately accepted.

---

## 6. METALS

The IR. `docs/appendix-d-metals.md` is the spec.

- one flat whole-program module; generics monomorphized under quoted
  names (`@"Pair<i32>::sum"`), closures lifted with heap env records,
  defers inlined at every exit, narrowing made explicit as `opt.unwrap`,
  `error` as a closed program-wide enum
- three sigils: `@name` function or global, `%name` register or named
  type, `$name` label
- a **global is module storage**, zero-initialized, reached through
  `global.ptr`. It carries no initializer — the front end lowers one into
  a function called before main.
- the **verifier** is real and catches type mismatches, undefined
  registers, definiteness across branches, boundary rules — and it is the
  only thing that reads a module before the backend does, so a producer
  bug that it does not catch reaches generated code

### Native layout

`docs/appendix-e-native-backend.md`. **Storage is C's; a register is one word.** A value in
memory is what `sizeof` says in C, at C's alignment, padded where C pads.
A register carries a scalar widened to a qword and an `f32` as a double,
so a load widens and a store narrows.

This bites in two places. What the runtime stores it stores the way
memory looks — a channel's elements, an `Array`'s, an `any`'s payload —
so a narrow scalar goes through a staging cell rather than by the address
of its register; forget that and an `i8` loses its sign or an `f32`
arrives as the bits of a double. And `metals.Layout` is the only place
that computes an offset: a backend that adds up field sizes itself is
wrong the first time a `u8` sits in front of a `u32`.

---

## 7. Working conventions

### No migration paths, ever

Gyro is pre-users. When a spelling changes, change it everywhere and
update the docs. **Do not** leave a compatibility shim. **Do not** add a
diagnostic whose only job is to point from the old spelling at the new
one. Same for syntax the language never had — an error naming a construct
borrowed from another language is carrying weight it has not earned.

Delete removed things outright. No tombstone comments, no placeholders
holding a gap in a numbering. Git has the history.

Applied recently: `sema.Check` (the single-file entry point) had no
production caller once fixtures moved to the loader, so it was deleted
rather than kept.

### Every diagnostic needs a code

Each front-end package maps **message format strings** to a `diag.Code`
in its own `codes.go`. The catalogue and the `gyro explain` text live in
`internal/diag/codes.go`.

`internal/diag/catalog_test.go` fails when a message has no entry, so
**rewording a message means updating the table**.

Codes are grouped: E01xx lexer, E02xx parser, E03xx literals/imports,
E04xx names, E05xx types, E06xx statements/borrows, W07xx leaks, W08xx
races. Currently E0101–E0610, W0701, W0801.

### Parser progress

Every loop over statements or declarations must guarantee forward
progress on malformed input. `TestParserAlwaysTerminates` covers this.

### Docs are part of the change

When a change touches syntax or semantics:

1. update the book chapter that owns the topic **first**
2. add a parser test
3. add or extend an example so the syntax has a real user
4. record the decision in `docs/decisions.md`

`docs/decisions.md` records what was decided **and what was rejected**,
with reasoning. Read it before proposing a change to settled syntax.

### Ask before inventing

Ask before inventing syntax or semantics the book does not cover. Guessing produces code that has to be undone. This is a real rule,
not a formality — the effect-row syntax was designed, implemented, and
then redesigned because it was not checked first.

### Commits

Short imperative subject. Body explains the reasoning when the diff does
not. **State that it was written by Claude, not a human** — as a line in
the body, not a `Co-Authored-By` trailer.

### Go style

Descriptive names (`recordID`, not `rid`). Guard clauses over nesting;
more than two levels of indentation is a smell. Short functions, one
responsibility. Comments explain **why**, never restate the code — no
comment beats an obvious one. Match surrounding code when these conflict.

### Do not reintroduce

`src/gyro.peggy`. The Peggy grammar was abandoned and deleted; it
describes a different language from the one implemented here.

---

## 8. The standard library

Bundled `.gyro` sources compiled into the binary, under
`internal/stdlib/src/`.

**A declaration with no body is a runtime intrinsic. Everything with a
body is ordinary Gyro, compiled like any other module.** What makes a
struct an opaque handle is having no fields, not the module it lives in.

Modules: `stdlib`, `mem`, `unsafe`, `fs`, `net`, `convert`, `time`, `os`, `fmt`, `math`.

`unsafe` holds the unchecked primitives, all `unsafe fn` (chapter 4.6):
`cast`, `ptr_from_int`, `ptr_to_int`, `offset`, `copy_bytes`, the three
`raw_` heap functions, `env_of` and `with_env`. `unsafe fn` is unsafe to
call, `safe fn` may use unchecked things and vouches for callers, a plain
`fn` may do neither; `unsafe` and `safe` are contextual words before `fn`
and before an import list. sema checks it in `internal/sema/unsafe.go`
(calls, union reads, `&f`); `@extern` declarations and functions of a `.c`
import are unsafe unless written `safe` or imported with `import safe`.
`mem` imports `unsafe`, never the other way round.

Runtime-backed: `print`/`println`/`eprintln`, `spawn`, `Array<T>`,
`Mutex<T>`/`MutexGuard<T>`, `from_utf8`, `mem`'s `size`/
`align`/`panic` and `unsafe`'s `raw_alloc`/`raw_free`/`raw_resize`,
`open`/`read_all`/`File::close`, `to_i32`/`to_f64`, `net::get`.

Written in Gyro: `String` (on `Array<u8>`), its UTF-8 encoder, and the
text methods — `split`, `trim`, `contains`, `starts_with`, `ends_with`,
`index_of`, `substring`, `repeat`; `mem`'s `alloc`, `free`, `heap` and
`Arena` over the allocator procedure (chapter 4.5 — an allocator is an
ordinary `Allocator` parameter, and a function allocates exactly when it
takes one). `join(parts, sep)` stays a function
because its receiver would be the slice. `fmt` is entirely Gyro too:
`printf`/`sprintf`/`eprintf` walk the format string over an `...any` pack
and read each argument with a `match`, so the whole formatter is one
module rather than a second implementation in C.

**Prefer Gyro.** Something with no interesting runtime requirement written
into `rt.c` instead is a second language to maintain it in, for nothing.

### Prefer a library function over a keyword

`spawn` was a keyword until closures made it unnecessary. Before adding
grammar, check whether a closure-taking function would do.

---

## 9. Known gaps

Kept honest in `docs/appendix-g-known-gaps.md` and `docs/decisions.md`
("Still open"). The ones that matter most:

**Linear memory has no producer.** METALS' `memory`, `trunc` and
`bitcast` came in for a bridge that read what clang made of C as wasm32.
That bridge is gone; the instructions stay, because a flat byte region
addressed by `u32` is what a producer with C's memory model needs and
that model is not going away. What exercises them now is METALS written
by hand in the corpus (`linear-memory.metals`,
`panic-linear-memory-bounds.metals`). Left undecided with it: **an
imported memory**, which is how two translation units would share one
heap — linking gives each its own.

**Blocking real applications:**

- **memory reclamation** — the allocator model (chapter 4.5) is in for
  `alloc`/`free`/`Arena`, the containers, the text functions and `format`;
  `#{}` allocates nothing. A capturing closure's environment lives
  in its frame and `keep` copies it into an allocator. The runtime's other
  storage (futures, channels, runtime error messages, and the text of
  a non-string piece a `Format` renders) still comes from the process heap.
- no sorting
- **FFI has two gaps left** — `@extern` carries primitives, `string`,
  slices, structs by value and opaque handles. No callback and no
  out-parameter, so a C API built on either (`qsort`, `SDL_PollEvent`)
  is still out of reach *through `@extern`*. Importing the `.c` instead
  routes through `internal/cc` and has no marshalling to have a gap in:
  the out-parameter case works today (`tests/testdata/run/c-import/`).

**Open in the C front end** (`internal/cc`, see
[appendix D](appendix-d-metals.md), "The C front end"):

- **Two `goto` shapes** — a jump *into* a block (a label in a statement
  list the goto is not inside), and a backward jump reaching past a later
  forward label, where the loop would have to cross out of the block.
  Both need a relooper — Ramsey's ICFP 2022 algorithm is the reference.
  Everything else works:
  a label jumped to from earlier gets a `block` around what precedes it,
  one jumped to from later gets a `loop` around what follows, and C's
  labels nest because C's statements do. A label inside a switch body is
  refused too — the body is a list of case labels.
- **Type punning is not diagnosed**, and neither is reading across two
  union members that share no initial sequence, nor reading a struct's
  storage as bytes — which is how a bit-field's placement is checked, in
  `tests/testdata/run/c-bitfield-bytes`. `void *` is in and a cast
  between pointer types is `ptr.erase` then `ptr.cast`, which is the
  producer's claim about what is at the address; a union is one storage
  with several readings of it. All of it reinterprets the bytes, exactly
  as C does. Round trips — the overwhelming majority — are fine, and so
  is the common initial sequence every tagged union in C is built on.
  What is left is undefined in C too, `char *` aliasing included, and
  catching it would need something that records which member was written.
  The evaluator used to be that and is gone.
- **A variadic function cannot cross into Gyro**: a `...` says nothing
  about what a call passes, so there is nothing for the far side to
  check. Nor can a C `enum`: Gyro's is a tagged sum. A `union` does
  cross: Gyro cannot declare one, so `headerGen` writes
  `export union Name { ... }` into generated text that only
  `parser.ParseForeign` reads as a declaration.
  `headerGen` in `internal/loader/foreign.go` **skips** an export it
  cannot spell rather than failing the module, so one such helper does
  not make a whole C library unimportable; naming a skipped one is an
  error at the import.
- **A case label inside a nested statement** — Duff's device. A switch
  body is a flat list of labels and statements here.
- **Structural matching across a *boundary***. Within one runtime an
  importer says which type it means (`metals.Import.Types`) and the
  linker compares the declarations. Across a boundary the value is
  copied, and nothing checks that two same-named records on either side
  agree.
- **A local aggregate initializer writes zeros member by member**, so
  `char buf[4096] = "hi"` is 4096 stores. The cheap fix is a bulk zero
  instruction.

Closed since this list was written: `Map<K, V>` (ordinary Gyro over
`Array<T>`), file writing and directory listing (`fs`), stdin, env vars
and exit codes (`os`), clocks and sleep (`time`); string literals,
file-scope and braced initializers, `static` locals, `switch`,
`sizeof(char *)` answering 8, function pointers, a program made of as
many `.c` files as it takes, `void *`, varargs, pointers to incomplete
types (`type %Name = opaque`, the way a library header hands out a
handle), unions (`type %Name = union { ... }`, with the common initial
sequence defined), bit-fields (a run packed into one
member of bytes, with `record align N` carrying the alignment their
declared types ask of the struct), `goto` in the shapes C is actually
written in, a C struct crossing into Gyro (an `import` may declare types,
the linker matches and resolves them, and there is one `%Point` in the
linked program), a `void *` crossing into Gyro (`anyptr` is a Gyro type,
forgotten with `as`, remembered with `unsafe`'s `cast`), and a callback crossing in both directions
(`code(A, B): C` is a Gyro type, and `&f` is how you get one).

**Safety holes:**

- **mutation rules unchecked** — `*self` vs `self`, assigning through a
  value receiver
- **`safe` is a promise nothing checks** — a `safe fn` and an
  `import safe` are the author's word (chapter 4.6)
- **use-after-free, double free and uninitialized reads undetected** —
  the heap frees for real, and a block from `alloc<T>()` is zero-filled.
  The evaluator poisoned freed and unwritten cells and aborted on a
  read; it is gone. The check belongs in a quarantining allocator
  written in Gyro over chapter 4.5, which does not exist yet.
- views through a pointer are untraceable
- effect rows under-report where a row cannot travel: calling a function
  value contributes nothing

Settled since: float division by zero and NaN ordering (IEEE for the
operators, a total order for `compare`), `<<` overflow (a shift never
panics, it drops the bits that leave the width), the untyped-operand
folding that made `1 << 40` disagree between the checker and a tier, and
the `select` arm chosen among ready arms, which is now unspecified by the
language rather than a divergence — a fair native runtime is queued work.

**Design questions open:** `@comptime` evaluation subset, drop/RAII (which
governs `MutexGuard::unlock`, `free`, `close` alike), decorator execution
model, `syntaxfn`, the Stack package manager, METALS linking/binary
encoding.

---

## 10. Hard-won gotchas

Things that cost real time. None are in any other file.

**The corpus finds what per-tier tests cannot.** Moving execution tests
to one corpus immediately surfaced five divergences that had existed for
a long time: `println(null)` did not lower, deferred call arguments were
evaluated at exit rather than registration, printing a tuple needed a
synthesized repr, and the native runtime detected neither use-after-free
nor uninitialized reads. Each tier had kept its own hand-picked subset.

**Register aliasing in the lowering.** A local's METALS register is
written in place by assignment. Anything capturing "the register name"
for later use captures the _final_ value. This is what broke defer.

**Instantiation errors land in the generic's body**, with a _related_
"in the instantiation X required here" at the call site. Tests that
assume both are at the call site are wrong.

**Cascading diagnostics are real.** A rejected row entry leaves the row
empty, so the return is unaccounted for too — two diagnostics, both
correct. `return null, 0` against `(i32, error?)` is two mismatches.

**Per-module checkers.** `c.funcSigs` is per-checker. For an imported
declaration the signature lives in the _defining_ module's checker; reach
it through `c.program.envs[decl]`.

**Synthesize in the lowering, not the backend.** Derived printing was a
runtime import. Moving it to one synthesized METALS function per type
meant no backend and no C work at all. Prefer this shape.

**The native layout is easy to forget and fails quietly.** Storage is
C's, a register is a word, and the two are not the same bytes. A
representation mismatch produces METALS that reads correctly and a binary
that prints garbage, which looks like a codegen bug and often is not one.
When anything crosses between generated code and the C runtime carrying a
slice or an element, check the stride and the width first — and read
`gyro metals` before `gyro nasm`, since that says which half is wrong.

**Empty error paths crash.** Returning "no type parameters" from an error
path while the declaration named one panicked the checker. Error paths
must still return well-formed data.

---

## 11. Current state, honestly

Programs parse, check, lower, verify and compile to native Linux x86-64
binaries. Channels, `spawn` and `async` run on OS threads.
Editor tooling exists: two tree-sitter grammars and a language server
with diagnostics, hover, definition, symbols, completion, inlay hints.

Gyro is usable for **CLI tools today**. It is not usable for anything
long-running, because nothing reclaims memory.

It also reaches C. `@extern("SDL2")` on a bodyless declaration links a
symbol, and `examples/triangle.gyro` rasterizes a Gouraud-shaded triangle
in pure Gyro and puts it in an SDL window.

A real program that works end to end:

```gyro
import { println, eprintln, Array, String, join } from "stdlib"
import { split, trim, starts_with } from "stdlib"
import { open, read_all } from "fs"
import { heap } from "mem"

fn main(args: string[]) {
  if args.len() < 2 {
    eprintln("usage: wc <file>")
    return
  }
  var file, err = open(args[1])
  if err != null {
    eprintln("cannot open #{args[1]}: #{err.message()}")
    return
  }
  defer file.close()

  var text, readErr = read_all(heap(), file)
  if readErr != null { return }

  var lines = text.split(heap(), "\n")
  defer lines.free()
  for line in lines.slice() {
    var clean = line.trim(heap())
    if clean.len() == 0 || clean.starts_with(heap(), "#") { continue }
    println(clean.split(heap(), " ").get(0))
  }
}
```
