# 3.2. Data types

Every type in the language, in one table. Most get a chapter of their own
later; this is the map.

| form | meaning |
| --- | --- |
| `i8 i16 i32 i64` | signed integers |
| `u8 u16 u32 u64` | unsigned integers |
| `usize` | pointer-width unsigned; lengths, indexes, `T[N]` sizes |
| `f32 f64` | floats |
| `rune` | a Unicode code point (32-bit) |
| `bool string void error` | other primitives |
| `any` | a value of any single type, carrying which |
| `anyptr` | an address whose target's type is forgotten; C's `void *` |
| `*T` | pointer, never null |
| `T?` | nullable |
| `T[]` | slice — a borrowed view |
| `T[N]` | fixed-size array |
| `chan<T>` | channel |
| `(A, B)` | tuple |
| `fn(A, B): C` | function |
| `async fn(A): B` | async function |
| `code(A, B): C` | the address of a function, with no environment; C's function pointer |
| `ast::Literal` | namespaced name |
| `Pair<i32>` | generic instantiation |

Integers carry their width in the name, deliberately: there is no `int`
whose size depends on the target. `usize` exists because a length and an
index are pointer-width by nature, and `rune` exists because a code point is
not a byte.

## Literals

```gyro
42        1_000_000     0xff      0b1010    0o755
3.14      1e9           1.5e-3
"text"    "hi #{name}"  'c'
true      false         null
(1, "a")                            // tuple
```

A character literal such as `'c'` has type `rune`. Strings interpolate with
`#{expr}`, where the expression is arbitrary Gyro; escape it as `\#{` to
write the sequence literally. The escapes are `\n`, `\t`, `\r`, `\0`, `\\`,
`\'`, `\"`, `\#`, `\xNN` and `\u{...}`.

## Numeric literals are context-typed

A numeric literal **takes the type the context requires**. `var h: u32 =
2166136261` needs no cast, and in `var bytes: u8[] = [104, 105]` the
elements are `u8`.

With no context, an integer literal is `i32` and a float literal is `f64`.
The default is a requirement like any other: `var x = 3000000000` is
rejected, because the type it would give `x` cannot hold the value. Write
`var x: i64 = 3000000000`.

Context never converts *typed* values — only literals adapt.

The context reaches through an expression built only from literals, so the
operands adapt too, and only the value that is finally *used* has to fit:

```gyro
var x: i8 = -128        // legal, though 128 alone is not an i8
var y: i64 = 1 << 40    // an i64 shift, not an overflowed i32 one
```

A shift is the case where nothing else can decide, because the count keeps
its own type: an untyped left operand takes its width from outside. In
`mask & (0xffffffff >> n)` with a `u32` mask, `0xffffffff` is a `u32`; `~0`
is likewise all ones at the width of whatever it meets.

Constant expressions fold during checking, so `var x: u8 = 200 + 56` is
caught at compile time rather than at run time.

## Conversions: `as`

`as` converts between numeric types (and `rune`): `x as i64`.

- Widening always succeeds.
- A narrowing conversion whose value does not fit the target **panics**,
  consistent with arithmetic overflow.
- Deliberate truncation is spelled with a mask first: `(x & 0xff) as u8`.

`as` is also how a value enters and leaves an `any`: `x as any` never fails,
and `v as i32` on an `any` panics when it holds something else — the same
shape as a narrowing conversion that does not fit
([chapter 14.1](ch14-01-any.md)).

**There are no implicit conversions between typed values**, with one
exception: an argument to a `...any` parameter is packed without writing
`as any`, because the call is what constructs the pack.

## Tuples

`(A, B)` is a first-class type: a variable, a field, a channel element.
Values are written `(a, b)` and read by destructuring or positional field
access.

```gyro
var pair = (200, null)
var code = pair.0
var a, b = pair

var results = chan<(string, error?)>()
results <- (body, null)
```

A multi-value `return a, b` is sugar for returning a tuple. `(expr)` with
one element stays plain grouping; there are no one-element tuples.

## Equality and ordering

`==` and `!=` work on primitives, pointers (identity), tuples and enums, and
on structs as compiler-derived field-wise comparison. Slices do not compare
with `==`; compare contents explicitly.

`hash(): u64` is derived wherever `==` is, and over the same parts: a struct
hashes its fields, a tuple its elements, an enum its tag and payload. Two
values that compare equal therefore hash equal without anything being
written down. A type that wants a different rule declares its own `hash`,
which displaces the derived one — and then it owns keeping the two agreed.
Pointers are the one thing `==` accepts and `hash` does not: `==` on a
pointer is identity, and an address is not the same from run to run.

The comparison operators `< <= > >=` work on **numeric primitives and
`rune` only**. Ordering anything else is spelled `a.compare(b)`, which
returns -1, 0 or 1. `string` carries one, ordering lexicographically by
byte — the order its code points are in — and a user type orders by writing
its own. That is also what makes a generic `max` work: the body calls
`compare`, and any type with `compare` fits ([chapter 10](ch10-00-generics.md)).

## Where the rest are covered

| type | chapter |
| --- | --- |
| `*T`, `T?` | [4.1](ch04-01-manual-memory.md), [4.3](ch04-03-nullability.md) |
| `T[]`, `T[N]`, `string` | [4.4](ch04-04-slices-and-views.md), [8.2](ch08-02-strings.md) |
| structs, enums | [5.1](ch05-01-structs.md), [6.1](ch06-01-enums.md) |
| `error` | [9](ch09-00-errors.md) |
| `fn`, `async fn` | [11](ch11-00-closures.md) |
| `chan<T>` | [13.2](ch13-02-channels.md) |
| `any` | [14.1](ch14-01-any.md) |
| `anyptr`, `code(..)` | [15](ch15-00-c-interop.md) |
