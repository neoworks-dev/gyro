# 8.2. Strings and text

`string` is a **borrowed view** of UTF-8 bytes. `String` is the owning
builder. The relationship is the one `T[]` has with `Array<T>`
([chapter 4.4](ch04-04-slices-and-views.md)), and the same rules follow from
it: a `string` never owns, so a function handing one back has to say where
the view came from.

## The built-in surface

These are part of the type and need no import:

| method | what it does |
| --- | --- |
| `len(): usize` | counts **bytes** |
| `runes(): rune[]` | code points, as a copy |
| `bytes(): u8[]` | the bytes, as a copy |
| `compare(other): i32` | lexicographic by byte |

`runes()` and `bytes()` **copy**, and therefore borrow nothing — a string is
immutable and shared while a slice is neither, and a rune is four bytes
where UTF-8 is not. That is why they carry `!{ alloc }` and no `&` entry.

Indexing a string with `[]` is not a thing: bytes and code points are
different questions, and each has a method that answers it.

## Interpolation

```gyro
println("max is #{MAX}, half of that is #{MAX / 2}")
```

`#{expr}` takes arbitrary Gyro and shows it the way `println` would, which
means a struct prints as its derived representation and an `any` prints as
whatever is inside it ([chapter 14.2](ch14-02-printing.md)). Escape the
sequence as `\#{`.

A literal with a `#{}` is not a `string`: it is a `Format`, which records
its pieces and allocates nothing. `println` streams it; `format(a, ...)`
from `mem` builds it into the allocator `a` and hands back a `string`.
Because it borrows what it names, a `Format` can only be passed as an
argument and never kept ([chapter 14.2](ch14-02-printing.md)). A literal
without a `#{}` is a plain `string` in module storage.

## Building text

```gyro
import { println, String } from "stdlib"

var out = String::new()
defer out.free()
out.push("hello")
out.push_rune('!')
println(out.text())
```

| method | notes |
| --- | --- |
| `String::new()`, `String::with_capacity(n)`, `String::of(text)` | |
| `push(*self, text: string)` | appends bytes |
| `push_rune(*self, value: rune)` | encodes one code point as UTF-8 |
| `len(self): usize` | bytes |
| `text(self): string` | reads the buffer back out |
| `to_string(self): string` | same, and what printing uses |
| `free(*self)` | |

`String` is ordinary Gyro over `Array<u8>`. `push_rune` is a UTF-8 encoder
written in the language, and a surrogate or out-of-range value becomes
U+FFFD — matching what `runes()` produces for an invalid sequence.

## The text methods are extensions

`split`, `trim`, `contains`, `starts_with`, `ends_with`, `index_of`,
`substring` and `repeat` are extension methods on `string`, written in Gyro,
and **have to be imported**:

```gyro
import { split, trim, starts_with } from "stdlib"

var clean = line.trim()
if clean.starts_with("#") { continue }
var parts = clean.split(" ")
defer parts.free()
```

`split` returns an owning `Array<string>` — free it. `index_of` returns
`usize?`, so a miss is a null you have to check
([chapter 4.3](ch04-03-nullability.md)).

`join(parts, sep)` stays a plain function rather than an extension, because
its receiver would be the slice rather than a string.

The whole of this layer is built on `Array<u8>`, `bytes()` and one
intrinsic, `from_utf8(bytes: u8[]): string`. A bundled module is only a
declaration stub where a declaration has no body — and here almost nothing
is.

## Two costs, known and accepted

- **`from_utf8` and `text()` copy.** Building text allocates, and nothing
  frees those allocations yet ([appendix G](appendix-g-known-gaps.md)).
- **A returned `string` field borrows its struct.** `string` is a view, so
  an accessor handing one back has to say so:

```gyro
fn host(c: Config): string !{ &c } {
  return c.host
}
```

That is not a quirk of the checker — the caller really does have to keep `c`
alive to use the result ([chapter 12](ch12-00-effect-rows.md)).
