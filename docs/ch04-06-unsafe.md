# 4.6. Unsafe code

Most of what Gyro checks, it checks for you: a slice cannot outlive its
storage, a nullable cannot be used unchecked, a leak is warned about. A
few operations cannot be checked by anything the compiler can see, because
what makes them right is a fact about the program that is not written
down: that an address holds the type it is about to be read as, that a
union holds the member it is read as, that a C function does what its
signature promises. Those operations are **unsafe**, and the language puts
them behind one boundary so that a reader can find every place a program
makes such a claim.

## Three kinds of function

A function is one of three things, and says which in front of `fn`:

```gyro
unsafe fn read_cell(p: anyptr): i32 { ... }    // unsafe to call
safe fn   first_byte(xs: u8[]): u8 { ... }     // may do unsafe things; anyone may call it
fn        double(x: i32): i32 { ... }          // may do neither
```

- An **`unsafe fn`** is unsafe to call, because it relies on something its
  caller has to uphold. Its body may use unchecked operations and call
  other unsafe functions. It may only be called from another `unsafe fn`
  or from a `safe fn`.
- A **`safe fn`** has the same freedom in its body, and nothing is asked of
  its callers: the author is vouching that no call, whatever it is handed,
  can do the wrong thing. This is where unsafe code is **contained**.
- A plain **`fn`** may do neither. Using an unchecked operation or calling
  an unsafe function in one is an error, and the diagnostic says what to
  write: `safe fn` if the function upholds what the operation needs, so
  that its callers need not know, or `unsafe fn` to leave that to them.

The two words are different claims, and both are needed. `unsafe fn` says
*dangerous to call*; `safe fn` says *uses danger inside, and is not*. A
language with one marker for both has to choose which one it means.

Containment is at the grain of a function, and there is no block form: no
`unsafe { ... }` inside a body. A function that contains an unsafe step is
a function whose contract is worth stating, and the step that needs
vouching for is usually small enough to be a function of its own.

`unsafe` is not an effect. It does not appear in a row
([chapter 12](ch12-00-effect-rows.md)) and does not travel up a call chain,
because an effect has no way to stop: if it did, a `safe fn` could not
exist. It propagates only through calls to an `unsafe fn`, and a `safe fn`
is where it ends.

`unsafe` and `safe` are **contextual**: they mean something in front of
`fn` (and in front of an import list, below) and are ordinary identifiers
everywhere else ([appendix A](appendix-a-keywords.md)). They go after
`export` and before `async`: `export unsafe fn`, `safe async fn`. A method
is marked the same way, `unsafe fn Cell::peek(self)`.

A closure literal is part of the function it is written in, so it has that
function's freedom: the author of a `safe fn` vouches for the closures
inside it as for the rest of the body. A closure is never unsafe to call
by itself, since a `fn` value carries no marker. Taking the address of an
unsafe function with `&f` needs an unsafe context for the same reason a
call does, and a pointer to a safe function is an ordinary code pointer.

## The `unsafe` module

The primitives are `unsafe fn` declarations in a bundled module called
`unsafe`, imported like anything else. Gyro has no universe scope, so they
had to come from some module, and naming it `unsafe` makes every file that
uses them findable from its imports. Every export is an `unsafe fn`.

```gyro
import { cast, offset, ptr_from_int, ptr_to_int, slice_from_raw } from "unsafe"
```

| function | what it does |
| --- | --- |
| `cast<T>(p: anyptr): *T` | puts a type back on an address that forgot it |
| `ptr_from_int<T>(address: usize): *T` | makes a pointer from an integer |
| `ptr_to_int(p: anyptr): usize` | the address a pointer holds, as an integer |
| `offset(base: anyptr, bytes: usize): anyptr` | the address `bytes` past `base` |
| `slice_from_raw<T>(p: anyptr, len: usize): T[]` | views `len` values of `T` at an address as a slice |
| `copy_bytes(to, from: anyptr, bytes: usize)` | copies bytes; the ranges may overlap |
| `raw_alloc`, `raw_free`, `raw_resize` | the process heap, which every allocator bottoms out in |
| `env_of<F>(f: F): anyptr?`, `with_env<F>(f: F, env: anyptr?): F` | read and replace a `fn` value's environment |

`cast` is the program's claim that a `T` lives at the address, the same
claim C's cast makes. It replaces `p as *T`, which no longer exists:
forgetting a type with `p as anyptr` cannot be wrong and stays an ordinary
`as`, while remembering one can, and so it is a call to an unsafe function
([chapter 15](ch15-00-c-interop.md)).

`ptr_from_int` is for an address the program did not get from an
allocation: a device register, a page table. The borrow analysis cannot see
where such a pointer points, so it treats the result as coming from outside
the program, the way it treats an `@extern` result: a function whose row
names no borrow gives the caller's analysis nothing to follow.

`slice_from_raw` is for memory that arrives as an address and a length: a
mapped GPU buffer, an SDL surface, an `mmap`. Nothing checks that the memory
holds `len` values of `T` or that it outlives the slice, so the call is the
program's claim, and the result is treated as coming from outside the
program, like a `ptr_from_int` result. Once made, the slice is an ordinary
`T[]`: indexing and narrowing are checked against `len`.

```gyro
safe fn pixels(buffer: anyptr, count: usize): u32[] {
  return slice_from_raw<u32>(buffer, count)
}
```

`offset` and the raw heap functions are what `mem` is written over
([chapter 4.5](ch04-05-allocators.md)); they were `mem`'s own, and are here
so that a program writing its own allocator, or a kernel writing its own
page frame table, has the same floor. `env_of` and `with_env` are the two
operations `keep` and `release` are written over: a closure's environment is
a record in the frame that wrote it, and its first word is the record's size
in bytes. Pointer arithmetic on a `*T` is not here yet
([appendix G](appendix-g-known-gaps.md)); when it arrives it lives here.

## Containing it

A `safe fn` wraps the step, and everything above it is ordinary Gyro. A
packet is four bytes laid out the way C lays them out, so an address and a
byte count name a field:

```gyro
import { cast, offset } from "unsafe"

struct Packet { tag: u8  low: u8  high: u8  last: u8 }

safe fn byte_at(p: *Packet, index: usize): u8 {
  return *cast<u8>(offset(p as anyptr, index))
}

fn main() {
  var p = Packet{ tag: 7, low: 16, high: 32, last: 255 }
  println(byte_at(&p, 2))        // 32
}
```

`byte_at` vouches that `index` stays inside the packet. It does not check
that: nothing does, and a caller that passes 40 gets what C gives. A
function that *did* check, and returned a byte only for an index below
four, would be one worth marking `safe`; one that left the index to the
caller is an `unsafe fn`. Which of the two a function is, is the decision
the marker records.

The standard library does this throughout. `mem`'s `alloc`, `keep` and
`release`, the heap and arena procedures, and the thread entry that `spawn`
starts are `safe fn`s over the `unsafe` module; `free`, `format` and
`Arena::new` need nothing from it and are plain.

## Foreign functions

A function C provides is unsafe to call unless someone says otherwise,
because the compiler can check its signature and nothing about what it does.

**`@extern` declarations are `unsafe fn` by default.** Writing `safe` on the
declaration vouches for it, which is the author saying that no call can
misuse the function:

```gyro
@extern("m") safe fn sqrt(x: f64): f64 !{}      // any f64 is fine
@extern("c") fn strlen(text: string): usize !{} // unsafe to call
```

**A function imported from a `.c` file is unsafe to call unless the import
vouches for it.** One line says it for every name the import binds:

```gyro
import safe { parse, free_json } from "./cJSON.c"
import { parse_unchecked } from "./cJSON.c"     // calls need an unsafe context
```

The C behind a function can corrupt memory even when its signature is
checked ([chapter 15](ch15-00-c-interop.md)), and one line per import keeps
the trust visible without marking every call. `import safe * as lib` vouches
for the namespace the same way. Vouching is about C, whose functions nothing
in Gyro can read; a Gyro module says for itself which of its functions are
unsafe, so `import safe` of one is an error. A vouched name that is
re-exported stays vouched, since the module that vouched is the one
answerable for it. The types and structs a C file declares need no vouching:
naming a type calls nothing.

## Unions

**Reading a union member needs an unsafe context.** Nothing records which
member was written last, so a read is the program's claim that the bytes
are this member's ([chapter 5.1](ch05-01-structs.md)). **Writing** a member
does not: it says nothing about what the other members now mean. A literal
sets one member and is safe too, and so is writing through a member that is
storage in the union (`v.bytes[3] = 1`, or `outer.inner.i = 4`), where the
name is only a way to say where the store goes. A compound assignment such
as `v.i += 1` reads first and needs the context. A member holding a pointer
is read to be written through, which is a read.

## What each kind may call

| caller | calls a plain `fn` | calls a `safe fn` | calls an `unsafe fn` |
| --- | --- | --- | --- |
| plain `fn` | yes | yes | error |
| `safe fn` | yes | yes | yes |
| `unsafe fn` | yes | yes | yes |

A plain function can reach unsafe code only through a `safe fn`, so reading
the `safe` and `unsafe` markers, and the imports from `unsafe`, finds every
place a program makes a claim the compiler does not check. Everything else
is checked as before: [chapter 4.2](ch04-02-leaks-and-defer.md) and
[chapter 12](ch12-00-effect-rows.md) are not relaxed inside a `safe fn`.
