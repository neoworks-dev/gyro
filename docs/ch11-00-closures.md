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
import { spawn } from "stdlib"

spawn(fn() produce(values, 10))
```

`spawn` is an ordinary library function taking a closure, and the closure is
what carries the arguments across to the new thread. It used to be grammar;
closures made the grammar unnecessary. **Prefer a library function over a
keyword** is a rule the language applies to itself.

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

## What closures cost

A capturing closure's environment is real heap storage — a record the
lowering allocates and the closure carries a pointer to. Nothing frees it
today, and it comes from the process heap rather than from an allocator
the program chose ([appendix G](appendix-g-known-gaps.md)).

A closure that allocates does so from an `Allocator` it captured like any
other local, or was handed as a parameter ([chapter 4.5](ch04-05-allocators.md)).
Nothing is captured on its behalf.
