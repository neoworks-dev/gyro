# 5.1. Structs

A struct declares data and nothing else. Behaviour lives beside it, as
free-standing methods ([chapter 5.2](ch05-02-methods.md)).

```gyro
export struct Stack {
  export capacity: i32
  top: i32
  items: Array<i32>
}
```

Visibility is per declaration **and** per field. `export` on the struct
makes the type visible to importers; `export` on a field makes that field
visible. A field without it is private to the module, whatever the struct's
own visibility is.

## Literals come in two forms

```gyro
Point{ x: 1, y: 2 }   // named
Pair<i32>{ 1, 2 }     // positional, in declaration order
```

A literal is one or the other, never a mix, and it must set **every** field.

## Private fields are what make invariants enforceable

**A struct literal is only legal where every field is visible.** A type with
a private field can therefore only be constructed through its own associated
functions:

```gyro
export fn Stack::new(capacity: i32): Stack {
  return Stack{ capacity: capacity, top: 0, items: Array<i32>::new(heap()) }
}
```

Outside the module, `Stack{ ... }` does not compile, so `Stack::new` is the
only way in and whatever it establishes holds for every `Stack` that exists.
That is the whole mechanism — there are no constructors, no privacy
modifiers beyond `export`, and no way around it.

## Structs are values

Assignment copies. Passing one to a function copies it, unless the parameter
is a pointer or the receiver is `self` — which borrows and never copies,
however large the struct is.

Equality, `hash()` and printing are derived from the fields, field by field,
for a struct that asks: `impl Eq for Point`, `impl Hash for Point` and
`impl Print for Point` with no block ([chapter 10.1](ch10-01-traits.md)).
Without the impl a struct does not compare, hash or print.
`println(p)` on a `Point` that asked prints `Point{ x: 1, y: 2 }` unless its
`Print` impl supplies its own `to_string(): string`
([chapter 14.2](ch14-02-printing.md)).

## Generic structs

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

Type arguments infer from the fields where they can, so `Pair{ 1, 2 }` also
works ([chapter 10](ch10-00-generics.md)).

## A struct in a condition

Because a condition is followed by `{`, a bare struct literal cannot appear
at the top level of an `if`, `while` or `for..in` head. Parenthesize:

```gyro
if (Point{ x: 1 }).x > 0 { }
for x in (Counter{ 0, 5 }) { }
```

## Structs and borrows

A struct **carries the origins of its fields**. A struct built over a local
array holds frame storage, and a view of its field is a view of this frame:

```gyro
struct View { data: i32[] }

fn bad(): i32[] {
  var xs = [1, 2, 3]
  var v = View{ data: xs }
  return v.data          // error: xs ends with this function
}
```

A heap buffer does not. `Stack{ items: Array<i32>::new(heap()) }` holds storage
that outlives the frame, and lending a view of it is fine — provided the row
says whose it is ([chapter 12](ch12-00-effect-rows.md)).

## Unions

Gyro declares no `union`. The untagged kind, one storage with several names
for it and nothing recording which is live, is C's, and a C `union` arrives
through an import as C declared it ([chapter 15](ch15-00-c-interop.md)).
Gyro's own sum type is the `enum`, which knows its variant, prints,
compares, and cannot be read wrong ([chapter 6.1](ch06-01-enums.md)).

## What a struct cannot do

There is no inheritance and no embedding. A struct is data; a contract it
keeps is a trait, written in an `impl Trait for Struct` block
([chapter 10.1](ch10-01-traits.md)), and a struct's own methods are not in
one ([chapter 5.2](ch05-02-methods.md)).

A fieldless struct marked `@extern` is a special case: an opaque handle to
something a C library owns ([chapter 15](ch15-00-c-interop.md)).
