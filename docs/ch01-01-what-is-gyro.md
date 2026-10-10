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

## One way to state a contract: a trait

**A contract is a trait, and a type opts in on purpose.** A type is `Eq`,
iterable, printable or usable in a generic function because its author wrote
`impl Trait for Type`, and a generic says what it needs of its parameters
with bounds, as in `fn max<T: Ord>`. A type's own methods stay free-standing,
`fn Type::name(self)`; an `impl` block is only how it keeps a promise to a
trait.

Having a method of the right name is not enough, and neither is being
structurally the same: what a type promises is what its author wrote down
([chapter 10.1](ch10-01-traits.md), and [`decisions.md`](decisions.md), "Traits").
The one contract still checked by shape is `error`, which is a type with
`message(): string` ([chapter 9](ch09-00-errors.md)).

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
nothing reclaims memory yet: `split` and
`text()` allocate and nothing frees them.
[Appendix G](appendix-g-known-gaps.md) is the honest list of what is missing.

## Where the rest of this book goes

Chapters 3 through 14 teach the language. Chapter 15 is how Gyro reaches C.
Chapter 16 is the toolchain, and chapter 17 is what happens to a program
after it type-checks — the IR, the tiers, and the corpus that holds them to
one answer.

Read [chapter 1.2](ch01-02-installation.md) next to build the compiler.
