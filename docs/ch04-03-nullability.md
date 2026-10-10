# 4.3. Nullability

A `*T` can never be null; only a `T?` can. The compiler refuses to
dereference a nullable value until it has been checked, and **narrows** the
type inside the check.

```gyro
fn find(id: i32): *Node? { ... }

var node = find(3)
// node.value          // error: node may be null
if node != null {
    println(node.value)  // narrowed to *Node here
}
```

Absence is expressed by the type, not by a value that any type can hold.
There is no bare `null` that fits everywhere — `null` is the absent case of
a `T?`, and nothing else.

## `?` binds to the outermost type

```gyro
*Node?      // a nullable pointer to a Node
*(Node?)    // a pointer to a nullable Node
```

This trips people up exactly once. The marker binds to the whole type, so
write the parentheses when you mean the other one.

## What narrowing follows

Narrowing is symbol-level and syntactic: it applies to a name, and the
checker follows the shapes a program actually uses.

- `!= null` and `== null`
- `&&`, so `if x != null && x.value > 0` works
- `!`, so `if !(x == null)` works
- `else`, so the negative branch of a null check narrows the other way
- early exits: `if x == null { return }` leaves `x` narrowed afterwards
- assignment **un-narrows** it again

A nullable value cannot be dereferenced, selected through, indexed or
iterated until it is narrowed. There is no operator that asserts a value is
non-null — the check *is* the assertion, and if you cannot phrase it the
compiler cannot see it.

## Optionals do not nest

`T??` is not a type. That is a design choice with a visible consequence in
`Map<K, V>`: a map whose values are themselves optional answers `null` from
`get()` both for a miss and for a stored `null`, and `contains(key)` is the
signal that separates them ([chapter 8.3](ch08-03-map.md)).

## Where nullables come from

- `Map<K, V>::get` answers `V?`
- `string::index_of` answers `usize?`
- `os::env` answers `string?`
- `os::readline` answers `string?`, null at end of file
- a C function returning a pointer that may be `NULL`
  ([chapter 15](ch15-00-c-interop.md))
- your own data structures: `next: *Node?` is how a list ends

## Under the hood

The lowering makes the guarantee explicit rather than trusting it: a
narrowed value still becomes an `opt.unwrap` instruction in METALS, which
aborts with `unwrap of a null value` if it is ever wrong. The checker's
proof and the run-time check are both there, in the same trapping style as
bounds checks and integer division ([appendix D](appendix-d-metals.md)).

For pointers this costs nothing in storage: `opt<ptr<T>>` is one machine
word, using the null address as the absent case, because no live pointer has
that value.
