# 3.6. Control flow

```gyro
if x < 0 {
} elif x == 0 {
} else {
}

while i * i <= n {
  i += 1
}

for var i = 0; i < 10; i += 1 {}
for value in 0..10 {}
for value in collection {}
```

Conditions take no parentheses; braces are always required.

## `elif`, never `else if`

`elif` is sugar and desugars to a nested `if`, leaving no trace in the
syntax tree. It is the **only** way to chain conditions: `else` takes a
block, so `else if` does not parse. One spelling, one shape.

## Loops

`while` takes a condition. The `for` statement comes in two forms — the
C-style header, and `for ... in` over something iterable:

```gyro
for var i = 0; i < 10; i += 1 { }
for i in 0..10 { }
```

`a..b` is half-open, so `0..xs.len()` visits every index and an inclusive
walk is the C-style header. `break` and `continue` do what they look like.

A loop body's `defer` runs at the end of **every iteration**, because a
defer runs at the exit of its enclosing block
([chapter 4.2](ch04-02-leaks-and-defer.md)).

## What `for ... in` accepts

Slices, fixed-size arrays, ranges and channels iterate natively. Any other
type is iterable by **implementing `Iterator<T>`**
([chapter 10.1](ch10-01-traits.md)): a `next(*self): (T, bool)` that hands
back the following element and whether there was one, where `false` ends the
loop.

```gyro
impl Iterator<i32> for Counter {
  fn next(*self): (i32, bool) {
    if self.n >= self.limit {
      return 0, false
    }
    self.n += 1
    return self.n, true
  }
}

for x in (Counter{ 0, 5 }) { println(x) }
```

A type with a `next` method and no impl is not an iterator.

The parentheses are the same rule conditions have: a brace after the
iterable opens the loop body, so a bare struct literal cannot sit at the top
level of a `for..in` head.

Iterating a channel drains it until it is closed
([chapter 13.2](ch13-02-channels.md)).

## `match` is an expression

Arms are `pattern: body`, one per line — an arm ends at the newline, like
every other statement. A body may be an expression, a block, or a
control-flow statement.

```gyro
var name = match n {
  0: "zero"
  1: "one"
  _: "other"
}
```

Because an arm body is delimited by the newline that ends it, a `return`
inside an arm yields at most one value; returning several needs a block.

A match over an enum is checked for exhaustiveness, and a match over an
`error` or an `any` is a type test. [Chapter 6.2](ch06-02-match.md) is the
whole story.

## What is missing on purpose

There is no `switch`, no ternary operator, no `do..while`, no labelled
break, and no `goto`. `match` covers the first two; the rest have not been
needed.
