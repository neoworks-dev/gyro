# 4.5. Allocators

Every allocation a Gyro program makes goes through an **allocator**, and
an allocator is an ordinary value. A function that allocates takes one as
a parameter, and the caller writes which one at the call. Nothing about it
is hidden: there is no ambient allocator, no effect that decides who
receives one, and no statement that swaps it for a stretch of code. If a
function takes an `Allocator`, it allocates; if it does not, it does not.

```gyro
import { alloc, heap, Allocator, Arena } from "mem"

fn make_node(a: Allocator, value: i32): *Node {
  var node = alloc<Node>(a)
  node.value = value
  return node
}

fn build_tree(depth: i32): i32 {
  var arena = Arena::new(heap(), 65536)
  defer arena.free()

  var root = make_node(arena.allocator(), depth)   // the arena's, as written
  ...
}                                                  // and arena.free() takes them all back
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
`Resize` means the request cannot be met.

A block from `Alloc` is **zero-filled**. That is part of the contract
because an allocator reuses memory, and what the last owner left in it
is not the new owner's to see.

## `alloc` and `free`

```gyro
export fn alloc<T>(a: Allocator): *T
export fn free<T>(a: Allocator, pointer: *T)
```

`alloc<T>(a)` asks `a` for one zero-filled `T`. When `a` answers null it
panics, with the size in the message, rather than handing back a pointer
that is not one. `free(a, p)` returns the block to `a`, and it has to be
the allocator the block came from: the call says which one, so a reader
can check that the two match. `size<T>()` and `align<T>()` answer what C's
`sizeof` and `_Alignof` would.

Neither is an effect ([chapter 12](ch12-00-effect-rows.md)). A function
that takes an `Allocator` shows it in its signature, and a function that
does not take one cannot reach any allocator but the process heap.

## Where the first one comes from

The process heap is `heap()` from `mem`. It is a value, and making one
allocates nothing, so it can be written wherever an allocator is needed:

```gyro
import { heap } from "mem"

fn keep(value: i32): *Node {
  return make_node(heap(), value)   // outlives whatever arena the caller is in
}
```

Every other allocator is built from one that already exists, which is why
nothing here needs a special start-up rule: `main` calls `heap()` like
anyone else.

## Passing it on

An allocator travels the way any value does. A function that builds
something out of several parts takes the allocator once and hands it to
each piece, and the choice the caller made reaches all of them:

```gyro
fn make_pair(a: Allocator, left: i32, right: i32): *Node {
  var first = make_node(a, left)
  first.next = make_node(a, right)
  return first
}
```

A struct that allocates over its life holds the allocator it was built
with, the way `Arena` does below. That is also what keeps a `free` honest:
the value that owns a block keeps the allocator that gave it out.

`&make_node` is an ordinary code pointer ([chapter 15](ch15-00-c-interop.md)),
and the `Allocator` is one of its parameters like any other. A C library
that calls a callback back cannot pass an `Allocator`, so a callback it
runs is written without one and reaches its allocator some other way, the
same as any state a callback needs.

An allocator is not thread-safe unless it says so. An `Arena` is not, and
an arena handed to another thread has to be the only thing using it.

## `Arena`

An arena hands out blocks from one buffer and frees them all at once. It
is ordinary Gyro in `mem`, written over the same `Allocator` a program
could write for itself:

```gyro
export fn Arena::new(a: Allocator, capacity: usize): Arena
export fn Arena::allocator(*self): Allocator
export fn Arena::free_all(*self)
export fn Arena::free(*self)
```

`new` takes its buffer from `a` and records `a`, so an arena inside an
arena is an ordinary thing to build, and `free` returns the buffer to the
allocator it came from without being told which. `allocator()` is the
value to pass to whatever should allocate from the arena; it points at the
arena, so the arena has to stay where it is for as long as that value is in
use. `free_all` resets the arena and every block it handed out is gone at
once.

An arena answers `Free` by doing nothing, and `Resize` by allocating a
fresh block and copying. It does not grow: an `Alloc` past its capacity
answers null, and `alloc<T>(arena.allocator())` panics with the size.

## Writing one

Anything with the right shape is an allocator. This one counts what
reaches it and leaves the work to the heap:

```gyro
struct Counter {
  allocs: i32
}

fn counting(data: anyptr?, mode: Mode, size: usize, align: usize,
            old: anyptr?, old_size: usize): anyptr? {
  if data == null {
    return null
  }
  var counter = data as *Counter
  if mode == Mode::Alloc {
    counter.allocs += 1
  }
  var h = heap()
  return h.proc(h.data, mode, size, align, old, old_size)
}

var counter = Counter{ allocs: 0 }
var counted = Allocator{ proc: &counting, data: &counter as anyptr }
var node = make_node(counted, 1)       // counter.allocs is now 1
```

## What honours the allocator today

`alloc`, `free`, `Arena` and everything written over them. The runtime's
own storage — an `Array<T>`'s buffer, the text `#{}` and the string
methods build, a future — still comes from the
process heap, because those take no allocator to honour
([appendix G](appendix-g-known-gaps.md)). So does a `to_string`, `hash` or
`message` the compiler calls on its own behalf while printing, hashing or
rendering an error. Containers that take an allocator and remember it, so
that growing and freeing agree by construction, are where this goes next.
