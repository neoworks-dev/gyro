# METALS

METALS is the shared intermediate representation that Gyro compiles into,
and that other language front ends (JavaScript, Python, ...) will compile
into, so that `import` can reach across languages. One format, many
producers, several consumers: the verifier, the evaluator
(`internal/meval`, the oracle the lowering is diff-tested against), and
the two native backends (`internal/nasm`, x86-64 via NASM, and
`internal/cgen`, generated C — see
[`native.md`](native.md)). `internal/lower` is the first producer and
`internal/lisp` — a statically typed Scheme — is the second, which is
what keeps the format honest: anything the IR assumes about its input
that only holds because Gyro's lowering happens to produce it shows up
there as a wrong answer or a verifier complaint.

`internal/wasm` is the third, and the one that stopped the format being
polite. It reads a wasm32 module — which is what `clang` makes of C — and
that is a language whose memory model METALS had no way to hold. Linear
memory, `trunc` and `bitcast` are what it needed, and each is a thing the
IR arguably should always have had for a producer that is not Gyro.

`internal/cc` is the fourth, and it is what the third was reconnaissance
for: a C front end that reads C itself and emits METALS' own types, so
that a `struct Point *` arrives as a `ptr<%Point>` rather than as the
`u32` it has become by the time clang is done. That is the difference
between C that *runs* on these tiers and C a Gyro program can `import`.
It shares Gyro's heap, which is allowed because a boundary is between
runtimes and the two are one; see [Boundary rules](#boundary-rules).
A C pointer is an `opt<ptr<T>>`, since C's is nullable and METALS' is
not, which is what turns a null dereference from undefined into an abort
that names itself.

`gyro metals file.gyro` prints a whole program as one module,
`gyro lisp file.lisp` prints what the Lisp front end produces,
`gyro cc file.c` prints what C becomes,
`gyro run a.metals b.metals` links modules and runs them — the same verb
that runs a `.gyro`, because both become one module before anything
executes — and `gyro build file.gyro` compiles a program to a Linux
executable.

This document is the source of truth for the format. The Go definitions
live in `internal/metals`; the text format below is what the printer emits
and the parser accepts, and every construct here exists in code.

## The eight decisions

Settled in review; recorded with rationale in
[`decisions.md`](decisions.md).

1. **Typed module IR.** METALS keeps modules, imports/exports, function
   signatures and declared type shapes — the JVM/CLR shape, not LLVM's.
   Interop is native to the format instead of bolted on.
2. **Registers + structured control flow.** Instructions write named
   virtual registers; control flow is `block`/`loop`/`br`/`if`/`switch`,
   never arbitrary goto. Registers are *not* SSA: reassignment is legal
   but a register's type is fixed at first assignment. The native backend
   SSA-constructs internally.
3. **No generics.** Front ends monomorphize before METALS; the IR only
   ever sees concrete types. A generic function crossing a language
   boundary is exported as concrete instantiations or with an `any`
   signature.
4. **`any` is the one escape hatch.** METALS is statically typed with a
   first-class dynamic value type and checked conversion ops. A Python
   function can export a typed signature or an `any` one — and a
   producer that *can* declare its types should, because then the call
   is an ordinary call with nothing inspected at run time.
5. **Copy at the boundary.** Values crossing between runtimes are
   converted/copied; no shared mutable memory between runtimes. A
   boundary is between runtimes and not between languages: producers
   that compile into METALS become one module on one heap and call each
   other outright.
   Pointers, channels and futures cannot appear in a cross-boundary
   signature. Long-lived foreign state is an opaque `handle`.
6. **Concurrency is hybrid**, the same line Gyro's grammar draws:
   `chan<T>` is an IR type and `send`/`recv`/`select`/`await` are
   verified instructions, while `spawn`, mutexes and the rest are runtime
   intrinsics — ordinary imported functions.
7. **Verified from day one.** METALS cannot trust its producers the way
   Gyro's backend trusts Gyro's checker; third parties will emit it. The
   verifier type-checks every register and instruction and enforces the
   boundary rules.
8. **Text first.** A canonical text form (this document's syntax) with a
   printer and parser, so IR is readable and diffable in tests. A binary
   encoding comes later and encodes the same tree.

## Module anatomy

```metals
module "gyro:examples/structs"

import "gyro:rt" {
  fn @spawn(fn())
  fn @println_str(str)
}
import "js:./widgets" {
  fn @render(any) -> any
}
import "cc:shapes" {
  type %Rect = record { w: i32, h: i32 }
  fn @area(%Rect) -> i32
}

type %Point = record { x: i32, y: i32 }
type %Shape = enum { Circle(f64), Rect(f64, f64), Point }
type %Event = union { kind: u32, point: %Point }
type %Session = opaque

global @counter: i32
global @greeting: array<u8, 3> = "hi\x00"

init @setup

fn @setup() {
  return
}

export fn @add(%a: i32, %b: i32) -> i32 {
  %sum = iadd %a, %b
  return %sum
}
```

- A module has a name, imports grouped by source module, named type
  declarations, globals, and functions.
- An import declares functions, and may declare types. A type is nominal,
  so two modules that each write `type %Rect = record { w: i32, h: i32 }`
  have two types and a value of one is not a value of the other. Writing
  the declaration inside an `import` says the opposite: this is the
  exporter's type, laid out here because a module holding one has to
  reserve the storage, and resolved to the exporter's declaration when
  the program is linked. There is one `%Rect` in the linked module and
  nothing is converted. See *Linking*.
- `@name` is a function or global; `%name` is a register or a named type;
  `$name` is a control-flow label. The three sigils keep the namespaces
  from colliding.
- A name after a sigil is a bare word, or a quoted string when it carries
  characters a word cannot: monomorphization and module qualification
  produce names like `@"Pair<i32>::sum"`, `@"list/util::helper"` and
  `%"Pair<i32>"`. Both spellings name the same namespace; the printer
  quotes exactly when it must.
- A global is module storage, zero-initialized and reached through
  `global.ptr`. It may carry a byte image — `= "hi\x00"` — which is the
  storage it starts with in place of those zeros, exactly `sizeof` bytes
  in the target's own representation. That is not an initializer: an
  initializer is an expression and needs an evaluation order, and static
  bytes have none. What an image cannot hold is a pointer, whose value is
  not known until the program is laid out, so the verifier refuses an
  image for any type with a pointer anywhere inside it.
- `init @name` is the function this module needs run before the program
  starts, which is where a global that *does* name an address gets
  written. It takes nothing and returns nothing. The linker prepends a
  call to each module's init to `@main`, in dependency order, so a
  module's storage is written after everything it imports has written
  its own and before anything reads it. That is the only place the call
  is made: a producer names the function, and does not arrange for it.
- A `memory` is a linear memory (below). A `data` segment writes a span of
  one before any function runs, which is a global's byte image one level
  down.
- Module names carry the producing language as a scheme prefix
  (`gyro:`, `lisp:`, `cc:`, `wasm:`, `js:`, `py:`); `metals:` names a module
  written as IR directly, with no source language above it. Runtime
  intrinsics are imports from runtime modules such as `gyro:rt`, not IR
  constructs.
  `c:` is the one scheme that names no producer at all — `c:SDL2` is a
  native library, so nothing links against it and the import survives to
  the backend, which turns it into a symbol and a `-l` flag.
- A call between two modules in different *runtimes* is a boundary
  crossing, and the boundary rules below apply to its signature. Every
  producer that compiles into METALS is one runtime; a native library and
  a foreign engine are not.

## Types

| form | meaning |
| --- | --- |
| `i8 i16 i32 i64 u8 u16 u32 u64 usize` | integers |
| `f32 f64` | floats |
| `bool rune str` | other primitives; `str` is an immutable string value |
| `ptr<T>` | pointer, never null, never crosses a boundary |
| `anyptr` | a pointer with the target's type erased; compares, casts back, loads nothing |
| `opt<T>` | nullable |
| `slice<T>` | borrowed view |
| `array<T, N>` | fixed-size array value |
| `tuple<A, B, ...>` | tuple value |
| `chan<T>` | channel |
| `future<T>` | un-awaited async result |
| `fn(A, B) -> R`, `async fn(A) -> R` | function values: `{ code, env }`, two words |
| `code(A, B) -> R` | the address of a function, one word, no environment |
| `%Name` | module-local record, union, enum, or opaque name |
| `any` | dynamically typed value, converted with checked ops |
| `handle` | opaque foreign reference; compares by identity only |

A function's result is absent for "returns nothing". Multiple results are
one tuple, matching Gyro.

**Layout belongs to the format, not to a consumer.** What a type occupies
in memory is C's answer — `sizeof`, C's alignment, C's padding — and
`metals.Layout` computes it once for every consumer, so that two backends
cannot pad a record differently. A register is a separate question and a
separate representation: one machine word per scalar, an `f32` carried as
the double it widens to. [`native.md`](native.md#value-layout) has the
table and the conversions between the two.

Named types are module-local: linking qualifies each with its module's
name, so two producers may declare the same one without colliding. A
record shape that must cross a boundary is redeclared by the importer;
matching the two declarations structurally is not implemented yet.

**`record align N` and `union align N` raise the alignment above what
the members require**, and round the size up to it the way C rounds a
struct up to its strictest member. Every offset is still the members'
own; this moves nothing. It exists for C's bit-fields: a run of them is
stored as the bytes it occupies, which align like bytes, while the
alignment C gives the struct comes from the bit-fields' *declared types*
— and those are not in the member list any more. `N` is a power of two
up to a word, because nothing here is aligned more than a word.

**`type %Name = union { ... }` is one storage with several readings of
it.** Every member begins at offset zero, the size is the widest
member's rounded up to the strictest member's alignment, and `field.ptr`
reaches a member the way it reaches a record's field — the same
instruction, a different offset, which is `metals.Layout`'s answer and no
consumer's.

It is a shape of its own rather than a record with coincident offsets,
because the difference is not where the members sit: **writing one member
unwrites the others**, which is a fact about reads and not about
addresses. A consumer that treated a union as a record would be right
about every address and wrong about every read.

An enum's payload is not this. An enum knows which variant it holds and
the format checks the tag; a union is C's, where nothing records which
member is live and the program is expected to know. So reading across
members is the language's rule and not the format's — defined for a
common initial sequence, undefined otherwise — and the tiers differ
there on purpose. The natives have C's own union under them and read
whatever the bytes were, which is what C leaves undefined; the evaluator
has no bytes to reinterpret, so it records the live member instead and
answers the defined reads while reporting the rest. That is the same
asymmetry `ptr.cast` has, and `tests/testdata/run/union-punning.metals`
is the case that says so.

A union does not compare and does not cross a runtime boundary, both for
the same reason: nothing records which member the storage means, so
equality has no question to answer and a copy has nothing the far side
could read. A record with one inside inherits both refusals.

**`type %Name = opaque` is a name with no shape.** It has no size, no
alignment and no fields, so the only position it may be written in is a
`ptr<T>`'s target — as a value, a member, an element, a parameter or a
result it is refused, and `load`, `store`, `ptr.offset`, `ptr.diff` and
`field.ptr` all refuse it too, each naming the layout as the reason. What
is left is a pointer that can be held, passed, stored, compared against
another of its type and against null, and have another type claimed onto
it with `ptr.cast`.

That is C's incomplete type, which is how every library header hands out
a handle: `struct SDL_Window;` and then `SDL_Window *` everywhere, so the
caller keeps the type and never learns the shape. Erasing one to `anyptr`
instead would compile and would throw away the point of the header — an
`SDL_Window *` and an `SDL_Renderer *` are different types, and staying
different is the whole of what the caller gets in exchange for being told
nothing. A pointer that forgot and a pointer to something unknown are two
different admissions, and only the second one is still checkable.

## Registers

- Parameters are registers, named in the signature.
- `%r = op ...` defines or reassigns a register. The first assignment
  fixes the type; every later assignment must produce the same type.
- A register must be assigned on every path before it is read. The
  verifier tracks definedness through the structured tree: after an
  `if`/`else`, only registers assigned in both branches count.

## Instructions

Every instruction is one line, except the structured ones, which nest.

### Constants and moves

```metals
%r = const.i32 42          ; const.<prim> for every primitive
%r = const.str "text"
%r = const.rune 'a'
%r = const.bool true
%r = const.null opt<i32>   ; the null of a specific optional type
%r = copy %x
```

### Arithmetic, bitwise, comparison

```metals
%r = iadd %a, %b     ; isub imul idiv irem — checked: overflow and /0 abort
%r = wadd %a, %b     ; wsub wmul — two's-complement wraparound
%r = fadd %a, %b     ; fsub fmul fdiv
%r = fmin %a, %b     ; fmax fcopysign
%r = fsqrt %x        ; fabs fceil ffloor ftrunc fnearest
%r = band %a, %b     ; bor bxor shl shr
%r = neg %x          ; bnot (integers), not (bool)
%r = eq %a, %b       ; ne — any comparable type, -> bool
%r = lt %a, %b       ; le gt ge — numeric and rune, -> bool
%r = conv %x -> u8   ; `as`: widening succeeds, narrowing aborts on loss
%r = trunc %x -> u8  ; integer narrowing that keeps the low bits
%r = bitcast %x -> u32 ; the same bits, read as another type of one width
```

Integer operands must share one type; there are no implicit conversions in
the IR either.

### The float intrinsics

`fsqrt`, `fabs`, `fceil`, `ffloor`, `ftrunc`, `fnearest`, `fmin`, `fmax`
and `fcopysign` are instructions rather than runtime imports. Each is one
machine instruction or a handful on both native backends, and a producer
that has them as *opcodes* — wasm does, so C does — would otherwise have
to call out of the sandbox to round a number.

They operate at their operand's width and return it: `f32` in, `f32`
back, rounded through single precision.

Three of them have a definition worth stating, because the obvious C
function is the wrong one:

- **`fnearest` is roundTiesToEven.** `fnearest 0.5` is `0.0`, `fnearest
  1.5` and `fnearest 2.5` are both `2.0`. C's `round` is
  ties-away-from-zero and would answer `1.0` and `3.0`; `nearbyint` under
  the default rounding mode is the C function that matches.
- **`fmin` and `fmax` propagate NaN.** If either operand is NaN the
  result is NaN. C's `fmin` and `fmax` return the operand that is *not*
  NaN, which silently removes the NaN from the computation. They also
  order the two zeros: `fmin -0.0, 0.0` is `-0.0` and `fmax` is `+0.0`,
  either way round.
- **The four roundings return a float**, not an integer. Rounding and
  converting are separate questions, and `conv` is the one that can
  abort. A NaN, an infinity and a signed zero pass through unchanged.

`fsqrt` of a negative operand is NaN, not an abort — the same split as
float division by zero: IEEE-754 defines an answer, so it is a value
rather than a programmer error.

A float constant that is not finite is spelled `inf`, `-inf` or `nan`:

```metals
%p = const.f64 inf
%n = const.f64 -inf
%q = const.f64 nan
```

The three conversions are deliberately separate, because they answer
three different questions and a language that needs one usually needs all
three:

- `conv` is **value-preserving**. It widens freely and aborts when a
  narrowing would lose the value. This is Gyro's `as`.
- `trunc` **keeps the low bits** of an integer and never aborts. It only
  narrows; widening is `conv`'s, which cannot lose anything. This is what
  a C cast means.
- `bitcast` **reinterprets** between two numeric types of the same width
  and never aborts: `i32`↔`u32`, `i64`↔`u64`, `i32`↔`f32`, `i64`↔`f64`.
  `bool` and `rune` are excluded, because not every bit pattern of their
  width is one of their values.

`bitcast` is what makes a signedness-per-operation source language
expressible without a second set of instructions. A wasm `i32` has no
signedness of its own — `i32.div_s` and `i32.div_u` carry it — so a
producer holds the value in an `i32` register and reads it through `u32`
for the unsigned operations. Nothing is copied and nothing is checked.

### Memory

```metals
%p = slot i32        ; a stack cell, for locals whose address is taken
%p = new %Point      ; heap cell, uninitialized
%p = global.ptr @counter ; module storage, live for the whole program
free %p
%v = load %p
store %p, %v
%q = field.ptr %p, 1 ; ptr<record> -> ptr<field 1's type>
%q = elem.ptr %p     ; ptr<array<T, N>> -> ptr<T>, the first element
%q = ptr.offset %p, %n ; ptr<T> -> the ptr<T> n elements along
%n = ptr.diff %a, %b   ; ptr<T>, ptr<T> -> i64, how many elements apart
%a = ptr.erase %p      ; ptr<T> -> anyptr; opt<ptr<T>> -> opt<anyptr>
%p = ptr.cast %a -> ptr<T> ; the type put back, as the producer's claim
```

`elem.ptr` is the array-to-pointer decay: an array's storage begins where
its first element does, so it changes what a pointer points at and not
where it points. It is separate from `ptr.offset` for exactly that
reason.

`ptr.offset` and `ptr.diff` are pointer arithmetic, and they are
**unchecked on purpose**. A bounds test would need the object's extent at
run time, which means a pointer stops being a machine word — and a
pointer being a machine word is most of what compiling C in rather than
sandboxing it buys. So the format says what C says: the result of
`ptr.offset` is a pointer only while it stays inside the object `%p`
points into, or one element past its end, and `ptr.diff` is defined only
for two pointers into one object. A producer that leaves the object has
written an undefined program.

The evaluator holds a producer to that — it knows which object a pointer
belongs to and aborts on a load through one that has left it — while the
native backends compute the address. That is a deliberate asymmetry, and
it is the same one `uninitVal` already has: the tiers agree on every
defined program, and the evaluator is the tier that can *see* an
undefined one.

`ptr.erase` and `ptr.cast` are the two halves of an erased pointer.
Erasing takes the target's type off and cannot fail; casting puts one
back and is the producer's **claim** that the object at that address is a
`T`. They are two instructions rather than one for the reason `conv`,
`trunc` and `bitcast` are three: erasing is total and restoring is an
assertion, and an instruction that answered both questions would let one
be mistaken for the other. Optionality is carried through rather than
dropped and rebuilt, because under the niche `opt<ptr<T>>` and
`opt<anyptr>` are the same machine word and making a producer branch to
preserve a null would be a lie about what the instruction does.

An `anyptr` supports exactly two things: comparison, and having a type
put back. There is no load, no store, no `field.ptr`, no `ptr.offset` and
no `ptr.diff`, because none of them has an answer without the target's
type — which is C's rule for `void *` already, so a C front end loses
nothing it had. `sizeof(void *)` still answers 8.

The claim is checked the way `ptr.offset`'s is. The native backends move
a register; the evaluator still knows what is really in the cell and
aborts when a cast names something else. Reading one object's bytes as
another type is undefined in C too, `char *` aliasing included — the
evaluator is the tier that can see it happening, and a cast it waves
through is one whose mistake it could not observe.

This is not a second addressing model. Linear memory is still the flat,
bounds-checked, `u32`-addressed region it was, and a `memory` and a
`ptr<T>` still never meet. An `anyptr` is not one either: erasing a
target's type is not the same as having no address space, so an `anyptr`
cannot cross a runtime boundary any more than a `ptr<T>` can.

### Linear memory

```metals
memory @heap 17, 1024      ; initial pages, and an optional maximum
data @heap, 1024, "hello"  ; written before any function runs

%v = mem.load.i32 @heap, %addr   ; i8 u8 i16 u16 i32 u32 i64 u64 f32 f64
mem.store.i32 @heap, %addr, %v
%n = mem.size @heap              ; -> u32, pages
%n = mem.grow @heap, %pages      ; -> i32, pages before, or -1
mem.copy @heap, %to, %from, %len
mem.fill @heap, %to, %byte, %len ; %byte is a u8
```

A linear memory is a flat, byte-addressed, bounds-checked region of
storage private to the module that declares it. It is sized in 64 KiB
pages, starts zeroed, and grows only through `mem.grow`.

It exists for producers whose source language has a memory model METALS'
typed pointers cannot express — C's, where a pointer is an integer,
objects overlap, `&local` outlives its type, and a struct is an offset
table over bytes. Every one of those (pointer arithmetic, unions,
bitfields, varargs, packed layout, unaligned access) is one flat region
and offsets into it, which is why this is one declaration and six
instructions rather than eight new type rules.

The rules that make it safe to add:

- **An address is a `u32`, not a `ptr<T>`.** So is every length and page
  count. That is what keeps the boundary rules intact: a linear address
  cannot appear in a crossing signature, because it is not a pointer and
  carries no claim about what it points at. A module that uses linear
  memory still exports the same copied values every other module does.
- **`ptr<T>` is untouched** — typed, non-null, non-crossing. `slot`,
  `new`, `field.ptr`, `load` and `store` mean exactly what they meant.
  The two memory models coexist and never mix; a producer uses one.
- **Every access is bounds-checked** against the memory's current size,
  and an access that runs past it aborts. A producer that is internally
  unsafe still cannot reach storage outside the region it declared. This
  is the reason to compile an unsafe language *into* METALS rather than
  link it: the sandbox is structural, not a promise.
- **Little-endian, unconditionally.** A byte-addressed memory whose
  meaning depended on the host would make a program's output a property
  of the machine that ran it.
- **A load's suffix names its result type and its width together.**
  `mem.load.u8` reads one byte and zero-extends; `mem.load.i8` reads one
  byte and sign-extends. A store requires its value's type to be exactly
  the suffix's, so a producer narrowing a wider register says `trunc`
  rather than letting the width of a store decide silently.
- **Memories are module-local.** Linking qualifies a memory's name with
  its module's, so two producers linked together get two memories rather
  than one heap they can reach across. That is the conservative reading,
  and the one the boundary rule already implies.

`mem.grow` answers with the page count *before* the call, or `-1` when it
cannot grow — a value the program handles, not an abort, because running
out of memory is not a bug in the program's own arithmetic.

### Records, enums, tuples, arrays

```metals
%r = record %Point(%x, %y)        ; every field, in declared order
%v = field.get %r, 0
field.set %r, 0, %v               ; mutates the register's value in place
%e = variant %Shape, 1(%w, %h)    ; variant index, payload
%t = variant.tag %e               ; -> i32
%v = variant.field %e, 1, 0       ; variant index, payload index
%t = tuple(%a, %b)
%v = tuple.get %t, 0
%a = array<i32>(%x, %y, %z)
%s = slice.of %a                  ; borrow, shares storage
%v = index.get %s, %i             ; bounds-checked, aborts out of range
index.set %s, %i, %v
%n = len %s                       ; -> usize
%r = str.concat %a, %b
```

Derived printing is not an IR concept: the front end generates an
ordinary `-> str` function per type and calls it.

### Optionals

```metals
%o = opt.wrap %v            ; T -> opt<T>; optionals do not nest
%v = opt.unwrap %o          ; opt<T> -> T, aborts on null
```

A null test is `eq`/`ne` against a `const.null` of the same optional
type. A front end whose checker proves a value non-null (Gyro's
narrowing) still emits `opt.unwrap`; the abort is that guarantee made
explicit, in the same trapping style as `idiv` and `index.get`.

**`opt<ptr<T>>`, `opt<code(...)>` and `opt<anyptr>` are one word.** Every
`ptr<T>` in a verified module addresses an object METALS produced, every
`code(...)` is the address of a function METALS emitted, and an `anyptr`
is one of those two with its type taken away rather than a number a
producer invented — so in each case the null address is a pattern no live
value has and the format reserves it: the optional is laid out inside its
payload rather than beside it, is one machine word in storage and in a
register, and travels in a register like any other address. `const.null`
is the zeroed store it already was, a wrap is a move, an unwrap is a null
test, and equality was always a word compare.

That niche is those three types'. A `handle`, a `chan` and a `future` are
the runtime's values, and which bit patterns they may hold is the
runtime's business rather than the format's. Everything else keeps the
flag-and-payload layout.

The reason to have it is C. A C pointer is `opt<ptr<T>>`, a C function
pointer is `opt<code(...)>` and a C `void *` is `opt<anyptr>` — see the C
front end below — and with a flag beside the payload `sizeof(char *)`
answers 16, which is self-consistent and is not C's ABI.

### Calls

```metals
%r = call @f(%a, %b)
%f = fn.ref @f
%c = closure @lifted, %env    ; binds the env as @lifted's first parameter;
                              ; %c's type drops that parameter. %env is a
                              ; ptr<record>, never a record value: the
                              ; closure outlives the frame that built it,
                              ; and a function value has one word to carry
                              ; its environment in.
%p = code.ref @f              ; the address alone, as a one-word code(...)
%r = call.indirect %f(%a)     ; through either a fn value or a code pointer
return %v                     ; plain `return` for a void function
abort "index out of range"
```

Closures are a lowering pattern, not magic: the front end lifts the body
to a top-level function taking an environment record, and `closure`
partially applies it.

**A `fn` and a `code` are two types because they are two sizes.** A `fn`
is `{ code, env }` and needs the second word, because a closure has to
keep what it captured somewhere; `call.indirect` through one tests the
environment word and passes it when it is there. A `code` has nothing to
capture, so it is the address alone: one word, one call, no test. That is
what C's function pointer is — `sizeof(int (*)(int))` answers 8, a struct
of callbacks is the bytes C lays out, and a table of them has C's stride
— and it is also what a producer wants whenever a callee is chosen at run
time without anything travelling beside it. `code.ref` refuses an `async`
function: an async function is started rather than called, so its address
is not something `call.indirect` could reach.

### Control flow

```metals
if %c {
  ...
} else {
  ...
}

block $done {
  loop $top {
    br_if %stop, $done
    br $top
  }
}

switch %tag {
  case 0 { ... }
  case 1 { ... }
  default { ... }
}
```

`br` and `br_if` target an enclosing label: a `block` label exits past
its end, a `loop` label re-enters its top. No other jumps exist.

### Concurrency

```metals
%ch = chan.new<i32> %cap    ; omit %cap for unbuffered
send %ch, %v                ; aborts on a closed channel
%t = recv %ch               ; -> tuple<T, bool>: (element, open)
chan.close %ch
%v = await %fut             ; only inside an async fn

select {
  recv %v = %ch { ... }     ; %v is bound inside the arm
  send %ch2, %x { ... }
  default { ... }
}
```

`spawn`, mutexes and everything else library-shaped are imports from a
runtime module, called like any function.

### any

```metals
%d = any.wrap %v            ; any copyable value -> any
%b = any.is %d -> i32       ; -> bool; the test that does not trap
%v = any.unwrap %d -> i32   ; checked; aborts on the wrong shape
%r = any.call %d(%a, %b)    ; -> any
%v = any.get %d, "name"     ; -> any
any.set %d, "name", %v
```

`any.is` is what makes an `any` *receivable*. Every other way to look
inside one traps on a mismatch, so a producer handed an `any` by a module
it knows nothing about would have no way to ask before committing.

An `any` is two words: a descriptor and a payload. Descriptors are static
data, one per distinct type a program wraps or tests, so identity is
structural equality and `any.is` is a pointer comparison. A value of one
word rides in the payload, which is why wrapping a scalar allocates
nothing; anything wider is heap storage the payload points at, and so is
every record whatever its width, so that `any.set` through one reference
is visible through the rest. Wrapping an `any` yields the same `any`:
they do not nest, the way optionals do not.

`any.get` and `any.set` reach the fields a record *declares*, by name.
`any.call` applies a wrapped function: the arguments arrive as `any` and
the result leaves as one, and the wrapped signature decides how each is
converted, so a caller that knows nothing about the callee still hands it
values of the types it declared.

Nothing obliges a producer to use any of this. A front end that knows its
types should declare them, and then a call from another language is an
ordinary call — no descriptor, no box, no test.

## Boundary rules

**A boundary is between two runtimes, not between two languages.** The
schemes that name producers compiling *into* METALS — `gyro:`, `lisp:`,
`cc:`, `wasm:`, `metals:` — are one runtime, because they literally
become one:
the linker makes them a single module, the verifier has checked all of
it, and it runs on one heap with one allocator. A call between two of
them is an ordinary call and may carry anything an ordinary call carries,
pointers included.

A boundary is where that stops being true, and there are two ways it can:
`c:`, a native library nothing verified, whose idea of what to do with an
address it is handed is its own; and another language's runtime embedded
in the process — `js:`, `py:` — with its own heap, its own lifetimes, and
in all likelihood a collector that moves things. An unrecognized scheme
is read as one of those, which is the conservative way round.

A signature is *boundary-crossing* when it is imported from a module in
another runtime, or exported to one. The verifier enforces the first and
the linker the second, because a module can see its own imports and only
the linker sees who imports what it exports. On every parameter and
result of such a signature:

- allowed: primitives, `str`, `opt`, `tuple`, `array`, `slice`, records
  and enums, `fn` (the runtime shims it), `any`, `handle`;
- forbidden: `ptr`, `chan`, `future`, and any type containing one.

"Containing one" is walked rather than assumed, records and enums
included: a struct crossing by value is its fields crossing by value, and
one of them holding a `ptr` is the shared mutable state the rule exists
to refuse.

Crossing copies: the caller's runtime converts arguments into the
callee's world and the result back. `slice` crosses as a copy of its
contents. Shared mutable state across runtimes does not exist; a foreign
object you must keep alive is a `handle`, and its operations are imported
functions taking that handle.

## Verification

`metals.Verify` checks a module with no knowledge of its producer:

- every named type, function, global, memory and label resolves;
- every register: assigned before read on all paths, one type forever;
- every instruction's operand and result types, per the rules above;
- every linear-memory address, length and page count is a `u32`, every
  access width is one a memory can hold, and every data segment fits
  inside the pages its memory starts with;
- `await` only inside `async fn`; `select`/`send`/`recv` only on channel
  types; `return` matches the declared result;
- boundary rules on every signature imported out of another runtime. An
  export is the linker's, which is the only side that knows who imports
  it.

## Linking

Every consumer takes one module, so a program of several becomes one
before any of them sees it. `metals.Link` takes the modules in dependency
order, the root last, and returns that module.

- **Signatures are matched.** An importer declares what it expects and
  the exporter has actual ones; only the linker sees both. A name that is
  missing, or declared but not exported, or exported with a different
  signature, is an error here. This is the check the format had nowhere
  to make.
- **The export side of the boundary is checked.** When the importer and
  the exporter are in different runtimes, the exporter's actual signature
  has to obey the rules above — and only here is that a question with two
  sides. It catches what the importer's own verification cannot: a record
  redeclared across the boundary whose real shape holds a `ptr`.
- **Names are qualified.** Everything a non-root module owns —
  functions, named types, globals, memories — is prefixed with its module name, so
  two producers may both export a `fib` and both declare a `Point`. The
  root keeps its names, which is what leaves `@main` as `@main`.
- **Imported types are resolved and matched.** A type declared inside an
  `import` is not a second type: it takes the exporter's qualified name,
  so a chain of importers lands on the one module that declares the
  shape and the linked program has one declaration of it. The importer's
  declaration is compared against the exporter's first, member for
  member in order, and a disagreement is an error here — that is the
  structural matching the format had nowhere to do. The exporter has to
  be a module in the program: a native library agreed to nothing.
- **A signature that names a type has to mean the same type.** Within one
  runtime, two modules whose signatures both say `%Rect` are only
  agreeing if the name lands on the same declaration — otherwise the
  match was a spelling. Importing the type is how the importer says so,
  and not importing it is an error. Across a boundary the question does
  not arise: the value is copied into the other side's own type, and
  what may cross at all is the rule above.
- **Initializers become calls.** Each module's `init`, if it names one,
  is prepended to `@main` in dependency order. This is why a program of
  one module is linked too: a producer names its initializer and never
  arranges the call, so there has to be one pass that does, and it has to
  be the pass that can see whether there is anything else to run first.
  The linked module carries no `init` of its own afterwards.
- **Resolved imports disappear.** A call to an imported function becomes
  a direct call to the qualified symbol. An import of something that is
  not a module in the program — `gyro:rt` and the runtime modules
  generally — survives, deduplicated.

## The C front end

`internal/cc` reads C and emits METALS directly. It is what a Gyro
program reaches when it imports a `.c` file; `internal/wasm` still owns a
`.c` that *is* the program, because that is where the bundled libc is
needed and where a sandbox is the point. The same file through both
routes must agree on what it computes.

It has its own preprocessor — Prosser's algorithm, with hidesets on the
tokens — because a header is how C says what a library's types are, and
`#include` is the only way to reach one. Without it an imported `.c`
could only be a file written for this compiler; with it, an imported
`.c` is a C file.

The mapping, where it is not obvious:

- `int` is `i32`, `long` and `long long` are `i64`, `char` is `i8` and
  `unsigned char` is `u8` — LP64's widths, which is what Linux x86-64
  means by C and what the runtime under all three tiers is built as.
- **A pointer is `opt<ptr<T>>`.** C's is nullable and METALS' is not, so
  every dereference is an `opt.unwrap` and a null one aborts where C
  leaves it undefined. It costs nothing in storage: `opt<ptr<T>>` is laid
  out inside the pointer, with the null address as the absent case, so
  `sizeof(char *)` answers 8 and a struct of pointers is the bytes C lays
  out. See the niche rule under Optionals.
- **An array is storage, not a value.** `int a[5]` is a `slot
  array<i32, 5>`; naming it anywhere but in a `sizeof` or a declaration
  is an `elem.ptr`, which is C's decay. `a[i]` is lowered as `*(a + i)`
  because that is what C defines it to be — which is also why `i[a]`
  works and why `&a[i]` needs no separate rule.
- **An enumeration is `int`**, which is what it is on this target, and
  its constants are resolved during lowering rather than during the
  parse — a local of the same name shadows one, and only the scope
  chain knows that.
- **Every local is a `slot`.** A C local's address may be taken, and
  which ones escape is an analysis the backends are better placed to do.
- **Arithmetic wraps**: `wadd` and not `iadd`. Signed overflow is
  undefined in C, so either reading would do — and the wasm bridge
  already translates the same program to the wrapping mnemonics.
- A C cast that narrows is `trunc`, one that widens is `conv`, and
  widening a signed value into a wider unsigned type is `conv` then
  `trunc`, which is C's sign-extend-then-reinterpret written out.
- A function without `static` is exported, which is C's own rule for
  what leaves a translation unit.

- **A string literal is module storage.** It is an `array<i8, N>` global
  carrying its own bytes as an image, terminator included, interned by
  content — so two identical literals are one array and comparing their
  addresses answers true. Naming one is a `global.ptr`, and everything
  else follows from the decay rule: `sizeof "abc"` is 4, `"abc"[1]` is
  an index into storage, and `char buf[] = "abc"` copies rather than
  points, because that is the one place C says a literal is not an
  address.
- **A file-scope initializer is one of two things.** One whose value is
  already known becomes the global's byte image and costs nothing at run
  time; one that names an address becomes stores in the module's `init`,
  because a byte image cannot hold a relocation. C's own division
  between an arithmetic constant expression and an address constant is
  the same division.
- **The null pointer constant is the integer 0**, cast or not, which is
  what makes `#define NULL ((void *)0)` mean what every header expects.
  It lowers to `const.null` of the type the expression is becoming, and
  the cast is seen through rather than performed: a null of the
  destination's type needs no trip through another one.
- **Varargs are an array the caller builds**, and METALS has no variadic
  call. A variadic function takes two parameters beyond the ones it
  declared: `void **` to the addresses of the arguments after the last
  named one, and a `long` count. Each of those arguments is spilled into
  a cell of its own and reached by an erased address, because one array
  cannot hold an `int`, a `double` and a `char *` directly — a pointer
  and a number do not share a slot in this IR, and addresses are all one
  type. `va_arg` casts a slot to the type it was asked for, so the cast
  is checked, and it tests the count first, so reading one argument too
  many aborts with a message on every tier rather than reading whatever
  the stack held. `<stdarg.h>` is bundled, `va_list` is an ordinary C
  struct in it, and only `va_start` and `va_arg` are builtins: doing
  nothing and copying a struct are both things C can already say.
- **A `void *` is `opt<anyptr>`**, an address with the target's type
  erased. C forbids `*p`, `p + 1`, `p[i]` and `sizeof *p` on one already,
  so the erasure costs the source language nothing it had, and
  `sizeof(void *)` still answers 8 — a struct with a `void *` in it is
  the bytes C lays out. A cast between two pointer types is `ptr.erase`
  then `ptr.cast`, which is what makes it a claim rather than a
  coincidence; the conversion to `void *` needs only the first half and
  the conversion back needs only the second, which is why C requires a
  cast in one direction and not the other. A pointer to a function does
  not join in: `void *` addresses storage, and converting one to a code
  pointer is a POSIX guarantee about a machine rather than a C one about
  a program.
- **A tag no file defines is `type %Tag = opaque`.** That is C's
  incomplete type and the way a library header hands out a handle: a
  caller may hold a `struct SDL_Window *`, pass it, store it, compare it
  and hand it back, and may not dereference one, reach a member, step
  one, subtract two or ask what one is the size of. Each of those six is
  refused here, with a position in the C source, and refused again by the
  verifier if the front end ever lets one slip. A member of an incomplete
  type is refused where it is written, which is C's rule and also what
  rules out a struct holding itself without the layout table having to
  notice the cycle.

  A conversion between two pointers to different types now needs a cast,
  which C's own constraints say and most C compilers only warn about.
  Through `void *` nothing is written in either direction, which is C's
  exception and the reason the type is there. Without that, two opaque
  handles would assign to each other freely and the distinction the
  header exists to keep would be decorative.

- **A `union` is METALS' union**, and a C tag names one or the other in
  one namespace, so `struct P` beside `union P` is an error rather than
  two types. A braced initializer writes the first member and only the
  first, which is all one storage has room for and what C picks. The
  read every C event loop is built on — `event.kind` while `event.key`
  is what was written — is the common initial sequence, and it is the
  one cross-member read a program is entitled to.

- **A run of bit-fields is one member of bytes**, and each of C's
  members remembers which bits of it are its own. METALS has no member
  measured in bits on purpose — a format whose records were bit-addressed
  would be one every consumer had to shift — so the bit cursor is the
  front end's, and every size and alignment it walks past comes from
  `metals.Layout`. Reading a bit-field is a load, a shift and a mask;
  writing one is a read-modify-write of the bytes it shares. `&`, and
  `sizeof` of one, are refused, which is C's rule and the reason it can
  be packed at all.

  The packing is x86-64's, so the sizes are the ones a C compiler
  answers: a bit-field starts at the next free bit and may not cross a
  boundary of its own declared type's size, an unnamed `:0` moves the
  cursor to the next such boundary and reserves nothing, and the
  struct's alignment takes in every bit-field's declared type — which is
  what `record align N` carries. In a union every member begins where
  the union does, so its bit-fields all start at bit zero and share one
  run rather than packing. A local holding one anywhere is zeroed before
  its first write, because writing a bit-field reads the bytes it shares
  and there is no partial store; the native backends zero a slot anyway,
  so this is what makes the evaluator agree rather than refuse a program
  C allows.

- **A `switch` is nested blocks**, one per group of case labels, in
  source order and innermost first, with the dispatch `switch` at the
  bottom. `br` to a group's label lands where that group begins, and
  running off the end of a group lands where the next one begins — so
  fallthrough is what happens when a group does not branch, which is
  exactly C's rule, and a `default` in the middle is just a group the
  default arm branches to. This needs no relooper: C's switch is a
  multiway branch into a linear body, which nested blocks express.

- **A `goto` is a branch out of a block or back to the top of a loop.**
  C's labels already nest, and the two directions want two different
  wrappers: a label jumped to from *earlier* gets a `block` around
  everything before it in its statement list, so branching out of that
  block lands where the label is; one jumped to from *later* gets a
  `loop` around everything from it onwards, so branching to that loop
  restarts it — landing in the same place — and running off the end
  falls out. A label jumped to from both sides gets both.

  The wrappers are peeled one label at a time, outermost first, with a
  forward one always outside a backward one in the same region. That is
  what makes `again: ... goto out; ... goto again; out:` work: the loop
  ends where the block does, and falling off it leaves the block, which
  lands at `out`. A function with any label reserves its storage at the
  top rather than where it is written, because branching out of a block
  discards everything assigned inside it and the register holding a
  local's address must not be one of those things.

  Two shapes are refused by name rather than mistranslated, and both are
  where a control-flow graph would be needed: a **jump into a block** — a
  label in one statement list reached from another — and a **backward
  jump reaching past a later forward label**, where the loop would have
  to cross out of the block. What is left covers what `goto` is actually
  written for in C: a forward jump to a cleanup label, a backward jump
  that is a loop, and breaking out of nested loops.
- **A `static` local is module storage under a qualified name.** The
  name is block-scoped and the storage is not, so the initializer is a
  global's rather than a statement — which is also why C only lets it be
  a constant expression. The symbol is `function$name`, and `$` is not
  in C's identifier space, so nothing a translation unit declares can
  collide with one.

- **A program is several files, and they become one module.** C files
  never include one another: a header declares, a `.c` defines, and
  nothing in the source says which `.c` defines what — which is why a C
  project's build system exists mostly to hold that list. The import
  names an entry file, and the list is recovered from it: whatever the
  entry needs and does not define is looked for in the `.c` files beside
  it, each file that supplies one is pulled in, and the question is asked
  again until nothing is missing. The search is that one directory and no
  deeper; a library laid out in subdirectories is imported through a file
  in each. A file nothing needs is never lowered, and one that does not
  even parse is not an error until something needs it.

  They become **one** METALS module, because a linkage unit is what a
  METALS module is. Two files that call each other are ordinary C and
  have no dependency order, so as separate modules they would be an
  import cycle; merged, they are two calls. The merge is also what makes
  the call checked — a C linker matches an undefined symbol against a
  defined one by name and compares nothing, so a prototype that
  disagrees with its definition links and then misbehaves, while two
  declarations of one name have to agree to get this far. `static` is
  the overlay that model needs: an internal name becomes `file$name`, so
  two files may each keep one, and a shared header's struct is one
  record when the two declarations agree and an error when they do not.
- **A pointer to a function is `opt<code(...)>`**, not `opt<ptr<T>>`:
  there is no object at the other end to load from. A function's name
  used as a value is a `code.ref` that decays to that pointer, `*fp` is
  the designator decaying straight back, and a call through one unwraps
  the optional first — so calling a null callback aborts with a message
  where C would jump to address zero.
- **The declarator is read as C reads it**, which is as the expression
  that would use what it declares: `int (*fp)(int)` says `(*fp)(int)` is
  an int. That is why a parenthesised declarator is read twice — the
  suffix after the closing paren binds tighter than the `*` inside it,
  and has not been seen when the inside is first reached — and it is what
  makes `int (*table[3])(int, int)`, `int (*chooser(int))(int, int)` and
  the abstract `int (*)(int)` in a cast all parse. An abstract declarator
  is the same declarator with the identifier left out.

What it does not do yet, refused by name rather than mistranslated: the
two `goto` shapes above, which is where the relooper LLVM does for the
bridge would have to be written. **Type punning** compiles and is a
program only two tiers can run: reading one object's bytes as another
type is undefined in C, `char *` aliasing included, and the evaluator
holds a producer to it. A callback does not cross into Gyro yet, because
Gyro has no spelling for a code pointer; nor does a `void *`, for the
same reason; nor does a variadic function, because a `...` says nothing
about what a call passes and there is nothing for the far side to check.
A `union` does not cross either — Gyro has no declaration for one. An
export the header cannot spell is **left out** rather than made an
error, so one such helper does not make the rest of a library
unimportable, and naming a skipped one is reported where it is imported.

A struct does cross. The generated header declares it as a Gyro struct
with the same members in the same order, and lowering imports the
`%Named` this module declares rather than rebuilding one from that text
— so a struct Gyro builds is one C reads a member of, and a pointer to a
Gyro local is one C writes through. An incomplete type arrives as a
fieldless struct, which is Gyro's own spelling for a handle it may keep
and not look inside.

## Still open

- What METALS expands to. The name predates the design.
- Structural matching of record shapes that *cross a boundary*. Within
  one runtime an importer says which type it means and the linker
  compares the declarations; across a boundary the value is copied, and
  nothing yet checks that two same-named records on either side have the
  same shape.
- The binary encoding.
- How foreign runtimes embed: a JS engine and the Gyro runtime in one
  process is a runtime problem the IR only gestures at via `any` and
  `handle`.
- Effect rows: Gyro parses them, nothing checks them; whether they
  belong in METALS signatures is undecided.
- `syntaxfn`, the METALS-facing parser-extension feature from the book.
- Whether two modules may ever *share* a linear memory. Linking gives
  each its own today, which is right for a sandbox and wrong for two C
  translation units that expect one heap. The wasm bridge does not have
  to answer this, because wasm-ld has already linked before anything
  arrives, and `internal/cc` does not either — it uses `ptr<T>` and the
  ordinary heap, which is the whole reason it exists.
- Whether any other optional deserves a niche. `opt<ptr<T>>` has one and
  nothing else does: a `handle` and a `chan` are the runtime's values,
  and what they may hold is the runtime's business rather than the
  format's. `opt<bool>` and a payload-free enum have obvious spare
  patterns and no one has needed them.
- How several C translation units become one program. `internal/cc`
  compiles one file and refuses a call to a function it cannot see. The
  preprocessor closed half of it — a header shared between files is a
  header now — and what is left is linkage: an `extern` declaration
  resolved against another `cc:` module rather than refused.
- A memory declared by one module and imported by another, which is how
  wasm spells the above and which the bridge refuses rather than guesses.
