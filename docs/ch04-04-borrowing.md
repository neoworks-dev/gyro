# 4.4. Borrowing

Gyro separates **what owns storage** from **what looks at it**, and the
separation is in the types and in the parameters.

| type | what it is |
| --- | --- |
| `T[]` | a slice: a pointer and a length, viewing elements it does not own |
| `T[N]` | a fixed-size array: N elements of storage, a value |
| `Array<T>` | a dynamic array: owns a buffer, grows, freed when destroyed |
| `string` | a view of UTF-8 bytes |
| `String` | an owning text builder, freed when destroyed |
| `dyn Trait` | a trait value: a pointer to a value and the table of its methods, viewing the value ([chapter 10.2](ch10-02-trait-values.md)) |

## Three ways to pass a value

A parameter says what the function does with its argument, and the call site
looks the same in all three cases:

```gyro
fn total(xs: Array<i32>): i32 { ... }       // borrows: reads, the caller keeps it
fn sort(xs: inout Array<i32>) { ... }        // inout: may change it in place
fn keep(xs: Array<i32>!) { ... }             // takes it: the caller's is gone
```

- **A plain parameter borrows.** The function may read the argument and may
  not change it, move it out or hand it to a parameter that takes. For a
  plain value this is no different from a copy, which is how it has always
  behaved.
- **`inout` borrows exclusively.** The function may change the argument, and
  for as long as the call runs, nothing else can see it: the same value
  cannot be passed twice to one call, once as `inout`, and a view of it may
  not be in use across the call. A method that changes its receiver takes
  `inout self`.
- **`T!` takes ownership** ([chapter 4.1](ch04-01-ownership.md)). The
  argument moves into the call, and the function destroys it, stores it or
  hands it on.

## Views

A slice, a `string` and a `dyn` value are views: they look at storage some
owner holds. Passing a view allocates nothing, and a slice can view a
fixed-size array, an `Array`, a literal or memory a library hands over:

```gyro
fn total(values: i32[]): i32 { ... }

let buf = Array<i32>::new()
buf.push(4)

total(buf.slice())
total([1, 2, 3])
```

**A view never outlives what it views, and what it views does not change
under it.** While a view of a value is still going to be used, that value
may not be moved, assigned, destroyed, passed as `inout` or changed by a
method that takes `inout self`. "Still going to be used" means up to the
view's last use, not to the end of its block:

```gyro
let words = Array<string>::new()
words.push("short")

let first = words.slice()[0] // first views words' buffer
words.push("longer")        // error: words changes while first is still used
println(first)
```

```gyro
let first = words.slice()[0]
println(first)              // the last use of first
words.push("longer")        // fine: first is never used again
```

The rule is what stops the classic dangling slice: a `push` may move an
array's buffer, and a view taken before it would point at memory that was
freed. Here the `push` is refused instead.

Writing through a slice changes the storage it views, so it counts as a
change to the owner: two views of the same storage can both be used at once
only if neither writes.

## Returning a view

A function may return a view of something it was given, and the compiler
works out from what:

```gyro
fn first(xs: i32[]): i32[] {
    return xs[0..1] // the result views xs: the only view parameter
}

fn worse(): i32[] {
    let xs = [1, 2, 3]
    return xs // error: that storage ends with this function
}
```

When a function has one parameter that is or holds a view, a returned view
comes from it. When it has none, a returned view can only be of something no
frame owns, such as a string literal. When it has several, the signature
says which, with `from`:

```gyro
fn pick(a: i32[], b: i32[]): i32[] from a {
    return a
}
```

The caller then treats the result as a view of the argument passed for `a`,
with all the rules above.

A struct, a tuple or an `any` holding a view is a view too: it carries the
view out of a function, and it may not outlive what it views.

```gyro
struct Window { data: i32[] }

fn window(xs: i32[]): Window {
    return Window{ data: xs } // the result views xs
}
```

## Narrowing a slice

`xs[lo..hi]` is a **window** onto the storage `xs` already views: the same
elements, seen through a shorter view. The range is half-open, so `xs[1..4]`
is elements 1, 2 and 3.

```gyro
let xs = [10, 20, 30, 40, 50]
total(xs[1..4])     // 20 + 30 + 40
total(xs[2..])      // to the end
total(xs[..2])      // from the start
```

Either end may be left off, but not both: `xs[..]` names the value it is
applied to, so it is refused rather than given a second meaning. Nothing is
copied and nothing is allocated, and writing through a window writes the
storage.

The bounds are checked. `lo <= hi <= len()` or the program panics, naming
both bounds and the length.

A window is a **value, not a place**. `xs[0..2] = ys` is an error and so is
`&xs[0..2]`. Its elements are places, as they always were.

**A `string` does not narrow.** Its length is in bytes and its content is
UTF-8, so a byte range could cut a code point in half. Narrowing text has to
say which unit it counts in, and that is not decided
([appendix G](appendix-g-known-gaps.md)).

A slice over memory that does not come from Gyro, such as a buffer a library
hands over as an address and a length, is made with `slice_from_raw` from
the `unsafe` module ([chapter 4.7](ch04-07-unsafe.md)), and nothing checks
how long that memory lives.

## Lengths and indexes are `usize`

`len()` returns `usize` and `[]` indexes with `usize`. Because arithmetic
panics on overflow, `xs.len() - 1` on an empty slice panics instead of
wrapping around to a huge number. Indexing out of range panics with the
length in the message.

There is no `append`: a view cannot grow the storage it looks at. Growth is
`Array<T>::push` ([chapter 8.1](ch08-01-array.md)).

## Strings are the same arrangement over bytes

`string` is a view of UTF-8 bytes and `String` is the owning builder. `len()`
counts bytes, and `runes()` and `bytes()` **copy** into a new owning value,
so neither borrows its receiver. [Chapter 8.2](ch08-02-strings.md) has the
text methods.
