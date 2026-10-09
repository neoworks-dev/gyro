# 10. Generics

Generics are **duck-typed**. A type parameter declares no bounds; the body's
usage is the requirement, checked when `T` becomes concrete.

```gyro
fn max<T>(a: T, b: T): T {
  if a.compare(b) > 0 {
    return a
  }
  return b
}

max(3, 7)        // ok: i32 has a built-in compare
max(v1, v2)      // ok: Version has compare
max(m1, m2)      // error: Mutex has no method compare
```

There is no `T: Ord`, no `where` clause, and nothing to implement. Any type
with the methods the body calls works; any type without them is a compile
error at instantiation.

## Checking happens per instantiation

A generic body is **re-checked for every distinct set of type arguments**.
That is what makes `max(m1, m2)` fail with "Mutex has no method compare" —
and the diagnostic lands in the generic's body, with a related note reading
"in the instantiation required here" at the call site. Both positions are
useful, and neither alone is.

Instantiation is cached and depth-capped, so a recursive instantiation
terminates rather than looping.

## Generic types

User structs may be generic, instantiated the same way:

```gyro
struct Pair<T> {
  first: T
  second: T
}

fn Pair<T>::swap(*self) {
  var tmp = self.first
  self.first = self.second
  self.second = tmp
}

var p = Pair<i32>{ 1, 2 }
```

Inference does the obvious things: a struct literal infers from its fields
(`Pair{ 1, 2 }`), and an owner argument infers through an associated
function, so `Mutex::new(0)` is a `Mutex<i32>` without the argument being
written.

## Type arguments live on path segments

`Array<i32>::new` parameterizes a **non-final** segment. That is why paths
are never treated as flat strings anywhere in the compiler, and why the
parser has to decide speculatively whether `<` opens a type-argument list or
is a comparison.

## Effects and borrows on a generic

A generic body's effects are judged per instantiation too: what a call on a
type parameter reaches is only known once the parameter is concrete.

Borrow rows on a generic are **conditional** — load-bearing where `T` turns
out to be a view, vacuous where it does not:

```gyro
fn pick<T>(a: T, b: T, f: bool): T !{ &a, &b } {
  if f {
    return a
  }
  return b
}

pick(1, 2, true)        // vacuous: i32 is not a view
pick("a", "b", false)   // checked: string is
```

The cost is that a generic returning one of its parameters needs the row
even where it never means anything — `fn identity<T>(x: T): T !{ &x }`. The
alternative was inferring generic rows from the body, which would make the
one place the annotation is hardest to read also the one place it is
invisible ([chapter 12](ch12-00-effect-rows.md)).

## Closures into generics

A closure argument is typed **after** the arguments that fix the type
parameters, so it does not matter which side of the call the closure sits
on:

```gyro
fn apply<T>(x: T, f: fn(T): T): T !{ &x }
fn twice<T>(f: fn(T): T, x: T): T !{ &x }

apply(5, fn(v) v * 3)      // T is i32, from the 5
twice(fn(v) v + 1, 10)     // likewise, though the closure comes first
```

## What happens to a generic at compile time

Generics do not survive lowering. The compiler **monomorphizes**: each
instantiation becomes its own function in the METALS module, under a quoted
name like `@"Pair<i32>::sum"`. The IR has no generics at all
([appendix D](appendix-d-metals.md)).

One consequence is visible from the language: a generic function has one
address *per instantiation*, so `&f` on a generic is not a code pointer that
a C signature could take ([chapter 15](ch15-00-c-interop.md)).

## Why no bounds

Bounds would need traits, and traits were removed. The duck-typed rule that
replaced them runs through the whole language — `error`, iteration,
printing, ordering and generics all work the same way, by having a method
rather than by declaring anything. The reasoning, and what was given up
along with it, is in [`decisions.md`](decisions.md) under "Traits were
removed" and "Generics are duck-typed".

What is given up is real: a generic's requirements are not visible in its
signature, and an unusable type argument is caught at the call site rather
than at the declaration. What is bought is that no type ever has to be
adapted to a contract it did not know about.
