# 3.3. Operators and arithmetic

| class | operators |
| --- | --- |
| arithmetic | `+ - * / %` — these panic on overflow |
| wrapping | `+% -% *%` — deliberate two's-complement wraparound |
| comparison | `== != < <= > >=` |
| logical | `&& \|\| !` |
| bitwise | `& \| ^ ~ << >>` — these never panic |
| assignment | `= += -= *= /= %= &= \|= ^= <<= >>=` |
| access | `.` field, `::` namespace, `[]` index |
| conversion | `as` |
| type | `?` nullable |
| other | `..` range, `@` decorator, `<-` channel, `!{}` effect row |

On a type the program declares, `+ - * / %`, unary `-`, `== !=` and
`< <= > >=` are the methods of the traits `Add`, `Sub`, `Mul`, `Div`, `Rem`,
`Neg`, `Eq` and `Ord`, and mean what the type's impl says
([chapter 10.1](ch10-01-traits.md)). The numbers have them built in, and
their rules below are unchanged. Wrapping arithmetic, the bitwise operators
and shifts are not traits and are the integers' alone.

## Overflow panics

`a + b` on integers panics when the result does not fit the operand type. So
does `a - b`, `a * b`, and a narrowing `as` that loses the value. Integer
divide by zero panics, and `i32::MIN / -1` panics as overflow.

Wrapping is available where it is actually wanted, and is spelled out:

```gyro
var hash: u32 = 2166136261
for byte in bytes {
  hash = hash *% 16777619 +% (byte as u32)
}
```

`+%`, `-%` and `*%` are two's-complement wraparound at the operand's width.
There is no wrapping division, because division does not wrap.

## Division truncates toward zero

Integer division truncates toward zero and `%` takes the sign of the
dividend: `-7 / 2 == -3`, `-7 % 2 == -1`.

## Floats are IEEE 754

Float division by zero does not panic: `1.0 / 0.0` is `+Inf`, `-1.0 / 0.0`
is `-Inf`, and `0.0 / 0.0` is `NaN`. IEEE defines those answers, so they are
values rather than bugs — unlike integer division by zero, which has no
answer to give.

NaN is unordered. `==` and `!=` aside, every comparison against NaN is
false, including `NaN == NaN`; `NaN != NaN` is true.

The three-way `compare()` method is different **on purpose**: it is a
**total** order, so that a comparator stays consistent. NaN compares above
every number and equal to another NaN, and `0.0` and `-0.0` compare equal.

```gyro
var nan = 0.0 / 0.0
nan <= 1.0            // false — unordered
nan.compare(1.0)      // 1     — total order, NaN is above
nan.compare(nan)      // 0
```

## Shifts never panic

A shift moves a bit pattern rather than computing a value, so bits that
leave the operand's width are simply gone — `<<` drops the high ones as
quietly as `>>` drops the low ones, and neither is an overflow. A count at
or beyond the width clears the word, or fills a signed `>>` with the sign;
it is defined, not undefined.

That is what makes a rotate ordinary Gyro:

```gyro
fn rotl(a: u32, b: u32): u32 {
  return (a << b) | (a >> (32 - b))
}
```

The bitwise operators are integer-only, and none of them has a wrapping
twin, because losing bits is what they are for.

## Two lexical details that shape the syntax

**`>>` is scanned as two `>` tokens.** That is what lets nested generic
arguments such as `Result<Vec<i32>, error>` close cleanly; the parser
rejoins them when a shift is meant. A tokenizer that merged them would break
generics, and this has been "fixed" wrongly before.

**`<-` is scanned greedily.** That makes `x<-1` ambiguous with a comparison
against a negative number, so the parser splits the token back apart
whenever it appears in binary-operator position. Written with a space,
`x < -1` is never ambiguous in the first place — and a send is a statement,
never an expression, so the two never really collide.

## Ranges

`a..b` is a half-open range: `0..10` visits 0 through 9, so `0..xs.len()`
visits every index. There is no inclusive form; an inclusive walk is a
C-style `for` header ([chapter 3.6](ch03-06-control-flow.md)).

## What panics, in one list

These are **programmer errors**, not conditions. They print a message with a
source position and abort. They are neither returnable nor catchable, which
is what keeps the `(T, error?)` channel meaningful and needs no unwinding
machinery.

- integer overflow, including a narrowing `as` that loses the value
- integer divide by zero, and `MIN / -1`
- indexing out of bounds
- dereferencing a null `T?`
- reading through a pointer to storage nothing has written (evaluator only —
  see [appendix G](appendix-g-known-gaps.md))
- taking a concrete type out of an `any` that holds another

```text
panic: index 7 out of range for length 3
  at main.gyro:12:11
```

[Chapter 9](ch09-00-errors.md) is about the other kind of failure — the kind
you are supposed to handle.
