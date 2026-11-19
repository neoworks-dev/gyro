# 4.1. Ownership

Every value has exactly one owner, and when the owner goes away, so does the
value. There is no garbage collector and nothing is freed by hand: an
`Array`, a `String` or a `File` releases what it holds when the variable,
field or container that owns it goes out of scope
([chapter 4.2](ch04-02-destroy-and-defer.md)).

```gyro
import { println, Array } from "stdlib"

fn main() {
    let names = Array<string>::new() // names owns the array and its buffer
    names.push("ada")
    names.push("grace")
    println(names.len())
}                                    // names goes out of scope: the buffer is freed
```

An owner is a variable, a field, an element of a container, or a parameter
that took ownership. Nothing else owns: a view such as a `string` or a `T[]`
looks at what someone else owns ([chapter 4.4](ch04-04-borrowing.md)).

## Owning values and plain values

A type is **owning** when something has to happen when a value of it goes
away: it implements `Destroy` ([chapter 4.2](ch04-02-destroy-and-defer.md)),
or one of its fields or elements is owning. `Array<T>`, `String`, `Map<K, V>`,
`Box<T>` and `File` are owning, and so is any struct that holds one.

Every other type is **plain**: the numbers, `bool`, `rune`, views, and any
struct or enum made only of those. A plain value is copied, exactly as it
always was, and nothing needs to be said about it:

```gyro
struct Point { x: i32, y: i32 }

let a = Point{ x: 1, y: 2 }
let b = a          // a copy
println(a.x)    // fine: a is still a Point
```

There is no `Copy` trait to opt into. Whether a type copies is decided by
what it is made of, so adding an owning field to a struct is what makes it
stop copying.

## Assigning an owning value moves it

An owning value is never copied implicitly, because two owners would destroy
it twice. Assigning it, returning it, storing it in a field or a container,
or passing it to a parameter that takes ownership **moves** it, and the
variable it moved out of no longer holds a value:

```gyro
let names = Array<string>::new()
let kept = names         // the array moves to kept
println(names.len())  // error: names was moved to kept
```

The check is the one that already stops a read before a write
([chapter 3.1](ch03-01-variables-and-constants.md)): a move leaves the
variable unassigned, and assigning it again makes it usable again. A value
moved on only one branch is destroyed at the end of the scope only if it is
still there, which the compiler tracks with a hidden flag.

## Parameters borrow unless they take

A parameter borrows its argument by default: the function may read it, and
the caller still owns it afterwards. A parameter whose type is written with
`!` takes ownership, and the argument is moved into the call:

```gyro
fn count(names: Array<string>): usize { // borrows
    return names.len()
}

fn Registry::adopt(inout self, names: Array<string>!) { // takes
    self.lists.push(names)
}

count(names)
count(names)          // fine: count only borrowed it
registry.adopt(names) // names moves into the registry
count(names)          // error: names was moved into adopt
```

The call site looks the same either way, and the signature is where the
difference is stated. `!` mirrors `?`: `T?` is a `T` that may be absent, and
`T!` is a `T` that is handed over. A parameter that may change its argument
is written `inout` ([chapter 4.4](ch04-04-borrowing.md)).

## Copying on purpose

An owning value is copied only when the program asks, with `clone()`:

```gyro
let backup = names.clone() // a second array with the same elements
```

`clone()` comes from the `Clone` trait. A struct gets it field by field with
an empty `impl Clone for Registry`, the way it gets `==` from
`impl Eq for Point` ([chapter 10.1](ch10-01-traits.md)), and a type that
must not be duplicated, such as a `File`, simply does not implement it.

## Gyro's memory model is C's

Ownership is a compile-time property and costs nothing in memory. A value
occupies exactly what `sizeof` would say in C, at C's alignment, padded where
C pads, and a move is a copy of those bytes. That is a commitment: it is
what makes a Gyro struct and a C struct of the same fields *the same bytes*,
with nothing converted at the boundary
([chapter 15](ch15-00-c-interop.md),
[appendix E](appendix-e-native-backend.md)).

## What the compiler checks

- a use of a variable whose value was moved out
- a view used after its source moved, changed or went away
  ([chapter 4.4](ch04-04-borrowing.md))
- a dereference of a raw pointer outside unsafe code
  ([chapter 4.5](ch04-05-box-and-pointers.md))
- a nullable used before a check ([chapter 4.3](ch04-03-nullability.md))
- a read of a variable no path has written
  ([chapter 3.1](ch03-01-variables-and-constants.md))

What a `safe fn` vouches for, and anything done through raw pointers, is
outside these checks ([chapter 4.7](ch04-07-unsafe.md)).
