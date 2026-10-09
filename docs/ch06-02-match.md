# 6.2. Match

`match` is an **expression**. Arms are `pattern: body`, one per line — an arm
ends at the newline, like every other statement. A body may be an
expression, a block, or a control-flow statement.

```gyro
var name = match n {
  0: "zero"
  1: "one"
  _: "other"
}
```

There are no commas between arms; the newline is the separator. Because an
arm body is delimited by the newline that ends it, a `return` inside an arm
yields at most one value — returning several needs a block.

## Patterns

Patterns are `_`, literals, bindings, enum variant destructuring, and struct
type-tests.

**Variant destructuring** binds a payload, and is always qualified:

```gyro
match shape {
  Shape::Circle(r): 3.14159 * r * r
  Shape::Rect(w, h): w * h
  Shape::Point: 0.0
}
```

**A type test** applies where the value's concrete type is not statically
known — an `error`, or an `any`. It matches when the value is of that type
and binds the whole value:

```gyro
match err {
  RangeError(e): report(e.value)
  _: println(err.message())
}
```

Any type may name itself in a test over an `any`, primitives included:
`i32(n)`, `string(s)`, `Point(p)`. A type no ordinary path can spell is
written out as itself:

```gyro
var text = match v {
  (i32, string)(pair): format(heap(), "pair #{pair.0}")
  i32[](xs):           format(heap(), "#{xs.len()} numbers")
  i32[3](fixed):       "three numbers"
  *Node(node):         format(heap(), "node #{node.value}")
  _:                   "something else"
}
```

A **plain binding** matches whatever is left, and closes a match the same
way `_` does.

## Exhaustiveness

A match over an enum is checked: every variant must be covered or a `_` arm
present, and the diagnostic names the variants that are missing.

The subtle rule is that **an arm covers a variant only when its payload
patterns bind rather than test**:

```gyro
match shape {
  Shape::Circle(r):   ...   // covers Circle
  Shape::Circle(0.0): ...   // matches one radius; Circle stays uncovered
}
```

So a match that filters on a payload value still needs an arm for the
variant in general.

Open subjects are exempt: integers, strings, `any`, and an `error` whose
concrete type is unknown have no finite variant set, so such a match is
never reported as partial. **Falling off the end of one panics at run
time.**

## Match in the flow analyses

A `match` runs exactly one arm, and the analyses know it:

- for definite assignment, what survives a match is what *every* arm wrote
  ([chapter 3.1](ch03-01-variables-and-constants.md))
- an arm that cannot fall through — one that returns — does not constrain
  the join

## What match does not do

There is no guard clause (`if` inside a pattern), no `|` alternation, no
range pattern, and no nested destructuring of struct fields. A pattern is a
literal, a binding, a variant with payload patterns, or a type test — and
anything else is an `if` inside the arm body.
