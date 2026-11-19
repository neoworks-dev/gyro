# 6.1. Enums

An enum is a closed set of variants, each of which may carry a payload.

```gyro
export enum Shape {
    Circle(f64),
    Rect(f64, f64),
    Point,
}
```

`Shape::Circle(2.0)` constructs one. Variant names are **always qualified**
with the enum's name, at construction and in patterns, so a bare `Circle`
never shadows anything and never has to be imported separately.

## Enums are values

`impl Eq for Shape` makes `==` and `!=` compare an enum by tag and payload,
and `impl Hash for Shape` derives `hash()` over the same parts; printing is
derived with `impl Print for Shape`, unless the impl supplies `to_string`
([chapter 10.1](ch10-01-traits.md)). Without the impls an enum does none of
the three.

An enum is one word of tag plus its widest variant's payload, laid out like
a C struct ([appendix E](appendix-e-native-backend.md)).

## Matching is exhaustive

This is the point of having them:

```gyro
fn area(shape: Shape): f64 {
    return match shape {
        Shape::Circle(r): 3.14159 * r * r
        Shape::Rect(w, h): w * h
        Shape::Point: 0.0
    }
}
```

Every variant must be covered, or a `_` arm present. Adding a variant later
breaks every non-`_` match loudly, which is the reason to write the match
without a catch-all in the first place. [Chapter 6.2](ch06-02-match.md) has
the exact coverage rules — including the one about payload patterns that
*test* rather than *bind*.

Only enums are enumerable. A match on an integer, a string, an `any`, or an
`Error` whose concrete type is unknown is never reported as partial, because
there is no finite variant set to compare against. A `T | Error` has exactly
two cases and is checked for both.

## What enums are not used for

Two jobs that would be an enum in other languages are not one here:

- **Errors.** There is no `Result` enum. A fallible function returns
  `T | Error`, a type of its own that `?` and a match know how to take
  apart ([chapter 9](ch09-00-errors.md)).
- **Optionality.** There is no `Option`. `T?` is a type-level marker with
  narrowing behind it ([chapter 4.3](ch04-03-nullability.md)).

Both were deliberate: the reasoning is in [`decisions.md`](decisions.md)
under "There is no `Result`" and "Absence is expressed by nullable types".

Enums also carry no methods of their own beyond what you write for them —
`fn Shape::describe(self): string` is an ordinary method
([chapter 5.2](ch05-02-methods.md)).
