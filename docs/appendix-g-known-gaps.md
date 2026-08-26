# Appendix G. What is not built yet

Gyro is usable for **CLI tools today**. It is not usable for anything
long-running, because nothing reclaims memory.

This appendix is the honest list. Where a gap is observable from a program,
it also lives in the execution corpus as a `//gyro:skip` with its reason
attached — a skip is a program that will fail the day the gap closes
([chapter 17.3](ch17-03-testing.md)).

## Blocking real applications

- **Memory reclamation.** Every `#{}`, `split` and `text()` allocates and
  nothing frees it. Strings, closure environments and futures leak by design
  in v1. Fine for a program that exits; fatal for a server.
- **No sorting** anywhere in the standard library.
- **Linux x86-64 only** for both native backends.

## Language features that are typed but not finished

| feature | state |
| --- | --- |
| mutation rules | `*self` vs `self`, assigning through a value receiver — unchecked |
| `@comptime` evaluation | typed, not evaluated |
| effect rows | `io` and `alloc` are checked against declared rows; row variables, function values and spawn/channel effects are not ([chapter 12](ch12-00-effect-rows.md)) |
| module resolution | done; `.stack/` packages await the Stack package manager |
| `->` and `=>` | reserved tokens, unused |
| `syntaxfn` parser extensions | designed, not parsed |

## Safety holes

- **Mutation rules are unchecked.**
- **Native use-after-free and uninitialized reads are undetected.** The
  evaluator poisons freed and unwritten storage and aborts; the C runtime
  frees for real and leaves storage as the allocator returned it. *The tier
  you ship is the one that does not check.* It cannot even name a double
  free reliably: the freeing routine writes its poison and then hands the
  block to libc, which reuses those bytes, so both tiers report `free of
  memory that is not allocated`.
- **A slice can outlive the memory behind it**, and nothing detects that
  ([chapter 4.4](ch04-04-slices-and-views.md)).
- **Views through a pointer are untraceable** by the borrow analysis.
- **Effect rows under-report** where a row cannot travel: calling a function
  value contributes nothing, and creating a capturing closure is not counted
  as `alloc`.

## Foreign functions

`@extern` carries primitives, `string`, slices, structs by value, opaque
handles, addresses (`*T`, `anyptr`) and code pointers, so out-parameters
and callbacks work through it ([chapter 15](ch15-00-c-interop.md)).

What it does not carry is a Gyro runtime object — a channel, a future, a
`fn` value, an `any` — at any depth.

**Nothing checks an `@extern` declaration against the library.** The symbol
is matched by name at link time and its signature is taken on trust, which
is C's own arrangement and the reason `import "./x.c"` is the stronger
route: there, the linker compares the declaration against the C the front
end read.

A program that calls a foreign function runs on the native tiers only.

## The C front end

Refused by name rather than mistranslated:

- **Two `goto` shapes** — a jump *into* a block (a label in a statement list
  the goto is not inside), and a backward jump reaching past a later forward
  label. Both need a relooper. Everything `goto` is actually written for —
  a forward jump to a cleanup label, a backward jump that is a loop,
  breaking out of nested loops — works.
- **A case label inside a nested statement** (Duff's device). A switch body
  is a flat list of labels and statements here.
- **A variadic C function cannot cross into Gyro**: a `...` says nothing
  about what a call passes, so there is nothing for the far side to check.
  Nor can a C `enum`: Gyro's is a tagged sum and the two are different
  things. A `union` crosses as a Gyro `union`
  ([chapter 5.1](ch05-01-structs.md)). An export the generated header cannot
  spell is **skipped** rather than failing the module, and naming a skipped
  one is an error at the import — as is an array of anything whose Gyro
  spelling is not one word, since `*u8[8]` reparses as a pointer to an
  array.

Runs, but on two tiers only:

- **Type punning**, reading across two union members that share no common
  initial sequence, and reading a struct's storage as bytes. All of it is
  undefined in C too, `char *` aliasing included. The natives reinterpret
  the bytes; the evaluator knows what is really there and says so. Round
  trips and the common initial sequence every tagged union is built on are
  fine on all three.

Known costs:

- **A local aggregate initializer writes zeros member by member**, so
  `char buf[4096] = "hi"` is 4096 stores. Correct, because a partial
  initializer must not depend on the tier; the cheap fix is a bulk zero
  instruction, not a weaker evaluator.
- **Structural matching across a runtime boundary** is unchecked: within one
  runtime the linker compares declarations, and across a boundary the value
  is copied and nothing checks that two same-named records agree.

## The wasm bridge

- **Multi-value** blocks, results and calls; **SIMD**; **reference types**;
  **atomics**; **passive data segments**. Each is refused with its own
  message rather than skipped.
- **An imported memory** — how wasm spells two modules sharing one heap.
  Linking gives each module its own, which is right for a sandbox and is the
  thing a real C front end will have to revisit.
- **No libc.** The bridge links freestanding; a program reaches the outside
  through an import it declares itself.

## METALS

Open questions in the format itself ([appendix D](appendix-d-metals.md)):

- what METALS expands to — the name predates the design
- the binary encoding
- structural matching of record shapes that cross a boundary
- how foreign runtimes embed: a JS engine and the Gyro runtime in one
  process is a runtime problem the IR only gestures at via `any` and
  `handle`
- whether effect rows belong in METALS signatures at all
- whether two modules may ever *share* a linear memory
- whether any optional other than `opt<ptr<T>>`, `opt<code(..)>` and
  `opt<anyptr>` deserves a niche

## The native backends

- `select` is polled and deterministic, not fair — which is why the language
  leaves the choice among ready arms unspecified
  ([chapter 13.2](ch13-02-channels.md)).
- No green threads: `spawn` and `async` are OS threads.
- Register allocation exists in the assembler backend but leaves work on the
  table; `make bench` is what measures it
  ([chapter 17.3](ch17-03-testing.md)).

## Open design questions

`@comptime`'s evaluation subset; drop/RAII, which governs
`MutexGuard::unlock`, `free` and `close` alike; the decorator execution
model; `syntaxfn`; the Stack package manager; METALS' binary encoding.

Each of these is recorded, with what has been rejected so far, in
[`decisions.md`](decisions.md) under "Still open".
