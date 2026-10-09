# The Gyro Book

This is Gyro's documentation: the language, the toolchain, and the machinery
underneath it. It reads front to back — each chapter assumes the ones before
it — but every chapter is also reachable on its own from the table of
contents.

**These chapters are the specification.** The compiler does not define
Gyro's behaviour. A program's output is what this book says it is, a test's
golden file is written by hand to match, and an implementation that
disagrees is the thing that is wrong.
[Chapter 17](ch17-01-the-backend.md) explains why that arrangement exists.

Two companion files sit beside the chapters:

- [`decisions.md`](decisions.md) — what was decided, what was rejected, and
  the reasoning for both. The chapters link into it wherever a rule has a
  story behind it.
- [`agent-context.md`](agent-context.md) — orientation for working *on* the
  compiler rather than *in* the language.
