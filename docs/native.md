# The native backends

There are two, and they are siblings. The assembler backend compiles a
verified METALS module to x86-64 NASM assembly, assembles it, and links
it with a small C runtime into a Linux executable; the C backend emits
one C translation unit instead and hands it to `cc` with the same
runtime:

```sh
gyro build examples/hello.gyro -o hello              # nasm + cc
gyro build examples/hello.gyro --backend c -o hello  # cc alone
./hello
```

The Go code lives in `internal/nasm` and `internal/cgen`; the runtime is
`internal/crt/src/rt.c`, embedded into the compiler and handed to `cc` at
link time by both. Correctness is defined by the corpus, not by another
tier: for every case, a native binary's stdout, stderr and exit code must
match the golden the case declares, and the METALS evaluator must match
the same golden. Neither backend is held against the evaluator's output —
both are held against what the case says the program prints.

Everything below — the value layout, the semantics the codegen must
preserve, the runtime contract — belongs to both backends. Only the
"Frames and the ABI" section is the assembler's alone; the C backend's
own section, at the end, says what it does instead.

The four decisions that shape the backend — C runtime on libc, System V
ABI, callee-saved-only register allocation, OS-thread concurrency — are
recorded in [`decisions.md`](decisions.md#native-backend).

## Value layout

**Storage is C's.** A type occupies what `sizeof` would say in C, at
C's alignment, with padding where C pads — so a Gyro struct and a C
struct of the same fields are the same bytes, and a `u32[]` is a
`uint32_t[]`. That is the whole point: `decisions.md` records the
commitment that Gyro's memory model is C's, and this is the layer where
it is true or it is not.

**A register is one word.** A METALS register carries a scalar widened
to a qword, and carries an `f32` as the double it widens to. Aggregates
are the same in both, since an aggregate register *is* its storage.

The arithmetic lives in `metals.Layout`, not in a backend: two backends
computing padding separately would agree until the first `u8` in front of
a `u32`, and the corpus would then be measuring which one guessed.

| type | storage size | alignment | layout |
| --- | --- | --- | --- |
| `i8`, `u8`, `bool` | 1 | 1 | `bool` is 0 or 1 |
| `i16`, `u16` | 2 | 2 | |
| `i32`, `u32`, `rune` | 4 | 4 | |
| `i64`, `u64`, `usize` | 8 | 8 | |
| `f32` | 4 | 4 | IEEE single; the register holds the double it widens to |
| `f64` | 8 | 8 | IEEE double |
| `str` | 8 | 8 | pointer to an immutable heap/static header `{ i64 len; char bytes[len] }` |
| `ptr<T>`, `chan<T>`, `future<T>`, `handle` | 8 | 8 | pointer to a runtime object |
| `anyptr` | 8 | 8 | the same address a `ptr<T>` is, with the format no longer recording what is at it; what C's `void *` is |
| `fn(..)` | 16 | 8 | `{ code, env }`; `env` is 0 for a bare `fn.ref` |
| `code(..)` | 8 | 8 | the address of a function, no environment beside it; what C's function pointer is |
| `slice<T>` | 16 | 8 | `{ data, len }`, borrowing its backing store |
| `any` | 16 | 8 | `{ descriptor, payload }` |
| `opt<ptr<T>>`, `opt<code(..)>`, `opt<anyptr>` | 8 | 8 | the address itself; 0 is null. None of the three is ever null, so the address no live value has is the spare pattern |
| `opt<T>`, otherwise | 8 + sizeof(T), rounded | max(8, align T) | `{ i64 flag, payload }`; flag 0 is null |
| `tuple<A, B, ..>` | C's | widest member's | elements in order, padded like a C struct |
| record | C's | widest field's | fields in declared order, padded like a C struct |
| union | widest member's, rounded | strictest member's | every member at offset 0; one of them holds a value at a time |
| `align N` on either | rounded up to N | max(members', N) | raises the alignment above what the members ask; moves no member |
| enum | 8 + widest variant, rounded | 8 | `{ i64 tag, payload }`; payload at 8, its fields padded like a C struct |
| `array<T, N>` | N · sizeof(T) | align T | elements in order, no padding between them |
| opaque `%Name` | — | — | no size and no alignment; only a `ptr<T>` may target one, and that pointer is a word like any other |

Nothing is aligned more than a word, which is what keeps an enum's
payload at a fixed offset and lets a frame hand out word-aligned cells.

Aggregates live inline: in a register's stack slot, in a record's field,
in an enum's payload. A `copy` is a `memcpy` of the static size, which
reproduces the evaluator's deep-copy semantics exactly, because
everything a value shares intentionally (slice backings, channels,
handles, pointers, string bytes) sits behind a pointer and everything it
owns sits inline.

`slice.of` borrows the array register's stack slot. A slice that
outlives the frame of the array it borrows dangles — the same
already-accepted hazard `decisions.md` records for the language.

### Between the two representations

Reading a narrow scalar out of storage widens it — by its own
signedness, and through `cvtss2sd` for an `f32` — and writing one back
narrows it. That conversion is every `load`, `store`, `field.get`,
`field.set`, `index.get`, `index.set`, `tuple.get`, `variant.field`,
`opt.wrap` and `opt.unwrap`, and it is one pair of helpers in each
backend rather than a case per instruction. In C it is C's own implicit
conversion, because a member is declared with its real type.

**What the runtime holds, it holds in storage representation** — a
channel's elements, an `Array`'s, a mutex's value, a future's result, an
`any`'s payload. That is forced rather than chosen: `Array<T>::slice`
hands out a slice that borrows the Array's own elements, so the two must
agree on stride, and after that the only coherent rule is that everything
the runtime stores looks the way memory looks.

A narrow scalar therefore cannot be handed to the runtime as the address
of its register: the register is wider, and an `f32`'s register is a
double. It goes through a **staging cell** — one word per frame in the
assembler, a typed temporary in C — which narrows on the way out and
widens on the way back. The internal argument buffer an async call packs
is the one exception: nothing outside a backend sees it, so it holds
registers.

## Frames and the ABI

This section is the assembler backend's alone; the C backend leaves
frames and calling conventions to the C compiler.

Every METALS register gets one frame slot — a word for a scalar, the
storage size for an aggregate, rounded up to a whole word so that a cell
whose size is not one leaves the next slot word-aligned — and the
allocator (below) may give the hottest of them a machine register to
live in instead. Anything without one is loaded from its slot, computed
in `rax`/`rcx`/`rdx`/`r10`/`r11`/`xmm0`/`xmm1`, and stored back. `rsp`
stays 16-aligned at every call. A `slot` cell is sized by its type's
storage rather than its register, because a pointer addresses it.

Calls are System V AMD64: integer-class scalars in `rdi, rsi, rdx, rcx,
r8, r9`, floats in `xmm0..xmm7`, overflow on the stack, scalar results
in `rax`/`xmm0`. Two refinements:

- **Aggregates pass by pointer.** An argument wider than 8 bytes is
  passed as a pointer to the caller's value; the callee copies it into
  its own frame on entry. This deviates from SysV classification, but
  only between generated functions: runtime signatures use scalars and
  pointers exclusively, so every `call` into C is exactly ABI-correct.
- **Aggregate results use sret.** The caller passes a destination
  pointer as a hidden first argument, which is SysV's own MEMORY-class
  return.

A closure's environment is an ordinary first parameter; `call.indirect`
tests the fn value's `env` word at runtime and emits the argument setup
both ways.

## Register allocation

`internal/nasm/regalloc.go` hands out the callee-saved registers —
`rbx`, `r12`..`r15` — and nothing else. That is what makes the
allocation one decision per METALS register rather than one per program
point: a value in a callee-saved register survives every call the frame
makes, so no instruction spills around a call and none reloads
afterwards. The prologue saves the registers it took to dedicated frame
slots and every `ret` path restores them, which keeps the frame size,
and with it `rsp`'s alignment, exactly what the layout computed.

A METALS register is a candidate when its type is one integer-class
word — an aggregate lives inline in memory by the layout table above,
and SysV has no callee-saved `xmm` register — and when no emitter needs
its address. The address-taken cases are an explicit table in
`rolesOf`: a channel send's element, an `await`'s destination, a select
arm's element, and every runtime import whose plan passes an argument or
a result by pointer. `frame.addr` fails the build if a register with a
home ever reaches it, so a missing entry is a build error rather than
wrong code.

Liveness is a backward dataflow over the structured body, with a
fixpoint at each `loop`. A linear live interval would *not* be safe
here: a value written before a loop and read inside it is live across
the back edge, which no first-mention-to-last-mention interval covers.
The interference graph is coloured greedily, hottest first — a mention
inside a loop counts ten times one outside it, and a register needs
three mentions before a home pays for the two accesses the prologue and
epilogue spend on it. Whatever does not fit keeps its slot.

Two things sit beside the allocation:

- **Immediates.** A register that one `const` defines and nothing else
  writes, whose value fits the sign-extended 32 bits an x86 immediate
  is, rides in the instructions that read it: no slot, no machine
  register, no store. Constants propagate through `copy` and through a
  `conv` whose value provably survives, which is what makes the fold
  reach anything, since the lowering writes both around every literal.
  A conversion the value would not survive keeps its runtime check, and
  a shift by a folded count picks its Go-semantics branch here instead
  of at run time.
- **Working registers.** An operation whose destination has a home
  computes in it rather than through `rax` and back, and comparisons,
  tests and switches read their operand where it lies.

## Semantics the codegen must preserve

- **Checked arithmetic.** `iadd`/`isub`/`imul` abort on overflow
  (`jo` at 64/32 bits, a range check at narrower widths, carry checks
  unsigned), `idiv`/`irem` abort on zero and on MIN/−1. Wrapping ops
  mask and re-extend to their width. Abort messages match the evaluator
  byte for byte: `integer overflow`, `division by zero`.
- **Go shift semantics.** A shift count ≥ 64 (or negative) does not get
  masked the x86 way: `shl`/unsigned `shr` produce 0, signed `shr`
  produces the sign fill. Counts below 64 shift the full qword and then
  re-extend to the operand width.
- **`conv` is checked.** Narrowing that loses the value aborts with
  `conversion loses the value`; floats truncate toward zero first. The
  hairy cases (u64↔f64, range checks) are runtime helpers, not inline
  code.
- **Structural equality.** `eq` on registers compares qwords; on floats,
  `ucomisd` with NaN handled (NaN ≠ NaN); on `str`, a runtime call; on
  aggregates, a synthesized per-type helper function walking the static
  layout — and that one compares each member at its own width, since
  reading a word out of a narrow field would compare the padding beside
  it, or the field next door.
- **Bounds and null checks.** `index.get`/`index.set` abort with the
  evaluator's formatted message; `opt.unwrap` aborts with
  `unwrap of a null value`.
- **Module storage.** A global with no byte image is zeroed storage —
  `.bss` in the asm backend, a file-scope object in the C one, both of
  which are zero for free. One that carries an image is real bytes in
  `.data`; the C backend has no way to spell an object of an arbitrary
  type as bytes, so it emits a union whose first member is the image and
  whose second is the object, which is a static initializer and not a
  copy at startup.
- **Aborts.** `abort` prints `panic: <message>` to stderr. On the main
  thread the process exits 1; on a spawned thread only that thread dies,
  exactly like the evaluator's goroutine recovery.

## The runtime contract

The C runtime implements the `gyro:rt` import surface. Monomorphized
import names map to C symbols plus static marshaling decided at codegen
time — `Array<Point>::push` becomes `gy_array_push(handle, &value)`
against an array that recorded its element size at `Array<T>::new`.

Obligations beyond the obvious:

- **Formatting matches Go.** The evaluator prints through Go's `strconv`,
  so the runtime reproduces it: shortest round-trip float formatting
  with Go's exponent thresholds (`12.56636`, `12`, `1e+21`, `1e-05`),
  `%q`-style quoting where repr quotes, Go's error strings for OS
  errors (`stat gyro.toml: no such file or directory`) and the
  runtime's own messages (`"x" is not an i32`, even
  `this runtime has no network`).
- **Hashing matches the evaluator bit for bit.** `hash<T>` covers the
  scalars only — splitmix64 over an integer's word or a float's bits,
  FNV-1a over a string's bytes, `-0.0` folded into `0.0`. The
  composites never reach C: the lowering synthesizes one function per
  type and those call these.
- **Channels.** Mutex + condvar ring buffer; capacity 0 is a true
  rendezvous (the sender blocks until a receiver takes the element).
  `recv` fills a `(element, open)` tuple; a closed channel yields
  zeroed memory with `open` false. `send` on a closed channel aborts.
- **`select` polls.** Each arm gets a try-operation; no arm ready and
  no default means sleep briefly and retry. Deterministic in-order
  polling, where the evaluator randomizes — recorded as an accepted
  divergence for programs that race arms.
- **Async is eager.** Calling an async function starts a pthread
  running a codegen-emitted thunk (unpack argument buffer, call, store
  result) and returns a future — a one-shot mutex/condvar cell.
  `await` joins it. An async `main` is simply run synchronously by the
  entry point, which is what eager-start-plus-immediate-await reduces
  to.
- **Memory.** `new` is a zero-filled allocation behind a header whose
  magic makes `free` detect double frees. Nothing else is ever freed:
  strings, closure environments and futures leak by design in v1
  (recorded as open). Use-after-free and read-of-uninitialized are
  *not* detected natively — the evaluator's poisoned cells have no
  cheap equivalent — so programs that would abort there diverge
  instead of matching.

## Foreign calls

A program that calls a C function ([`language.md`](language.md#foreign-functions))
adds two pieces to a build, both shared by the backends rather than
written twice.

**A generated shim, one wrapper per foreign symbol.** `internal/crt`'s
`Shim` renders it from the module's `c:`-scheme imports; it is compiled
beside `rt.c` and the program, and both backends call the wrapper rather
than the library. The wrapper takes and returns the uniform shape — every
scalar one qword, `double` for a float, `void *` for a handle or a
struct — and calls the real function with its declared C widths, which is
what keeps a `float` parameter or an `int` result from being read as
eight bytes of whatever the callee left behind. The real function is
declared under a private name bound to its symbol with an `__asm__`
label, so the declaration never collides with a system header the runtime
already included.

**A struct by value crosses in the wrapper.** A record reaches the shim
as a pointer to the caller's copy — the aggregate convention both
backends already have — and the wrapper dereferences it into the by-value
argument C's ABI wants; a struct result comes back through a hidden out
pointer. Neither backend implements System V's struct classification,
because `cc` implements it already. The shim declares the struct with its
real C members and a `_Static_assert` per field offset against the layout
above, so the claim that the two languages agree on the bytes is checked
on every build rather than argued for.

The libraries come from the same imports and become `-l` flags. Nothing
in either backend knows what SDL is.

**Marshalling as ordinary runtime calls.** `internal/lower` wraps a
foreign call in `ffi.cstr`, `ffi.data` and `ffi.release`, so what reaches
codegen has scalar, handle and record arguments only. `gy_ffi_cstr`
NUL-terminates a copy of a string's bytes, `gy_ffi_from_cstr` copies text
back the other way, and `gy_ffi_opt_handle`/`gy_ffi_handle_opt` convert
between a null pointer and `opt<T>`'s flag. A slice needs no conversion
at all — its elements are already what C reads — so `gy_ffi_data` lends
the slice's own pointer and nothing is released. The string copy is
released after the call returns: a library that keeps that pointer, or
the slice's, keeps a dangling one.

## Linear memory

A METALS `memory` ([`metals.md`](metals.md#linear-memory)) is 24 bytes of
module storage holding rt.c's `gy_memory` — `{ bytes, size, max }` — and
nothing else. Both backends declare one per memory, call `gy_mem_init`
before `main` runs, and call `gy_mem_data` once per data segment.

The split between the two is the same one the rest of the runtime uses,
drawn where it saves an implementation rather than where it is tidiest:

- **Allocation, growth, the bulk operations and the abort text are
  rt.c's.** `gy_mem_init`, `gy_mem_grow`, `gy_mem_copy`, `gy_mem_fill`
  and `gy_mem_data` are ordinary calls from both backends. Each is one
  `memmove`, `memset` or `realloc`, so a call is not in the way of
  anything, and one implementation is one abort message.
- **The scalar access is inlined by both.** A bounds-checked load is five
  instructions and a call would cost more than that by itself, and this
  is the hot path of every program a C front end will produce. `cgen`
  emits `gy_mem_read`/`gy_mem_write` from its prelude, whose width is a
  literal at every call site, so `cc` folds the `memcpy` into a single
  load. `nasm` emits the check and the sized `mov` directly, choosing
  `movsx`/`movzx` by the access type's own signedness.

Both compute `addr + width` in 64 bits before comparing against the size,
so an address near the top of the `u32` range cannot wrap into a valid
one. An `f32` in memory is four IEEE-754 bytes while an `f32` register is
the double it widens to, so a load converts on the way in and a store on
the way out — the same asymmetry `conv` already has.

`trunc` and `bitcast` are the same two steps in both backends: get the
value's byte pattern, then read it back as the target type. For an
integer that is one extension instruction; for a float it goes through
`cvtsd2ss`/`movd`, because the pattern of an `f32` is the single's, not
the double's the register holds.

## The C backend

`internal/cgen` emits one self-contained C file per program and compiles
it with `cc -O1 -fno-strict-aliasing program.c rt.c -lpthread -lm`. It
exists for two reasons, neither of them replacing the assembler: it
checks the METALS lowering without the assembler-level bug surface — a
disagreement between the two native tiers on the same module is a codegen
bug, and the C one is far easier to read — and it gives us a second
binary from the same IR to benchmark the assembler's output against.
`gyro cgen file.gyro` prints the C the way `gyro nasm` prints the asm.

What it does the same way:

- **The layout table above, exactly.** Every member is declared with its
  real C type, so C's natural layout *is* the documented one and needs no
  packing attributes — that is what makes the table C's rather than a
  reimplementation of it. Each typedef carries a `_Static_assert` on its
  size and one per member offset, which turns any drift into a build
  error rather than a wrong answer at the runtime boundary. A size
  assert alone would pass on two structs that pad differently and total
  the same, which is why the offsets are asserted too.
- **The runtime.** The same `rt.c`, the same `gy_*` symbols, the same
  monomorphized-import table (`Array<Point>::push` becomes
  `gy_array_push(handle, &value, 32)`), the same abort messages.
- **Structured control flow.** `block`/`loop`/`br`/`br_if` become C
  labels and `goto`; a `br` only ever targets an enclosing label, so
  every jump is a legal one. `if` and `switch` map straight across.
  Nothing inside a case body emits a C loop or switch of its own, so no
  `break` can be captured by the wrong construct.
- **The semantics C would otherwise get wrong.** Checked arithmetic,
  Go's shift semantics, `idiv`'s zero and MIN/−1 tests, and the checked
  conversions are a block of `static inline` helpers at the top of the
  file. The tests come *before* the operation, because C's signed
  overflow, division by zero and shift counts past the width are
  undefined rather than trapping.

What it does differently:

- **Registers are C locals.** A METALS register is not SSA but its type
  is fixed at the first assignment, which is exactly what a C local
  wants. `slot` gets a second, addressable local that the instruction
  re-zeroes, so a slot inside a loop is a fresh cell each pass;
  `slice.of` borrows it with `&`, the same borrow the asm makes of a
  stack slot.
- **Aggregates pass and return by value.** There is no sret and no
  argument-pointer convention between generated functions: C's own
  struct passing already does that job, and the runtime boundary is
  untouched because `rt.c` only ever sees scalars and pointers. The
  entry point is the one exception — `rt.c` calls
  `gy_user_main(void *args)` with a pointer to the command-line slice,
  so a small wrapper dereferences it.
- **Register allocation is the C compiler's.** The asm backend colours
  five callee-saved registers and leaves the rest in frame slots;
  `cc -O1` allocates the whole file, and inlines across it. The gap
  between the two on `tests/bench` is the measurement of what the
  assembler still leaves on the table.

The C compiler must accept two GNU extensions: a zero-length array
member, which is what keeps `array<T, 0>` at size 0, and casting between
`void *` and a function pointer. `-fno-strict-aliasing` is not optional:
aggregates move through `void *` all over the generated code — array
elements, channel payloads, future cells — and type-based aliasing would
let the optimizer assume those accesses cannot touch the same storage.

## v1 limitations

All recorded in `decisions.md` as still open:

- Printing a composite (record, slice, tuple) without a `to_string`
  method fails at build time; the lowering must learn to synthesize
  per-type repr functions first.
- Implicit allocations leak; there is no GC and no ownership pass.
- `select` is polled and deterministic, not fair.
- Use-after-free and uninitialized reads are undetected.
- A foreign call cannot take a callback or an out-parameter, and these
  are the only two tiers that can make one at all.
- Linux x86-64 only.
