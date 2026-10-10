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
| `compare(other): i32` | lexicographic by byte |

## Copying out: `runes` and `bytes`

`runes` and `bytes` are extension methods from `stdlib`, and they take the
allocator the copy is made from:

| method | what it does |
| --- | --- |
| `runes(a: Allocator): rune[]` | code points, as a copy |
| `bytes(a: Allocator): u8[]` | the bytes, as a copy |

```gyro
import { runes } from "stdlib"
import { heap } from "mem"

for r in text.runes(heap()) { ... }
```

Both **copy**, and therefore borrow nothing — a string is immutable and
shared while a slice is neither, and a rune is four bytes where UTF-8 is
not. That is why they carry `!{}` and no `&` entry. The copy is a block
from `a`, so in an arena it is reclaimed with the arena; from `heap()` it
is not reclaimed, because a slice has no way to hand its block back.

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
import { heap } from "mem"

var out = String::new(heap())
defer out.free()
out.push("hello")
out.push_rune('!')
println(out.text(heap()))
```

A `String` is made with an allocator and records it, as an `Array<T>`
does ([chapter 8.1](ch08-01-array.md)): `push` grows its bytes from it,
and `free` returns them to it. `text` builds the `string` it hands back
from the allocator it is passed, which need not be the one the builder
uses.

| method | notes |
| --- | --- |
| `String::new(a)`, `String::with_capacity(a, n)`, `String::of(a, text)` | record `a` |
| `push(*self, text: string)` | appends bytes |
| `push_rune(*self, value: rune)` | encodes one code point as UTF-8 |
| `len(self): usize` | bytes |
| `text(self, a: Allocator): string` | reads the buffer back out into a block from `a` |
| `to_string(self): string` | same from the process heap, and what printing uses |
| `free(*self)` | returns the bytes to the recorded allocator |

`String` is ordinary Gyro over `Array<u8>`. `push_rune` is a UTF-8 encoder
written in the language, and a surrogate or out-of-range value becomes
U+FFFD — matching what `runes()` produces for an invalid sequence.

## The text methods are extensions

`split`, `trim`, `contains`, `starts_with`, `ends_with`, `index_of`,
`substring` and `repeat` are extension methods on `string`, written in Gyro,
and **have to be imported**. The ones that build text (`split`, `trim`,
`substring`, `repeat`) take the allocator they build from as their first
argument after the receiver, and a function allocates exactly when it is
handed one. The predicates `contains`, `starts_with`, `ends_with` and
`index_of` only answer a question, so they take none and compare the two
strings' bytes in place:

```gyro
import { split, trim, starts_with } from "stdlib"
import { heap } from "mem"

var clean = line.trim(heap())
if clean.starts_with("#") { continue }
var parts = clean.split(heap(), " ")
defer parts.free()
```

`split` returns an owning `Array<string>` made from the allocator it was
passed — free it. `index_of` returns
`usize?`, so a miss is a null you have to check
([chapter 4.3](ch04-03-nullability.md)).

`join(a, parts, sep)` stays a plain function rather than an extension,
because its receiver would be the slice rather than a string.

The whole of this layer is built on `Array<u8>`, `bytes`, a private
non-copying view of a string's bytes and one intrinsic,
`from_utf8(a: Allocator, bytes: u8[]): string`. A bundled module is only a
declaration stub where a declaration has no body — and here almost nothing
is.

## Two costs, known and accepted

- **`from_utf8` and `text` copy.** The result is a block from the allocator
  passed in, and a `string` cannot hand it back, so text from `heap()` is
  not reclaimed ([appendix G](appendix-g-known-gaps.md)).
- **A returned `string` field borrows its struct.** `string` is a view, so
  an accessor handing one back has to say so:

```gyro
fn host(c: Config): string !{ &c } {
    return c.host
}
```

That is not a quirk of the checker — the caller really does have to keep `c`
alive to use the result ([chapter 12](ch12-00-effect-rows.md)).
