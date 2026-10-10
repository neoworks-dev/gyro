# 10. Generics

A generic function or type takes **type parameters**, and a parameter states
what it needs of its argument with **bounds**: the traits the argument has
to implement. The body may use exactly what the bounds provide, and a call
with a type that does not provide it is an error at the call.

```gyro
import { Ord } from "stdlib"

fn max<T: Ord>(a: T, b: T): T !{ &a, &b } {
  if a > b {
    return a
  }
  return b
}

max(3, 7)        // ok: the numbers are Ord
max(v1, v2)      // ok: Version says so, in an impl
max(m1, m2)      // error: Mutex does not implement Ord
```

`Ord` is a trait ([chapter 10.1](ch10-01-traits.md)). A bound is part of the
signature, so the requirements of a function are on its first line and
nowhere else.

## Bounds

Several bounds on one parameter are joined with `&`:

```gyro
fn count_distinct<K: Eq & Hash>(keys: K[]): usize {
  var seen = Map<K, bool>::new(heap())
  defer seen.free()
  for key in keys {
    seen.insert(key, true)
  }
  return seen.len()
}
```

A trait that is itself bounded by another carries it along: `Ord` requires
`Eq`, so a parameter bounded by `Ord` may use `==` too.

**Every parameter that the body does anything with states its bounds.**
There is no way to leave them off and have the body's usage stand in for
them. A parameter with no bounds can be stored, passed on, returned and put
in a container, and nothing else:

```gyro
fn describe<T>(item: T): string {
  return item.name()      // error: T has no method name
}
```

The bounds are what a body may use, and the compiler holds it to them. A
generic body is checked **once against its bounds**, with the parameter
opaque, and then **again for every distinct set of type arguments** it is
called with, to produce the code for them. Because the first check allows
nothing the bounds do not give, the second cannot fail for a type that met
them.

## What a bound allows

| the parameter is bounded by | the body may |
| --- | --- |
| a trait of the program's own | call that trait's methods, and those of the traits it requires |
| `Eq` | compare with `==` and `!=` |
| `Ord` | order with `< <= > >=` and call `compare` (which also gives `Eq`) |
| `Add`, `Sub`, `Mul`, `Div`, `Rem` | use `+ - * / %` on two values of the parameter |
| `Neg` | negate with unary `-` |
| `Hash` | call `hash` |
| `Print` | pass the value to `println` and interpolate it |
| `Iterator<T>` | walk it with `for..in` |
| `Index<T>` | index it |

The other operators are the numbers' alone. A parameter has no `&`, `<<`
or `+%`, and no fields: it is some type, and the bounds are all the body
knows about it.

## Where bounds are written

On a generic function, after the parameter name, and on a generic type, where
the parameter is introduced:

```gyro
struct Map<K: Eq & Hash, V> {
  buckets: Array<Array<(K, V)>>
  count: usize
}

fn Map<K, V>::insert(*self, key: K, value: V) {
  // K is Eq and Hash here, because the type says so
}
```

A method of a generic type **inherits the bounds of the type** and does not
restate them: `fn Pair<T: Eq>::swap` is an error, since the type is the one
place that says what its parameter must be. Naming the type with an
argument that does not meet its bounds — `Map<Mutex<i32>, bool>` — is the
error, at the mention.

A type whose parameters are only stored needs no bounds:

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
written. The bounds are checked against what was inferred.

## An impl cannot add bounds to the type

`impl Add for Money<T>` is an impl of `Add` for every `Money<T>` the type
allows, and cannot ask for more than the type does. A type whose impls need
`T: Add` declares `struct Money<T: Add>`. Conditional impls — `Money<T>` is
`Add` only when `T` is — are not built ([appendix G](appendix-g-known-gaps.md)).

## Where errors land

An unmet bound is reported **at the call**, naming the bound and who asked
for it:

```text
error[E0527]: Mutex<i32> does not implement Ord, which max requires of T
```

A body that does something its bounds do not allow is reported **in the body**,
once, whether or not anything calls it:

```text
error[E0405]: T has no method name
```

Instantiation is cached and depth-capped, so a recursive instantiation
terminates rather than looping. An instantiation that fails anyway — which
only a bug in the checker can cause — reports its error in the generic's body
with a related note reading "in the instantiation required here".

## Effects and borrows on a generic

A generic body's effects are judged per instantiation: what a call on a type
parameter reaches is only known once the parameter is concrete.

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

## Type arguments live on path segments

`Array<i32>::new` parameterizes a **non-final** segment. That is why paths
are never treated as flat strings anywhere in the compiler, and why the
parser has to decide speculatively whether `<` opens a type-argument list or
is a comparison.

## What happens to a generic at compile time

Generics do not survive lowering. The compiler **monomorphizes**: each
instantiation becomes its own function in the METALS module, under a quoted
name like `@"Pair<i32>::sum"`. The IR has no generics at all
([appendix D](appendix-d-metals.md)).

One consequence is visible from the language: a generic function has one
address *per instantiation*, so `&f` on a generic is not a code pointer that
a C signature could take ([chapter 15](ch15-00-c-interop.md)).

## Why bounds are required

Gyro once checked a generic by what its body used and declared nothing. The
errors then pointed into the body, the requirement was nowhere in the
signature, and a type was adapted to a contract only by accident of having
a method of the right name. Bounds put the requirement where a reader and a
caller both look, and make a type opt in on purpose. The reasoning, and the
alternatives that were rejected, are in [`decisions.md`](decisions.md) under
"Generics are bounded" and "Traits".
