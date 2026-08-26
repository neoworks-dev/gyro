# 17.3. Testing

Most of the test suite is written in Gyro, not in Go. Two corpora carry it,
and a Go test is what you write when neither corpus can express the
behaviour.

## The execution corpus

`tests/testdata/run/` — programs paired with the output they must produce.

```text
tests/testdata/run/tuples.gyro
tests/testdata/run/tuples.out
tests/testdata/run/cross-module/main.gyro      # multi-module case
tests/testdata/run/cross-module/util.gyro
tests/testdata/run/cross-module/expected.out
```

A single-file case is `NAME.gyro` + `NAME.out`; a multi-module case is a
`NAME/` directory with `main.gyro` and `expected.out`. Every case is built
and run, and held to the golden ([chapter 17.1](ch17-01-the-backend.md)).

**Write the golden by hand.** Deciding what a program should print is the
work; there is no `-update` flag, and pasting in what the compiler produced
turns the corpus into a check that the compiler is deterministic.

Directives, written as comments in the root file:

| directive | meaning |
| --- | --- |
| `//gyro:panic <fragment>` | must abort; the message must contain the fragment; the golden holds stdout before the panic |
| `//gyro:unordered` | concurrent output: the program must succeed, output is not compared |
| `//gyro:skip <why>` | a known gap, with the reason it is one |
| `//gyro:args a b c` | main's arguments, after a fixed `argv[0]` of `program` |
| `//gyro:stdin <line>` | a line of standard input; repeatable |
| `//gyro:exit <code>` | the exit code the program must produce |

`//gyro:exit` cannot combine with `//gyro:panic`, since a panic always exits
1.

**A known gap belongs here as a skip with its reason attached, not in
prose**: a skip is a program that will fail the day the gap closes.

### METALS by hand

A root may also be `NAME.metals` — IR written by hand, which is how the
corpus reaches an instruction no lowering happens to emit. It is built and
run the same way, because it becomes the same thing; only the producer in
front differs. Where `//` is not the comment syntax the directives are
spelled `;gyro:`.

## The diagnostics corpus

`internal/sema/testdata/check/` — a Gyro file that says what it expects of
itself.

```gyro
var x: u8 = 300                //~ E0301 does not fit u8
return xs                      //~ E0609 which the row does not name
                               //~^ help: say so in the row: !{ &xs }
```

The rules:

- every diagnostic must be claimed by a `//~` marker **on its line**
- every marker must be satisfied
- a file with no markers must check clean
- `//~^` claims the line above, one caret per line
- a marker may pin the code (`E0301`) and may claim `help:` or `note:`
  output instead of the message

Fixtures go through the **loader**, so imports resolve for real.

Why this shape: the older helpers matched a substring anywhere in the bag of
diagnostics, which asserts nothing about position. A diagnostic pointing at
the wrong line passed, and so did an unexpected second one. Don't weaken a
fixture to a broad substring check to make it pass.

## What stays in Go

Only what neither corpus can express:

- `parser` — tree shapes, precedence, error recovery, `TestParserAlwaysTerminates`
- `lexer` — token kinds, automatic termination, keyword and non-keyword sets
- `metals` — text-format round trips, verifier rejections
- `lsp` — protocol behaviour
- `loader` — module graph, cycles, resolution
- `diag` — rendering, and the catalogue tests
- `lower` — METALS naming and record layout, nothing about output
- `sema` — focused helpers such as `editDistance`, and the `Info` the
  backends read

## The benchmark suite

`tests/bench/` — Gyro programs that each isolate one codegen decision, and,
where the case is a port, the C it came from.

| case | what it isolates |
| --- | --- |
| `call-fib` | call sequence: prologue, argument moves, return path |
| `loop-index` | loop induction and `index.get` bounds checks |
| `arith-checked` | the overflow test on every add, subtract and multiply |
| `struct-array` | aggregate copies in and out of `Array<T>` |
| `string-concat` | allocation growth and the per-byte `String::push` |
| `startup` | baseline: process start, runtime init, exit |
| `sha256` | ported C: 32-bit arithmetic over a fixed buffer |
| `regex` | ported C: calls, branches and backtracking |
| `jsmn` | ported C: one read and a switch per input byte |
| `cjson` | ported C: one heap node per value, pointer-chased |
| `md4c` | ported C: ~90 functions behind a callback table |
| `perft` | ported C: recursion over u64 bitboards and big tables |

A case is `NAME.gyro` or a `NAME/` directory with `main.gyro`. A directory
case may add `reference.c` — a driver mirroring `main.gyro` — plus the
upstream C files copied in unmodified, which the harness builds at `-O1`
and reports as a `cc` column. A ported case imports its library **out of
the execution corpus**, so each port exists once: the one whose output the
corpus already holds to a golden.

Each program times its own hot region and prints `gyro:bench <nanoseconds>`
on stderr, so setup and process start stay out of the number. Both binaries
in a case must print the same checksum on stdout, because comparing programs
that computed different things measures nothing.

It is off during `go test ./...`:

```sh
make bench                 # everything
BENCH=sha256 make bench    # one case
REPEAT=9 make bench        # more runs; the fastest counts
```

Read the **spread**, not the average: a backend behind by the same factor
everywhere has one problem, a backend at 1.06x on four cases and 2.25x on
the fifth has a specific one. It measures codegen, not correctness — a
performance claim about codegen needs a case here.

## Before a change is done

```sh
go test ./...
make check
make vet
make grammar     # if a tree-sitter grammar or the scanner changed
```

And the ordering rule from [chapter 17.1](ch17-01-the-backend.md): when a
behaviour is new, prefer adding a corpus case over adding a Go test, and
prefer writing the semantic once during lowering over writing it into the
backend and its runtime.
