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

`internal/interp` is a tree-walking interpreter over the **checked syntax
tree** — it does not use METALS. It is the executable specification.

### Package sizes, roughly

| package  | lines | what it is                                                    |
| -------- | ----- | ------------------------------------------------------------- |
| `sema`   | 6.4k  | the bulk of the compiler; every safety analysis lives here    |
| `lower`  | 4.3k  | Gyro → METALS, including monomorphization and closure lifting |
| `metals` | 3.5k  | IR types, text format, verifier                               |
| `interp` | 3.2k  | the spec                                                      |
| `nasm`   | 2.6k  | x86-64 code generation                                        |
| `cgen`   | 2.5k  | the C sibling backend, prelude included                       |
| `crt`    | 1.0k  | `src/rt.c`, the runtime both native backends link             |
| `lsp`    | 2.5k  | language server, hand-rolled, zero dependencies               |
| `parser` | 2.2k  |                                                               |
| `meval`  | 1.6k  |                                                               |
| `ast`    | 1.5k  | tree + dumper                                                 |

Also: `tree-sitter-gyro/` and `tree-sitter-metals/` (grammars, external
scanner, queries, corpus), `internal/stdlib/` (bundled `.gyro` sources
compiled into the binary), `examples/` (8 reference programs).

### The three-tier oracle

This is the single most important architectural fact.

**Every program runs three ways and all three must agree byte for byte:**
the interpreter, the METALS evaluator, and a native binary. The
interpreter defines the answer; the other two are measured against it.

When they disagree, the interpreter is right unless there is a reason to
decide otherwise — and deciding otherwise is a language decision that
belongs in `docs/decisions.md`.

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

Every case runs on **all four tiers** — `interp`, `metals`, `native`
(x86-64) and `cnative` (generated C) — and all four are held to the same
golden. `go test ./tests -update` rewrites goldens from what the
interpreter produced.

Directives, written as comments in the root file:

| directive                  | meaning                                                                                          |
| -------------------------- | ------------------------------------------------------------------------------------------------ |
| `//gyro:panic <fragment>`  | must abort; fragment matches the interpreter's message; the golden holds stdout before the panic |
| `//gyro:unordered`         | concurrent output; tiers must all succeed but output is not compared                             |
| `//gyro:skip <tier> <why>` | a known gap: `interp`, `metals`, `native` or `cnative`                                           |
| `//gyro:args a b c`        | main's arguments, after a fixed `argv[0]` of `program`                                           |

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
- `interp` — `Panic`'s source position, nothing else
- `lower` — METALS naming and record layout, nothing about output
- `sema` — `editDistance`, and the `Info` the backends read

### 3.4 Commands

```sh
make build     # -> bin/gyro
make test      # go test ./...
make check     # parse every example
make vet
make grammar   # regenerate + test both tree-sitter grammars
go test ./tests -update    # rewrite execution goldens
```

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
- `internal/meval` executes it and is the lowering's oracle

### Native layout, v1

`docs/native.md`. **Every scalar occupies one qword. Nothing is packed.**

This bites. A `u8[]` slice is word-strided while a string's bytes are
packed UTF-8 — the two cannot alias, which is why `bytes()` copies. Any
"view" conversion between differently-strided representations is wrong.

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

Modules: `stdlib`, `mem`, `fs`, `net`, `convert`.

Runtime-backed: `print`/`println`/`eprintln`, `spawn`, `Array<T>`,
`Mutex<T>`/`MutexGuard<T>`, `from_utf8`, `alloc`/`free`/`size`,
`open`/`read_all`/`File::close`, `to_i32`/`to_f64`, `net::get`.

Written in Gyro: `String` (on `Array<u8>`), its UTF-8 encoder, and the
text methods — `split`, `trim`, `contains`, `starts_with`, `ends_with`,
`index_of`, `substring`, `repeat`. `join(parts, sep)` stays a function
because its receiver would be the slice.

**Prefer Gyro.** Three native implementations (interp, meval, rt.c) of
something with no interesting runtime requirement is three places to
diverge.

### Prefer a library function over a keyword

`spawn` was a keyword until closures made it unnecessary. Before adding
grammar, check whether a closure-taking function would do.

---

## 9. Known gaps

Kept honest in `docs/language.md` ("Still to come") and
`docs/decisions.md` ("Still open"). The ones that matter most:

**Blocking real applications:**

- **memory reclamation** — every `#{}`, `split`, `text()` allocates and
  nothing frees it. Fine for a CLI that exits; fatal for a server.
- no sorting

Closed since this list was written: `Map<K, V>` (ordinary Gyro over
`Array<T>`), file writing and directory listing (`fs`), stdin, env vars
and exit codes (`os`), clocks and sleep (`time`).

**Safety holes:**

- **mutation rules unchecked** — `*self` vs `self`, assigning through a
  value receiver
- **native use-after-free and uninitialized reads undetected** — the
  interpreter and evaluator poison and abort; the C runtime frees for
  real and leaves storage as the allocator returned it. The tier you ship
  is the one that does not check.
- views through a pointer are untraceable
- concrete effects (`io`, `alloc`) parsed and reserved but unchecked

**Semantic divergences, undecided:**

- checker folds `1 << 40` into i64 context; the interpreter evaluates as
  i32 and panics
- `<<` overflow panics in the interpreter, wraps in METALS `shl`
- select fairness: evaluator picks at random, native polls in order

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

**The native layout is easy to forget and fails quietly.** One qword per
scalar, nothing packed. A representation mismatch produces _correct
interpreter output_ and native garbage, which reads like a backend bug
and is not one. When anything crosses the Go/C boundary carrying a slice,
check the stride first.

**Untyped literals need defaulting in more than one place.** `4.double()`
needed `types.Default` in sema _and_ a matching case in the interpreter.

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

A real program that works end to end, identically interpreted and native:

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
