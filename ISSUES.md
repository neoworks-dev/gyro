# ISSUES — one obvious way

Gyro's goal: a language a model is less likely to get wrong, because there is
one spelling per concept and every name in scope is visible. Measured against
`examples/`, these are the places where a second spelling, an overloaded sigil
or a non-local obligation crept back in. Nothing here is a bug report — each
one is a design question the book has to answer.

## 1. Effect and borrow rows

`structs.gyro:36,57,68`, `triangle.gyro:46,53,57,121`

A row is a second type-level language, written by hand, but derived from what
the body and the bodies it calls do. `preferred` must union what `longest`
names, so the obligation is non-local. Worse, the corpus contradicts itself on
when a row is required at all: `hello.gyro:5` says a row is not how the
compiler finds out, yet the externs carry `!{ io, alloc }` and one carries an
empty `!{}`. The rule cannot be inferred from the examples.

Decide one: rows are always inferred and only ever written as documentation, or
rows are always required. The middle is where the guessing happens.

## 2. Sigil overload

`*` is a pointer type (`*Node`), a mutable receiver (`*self`), a dereference
(`*address`) and a by-reference parameter (`*Array<u32>`), while an extern
handle that is a pointer in C is spelled `Window?`. `&` is address-of
(`&head.value`) and a borrow source in a row (`&self`).

Four meanings of one character, in a language whose selling point is that
reading is unambiguous.

## 3. Two loop forms

`fibonacci.gyro:42` is `for var i = 0; i <= 10; i += 1`; `fibonacci.gyro:48` is
`for i in 0..15`. Same shape, same file, same job. That is the JavaScript
problem in miniature. Ranges plus `while` cover the C form; delete it.

## 4. `as` answers three questions

- widening, which always succeeds: `byte as u32`
- narrowing, which panics on range: `(value * 255.0) as u32`
- entering and leaving an `any`, which panics on type: `7 as any`, `held as i32`

Two different panics behind one keyword, and the third is a dynamic type test
that has nothing to do with conversion. Split it.

## 5. `Array<i32>` and `i32[]` look alike and mean opposites

One owns its buffer and must be freed; the other borrows and never owns. The
semantics are right and the distinction is load-bearing, but two things with
opposite ownership should not differ by punctuation.

## 6. Nested tuple out of a two-name receive

`concurrency.gyro:110`: `var body, more = <-results` where the element is
itself a tuple, so the payload is `body.0`. The two-name form already means
`(value, open)`, so the nesting depth depends on a rule with no visual cue.

## 7. Three concurrency models

`concurrency.gyro:3-5` names them itself: threads, channels, async. The tell is
`closures.gyro:62` — `var later = async fn(): i32 { return 1 }` is declared and
never used. If the example cannot motivate the third model, a caller will not
either.

## 8. `@concurrent` is invisible parallelism

`concurrency.gyro:51`: `sum_shared` reads as a sequential loop and is not one.
A decorator above the signature changes the meaning of every line below it.

Decorators are also a closed set the compiler knows — `@comptime`,
`@concurrent`, `@extern`, `@repr` — with no import and therefore no discovery
path. Everything else in the language is reachable from an import line; these
have to be memorised.

## 9. Struct literals have two forms

`methods.gyro:80` is `Vector2{ x: 1.0, y: 2.0 }`; `methods.gyro:85` is
`Pair<i32>{ 1, 2 }`. Named and positional, same construct.

## 10. Discard noise

`triangle.gyro:271-273` is three consecutive `var _ = ` lines. The rule is
right — dropping an error must be visible in the source — but the extern call
that returns a status nobody reads is the common case, and it pays the full
price.

## 11. `0 - 1` where `-1` is meant

`triangle.gyro:238` passes `0 - 1` to `SDL_CreateRenderer`, while
`methods.gyro:22` returns `-1` happily. Either unary minus does not reach an
argument position, or an untyped negative literal does not adapt to a
parameter type. Find out which.

## Priority

Rows, the C-style `for`, and `as`. Those three buy the most per unit of churn:
the first removes a hand-maintained second language, the second removes a
coin flip, the third removes two silent panics hiding behind one keyword.
