# 13.1. Threads

Gyro has three concurrency mechanisms, and they answer three separate
questions:

| mechanism | question |
| --- | --- |
| threads | how do I use every core? |
| channels | how do I hand work between them safely? |
| async | how do I hold many idle IO operations at once? |

This chapter is the first.

## `spawn`

`spawn` starts a call on another thread. It is an ordinary function from
`mem` taking an allocator and a closure, **not grammar** — the closure is
what carries the arguments across:

```gyro
import { heap, spawn } from "mem"

spawn(heap(), fn() produce(values, 10))
```

A thread outlives the function that started it, so `spawn` calls
`keep(a, body)` ([chapter 11](ch11-00-closures.md)) to copy the closure's
environment out of the caller's frame into `a`, and the thread releases it
to `a` when the closure returns. The allocator therefore has to be one that
can be used from another thread.

`@concurrent(threads = N)` configures a parallel region as a decorator on a
function.

A spawned thread is a real OS thread, started with the METALS instruction
`thread.start` ([appendix D](appendix-d-metals.md)): `spawn` is Gyro, in
`mem`, and the backend alone knows how a thread begins. A panic on a spawned
thread kills that thread only; on the main thread it ends the process.

## Mutexes

`Mutex` and friends come from the standard library:

```gyro
import { Mutex } from "stdlib"

var total = Mutex::new(0)

var guard = total.lock()
defer guard.unlock()
guard.set(guard.get() + value)
```

`Mutex::new(0)` constructs one — and infers `Mutex<i32>` from the argument —
and `lock()` returns the guard directly. **There is no poisoning**, because
a Gyro panic aborts the process rather than unwinding past a held lock, so
no thread ever observes a lock its holder abandoned.

Releasing is explicit: `guard.unlock()`, usually as `defer guard.unlock()`.
Gyro has no destructors, so nothing can release a guard implicitly. If a
drop-based design ever lands, the explicit call becomes sugar for it.

Remember that a `defer` runs at the exit of its **enclosing block**, so a
`defer guard.unlock()` inside a loop body unlocks every iteration — which is
usually what you want, and is worth being deliberate about.

## Atomics

A `Mutex` guards a value larger than one word, or an update that takes
several steps. For a single number or flag, `Atomic<T>` from `stdlib` does
it without a lock:

```gyro
import { Atomic } from "stdlib"

var hits = alloc<Atomic<i64>>(heap())
hits.store(0)
spawn(heap(), fn() { var _ = hits.fetch_add(1) })
```

`T` is an integer of 8 to 64 bits, a `bool` or a pointer. `load`, `store`
and `swap` do what they say; `compare_and_swap(expected, desired)` stores
`desired` only if the cell holds `expected` and answers whether it did; the
`fetch_` operations (`add`, `sub`, `and`, `or`, `xor`, for integers) answer
the value from before. `fence()` keeps every atomic operation before it
from being seen after one that follows.

There is exactly one ordering, the strongest: every operation is seen by
every thread in one order, and nothing moves across it. Gyro has no weaker
ones to choose, so there is no way to choose wrongly.

The cell is the value, stored where the `Atomic` is. A thread shares one
the way it shares any memory, by pointer, as above; copying an `Atomic`
copies the number it holds, not the cell. The race warning below knows
this and stays quiet for a pointer, or a slice, of `Atomic`.

## The race warning

A closure captures by copy. That is safe for a value and **not** for a
reference: copying a `*T` copies the pointer, and copying a `T[]` copies a
pointer and a length over storage it does not own. Both threads then reach
the same memory.

Handing a reference to `spawn` that way **warns**:

```gyro
var node = alloc<Node>(heap())
spawn(heap(), fn() { node.value = 1 })      // warns: both threads reach node

spawn(heap(), fn[move node]() { ... })      // fine: the binding goes with it
spawn(heap(), fn() { ch <- 1 })             // fine: a channel is built for this
```

A channel capture is never reported, since carrying work between threads is
what a channel is for, a pointer to an `Atomic` is not either, since every
access to one is atomic, and a `move` capture hands the binding over rather
than sharing it. A closure that is not spawned is not reported either.

**This does not prove the absence of a race.** A pointer reached through a
struct field, and two spawns over one channel, are both invisible to it; so
is anything the callee of a captured function does. It catches the direct
case, which is the one people write by accident.

## Threads and the rest of the language

- A panic on a spawned thread prints and kills that thread; the process
  keeps running.
- `spawn` declares `!{ io }`, which covers starting the thread and
  stands in for the task's own effects until a row can travel with a
  function value ([chapter 12](ch12-00-effect-rows.md)).
- Concurrent programs whose output order is not determined cannot be pinned
  by a golden file, which is what the corpus's `//gyro:unordered` directive
  is for ([chapter 17.3](ch17-03-testing.md)).

Next: [channels and `select`](ch13-02-channels.md), which is how threads are
supposed to talk.
