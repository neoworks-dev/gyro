# 4.2. Leaks and `defer`

## `defer`

`defer expr` runs a call at **block exit** — when the enclosing `{ }` ends,
in reverse order of registration. A loop body's defers run every iteration.

```gyro
fn sum_pair(a: i32, b: i32): i32 {
  var node = alloc<Node>()
  defer free(node)

  node.value = a + b
  if node.value < 0 {
    return 0        // the defer runs here too
  }
  return node.value
}
```

Two details that are easy to get wrong, and that every tier is held to:

- **Arguments are captured at registration**, not at exit. `defer free(p)`
  frees the pointer `p` held when the `defer` ran, not whatever `p` was
  reassigned to afterwards.
- **The block is the unit, not the function.** A `defer` inside a `for` body
  runs at the end of each iteration, which is what makes this correct:

```gyro
for line in lines.slice() {
  var parts = line.split(" ")
  defer parts.free()
  ...
}
```

There are no destructors, so nothing releases anything implicitly — a mutex
guard, a file handle and an allocation all end with a call you wrote. `defer`
is how that call stops being something you can forget on the third early
return.

## The leak analysis

A sema pass tracks each `alloc<T>()` and **warns** when one can reach the
end of its block with nothing having freed or taken it. Warnings, not
errors, so arena and process-lifetime patterns are not blocked.

An allocation is **settled** by `free`, and equally by anything that hands
it on:

- returning it
- sending it on a channel
- storing it in a struct or array literal
- passing it to a call
- moving it into a closure with `fn[move node]`

Only a bare name counts. `node.value` reads *through* the pointer and leaves
ownership where it was.

```gyro
// The allocation escapes through the return value, so ownership moves to
// the caller and no warning is reported here.
export fn make_node(value: i32): *Node {
  var node = alloc<Node>()
  node.value = value
  node.next = null
  return node
}
```

## Freeing on one path is not freeing

A `return` is an exit the check sees, so an allocation the return does not
carry with it is reported **at the return** rather than at the closing
brace:

```gyro
fn g(cond: bool) {
  var node = alloc<Node>()   // warns: the early return loses it
  if cond {
    return
  }
  free(node)
}
```

`defer free(node)` immediately after the allocation is the fix, and is the
idiom for a reason: it settles every exit at once.

## What it does not see

The analysis is deliberately simple — gen-only, no fixpoint over loops — and
it is honest about that. A loop that frees a chain link by link is opaque to
it, which is why `free_chain` in [chapter 4.1](ch04-01-manual-memory.md) is
written out by hand and warns about nothing.

It also says nothing about library types. `Array<T>`, `String` and `Map<K, V>`
own heap buffers and are freed with `free()`; forgetting one leaks, silently,
today. And the runtime itself leaks by design in v1: strings, closure
environments and futures are never reclaimed
([appendix G](appendix-g-known-gaps.md)).
