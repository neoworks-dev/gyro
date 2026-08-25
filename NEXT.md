# Next

## Storage is C's layout — landed 2026-08-25

A value in memory is now what `sizeof` would say in C: natural widths,
natural alignment, padding where C pads. A METALS register still carries
a scalar widened to a qword and an `f32` as a double, so a load widens
and a store narrows; `docs/native.md` has the table and the conversions,
and `docs/decisions.md` has why there is one layout rather than two.

This is step one of the memory-model commitment made the same day, and it
came out differently from how that entry planned it. `@repr(C)` as an
opt-in beside the qword layout would have given `ptr<i32>` two meanings,
and `p.x` on any addressable struct lowers to `field.ptr` and a `load`,
so field addresses are not a corner to wave off. One layout, C's, no
decorator.

- **`metals.Layout`** — the offsets belong to the format, not to a
  backend. Two backends computing padding separately agree until the
  first `u8` in front of a `u32`.
- **Structs cross `@extern` by value**, both ways, because the bytes are
  the same bytes. The generated shim declares the C struct and carries a
  `_Static_assert` per field offset, so the claim is checked by `cc` on
  every build; the wrapper is also where a struct becomes C's by-value
  argument, so neither backend implements SysV struct classification.
- **`ffi.pack` is gone.** A `u32[]` *is* a `const uint32_t *`, so a slice
  is lent rather than restrided. `ffi.cstr` remains: C wants a NUL where
  Gyro has a length, which is representation and not layout.
- **What the runtime stores, it stores the way memory looks** — channel
  elements, `Array` elements, a future's result, an `any`'s payload —
  because `Array<T>::slice` borrows the Array's own elements and the two
  must agree on stride. A narrow scalar reaches the runtime through a
  staging cell instead of by the address of its register.
- **An `f32` constant was not rounded to single precision** by either
  native backend, which the evaluator did — found while writing the
  corpus case, fixed in both.
- Corpus: `packed-scalars.gyro`, `extern-struct.gyro`, and narrow fields
  added to `any-ops.metals`. `internal/metals/layout_test.go` covers the
  padding cases, `internal/crt/shim_test.go` the generated struct.

### Open after it

- [ ] **Callbacks and out-parameters** are the rest of the wider FFI, and
      the reason the SDL demo pumps events instead of polling them.
- [ ] **The direct C front end** (`internal/cc`) is still the step that
      makes "any existing C program imports unmodified" true.

## The interpreter is gone — landed 2026-08-25

`internal/interp` is deleted. Three tiers remain, all over METALS: the
evaluator and the two native backends. `gyro run` survives and lowers
first, and since every input becomes METALS it now takes `.gyro`,
`.metals`, `.lisp`, `.c` and `.wasm` — `gyro meval` is gone rather than
kept as a second spelling of one act.

**No tier is the oracle, and goldens are written by hand.** `-update` is
deleted with the tier it regenerated from. The reasoning is in
`docs/decisions.md`; the short version is that an oracle tier writes its
own exam, so an interpreter bug became the specification the moment
anyone ran `-update`, and the other tiers were then held to that bug.

It was also falling behind: it walked Gyro syntax, so it could never run
a `.metals`, `.lisp`, `.c` or `.wasm` root, and it was the only tier that
computed `f32` arithmetic at double precision.

Checking `//gyro:panic` against every tier instead of the oracle alone
immediately caught four fragments that matched only the interpreter's
wording, and one real divergence: `gy_free` writes its poison and then
hands the block to libc, which reuses those bytes, so the C runtime can
never distinguish a double free from a wild pointer. Both now say `free
of memory that is not allocated`.

## Float intrinsics and a libc for C — landed 2026-08-25

- **Nine float intrinsics as METALS instructions** — `fsqrt`, `fabs`,
  `fceil`, `ffloor`, `ftrunc`, `fnearest`, `fmin`, `fmax`, `fcopysign`.
  Instructions rather than runtime imports because wasm has them as
  opcodes, so C does, and an import would mean calling out of the sandbox
  to round a number. `internal/stdlib/src/math.gyro` gives Gyro the same
  nine — it had no `sqrt` at all before this.
- **`f32` arithmetic was computed at double precision in every tier.**
  Fixed: every operation producing an `f32` rounds through single
  precision. `(1e20f * 1e20f) / 1e20f` is `+Inf` now, as in C.
- **`inf`, `-inf` and `nan`** are how a non-finite float constant is
  written. There was no spelling before, and the bridge produced
  `+Inf.0`, which did not reparse.
- **`internal/clibc`** — a C standard library compiled in beside every
  program: string.h, malloc over `memory.grow`, stdio with a full
  printf, the parsers, qsort/bsearch, ctype, math.h. wasi-libc was
  rejected because it needs a sysroot almost nobody has installed, so
  the case proving it worked would skip everywhere.
- **The floor is three imports, all scalars** — `write_byte`,
  `read_byte`, `exit`. Not `fd_write`, because that takes a pointer into
  the guest's memory and a linear address is a `u32`. stdio buffers
  inside the guest and drains a byte at a time.
- Corpus: `math.gyro`, `float-ops.metals`, `c-float.c`, `c-libc.c`,
  `c-libc-io.c`. The libc cases match glibc byte for byte on all three
  tiers.

### Open after it

- [ ] **printf's `%f`/`%e`/`%g` are exact only below 2^53.** Half-to-even
      and glibc-identical while `|v| * 10^precision` fits a double's
      integer range, which covers everything printed to be read; above it
      the last digit can differ by one. `tests/testdata/run/cjson/
      dtoa.gyro` is the exact algorithm already written in this
      repository — porting it into `internal/clibc/src/stdio.c` closes
      this, and porting it into `internal/stdlib/src/fmt.gyro` closes the
      identical gap on Gyro's side. One algorithm, two callers.
- [ ] **Per-byte i/o at the floor.** Correct and slow: one runtime call
      per byte written. The fix is not a bulk import — that would make a
      linear address a pointer at the boundary — but a bulk *instruction*
      copying between a linear memory and an `Array<u8>` handle, checked
      on both sides. Worth doing when a benchmark shows it, not before.
- [ ] **No `FILE`, no `fopen`.** The floor has three descriptors and no
      way to name a fourth. Real file i/o needs a capability design, not
      another import.
- [ ] **`clibc` has no `setjmp`, `time`, `locale` or wide characters.**
      Each is a decision rather than an omission to fill in silently.

## C on METALS — landed 2026-08-25

C compiles to METALS and runs on all three METALS tiers. `gyro cc
prog.c` prints the IR; the corpus case `c-bridge.c` produces the same
output under the evaluator, the assembler backend and the C backend, and
the same output as the program compiled natively by clang.

This is stage zero of making C a producer, and it was done in this order
on purpose: the IR extension was the risky part, and a bridge tests it
against real compiled C months before a front end could.

- **Linear memory in METALS** — a `memory` declaration, `data` segments,
  and `mem.load`/`mem.store`/`mem.size`/`mem.grow`/`mem.copy`/`mem.fill`.
  A flat, bounds-checked byte region, little-endian, sized in 64 KiB
  pages. Addresses are `u32`, deliberately not `ptr<T>`: a linear address
  cannot appear in a crossing signature, so the boundary rules did not
  move, and every access is checked, so a producer that is internally
  unsafe still cannot reach anything it did not declare. That check is
  the argument for compiling C in rather than linking it.
- **`trunc` and `bitcast`** — the wrapping narrowing and the same-width
  reinterpretation. `conv` stays value-preserving and trapping, which is
  what Gyro's `as` needs; a C cast and a signedness-per-operation
  language need the other two, and neither is `conv` with a rule removed.
- **`internal/wasm`** — a wasm32 binary reader and a translator. wasm's
  operand stack becomes registers named for depth *and* type, which is
  what makes a slot that holds an i32 here and an f64 there fit a format
  where a register's type is fixed. Its control flow maps across almost
  unchanged, because LLVM has already relooped it — which is why this
  route exists at all.
- **Both native backends** — `gy_memory` in `rt.c` with the allocation,
  growth, bulk operations and abort text; the scalar access inlined by
  each backend, since that is the hot path of anything a C front end will
  emit.

### Open after it

- [x] **The float intrinsics** — landed as METALS instructions, and as
      `internal/stdlib/src/math.gyro` on Gyro's side.
- [x] **libc** — landed as `internal/clibc`, bundled rather than
      wasi-libc, over a three-import scalar floor.
- [ ] **A shared memory between modules.** Linking gives each its own,
      which is right for a sandbox and wrong for two C translation units
      that expect one heap. The bridge never sees this because wasm-ld
      has already linked; a front end will.
- [ ] **`internal/cc`**, the real front end: chibicc-shaped, its own
      scheme, with the relooper (Ramsey's ICFP 2022 algorithm) as the one
      piece the bridge is currently getting for free. Its output diffed
      against this bridge's on the same C is the oracle that makes it
      cheap to trust.

## Foreign functions — landed 2026-08-24

`@extern("SDL2")` on a declaration with no body links a C symbol, and
`examples/triangle.gyro` puts a triangle Gyro rasterized itself into an
SDL window.

- **`@extern`** — a fifth compiler-known decorator, so no new grammar. It
  takes the library and an optional `symbol =` override, and it also
  marks the fieldless struct that stands for an opaque C pointer.
  `internal/sema/extern.go` holds it to what may sit under it and to what
  may cross (E0613, E0614).
- **`c:` module scheme** — a foreign import lowers to `c:SDL2`, which
  makes the verifier read the signature as boundary-crossing for free and
  tells the backends what to link.
- **Marshalling in the lowering** — `ffi.cstr`, `ffi.pack`,
  `ffi.from_cstr`, `ffi.release` and the two handle/`opt` conversions, so
  what reaches either backend has scalar and handle arguments only. The
  two copies are forced by the layout rule: a Gyro `u32[]` is qword-
  strided and `const uint32_t *` is not. (`ffi.pack` and that rule are
  both gone as of the layout entry at the top.)
- **`crt.Shim`** — one generated C wrapper per symbol, compiled beside
  `rt.c`, applying C's real widths. Both backends call the wrapper, so
  neither knows an `int` from an `int64_t`.

Still narrow, and deliberately: no struct by value, no callback, no
out-parameter, which is why the demo pumps events instead of polling
them. The next thing here is a packed representation, not another
special case. (It landed as C's layout outright; structs by value came
with it, and callbacks and out-parameters are what is left.)

**The cost, which is the reason this was a decision and not a patch:** a
program that calls C runs on the two native tiers only. The interpreter
and the evaluator are Go with no cgo, so both refuse it. `tests/testdata/
run/extern-c.gyro` carries the skips with their reasons.

## Cross-language METALS — landed 2026-08-24

METALS has a second producer and a linker, and a Gyro program can import
a module another language wrote:

```gyro
import { fib } from "./fib.lisp"
```

- **`internal/lisp`** — a statically typed Scheme. `define`, `if`, `let`,
  `let*`, `set!`, `begin`, `cond`, `and`/`or`, `lambda`, internal
  `define`, closures with capture-by-copy, n-ary and chained operators,
  i64/f64/bool/str. Types are declared with `(: name (-> i64 i64))`, and
  the surface is otherwise ordinary Scheme. Declaring them is what makes
  a call from Gyro a direct call with nothing inspected at run time.
- **`metals.Link`** — the pass `metals.md` listed as undesigned. Merges
  modules root-last, qualifies everything a dependency owns, resolves
  imports to direct calls, and compares an importer's declared signature
  against the exporter's actual one. That check had nowhere to live
  before.
- **`dyn` renamed to `any`, and implemented.** It was dead in every
  consumer — `nasm` literally failed on it. Now real in `meval`, `crt`,
  `cgen` and `nasm`: two words, a descriptor and a payload, interned one
  per type, so a shape test is a pointer compare and wrapping a scalar
  allocates nothing. `any.is` is new — without it an `any` could be
  produced but never received, since every other way in traps.
- **A closure's environment must be `ptr<record>`.** The verifier
  accepted a record value; both native backends miscompiled one. Gyro's
  lowering only ever emitted `new`, so nothing had exercised it. The
  rule was tightened, not the backends loosened — this is the first
  thing the second producer found and the argument for having one.
- Corpus: `lisp-import/` (Gyro importing Lisp, three tiers),
  `lisp-core.lisp` (the language alone), `any-ops.metals` (all six
  instructions). The harness now takes `.metals` and `.lisp` roots and
  honours `//gyro:skip interp`, which it had parsed and ignored.

### Open after it

- [ ] **Gyro's `any` still lowers to a closed enum**, not to METALS
      `any`. Two mechanisms for one idea. Switching would delete
      `internal/lower/any.go` and give the `any`, `any-structural`,
      `any-equality`, `variadics` and `fmt` corpus cases as coverage of
      the real instruction — but `any` is closed whole-program today
      (`sema/pattern.go` can say "an any never holds i32"), and an open
      one cannot be, so that diagnostic would go. Decide before the two
      drift.
- [ ] **The Lisp has no pairs, lists, symbols or `quote`.** The typed
      core was deliberate, but those are what makes a Lisp a Lisp, and
      they are what `any` exists for: a pair is a record of two `any`
      reached through `any.get`/`any.set`, which is now implemented
      everywhere. This is the natural next piece.
- [ ] **No tail calls.** `loop-add` recurses per element and will
      overflow where a Scheme must not. METALS has `loop`, so a call in
      tail position to the enclosing function should become a jump.
- [ ] **Recursive internal defines.** `(define (go n) (: f ...) (define
      (f v) (f v)) ...)` cannot see itself: the name binds after the
      body is lifted. Needs a letrec-shaped binding.
- [ ] **No module-level values in the Lisp.** Only functions, because a
      module-level initializer has to run before `main` and METALS
      leaves the ordering to the front end. Gyro already solved this for
      itself (`emitGlobalInit`); the Lisp should do the same.
- [ ] **The linker does not match record shapes structurally.** An
      importer that needs a foreign record declares its own and nothing
      checks the two agree. Signatures are compared; shapes are not.
- [ ] **`any.call` handles neither an async callee nor a void result.**
      Both fail at code generation with a clear message; neither is
      reachable from any producer yet.
- [ ] **An exported Lisp name has to be spellable in Gyro**, so
      `scale-by` cannot cross while `even-p` can stay private. Either
      mangle at the boundary and carry the mapping, or keep the rule and
      say so in the docs.

Open items as of 2026-08-24, after the cgen / Map / fs / time / os /
effects-v1 workstream (history through `585f5ce`). The language questions
that stood open here are now decided — they are recorded below with the
work each one implies, and each still needs its `docs/decisions.md` entry.

Three more C libraries were ported the same day — cJSON, md4c and a
bitboard perft — and most of what is newly queued below came out of them.
Two compiler bugs (a METALS float constant with a signed exponent did not
reparse; a literal argument to a generic parameter defaulted to i32 in the
interpreter alone) are fixed. The rest are gaps the ports had to write
around, and they are listed with the port that found them.

## Decisions settled 2026-08-24

Decided in one pass, and all of them implemented and recorded in
`docs/decisions.md` except the variadics design, which is queued below.

- **`f / 0.0` is IEEE.** *(done)* The interpreter stopped panicking on
  float division by zero and joined the other three tiers: `f / 0.0` is
  ±Inf, `0.0 / 0.0` is NaN. Integer `/ 0` and `% 0` keep panicking — that
  one is genuinely undefined, IEEE division is not. Corpus:
  `tests/testdata/run/float-ieee.gyro`.
- **NaN is IEEE for the operators, total-ordered for `compare`.** *(done)*
  `<`, `<=`, `>`, `>=` are all false when either operand is NaN, in every
  tier — the METALS evaluator turned out to have the interpreter's bug
  too, folding "unordered" into "equal". `compare()` is now a *total*
  order so a comparator stays consistent: NaN sorts above every number,
  NaN vs NaN is 0, `-0.0` and `0.0` compare equal. The float NaN tests are
  synthesized in `ensureCompareFn`, so only floats pay for them.
- **Effects are inferred across module boundaries.** *(done)* This reverses the
  recorded rejection of cross-module inference and cancels the planned
  "exported functions must state their row" sweep: rows stay optional
  everywhere, `main` needs no exemption, and a call to a rowless Gyro
  function contributes what its body does no matter which module it lives
  in. Gyro compiles whole-program and the loader holds every source, so
  the precision is free. The cost accepted: a dependency's private body is
  part of what its callers can promise, and a library that wants a stable
  effect interface has to write the row.
- **A rowless intrinsic contributes every effect.** *(done)* A declaration
  with no body has nothing to infer from, so absent a row it is assumed to
  do `io` and `alloc`. The runtime boundary is the one place a row is the
  only possible source of truth. Every intrinsic in
  `internal/stdlib/src/` now states a row — `Array::get/set/len/pop/free`
  and the `Mutex`/`MutexGuard` surface are `!{}`, `spawn` is
  `!{ io, alloc }`, `convert`'s parsers are `!{ alloc }`, and `os::env`
  gained the `alloc` it always did (it returns owned text). Falling out of
  it: the `string` predicates (`starts_with`, `index_of`, `contains`, …)
  declare `!{ alloc }`, because `bytes()` copies.
- **Variadics: heterogeneous packs.** Gyro gets a variadic form, and the
  useful one is the `printf` shape — `f(1, "x", true)` — not a
  homogeneous `...T`. That needs a dynamic value with a runtime type tag
  in all four tiers, which is the largest single item on this list and
  wants its own design pass before any code: the spelling, whether the
  pack is a first-class value, how it interacts with `dyn` in METALS
  (which already exists as the escape hatch for dynamic languages), how
  printing derives over it, and what a callee may do with an element
  besides print it. Design first, then a decisions.md entry, then the four
  tiers.
- **`Map<K, V?>` stays legal.** *(done)* `get()` answers null both for a
  miss and for a stored null; `contains(k)` is the miss signal and already
  existed. It was not free after all: the type layer built a nested
  optional for a `T?` result instantiated with an optional `T`, which
  METALS rightly refused to verify, so `types.NewOptional` now collapses
  the nested case. Corpus: `tests/testdata/run/map-optional-values.gyro`.
- **The `select` arm chosen among ready arms is unspecified.** *(done —
  documented; the runtime item is queued below.)* The
  evaluator's randomization and the native runtime's in-order poll are
  both conforming; a program that depends on the choice is wrong. This
  makes the recorded divergence a language property instead of a bug, and
  leaves starvation as a runtime quality item (below).
- **Effects v1 edges**, decided together with the above and implemented:
  - Unknown effect names are rejected. `!{ foo }` is an error with a new
    diagnostic code; the vocabulary is closed at `io` and `alloc` plus
    `&name` borrow entries.
  - `runes()` and `bytes()` are `alloc`. Both copy into new storage, so
    the built-in method surface carries a small fixed effect table even
    though it has no declaration to hang a row on.
  - A closure's own declared row is checked against its body. Calling a
    closure still contributes nothing to the caller — that waits for
    effect polymorphism. Creating a capturing closure allocates an
    environment in the native tiers and is *not* an `alloc` effect in v1;
    noted as a known under-report.
  - Module-level `var` initializers run before `main` outside any row and
    are unconstrained in v1. Whatever `@comptime` decides for them is the
    natural place to revisit it.
  - A generic callee without a row is read through its instantiations:
    effects are unioned over every recorded instance, so a lenient generic
    body no longer contributes nothing.

## Work queued

- [x] **Variadics and `any`** — landed. `name: ...T` collects the rest and
      is a `T[]` inside the body; `sum(rest...)` forwards a pack whole;
      `...any` is the heterogeneous form. `any` holds one value and
      remembers its type, entered and left with `as`, tested with a match
      arm that may name a type no path can spell. It lowers to a
      synthesized closed enum over every wrapped type in the program — the
      way `error` already lowers — so no IR instruction, runtime tag or
      backend work was needed. `internal/stdlib/src/fmt.gyro` is a printf
      written in ordinary Gyro over such a pack. Corpus:
      `variadics`, `any`, `any-structural`, `any-equality`, `fmt`.
- [ ] **Borrow provenance does not look inside an aggregate result.**
      `viewType` recognizes a slice, a string and an optional of those, so
      a view returned *inside* something is not tracked: a struct with a
      slice field, a tuple carrying one, and now an `any` wrapping one all
      leave a function without a row and without a complaint.
      `fn f(xs: i32[]): any { return xs as any }` is accepted, and the
      caller can outlive the storage. This predates `any` — the struct
      case behaves identically today — and the fix is one piece of work
      for all three: teach provenance to look through aggregate results,
      which also means `!{ &xs }` becomes legal (and required) on them.
- [ ] **Use-after-free and uninitialized-read detection in the C
      runtime** (`internal/crt/src/rt.c`). This also brings back the
      double-free message: `gy_free` hands the block to libc, which
      overwrites the poison, so both tiers now say the weaker `free of
      memory that is not allocated`. The evaluator poisons freed and
      unwritten storage and aborts; the C runtime frees for real and
      leaves memory as the allocator returned it. Closing
      this removes the last three corpus skips
      (`panic-use-after-free`, `panic-uninitialized-{field,local}`),
      which currently apply to both native tiers. Sketch: `gy_free`
      poisons the payload behind the header magic instead of releasing,
      and `new` returns poison-filled cells that `load` checks — needs a
      cheap check that does not wreck the benchmark story.
- [x] **A register allocator for nasm** — landed.
      `internal/nasm/regalloc.go` colours an interference graph over the
      five callee-saved registers, which is what keeps it one decision
      per METALS register instead of one per program point: a value in
      `rbx` or `r12`..`r15` survives every call, so nothing spills
      around one. Liveness is a real backward dataflow with a fixpoint
      per `loop` — a linear interval is unsafe, since a value written
      before a loop and read inside it is live across the back edge.
      Three things landed with it, because the allocator is what exposed
      them: small integer constants ride as immediates (propagated
      through `copy` and through a value-preserving `conv`, without
      which nothing folds, since the lowering writes both around every
      literal); aggregate moves up to eight words are written out
      instead of `rep movsq`, which costs tens of cycles before it moves
      a byte; and an operation whose destination has a home computes in
      it rather than through `rax`. Recorded in `docs/decisions.md` and
      `docs/native.md`. Best of 5:

      | case            |    nasm |    cgen | was       | now       |
      | --------------- | ------- | ------- | --------- | --------- |
      | `regex`         | 478.9ms |  99.0ms |    13.64x |     4.84x |
      | `sha256`        | 369.1ms | 177.2ms |     2.74x |     2.08x |
      | `jsmn`          | 203.9ms | 112.3ms |     4.57x |     1.82x |
      | `arith-checked` |  32.0ms |  19.3ms |     5.70x |     1.66x |
      | `struct-array`  |  89.1ms |  78.9ms |     2.19x |     1.13x |
      | `call-fib`      |  28.0ms |  25.5ms |     1.61x |     1.10x |
      | `string-concat` | 153.8ms | 140.4ms |     1.05x |     1.10x |
      | `loop-index`    |  99.5ms | 101.1ms |     1.06x |     0.98x |

      What is left is not register pressure. Instrumenting the `regex`
      port, exactly one of its twelve functions wants more registers
      than the five supply; the rest get everything they ask for. So
      extending the allocator to the caller-saved registers — which
      would need a live-across-call analysis and spill code at each call
      site — is not where the remaining gap is. It is in the two items
      below, both of which the allocator work uncovered, plus inlining:
      `regex` calls `byte_at` once per input byte and `cc -O1` inlines
      it where nasm cannot.

      Three ports later that reading holds, and inlining is now the
      whole of it. `md4c` at 3.42x and `perft` at 3.56x join `regex` at
      4.86x, and all three are call-dominated — md4c is ~90 functions
      calling `Ctx::ch()` once per byte examined, perft recurses per
      node with no hot leaf at all. Everything in the suite that is not
      call-dominated sits between 1.00x and 2.10x. So **inlining is the
      next codegen item**, ahead of both lowering fixes below.
- [ ] **The lowering loads a whole aggregate to index one element.**
      `self.data[j]`, where `data` is a `u8[64]` field behind a `*Ctx`,
      lowers to `field.ptr` → `load` → `index.get`: the `load` copies
      all 512 bytes for every element read. In `sha256::Ctx::transform`
      that is a `rep movsq` of 64 words per byte of the message
      schedule. It should compute an element address through the
      pointer instead. This is a lowering fix, so it pays in both native
      tiers at once, and it is most of why `cgen/cc` is 13x on `sha256`
      and 9x on `jsmn`.
- [ ] **The lowering emits `%x = copy %x`.** After `index.set %m, %i,
      %v` it writes `%m = copy %m`, a self-copy that meant a 512-byte
      `rep movsq` doing nothing. The assembler now drops a copy whose
      source and destination are the same location, but the lowering
      should not emit it: the evaluator and cgen pay for it too.
- [ ] **What the ports cost against C, and why they differ.** The six
      ported cases spread from 1.4x to 13.2x on cgen, which is the
      useful number now that the column exists:

      * `regex` **1.36x** — near parity. The matcher does real work per
        byte it reads (a recursive call, a class scan), so the fixed
        cost of an element access is amortized.
      * `perft` **4.43x** — a magic multiply, a recursive call and an
        18-word position copy per move, so the table access amortizes
        better than jsmn's even though the slider tables are enormous.
      * `md4c` **8.84x** — a byte read and a dispatch per input byte,
        with the mark and block stacks touched by index.
      * `jsmn` **9.25x** — one read and a switch per input byte, plus a
        token table touched by index. Almost nothing happens between
        accesses.
      * `cjson` **11.13x** — one heap node per JSON value, pointer-chased
        twice (parse builds the tree, print walks it), so it pays
        allocation on top of the access cost.
      * `sha256` **13.19x** — the worst. `u8[64]`, `u32[8]` and `u32[64]`
        walked element by element, where C indexes packed arrays it has
        already hoisted into registers.

      The common factor is the element access: a `gy_array_get` /
      `gy_array_set` call plus a bounds check where C does a `mov`, and
      one qword per scalar where C packs bytes. Two things to try, in
      order: inline the access for a statically known element type
      instead of calling the runtime, and hoist the bounds check out of
      a loop whose index is provably in range. Both are cgen-side and
      both would show here first.
- [ ] **Port small C libraries and run their own test suites.** Real
      programs written by someone who had never heard of Gyro find gaps
      that hand-written corpus cases do not. Candidates cloned to
      `../c-ports/` (outside this repo): `tiny-regex-c`,
      `crypto-algorithms`, `jsmn`, `tinyexpr`, `inih`, `heatshrink`.

      Done: **tiny-regex-c** → `tests/testdata/run/regex/`. All 76 cases
      of its `tests/test1.c` produce byte-identical output to the C
      original on all four tiers; `../c-ports/tiny-regex-c/gyro/`
      keeps `reference.c`, the C driver printing the same lines, for
      re-diffing. It found one real bug (a tuple literal defaulted its
      elements instead of adapting them, so `(-1, 0)` came out
      `(i32, i64)` — fixed) and needed three workarounds worth deciding
      on: no unions (a tagged union became a struct carrying both an
      offset and a character), no pointer arithmetic (every walk became
      an index plus a `byte_at` that answers 0 past the end), and
      in/out parameters (C's `int* matchlength` became a threaded
      return value). Variadics — the other thing every port asks for —
      are now implemented, and `fmt::printf` covers what those tests
      print; unions, pointer arithmetic and in/out parameters are still
      open.

      Done: **crypto-algorithms** (MD5 and SHA-256) →
      `tests/testdata/run/crypto/`. 152 digest lines byte-identical to
      the C original on all four tiers;
      `../c-ports/crypto-algorithms/gyro/reference.c` is the C driver
      over the upstream `md5.c` and `sha256.c` that prints them. The
      table is the two upstream test files' vectors plus a sweep of
      every message length from 0 to 70 bytes, which brackets both
      padding branches; the upstream megabyte vector is a tenth of a
      percent of its length here, so the tree-walking tier can run it.

      It found one real bug: an untyped operand *inside* an expression
      that took a type from context kept `UntypedInt` and lowered at the
      i32 default, so `mask & (0xffffffff >> n)` reached METALS as
      `shr i32, u32` and `~0` as `band u32, i32`. Fixed, and fixing it
      closed the recorded checker/interpreter divergence on untyped
      operands — `var x: i64 = 1 << 40` is now an i64 shift in the
      interpreter too. Corpus:
      `tests/testdata/run/untyped-operands.gyro`.

      It also forced the open shift-overflow question, which is now
      **decided: a shift never panics.** `<<` drops the bits that leave
      the width, as `>>` always has, so the interpreter joined the other
      three tiers and both ports write their rotates the way C does.
      Recorded in `docs/decisions.md`; corpus `shift-edges.gyro` grew
      the overflow cases.

      Three things the port still had to work around:

      * **A fixed-size array has no zero value.** `u8[64]` needs a
        64-element literal, and a local a loop fills is rejected by
        definite assignment, so SHA-256's message schedule is zeroed
        before it is written — dead work in the hot loop, and three
        constant blocks of literal zeros in the source.
      * **No wrapping compound assignment.** `ctx.n +%= 1` does not
        parse, so every accumulate is spelled out. The operator table
        has `+=` but nothing for `+% -% *%`.
      * **`self` is not usable as an ordinary parameter name**, so a
        free `transform(ctx: *Ctx)` mirroring C's `sha256_transform`
        became a private method instead. Not a real cost, but it is a
        keyword the docs describe as bound to methods.

      Done: **jsmn** → `tests/testdata/run/jsmn/`. The default build —
      no `JSMN_STRICT`, no `JSMN_PARENT_LINKS`, what `make test_default`
      runs — with the token table of every case in `test/tests.c`
      printed instead of a PASSED/FAILED count: 200 lines
      byte-identical to the C original on all four tiers, including the
      prefix sweep of `test_partial_string`, the small-pool sweep of
      `test_array_nomem` and the count-only mode.
      `../c-ports/jsmn/gyro/reference.c` is the C driver printing them.

      It found no bug — the port was byte-identical on the first run —
      and needed three shapes worth noting, all of them already-known
      gaps rather than new ones: C's `tokens == NULL` count-only mode
      became an explicit `count_only` flag, `jsmntok_t*` walking a
      caller's array became index plus get/set (no interior pointers),
      and `jsmn_parse_primitive`'s `goto found` became a flag and a
      shared tail. The port is what `tests/bench/testdata/jsmn` measures
      against upstream `jsmn.h`.

      Done: **cJSON** (core `cJSON.c`, not `cJSON_Utils.c`) →
      `tests/testdata/run/cjson/`. 598 lines byte-identical to the C
      original on all four tiers, covering all ten
      `tests/inputs/testN.expected` documents plus the unity suites
      turned into printed tables;
      `../c-ports/cJSON/gyro/reference.c` is the C driver.

      This is the first port whose subject is *ownership* rather than
      arithmetic. C's union became `enum Value { Invalid, False, True,
      Null, Number(f64, i32), Text(string), Raw(string), List, Object }`
      while `next`/`prev`/`child`/`key` stayed ordinary fields — the
      split is on what is genuinely a union, since those four are walked
      by `Delete`, detach and replace without regard to the kind. One
      `alloc<Node>()`, `delete` freeing the subtree exactly where
      `cJSON_Delete` does, every failure path freeing its partial chain;
      the interpreter poisons freed cells, so the interp tier passing is
      a real check of that. Strings leak by design — `from_utf8`
      allocates and nothing reclaims text.

      It found one real bug: **METALS could not reparse a float constant
      with a signed exponent.** `const.f64 1e-09` printed fine and
      scanned back as `1e` followed by `-09`, so `gyro metals` output for
      any program containing `10e-10` failed to round-trip. Fixed in
      `internal/metals/parse.go`; regression `TestSignedExponentModule`.

      Two things it forced, both now in `docs/decisions.md` under "Still
      open":

      * **Nothing in Gyro can format a float to a precision.** `fmt` has
        no `%e`/`%f`/`%g`, and `as` converts a value rather than exposing
        a representation, so a double's bits are unreachable.
        `dtoa.gyro` (358 lines) therefore implements `%.Ng` exactly:
        recover `mantissa · 2^exp` losslessly, expand `m·2^e =
        m·5^-e·10^e` over a base-10⁹ bignum, round half-to-even on the
        exact remainder, then apply `%g`'s form choice. Validated
        byte-identical against glibc on 4000 random bit patterns at both
        `%.15g` and `%.17g`. The shortest-representation shortcut is not
        available, because this cJSON round-trips through
        `compare_double`'s 1-ulp tolerance rather than `!=`.
      * **The leak analysis neither caught anything nor complained.** It
        false-positives the linked-list idiom — an allocation stored into
        a field through a pointer (`p.child = n`) is not settled, though
        a struct-literal field is — and it is blind through one level of
        indirection, so `var n = new_node()` is untracked entirely.
        Routing links through a helper (a call argument settles) left the
        port checking with zero warnings while doing real manual memory
        management.

      Also: null narrowing follows `&&` but not `||`, hit five times; a
      match *expression* takes its type from its first arm, so every
      `string?` accessor became a statement `match` into a pre-declared
      local; and `cJSON_IsReference`/`StringIsConst` were left out rather
      than ported into a "do not free this" flag.

      Done: **md4c** (CommonMark, no extensions) →
      `tests/testdata/run/md4c/`. **652 of 652 spec examples
      byte-identical to upstream `md2html`, on the first run, on all four
      tiers** — no compiler bug found, nothing under `internal/`
      touched. 4109 lines from `md4c.c`'s 7284 with every
      `parser.flags` branch *absent* rather than switched off, plus
      `html.gyro`, a generated `entity.gyro` (2125 entities) and
      `unicode.gyro`. The golden is md2html's own output, not
      `spec.txt`'s: md4c differs from the spec byte-for-byte on 111 of
      the 652, and the port is of md4c.

      It answered the question `tinyexpr` was queued for. **Function
      values in struct fields work end to end**: `md::Parser` is five
      function-valued fields, `html::parser_for(*Render)` returns one
      whose closures all capture the same renderer, and md4c's `void*
      userdata` parameter disappears from every signature — a closure
      captures by copy and copying a `*T` copies the pointer, so five
      closures mutate one renderer, and `Array<T>` being a runtime handle
      means the output buffer needs no pointer either. Recorded in
      `docs/decisions.md`.

      The gap it exposed is new: **a slice cannot be narrowed.** There is
      no `xs[a..b]`, so md4c's text callback — `(const CHAR*, MD_SIZE)`
      pointing into the document, into a merged-line buffer, or at a
      literal — became `(kind, source: u8[], beg, end)` threaded through
      every signature, with real copies where md4c points without
      copying (link labels before lookup, single-line titles, the BOM
      trim). This is the slice-shaped twin of "no pointer arithmetic"
      and it is what a callback-driven library asks for first. Beside it:
      `bytes()` on a string literal allocates, which costs 5.4% of the
      renderer's hot region and leaks per emission.

      Everything else it worked around was already known — the mark and
      block/line/verbatimline unions became a side table and parallel
      arrays; `OFF* p_end` out-parameters became tuple elements and four
      named result structs; `Array<T>` has no truncate so `n_marks` and
      friends are logical lengths beside a growing array; `Array::get`
      returns a copy, which is genuinely error-prone where md4c holds
      `opener` and `closer` and mutates through both; no `goto`; and
      every offset is `i64` rather than unsigned, because Gyro panics on
      underflow. One upstream aliasing hack was deliberately dropped:
      `md_analyze_line` reinterprets the last eight bytes of the block
      buffer as an `MD_BLOCK`, which can misfire on an `MD_LINE` whose
      `beg` ends in `0x04`; an explicit `top_block_is_li()` is identical
      on all 652.

      Done: **bbc perft** (Code Monkey King's didactic bitboard engine,
      movegen and perft only) → `tests/testdata/run/perft/`. 275 lines
      byte-identical to the C original on all four tiers, printing a
      perft *divide* — nodes per root move — then the total, so a
      mismatch names the move that broke. Positions and depths sized for
      the tree-walker: start d3, kiwipete d2, CPW 3 d3, CPW 4/5/6 d2,
      plus castling, promotion, en-passant and pinned-piece FENs. Every
      total matches the published CPW numbers *and* the C binary; deeper
      runs cross-checked during development (start d4 197281, kiwipete
      d4 4085603, CPW 3 d4 43238). Upstream is GPL-3 and the repo is MIT,
      so the copy under `tests/bench/testdata/perft/` keeps its
      attribution intact and both drivers record that it is built only as
      a standalone benchmark binary, never linked into the compiler or
      runtime.

      It found one real bug: **a literal argument to a generic parameter
      was defaulted to i32 in the interpreter alone.** `Array<u64>::new()`
      then `push(7)` then `+%` panicked `mismatched operands` in the
      interpreter and was correct in the other three tiers, because
      `defaultedArgs` settled every native-call argument to the i32
      default and threw away the `T = u64` substitution sema had already
      made. Fixed in `internal/interp/{call.go,value.go,native.go}`;
      corpus `tests/testdata/run/generic-arg-literal.gyro`.

      It also found the sharpest footgun so far, which is not a bug but
      behaves like one: **a bare name in a match pattern binds, it does
      not compare.** `match piece { WQ: "q"  WR: "r" … }` matches the
      first arm for every piece, so every promotion printed `q`. It
      compiles clean and answers wrong. Now in `docs/decisions.md` under
      "Still open".

      The fixed-size-array gap dominated the port's cost. `u64[64][512]`
      and `[64][4096]` would need 32768 and 262144 literal zeros, and
      there are no multidimensional arrays, so all seven attack tables
      became flat `Array<u64>` indexed `square * stride + index` — a
      `gy_array_get` call and a bounds check where C does a `mov`, which
      is most of the 4.43x against C. The same gap pushed C's per-node
      `int moves[256]` into one shared `Array<i32>` with ply-owned
      windows. `copy_board`/`take_back` ported as whole-struct copies of
      the same 18 words the C macros memcpy, and the double save/restore
      C does was kept rather than optimized away, so the two sides
      measure the same work.

      One deliberate deviation: the table build enumerates occupancy
      subsets with the carry-rippler (`o = (o -% mask) & mask`) instead
      of upstream's `set_occupancy`, which costs a pass over the mask per
      variation and was 1.14s of a 2.17s init in the tree-walker.
      Identical tables; documented in the file header.

      Next: `heatshrink` if bit-level I/O is the useful stress, or
      `tinyexpr` for f64 arithmetic throughout — though the function
      table it was queued for is now answered by md4c, and its `%g`
      output would land on the same missing float formatting cJSON had to
      write from scratch.
- [ ] **`fmt` cannot format a float at all.** No `%e`, no `%f`, no `%g`,
      and no way to read a double's bits, so printing `3.14159` to three
      places is not expressible in Gyro today. The cJSON port needed it
      badly enough to write `tests/testdata/run/cjson/dtoa.gyro` — an
      exact `%.Ng`, bignum and all, validated byte-identical against
      glibc on 4000 random bit patterns. That file is the implementation
      the stdlib wants; the decision is whether it moves into
      `internal/stdlib/src/fmt.gyro` as ordinary Gyro or whether the
      conversion becomes an intrinsic the four tiers each provide.
      Ordinary Gyro is the default answer under the stdlib rule, and the
      port proves it is possible; the intrinsic is only worth it if the
      cost shows. Related and smaller: `to_f64` drops the value on a
      range error where `strtod` answers ±Inf.
- [ ] **A slice cannot be narrowed.** There is no `xs[a..b]`, so every C
      API shaped `(pointer, size)` forces a copy or a `(base, beg, end)`
      triple through every signature — md4c's text callback and its
      label/title handling both, with real copies where md4c points into
      the document without copying. This is distinct from the recorded
      pointer-arithmetic item: a slice already carries a length, so
      narrowing one is a bounds computation, not aliasing. It is the
      first thing a callback-driven library asks for.
- [ ] **Leak analysis has two holes that cancel each other out.** An
      allocation stored into a field reached *through a pointer* (`p.child
      = n`) is not settled, although the same store in a struct literal
      is — so the linked-list idiom false-positives W0701. And tracking
      does not survive one level of indirection: `var n = new_node()` is
      not tracked at all, so writing a constructor, which is how anyone
      would write it, silences the analysis. cJSON does real manual
      memory management and checks with zero warnings. Either settling
      grows to cover a store through a pointer and tracking follows a
      function that returns an allocation, or the analysis is documented
      as direct-`alloc`-only and stops pretending otherwise.
- [ ] **Null narrowing does not follow `||`.** `if p != null && p.child
      != null` is accepted; `if p == null || p.child == null` is E0510 at
      `.child`. Same fact under negation, and the second is how a guard
      clause is written. Five occurrences in cJSON alone.
- [ ] **A `const` in a match pattern binds instead of comparing.**
      `match piece { WQ: "q"  WR: "r" … }` matches the first arm for
      every value: a bare name is a binding. It compiles clean and
      answers wrong, which is how the perft port printed `q` for every
      promotion. Either warn when a binding pattern shadows a
      module-scope `const`, or let a path pattern mean equality. This is
      the only gap so far that produced silently wrong output with no
      diagnostic anywhere.
- [ ] **A match *expression* takes its type from its first arm**, so
      `Value::Text(s): s` beside `_: null` is E0516 and every optional
      accessor becomes a statement `match` writing into a pre-declared
      local. Decide whether the arms should unify instead — the same
      question the `if` expression form will ask.
- [ ] **Inlining in nasm** — now the top codegen item, ahead of the two
      lowering fixes above. The register allocator closed the pressure
      problem; what is left in the three worst `nasm/cgen` cases is call
      overhead, and all three are call-dominated: `regex` 4.86x
      (`byte_at` per input byte), `perft` 3.56x (recursion, no hot leaf),
      `md4c` 3.42x (~90 functions, `Ctx::ch()` per byte examined).
      Everything not call-dominated sits between 1.00x and 2.10x.
- [ ] **The METALS evaluator is 2.5x slower than the tree-walking
      interpreter** on perft — 6.55s against 2.66s for the same corpus
      case. The evaluator is meant to be the faster of the two and is on
      every other case. Something about a call-heavy recursive workload
      is pathological there; worth profiling before it becomes the reason
      a corpus case has to shrink.
- [x] **The i32 default is range-checked** — landed. `var x =
      3000000000` and `println(1 << 40)` are errors now; defaulting is a
      type decision, so the value has to fit the type it decides.
      `defaultExpr` is the single place it happens, and every site that
      defaulted an expression's type goes through it (annotationless
      `var`, range endpoints, array and tuple elements, match arms, a
      closure's expression body, an argument wrapped into `any`, an
      argument inferring a generic parameter). Sites that default a bare
      type with no expression keep `types.Default`. Recorded in
      `docs/decisions.md`; fixtures in
      `internal/sema/testdata/check/literals.gyro`.
- [ ] **Sorting** — last stdlib gap blocking ordinary programs. Closures
      and generics exist, so `sort(xs, cmp)` is pure Gyro; decide
      comparator shape (`fn(T, T): i32` vs `compare` duck-typing). The
      total-order `compare` decided above is what makes the duck-typed
      form safe on floats.
- [ ] **A fair `select` in the native runtime.** The choice among ready
      arms is unspecified, so in-order polling is legal — but it can
      starve an arm behind a busy one. The fix is an arm-descriptor table
      the runtime chooses from, instead of the straight-line polls both
      backends emit today; that also gives the runtime somewhere to
      randomize.
- [x] **Benchmark suite** `tests/bench/` — landed. Twelve programs, one
      codegen decision each, both backends from the same lowered module,
      `make bench` (`BENCH=` filters, `REPEAT=` sets the run count).
      Each program times its own hot region, so process start and setup
      are out of the number, and the two binaries must agree on a
      checksum.

## Still undecided

Language questions nobody has answered yet, kept short:

- **Unions**, **pointer arithmetic** and **in/out parameters** — the
  three workarounds the tiny-regex-c port needed, and every port since
  has needed all three again. cJSON is the datapoint on unions: the
  genuinely-union part became an enum with payloads and the rest stayed
  fields, which reads better than C and costs nothing, so the case for
  unions is weaker than it was. In/out parameters are the opposite —
  md4c needed four named result structs purely to return what C writes
  through `OFF*`.
- **Zero values for fixed-size arrays** — `u8[64]` can only be written
  as 64 literal zeros, and definite assignment rejects a local that a
  loop fills, so a buffer costs both source noise and a zeroing pass
  nothing needs. perft is the case that makes this expensive rather than
  annoying: `u64[64][512]` and `[64][4096]` are unwritable, there are no
  multidimensional arrays either, so all seven attack tables became flat
  `Array<u64>` with hand-computed `square * stride + index` — and that
  runtime access is most of the port's 4.43x against C.
- **Truncating an `Array<T>`** — there is no way to shorten one, so
  jsmn, md4c and perft all carry a logical length beside a growing array
  and push-or-set into it. Three ports is enough of a pattern to decide
  whether `Array` grows a `truncate`/`set_len`.
- **Compound assignment for the wrapping operators** — `+=` exists,
  `+%=` does not parse, so every wrapping accumulate is spelled out
  (`ctx.n = ctx.n +% 1`). Either add `+%= -%= *%=` or decide the
  asymmetry is deliberate and say so in `language.md`.

## Housekeeping

- [x] `//gyro:stdin` / `//gyro:exit` directives exist in the harness —
      CLAUDE.md's directive list now mentions them, along with the
      `.metals`/`.lisp` roots and the `;gyro:` spelling.
- [ ] `docs/agent-context.md` still describes three tiers in places
      (§2 pipeline diagram) — cgen updated some sections; sweep the
      rest.
- [ ] Decide whether `gyro build` defaults to the C backend eventually
      (nasm as opt-in) — flagged when cgen landed, undecided.
