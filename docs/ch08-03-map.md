# 8.3. `Map<K, V>`

The hash map. Like `String`, it is **ordinary Gyro over `Array<T>`**:
buckets of `(K, V)` pairs chained on the derived `hash()`.

```gyro
import { println, Map } from "stdlib"

var ages = Map<string, i32>::new()
defer ages.free()

ages.insert("ada", 36)

var age = ages.get("ada")
if age != null {
  println(age)
}
```

## Any key that supports `==` works

There is no `Hashable` to implement, because `hash()` is derived wherever
`==` is and over the same parts: a struct hashes its fields, a tuple its
elements, an enum its tag and payload. Two values that compare equal
therefore hash equal without anything being written down
([chapter 3.2](ch03-02-data-types.md)).

A type that wants a different rule declares its own `hash(): u64`, which
displaces the derived one — and then owns keeping the two consistent.

Pointers are the exception: `==` accepts them and `hash` does not, because
an address is not the same from run to run.

## The surface

| method | notes |
| --- | --- |
| `Map<K, V>::new(): Map<K, V>` | |
| `insert(*self, key: K, value: V)` | replaces on an equal key |
| `get(self, key: K): V?` | null on a miss |
| `contains(self, key: K): bool` | |
| `remove(*self, key: K): bool` | reports whether the key was present |
| `len(self): usize`, `is_empty(self): bool` | |
| `keys(self): Array<K>` | a snapshot, in deterministic bucket order |
| `free(*self)` | |

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
