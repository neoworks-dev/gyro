# 4.1. Manual memory

Memory is manual, with a compiler-checked safety net. There is no garbage
collector, no ownership system that moves values for you, and no
destructors — and there is a static analysis that tells you when an
allocation is about to be lost.

- `alloc<T>(a)` returns `*T` from the **allocator** `a`, and `free(a, p)`
  releases it to the same one. Both are imported from the `mem` module,
  and the allocator is an ordinary argument written at the call
  ([chapter 4.5](ch04-05-allocators.md)); `heap()` is the process heap.
- `&x` takes an address, `*p` dereferences.
- `defer expr` runs a call at block exit ([chapter 4.2](ch04-02-leaks-and-defer.md)).
- A sema pass **warns** when an allocation can reach the end of its block
  with nothing having freed or taken it.

```gyro
import { alloc, free, heap } from "mem"

fn f(): i32 {
    var node = alloc<Node>(heap())
    defer free(heap(), node)
    return node.value
}
```

## Pointers are never null

`*T` cannot be null. Only `*T?` can, and it cannot be dereferenced until it
has been checked ([chapter 4.3](ch04-03-nullability.md)). So a linked list's
tail is `next: *Node?`, and every walk of it has a check in it:

```gyro
struct Node {
    value: i32
    next: *Node? // nullable: the last node has no successor
}

export fn free_chain(head: *Node) {
    var current: *Node? = head
    while current != null {
        // `current` is narrowed to *Node inside the check, so the field access
        // needs no unwrapping.
        var next = current.next
        free(heap(), current)
        current = next
    }
}
```

Field access through a pointer needs no `*`: `node.value` reads through it.
`*p` is for the value itself, and `*p = 5` assigns through it.

```gyro
var head = make_node(1)
var address = &head.value
println(*address)
```

## Gyro's memory model is C's

A value in memory occupies exactly what `sizeof` would say in C, at C's
alignment, padded where C pads. That is not an implementation detail that
happens to be true today — it is a commitment, and it is what makes a Gyro
struct and a C struct of the same fields *the same bytes*, with nothing
converted at the boundary ([chapter 15](ch15-00-c-interop.md),
[appendix E](appendix-e-native-backend.md)).

The price is the one C pays: a pointer is a machine word with no extent
attached, so nothing at run time knows how big the object behind it is.
Gyro pays it deliberately, and the compile-time analyses below are what it
buys back.

## What the compiler checks, and what it does not

Checked:

- an allocation lost at the end of its block, or at a `return` that does not
  carry it ([chapter 4.2](ch04-02-leaks-and-defer.md))
- a nullable dereferenced before a check ([chapter 4.3](ch04-03-nullability.md))
- a view outliving nothing in particular — that is, a returned `T[]` or
  `string` whose source is not named in the effect row
  ([chapter 12](ch12-00-effect-rows.md))
- a read of a variable no path has written ([chapter 3.1](ch03-01-variables-and-constants.md))

Not checked, and worth knowing:

- **A slice can outlive the memory behind it**, and nothing currently
  detects that.
- **Views through a pointer are untraceable.** The borrow analysis follows
  values and field chains, not stores through pointers.
- **Use after free and double free** are caught by the METALS evaluator,
  which poisons freed cells. The native runtime frees for real, so a native
  binary misses both ([appendix G](appendix-g-known-gaps.md)).
- **Mutation rules are unchecked**: `*self` versus `self`, and assigning
  through a value receiver.

## `alloc` is not how most programs get memory

Most programs never write `alloc<T>(a)`. Growing storage is `Array<T>`, text
is `String`, and both own a buffer and are freed with `free()`
([chapter 8](ch08-01-array.md)). `alloc` is for the cases where a program
lays out its own nodes — a tree, a list, an allocator of its own.

Memory from `alloc<T>(heap())` is **zero-filled**: that is the allocator's
contract ([chapter 4.5](ch04-05-allocators.md)), because an allocator
reuses memory and what its last owner left there is not the new owner's
to see. Reading a field before writing it is still a mistake — a zero
`string` is not text — and nothing detects it
([appendix G](appendix-g-known-gaps.md)).

There is one more way to hand ownership on, and it is the only thing a
capture list can say:

```gyro
export fn defer_release(value: i32): fn() {
    var node = alloc<Node>(heap())
    node.value = value

    return fn[move node]() {
        free(heap(), node)
    }
}
```

`fn[move node]` moves the binding into the closure. Captures are by copy
otherwise, so `move` is what makes an allocation's escape visible in the
source — to a reader, and to the leak analysis
([chapter 11](ch11-00-closures.md)).
