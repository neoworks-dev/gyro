# 17.1. Three tiers, no oracle

This is the single most important architectural fact about the project, and
it changes how you read every other chapter.

**Every program runs three ways and all three must agree byte for byte:**

| tier | what it is |
| --- | --- |
| `metals` | the METALS evaluator, written in Go |
| `native` | a binary from the assembler backend (x86-64 NASM) |
| `cnative` | a binary from the C backend (generated C) |

**No tier defines the answer.** This book does. A test's golden file is
written by hand to say what a program prints, and a tier that disagrees with
it is wrong — including when it is the only tier that disagrees, and
including when all three agree with each other and none of them agrees with
the documentation.

## Why it is arranged this way

There is no flag that regenerates a golden from a tier's output. That is
deliberate, and it is the lesson from the first version of this corpus,
which rewrote goldens from the tree-walking interpreter: what that bought
was a tier whose bugs became the specification until someone read the file.

A tier that can write its own exam has stopped being a check on anything.

So when tiers disagree, none of them is presumptively right. Read the
chapter that covers the behaviour, decide what the program *should* print,
and record the decision in [`decisions.md`](decisions.md) if the docs did
not already answer it.

## What this buys

Moving the execution tests into one corpus immediately surfaced five
divergences that had existed for a long time: `println(null)` did not lower,
deferred call arguments were evaluated at exit rather than at registration,
printing a tuple needed a synthesized representation, and the native runtime
detected neither use-after-free nor uninitialized reads. Each tier had kept
its own hand-picked subset of tests, and each subset had been chosen — not
deliberately — to avoid what that tier got wrong.

## The semantics every tier must reproduce

The book pins these, and all three tiers have to produce them identically:

- overflow and underflow panic, per width; wrapping is `+% -% *%`
- division truncates toward zero, so `-7 / 2 == -3`, and `%` takes the sign
  of the dividend
- a narrowing `as` that loses the value panics
- bounds, null and uninitialized-read panics carry source positions
- `defer` runs at block exit in reverse order — per loop iteration included,
  with arguments captured at registration
- closures capture by copy
- structs have value semantics
- printing is derived, with `to_string` overriding it
- `spawn`, channels and `select` run on real threads
- `async` is futures
- float formatting is shortest round-trip, with the same exponent thresholds
  everywhere

Each has a chapter above; this list is what a backend is measured against.

## Where behaviour should be implemented

**Once, at the highest shared layer possible.** Prefer synthesizing
behaviour during lowering over duplicating it in the evaluator and in each
backend.

Derived printing is the worked example. It began as a runtime import the
evaluator could answer and the native backend could not; moving it to one
synthesized METALS function per type fixed both tiers at once, with no
backend and no C work ([chapter 17.2](ch17-02-the-pipeline.md)).

The same instinct explains why so much of the standard library is ordinary
Gyro: two native implementations of something with no interesting runtime
requirement are two places to diverge.

## Divergences that are allowed

A few, each recorded rather than tolerated quietly:

- **Which ready `select` arm runs** is unspecified by the language, so the
  evaluator randomizing and the native runtime polling in source order are
  both correct ([chapter 13.2](ch13-02-channels.md)).
- **Use-after-free and uninitialized reads** are caught by the evaluator,
  which poisons cells, and not by the native runtime, which frees for real.
  Those cases live in the corpus as skips with the reason attached.
- **A program that calls a C function under `@extern`** runs on the native
  tiers only: the evaluator is Go with no C in it, and refuses the program
  rather than inventing an answer ([chapter 15](ch15-00-c-interop.md)).
- **Type punning** — reading one object's bytes as another type — is
  undefined in C, and the tiers differ on purpose: the natives reinterpret
  the bytes, and the evaluator knows what is really there and says so.

A gap belongs in the corpus as a `//gyro:skip <tier> <why>`, not in prose. A
skip is a program that will fail the day the gap closes
([chapter 17.3](ch17-03-testing.md)).
