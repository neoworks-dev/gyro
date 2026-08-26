# 17.1. One backend, no oracle

This is the single most important architectural fact about the project, and
it changes how you read every other chapter.

**The backend does not define the answer. This book does.** A test's golden
file is written by hand to say what a program prints, and a backend that
disagrees with it is wrong — including when the backend is the only thing
that has ever run the program, and including when nobody noticed until the
golden was read.

Everything the compiler can read becomes METALS, and one thing consumes it:

| stage | what it is |
| --- | --- |
| `internal/lower` | Gyro → METALS |
| `internal/cc` | C → METALS, for a `.c` a Gyro program imports |
| `internal/nasm` | METALS → x86-64 assembly, linked against `internal/crt` |

## Why the golden is written by hand

There is no flag that regenerates a golden from what the backend produced.
That is deliberate, and it is the lesson from the first version of this
corpus, which rewrote goldens from a tree-walking interpreter: what that
bought was an implementation whose bugs became the specification until
someone read the file.

An implementation that can write its own exam has stopped being a check on
anything. Deciding what a program *should* print is the work; a harness
that copied the answer out of the compiler would be checking only that the
compiler is deterministic.

So when a program prints something surprising, the backend is not
presumptively right. Read the chapter that covers the behaviour, decide
what the program should print, and record the decision in
[`decisions.md`](decisions.md) if the docs did not already answer it.

## What a second tier used to buy, and what replaces it

For a while every program ran three ways — a METALS evaluator, the
assembler backend, and a second native backend that emitted C — and all
three had to agree. Moving the execution tests into one corpus that way
immediately surfaced five divergences that had existed for a long time:
`println(null)` did not lower, deferred call arguments were evaluated at
exit rather than at registration, printing a tuple needed a synthesized
representation, and the native runtime detected neither use-after-free nor
uninitialized reads. Each tier had kept its own hand-picked subset of tests,
and each subset had been chosen — not deliberately — to avoid what that tier
got wrong.

That cross-check is gone. What it did was catch a wrong answer that looked
plausible, and nothing else does that automatically now. Two things carry
the weight instead:

- **The golden, and the fact that it is written before the program runs.**
  This was always the real check; the tiers were a way of noticing when it
  had been skipped.
- **The METALS verifier**, which reads a module before anything consumes
  it and does not care which producer made it
  ([appendix D](appendix-d-metals.md)).

Some things the evaluator caught are simply not caught any more, and they
are listed as gaps rather than quietly dropped
([appendix G](appendix-g-known-gaps.md)).

## The semantics the backend must reproduce

The book pins these:

- overflow and underflow panic, per width; wrapping is `+% -% *%`
- division truncates toward zero, so `-7 / 2 == -3`, and `%` takes the sign
  of the dividend
- a narrowing `as` that loses the value panics
- bounds and null panics carry source positions
- `defer` runs at block exit in reverse order — per loop iteration included,
  with arguments captured at registration
- closures capture by copy
- structs have value semantics
- printing is derived, with `to_string` overriding it
- `spawn`, channels and `select` run on real threads
- `async` is futures
- float formatting is shortest round-trip, with the same exponent thresholds
  everywhere

Each has a chapter above; this list is what the backend is measured against.

## Where behaviour should be implemented

**Once, at the highest shared layer possible.** Prefer synthesizing
behaviour during lowering over writing it into the backend, and prefer the
C runtime over the assembler when it needs to exist at runtime at all.

Derived printing is the worked example. It began as a runtime import; moving
it to one synthesized METALS function per type meant no backend and no C
work at all ([chapter 17.2](ch17-02-the-pipeline.md)).

The same instinct explains why so much of the standard library is ordinary
Gyro: a thing with no interesting runtime requirement should not be written
in assembly and in C to then be maintained in both.

## What is deliberately unspecified

**Which ready `select` arm runs.** The language does not say, so the runtime
polling in source order is correct — and so would randomizing be
([chapter 13.2](ch13-02-channels.md)). A program that depends on the choice
is the thing that is wrong.

A gap belongs in the corpus as a `//gyro:skip <why>`, not in prose. A skip
is a program that will fail the day the gap closes
([chapter 17.3](ch17-03-testing.md)).
