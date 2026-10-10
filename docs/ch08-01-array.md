# 8.1. `Array<T>`

`Array<T>` owns a buffer, grows, and is freed explicitly. It is the
counterpart to `T[]`, which borrows and cannot grow
([chapter 4.4](ch04-04-slices-and-views.md)).

An array is made with an allocator and **remembers it**
([chapter 4.5](ch04-05-allocators.md)). Its header and its buffer come from
that allocator, `push` grows the buffer through it, and `free` hands the
buffer back to it. Nothing after `new` names an allocator, which is what
keeps an array from being freed to one it did not come from.

```gyro
import { println, Array } from "stdlib"
import { heap } from "mem"

var buf = Array<i32>::new(heap())
defer buf.free()

buf.push(4)
buf.push(9)
println(buf.len())      // 2
println(buf[0])         // 4
```

## The surface

| method | row | notes |
| --- | --- | --- |
| `Array<T>::new(a: Allocator): Array<T>` | `!{}` | records `a` |
| `Array<T>::with_capacity(a: Allocator, capacity: usize): Array<T>` | `!{}` | records `a`; the first buffer holds `capacity` elements |
| `push(*self, value: T)` | `!{}` | may grow the buffer, from the recorded allocator |
| `pop(*self): (T, bool)` | `!{}` | the flag reports whether there was one |
| `get(self, index: usize): T` | `!{}` | a **copy** of the element; bounds-checked |
| `set(*self, index: usize, value: T)` | `!{}` | |
| `at(self, index: usize): *T` | `!{}` | the element's own address; bounds-checked |
| `len(self): usize` | `!{}` | |
| `is_empty(self): bool` | `!{}` | ordinary Gyro over `len` |
| `slice(self): T[]` | `!{ &self }` | a view of the array's own elements |
| `free(*self)` | `!{}` | returns the buffer to the recorded allocator |

`Array<T>` is ordinary Gyro in the bundled `stdlib`, written over the
`unsafe` module ([chapter 4.6](ch04-06-unsafe.md)) and the allocator
([chapter 4.5](ch04-05-allocators.md)). An array is a handle: a pointer to a
header holding the buffer, the length, the capacity and the allocator, so a
copy of the value is the same array. The header stays allocated after `free`,
which is how a second `free` and a use after `free` are caught. `pop` on an
empty array answers a zero-filled element beside `false`, since Gyro has no
other value to put there.

## Indexing

`xs[i]` is a **place**, not a value:

```gyro
var xs = Array<Point>::new(heap())
defer xs.free()
xs.push(Point{ x: 1, y: 2 })

println(xs[0].x)     // 1
xs[0].x = 9          // writes the array
xs[1] = Point{ x: 3, y: 4 }
bump(&xs[0].x)       // a pointer into the buffer
```

It lowers through `at`, so an index reaches the element where it lies.
That is what separates it from `get`, which answers with a **copy** —
`xs.get(0).x = 9` mutates a copy and nothing else. Both stay: `get` is
what you want when a copy is what you want, and it is the only one of the
two that is safe to hold across a `push`.

**An element address is into the buffer, and `push` may move it.** Growing
the array reallocates, exactly as `&vec[i]` in C is invalidated by
`push_back`. Nothing detects that.

`xs[lo..hi]` narrows it, through the same view `slice()` hands out, so a
window borrows the array's buffer and copies nothing
([chapter 4.4](ch04-04-slices-and-views.md)).

Every form is bounds-checked and panics with the length in the message,
including taking an address: the address is refused where it is made
rather than where it is later written through.

## Borrowing an array

`slice()` hands out a view that borrows the array's own elements — which is
why its row says `&self`, and why a function returning that view has to name
where it came from:

```gyro
export fn Stack::view(self): i32[] !{ &self } {
  return self.items.slice()
}
```

The view stays valid exactly as long as the array's buffer does. Free the
array and the slice dangles; nothing detects that
([chapter 4.4](ch04-04-slices-and-views.md)).

Iterating is the same view:

```gyro
for value in buf.slice() {
  println(value)
}
```

## Freeing

`free()` returns the buffer to the allocator the array was made with. The
header stays behind, so a second `free` is still caught. There are no
destructors, so this is a call you write — usually as a `defer` on the line
after construction. An array made in an arena needs none, because the arena
takes everything back at once:

```gyro
var out = Array<i32>::new(heap())
defer out.free()
```

An `Array<T>` returned from a function is the caller's to free:

```gyro
fn apply(values: i32[], f: fn(i32): i32): Array<i32> {
  var out = Array<i32>::new(heap())
  for value in values {
    out.push(f(value))
  }
  return out
}

var doubled = apply([1, 2, 3], fn(x) x * 2)
defer doubled.free()
```

Note what the signature says: the input **borrows** and the result **owns**.
A function that means to give ownership away returns `Array<T>`, never a
slice.

## Elements are stored the way memory looks

An `Array<T>` records its element size when it is created, and stores
elements at C's stride for `T` — so `Array<u8>` is byte-strided and
`Array<Point>` holds the same bytes a C array of that struct would. That is
what makes `slice()` a borrow of the array's own storage rather than a copy
([appendix E](appendix-e-native-backend.md)).

## Sorting

There is none, yet. See [appendix G](appendix-g-known-gaps.md).
