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
link time by both. Correctness is defined by the oracle chain: for every
example, a native binary's stdout, stderr and exit code must be
byte-identical to `gyro run`, which is itself diff-tested against the
METALS evaluator.

Everything below — the value layout, the semantics the codegen must
preserve, the runtime contract — belongs to both backends. Only the
"Frames and the ABI" section is the assembler's alone; the C backend's
own section, at the end, says what it does instead.

The four decisions that shape the backend — C runtime on libc, System V
ABI, spill-everything codegen, OS-thread concurrency — are recorded in
[`decisions.md`](decisions.md#native-backend).

## Value layout

Every METALS type has a static size, a multiple of 8; alignment is
always 8. Nothing is packed in v1.

| type | size | layout |
| --- | --- | --- |
| `i8..i64`, `u8..u64`, `usize`, `rune`, `bool` | 8 | sign/zero-extended to the full qword |
| `f64`, `f32` | 8 | IEEE double; an `f32` is a double rounded at `conv`, mirroring the evaluator |
| `str` | 8 | pointer to an immutable heap/static header `{ i64 len; char bytes[len] }` |
| `ptr<T>`, `chan<T>`, `future<T>`, `handle` | 8 | pointer to a runtime object |
| `fn(..)` | 16 | `{ code, env }`; `env` is 0 for a bare `fn.ref` |
| `slice<T>` | 16 | `{ data, len }`, borrowing its backing store |
| `opt<T>` | 8 + sizeof(T) | `{ flag, payload }`; flag 0 is null |
| `tuple<A, B, ..>` | Σ sizeof | elements in order |
| record | Σ sizeof | fields in declared order |
| enum | 8 + max variant | `{ tag, payload }`; payload fields in order |
| `array<T, N>` | N · sizeof(T) | elements in order |

Aggregates live inline: in a register's stack slot, in a record's field,
in an enum's payload. A `copy` is a `memcpy` of the static size, which
reproduces the evaluator's deep-copy semantics exactly, because
everything a value shares intentionally (slice backings, channels,
handles, pointers, string bytes) sits behind a pointer and everything it
owns sits inline.

`slice.of` borrows the array register's stack slot. A slice that
outlives the frame of the array it borrows dangles — the same
already-accepted hazard `decisions.md` records for the language.

## Frames and the ABI

This section is the assembler backend's alone; the C backend leaves
frames and calling conventions to the C compiler.

Every METALS register gets one frame slot of its type's size; every
instruction loads its operands from slots, computes in `rax`/`rcx`/
`rdx`/`r10`/`r11`/`xmm0`/`xmm1`, and stores the result back. No values
stay in machine registers across instructions. `rsp` stays 16-aligned
at every call.

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
- **Structural equality.** `eq` on scalars compares qwords; on floats,
  `ucomisd` with NaN handled (NaN ≠ NaN); on `str`, a runtime call; on
  aggregates, a synthesized per-type helper function walking the static
  layout.
- **Bounds and null checks.** `index.get`/`index.set` abort with the
  evaluator's formatted message; `opt.unwrap` aborts with
  `unwrap of a null value`.
- **Aborts.** `abort` prints `panic: <message>` to stderr. On the main
  thread the process exits 1; on a spawned thread only that thread dies,
  exactly like the evaluator's goroutine recovery.

## The runtime contract

The C runtime implements the `gyro:rt` import surface. Monomorphized
import names map to C symbols plus static marshaling decided at codegen
time — `Array<Point>::push` becomes `gy_array_push(handle, &value)`
against an array that recorded its element size at `Array<T>::new`.

Obligations beyond the obvious:

- **Formatting matches Go.** The oracle prints through Go's `strconv`,
  so the runtime reproduces it: shortest round-trip float formatting
  with Go's exponent thresholds (`12.56636`, `12`, `1e+21`, `1e-05`),
  `%q`-style quoting where repr quotes, Go's error strings for OS
  errors (`stat gyro.toml: no such file or directory`) and the
  interpreter's own messages (`"x" is not an i32`, even
  `the interpreter has no network`).
- **Hashing matches the oracle bit for bit.** `hash<T>` covers the
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

- **The layout table above, exactly.** Every generated struct has
  8-byte members only, so C's natural layout *is* the documented one and
  needs no packing attributes. Each typedef carries a `_Static_assert` on
  its size, which turns any drift into a build error rather than a wrong
  answer at the runtime boundary.
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
- **Register allocation is the C compiler's.** The asm backend spills
  every register by design; `cc -O1` does not. That difference is the
  point of having both.

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
- Linux x86-64 only.
