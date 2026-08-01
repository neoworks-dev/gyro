# Gyro

A systems language with explicit control over memory, concurrency and
compile-time execution — and the front end for a longer-term experiment in
compiling several languages into one shared intermediate representation.

> **Status: early. The compiler front end works; there is no code generation
> yet.** `gyro ast` and `gyro check` parse real programs and report real
> diagnostics. Nothing runs. See [Roadmap](#roadmap) for what is and is not
> built.

```gyro
import { println } from "@gyro/stdlib"

export struct Stack {
  export capacity: i32
  top: i32
}

impl Stack {
  export fn new(capacity: i32): Stack {
    return Stack{ capacity: capacity, top: 0 }
  }

  fn is_empty(self): bool {
    return self.top == 0
  }
}

fn main() {
  var stack = Stack::new(16)
  println("empty: #{stack.is_empty()}")
}
```

## Why

Gyro is the ground floor of a larger idea. The eventual target is **METALS**,
a shared intermediate language that several source languages compile into, so
that a single project can import Python, JavaScript and Gyro and have them
compiled together rather than glued together at runtime.

That is a hard problem and it is not what this repository is trying to solve
yet. METALS does not exist. The bet is that the only credible way to get
there is to first have one language whose semantics are fully pinned down and
whose compiler actually produces machine code — so Gyro comes first, and the
intermediate representation it lowers to is designed to become METALS rather
than being thrown away.

The related work lives in separate repositories: **meta**, a META-II style
compiler-compiler, and **gyro-book**, the prose documentation.

## Design decisions

The choices below are settled and implemented in the parser. The full grammar
is in [`docs/language.md`](docs/language.md).

| area | decision |
| --- | --- |
| declarations | `var` / `const`, annotation optional, types inferred |
| functions | `fn f(a: i32): i32` — return type after a colon |
| closures | anonymous `fn`, inferred types, explicit capture lists |
| syntax | no semicolons, no parentheses on `if` / `while` / `for` |
| primitives | explicit widths: `i8`–`i64`, `u8`–`u64`, `f32`, `f64` |
| types | `struct` holds data, `impl` holds methods, `trait` holds contracts |
| generics | `fn max<T: Ord & Display>` — bounds joined by intersection |
| null | `*T` is never null; only `T?` is, and it must be checked |
| visibility | explicit `export`; no capitalization rules, no default exports |
| modules | one file is one module |
| errors | `(T, error?)` pairs, checked; `error` is an interface |
| panics | out-of-bounds, null deref and overflow abort; `+%` wraps on purpose |
| memory | manual `alloc` / `free`, `defer`, leak analysis warns |
| slices | `T[]` borrows, `Array<T>` owns and grows |
| pattern matching | `match subject { pattern: body, ... }` |
| concurrency | threads, channels (`chan<T>`, `select`), `async` / `await` |
| effects | `!{ io, alloc }` rows — parsed, not yet checked |

Every one of these, with the alternatives that were rejected and why, is
recorded in [`docs/decisions.md`](docs/decisions.md).

Two rules drive most of the rest:

**Nothing is implicit.** If a name is not part of the formal syntax, it has
to be imported. There is no universe scope, so `println` is imported while
`ch.close()` and `xs.len()` are methods on built-in types. Visibility is
declared, never inferred from spelling. Even `spawn` is a library function
rather than a keyword — it takes a closure, so it needs no grammar of its
own. Closures name what they capture for the same reason.

**Errors cannot escape by accident.** A fallible function returns
`(T, error)`, and discarding the error requires writing `_` explicitly.

## Getting started

Requires Go 1.24 or newer.

```sh
git clone https://github.com/gyro-lang/gyro
cd gyro
make build
```

```sh
./bin/gyro check examples/structs.gyro    # parse and report diagnostics
./bin/gyro ast examples/structs.gyro      # dump the syntax tree
./bin/gyro tokens examples/hello.gyro     # dump the token stream
./bin/gyro explain E0401                  # what a diagnostic code means
```

Diagnostics print as a code frame, with the span underlined and the fix
suggested under it:

```
error[E0401]: undeclared name println
 --> hello.gyro:2:3
  |
2 |   println("hi")
  |   ^^^^^^^ not found in this scope
  |
  = help: "@gyro/stdlib" exports println; add: import { println } from "@gyro/stdlib"
```

```sh
make test    # run the test suite
make check   # parse every example
make fmt     # gofmt
```

The programs in [`examples/`](examples) are the working reference for the
syntax; each one parses cleanly and is checked by the test suite.

| example | shows |
| --- | --- |
| `hello.gyro` | basics, string interpolation |
| `fibonacci.gyro` | recursion, loops, `@comptime` |
| `structs.gyro` | structs, impl blocks, enums, `match` |
| `errors.gyro` | `(T, error)` returns and explicit handling |
| `memory.gyro` | `alloc` / `free`, `defer`, nullable pointers, escape |
| `closures.gyro` | anonymous functions, inference, capture lists |
| `traits.gyro` | traits, bounds, operator traits |
| `concurrency.gyro` | threads, channels, `select`, `async` / `await` |

## Architecture

The compiler is written in Go. It was originally a TypeScript prototype built
on a Peggy grammar; that was replaced because a generated PEG parser gives
poor error messages, is slow, and would have fought the syntax extensions the
language is meant to support. The old implementation is preserved in git
history at commit `efc8c13`.

```
main.go              CLI driver
internal/token/      token kinds and keywords
internal/lexer/      hand-written scanner, terminator insertion
internal/ast/        syntax tree and its dumper
internal/parser/     recursive descent, precedence climbing for expressions
internal/diag/       source positions and diagnostics
examples/            reference programs
docs/language.md     grammar and semantics
docs/decisions.md    what was decided, and what was rejected
```

The parser is hand-written recursive descent, with a precedence-climbing loop
for expressions. It buffers the whole token stream, which makes the bounded
backtracking needed to tell `a < b` from `f<T>(x)` a matter of resetting an
index.

Three ambiguities are worth knowing about, because they shaped the design:

- **`f<T>(x)` versus `a < b`.** Resolved by a speculative parse that only
  commits when the angle brackets close and a call follows.
- **`ch <- v` versus `x < -1`.** The lexer scans `<-` greedily; the parser
  splits it back apart in binary-operator position. Sends are statements, so
  the two never collide.
- **`Array<i32>::new()` versus `a < b`.** Type arguments can sit on any path
  segment, so the same speculative parse decides whether `<` opens an
  argument list or is a comparison.
- **`if Point { }`.** A struct literal cannot start at the top level of a
  condition, since the brace opens the body. Same resolution as Rust.

## Roadmap

**Done**

- [x] Lexer with automatic statement termination
- [x] Recursive-descent parser and full syntax tree
- [x] Diagnostics with real source positions and error recovery
- [x] CLI: `check`, `ast`, `tokens`
- [x] Test suite and reference examples

**Next — v0.2, semantic analysis**

- [ ] Name resolution, scopes, module graph
- [ ] Type checking and inference
- [ ] Exhaustiveness checking for `match`
- [ ] Unused-error and leak analysis

**v0.3 — code generation**

- [ ] SSA-form intermediate representation (the seed of METALS)
- [ ] Register allocation
- [ ] x86-64 backend
- [ ] `@comptime` evaluation

**Later**

- [ ] Effect row checking and inference
- [ ] Generic instantiation
- [ ] `@gpu`, `@concurrent`, `@distributed` lowering
- [ ] Stack, the package manager
- [ ] METALS, and non-Gyro front ends

## Contributing

The language design is still moving. Before implementing anything
substantial, open an issue — a decision recorded in `docs/language.md` is
worth more right now than code against a design that is about to change.

New syntax needs, in order: an entry in `docs/language.md`, a parser test, and
an example program that exercises it.

## License

MIT. See [LICENSE](LICENSE).
