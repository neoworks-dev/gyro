# Done

What landed, newest first. Open work is tracked as issues on `neoworks-dev/gyro`; an
entry here that left something behind says where it went. The reasoning
that outlived the work is in `docs/decisions.md` — this is the record of
when, not the record of why.

Entries below are kept as they were written. Several describe things that
have since been deleted; where that is so, a later entry says it, and
`docs/decisions.md` carries the reversal.

## Four packages are gone — landed 2026-08-26

`internal/meval`, `internal/cgen`, `internal/lisp` and `internal/wasm`
are deleted, and `internal/clibc` with the bridge it existed for. What is
left is one consumer of METALS — `internal/nasm` over `internal/crt` —
and two producers, `internal/lower` and `internal/cc`.

**The three-tier cross-check is gone, and that is the cost.** Three tiers
held to one hand-written golden caught a wrong answer that looked
plausible; one tier does not. What survives is the part that was always
doing the work: the golden is written before the program runs, and there
is still no flag that regenerates it.

- **`gyro run` builds and executes.** It compiles into a temporary
  directory, runs the binary and forwards the exit code, so it is a
  spelling of `gyro build` rather than a second way to execute a program.
  `gyro lisp` and `gyro cgen` are gone; `gyro build --backend` is gone;
  `gyro cc file.c` now prints what the direct C front end makes of a file.
- **A `.c` is not a root.** The front end produces a library, and C's
  `int main` returns an exit code a METALS entry has nowhere to put —
  routing one through anyway returned 0 whatever `main` said, which is a
  wrong answer rather than a missing feature.
- **What stopped being detected**, recorded rather than dropped:
  use-after-free, read-of-uninitialized, type punning across union
  members with no common initial sequence, and a `ptr.offset`/`ptr.cast`
  that has left its object. The evaluator caught all four; nothing does
  now. The corpus cases that only ever passed there — `panic-use-after-
  free`, `panic-uninitialized-{local,field}`, `panic-union-punning`,
  `union-punning.metals` — were deleted rather than kept as programs
  nothing runs. `//gyro:skip` loses its tier argument.
- **Linear memory stays, with no producer.** `memory`, `trunc` and
  `bitcast` are what the bridge left in the format, and a flat
  `u32`-addressed region is what a producer with C's memory model needs.
  `linear-memory.metals` and `panic-linear-memory-bounds.metals` are what
  exercise them now.
- **`internal/cc`'s tests build and run** instead of evaluating, which is
  what took that package from cached-instant to ~29s in `go test ./...`.
  The `.lisp` and bridge `.c` corpus roots went with their producers.

## C compiles into Gyro — landed 2026-08-26

`import { parse } from "./cJSON.c"` reads the C itself. `internal/cc` is
the fourth METALS producer and the one the wasm bridge was
reconnaissance for: the bridge proved METALS can receive a C-shaped
program, but by the time a `struct Point *` reaches wasm it is a `u32`,
so it can be run and never imported as what it was. The front end keeps
the types.

**Which route a `.c` takes is decided by where it appears.** A `.c` that
is the whole program goes through the bridge, sandboxed, over linear
memory; a `.c` a Gyro program imports goes through the front end, into
Gyro's own heap. The same file through both must compute the same thing,
and the corpus is where that is held.

- **A preprocessor of its own** — `#include`, `#define`, `#if`, using
  Prosser's algorithm, each token carrying the set of macros it came out
  of. It could not be borrowed: a header is how C says what a library's
  types are, and without `#include` an imported `.c` could only ever be a
  file written for this compiler.
- **C's types become METALS' own.** A struct is a record, `T *` is
  `opt<ptr<T>>` (METALS' pointer is never null and C's is null half the
  time), a tag nothing defines is `opaque`, `void *` is an erased pointer
  reached through `ptr.erase`/`ptr.cast`, a function pointer is
  `code(...)`, a union is a union, and a bit-field run packs into one
  member of bytes with `record align N` carrying what the declared types
  ask of the struct.
- **`goto`, without giving METALS a jump.** C's labels nest because C's
  statements do: a label jumped to from earlier gets a `block` around
  what precedes it, one jumped to from later gets a `loop` around what
  follows, one that is both gets both. Two shapes are left over and they
  are the relooper's — see #42.
- **A program is as many files as it takes.** C has no way to say which
  `.c` defines a symbol, so the set is found rather than declared:
  `resolve.go` starts at the file Gyro imported and follows what it
  needs. They lower into one module, because a linkage unit is what a
  METALS module is.
- **Types cross, not just calls.** An importer names a type instead of
  redeclaring it (`metals.Import.Types`), the linker matches and resolves
  them, and there is one `%Point` in the linked program. `headerGen` in
  `internal/loader/foreign.go` skips an export it cannot spell rather
  than failing the module — naming a skipped one is an error at the
  import.
- Corpus: `c-import`, `c-library`, `c-multifile`, `c-opaque`, `c-goto`,
  `c-switch`, `c-varargs`, `c-varargs-overrun`, `c-bitfield`,
  `c-bitfield-bytes`, `c-union`, `c-union-type`, `c-voidptr`,
  `c-voidptr-cross`, `c-callbacks`, `c-callback-cross`, `c-struct-cross`,
  `c-layout`, `c-strings`.

Left open: the two `goto` shapes, type punning across union members that
share no initial sequence, a variadic or a C `enum` crossing into Gyro,
Duff's device, structural matching across a *boundary*, and a local
aggregate initializer that writes zeros member by member.
`docs/agent-context.md` §"Open in the C front end" is the maintained
list.

## Unions, narrowing, and places everywhere — landed 2026-08-26

Four things that the ports had been asking for since tiny-regex-c, done
together because they turned out to be one subject: **what is a place**.

- **`union Name { ... }`** is a Gyro declaration. One storage with
  several names for it, C's own layout, and nothing recording which
  member is live — which is what decides the rest: no `==`, no `hash`,
  no `any`, printing prints the word `union`, and a literal names
  exactly one member. Members are bytes, so a `string`, a slice, an
  `Array<T>`, an enum, a nullable, a channel and a `fn` are each refused
  with their own reason; a `*T` is a member, because C's tagged unions
  are built on that. METALS has had the shape since the C front end
  needed it, so this is the surface plus the type layer.

  **A C `union` now crosses into Gyro as a type**, which is the thing it
  was for: `declarable` refused any type that was not a record, so a
  library handing one out could be called but never named. A fixed-size
  array member crosses too now — `struct { char name[16]; }` was
  unspellable for the same reason.

  `types.Union` is its own type rather than a flag on `types.Struct`, and
  the payoff is that every switch which does not mention it refuses by
  *not having a case*. That is the safe direction.

- **`xs[lo..hi]` narrows a slice.** Half-open, bounds-checked, free — the
  same elements through a shorter view. Either end may be left off;
  `xs[..]` is refused. A fixed-size array narrows to a `T[]`; a `string`
  does not narrow at all, because its length is in bytes and its content
  is UTF-8. This closes what md4c named "the first thing a
  callback-driven library asks for", and it is the *checked* answer to
  what the ports called pointer arithmetic — a slice knows its own
  extent, so a window is a bounds computation rather than aliasing.

  METALS grew `slice.sub`, and `slice.of` grew a second operand form:
  the address of an array as well as an array, since narrowing an
  addressable one through the value form would have viewed a copy.

- **`@extern` carries addresses.** `*T`, `anyptr` and `code(...)` cross,
  so out-parameters and callbacks work through a linked `.so` —
  `qsort`, `SDL_PollEvent`, `frexp`. The rule that moved is which
  question a boundary asks: a `js:` or `py:` runtime has a heap of its
  own and an address means nothing there, while a linked library runs on
  this thread over this allocator and there is no second heap to copy
  into. A channel and a future stay refused at both, for a different
  reason — C has no representation for one at any width.

  `internal/crt/shim.go` grew a real C declarator emitter for it:
  `int (*)[4]` and `int (*)(const void *, const void *)` are not a base
  type with a `*` glued on.

- **An index is a place, on every container.** `xs[i]` works on an
  `Array<T>` now, and on all three containers `xs[i] = v` and `&xs[i]`
  work. It lowers through a new `Array<T>::at(self, index): *T`, so an
  element is reached where it lies — `xs.get(0).x = 9` mutates a copy
  and nothing else, which is what md4c called error-prone. `get` stays:
  a copy is often what you want, and it is the only one of the two safe
  to hold across a `push`. `index.ptr` grew a slice operand to go with
  it. Also cheaper: one call and one copy fewer than `gy_array_get`.

  `&arr[i]` on a fixed-size array was a missing case in
  `placeOfAddressable` — `index.ptr` already computed the address.

Corpus: `union-tagged`, `panic-union-punning`, `c-union-type/`,
`slice-narrowing`, `slice-sub.metals`, `panic-slice-sub-out-of-range`,
`panic-narrowing-out-of-range`, `panic-narrowing-reversed`,
`element-address`, `panic-element-address-out-of-range`,
`array-indexing`, `panic-array-index-out-of-range`, `extern-pointers`.
Fixtures: `check/union.gyro`, `check/narrowing.gyro`, and the `extern`,
`anyptr` and `code-pointers` fixtures revised.

## Storage is C's layout — landed 2026-08-25

A value in memory is now what `sizeof` would say in C: natural widths,
natural alignment, padding where C pads. A METALS register still carries
a scalar widened to a qword and an `f32` as a double, so a load widens
and a store narrows; `docs/appendix-e-native-backend.md` has the table
and the conversions, and `docs/decisions.md` has why there is one layout
rather than two.

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
  both gone as of the layout entry above.)
- **`crt.Shim`** — one generated C wrapper per symbol, compiled beside
  `rt.c`, applying C's real widths. Both backends call the wrapper, so
  neither knows an `int` from an `int64_t`.

Started narrow — no struct by value, no callback, no out-parameter, which
is why the demo pumped events instead of polling them. All three landed
since: C's layout outright brought structs by value, and callbacks and
out-parameters landed on 2026-08-26. The demo polls now.

**The cost, which is the reason this was a decision and not a patch:** a
program that calls C runs on the two native tiers only. The evaluator is
Go with no cgo, so it refuses. `tests/testdata/run/extern-c.gyro` carries
the skips with their reasons.

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
  instructions). The harness now takes `.metals` and `.lisp` roots.

## Decisions settled 2026-08-24

Decided in one pass, and all of them implemented and recorded in
`docs/decisions.md`.

- **`f / 0.0` is IEEE.** `f / 0.0` is ±Inf, `0.0 / 0.0` is NaN. Integer
  `/ 0` and `% 0` keep panicking — that one is genuinely undefined, IEEE
  division is not. Corpus: `tests/testdata/run/float-ieee.gyro`.
- **NaN is IEEE for the operators, total-ordered for `compare`.**
  `<`, `<=`, `>`, `>=` are all false when either operand is NaN, in every
  tier — the METALS evaluator turned out to have the interpreter's bug
  too, folding "unordered" into "equal". `compare()` is now a *total*
  order so a comparator stays consistent: NaN sorts above every number,
  NaN vs NaN is 0, `-0.0` and `0.0` compare equal. The float NaN tests are
  synthesized in `ensureCompareFn`, so only floats pay for them.
- **Effects are inferred across module boundaries.** This reverses the
  recorded rejection of cross-module inference and cancels the planned
  "exported functions must state their row" sweep: rows stay optional
  everywhere, `main` needs no exemption, and a call to a rowless Gyro
  function contributes what its body does no matter which module it lives
  in. Gyro compiles whole-program and the loader holds every source, so
  the precision is free. The cost accepted: a dependency's private body is
  part of what its callers can promise, and a library that wants a stable
  effect interface has to write the row.
- **A rowless intrinsic contributes every effect.** A declaration
  with no body has nothing to infer from, so absent a row it is assumed to
  do `io` and `alloc`. The runtime boundary is the one place a row is the
  only possible source of truth. Every intrinsic in
  `internal/stdlib/src/` now states a row — `Array::get/set/len/pop/free`
  and the `Mutex`/`MutexGuard` surface are `!{}`, `spawn` is
  `!{ io, alloc }`, `convert`'s parsers are `!{ alloc }`, and `os::env`
  gained the `alloc` it always did (it returns owned text). Falling out of
  it: the `string` predicates (`starts_with`, `index_of`, `contains`, …)
  declare `!{ alloc }`, because `bytes()` copies.
- **`Map<K, V?>` stays legal.** `get()` answers null both for a
  miss and for a stored null; `contains(k)` is the miss signal and already
  existed. It was not free after all: the type layer built a nested
  optional for a `T?` result instantiated with an optional `T`, which
  METALS rightly refused to verify, so `types.NewOptional` now collapses
  the nested case. Corpus: `tests/testdata/run/map-optional-values.gyro`.
- **The `select` arm chosen among ready arms is unspecified.** The
  evaluator's randomization and the native runtime's in-order poll are
  both conforming; a program that depends on the choice is wrong. This
  makes the recorded divergence a language property instead of a bug, and
  leaves starvation as a runtime quality item (#41).
- **A shift never panics.** `<<` drops the bits that leave the width, as
  `>>` always has. Corpus: `shift-edges.gyro`.
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

## Out of the 2026-08-24 queue

- **Variadics and `any`.** `name: ...T` collects the rest and
  is a `T[]` inside the body; `sum(rest...)` forwards a pack whole;
  `...any` is the heterogeneous form. `any` holds one value and
  remembers its type, entered and left with `as`, tested with a match
  arm that may name a type no path can spell. It lowers to a
  synthesized closed enum over every wrapped type in the program — the
  way `error` already lowers — so no IR instruction, runtime tag or
  backend work was needed. `internal/stdlib/src/fmt.gyro` is a printf
  written in ordinary Gyro over such a pack. Corpus: `variadics`, `any`,
  `any-structural`, `any-equality`, `fmt`.

- **A register allocator for nasm.**
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
  `docs/appendix-e-native-backend.md`. Best of 5:

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
  site — is not where the remaining gap is. It is inlining, which is now
  the top codegen item (#39).

- **The lowering loads a whole aggregate to index one element.**
  Landed, and it took both this and the self-copy below with one
  mechanism. `self.data[j]`, where `data` is a fixed-size array
  field behind a `*Ctx`, lowered to `field.ptr` → `load` →
  `index.get`, and the `load` copied every element to read one:
  `md5::transform` moved ~320 bytes per iteration to produce four.

  METALS grew `%q = index.ptr %p, %i` — `ptr<array<T, N>>` and a
  `usize` to the `ptr<T>` of element `i`, bounds-checked against the
  `N` in the type — and `indexPlace` reaches an addressable
  fixed-size array through it, so `load`/`store` touch the element
  where it lies. It is a separate instruction rather than a wider
  `index.get` because `elem.ptr` and `ptr.offset` are unchecked and
  `metals.Abort` carries only a static message, so a lowering-side
  check would have had to reword the panic the three tiers compare.
  Recorded in `docs/decisions.md` and `docs/appendix-d-metals.md`;
  corpus `index-ptr.metals`, `panic-index-ptr-out-of-range.metals`,
  `array-field-through-pointer.gyro`,
  `panic-array-field-out-of-range.gyro`. Best of 5:

  | case     | cgen was | cgen now | cgen/cc was | now       |
  | -------- | -------- | -------- | ----------- | --------- |
  | `sha256` |  131.9ms |   77.4ms |       9.62x |     5.66x |
  | `perft`  |   64.8ms |   54.8ms |       4.85x |     4.10x |
  | `md4c`   |  119.4ms |  115.0ms |       8.70x |     8.24x |
  | `jsmn`   |  107.3ms |  108.1ms |       8.84x |     8.86x |
  | `cjson`  |   64.1ms |   65.5ms |       9.99x |     9.99x |

  `jsmn` and `cjson` did not move, and that is the finding: their
  hot containers are not `T[N]` behind a pointer. jsmn indexes a
  token table and cjson pointer-chases heap nodes, so what they pay
  is the runtime element call, not the aggregate copy.

- **The lowering emits `%x = copy %x`.** Landed with the above.
  `index.set` mutates a register, so an array reached through a
  place was stored back afterwards, and when the register was
  already its own storage that was `%m = copy %m` — every element
  copied to change nothing. Storing through an element pointer needs
  no write-back at all, and `localPlace` drops a store of a register
  onto itself. No self-copy survives in any of the four ports.

- **The i32 default is range-checked.** `var x = 3000000000` and
  `println(1 << 40)` are errors now; defaulting is a type decision, so the
  value has to fit the type it decides. `defaultExpr` is the single place
  it happens, and every site that defaulted an expression's type goes
  through it (annotationless `var`, range endpoints, array and tuple
  elements, match arms, a closure's expression body, an argument wrapped
  into `any`, an argument inferring a generic parameter). Sites that
  default a bare type with no expression keep `types.Default`. Recorded in
  `docs/decisions.md`; fixtures in
  `internal/sema/testdata/check/literals.gyro`.

- **Benchmark suite** `tests/bench/`. Twelve programs, one
  codegen decision each, both backends from the same lowered module,
  `make bench` (`BENCH=` filters, `REPEAT=` sets the run count).
  Each program times its own hot region, so process start and setup
  are out of the number, and the two binaries must agree on a
  checksum.

- **`//gyro:stdin` / `//gyro:exit` directives** documented in `CLAUDE.md`,
  along with the `.metals`/`.lisp` roots and the `;gyro:` spelling.

- **`docs/agent-context.md` swept** for the tier count and the pipeline
  diagram after the interpreter was deleted.

## The C library ports

Real programs written by someone who had never heard of Gyro, run against
their own test suites. What each one found is listed here because the
finding is the point; the gaps still open are issues. Clones live
in `../c-ports/`, outside this repo.

**tiny-regex-c** → `tests/testdata/run/regex/`. All 76 cases of its
`tests/test1.c` produce byte-identical output to the C original;
`../c-ports/tiny-regex-c/gyro/reference.c` keeps the C driver printing
the same lines. It found one real bug — a tuple literal defaulted its
elements instead of adapting them, so `(-1, 0)` came out `(i32, i64)` —
and named the trio: no unions, no pointer arithmetic, no in/out
parameters. All three have landed since.

**crypto-algorithms** (MD5 and SHA-256) → `tests/testdata/run/crypto/`.
152 digest lines byte-identical to the C original. The table is the two
upstream test files' vectors plus a sweep of every message length from 0
to 70 bytes, which brackets both padding branches.

It found one real bug: an untyped operand *inside* an expression that
took a type from context kept `UntypedInt` and lowered at the i32
default, so `mask & (0xffffffff >> n)` reached METALS as `shr i32, u32`
and `~0` as `band u32, i32`. Fixed, and fixing it closed the recorded
checker/tier divergence on untyped operands. Corpus:
`tests/testdata/run/untyped-operands.gyro`. It also forced the shift
overflow question, decided above.

Three things it worked around: a fixed-size array has no zero value; no
wrapping compound assignment; `self` is not usable as an ordinary
parameter name. The first two are still open.

**jsmn** → `tests/testdata/run/jsmn/`. The default build — no
`JSMN_STRICT`, no `JSMN_PARENT_LINKS`, what `make test_default` runs —
with the token table of every case in `test/tests.c` printed instead of a
PASSED/FAILED count: 200 lines byte-identical to the C original,
including the prefix sweep of `test_partial_string`, the small-pool sweep
of `test_array_nomem` and the count-only mode.

It found no bug — byte-identical on the first run — and needed three
shapes, all already-known gaps: C's `tokens == NULL` count-only mode
became an explicit `count_only` flag, `jsmntok_t*` walking a caller's
array became index plus get/set, and `jsmn_parse_primitive`'s `goto
found` became a flag and a shared tail.

**cJSON** (core `cJSON.c`) → `tests/testdata/run/cjson/`. 598 lines
byte-identical to the C original, covering all ten
`tests/inputs/testN.expected` documents plus the unity suites turned into
printed tables.

This is the first port whose subject is *ownership* rather than
arithmetic. C's union became `enum Value { Invalid, False, True, Null,
Number(f64, i32), Text(string), Raw(string), List, Object }` while
`next`/`prev`/`child`/`key` stayed ordinary fields — the split is on what
is genuinely a union, since those four are walked by `Delete`, detach and
replace without regard to the kind. One `alloc<Node>()`, `delete` freeing
the subtree exactly where `cJSON_Delete` does, every failure path freeing
its partial chain.

It found one real bug: **METALS could not reparse a float constant with a
signed exponent.** `const.f64 1e-09` printed fine and scanned back as
`1e` followed by `-09`. Fixed in `internal/metals/parse.go`; regression
`TestSignedExponentModule`.

It also forced float formatting and exposed the two leak-analysis holes,
both still open, and wrote `dtoa.gyro` (358 lines) — an exact `%.Ng`,
bignum and all, validated byte-identical against glibc on 4000 random bit
patterns at both `%.15g` and `%.17g`.

**md4c** (CommonMark, no extensions) → `tests/testdata/run/md4c/`. **652
of 652 spec examples byte-identical to upstream `md2html`, on the first
run** — no compiler bug found, nothing under `internal/` touched. 4109
lines from `md4c.c`'s 7284 with every `parser.flags` branch *absent*
rather than switched off, plus `html.gyro`, a generated `entity.gyro`
(2125 entities) and `unicode.gyro`. The golden is md2html's own output,
not `spec.txt`'s: md4c differs from the spec byte-for-byte on 111 of the
652, and the port is of md4c.

It answered the question `tinyexpr` was queued for. **Function values in
struct fields work end to end**: `md::Parser` is five function-valued
fields, `html::parser_for(*Render)` returns one whose closures all
capture the same renderer, and md4c's `void* userdata` parameter
disappears from every signature. Recorded in `docs/decisions.md`.

The gap it exposed — a slice cannot be narrowed — has since landed as
`xs[a..b]`. One upstream aliasing hack was deliberately dropped:
`md_analyze_line` reinterprets the last eight bytes of the block buffer
as an `MD_BLOCK`, which can misfire on an `MD_LINE` whose `beg` ends in
`0x04`; an explicit `top_block_is_li()` is identical on all 652.

**bbc perft** (Code Monkey King's didactic bitboard engine, movegen and
perft only) → `tests/testdata/run/perft/`. 275 lines byte-identical to
the C original, printing a perft *divide* — nodes per root move — then
the total, so a mismatch names the move that broke. Every total matches
the published CPW numbers *and* the C binary. Upstream is GPL-3 and the
repo is MIT, so the copy under `tests/bench/testdata/perft/` keeps its
attribution intact and both drivers record that it is built only as a
standalone benchmark binary.

It found one real bug — a literal argument to a generic parameter was
defaulted to i32 in the interpreter alone; corpus
`tests/testdata/run/generic-arg-literal.gyro` — and the sharpest footgun
so far: **a bare name in a match pattern binds, it does not compare**, so
every promotion printed `q`. Still open.

The fixed-size-array gap dominated the port's cost: `u64[64][512]` and
`[64][4096]` would need 32768 and 262144 literal zeros, and there are no
multidimensional arrays, so all seven attack tables became flat
`Array<u64>` indexed `square * stride + index`. One deliberate deviation:
the table build enumerates occupancy subsets with the carry-rippler
(`o = (o -% mask) & mask`) instead of upstream's `set_occupancy`.
