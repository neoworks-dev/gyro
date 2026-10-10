# 4.4. Slices, arrays and views

Gyro separates **what owns storage** from **what looks at it**, and the
separation is in the types.

| type | what it is |
| --- | --- |
| `T[]` | a slice: a pointer and a length, borrowing memory it does not own |
| `T[N]` | a fixed-size array: N elements of storage, a value |
| `Array<T>` | a dynamic array: owns a heap buffer, grows, freed explicitly |
| `string` | a borrowed view of UTF-8 bytes |
| `String` | an owning text builder, freed explicitly |
| `dyn Trait` | a trait value: a pointer to a value and the table of its methods, borrowing the value ([chapter 10.2](ch10-02-trait-values.md)) |

Passing a slice allocates nothing. It can view a fixed-size array, an owning
collection, a literal, or GPU device memory — the slice does not know or
care which.

```gyro
fn total(values: i32[]): i32 { ... }   // borrows

var buf = Array<i32>::new(heap())
defer buf.free()
buf.push(4)

total(buf.slice())
total([1, 2, 3])
```

An array literal such as `[1, 2, 3]` is a fixed-size array value in the
enclosing scope; a slice of it borrows that storage. That is why passing one
to a function is free, and why returning a view of one is an error
([chapter 12](ch12-00-effect-rows.md)).

## Narrowing a slice

`xs[lo..hi]` is a **window** onto the storage `xs` already views: the same
elements, seen through a shorter view. The range is half-open, like every
other range, so `xs[1..4]` is elements 1, 2 and 3.

```gyro
fn total(xs: i32[]): i32 { ... }

var xs = [10, 20, 30, 40, 50]
total(xs[1..4])     // 20 + 30 + 40
total(xs[2..])      // to the end
total(xs[..2])      // from the start
```

Either end may be left off — the container's own bound is used — but not
both: `xs[..]` names the value it is applied to, so it is refused rather
than given a second meaning.

**Nothing is copied and nothing is allocated.** A slice is a pointer and a
length, so narrowing one moves the pointer and shortens the length. Writing
through a window is writing to the storage:

```gyro
var window = xs[1..4]
window[0] = 7
println(xs[1])      // 7
```

A fixed-size array narrows too, and the result is a `T[]`: the length stops
being static, which is exactly what the type is for. So does an
`Array<T>`, through the view it already hands out
([chapter 8.1](ch08-01-array.md)).

The bounds are checked. `lo <= hi <= len()` or the program panics, naming
both bounds and the length — the two ways to be wrong are different
questions, and one of them (`xs[2..1]`, both inside the length) has nothing
an "index out of range" could say about it.

A slice over memory that does not come from a Gyro array, such as a mapped
buffer a library hands over as an address and a length, is made with
`slice_from_raw` from the `unsafe` module
([chapter 4.6](ch04-06-unsafe.md)).

A window is a **value, not a place**. `xs[0..2] = ys` is an error and so is
`&xs[0..2]`: there is no storage of its own to assign into or take the
address of. Its elements are places, as they always were.

**A `string` does not narrow.** Its length is in bytes and its content is
UTF-8, so a byte range could cut a code point in half and hand back a
`string` that is not text. Narrowing text has to say which unit it counts
in, and that is not decided ([appendix G](appendix-g-known-gaps.md)).

## There is no `append`

`T[]` borrows memory it cannot grow, so there is nothing sensible for
`append` to mean. Growth lives in `Array<T>::push`
([chapter 8.1](ch08-01-array.md)).

## Lengths and indexes are `usize`

`len()` returns `usize` and `[]` indexes with `usize`. Because arithmetic
panics on overflow, `xs.len() - 1` on an empty slice panics loudly instead
of wrapping around to a huge number and indexing into nothing.

Indexing out of range panics with the length in the message.

## Strings are the same arrangement over bytes

`string` is a borrowed view of UTF-8 bytes; `String` is the owning builder.

- `len()` counts **bytes**
- `runes()` iterates code points as `rune`
- `bytes()` yields the bytes as `u8[]`

Both `runes()` and `bytes()` **copy** — a string is immutable and shared and
a slice is neither, and a rune is four bytes where UTF-8 is not — so neither
borrows its receiver. [Chapter 8.2](ch08-02-strings.md) has the text
methods.

## The hazard that is accepted

**A slice can outlive the memory behind it, and nothing detects that.** Free
the `Array<T>` a slice views and the slice dangles, exactly as in C.

What the compiler *does* enforce is provenance at function boundaries: a
function returning `T[]` or `string` has to name in its effect row which
parameter the view came from, and returning a view of this frame's storage
is an error. That catches the common case — handing back a view of a local —
and says nothing about lifetime beyond the call.

```gyro
fn first(xs: i32[]): i32[] !{ &xs } {
  return xs        // fine: the row says the result views xs
}

fn worse(): i32[] {
  var xs = [1, 2, 3]
  return xs        // error: that storage ends with this function
}
```

Because a view never owns, a function that means to give the caller
ownership returns `Array<T>` or `String`, not a slice or a `string`. The
exception is a view of something static, like a string literal, which no
frame owns.

The view need not be the result. A struct, a tuple or an `any` holding one
carries it out of the function, so it takes the same row, and a view put
into a field or a container that outlives the frame is refused where it goes
in ([chapter 12](ch12-00-effect-rows.md)):

```gyro
struct Window { data: i32[] }

fn window(xs: i32[]): Window !{ &xs } {
  return Window{ data: xs }
}

fn remember(out: Array<i32[]>) {
  var buf = [1, 2, 3]
  out.push(buf)      // error: buf ends with this function, out does not
}
```

Two things stay outside it. A view stored by a function it was passed to is
not followed into that function, and nothing here notices that the memory
behind a view was freed while the view was still in use.
