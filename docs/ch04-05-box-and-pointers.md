# 4.5. `Box` and pointers

Most values live in a variable, a field or a container. When a value has to
live on its own, as the nodes of a list or a tree do, it goes in a **box**,
and a pointer is how a program looks at one value without owning it.

## `Box<T>` owns one value

`Box<T>` holds a single `T` in memory of its own and owns it: when the box is
destroyed, the value in it is destroyed and the memory goes back to the
allocator ([chapter 4.2](ch04-02-destroy-and-defer.md)).

```gyro
import { Box } from "stdlib"

struct Node {
    value: i32
    next: Box<Node>? // the rest of the list, or null at its end
}

let list = Node{ value: 1, next: Box::new(Node{ value: 2, next: null }) }
```

`Box::new(value)` takes the value and allocates from the heap, and
`Box::new(value, a)` from the allocator `a`
([chapter 4.6](ch04-06-allocators.md)). Reading through a box needs nothing
written: for a `b: Box<Node>`, `b.value` reads the field of the boxed node,
`b.value = 3` writes it, and a method of `Node` is called on `b` as it is on
a `Node`. A box that may be absent is checked before it is used, like any
nullable ([chapter 4.3](ch04-03-nullability.md)), and the check narrows the
name that holds it, so a field of a node is walked with a pointer, as below.
A box is owning, so it moves like any owning value, and dropping the head of
a list destroys every node after it.

Changing what is in a box changes the box: the value belongs to it, so a
box that is a plain parameter or a `const` cannot be written through, and a
value cannot be moved out of one, as it cannot out of a field.

## A pointer views one value

`&x` makes a `*T`: a pointer to `x`, which views `x` the way a slice views
the elements it covers ([chapter 4.4](ch04-04-borrowing.md)). Everything the
borrowing rules say about views holds for it. While the pointer is still
going to be used, `x` may not be moved, destroyed or changed through
anything else, and a pointer to a local cannot leave the function.

```gyro
fn last(list: Node): i32 {
    let current: *Node = &list
    let next: *Node? = &current.next // null at the end of the list
    while next != null {
        current = next
        next = &current.next
    }
    return current.value
}
```

`&` on a box points at the value inside it, not at the box, and `&` on a
nullable box gives a nullable pointer, null when the box is absent. Field
access through a pointer needs no `*`: `current.value` reads through it. `*p` is the value itself, and `*p = 5` assigns through it, which is a
change to what `p` views.

`*T` cannot be null. Only `*T?` can, and it cannot be used until it has been
checked.

## Raw pointers

Some pointers come from places the compiler cannot follow: memory a C
library hands back, an address computed from an integer, a pointer cast from
an `anyptr`. Each of those is made by an `unsafe fn` from the `unsafe`
module, or by a C function, which is unsafe to call
([chapter 4.7](ch04-07-unsafe.md)). The code that makes one is vouching that
it points at a live `T` for as long as it is used, and nothing checks it
afterwards.

`alloc<T>(a)` and `free(a, p)`, which hand out and take back raw memory, are
`unsafe fn` for the same reason, and a program that wants a value of its own
on the heap uses `Box<T>` instead.
