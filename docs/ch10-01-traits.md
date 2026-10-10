# 10.1. Traits

A **trait** is a list of methods a type promises to provide. A type
implements it in an `impl` block, and a generic bounds its parameters by it
([chapter 10](ch10-00-generics.md)).

```gyro
trait Shape {
  fn area(self): i32
  fn name(self): string

  // A default body is shared by every type that does not write its own.
  fn describe(self): string {
    return format(heap(), "#{self.name()} of area #{self.area()}")
  }
}

struct Square {
  side: i32
}

impl Shape for Square {
  fn area(self): i32 {
    return self.side * self.side
  }
  fn name(self): string {
    return "square"
  }
}
```

A method in a trait is a signature, with a body if it has a default. It
takes a receiver, `self` or `*self`, like any method. Inside the trait,
`Self` is the type that implements it, so `Ord` can say
`fn compare(self, other: Self): i32` and every implementer compares with its
own kind.

## Implementing

`impl Trait for Type { ... }` supplies the methods. The block is held to the
trait:

- every method the trait declares without a body is there
- no method the trait does not declare is
- each has the trait's signature, with `Self` read as the type

Each failure is its own error, naming the method. An impl is for a **struct,
enum or union that the same module declares**, and a type implements a trait
once.

A type's own methods are not written in an `impl`. They stay free-standing,
`fn Square::new(side: i32): Square`, exactly as in
[chapter 5.2](ch05-02-methods.md). What an `impl` block adds to a type are
methods too: `square.area()` is an ordinary call, and the methods share the
type's namespace, so a trait method and an own method with one name collide,
and so do the methods of two traits.

A generic type is implemented for by naming its own parameters:

```gyro
impl Eq for Pair<T>
impl Shape for Stack<T> { ... }
```

The names after the type are the type's parameters, nothing else, and the
bounds are the ones the type declared.

## Requiring other traits

```gyro
trait Widget: Element {
  fn click(*self)
}
```

A type that implements `Widget` has to implement `Element` too, and a
parameter bounded by `Widget` has the methods of both. Several are joined
with `&`: `trait Entity: Named & Printable`.

## Traits with parameters

A trait takes type parameters, written like a type's: `trait Iterator<T>`.
An impl names the arguments, `impl Iterator<i32> for Countdown`, and a bound
does, `I: Iterator<i32>`. A default body is only allowed in a trait with
none.

## Associated types

A trait can leave a type for each impl to fill in. It declares the type with
`type`, and an impl gives it with `type Name = T`:

```gyro
trait Container {
  type Item

  fn first(self): Self::Item
  fn count(self): usize
}

struct Box<T> {
  value: T
}

impl Container for Box<T> {
  type Item = T

  fn first(self): T {
    return self.value
  }
  fn count(self): usize {
    return 1
  }
}
```

Inside the trait and inside its impls the type is written `Self::Item`. On a
type parameter bounded by the trait it is `C::Item`, and it stands for
whatever the argument's impl gave:

```gyro
fn head<C: Container>(c: C): C::Item {
  return c.first()
}

head(Box<i32>{ value: 7 })    // an i32
```

An associated type may be bounded, with the bounds joined by `&` as
everywhere: `type Iter: Iterator<Self::Item>`. A body that has a `C` can then
use what the bounds give `C::Iter`, and an impl has to give a type that
satisfies them.

An impl defines every associated type its trait declares, and no others, and
the type it gives satisfies the declared bounds. `C::Item` is an error when no
bound of `C` declares an `Item`, and when two of them do, since nothing says
which is meant. The bounds of the traits a trait requires count: a bound
`C: Widget` reaches the associated types of `Element` in `trait Widget: Element`.

A bound can pin an associated type inside the trait's argument list, after
any positional arguments:

```gyro
fn total<C: Container<Item = i32>>(c: C): i32 {
  return c.first()          // C::Item is i32 here
}

fn tag<P: Pair<i32, Item = string>>(p: P): string
```

The pin has to name an associated type the trait declares, and an argument
satisfies the bound only when its impl gave exactly that type. In the body,
`C::Item` is the pinned type.

`type` is contextual: it begins an associated type only at the start of a
member in a trait or an impl, and is an ordinary name everywhere else, so a
field called `type` still works. A trait with associated types cannot be a
trait value yet ([chapter 10.2](ch10-02-trait-values.md)): the type behind a
`dyn Container` is unknown, and so is its `Item`.

## The capability traits

Equality, hashing, printing, ordering, the operators and iteration are
traits, declared in `stdlib` like any others and imported where they are
named. The compiler gives each a meaning by where it is declared.

| trait | provides | what uses it |
| --- | --- | --- |
| `Eq` | `eq(self, other: Self): bool` | `==` and `!=`; keys of a `Map` |
| `Hash` | `hash(self): u64` | keys of a `Map` |
| `Print` | `to_string(self): string` | `println`, `print`, `#{}` |
| `Ord: Eq` | `compare(self, other: Self): i32` | `< <= > >=` |
| `Add`, `Sub`, `Mul`, `Div`, `Rem` | `add`, `sub`, `mul`, `div`, `rem` taking and giving `Self` | `+ - * / %` |
| `Neg` | `neg(self): Self` | unary `-` |
| `Iterator<T>` | `next(*self): (T, bool)` | `for..in` |
| `Index<T>` | `at(*self, index: usize): *T` | `value[i]` |

**The built-in types implement the ones that apply to them without any
impl.** Every number is `Eq`, `Hash`, `Print`, `Ord` and has the operators;
`string` and `rune` are `Ord`; a tuple, an optional, a fixed-size array or a
pointer is `Eq` and `Print` where what it holds is. Nothing is written for
them.

**A type the program declares has one only by saying so.** `==` on a `Point`
that does not implement `Eq` is an error, as is printing it, and so is
passing it where a bound asks for either:

```text
error[E0527]: Point does not implement Eq, which the operator == needs
  = help: add: impl Eq for Point
```

### Deriving

`impl Eq for Point` with **no block** asks for the implementation the
compiler builds from the type's fields: equal when every field is, hashed
and printed field by field. It is available for `Eq`, `Hash` and `Print`,
and only where every field has the capability itself:

```gyro
struct Line {
  item: string
  price: Price
}

impl Eq for Line
impl Hash for Line
impl Print for Line
```

A field of the type's own whose impl is written out is used where the
derived one reaches it, so `Line` above compares `price` with the `eq` in
`Price`'s impl. An opt-in with no block is a statement that the plain rule
is what is wanted; an opt-in with a block replaces it, and then the block
owns keeping `Eq` and `Hash` agreed.

A type that holds a union, a pointer it should hash, or a field with no
`Eq` cannot derive it and says so.

### Writing one out

```gyro
impl Ord for Version {
  fn compare(self, other: Self): i32 {
    if self.major != other.major {
      if self.major < other.major {
        return -1
      }
      return 1
    }
    return 0
  }
}

impl Print for Version {
  fn to_string(self): string {
    return format(heap(), "v#{self.major}.#{self.minor}")
  }
}
```

`Print`'s method replaces the derived text, and `println(v)` and `#{v}` use
it. It is a hook rather than something to call on a parameter: a number's
text is made by the runtime and has nowhere to be allocated from, so a body
bounded by `Print` hands the value to `println`, and does not call
`to_string` on it.

### Operators

On a type the program declares, an operator is the method of a trait:
`a + b` is `a.add(b)`, `-a` is `a.neg()`, `a < b` is `a.compare(b) < 0`.

```gyro
impl Add for Vector2 {
  fn add(self, other: Self): Self {
    return Vector2{ x: self.x + other.x, y: self.y + other.y }
  }
}
```

The traits take and give `Self`: both operands and the result are one type,
so a vector times a scalar is not an `impl Mul` (it is a method, `v.scaled(2.0)`).
Wrapping arithmetic, the bitwise operators and shifts belong to the integers
and are not traits. The numbers' own overflow rules are unchanged.

On the built-in types the methods exist too — `3.add(4)`, `x.eq(y)` — so a
generic body that calls `a.add(b)` instead of `a + b` works for both.

### Iteration

`for x in thing` over a type the program declares goes through `Iterator<T>`:

```gyro
impl Iterator<i32> for Countdown {
  fn next(*self): (i32, bool) {
    if self.from == 0 {
      return 0, false
    }
    var current = self.from
    self.from -= 1
    return current, true
  }
}

var ticks = Countdown{ from: 3 }
for tick in ticks {
  println(tick)
}
```

`false` ends the loop. The loop works on its own copy of the value.

### Indexing

`value[i]` on a type that implements `Index<T>` calls `at` and reads or
writes through the address it returns, so an index is a place on it as it is
on an `Array<T>`: `grid[4] = 9`, `grid[1] += 2`, `&grid[4]`.

## Checking a default

A default body is checked once with `Self` opaque and bounded by the trait,
so it may call the trait's own methods and nothing else of `Self`; then once
for every type that takes it, with `Self` bound to that type.

## Why traits are nominal

A type implements a trait because its author said so. Having a method of the
right name is not enough, since a method of the right name can mean
something else. The alternatives and the reasons are in
[`decisions.md`](decisions.md) under "Traits".
