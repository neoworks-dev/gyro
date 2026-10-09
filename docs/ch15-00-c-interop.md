# 15. C interoperability

There are two ways to reach C, and they are not variations on one theme.

| route | what it is | when |
| --- | --- | --- |
| `@extern` | link a symbol from a native library | the source is not yours: `libm`, SDL2 |
| `import "./file.c"` | compile the C **into your program** | the source is yours, or vendored |

The first marshals values across a boundary. The second has no boundary to
cross: the C is read by Gyro's own C front end, becomes METALS, and is
linked into the same module as your Gyro code — so a call is an ordinary
call and a pointer is an ordinary pointer.

## Foreign functions with `@extern`

A declaration with no body is a foreign function — that is already how the
standard library's intrinsics are written. `@extern` says which foreign
world one belongs to, and the library it is linked from:

```gyro
@extern("m")
export safe fn sqrt(x: f64): f64 !{}

@extern("SDL2")
export safe fn SDL_Init(flags: u32): i32 !{ io }

@extern("SDL2", symbol = "SDL_Delay")
export safe fn delay(millis: u32) !{ io }
```

An `@extern` function is **unsafe to call** unless the declaration says
otherwise: the compiler can check the signature and nothing about what the
C does. Writing `safe` before `fn` vouches for it, and then any function
may call it ([chapter 4.6](ch04-06-unsafe.md)):

```gyro
@extern("m")
safe fn sqrt(x: f64): f64 !{}          // no f64 misuses it

@extern("c")
fn strlen(text: string): usize !{}     // callable from an unsafe fn or a safe fn
```

The string is the library, spelled as the linker wants it without the `lib`
prefix — `"m"` is `-lm`, `"SDL2"` is `-lSDL2`. `"c"` names libc, which is
always linked anyway. The symbol is the declaration's own name unless
`symbol =` gives another, which is how a C name that is not a legal Gyro
identifier gets in.

A declaration under `@extern` **must have no body** and **must state its
effect row**: there is no source to read, so an unstated row would be
assumed to do everything ([chapter 12](ch12-00-effect-rows.md)).

### What can cross

A linked library is **not a second heap**. It runs on this thread, in this
address space, over the same allocator the Gyro runtime itself calls. What
separates it from Gyro is *representation* — C has real widths where a
METALS register is a qword — and converting that is all the boundary does.

So an address crosses as itself, and what does not cross is a Gyro runtime
object, which C has no spelling for at any width.

| Gyro | C | how |
| --- | --- | --- |
| `i8`..`u64`, `usize`, `f32`, `f64`, `bool`, `rune` | the same width | by value |
| `string` | `const char *` | copied and NUL-terminated for the call |
| `T[]` | `const T *` | the slice's own elements, borrowed for the call |
| a struct whose fields all cross | the same struct | by value; the same bytes on both sides |
| a fieldless `@extern` struct | `void *` | an opaque handle, by identity |
| `T?` of a handle | a pointer that may be `NULL` | null is `NULL` |
| `*T` | `T *` | the address itself — an out-parameter |
| `anyptr` | `void *` | the address itself, target forgotten |
| `code(A, B): C` | `C (*)(A, B)` | the address of a function, either direction |
| `void` | `void` | |

Rejected, with a diagnostic naming the type: `chan<T>`, futures, `fn`
values, `any`, tuples, enums, and `T?` of anything but a handle — at any
depth, so a `*chan<i32>` is refused for the same reason a `chan<i32>` is.
Several results are rejected too — a C function returns one value, and
`(T, error?)` is Gyro's shape, not C's.

A `fn` is refused where a `code` is accepted because the two are different
sizes: a `fn` is code *and* the environment it captured, which is two
words, and a C function pointer has room for one. `&f` on a declared
function is the code pointer, and the diagnostic says so.

A returned `string` is a **copy** of what the pointer addressed, because the
bytes belong to the library and it may reuse them on the next call. A
returned null pointer becomes `""`: a Gyro string is never absent.

### Out-parameters and callbacks

```gyro
@extern("m")
safe fn frexp(value: f64, exponent: *i32): f64 !{}

@extern("c")
safe fn qsort(base: anyptr, count: usize, size: usize, compare: code(anyptr, anyptr): i32) !{}

safe fn ascending(a: anyptr, b: anyptr): i32 {
  var left = cast<i32>(a)
  var right = cast<i32>(b)
  if *left < *right { return 0 - 1 }
  if *left > *right { return 1 }
  return 0
}

var exponent: i32 = 0
var fraction = frexp(1024.0, &exponent)   // 0.5, and exponent is 11

var xs = [5, 3, 9, 1, 7]
qsort(&xs[0] as anyptr, 5, 4, &ascending) // C's loop, this program's order
```

`&xs[0]` is the element's own address, and the stride C walks is the one
Gyro laid out — an `i32` is four bytes of storage
([chapter 4.1](ch04-01-manual-memory.md)).

**Nothing checks what the library does with an address**, which is the same
thing that is true of C. A pointer handed out lives as long as whatever it
points into, and a library that keeps one keeps it past the call.

### Structs cross as bytes

A struct crosses by value, in both directions, because Gyro lays one out the
way C does — same widths, same alignment, same padding. A Gyro struct and a
C struct of the same fields **are the same bytes**, and nothing is
converted:

```gyro
struct Div {
  quot: i32
  rem: i32
}

@extern("c")
fn div(numerator: i32, denominator: i32): Div !{}
```

What a struct may *hold* is narrower than what an argument may be, because a
field is bytes C reads out of the struct rather than a value converted for
the call. Scalars, fixed arrays and other such structs are members; a
`string`, a slice and a nullable are not, and the diagnostic names the
field's type rather than the struct's.

The claim that the two languages agree on the bytes is checked on every
build: the generated shim declares the struct with its real C members and a
`_Static_assert` per field offset ([appendix E](appendix-e-native-backend.md)).

### Opaque handles

An opaque handle is a fieldless struct marked `@extern`. It has no literal,
no fields and no operations of its own — it is a `void *` you can only pass
back to the library that gave it to you:

```gyro
@extern("SDL2")
export struct Window {}

@extern("SDL2")
export safe fn SDL_CreateWindow(
  title: string, x: i32, y: i32, w: i32, h: i32, flags: u32,
): Window? !{ io }
```

### What it costs

A program that calls a foreign function **runs only on the native tiers**.
The METALS evaluator is Go with no C in it, so it cannot make the call, and
it refuses the program rather than inventing an answer.

`examples/triangle.gyro` is the reference: a Gouraud-shaded triangle
rasterized in pure Gyro and put in an SDL window.

## Importing a `.c` file

```gyro
import safe { add, divmod } from "./mathlib.c"
```

The loader files `mathlib.c` as a foreign module the way it does a Gyro one:
Gyro's own C front end reads the C — its own preprocessor included, so
`#include` works and a header is a header — and emits METALS with C's
declared types. `struct Point *` arrives as a pointer to a `%Point`, not as
an integer.

A function imported from a `.c` file is **unsafe to call** unless the import
vouches for it, as `safe` does above: the C behind it can corrupt memory
even though its signature is checked. One word on the import line keeps the
trust visible without marking every call, and it covers every name the line
binds ([chapter 4.6](ch04-06-unsafe.md)). Without it a call needs an
`unsafe fn` or a `safe fn` around it. `import safe * as lib from "./x.c"`
vouches for the namespace, and `safe` on an import of a Gyro module is an
error, since a Gyro function already says for itself whether it is unsafe.
The types a C file declares need no vouching.

Because there is no boundary here, the restrictions above do not apply.
**Pointers cross**, so out-parameters work:

```gyro
import { alloc, free, heap } from "mem"
import safe { divmod } from "./mathlib.c"

var whole = alloc<i32>(heap())
var rest = alloc<i32>(heap())
defer free(heap(), whole)
defer free(heap(), rest)
divmod(47, 10, whole, rest)
println(*whole)
```

A C struct crosses as a Gyro struct — there is one record in the linked
program, not two that agree — and an incomplete type (`struct Session;`)
arrives as a fieldless struct, which is Gyro's spelling for a handle you may
keep and not look inside.

A C pointer is nullable where Gyro's is not, so dereferencing one that is
null **aborts with a message** where C leaves it undefined.

A C `union` crosses as a Gyro `union` — same members, same storage, one
declaration ([chapter 5.1](ch05-01-structs.md)) — which is what `union` is
in the language for. Reading one of its members needs an unsafe context,
like any union. A C `enum` does not: Gyro's is a tagged sum and the
two are different things.

An export the generated header cannot spell is left out rather than made an
error, so one unspellable helper does not make a library unimportable;
naming a skipped one is an error at the import. A variadic C function is the
shape that cannot cross today ([appendix G](appendix-g-known-gaps.md)).

## `anyptr` — C's `void *`

`anyptr` is an address whose target's type has been forgotten. It is the
only pointer in Gyro that is not a pointer *to* something, so it has none of
a pointer's operations: nothing to dereference, nothing to step, nothing to
order, nothing to read. What it has is identity, and `as` in one direction:

```gyro
import { cast } from "unsafe"
import safe { Cell, keep, held } from "./store.c"

safe fn value_at(address: anyptr): i32 {
  return cast<Cell>(address).value   // remembering: this program's claim
}

var c = Cell{ value: 7 }
keep(0, &c as anyptr)          // forgetting: always true, never fails

var back = held(0)             // C's `void *` arrives as anyptr?
if back != null {
  println(value_at(back))
}
```

Forgetting is free and cannot be wrong, so it is an ordinary `as`.
**Remembering is a claim** about what is at the address, the same claim C's
own cast makes, and the native tiers do what C does when it is wrong. It is
`cast<T>(p)` from the `unsafe` module, an unsafe function, so a program
makes the claim inside an `unsafe fn` or a `safe fn`
([chapter 4.6](ch04-06-unsafe.md)). `p as *T` on an `anyptr` is not a
conversion in the language.

Only a pointer converts to an `anyptr`; an integer does not. A pointer made
from an integer is `ptr_from_int` in the same module, for the addresses a
program did not get from an allocation.

An `anyptr` cannot go into an `any`: an `any` promises to give the concrete
type back, and this one has thrown it away. It does cross to a foreign
function under `@extern`, because a linked library shares the address space
the address belongs to. Printing one prints the word `anyptr`.

It exists for C. Every registry, callback table and generic container in C
is written over `void *`, and until there was a spelling for one, none of
those functions could be imported at all.

## `code(..)` — C's function pointer

`code(A, B): C` is the address of a function and nothing beside it. `&f` on
a declared function is one:

```gyro
import { count_kept, fold } from "./filter.c"

fn big(v: i32): i32 {
  if v > 5 { return 1 }
  return 0
}

fn sum(acc: i32, v: i32): i32 { return acc + v }

var kept = count_kept(&big)      // C's loop, this program's decision
var total = fold(&sum, 0)
```

It is not a `fn` with a flag — [chapter 11](ch11-00-closures.md) explains
why the two are separate types, and which functions have no single address
to take.

A code pointer is called like anything else, compares by identity, and
cannot go into an `any`. Printing one prints the word `code`. It works in
both directions and by both routes: C's loop can run a Gyro function it was
handed, and a pointer C returns can be called from Gyro.

A function that allocates takes an `Allocator` as an ordinary parameter
([chapter 4.5](ch04-05-allocators.md)), so `&f` on one is an ordinary code
pointer whose type has that parameter in it. C has no way to pass an
`Allocator`, so a callback C is to call is written without one, and gets
the allocator it needs from wherever the program keeps it.

## Which route to use

Use `@extern` when the library is a `.so` you are linking against.

Use `import "./x.c"` when you have the source. It is still the stronger
route, for two reasons that have nothing to do with what crosses: the call
is **checked** — the linker compares your declaration against the C the
front end actually read, where `@extern` matches a symbol name and trusts
you — and the program **runs on all three tiers**, because the C became
METALS like everything else ([appendix D](appendix-d-metals.md)). A
program that calls `@extern` runs on the two native tiers only.
