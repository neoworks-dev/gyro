# Gyro: orientation for an agent

Source material for a system prompt. Everything an agent needs to work on
this repository effectively, including the parts that are not derivable
from the code and the parts that are only learned by getting them wrong.

`CLAUDE.md` is the short operational version. This is the long one, with
reasoning attached.

### Which docs are authoritative

| file                | status                                                 |
| ------------------- | ------------------------------------------------------ |
| `docs/language.md`  | **source of truth** for syntax and semantics           |
| `docs/decisions.md` | what was decided and what was rejected, with reasoning |
| `docs/metals.md`    | the IR spec                                            |
| `docs/native.md`    | the native backend and its layout rules                |
| `docs/tooling.md`   | grammars and language server                           |
| `CLAUDE.md`         | operational rules for an agent                         |
| `docs/index.md`     | **stale fossil — ignore**                              |
| `README.md`         | stale in places; verify before trusting                |

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
  ├─→ internal/meval    METALS evaluator
  ├─→ internal/nasm     METALS → x86-64 NASM
  └─→ internal/cgen     METALS → C
                        both link internal/crt, the shared C runtime
```

Everything the compiler can read becomes METALS, and everything that
runs, runs from METALS. There is no second execution path.

### Package sizes, roughly

| package  | lines | what it is                                                    |
| -------- | ----- | ------------------------------------------------------------- |
| `sema`   | 6.4k  | the bulk of the compiler; every safety analysis lives here    |
| `lower`  | 4.3k  | Gyro → METALS, including monomorphization and closure lifting |
| `metals` | 3.5k  | IR types, text format, verifier                               |
| `nasm`   | 2.6k  | x86-64 code generation                                        |
| `cgen`   | 2.5k  | the C sibling backend, prelude included                       |
| `crt`    | 1.0k  | `src/rt.c`, the runtime both native backends link             |
| `lsp`    | 2.5k  | language server, hand-rolled, zero dependencies               |
| `parser` | 2.2k  |                                                               |
| `meval`  | 1.6k  |                                                               |
| `ast`    | 1.5k  | tree + dumper                                                 |

Also: `tree-sitter-gyro/` and `tree-sitter-metals/` (grammars, external
scanner, queries, corpus), `internal/stdlib/` (bundled `.gyro` sources
compiled into the binary), `examples/` (10 reference programs), and
METALS' other producers — `internal/lisp/`, `internal/wasm/` and
`internal/cc/` (C read directly, which is what a Gyro program imports a
`.c` through; the wasm bridge keeps a `.c` that is the whole program).

### Three tiers, no oracle

This is the single most important architectural fact.

**Every program runs three ways and all three must agree byte for byte:**
the METALS evaluator, a native binary from the assembler backend, and a
native binary from the C backend.

**No tier defines the answer.** The golden file does, and it is written
by hand. There is no flag that regenerates it from a tier's output,
because a tier that can write its own exam has stopped being a check on
anything — the first version of this corpus rewrote goldens from the
tree-walking interpreter, and what that bought was a tier whose bugs
became the specification until someone read the file.

So when tiers disagree, none of them is presumptively right: read
`docs/language.md`, decide what the program should print, and record the
decision in `docs/decisions.md` if the docs did not already answer it.

---

## 3. Testing model

**This is where most of the leverage is. Read this before writing a test.**

Two corpora written in Gyro carry most of the suite. Prefer adding to
them over writing another Go test.

### 3.1 Execution corpus — `tests/testdata/run/`

66 cases. One `.gyro` file beside the `.out` it must print.

```
tests/testdata/run/tuples.gyro
tests/testdata/run/tuples.out
tests/testdata/run/cross-module/main.gyro    # multi-module case
tests/testdata/run/cross-module/util.gyro
tests/testdata/run/cross-module/expected.out
```

Every case runs on **all three tiers** — `metals`, `native` (x86-64) and
`cnative` (generated C) — and all three are held to the same golden,
which you write.

Directives, written as comments in the root file:

| directive                  | meaning                                                                                    |
| -------------------------- | ------------------------------------------------------------------------------------------ |
| `//gyro:panic <fragment>`  | must abort; every tier's message must contain the fragment; the golden holds stdout before the panic |
| `//gyro:unordered`         | concurrent output; tiers must all succeed but output is not compared                       |
| `//gyro:skip <tier> <why>` | a known gap: `metals`, `native` or `cnative`                                               |
| `//gyro:args a b c`        | main's arguments, after a fixed `argv[0]` of `program`                                     |

**A gap belongs in the corpus as a skip with its reason attached, not in
prose.** A skip is a program that will fail the day the gap closes. Three
skips exist right now, all the same gap (native runtime does not poison
freed or uninitialized storage).

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
builds them with the flags `cgen` uses on its own output (`cc -O1
-fno-strict-aliasing`) and adds a `cc` column with `nasm/cc` and
`cgen/cc` beside it, so the table answers what the language costs against
the C it replaces and not only what one backend costs against the other.
A ported case imports its library **out of the execution corpus** rather
than copying it, so there is one port per library: the one whose output
the corpus already compares against the C original's.

Each program times its own hot region with `time.monotonic` —
`clock_gettime` in the C references — and prints `gyro:bench
<nanoseconds>` on stderr, so setup, process start and runtime init stay
out of the number; process wall time and build time are reported in a
second table. Every binary in a case must print the same checksum on
stdout or the case fails — comparing programs that computed different
things measures nothing, and for a port that agreement is the whole
claim.

The suite is off during `go test ./...` and runs from `make bench`
(`BENCH=<substring>` filters, `REPEAT=<n>` sets the run count; the
fastest run counts). Read the **spread**, not the average: a backend
behind by the same factor everywhere has one problem, a backend at 1.06x
on four cases and 2.25x on the fifth has a specific one.

The six ports spread widely against C — `regex` 1.4x, `perft` 4.4x,
`md4c` 8.8x, `jsmn` 9.3x, `cjson` 11.1x, `sha256` 13.2x — and what
separates them is how much work each does per element access, since every
one of those goes through a runtime call and a bounds check while C reads
a byte. The two backends spread differently, and along a different axis:
`regex` 4.86x, `perft` 3.56x and `md4c` 3.42x are all call-dominated,
while everything else sits between 1.00x and 2.10x, which is what points
at inlining rather than register pressure.

### 3.5 Commands

```sh
make build     # -> bin/gyro
make test      # go test ./...
make bench     # both native backends on tests/bench, ratio reported
make check     # parse every example
make vet
make grammar   # regenerate + test both tree-sitter grammars
```

Execution goldens are written by hand. There is no `-update`.

Before any commit: `go test ./...`, `make check`, `make vet`. If grammars
changed, `make grammar` too (46 and 8 parses respectively).

---

## 4. Language surface

`docs/language.md` is the source of truth. Highlights an agent needs
loaded:

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
`println` is imported. `ch.close()`, `xs.len()`, `s.runes()`, `s.bytes()`
are methods on built-in types and need none — they are part of the type.

**Do not add predeclared functions.**

### Extension methods

`fn string::trim(self): string` attaches a method to a built-in type from
ordinary Gyro. Any module may write one.

The rule that keeps this out of universe scope: an extension is **not
attached to the type**. It is a module-scope name under its own method
name, exported and imported like a function, callable exactly where that
name is in scope. `line.trim()` works because `trim` was imported.

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
| leak warnings        | `leak.go`    | an `alloc<T>()` reaching a block's end unfreed (W0701)                   |
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

The IR. `docs/metals.md` is the spec.

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
  registers, definiteness across branches, boundary rules
- `internal/meval` executes it, and is where a lowering bug shows up
  first — it is the tier closest to what the lowering emitted

### Native layout

`docs/native.md`. **Storage is C's; a register is one word.** A value in
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

1. update `docs/language.md` **first**
2. add a parser test
3. add or extend an example so the syntax has a real user
4. record the decision in `docs/decisions.md`

`docs/decisions.md` records what was decided **and what was rejected**,
with reasoning. Read it before proposing a change to settled syntax.

### Ask before inventing

Ask before inventing syntax or semantics `docs/language.md` does not
cover. Guessing produces code that has to be undone. This is a real rule,
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

Modules: `stdlib`, `mem`, `fs`, `net`, `convert`, `time`, `os`, `fmt`.

Runtime-backed: `print`/`println`/`eprintln`, `spawn`, `Array<T>`,
`Mutex<T>`/`MutexGuard<T>`, `from_utf8`, `alloc`/`free`/`size`,
`open`/`read_all`/`File::close`, `to_i32`/`to_f64`, `net::get`.

Written in Gyro: `String` (on `Array<u8>`), its UTF-8 encoder, and the
text methods — `split`, `trim`, `contains`, `starts_with`, `ends_with`,
`index_of`, `substring`, `repeat`. `join(parts, sep)` stays a function
because its receiver would be the slice. `fmt` is entirely Gyro too:
`printf`/`sprintf`/`eprintf` walk the format string over an `...any` pack
and read each argument with a `match`, so the whole formatter is one
module and no tier implements it separately.

**Prefer Gyro.** Two native implementations (meval, rt.c) of something
with no interesting runtime requirement are two places to diverge.

### Prefer a library function over a keyword

`spawn` was a keyword until closures made it unnecessary. Before adding
grammar, check whether a closure-taking function would do.

---

## 9. Known gaps

Kept honest in `docs/language.md` ("Still to come") and
`docs/decisions.md` ("Still open"). The ones that matter most:

**Open in the wasm bridge** (`internal/wasm`, see
[`decisions.md`](decisions.md)):

- **the float intrinsics** — `sqrt`, `floor`, `ceil`, `trunc`, `nearest`,
  `abs`, `min`, `max`, `copysign` have no METALS instruction and no
  runtime import, so a C program that reaches for one is refused by name.
  They want a decision about where math belongs, not a patch.
- **multi-value** blocks, results and calls; **SIMD**; **reference
  types**; **atomics**; **passive data segments**. Each is refused with
  its own message rather than skipped.
- **an imported memory**, which is how wasm spells two modules sharing one
  heap. Linking gives each module its own, which is right for a sandbox
  and is the thing a real C front end will have to revisit.
- **no libc.** The bridge links freestanding; a program reaches the
  outside through an import it declares itself. wasi-libc's split — a
  syscall floor as imports, everything above it compiled — is the shape
  to copy, and nothing has been.

**Blocking real applications:**

- **memory reclamation** — every `#{}`, `split`, `text()` allocates and
  nothing frees it. Fine for a CLI that exits; fatal for a server.
- no sorting
- **FFI has two gaps left** — `@extern` carries primitives, `string`,
  slices, structs by value and opaque handles. No callback and no
  out-parameter, so a C API built on either (`qsort`, `SDL_PollEvent`)
  is still out of reach *through `@extern`*. Importing the `.c` instead
  routes through `internal/cc` and has no marshalling to have a gap in:
  the out-parameter case works today (`tests/testdata/run/c-import/`).

**Open in the C front end** (`internal/cc`, see
[`metals.md`](metals.md), "The C front end"):

- **Two `goto` shapes** — a jump *into* a block (a label in a statement
  list the goto is not inside), and a backward jump reaching past a later
  forward label, where the loop would have to cross out of the block.
  Both need the relooper LLVM does for the bridge. Everything else works:
  a label jumped to from earlier gets a `block` around what precedes it,
  one jumped to from later gets a `loop` around what follows, and C's
  labels nest because C's statements do. A label inside a switch body is
  refused too — the body is a list of case labels.
- **Type punning runs on two tiers**, and so does reading across two
  union members that share no initial sequence, and reading a struct's
  storage as bytes — which is how a bit-field's placement is checked, in
  `tests/testdata/run/c-bitfield-bytes`. `void *` is in and a cast
  between pointer types is `ptr.erase` then `ptr.cast`, which is the
  producer's claim about what is at the address; a union is one storage
  with several readings of it. The natives reinterpret the bytes; the
  evaluator knows what is really there and says so. Round trips — the
  overwhelming majority — are fine, and so is the common initial sequence
  every tagged union in C is built on. What is left is undefined in C
  too, `char *` aliasing included, and running it here would need a
  byte-addressable object model in the evaluator. See
  `tests/testdata/run/union-punning.metals`.
- **A variadic function cannot cross into Gyro**: a `...` says nothing
  about what a call passes, so there is nothing for the far side to
  check. Nor can a `union`, which Gyro cannot declare.
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
  `char buf[4096] = "hi"` is 4096 stores. Correct, because a `slot` is
  zeroed by the native backends and deliberately left unwritten by the
  evaluator, and a partial initializer must not depend on the tier. The
  cheap fix is a bulk zero instruction, not a weaker evaluator.

Closed since this list was written: `Map<K, V>` (ordinary Gyro over
`Array<T>`), file writing and directory listing (`fs`), stdin, env vars
and exit codes (`os`), clocks and sleep (`time`); string literals,
file-scope and braced initializers, `static` locals, `switch`,
`sizeof(char *)` answering 8, function pointers, a program made of as
many `.c` files as it takes, `void *`, varargs, pointers to incomplete
types (`type %Name = opaque`, the way a library header hands out a
handle), unions (`type %Name = union { ... }`, with the common initial
sequence answered on every tier), bit-fields (a run packed into one
member of bytes, with `record align N` carrying the alignment their
declared types ask of the struct), `goto` in the shapes C is actually
written in, a C struct crossing into Gyro (an `import` may declare types,
the linker matches and resolves them, and there is one `%Point` in the
linked program), a `void *` crossing into Gyro (`anyptr` is a Gyro type,
and `as` goes both ways), and a callback crossing in both directions
(`code(A, B): C` is a Gyro type, and `&f` is how you get one).

**Safety holes:**

- **mutation rules unchecked** — `*self` vs `self`, assigning through a
  value receiver
- **native use-after-free and uninitialized reads undetected** — the
  evaluator poisons and aborts; the C runtime frees for real and leaves
  storage as the allocator returned it. The tier you ship is the one
  that does not check. It cannot even name a double free: `gy_free`
  writes its poison and then hands the block to libc, which reuses those
  bytes, so both tiers say `free of memory that is not allocated`.
- views through a pointer are untraceable
- effect rows under-report where a row cannot travel: calling a function
  value contributes nothing, and creating a capturing closure is not
  counted as `alloc`

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
runtime import the evaluator could answer and the native backend could
not. Moving it to one synthesized METALS function per type fixed both
tiers at once, with no backend or C work. Prefer this shape.

**The native layout is easy to forget and fails quietly.** Storage is
C's, a register is a word, and the two are not the same bytes. A
representation mismatch produces _correct evaluator output_ and native
garbage, which reads like a backend bug and is not one. When anything
crosses the Go/C boundary carrying a slice or an element, check the
stride and the width first.

**Untyped literals need defaulting in more than one place.** `4.double()`
needed `types.Default` in sema _and_ a matching case in the tier that
runs it.

**Empty error paths crash.** Returning "no type parameters" from an error
path while the declaration named one panicked the checker. Error paths
must still return well-formed data.

---

## 11. Current state, honestly

Programs parse, check, run, lower, verify, evaluate and compile to native
Linux x86-64 binaries. Channels, `spawn` and `async` run on OS threads.
Editor tooling exists: two tree-sitter grammars and a language server
with diagnostics, hover, definition, symbols, completion, inlay hints.

Gyro is usable for **CLI tools today**. It is not usable for anything
long-running, because nothing reclaims memory.

It also reaches C. `@extern("SDL2")` on a bodyless declaration links a
symbol, and `examples/triangle.gyro` rasterizes a Gouraud-shaded triangle
in pure Gyro and puts it in an SDL window — which is the first thing here
that the evaluator cannot run, since it is Go with no C in it.

A real program that works end to end, identically on all three tiers:

```gyro
import { println, eprintln, Array, String, join } from "stdlib"
import { split, trim, starts_with } from "stdlib"
import { open, read_all } from "fs"

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

  var text, readErr = read_all(file)
  if readErr != null { return }

  var lines = text.split("\n")
  defer lines.free()
  for line in lines.slice() {
    var clean = line.trim()
    if clean.len() == 0 || clean.starts_with("#") { continue }
    println(clean.split(" ").get(0))
  }
}
```
