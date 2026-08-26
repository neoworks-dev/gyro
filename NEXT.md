# Next

What is open. What landed is in `DONE.md`, newest first; what was decided
and why is in `docs/decisions.md`. An item here is either work with a
known shape or a question nobody has answered — the two are kept apart at
the bottom.

## Codegen and performance

- [ ] **Inlining in nasm** — the top codegen item. The register allocator
      closed the pressure problem; what is left in the three worst
      `nasm/cgen` cases is call overhead, and all three are
      call-dominated: `regex` 4.86x (`byte_at` per input byte), `perft`
      3.56x (recursion, no hot leaf), `md4c` 3.42x (~90 functions,
      `Ctx::ch()` per byte examined). Everything not call-dominated sits
      between 1.00x and 2.10x.
- [ ] **The runtime element access is what the ports pay against C.**
      After the `index.ptr` landing the spread on cgen is `perft` 4.10x,
      `sha256` 5.66x, `md4c` 8.24x, `jsmn` 8.86x, `cjson` 9.99x, with
      `regex` at 1.36x (measured before that change and not moved by it).
      The common factor in the slow ones is an element access that is not
      `T[N]` behind a pointer: a `gy_array_get`/`gy_array_set` call plus a
      bounds check where C does a `mov`. Two things to try, in order:
      inline the access for a statically known element type instead of
      calling the runtime, and hoist the bounds check out of a loop whose
      index is provably in range. Both are cgen-side and both would show
      in `tests/bench/` first.
- [ ] **The METALS evaluator is pathological on call-heavy recursion.**
      On the `perft` corpus case it ran 6.55s against the deleted
      tree-walking interpreter's 2.66s, and it was faster on every other
      case. The comparison tier is gone, so the number cannot be
      reproduced — but the shape of it is worth profiling before it
      becomes the reason a corpus case has to shrink.
- [ ] **A fair `select` in the native runtime.** The choice among ready
      arms is unspecified, so in-order polling is legal — but it can
      starve an arm behind a busy one. The fix is an arm-descriptor table
      the runtime chooses from, instead of the straight-line polls both
      backends emit today; that also gives the runtime somewhere to
      randomize.

## The C front end

`internal/cc` landed (see `DONE.md`). `docs/agent-context.md` §"Open in
the C front end" is the maintained list; the load-bearing entries:

- [ ] **Two `goto` shapes need the relooper** — a jump *into* a block,
      and a backward jump reaching past a later forward label. Both are
      what LLVM does for the wasm bridge and what a direct front end has
      to write itself (Ramsey's ICFP 2022 algorithm is the reference).
      A label inside a switch body is refused for the same reason.
- [ ] **Type punning runs on two tiers.** Reading across two union
      members that share no initial sequence, and reading a struct's
      storage as bytes, are undefined in C too — but the natives
      reinterpret the bytes while the evaluator knows what is really
      there and says so. Closing it needs a byte-addressable object model
      in the evaluator. Carried as skips in `c-bitfield-bytes` and
      `union-punning.metals`.
- [ ] **A variadic function and a C `enum` cannot cross into Gyro.** A
      `...` says nothing about what a call passes, and Gyro's enum is a
      tagged sum. `headerGen` skips what it cannot spell, so one such
      helper does not make a library unimportable.
- [ ] **Structural matching across a *boundary*.** Within one runtime the
      linker compares an importer's declared types against the
      exporter's; across a boundary the value is copied and nothing
      checks that two same-named records agree.
- [ ] **A local aggregate initializer writes zeros member by member**, so
      `char buf[4096] = "hi"` is 4096 stores. The cheap fix is a bulk
      zero instruction, not a weaker evaluator.
- [ ] **A shared memory between wasm-bridge modules.** Linking gives each
      its own, which is right for a sandbox and wrong for two C
      translation units that expect one heap. The bridge never sees this
      because wasm-ld has already linked; only the bridge route is
      affected, since `internal/cc` uses `ptr<T>` and one heap.

## `clibc` and the sandboxed route

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

## Standard library

- [ ] **Sorting** — the last stdlib gap blocking ordinary programs.
      Closures and generics exist, so `sort(xs, cmp)` is pure Gyro;
      decide the comparator shape (`fn(T, T): i32` vs `compare`
      duck-typing). The total-order `compare` is what makes the duck-typed
      form safe on floats.
- [ ] **`fmt` cannot format a float at all.** No `%e`, no `%f`, no `%g`,
      and no way to read a double's bits, so printing `3.14159` to three
      places is not expressible in Gyro today. `tests/testdata/run/cjson/
      dtoa.gyro` is the implementation the stdlib wants; the decision is
      whether it moves into `internal/stdlib/src/fmt.gyro` as ordinary
      Gyro or whether the conversion becomes an intrinsic the three tiers
      each provide. Ordinary Gyro is the default answer under the stdlib
      rule, and the port proves it is possible; the intrinsic is only
      worth it if the cost shows. Related and smaller: `to_f64` drops the
      value on a range error where `strtod` answers ±Inf.
- [ ] **Memory reclamation.** Every `#{}`, `split` and `text()` allocates
      and nothing frees it — fine for a CLI that exits, fatal for a
      server. Gyro stays unmanaged, so the answer is arenas, a
      quarantining allocator, or stronger static analysis, not a
      collector.

## Safety analyses

- [ ] **Use-after-free and uninitialized-read detection in the C
      runtime** (`internal/crt/src/rt.c`). The evaluator poisons freed
      and unwritten storage and aborts; the C runtime frees for real and
      leaves memory as the allocator returned it, so **the tier you ship
      is the one that does not check**. It cannot even name a double
      free: `gy_free` writes its poison and then hands the block to libc,
      which reuses those bytes, so both native tiers say the weaker `free
      of memory that is not allocated`. Closing this removes the three
      corpus skips (`panic-use-after-free`,
      `panic-uninitialized-{field,local}`). Sketch: `gy_free` poisons the
      payload behind the header magic instead of releasing, and `new`
      returns poison-filled cells that `load` checks — needs a cheap
      check that does not wreck the benchmark story.
- [ ] **Leak analysis has two holes that cancel each other out.** An
      allocation stored into a field reached *through a pointer*
      (`p.child = n`) is not settled, although the same store in a struct
      literal is — so the linked-list idiom false-positives W0701. And
      tracking does not survive one level of indirection: `var n =
      new_node()` is not tracked at all, so writing a constructor, which
      is how anyone would write it, silences the analysis. cJSON does
      real manual memory management and checks with zero warnings. Either
      settling grows to cover a store through a pointer and tracking
      follows a function that returns an allocation, or the analysis is
      documented as direct-`alloc`-only and stops pretending otherwise.
- [ ] **Borrow provenance does not look inside an aggregate result.**
      `viewType` recognizes a slice, a string and an optional of those, so
      a view returned *inside* something is not tracked: a struct with a
      slice field, a tuple carrying one, and an `any` wrapping one all
      leave a function without a row and without a complaint.
      `fn f(xs: i32[]): any { return xs as any }` is accepted, and the
      caller can outlive the storage. The fix is one piece of work for all
      three: teach provenance to look through aggregate results, which
      also means `!{ &xs }` becomes legal (and required) on them.
- [ ] **An `Array<T>` element address is invalidated by `push`**, and
      nothing detects it. Same hazard the slice already has.
- [ ] **Mutation rules unchecked** — `*self` vs `self`, and assigning
      through a value receiver.

## Language gaps the ports found

- [ ] **A `const` in a match pattern binds instead of comparing.**
      `match piece { WQ: "q"  WR: "r" … }` matches the first arm for
      every value: a bare name is a binding. It compiles clean and
      answers wrong, which is how the perft port printed `q` for every
      promotion. Either warn when a binding pattern shadows a
      module-scope `const`, or let a path pattern mean equality. This is
      the only gap so far that produced silently wrong output with no
      diagnostic anywhere.
- [ ] **Null narrowing does not follow `||`.** `if p != null && p.child
      != null` is accepted; `if p == null || p.child == null` is E0510 at
      `.child`. Same fact under negation, and the second is how a guard
      clause is written. Five occurrences in cJSON alone.
- [ ] **A match *expression* takes its type from its first arm**, so
      `Value::Text(s): s` beside `_: null` is E0516 and every optional
      accessor becomes a statement `match` writing into a pre-declared
      local. Decide whether the arms should unify instead — the same
      question the `if` expression form will ask.
- [ ] **Port more small C libraries and run their own test suites.** Six
      done (see `DONE.md`); every one of them found something
      hand-written corpus cases did not. Remaining candidates cloned to
      `../c-ports/`: `tinyexpr`, `inih`, `heatshrink`. `heatshrink` if
      bit-level i/o is the useful stress, `tinyexpr` for f64 arithmetic
      throughout — though the function table it was queued for is now
      answered by md4c, and its `%g` output lands on the missing float
      formatting above.

## METALS and its other producers

- [ ] **Gyro's `any` still lowers to a closed enum**, not to METALS
      `any`. Two mechanisms for one idea. Switching would delete
      `internal/lower/any.go` and give the `any`, `any-structural`,
      `any-equality`, `variadics` and `fmt` corpus cases as coverage of
      the real instruction — but `any` is closed whole-program today
      (`sema/pattern.go` can say "an any never holds i32"), and an open
      one cannot be, so that diagnostic would go. Decide before the two
      drift.
- [ ] **The linker does not match record shapes structurally.** An
      importer that needs a foreign record declares its own and nothing
      checks the two agree. Signatures are compared; shapes are not.
      (Within one runtime a `.c` importer names types instead — the gap
      is the general case and the cross-boundary one above.)
- [ ] **`any.call` handles neither an async callee nor a void result.**
      Both fail at code generation with a clear message; neither is
      reachable from any producer yet.

### The Lisp

- [ ] **No pairs, lists, symbols or `quote`.** The typed core was
      deliberate, but those are what makes a Lisp a Lisp, and they are
      what `any` exists for: a pair is a record of two `any` reached
      through `any.get`/`any.set`, which is implemented everywhere. The
      natural next piece.
- [ ] **No tail calls.** `loop-add` recurses per element and will
      overflow where a Scheme must not. METALS has `loop`, so a call in
      tail position to the enclosing function should become a jump.
- [ ] **Recursive internal defines.** `(define (go n) (: f ...) (define
      (f v) (f v)) ...)` cannot see itself: the name binds after the body
      is lifted. Needs a letrec-shaped binding.
- [ ] **No module-level values.** Only functions, because a module-level
      initializer has to run before `main` and METALS leaves the ordering
      to the front end. Gyro already solved this for itself
      (`emitGlobalInit`); the Lisp should do the same.
- [ ] **An exported Lisp name has to be spellable in Gyro**, so
      `scale-by` cannot cross while `even-p` can stay private. Either
      mangle at the boundary and carry the mapping, or keep the rule and
      say so in the docs.

## Still undecided

Language questions nobody has answered yet, kept short:

- **Pointer arithmetic on `*T`** — the one of the tiny-regex-c trio not
  built. Slice narrowing covers most of what the ports wanted arithmetic
  for, since a slice knows its extent and a window is therefore checked.
  What is left uncovered is a cursor into a heap run that is not a slice.
  METALS has `elem.ptr`, `ptr.offset` and `ptr.diff` already and
  `internal/cc` uses them, so the question is whether Gyro's surface
  should have the unchecked operation at all.
- **Narrowing text.** `s[a..b]` on a `string` is refused; whether a text
  window counts bytes, runes or something else has not been answered.
- **Zero values for fixed-size arrays** — `u8[64]` can only be written
  as 64 literal zeros, and definite assignment rejects a local that a
  loop fills, so a buffer costs both source noise and a zeroing pass
  nothing needs. perft is the case that makes this expensive rather than
  annoying: `u64[64][512]` and `[64][4096]` are unwritable, there are no
  multidimensional arrays either, so all seven attack tables became flat
  `Array<u64>` with hand-computed `square * stride + index` — and that
  runtime access is most of the port's gap against C.
- **Truncating an `Array<T>`** — there is no way to shorten one, so
  jsmn, md4c and perft all carry a logical length beside a growing array
  and push-or-set into it. Three ports is enough of a pattern to decide
  whether `Array` grows a `truncate`/`set_len`.
- **Compound assignment for the wrapping operators** — `+=` exists,
  `+%=` does not parse, so every wrapping accumulate is spelled out
  (`ctx.n = ctx.n +% 1`). Either add `+%= -%= *%=` or decide the
  asymmetry is deliberate and say so in the book.
- **`self` as an ordinary parameter name** is refused, so a free
  `transform(ctx: *Ctx)` mirroring C's `sha256_transform` had to become a
  private method. Small, but it is a keyword the docs describe as bound
  to methods.

## Housekeeping

- [ ] `docs/agent-context.md` §"Blocking real applications" still says
      `@extern` has no callback and no out-parameter. Both landed
      2026-08-26 (`tests/testdata/run/extern-pointers.gyro`).
- [ ] `METALS_NEXT.md` §10 is measured against a tree that has moved:
      Gyro can import a `.c` now (`internal/loader/foreign.go` knows the
      extension), and a C program through `internal/cc` is as many files
      as it takes (`c-multifile`). What survives is the bridge route —
      two `.c` roots on one command line still link as two modules with
      one linear memory each — and the libc breadth list. Re-verify the
      section or fold what is left into this file.
- [ ] Decide whether `gyro build` defaults to the C backend eventually
      (nasm as opt-in) — flagged when cgen landed, still `--backend nasm`
      by default.
