# METALS_NEXT — many languages, one substrate

**Almost nothing in this document is implemented** — the exception is the
first two steps of §9, marked there. It records a design direction and
the reasoning behind it, so the next decision does not have to re-derive
the argument. `docs/metals.md` and `docs/decisions.md` remain the source
of truth for what METALS *is*; this is what it could become and what each
step would cost.

**One decision is now settled** (2026-08-25, recorded in
`docs/decisions.md` under "Gyro's memory model is C's, and stays C's"):
Gyro stays unmanaged. §5 is kept as the reasoning that decision rejected;
§9's sequence no longer starts with Gyro. The near-term direction is the
other half of this document: full C compatibility — any existing C
program importable directly — via a C layout, a wider FFI, and the
direct C front end of §6.

**And the first two steps of §9 are built** (2026-08-25). Storage is C's
everywhere rather than packed behind an opt-in decorator, and a struct
crosses `@extern` by value. What §9 called "packed `@repr(C)` beside the
one-qword rule" turned out to be the wrong shape — two layouts give
`ptr<i32>` two meanings — so there is one layout and no decorator; see
`docs/decisions.md`, "Storage is C's layout, and there is only one".

The question it answers: can C, C++, Zig, Rust, Go, Lua, Ruby, Python and
JavaScript all be direct METALS producers, interoperating without copies,
without changing what any of them mean?

---

## 1. The finding

**The dividing line is not "many languages". It is managed versus
unmanaged.**

Every conflict in the analysis reduces to one of two facts about a
producer: whether a collector must find its references, and whether its
objects may move. Languages that answer the same way share a heap for
free. Languages that answer differently cannot share one at any price.

- **Unmanaged** — C, C++, Zig, Rust. Raw addresses, static layouts, no
  collector. Mutually zero-copy *already*: a `#[repr(C)]` struct, an
  `extern struct` and a C struct are the same bytes. That is what a C ABI
  is.
- **Managed** — Go, Lua, Ruby, Python, JavaScript. Traced heaps, declared
  or discoverable shapes. Mutually zero-copy *if they agree on a
  representation*, which they will when we produce all of them. Gyro was
  a candidate to join and has since decided not to: it stays on the
  unmanaged side, with C.

METALS already has both models and already refuses to mix them. The
direction below is that decision extended, not replaced.

### Direct producer ≠ shared heap

Worth stating because it dissolves a large part of the perceived problem.
C is a direct producer today and stays on linear memory forever. "All
languages become direct producers" does not require one heap; it requires
one *IR*, with two memory models under it and a verified boundary
between.

### Conversion is the copy

Bytes are bytes and any representation can be converted to any other.
That is not a workaround for a limitation — "copy at the boundary" is the
name for exactly that conversion. Zero-copy therefore never comes from a
cleverer conversion; it comes from the two sides having agreed on the
bits beforehand. **Agreement is available whenever we own the front end,**
because tagged words, object headers and refcount fields are runtime
implementation choices, not language semantics. `1 + 2` in Python does
not depend on `PyObject`; `1 + 2` in OCaml does not depend on the low tag
bit.

C is the exception, and not for implementation reasons. Its standard
guarantees that objects have addresses, that `(uintptr_t)p` round-trips,
and that any object may be `memcpy`'d into untyped bytes. Those
guarantees *are* the language.

---

## 2. C and a traced heap

A precise collector must know which words are references. C's front end
knows the static types, but C permits — and the standard blesses — every
one of these:

```c
union { void *p; long n; } slot;          /* pointer or integer? runtime history decides */
long saved = (long)obj; obj = NULL;       /* the only reference now lives in an integer */
memcpy(buf, node, sizeof(Node));          /* pointers now inside a char[] */
n->link = (Node *)((uintptr_t)a ^ (uintptr_t)b);  /* neither pointer exists in memory */
p = (Node *)((uintptr_t)p | 1);           /* tagged pointer: the stored value is not an address */
int *q = &arr[5];                         /* the only reference is interior */
```

A compiler must be sound for every program the language permits, so the
front end has two honest options: reject these (a different language) or
guess (unsound). Hence:

**Precise tracing, a moving collector, unmodified C — pick two.**

### The resolution: C never holds a raw managed pointer

**Status: recommended.** C's world is `u32` offsets into a linear memory.
A `ptr<T>` is a host pointer that does not exist in that model, and no
instruction puts one there. So if C obtains managed references at all, it
obtains them as **handles: indices into a table we own.**

That table *is* the precise root set. Nothing is scanned, nothing is
guessed, and the collector may still move objects because it updates the
table while C's index is unchanged — the same reason JNI global refs and
.NET `GCHandle` work with compacting collectors.

The compatibility worry is vacuous: **no existing C source holds managed
references.** Real C points at its own `malloc`'d memory, which lives in
the linear region and is not collected. Only new glue code touches
managed objects, and that code is being written anyway. Nothing is
rewritten.

**Rejected: conservative tracing as the primary mechanism.** Boehm-style
scanning exists to retrofit a collector under C that already holds raw
pointers. That is not our situation, and the cost is global: ambiguous
roots pin objects, and pinning stops *everything* from moving, for every
language. Go and Lua would pay C's tax for nothing.

**Kept as an option for one narrow job:** collecting C's *own* heap, so
`malloc` without `free` stops leaking in a long-running process. A
conservative mark-sweep inside one linear memory is unusually safe here —
the scan is over a flat byte slice, a false positive retains garbage
inside C's own region and can touch nothing else, and a false positive can
never free something live. Optional, contained, orthogonal to everything
above.

---

## 3. The two-layer rule

**The low layer** — linear memory, packed layout, unaligned access,
`volatile`, atomics, SIMD — serves C, C++, Zig and Rust.

**The high layer** — typed cells, a traced heap, declared shapes,
dynamic objects — serves Go, Lua, Ruby, Python and JavaScript. Gyro sits
in the low layer with C, by decision.

One verified boundary between them: handles in one direction, layout-based
reads in the other.

**The failure mode is letting the low layer leak upward.** The moment a
managed producer can observe a raw address, precise tracing, movement,
and the sandbox all fail at once. This is the same rule `CLAUDE.md`
already states — *the closure-environment rule was tightened, not
loosened, when the second producer broke it, and the third got linear
memory added rather than `ptr<T>` weakened.* Add a model; never merge two.

There is a real tension to hold: **the more METALS becomes a good C and
Rust target, the more it becomes LLVM; the more it becomes a good Lua and
Python target, the more it becomes a managed runtime IR.** LLVM and the
JVM are two different artifacts for a reason. Two layers is how both fit
in one format without either becoming a bad version of the other.

---

## 4. What METALS has to grow

In dependency order. Each entry names who needs it and what else it pays
for, because none of these is worth building for one guest.

### 4.1 A precise traced heap

*Needed by: Go, Lua, Ruby, Python, JavaScript.*

Closer than it looks. METALS declares record shapes, and `any` already
interns a static descriptor per type carrying field names, byte offsets
and per-field descriptor pointers (`internal/cgen/any.go:85`). That is a
GC type map under another name.

Start non-moving and precise. Moving comes later, behind handle
indirection for the boundary and root maps at safepoints for the interior.

**Also closes:** the `memory reclamation` gap, which currently blocks any
long-running program.

### 4.2 Stack switching

*Needed by: Go's goroutines, Lua's coroutines, Ruby's fibers.*

The largest addition, and the one with no workaround on a traced heap: a
guest can only manage its own stacks when its stacks are bytes it owns,
which is exactly what it surrenders by leaving linear memory. Structured
control flow over a non-reifiable call stack cannot express suspension.

Design references: WebAssembly's stack-switching proposal, or effect
handlers.

**Also closes:** the `green threads` gap. `spawn` is a full pthread today;
the same primitive makes it cheap. One feature, two payoffs.

### 4.3 Dynamic objects with shape transitions

*Needed by: Python, Ruby, JavaScript.*

`any.get`/`any.set` reach the fields a record *declares*; those three
languages add fields at runtime. Shapes must transition — hidden classes,
which V8, SpiderMonkey and Ruby 3.2+ all converged on independently — and
method dispatch must go through a mutable table for monkeypatching and
prototype mutation.

The largest semantic surface of the five, and precisely what wasm-GC
lacks.

**Hard rule attached to it:** declared records and dynamic objects are two
different things, and a static producer must never touch the dynamic
path. Get this wrong and every language runs at the speed of the most
dynamic one.

### 4.4 Runtime loading

*Needed by: `eval`, `exec`, `define_method`, `new Function`, Lua `load`.*

METALS is whole-program; `metals.Link` runs before anything does.
Supporting `eval` means loading METALS at runtime — and the three-tier
architecture already contains the answer: **AOT code runs on nasm or
cgen, `eval`'d code runs on meval.** The evaluator stops being only the
oracle and becomes the fallback execution mode.

### 4.5 Optional refcounting over the tracer

*Needed by: Python's `__del__` timing, and any RAII-shaped guest.*

Let a producer refcount its own objects while the collector handles
cycles. That is CPython's own design, so the semantics are inherited
rather than approximated. Hybrid RC/tracing is well-trodden.

---

## 5. What this costs Gyro — priced, and rejected

**Decided against** (2026-08-25; see `docs/decisions.md`, "Gyro's memory
model is C's, and stays C's"). The ledger below is why: it is a language
redefinition, and the identity it deletes — manual memory, the borrow and
leak analyses, C-family membership — is the language. Kept as the record
of what was rejected and what it would have cost.

Gyro moving onto a traced heap is **a language redefinition, not a
memory-model swap.** Recorded plainly:

- `free` becomes vestigial. `panic-use-after-free` and `panic-double-free`
  describe behavior that would no longer exist — deleted outright, per the
  project's own rule.
- **The borrow and leak analyses lose their reason to exist** in their
  current form. They police lifetimes by hand; a collector does that.
  What survives is aliasing analysis, a much smaller job. This is the
  largest identity change: a meaningful part of what Gyro currently *is*
  lives in `internal/sema`.
- `MutexGuard::unlock`-is-explicit was justified by "Gyro has no
  destructors". With 4.5 available, the open `guard/resource release`
  question gets a real answer instead of a workaround.
- Allocation gets faster (bump pointer); write barriers and collection
  pauses appear where there were none.
- A packed `@repr(C)` layout is wanted alongside the one-qword-per-scalar
  rule, so Gyro can read a Rust or Zig struct in place.

Net: Gyro becomes a managed language that can still talk to unmanaged
ones. `docs/decisions.md` would need a large entry, and `docs/native.md`'s
layout table would grow a second row set.

---

## 6. Direct targeting instead of the wasm bridge

The bridge exists because clang already produces a C-shaped program and
the relooper has already run (`internal/wasm/clang.go:14-21`). Making
METALS a *direct* target for C, Rust, Zig and Go changes the trade.

**What it fixes.** The lossy round-trip through wasm — where types,
structs and the original control flow are destroyed and reconstructed
from `i32` offsets — disappears. So does wasm32's ILP32 data model: 64-bit
pointers, no 4 GB ceiling, and `long` is `long` again, which was a
*compatibility* problem and not only a performance one. Well-typed C and
Rust could emit `ptr<T>` rather than bytes, at the cost of the sandbox for
that code.

**What it costs.** Two things, both large.

*The optimizer.* LLVM is twenty years of inlining, SROA, GVN, LICM, alias
analysis, vectorization, scheduling and register allocation. There is one
escape hatch already built: **cgen emits C, so the host compiler
optimizes** — `internal/cgen/build.go:52` passes `-O1
-fno-strict-aliasing` today, and that is raisable. On that tier LLVM's
optimizer is kept for free while we own the IR above it. `nasm` is where
the bill lands.

*The relooper.* C's `goto` can produce genuinely irreducible control flow,
and turning it into `block`/`loop`/`br` requires node duplication or a
dispatch loop. Going direct takes back in-house the one piece `clang.go`
identifies as the hardest part of a real C front end.

### The move that makes it tractable: backends, not front ends

- **Rust** — implement a `rustc_codegen_ssa` backend emitting METALS and
  consume MIR, the way `rustc_codegen_cranelift` does. Borrowck, trait
  resolution and monomorphization are inherited, and semantics are
  preserved by construction rather than by matching a spec.
- **Zig** — self-hosted backend infrastructure already emits LLVM, C and
  native x86; a METALS backend slots into an existing seam.
- **Go** — the `gc` toolchain has a well-defined SSA IR; same story.
- **C** — small enough to write honestly. Preprocessor, C11, bitfields,
  VLAs, `_Generic`.
- **C++** — don't. Use Clang's AST if it is ever needed. The list of
  complete C++ front ends has four entries for a reason.

### What the low layer must grow for this

Packed and arbitrary struct layout (today: *every scalar occupies one
qword, nothing is packed*), unaligned access, `volatile`, atomics with a
memory ordering, SIMD vector types, `i128`, bitfields, inline-asm or
intrinsic passthrough, and irreducible control flow.

---

## 7. The performance ledger

Honest, and the two halves have opposite answers.

| | vs. its best existing implementation | why |
| --- | --- | --- |
| C, C++, Zig, Rust | ~1.0–1.3× via direct target; parity plausible on cgen, worse on nasm until it grows an optimizer | bounds checks where the sandbox is kept; otherwise the host compiler still optimizes |
| Go | 1.2–3× slower than the `gc` toolchain | competing with a mature AOT compiler, GC and escape analysis |
| Lua | ~2–5× **faster** than PUC-Rio; 5–20× slower than LuaJIT | beat a bytecode interpreter, lose to a trace compiler |
| Python | ~1.5–4× **faster** than CPython; slower than PyPy | same shape |
| Ruby | faster than CRuby | same shape |
| JavaScript | 10–100× slower than V8 on hot code | no AOT compiler has ever matched a good JS JIT |

**The structural reason, which no amount of IR work removes:** dynamically
typed languages get their speed from *speculation* — observing that a call
site has seen one shape ten thousand times, compiling for it, and
deoptimizing when wrong. An AOT compiler cannot know what it has not
seen. METALS has no runtime codegen, so that route is closed unless a JIT
is added. Every dynamic language therefore beats its naive interpreter and
loses to its JIT. No exceptions.

Also note: Gyro today has **no** collector. Moving it onto a traced heap
is a performance regression for Gyro, bought for correctness and for the
reclamation gap closing.

### What is genuinely zero-overhead

Not per-language speed. **Combining languages.**

- A Gyro function calling Lua calling Go, one heap, one representation, is
  a direct call: no marshalling, no FFI transition, no serialization, no
  copies. Today CPython↔C is cheap but CPython↔Go is a subprocess or a
  socket; here it is a call.
- **Whole-program optimization across language boundaries.** Once Lua and
  Gyro are in one METALS module, Gyro inlines into Lua. No FFI can do
  that, ever, and some mixed programs would beat the sum of their
  best-in-class parts.
- A verified boundary and a real sandbox, which none of the fast
  implementations offer.

Price the two halves separately when deciding what to build. For the
unmanaged family METALS *costs* performance and buys safety and
verification. For the managed family it costs nothing at the boundary and
buys interop that does not currently exist at any price.

---

## 8. Per-language verdict

| | strategy | needs | catch |
| --- | --- | --- | --- |
| C, C++, Zig, Rust | direct producer, linear memory | §6 | stays unmanaged, by design |
| **Lua** | **native producer, shared heap** | 4.1, 4.2 | none worth the name — the best first target by a distance |
| **Go** | native producer, shared heap | 4.1, 4.2 | `unsafe.Pointer` and `reflect` need a layout escape hatch |
| **JavaScript** | native producer, shared heap | 4.1, 4.3, 4.4 | large but mechanical; uniquely **no C-extension ecosystem to break**, since DOM and Node are host APIs |
| **Python, Ruby** | native producer, shared heap | 4.1–4.5 | numpy and nokogiri — see below |

**On the C-extension wall.** It is a road, not a wall: compile the
extensions as linear-memory C and bridge them to managed objects through a
C-API emulation layer. GraalPython does exactly this and numpy works
there. Slow, a large compatibility surface, years of work — but it exists,
and it should be known before deciding Python's tier.

**The alternative for Python, Ruby and Go: host the runtime whole.**
CPython, CRuby and Go all already ship wasm targets, so compiling them
into their own linear memories gives *complete* compatibility for free —
`sys.getrefcount` works, `__del__` fires on the last decref, numpy works,
because it is CPython. The price is interpreter speed, no JIT ever,
several MB per guest, and copies at the boundary. Pyodide and `ruby.wasm`
are the shipped existence proofs. This is the compatibility end of the
dial; §4 is the interop end. Both are legitimate; they cannot be occupied
at once.

---

## 9. Sequence

Reordered by the §5 decision: Gyro stays unmanaged, so the traced heap is
no longer step one and the C-compatibility work moves to the front.

1. ~~**Packed `@repr(C)` layout** beside the one-qword rule~~ —
   **done**, as one C layout rather than two: a register still carries a
   widened scalar, but everything in memory is what C would lay out.
2. **FFI growth.** Structs by value is **done** — a record crosses as
   the same bytes, with the generated shim asserting the layout against
   `cc` — leaving callbacks and out-parameters.
3. **Direct C → METALS front end** (§6), emitting `ptr<T>` into Gyro's
   own memory, with the loader learning `.c`. The bar: any existing C
   program imports unmodified. The wasm bridge stays as the sandboxed
   reference answer for the corpus.
4. **Reclamation, solved unmanaged.** Arenas, a quarantining allocator,
   or stronger static analysis — the gap still gates long-running
   programs, and a collector is no longer the answer for Gyro.

The managed-guest track (traced heap 4.1, stack switching 4.2, then Lua,
then Go) remains coherent and remains unbuilt. If it is ever taken up,
the heap is for the guests; Gyro does not move onto it.

---

## 10. Near-term, and verified

These are measured against the tree, not reasoned about, and they gate
everything above.

- **One `.c` is one module is one memory.** `gyro run cJSON.c reference.c`
  fails with `the module exports no main`, because `wasm.CompileC` takes a
  single path. Cheapest correct fix: one `wasm-ld` invocation over all
  objects, translating the single module that comes out — which is how
  these languages already link, and it sidesteps imported memory entirely.
- **Gyro cannot import a C or wasm module.**
  `internal/loader/foreign.go:24` knows `.lisp` and `.scm` only, so
  `import { add } from "./lib.c"` is handed to the Gyro parser and fails
  with `E0202: expected a declaration, found identifier __attribute__`.
  The mechanism — `Module.Foreign`, `addForeign`, verify-then-synthesize —
  is already built.
- **`argc`/`argv` are zeroed** (`internal/wasm/translate.go:297-306`), so
  `//gyro:args` is inert for `.c` cases.
- **libc breadth.** No transcendental math (`sin`, `pow`, `exp`, `log`),
  no `scanf` family, no `fgets`/`fread`, no `FILE` beyond the three
  standard streams, no `time.h`, and no `inttypes.h`, `setjmp.h`,
  `locale.h` or `stdatomic.h`. Upstream jsmn runs unmodified except its
  `clock_gettime` timer; cJSON is blocked on `sscanf`.

---

## 11. Decisions to revisit, not to quietly contradict

- **wasi-libc** (`docs/decisions.md:1673`) was rejected because the
  sysroot is uncommon and the corpus case would skip nearly everywhere.
  That is a CI-testability objection, not a technical one. If hosting real
  runtimes becomes a goal, the calculus changes and the rejection should
  be re-argued.
- **Atomics and a memory ordering.** Still the addition that commits the
  format to a memory model all three tiers must reproduce identically.
  Required by Rust's `std::sync`, C++ `<atomic>`, and any threaded guest.
- **Imported memory** (`docs/metals.md:523`) is only needed for
  separately-shipped modules sharing a heap. A single link step avoids it.
  Do not add it to solve multi-file C.

## 12. Rejected

- **Merging the two memory models.** Moving collectors and raw addresses
  cannot both be true of one object; `&mut` cannot cross into an aliasing
  runtime soundly; two collectors sharing a graph cannot collect a cycle
  spanning it. wasm-GC, designed from scratch with hindsight, made the same
  split — GC references cannot point into linear memory and cannot be
  stored in it.
- **A bulk import taking an address and a length.** Reintroduces the leak
  the boundary rule exists to prevent. If per-byte i/o costs too much, the
  answer is a bulk *instruction* between a memory and an `Array<u8>`
  handle, checked on both sides.
- **Conservative tracing as the general mechanism.** See §2.
- **Two layouts, one opt-in** (2026-08-25). A packed `@repr(C)` beside
  the qword layout would have made `ptr<i32>` mean a four-byte cell in
  one struct and an eight-byte cell in another, which is the same
  one-type-two-representations mistake the memory-model rule refuses.
  One layout, and it is C's.
- **Claiming "no behavior changes".** The honest claim is: each language's
  semantics preserved when it runs alone, modulo a documented list, with
  interop through a declared protocol and specified conversions. The seams
  are where behavior is defined by us rather than inherited, and they
  should be written down rather than papered over.
- **Gyro on the traced heap** (2026-08-25). §5 priced it; the price was
  the language's identity — manual memory, the borrow and leak analyses,
  and the C-family membership that full C interop depends on. Gyro stays
  unmanaged; the heap, if built, is for managed guests only. Recorded in
  `docs/decisions.md`.
