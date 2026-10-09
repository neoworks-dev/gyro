# 1.1. What Gyro is

Gyro is a systems language with manual memory, explicit effects, and a
compiler that refuses more programs than it has to. It compiles to a shared
intermediate representation called METALS, which runs three ways: an
evaluator, x86-64 assembly, and generated C.

**The goal that drives every design decision:** Gyro should be explicit
about side effects and unwanted control flow, and safe enough that a machine
can generate code in it and be caught when it is wrong. Not "safe" as
marketing — safe as in the compiler refuses the program.

That goal is why the language has:

- no zero values, so nothing is silently uninitialized
- no implicit conversions, anywhere, between any two typed values
- overflow that panics rather than wraps, with wrapping spelled `+%`
- exhaustive matches over enums
- definite assignment, flow-sensitively checked
- borrow provenance written into signatures
- leak and data-race warnings

None of those is a lint you can switch off. Each is a rule the compiler
enforces, and [chapter 12](ch12-00-effect-rows.md) and
[chapter 4](ch04-01-manual-memory.md) are where most of them live.

## One principle, everywhere: duck typing

**Gyro is duck-typed wherever a contract is needed.** A type is an `error`
by having `message(): string`. A type is iterable by having `next()`. A type
works in a generic function by having whatever methods that function calls.

Nothing is ever declared to satisfy a contract; having the right methods is
the whole test. There is no `trait`, no `impl`, no `Self`, and no bounds on
a type parameter — they were removed on purpose ([`decisions.md`](decisions.md),
"Traits were removed"), and what replaced them is nothing at all.

## Nothing is implicit

If a name is not part of the formal syntax, it has to be imported. There is
no universe scope and no predeclared functions, so `println` is imported
while `xs.len()` and `ch.close()` are methods that belong to their types.
Visibility is declared with `export` and never inferred from spelling.
Even `spawn` is an ordinary library function rather than a keyword — it
takes a closure, so it needs no grammar of its own.

The one implicit conversion in the language is packing an argument into a
`...any` parameter, and [chapter 14](ch14-01-any.md) explains why that one
is the call's own doing.

## Errors cannot escape by accident

A fallible function returns `(T, error?)` and the caller has to handle it.
There is no propagation operator, no exceptions and no unwinding, and
discarding an error takes an explicit `_`, so ignoring one is always visible
in the source. Programmer errors — an index out of range, a divide by zero,
a null dereference, an overflow — are not errors in that sense at all: they
print a message with a source position and abort.

## What it is not, yet

Gyro is a work in progress with **no users**. Nothing needs a migration
path, and nothing gets one: when a spelling changes it changes everywhere,
and the old one is deleted rather than deprecated.

It runs CLI tools today. It is not usable for anything long-running, because
nothing reclaims memory yet: every string interpolation, `split` and
`text()` allocates and nothing frees it.
[Appendix G](appendix-g-known-gaps.md) is the honest list of what is missing.

## Where the rest of this book goes

Chapters 3 through 14 teach the language. Chapter 15 is how Gyro reaches C.
Chapter 16 is the toolchain, and chapter 17 is what happens to a program
after it type-checks — the IR, the tiers, and the corpus that holds them to
one answer.

Read [chapter 1.2](ch01-02-installation.md) next to build the compiler.
