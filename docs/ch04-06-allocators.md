# 4.6. Allocators

Every allocation goes through an **allocator**, and an allocator is an
ordinary value. Most code never names one: a container, a `String` or a
`Box` made without one uses the process heap. Code that cares where its
memory comes from, such as a frame that builds a whole tree and throws it
away at once, passes the allocator it wants, and the choice is written at
the call.

```gyro
import { Array } from "stdlib"
import { Arena, heap } from "mem"

let names = Array<string>::new()                 // the heap
let scratch = Arena::new(heap(), 65536)
let parts = Array<string>::new(scratch.allocator()) // the arena
```

There is no ambient allocator and no statement that swaps one in for a
stretch of code: the default is the heap, and anything else is an argument.

## Values remember theirs

An owning value records the allocator it was made with, and everything it
allocates afterwards comes from the same one: an `Array` grows through it
when it is pushed onto, and gives its buffer back to it when it is destroyed
([chapter 4.2](ch04-02-destroy-and-defer.md)). Growing a buffer from one
allocator and freeing it to another cannot be written.

The functions that build a new owning value from a borrowed one, such as
`format`, `split`, `join`, `runes` and the readers in `fs` and `os`, take
the allocator as their last parameter, defaulting to the heap
([chapter 8.2](ch08-02-strings.md)).

## An allocator is a value

```gyro
export enum Mode { Alloc, Free, FreeAll, Resize }

export struct Allocator {
    proc: code(anyptr?, Mode, usize, usize, anyptr?, usize): anyptr?
    data: anyptr?
}
```

Two words: a procedure and whatever state the procedure needs, both visible
so a program can build an allocator of its own. The procedure answers every
request, and the mode says which one it is:

| mode | arguments | answers |
| --- | --- | --- |
| `Alloc` | `size`, `align` | a zero-filled block of `size` bytes at `align`, or null |
| `Free` | `old`, `old_size` | nothing; the block is the allocator's again |
| `FreeAll` | | nothing; every block it handed out is released at once |
| `Resize` | `old`, `old_size`, `size`, `align` | a block of `size` bytes holding the first `min(old_size, size)` bytes of `old`, the rest zero, or null |

The procedure is `(data, mode, size, align, old, old_size)`. An allocator
that cannot do a mode answers with nothing for `Free` and `FreeAll`, and
with a fresh block for `Resize`. Null from `Alloc` or `Resize` means the
request cannot be met, and the container that asked panics with the size.

A block from `Alloc` is **zero-filled**, because an allocator reuses memory,
and what the last owner left in it is not the new owner's to see.

The process heap is `heap()` from `mem`. Making one allocates nothing, and
every other allocator is built from one that already exists.

## `Arena`

An arena hands out blocks from one buffer and takes them all back at once.
It is ordinary Gyro in `mem`, written over the same `Allocator`:

```gyro
export fn Arena::new(a: Allocator, capacity: usize): Arena
export fn Arena::allocator(self): Allocator
export fn Arena::reset(inout self)
```

`new` takes its buffer from `a` and records it, so an arena inside an arena
is an ordinary thing to build. When the arena is destroyed, its buffer goes
back to `a`. `reset` empties it, and every block it handed out is gone at
once.

An arena answers `Free` by doing nothing and `Resize` by allocating a fresh
block and copying. It does not grow: an `Alloc` past its capacity answers
null.

### What lives in an arena views it

A value made from `arena.allocator()` views the arena, the way a slice views
an array ([chapter 4.4](ch04-04-borrowing.md)), so the borrowing rules keep
it from outliving the arena:

```gyro
fn build(): Array<i32> {
    let frame = Arena::new(heap(), 4096)
    let xs = Array<i32>::new(frame.allocator())
    xs.push(1)
    return xs // error: xs views frame, which ends with this function
}
```

`reset` takes `inout self`, so it is refused while anything made from the
arena is still going to be used, and the values made before it cannot be
read after it. A value made from an `Allocator` parameter views that
parameter, so the arena stays the caller's.

A value from the heap views nothing, since the heap is never emptied as a
whole.

An allocator is not thread-safe unless it says so. An `Arena` is not, and an
arena handed to another thread has to be the only thing using it.

## Writing one

Anything with the right shape is an allocator. This one counts what reaches
it and leaves the work to the heap:

```gyro
struct Counter {
    allocs: i32
}

safe fn counting(data: anyptr?, mode: Mode, size: usize, align: usize,
    old: anyptr?, old_size: usize): anyptr? {
    if data == null {
        return null
    }
    let counter = cast<Counter>(data)
    if mode == Mode::Alloc {
        counter.allocs += 1
    }
    let h = heap()
    return h.proc(h.data, mode, size, align, old, old_size)
}
```

The procedure is a `safe fn` because it gives `data` its type back with
`cast` from the `unsafe` module, and vouches that the allocator it was
registered with carries a `Counter` ([chapter 4.7](ch04-07-unsafe.md)). The
raw heap under `heap()` is the `unsafe` module's `raw_alloc`, `raw_free` and
`raw_resize`.
