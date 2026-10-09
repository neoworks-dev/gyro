# Appendix B. Operators

| class | operators |
| --- | --- |
| arithmetic | `+ - * / %` — these panic on overflow |
| wrapping | `+% -% *%` — deliberate two's-complement wraparound |
| comparison | `== != < <= > >=` |
| logical | `&& \|\| !` |
| bitwise | `& \| ^ ~ << >>` — these never panic |
| assignment | `= += -= *= /= %= &= \|= ^= <<= >>=` |
| access | `.` field, `::` namespace, `[]` index, `[lo..hi]` slice narrowing |
| conversion | `as` |
| type | `?` nullable |
| other | `..` range, `@` decorator, `<-` channel, `!{}` effect row |

There is no `++` or `--`; increment is `i += 1`. Operators are not
overloadable, so `<` on a user type is an error that points at
`a.compare(b)`.

## Which types each accepts

| operator | operands |
| --- | --- |
| `+ - * / %` | matching numeric types |
| `+% -% *%` | matching integer types |
| `& \| ^ ~ << >>` | integers only |
| `< <= > >=` | numeric primitives and `rune` only |
| `== !=` | primitives, pointers (identity), tuples, enums, structs (derived field-wise). **Not slices.** |
| `&& \|\| !` | `bool` |
| `as` | numeric types and `rune`; into and out of `any`; between pointer types and `anyptr` |

Operands must already share a type: there are no implicit conversions
between typed values, and only literals adapt to context
([chapter 3.2](ch03-02-data-types.md)).

## What panics, and what does not

| operation | on failure |
| --- | --- |
| `+ - * /` on integers, overflow | panic |
| `/` `%` by zero, integers | panic |
| `i32::MIN / -1` | panic (overflow) |
| narrowing `as` that loses the value | panic |
| `as` out of an `any` holding another type | panic |
| indexing out of range | panic |
| narrowing outside `lo <= hi <= len()` | panic |
| dereferencing a null `T?` | panic |
| `+% -% *%` | wrap |
| `<< >>` past the width | defined: zero, or the sign fill for a signed `>>` |
| `/ 0.0` on floats | `+Inf`, `-Inf` or `NaN` |

## Comparison against NaN

Every comparison against NaN is false except `!=`, which is true. The
three-way `compare()` method is a **total** order instead: NaN is above every
number and equal to another NaN, and `0.0` and `-0.0` compare equal
([chapter 3.3](ch03-03-operators-and-arithmetic.md)).

## Two tokens with special handling

- **`>>` is scanned as two `>` tokens** and rejoined by the parser when a
  shift is meant, so nested generic arguments close cleanly.
- **`<-` is scanned greedily** and split back apart in binary-operator
  position, so `x < -1` is a comparison and `ch <- v` is a send. Sends are
  statements only.

## Line continuation

A line **beginning** with one of these continues the previous statement:
`.`, `,`, `)`, `]`, `&&`, `||`, `==`, `!=`, `<=`, `>=`, `<`, `>`, `+`, `/`,
`%`, `|`, `^`, `<<`, `>>`, `..`, `as`.

A line **ending** with an operator continues too, since an operator is not a
token that can end a statement — with two exceptions, `>` and `?`, which are
in the terminator set because a type can end a line in a signature-only
declaration. So a trailing `>` cannot split an expression.

`-`, `*`, `&` and `<-` are **not** in that set, because a statement can begin
with each of them ([chapter 3.5](ch03-05-lines-and-comments.md)).
