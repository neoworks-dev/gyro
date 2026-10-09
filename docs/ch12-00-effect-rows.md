# 12. Effect rows

A signature may carry an **effect row** describing what the function does
and what its result borrows. Both kinds of entry are checked: the concrete
effect `io` against what the body performs, and the borrow
entries against what the result views.

```gyro
fn add(a: i32, b: i32): i32                       // no row: unconstrained
export fn log(msg: string) !{ io }
export fn load(p: string): (string, error?) !{ io }
fn map<T, U>(xs: T[], f: fn(T): U !e): U[] !e     // effect-polymorphic
@gpu fn kernel(a: *f32) !{}                       // empty row
```

Forms: `!{}`, `!{ a, b }`, `!e` for a bare row variable, and `!{ a, ..e }`
for concrete effects plus a row variable.

The row goes after the result type, and it is the last thing in a signature.

## A declared row is a complete statement

A function that writes **any** row — even one holding only borrows —
promises that its body performs no concrete effect the row does not name. A
row without `io` promises no io.

## Allocation is not an effect

There is no effect for allocating. A function allocates when it is handed
an `Allocator` as a parameter ([chapter 4.5](ch04-05-allocators.md)), and
that is visible in its signature without a row to say it again. So `!{}`
says nothing about allocation: `alloc` and `free` carry it, a function
that takes an `Allocator` may be `!{}`, and so may `Array<T>::push`, which
grows its buffer.

A function with **no** row is unconstrained, exported or not. A row is how a
signature *states* an interface; it is never the only way to know what a
function does, because Gyro compiles whole programs and a rowless body can
simply be read.

```gyro
fn quiet(n: i32) !{} {
  println(n)         // error: println does io, which the row does not name
}

fn loud(n: i32) !{ io } {
  println("n = #{n}")   // fine: the row names io
}
```

**The vocabulary is closed.** `io` is the one concrete effect and
`&name` is a borrow entry; anything else in a row is an error rather than an
entry that quietly promises nothing.

## Where effects come from

What a body performs is judged from its sources:

- **A call** contributes the callee's declared row when it states one. When
  the callee states none, its effects are **inferred from its body** — in
  whatever module that body lives in, transitively. A rowless import is
  read, not assumed.
- **An intrinsic** — a declaration with no body anywhere — is the one thing
  that cannot be read. Without a row it is assumed to do `io`,
  which is why every intrinsic in the standard library writes one, down to
  `Array<T>::get(self, index: usize): T !{}`.
- **The built-in method surface** performs no effect: `runes()`, `bytes()`,
  `len`, `close`, `hash`, `compare` and `message` do no io.
- **The standard library states rows on its surface**: `println` is
  `!{ io }`, `Array<T>::push` is `!{}` (it may grow the buffer),
  `read_all` is `!{ io }`, and `mem`'s `alloc` and `free` are `!{}`.

A generic body's effects are judged per instantiation, like its borrows: a
rowless generic callee is read through its instantiations and contributes
the union of what they reach.

## The route is part of the diagnostic

A row is violated at a call the body makes, but the effect can arrive many
calls below it, in another module, and none of it is visible in the
signature the body called. So the error carries the route inference walked
to find it — a frame per hop, quoting the call that made it, the last
naming what performs the effect:

```text
error[E0611]: shape does io, which the row does not name
  --> render.gyro:38:3
   |
38 |   shape(message)
   |   ^^^^^
  --> render.gyro:34:3
   |
34 |   emit(message)
   |   ---- shape calls emit
  --> text.gyro:30:3
   |
30 |   println(message)
   |   ------- emit calls println, which does io
   |
   = help: name it in the row: !{ io }
```

Without it a `!{}` is reported wrong and the reason is left to be searched
for, which is the cost of inference reaching as far as it does. The frames
quote the source rather than citing it, because what they point at is
exactly the code the reader cannot see from the call being reported. A
callee that performs the effect itself has no route and gets no frames: the
message already names it. Two effects arriving by different routes are
shown separately; two arriving along the same calls are shown once.

A closure may write its own row, and its body is checked against it:

```gyro
var quiet = fn(n: i32) !{} {
  println(n)           // error: println does io, which the row does not name
}
```

## Borrows

An entry written `&name` says **the result is a view into that parameter**.
`T[]` and `string` borrow storage they do not own, so a function returning
one has to say where it came from:

```gyro
fn first(xs: i32[]): i32[] !{ &xs }
fn pick(a: i32[], b: i32[]): i32[] !{ &a }
fn spans(a: i32[], b: i32[]): i32[] !{ &a, &b }
fn head(s: string): string !{ io, &s }        // beside an effect
fn make(): i32[]                              // no &: owns its result
```

`&` reads the same way it does everywhere else — a reference to the thing
named. Each source carries its own `&`, so borrows and effect names never
run together in one list.

**This is always written out. There is no elision:** a row without any `&`
entry says the result is a view into nothing.

```gyro
fn good(xs: i32[]): i32[] !{ &xs } {
  return xs        // fine: the row says the result views xs
}

fn bad(xs: i32[]): i32[] {
  return xs        // error: the row names no source
}

fn worse(): i32[] {
  var xs = [1, 2, 3]
  return xs        // error: that storage ends with this function
}
```

Because a view never owns, **a function returning `T[]` or `string` needs a
`&` entry in practice.** There is no way to hand back storage through one; a
function that means to give the caller ownership returns `Array<T>`, or
`String` rather than `string`. The exception is a view of something static,
like a string literal, which no frame owns.

## The reach of it

`string` is a view, so an accessor handing back a string field borrows the
struct it came from:

```gyro
fn host(c: Config): string !{ &c } {
  return c.host
}
```

That is not a quirk of the checker — the caller really does have to keep `c`
alive to use the result. It is the price of `string` being a borrowed view
and of borrows being written out rather than inferred, and it is an accepted
ergonomic cost.

The name must be a parameter of the function, and that parameter has to be a
view itself — there is nothing to borrow from an `i32`. A borrow entry on a
result that is not a view is an error rather than a no-op.

## Rows are read at call sites

What a call views reaches the caller, across a module boundary as well:

```gyro
fn pick(a: i32[], b: i32[]): i32[] !{ &a } { return a }

fn caller(xs: i32[], ys: i32[]): i32[] !{ &xs } {
  return pick(xs, ys)      // views xs, because pick's row says so
}

fn bad(ys: i32[]): i32[] !{ &ys } {
  var local = [1, 2, 3]
  return pick(local, ys)   // error: the result views this frame
}
```

A caller has to account for **every** source the callee names: `both(xs, ys)`
with `!{ &a, &b }` on `both` needs `!{ &xs, &ys }`.

## Receivers and fields

`&self` names a method's receiver, and a field chain roots at whatever holds
it — `self.items` is the receiver's storage, `s.items` is `s`'s:

```gyro
export fn Stack::view(self): i32[] !{ &self } {
  return self.items.slice()
}

fn contents(s: Stack): i32[] !{ &s } {
  return s.items.slice()
}
```

A struct carries what its fields carry, so frame storage behind a field is
still the frame's:

```gyro
struct View { data: i32[] }

fn bad(): i32[] {
  var xs = [1, 2, 3]
  var v = View{ data: xs }
  return v.data          // error: xs ends with this function
}
```

A heap buffer does not — `Stack{ items: Array<i32>::new() }` holds storage
that outlives the frame, and lending a view of it is fine.

What is tracked here is **provenance, not lifetime**: where a view came
from, not how long the storage lives. That is why a slice can still outlive
its backing memory ([chapter 4.4](ch04-04-slices-and-views.md)) — this
analysis was never the one that would catch it.

## Borrows on a generic

A generic row is **conditional**: load-bearing where `T` turns out to be a
view, vacuous where it does not. It is stated once and serves every
instantiation ([chapter 10](ch10-00-generics.md)).

## Why rows exist at all

The decorators are the motivation. `@comptime` needs purity, `@gpu` forbids
allocation and syscalls, and `@distributed` restricts what may cross a node
boundary. Today each of those is a bespoke check; effect rows are how they
become one mechanism.

## What is outside v1, deliberately

- a row variable (`!e`, `!{ io, ..e }`) leaves the row **open and
  unchecked**
- *calling* a function value — a closure or an `fn` parameter — contributes
  nothing to the caller ([chapter 11](ch11-00-closures.md))
- module-level `var` initializers run before `main`, outside any row, over
  the process heap
- channel operations and `Mutex` carry no effect
- `spawn` declares `!{ io }`, which covers starting the thread and
  stands in for the task's own effects until a row can travel with a
  function value

Effect rows are also not part of METALS: Gyro checks them, and the IR
carries no trace ([appendix D](appendix-d-metals.md)).
