# 8.3. `Map<K, V>`

The hash map. Like `String`, it is **ordinary Gyro over `Array<T>`**:
buckets of `(K, V)` pairs chained on `hash()`. Its declaration is
`Map<K: Eq & Hash, V>`: a key is any type that is `Eq` and `Hash`.

```gyro
import { println, Map } from "stdlib"
import { heap } from "mem"

var ages = Map<string, i32>::new(heap())
defer ages.free()

ages.insert("ada", 36)

var age = ages.get("ada")
if age != null {
    println(age)
}
```

## A key is `Eq` and `Hash`

The numbers, `string`, `bool` and `rune` are both, and so are tuples of them.
A struct or enum the program declares is a key once it says so:

```gyro
struct Point {
    x: i32
    y: i32
}

impl Eq for Point
impl Hash for Point

var visited = Map<Point, bool>::new(heap())
```

With no block each is derived from the fields: a struct hashes its fields, a
tuple its elements, an enum its tag and payload, so two values that compare
equal hash equal without anything being written down
([chapter 10.1](ch10-01-traits.md)). A map keyed by a type that lacks either
is an error where the map type is written.

A type that wants a different rule writes the impl out with a block, which
replaces the derived one — and then owns keeping `Eq` and `Hash` consistent.

Pointers are the exception: `==` accepts them and `hash` does not, because
an address is not the same from run to run.

## The surface

| method | notes |
| --- | --- |
| `Map<K, V>::new(a: Allocator): Map<K, V>` | records `a`; the table and every bucket come from it |
| `insert(*self, key: K, value: V)` | replaces on an equal key |
| `get(self, key: K): V?` | null on a miss |
| `contains(self, key: K): bool` | |
| `remove(*self, key: K): bool` | reports whether the key was present |
| `len(self): usize`, `is_empty(self): bool` | |
| `keys(self): Array<K>` | a snapshot from the map's allocator, in deterministic bucket order |
| `free(*self)` | returns the table and every bucket to the recorded allocator |

`keys()` copies into a fresh `Array<K>` that the caller frees. The order is
bucket order — deterministic across runs and tiers, but not insertion order
and not sorted.

Like `Array<T>`, a map is freed explicitly.

## Optionals do not nest

A `V` that is itself optional is allowed, and it has one consequence worth
knowing: `get()` on a `Map<K, V?>` answers `null` both for a miss and for a
stored `null`. `contains(key)` is the signal that separates them
([chapter 4.3](ch04-03-nullability.md)).

## Counting, the usual shape

```gyro
var seen = counts.get(word)
if seen == null {
    counts.insert(word, 1)
} else {
    counts.insert(word, seen + 1)
}
```

`seen` is an `i32?`, so the arithmetic only compiles inside the branch where
it has been narrowed. There is no "get or default" — a default is a decision
the caller makes, and it is one line.
