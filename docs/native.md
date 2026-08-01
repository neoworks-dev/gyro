# The native backend

The native backend compiles a verified METALS module to x86-64 NASM
assembly, assembles it, and links it with a small C runtime into a Linux
executable:

```sh
gyro build examples/hello.gyro -o hello   # nasm + cc must be installed
./hello
```

The Go code lives in `internal/nasm`; the runtime is `internal/nasm/rt.c`,
embedded into the compiler and handed to `cc` at link time. Correctness
is defined by the oracle chain: for every example, the native binary's
stdout, stderr and exit code must be byte-identical to `gyro run`, which
is itself diff-tested against the METALS evaluator.

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

## v1 limitations

All recorded in `decisions.md` as still open:

- Printing a composite (record, slice, tuple) without a `to_string`
  method fails at build time; the lowering must learn to synthesize
  per-type repr functions first.
- Implicit allocations leak; there is no GC and no ownership pass.
- `select` is polled and deterministic, not fair.
- Use-after-free and uninitialized reads are undetected.
- Linux x86-64 only.
