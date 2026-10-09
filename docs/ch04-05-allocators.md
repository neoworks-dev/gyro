# 4.5. Allocators

Every allocation a Gyro program makes is made by an **allocator**, and a
function that allocates is handed one. Which one it is handed is decided by
its caller, and the caller decides with one statement. Nothing about this
is hidden state: the allocator is a parameter, the effect row is what says
a function takes it, and the row is checked ([chapter 12](ch12-00-effect-rows.md)).

```gyro
import { alloc, Arena } from "mem"

fn build_tree(depth: i32): i32 {
  var arena = Arena::new(65536)
  defer arena.free()

  with arena.allocator()
  var root = make_node(depth)       // every alloc<T>() below is the arena's
  ...
}                                    // and arena.free() takes them all back
```

## An allocator is a value

```gyro
export enum Mode { Alloc, Free, FreeAll, Resize }

export struct Allocator {
  export proc: code(anyptr?, Mode, usize, usize, anyptr?, usize): anyptr?
  export data: anyptr?
}
```

Two words: a procedure and whatever state the procedure needs, both
visible so a program can build an allocator of its own. The
procedure answers every request, and the mode says which one it is:

| mode | arguments | answers |
| --- | --- | --- |
| `Alloc` | `size`, `align` | a zero-filled block of `size` bytes at `align`, or null |
| `Free` | `old`, `old_size` | nothing; the block is the allocator's again |
| `FreeAll` | | nothing; every block it handed out is released at once |
| `Resize` | `old`, `old_size`, `size`, `align` | a block of `size` bytes holding the first `min(old_size, size)` bytes of `old`, the rest zero, or null |

The procedure is `(data, mode, size, align, old, old_size)`. An
allocator that cannot do a mode — an arena does not free one block, a
heap has nothing to free all at once — answers with nothing for `Free`
and `FreeAll`, and with a fresh block for `Resize`. Null from `Alloc` or
`Resize` means the request cannot be met, and `alloc<T>()` panics on it
with the size in the message rather than handing back a pointer that is
not one.

A block from `Alloc` is **zero-filled**. That is part of the contract
because an allocator reuses memory, and what the last owner left in it
is not the new owner's to see.

## Which allocator a function uses

A function has exactly one allocator at a time: the one it received. It
is reachable by name:

```gyro
import { allocator } from "mem"

fn build(): *Node !{ alloc } {
  var current = allocator()      // the allocator build was handed
  ...
}
```

`allocator()` is an ordinary import with the row `!{ alloc }`, and that
row is what puts the allocator in the function's hands: **a function
whose row names `alloc` receives an allocator**, as a parameter the
compiler passes for you. Rows are inferred when unstated, so a function
that never writes a row and calls `alloc<T>()` receives one all the same,
and a function that writes `!{}` never does. That is the whole mechanism;
[chapter 12](ch12-00-effect-rows.md) has the rules.

`alloc<T>()` and `free(p)` are written in Gyro over that allocator, which
is why both carry `!{ alloc }` — `free` has to hand the block back to the
allocator that gave it out, so it uses the allocator the same way `alloc`
does. `size<T>()` and `align<T>()` answer what C's `sizeof` and
`_Alignof` would.

## `with` changes it for the rest of the block

```gyro
with arena.allocator()
```

`with expr` is a statement. From that line to the end of the enclosing
`{ }`, every call that takes an allocator is handed this one, blocks
inside included. When the block ends the outer allocator is back, the
way a `defer` is the block's and not the function's
([chapter 4.2](ch04-02-leaks-and-defer.md)). The expression must be an
`Allocator`, and it is evaluated once, where it is written.

```gyro
fn parse(line: string) {
  var scratch = Arena::new(4096)
  defer scratch.free()

  var kept = Array<string>::new()           // the caller's allocator
  {
    with scratch.allocator()
    var parts = line.split(" ")             // the arena's
    kept.push(parts.get(0))
  }
  ...                                        // the caller's again
}
```

Two `with` statements in one block are fine: the second replaces the
first for what follows it. A `with` inside a loop body is the body's, so
it applies to the rest of each iteration and is evaluated each time.

`with` is not an effect. Writing one says nothing about what the
function itself does; the function that *uses* the allocator is the one
whose row says so.

## Where the first one comes from

`main` receives the process heap, and so does every module-level
initializer ([chapter 3.1](ch03-01-variables-and-constants.md)). The
heap is `heap()` from `mem`, reachable from anywhere with no row at all:
it is a value, and making one allocates nothing.

```gyro
import { heap } from "mem"

fn keep(text: string): String !{ alloc } {
  with heap()
  return String::of(text)       // outlives whatever arena the caller is in
}
```

## Closures capture it

A closure that allocates captures the allocator the way it captures any
other local: by copy, at creation. Calling the closure later allocates
from *that* allocator, wherever the call happens. This is what carries an
allocator into a `spawn`, and it is why calling a function value needs no
row: the value already holds everything it needs.

```gyro
var arena = Arena::new(4096)
defer arena.free()

with arena.allocator()
var make = fn(n: i32): *Node {     // captures the arena's allocator
  var node = alloc<Node>()
  node.value = n
  return node
}
```

A named function that allocates, taken as a value (`var f = build`),
closes over the allocator current where it was taken, for the same
reason.

Where no allocator is current — a closure written inside a function
whose row has no `alloc`, or a module initializer — the heap is what
there is, and that is what the closure captures.

An allocator is not thread-safe unless it says so. An `Arena` is not,
and a closure that carries one to another thread has to be the only
thing using it.

## `&f` on an allocating function

`&build` is a code pointer ([chapter 15](ch15-00-c-interop.md)), one
word with nowhere to hold an allocator. A C library that calls it back
cannot pass one either. So the address taken is a small thunk that
supplies the process heap and calls `build`: an allocating callback
allocates from the heap, whatever `with` was in force where its address
was taken.

## `Arena`

An arena hands out blocks from one buffer and frees them all at once.
It is ordinary Gyro in `mem`, written over the same `Allocator` a program
could write for itself:

```gyro
export fn Arena::new(capacity: usize): Arena !{ alloc }
export fn Arena::allocator(*self): Allocator !{}
export fn Arena::free_all(*self) !{}
export fn Arena::free(*self) !{ alloc }
```

`new` takes its buffer from the allocator current where it is called,
so an arena inside an arena is an ordinary thing to build. `allocator()`
is the value to hand to `with`; it points at the arena, so the arena has
to stay where it is for as long as that value is in use. `free_all`
resets the arena and every block it handed out is gone at once. `free`
returns the buffer to the allocator `new` took it from.

An arena answers `Free` by doing nothing, and `Resize` by allocating a
fresh block and copying. It does not grow: an `Alloc` past its capacity
answers null, and `alloc<T>()` panics.

## What honours the allocator today

`alloc<T>()`, `free`, `Arena` and everything written over them. The
runtime's own storage — an `Array<T>`'s buffer, the text the string
methods build, a closure's environment, a future — still comes
from the process heap and is not yet routed through the allocator it was
handed ([appendix G](appendix-g-known-gaps.md)). So does a `to_string`,
`hash` or `message` the compiler calls on its own behalf while printing,
hashing or rendering an error: those run on the heap, since nothing
chose an allocator for them. `Array<T>` recording its allocator, and
text coming out of the current one, is where this goes next.
