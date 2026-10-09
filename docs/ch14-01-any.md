# 14.1. `any`

`any` holds a value of exactly one other type and remembers which. It is a
first-class type: a variable, a field, a slice element, a channel element.

It exists so that a `...any` pack can carry mixed types — which is what
makes a `printf` possible in ordinary Gyro
([chapter 14.2](ch14-02-printing.md)).

## In and out

A value enters an `any` with `as`, which never fails, and leaves it with
`as` back to a concrete type, which **panics** when the type disagrees — the
same rule narrowing conversions follow
([chapter 3.2](ch03-02-data-types.md)).

```gyro
var v: any = 42 as any
var n = v as i32         // 42
var s = v as string      // panics: any holds i32, not string
```

The only implicit conversion in the language is **packing**: an argument to
a `...any` parameter is wrapped without writing `as any`, because the call
is what constructs the pack. Assignment never wraps — `var x: any = 42` is
an error and `42 as any` is the fix.

## Asking instead of asserting

`match` tests the type rather than asserting it, spelled like the type test
on an `error`:

```gyro
var text = match v {
  i32(n):    format(heap(), "int #{n}")
  string(s): format(heap(), "text #{s}")
  _:         "something else"
}
```

`any` is an open subject like `error`, so a match on one is never reported
as partial — and falling off the end panics. Write the `_`.

A type an ordinary path cannot spell is written out in the arm:

```gyro
var text = match v {
  (i32, string)(pair): format(heap(), "pair #{pair.0}")
  i32[](xs):           format(heap(), "#{xs.len()} numbers")
  i32[3](fixed):       "three numbers"
  *Node(node):         format(heap(), "node #{node.value}")
  _:                   "something else"
}
```

## What cannot go in

An `any` holds any type **except** the ones that could not answer for
themselves afterwards:

- `void` and `null`
- another `any` — they do not nest
- an `error` or a `T?`, both of which hide the concrete type the `any` is
  meant to be sure of
- a channel, a future, a function value
- `anyptr` and `code(..)` ([chapter 15](ch15-00-c-interop.md))
- the runtime-owned handles, such as `Array` and `Mutex`

## Printing one needs no unwrapping

```gyro
println(v)
```

prints whatever is inside, using that type's derived representation. So a
`...any` pack prints element by element with nothing unwrapped.

## It is not an escape hatch

Two things keep `any` from becoming one.

**It is a closed set.** Gyro compiles whole programs, so the set of types
any `as any` in the program mentions is known at compile time. `any` is that
set, not a runtime type registry.

**There is no way to call a method on an `any`** without first naming the
type it holds. You cannot ask an `any` for `message()` and hope; you match
on the type, and inside the arm you have a value of a real type with real
methods.

## Under the hood

An `any` is **two words** — a descriptor and a payload — not a heap box. A
scalar rides in the payload, so wrapping one allocates nothing; anything
wider is heap storage the payload points at. Descriptors are static data,
one per distinct type the program wraps, so the type test is a pointer
comparison ([appendix D](appendix-d-metals.md)).

That matters beyond Gyro: METALS is meant to carry several source languages,
and `any` is the escape hatch for the ones that genuinely cannot declare
their types. A producer that *can* declare them should, because then a call
across the language boundary is an ordinary call with nothing inspected at
run time.
