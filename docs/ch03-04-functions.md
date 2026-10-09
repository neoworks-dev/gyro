# 3.4. Functions

```gyro
fn add(a: i32, b: i32): i32 {
  return a + b
}

fn nothing() {}                      // no annotation means no result
fn load(p: string): (string, error?) // returns a tuple
fn max<T>(a: T, b: T): T             // generic
async fn fetch(u: string): (string, error?)
fn apply(f: fn(i32): bool) {}        // function-valued parameter
fn sum(xs: ...i32): i32              // variadic
fn printf(fmt: string, args: ...any) // variadic over mixed types
```

The return type follows a colon, which is the same marker a parameter's type
uses and the same one a closure uses. A function with no `:` returns
nothing; there is no `void` to write.

Parameters may carry defaults: `fn f(n: i32 = 4)`.

A declaration **with no body** is not a stub — it is a foreign function or a
runtime intrinsic ([chapter 15](ch15-00-c-interop.md)), which is why the
standard library's own surface is written that way.

## Returning several values

`return a, b` is sugar for returning a tuple, and the caller destructures:

```gyro
fn divmod(a: i32, b: i32): (i32, i32) {
  return a / b, a % b
}

var q, r = divmod(17, 5)
```

A fallible function is this shape with `error?` on the right
([chapter 9](ch09-00-errors.md)).

## Variadic parameters

A final parameter written `name: ...T` collects the remaining arguments.
Inside the body it is an ordinary `T[]` slice — `len()`, indexing and
`for..in` all work, and it borrows storage the caller owns, so it must not
outlive the call.

```gyro
fn sum(xs: ...i32): i32 {
  var total = 0
  for x in xs {
    total += x
  }
  return total
}

sum()           // 0
sum(1, 2, 3)    // 6
```

Only the last parameter may be variadic, it takes no default, and a call
site may supply zero arguments for it.

An existing slice is forwarded with a trailing `...`, which is the only way
to pass one as the whole pack:

```gyro
fn log_all(rest: ...i32) {
  sum(rest...)    // forwards the pack
  sum(rest)       // error: i32[] is not an i32
}
```

The heterogeneous form is `...any`, which is what `printf`-shaped code
wants — one pack, mixed types, each remembering what it is
([chapter 14.1](ch14-01-any.md)).

## Function values

`fn(A, B): C` is a type; `fn(a: A, b: B): C { ... }` is a value. In type
position it is a signature, in expression position it is a closure, and the
two never collide because one has parameter names and a body and the other
does not.

```gyro
fn apply(values: i32[], f: fn(i32): i32): Array<i32> {
  var out = Array<i32>::new()
  for value in values {
    out.push(f(value))
  }
  return out
}

var doubled = apply([1, 2, 3], fn(x) x * 2)
```

[Chapter 11](ch11-00-closures.md) covers closures, capture, and the
one-word `code(..)` type a C signature has room for.

## Effect rows, briefly

A signature may carry an effect row after the result:

```gyro
export fn log(msg: string) !{ io }
export fn load(p: string): (string, error?) !{ io, alloc }
fn first(xs: i32[]): i32[] !{ &xs }
```

A row states what the body does (`io`, `alloc`) and what the result borrows
(`&name`). A function with **no** row is unconstrained; a function *with* a
row promises the row is complete. A function returning `T[]` or `string`
needs a `&` entry in practice, because a view never owns what it points at.
[Chapter 12](ch12-00-effect-rows.md) is the full treatment.

## Methods are functions too

A method is a free-standing function whose name is qualified by its type —
`fn Stack::push(*self, v: i32)`. There is no `impl` block and no `Self`.
[Chapter 5.2](ch05-02-methods.md).
