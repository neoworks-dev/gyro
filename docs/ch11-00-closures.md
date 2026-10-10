# 11. Closures and function values

A closure is an anonymous function, written with the same `fn` and the same
`:` result marker as a declaration. There is one spelling of "function" in
the language.

```gyro
var double = fn(a: i32): i32 { return a * 2 }
var greet  = fn() { println("hi") }
var later  = async fn(): i32 { return 1 }
```

The type a closure inhabits is the function type that already exists:
`fn(i32): i32`, or `async fn(): i32` for an async closure. In type position
`fn(A): B` is a type; in expression position `fn(a: A): B { ... }` is a
value. The two never collide.

## Two shorthands

Parameter and result types may be omitted when the expected function type is
known from context, and a body consisting of one expression needs neither
braces nor `return`:

```gyro
xs.map(fn(x) x * 2)
xs.filter(fn(x) x > 0)
```

"Known from context" includes a generic call, where the context is the
instantiation ([chapter 10](ch10-00-generics.md)).

## Capture is by copy

A closure captures the locals it uses **by copy**, implicitly. Nothing is
declared:

```gyro
var factor: i32 = 3
var scale = fn(x: i32): i32 { return x * factor }
println(scale(5))      // 15
```

The capture list exists for exactly one purpose: **transferring ownership**.

```gyro
var node = alloc<Node>(heap())
var release = fn[move node]() { free(heap(), node) }   // ownership moves in
```

`fn[move node]` moves the binding into the closure, which is what lets the
leak analysis see an allocation escape
([chapter 4.2](ch04-02-leaks-and-defer.md)). `move` is contextual — an
ordinary identifier everywhere else.

Copying is safe for a value and not for a reference, and that difference is
where the concurrency warning comes from: copying a `*T` copies the pointer,
and copying a `T[]` copies a pointer and a length over storage it does not
own, so both threads then reach the same memory
([chapter 13.1](ch13-01-threads.md)).

## Closures are why `spawn` is not a keyword

```gyro
import { heap, spawn } from "mem"

spawn(heap(), fn() produce(values, 10))
```

`spawn` is an ordinary library function taking a closure, and the closure is
what carries the arguments across to the new thread. It used to be grammar;
closures made the grammar unnecessary. **Prefer a library function over a
keyword** is a rule the language applies to itself. It takes an allocator
because the thread outlives the function that started it, so the closure has
to be copied out of that function's frame ([below](#a-closure-lives-in-its-frame)).

## Passing functions around

```gyro
fn apply(values: i32[], f: fn(i32): i32): Array<i32> {
  var out = Array<i32>::new(heap())
  for value in values {
    out.push(f(value))
  }
  return out
}

fn fold(values: i32[], start: i32, f: fn(i32, i32): i32): i32 {
  var total = start
  for value in values {
    total = f(total, value)
  }
  return total
}

var doubled = apply([1, 2, 3], fn(x) x * 2)
defer doubled.free()

var sum = fold([1, 2, 3, 4], 0, fn(acc, x) acc + x)
```

## A `fn` is two words; `code(..)` is one

A `fn` value is **the code and whatever it captured** — two words. That is
what makes closures possible, and it is also why a `fn` cannot cross into C:
a C signature has nowhere to put the second word.

`code(A, B): C` is the other type: the address of a function and nothing
beside it, which is exactly C's function pointer. `&f` on a declared
function produces one:

```gyro
fn big(v: i32): i32 {
  if v > 5 { return 1 }
  return 0
}

var kept = count_kept(&big)      // C's loop, this program's decision
```

The two are separate types rather than one type with a flag, because a type
that meant "a function value that promises not to capture" would be a
promise the type does not make and the checker would have to keep making
instead.

`&f` needs `f` to have one address to take, which rules out four things:

| what | why |
| --- | --- |
| a generic function | one address per instantiation, not one function |
| a method | needs a receiver |
| an `async` function | run by this program's scheduler, not by whoever holds the address |
| a variadic function | carries an argument pack a bare address cannot |

A nullable code pointer needs parentheses — `(code(i32): i32)?` — because a
`?` written after the result would bind to the result. The same grouping
applies to a `fn` type and to a parenthesized tuple result.
[Chapter 15](ch15-00-c-interop.md) is where code pointers get used.

## A closure lives in its frame

A closure that captures something keeps what it captured in a record, its
**environment**, and the record is stored in the stack frame of the function
that wrote the closure, the way a local struct is. Writing a closure
allocates nothing. The `fn` value is still two words, the code and a pointer
to that record, and a closure that captures nothing has a null pointer and
no record at all. A named function taken as a value has none either.

```gyro
var factor = 3
var scale = fn(x: i32): i32 { return x * factor }   // the record is in this frame
```

Every copy of the value shares the one record, so a closure's mutations of
what it captured accumulate across calls.

The consequence is the one a slice of an array literal already has: **the
closure borrows the frame.** The borrow analysis
([chapter 4.4](ch04-04-slices-and-views.md)) treats a capturing closure as a
view of the function that wrote it, and a `return` of one is an error:

```gyro
fn adder(n: i32): fn(i32): i32 {
  return fn(x) x + n     // error: this returns a view into storage that ends with this function
}
```

Passing a closure down to `apply` or `fold` above is exactly what the frame
is for: they run while the frame is alive, and nothing is allocated.

### `keep` and `release`

A closure that has to live longer than its frame is copied out of it into an
allocator with `keep`, from `mem`:

```gyro
export fn keep<F>(a: Allocator, f: F): F
export fn release<F>(a: Allocator, f: F)
```

`keep(a, f)` allocates the record from `a`, copies it, and answers a value
with the same code and the new record. `release(a, f)` gives that record
back. `F` has to be a function type; anything else is an error. A closure
with a null pointer is returned unchanged by `keep` and ignored by
`release`, so neither allocates for a closure that captured nothing.

```gyro
import { keep, release, Allocator } from "mem"

fn adder(a: Allocator, n: i32): fn(i32): i32 {
  return keep(a, fn(x: i32): i32 { return x + n })   // copied out of this frame into a
}
```

After `keep` the closure's storage is `a`'s, and the borrow analysis no longer
ties it to the frame. The copy is a copy: it has its own record, so mutations
do not reach the original. A `fn[move node]` capture list moves the binding
into the record and into the copy, as it does into the record in the frame
([chapter 4.2](ch04-02-leaks-and-defer.md)). `release` has to be given the
allocator the closure was kept in, and only a closure `keep` returned; giving
it a frame closure frees memory that was never allocated.

Every record starts with a header word holding its size in bytes. That is
what lets `keep` be ordinary Gyro: the type `fn(i32): i32` does not say how
big the record is, and the header does. Records are word aligned.

### Storing a closure

The analysis follows a closure through locals, returns, struct fields and
containers, the way it follows a slice of an array literal
([chapter 12](ch12-00-effect-rows.md)). A closure that borrows the frame
cannot be returned, inside a struct or a container as well as by itself, and
cannot be stored into anything that outlives the frame: a parameter, the
receiver, a module-level variable. **Storing a closure takes `keep`.**

```gyro
fn on_open(handlers: Array<fn(): i32>, n: i32) {
  handlers.push(fn(): i32 { return n })              // error: n lives in this frame
  handlers.push(keep(heap(), fn(): i32 { return n }))   // fine
}

fn parser_for(a: Allocator, depth: i32): Handlers {
  return Handlers{
    on_open: keep(a, fn(): i32 { return depth }),
    on_close: keep(a, fn(): i32 { return depth + 1 }),
  }
}
```

A closure written in a loop body and pushed into a container declared
outside the loop is refused even when the container never leaves the
function. The loop writes one record in the frame, so without `keep` every
element would point at it and see the last iteration's captures.

```gyro
for i in 0..3 {
  fns.push(fn(): i32 { return i })               // error: one record, shared
  fns.push(keep(a, fn(): i32 { return i }))      // fine: one record each
}
```

`keep` is generic over the closure's type, so a closure written inside the
call states its own parameter and result types. A closure that captures
nothing has no record and stores freely. What is
still not followed is a closure handed to a function that stores it
([appendix G](appendix-g-known-gaps.md)).

Futures and channels are not part of this; they still use the process heap
([appendix G](appendix-g-known-gaps.md)).

A closure that allocates does so from an `Allocator` it captured like any
other local, or was handed as a parameter ([chapter 4.5](ch04-05-allocators.md)).
Nothing is captured on its behalf.
