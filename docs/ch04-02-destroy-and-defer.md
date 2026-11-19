# 4.2. `Destroy` and `defer`

When an owner goes out of scope, the value it owns is **destroyed**: the
type's `destroy` runs, and then its fields are destroyed in turn. That is
how an `Array` frees its buffer, how a `File` closes its descriptor and how a
`MutexGuard` unlocks, and none of them needs a call written at the end.

## `Destroy`

A type says what destroying it means by implementing the `Destroy` trait,
imported from `stdlib`:

```gyro
import { Destroy } from "stdlib"

export struct File {
    private fd: i32
}

impl Destroy for File {
    fn destroy(inout self) {
        close_fd(self.fd)
    }
}
```

`destroy` takes `inout self`: it may change the value, and it is the last
thing that ever sees it. It is never called by name. A type implements
`Destroy` at most once, and only a type that the same module declares, like
any trait ([chapter 10.1](ch10-01-traits.md)).

A struct that holds owning fields needs no `Destroy` of its own: its fields
are destroyed anyway. `Destroy` is for what the fields do not already say,
such as a descriptor stored as a number.

## When destruction happens

A value is destroyed when its owner goes away:

- a local, at the end of the block that declared it
- a parameter that took ownership, at the end of the function
- a field, when the value holding it is destroyed, after that value's own
  `destroy`, in declaration order
- an element of a container, when the container is destroyed or the element
  is removed and not handed on
- a variable that is assigned a new value, just before the assignment, so
  the old value does not linger

A value that was moved out is not destroyed by its old owner, because it is
no longer there ([chapter 4.1](ch04-01-ownership.md)). A value returned from
a function is not destroyed at its end either: it moved to the caller.

Locals are destroyed in **reverse order of declaration**, so a value is
always destroyed before anything it was built from:

```gyro
fn copy_file(from: string, to: string) {
    let source = File::open(from)
    let target = File::create(to)
    ...
}   // target is closed first, then source
```

A panic ends the program without unwinding, so nothing is destroyed on the
way out ([chapter 9](ch09-00-errors.md)). The operating system takes back
memory and descriptors with the process.

## `defer`

`defer expr` runs a call at **block exit**, which is for cleanup that is not
a value going away: restoring a terminal, printing a closing line, undoing a
change a function made to something it only borrowed.

```gyro
fn render(screen: inout Screen) {
    screen.hide_cursor()
    defer screen.show_cursor()

    if screen.width() == 0 {
        return // the defer runs here too
    }
    screen.draw()
}
```

Two details that are easy to get wrong:

- **Arguments are captured at registration**, not at exit. `defer f(x)`
  passes the `x` that existed when the `defer` ran, not whatever `x` was
  reassigned to afterwards.
- **The block is the unit, not the function.** A `defer` inside a loop body
  runs at the end of each iteration.

A `defer` and a declaration are registered in the order they are written,
and at the end of the block everything runs in reverse: the defers, and the
destruction of the locals, interleaved as they were written. So a `defer`
written after a local runs while that local still exists.
