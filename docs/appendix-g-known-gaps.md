# Appendix G. What is not built yet

Gyro is usable for **CLI tools today**. It is not usable for anything
long-running, because nothing reclaims memory.

This appendix is the honest list. Where a gap is observable from a program,
it also lives in the execution corpus as a `//gyro:skip` with its reason
attached — a skip is a program that will fail the day the gap closes
([chapter 17.3](ch17-03-testing.md)).

## Blocking real applications

- **Memory reclamation.** The allocator model
  ([chapter 4.5](ch04-05-allocators.md)) is the answer, and
  `alloc<T>(a)`, `free(a, p)`, `format(a, ...)` and the containers and text
  functions honour it today, but the runtime's own storage does not yet: a
  future, a channel and the message of an error
  the runtime reports all come from the process heap and leak by design.
  Fine for a program that exits; fatal for a server. An `Array`, `Map` or
  `String` from `heap()` that is never freed is not warned about.
- **Formatting into an allocator is only half routed.** `format(a, ...)`
  puts the result in `a`, but the text of a piece that is not a string — an
  integer's digits, a `to_string` result, a derived representation — is made
  by the runtime on the process heap first, and nothing frees it. Streaming
  has the same cost for those pieces except the primitives, which the
  runtime writes directly.
- **A frame closure can outlive its frame through a container.** A
  closure's environment lives in the frame that wrote it and the borrow
  analysis rejects returning one, but it does not follow one into a struct
  field or an `Array`: that is the same hole a slice of an array literal has
  (issue #58). A closure written in a loop body and pushed without `keep`
  leaves every element sharing one record. The corpus keeps it as
  `closure-loop-push.gyro`, skipped with that reason.
- **No sorting** anywhere in the standard library.
- **Linux x86-64 only**, which is the one thing the backend emits.

## Language features that are typed but not finished

| feature | state |
| --- | --- |
| mutation rules | `*self` vs `self`, assigning through a value receiver — unchecked |
| `@comptime` evaluation | typed, not evaluated |
| effect rows | `io` is checked against declared rows; row variables, function values and spawn/channel effects are not ([chapter 12](ch12-00-effect-rows.md)) |
| module resolution | done; `.stack/` packages await the Stack package manager |
| `->` and `=>` | reserved tokens, unused |
| `syntaxfn` parser extensions | designed, not parsed |

## Safety holes

- **Mutation rules are unchecked.**
- **Use-after-free, double free and uninitialized reads are undetected.**
  The heap frees for real, and a block from `alloc<T>()` is zero-filled,
  so a field read before it is written reads zero rather than aborting. A
  METALS evaluator used to poison freed and unwritten cells and abort on
  a read; it is gone, and with it the only thing that caught either. The
  place for that check now is an allocator: one that quarantines freed
  blocks and poisons them is ordinary Gyro over
  [chapter 4.5](ch04-05-allocators.md), and is not written yet.
- **Type punning is not diagnosed.** Reading across two members of a C union that
  share no common initial sequence, or reading a struct's storage as
  bytes, reinterprets whatever is there. It is undefined in C too, `char *`
  aliasing included, and the information needed to catch it — which member
  was written — is not stored anywhere.
- **A pointer that has left its object is not caught.** `ptr.offset` past
  an object's extent, and a `ptr.cast` naming a type the storage is not,
  are both a producer's claim and nothing verifies either
  ([appendix D](appendix-d-metals.md)).
- **A slice can outlive the memory behind it**, and nothing detects that
  ([chapter 4.4](ch04-04-slices-and-views.md)).
- **Views through a pointer are untraceable** by the borrow analysis.
- **Effect rows under-report** where a row cannot travel: calling a function
  value contributes nothing.

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
  things. A `union` crosses as the type C declared
  ([chapter 15](ch15-00-c-interop.md)). An export the generated header cannot
  spell is **skipped** rather than failing the module, and naming a skipped
  one is an error at the import — as is an array of anything whose Gyro
  spelling is not one word, since `*u8[8]` reparses as a pointer to an
  array.

- **A `.c` cannot be a whole program.** The front end produces a library:
  what a Gyro program imports from a `.c` is its declared types and
  functions. C's entry is an `int main` whose result is the process exit
  code, and a METALS entry has nowhere to put that.

Known costs:

- **A local aggregate initializer writes zeros member by member**, so
  `char buf[4096] = "hi"` is 4096 stores. The cheap fix is a bulk zero
  instruction.
- **Structural matching across a runtime boundary** is unchecked: within one
  runtime the linker compares declarations, and across a boundary the value
  is copied and nothing checks that two same-named records agree.

## Linear memory

METALS has linear memory, `trunc` and `bitcast` — a flat, bounds-checked
byte region whose addresses are `u32` integers, which is what a producer
whose source language has that memory model needs
([appendix D](appendix-d-metals.md)). **No producer emits one today.** It
came in for a bridge that read what clang made of C as wasm32; that bridge
is gone, and what exercises the instructions now is METALS written by hand
in the execution corpus.

Left undecided with it:

- **A memory declared by one module and imported by another**, which is how
  two translation units would share one heap. Linking gives each its own.
- **A bulk instruction between a memory and an `Array<u8>` handle.** The
  boundary between a memory and the outside is per-byte on purpose — an
  import taking an address and a length would make a linear address a
  pointer, which is the one thing the model forbids — so if per-byte i/o
  ever costs too much, the answer is an instruction checked on both sides.

## METALS

Open questions in the format itself ([appendix D](appendix-d-metals.md)):

- what METALS expands to — the name predates the design
- the binary encoding
- structural matching of record shapes that cross a boundary
- how foreign runtimes embed: a JS engine and the Gyro runtime in one
  process is a runtime problem the IR only gestures at via `any` and
  `handle`
- whether effect rows belong in METALS signatures at all
- whether any optional other than `opt<ptr<T>>`, `opt<code(..)>` and
  `opt<anyptr>` deserves a niche

## The native backend

- `select` is polled and deterministic, not fair — which is why the language
  leaves the choice among ready arms unspecified
  ([chapter 13.2](ch13-02-channels.md)).
- No green threads: `spawn` and `async` are OS threads.
- Register allocation exists but leaves work on the table; `make bench` is
  what measures it, against the C each case was ported from
  ([chapter 17.3](ch17-03-testing.md)).
- **There is no second implementation to disagree with.** A wrong answer
  that is plausible is now caught only by a golden someone wrote by hand
  ([chapter 17.1](ch17-01-the-backend.md)).

## Open design questions

`@comptime`'s evaluation subset; drop/RAII, which governs
`MutexGuard::unlock`, `free` and `close` alike; the decorator execution
model; `syntaxfn`; the Stack package manager; METALS' binary encoding.

Each of these is recorded, with what has been rejected so far, in
[`decisions.md`](decisions.md) under "Still open".
