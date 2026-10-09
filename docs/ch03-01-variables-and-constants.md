# 3.1. Variables and constants

```gyro
var x: i32 = 0
var y = 42            // inferred
const MAX: i32 = 100  // compile-time constant
var value, err = f()  // tuple destructuring
var _, err = f()      // `_` discards explicitly
```

Bindings are `var` and `const`; assignment is `=`. **There is no `:=`** —
one spelling for one thing, everywhere, including inside a `select` arm
where other languages introduce a second form.

The type annotation is optional wherever the type can be inferred from the
initializer. `const` is a compile-time constant, not a read-only variable.

## There are no zero values

`var x: i32` with no initializer is legal. Reading `x` before every path to
the read has assigned it is a compile error — **definite assignment** — and
the analysis is flow-sensitive. Nothing springs into existence unwritten.

```gyro
var x: i32
if cond {
  x = 1
}
println(x)        // error: not assigned when cond was false

if cond { x = 1 } else { x = 2 }
println(x)        // fine
```

The rules the checker follows:

- A branch that cannot fall through does not have to assign, so an early
  `return` leaves the other branch's write standing.
- A loop body may run zero times, so nothing it writes counts afterwards.
  The exception is a loop that cannot fall out of its condition, where
  `break` is the only exit: the states at the breaks are what survive.
- A `match` or `select` runs exactly one arm, so what survives is what
  *every* arm wrote.
- Taking an address counts as assigning. `&x` names storage that is real and
  sized whether or not anything has been written there, and writes through
  the pointer cannot be followed. Reading through such a pointer before a
  write panics at run time rather than failing the build.
- A closure may read only what is already assigned where it is written, and
  what it assigns does not count outside it — it runs at a time this
  analysis cannot pin down.

Why this and not zero values: a zero value is a decision the language makes
on your behalf about what an unwritten variable means, and it is wrong about
as often as it is right. The reasoning is recorded in
[`decisions.md`](decisions.md) under "Definite assignment, no zero values";
the flow state that implements it is deliberately gen-only and needs no
fixpoint over loops.

## Shadowing

An inner block may shadow an outer name. Redeclaring a name in the **same**
block is an error.

```gyro
var x = 1
if cond {
  var x = 2      // ok: inner scope
}
var x = 3        // error: already declared in this block
```

## Destructuring

A binding may take several names, which destructures a tuple:

```gyro
var value, err = load(path)
var _, err = load(path)      // the value is deliberately dropped
```

`_` is not a name — it discards. That matters most for errors: dropping an
error result is a compile error, so ignoring one is always visible as a `_`
in the source ([chapter 9](ch09-00-errors.md)).

A two-name receive from a channel has the same shape and one extra rule: the
flag guards the value, so reading the value where the flag was false does
not compile ([chapter 13.2](ch13-02-channels.md)).

## Module-level bindings

`var` and `const` are legal at module scope. A module-level `const` is a
compile-time constant; a module-level `var` is program storage whose
initializer runs before `main`, in declaration order, and across modules in
dependency order. Those initializers run outside any effect row.

## Assignment operators

`=` assigns; `+= -= *= /= %= &= |= ^= <<= >>=` combine. There is no `++` or
`--` — increment is `i += 1`, one way only.
