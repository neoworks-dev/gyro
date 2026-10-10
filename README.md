# Gyro

A systems language with manual memory, explicit effects, and a compiler that
refuses more programs than it has to — and the front end for a longer-term
experiment in compiling several languages into one shared intermediate
representation.

> **Status: early, but real.** Programs parse, type-check, run, lower to the
> METALS IR, verify, evaluate, and compile to native Linux x86-64 binaries.
> Gyro is usable for CLI tools today and unusable for anything long-running,
> because nothing reclaims memory yet. See
> [what is not built](docs/appendix-g-known-gaps.md).

```gyro
import { println, Array } from "stdlib"
import { heap } from "mem"

export struct Stack {
  export capacity: i32
  items: Array<i32>
}

export fn Stack::new(capacity: i32): Stack {
  return Stack{ capacity: capacity, items: Array<i32>::new(heap()) }
}

export fn Stack::push(*self, value: i32) {
  self.items.push(value)
}

// A slice borrows, so the row names where the view came from. Without
// `&self` this does not compile.
export fn Stack::view(self): i32[] !{ &self } {
  return self.items.slice()
}

export fn Stack::free(*self) {
  self.items.free()
}

fn total(values: i32[]): i32 {
  var sum = 0
  for value in values {
    sum += value
  }
  return sum
}

fn main() {
  var stack = Stack::new(16)
  defer stack.free()

  stack.push(2)
  stack.push(40)
  println("total: #{total(stack.view())}")
}
```

## Documentation

**[The Gyro Book](docs/SUMMARY.md)** is the language documentation and the
specification: syntax and semantics chapter by chapter, the toolchain, and
appendices for the IR, the native backend, the standard library and the
diagnostics.

- [`docs/decisions.md`](docs/decisions.md) — what was decided, what was
  rejected, and why
- [`docs/agent-context.md`](docs/agent-context.md) — orientation for working
  on the compiler itself

## Why

**The goal that drives every design decision:** Gyro should be explicit about
side effects and unwanted control flow, and safe enough that a machine can
generate code in it and be caught when it is wrong. Not "safe" as marketing —
safe as in the compiler refuses the program.

That is why there are no zero values, no implicit conversions between typed
values, overflow that panics rather than wraps, exhaustive matches, definite
assignment, borrow provenance written into signatures, and leak and race
warnings.

The longer-term bet is **METALS**: a shared intermediate representation that
several source languages compile into, so a project can import them and have
them compiled together rather than glued together at run time. That is no
longer a sketch — METALS is specified, verified and linked, and it has two
producers today: Gyro, and a C front end that a Gyro program reaches when it
imports a `.c` file.

The related work lives in a separate repository: **meta**, a META-II style
compiler-compiler.

## Design at a glance

| area | decision |
| --- | --- |
| declarations | `var` / `const`, annotation optional, types inferred; no `:=` |
| functions | `fn f(a: i32): i32` — return type after a colon |
| methods | free-standing `fn Type::name(self)`; an `impl Trait for Type` block is how a type keeps a promise to a trait |
| traits | `trait`, `impl`, bounded generics (`<T: Eq & Hash>`), `dyn Trait` values, operator traits; `Eq`, `Hash`, `Print` and `Ord` are explicit |
| syntax | no semicolons, no parentheses on `if` / `while` / `for`; `elif`, never `else if` |
| primitives | explicit widths: `i8`–`i64`, `u8`–`u64`, `usize`, `f32`, `f64`, `rune` |
| null | `*T` is never null; only `T?` is, and it must be checked |
| visibility | explicit `export`; no capitalization rules, no default exports |
| modules | one file is one module |
| errors | `(T, error?)` pairs, checked; `error` is any type with `message()` |
| panics | out-of-bounds, null deref, divide by zero and overflow abort; `+%` wraps on purpose |
| memory | manual `alloc(a)` / `free(a, p)` over explicit allocators, `defer` at block exit, leak analysis warns |
| slices | `T[]` borrows, `Array<T>` owns and grows |
| pattern matching | `match subject { pattern: body }`, exhaustive over enums |
| concurrency | threads, channels (`chan<T>`, `select`), `async` / `await` |
| effects | `!{ io }` rows, checked; `!{ &xs }` says what the result borrows |
| C | `@extern("SDL2")` links a symbol; `import "./x.c"` compiles the C in |

Two rules drive most of the rest:

**Nothing is implicit.** If a name is not part of the formal syntax, it has
to be imported. There is no universe scope, so `println` is imported while
`xs.len()` and `ch.close()` are methods on their types. Even `spawn` is a
library function rather than a keyword — it takes a closure, so it needs no
grammar of its own.

**Errors cannot escape by accident.** A fallible function returns
`(T, error?)`, and discarding the error requires writing `_` explicitly.

## Getting started

Requires Go 1.24 or newer. `gyro build` additionally needs `nasm` and a C
compiler, on Linux x86-64.

```sh
git clone https://github.com/gyro-lang/gyro
cd gyro
make build
```

```sh
./bin/gyro check examples/structs.gyro       # type-check, report diagnostics
./bin/gyro run examples/structs.gyro         # build it somewhere temporary, run it
./bin/gyro build examples/hello.gyro -o hi   # native binary
./bin/gyro explain E0401                     # what a diagnostic code means
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
  = help: "stdlib" exports println; add: import { println } from "stdlib"
```

The full command list is in
[the book](docs/ch16-01-the-gyro-command.md); editor setup, including a
working Neovim configuration, is in
[chapter 16.2](docs/ch16-02-editors.md).

## Examples

The programs in [`examples/`](examples) are the working reference for the
syntax; each one is checked by the test suite.

| example | shows |
| --- | --- |
| `hello.gyro` | bindings, interpolation, an effect row |
| `fibonacci.gyro` | recursion, loops, `@comptime` |
| `structs.gyro` | structs, free methods, enums, `match`, borrows |
| `errors.gyro` | `(T, error?)` returns and explicit handling |
| `memory.gyro` | `alloc` / `free`, `defer`, nullable pointers, `move` |
| `closures.gyro` | anonymous functions, inference, capture lists |
| `methods.gyro` | methods, traits, bounded generics, operator traits, `to_string` |
| `traits.gyro` | a trait with a default method, `dyn Trait` in an `Array`, `Iterator` |
| `variadics.gyro` | `...T` packs, `...any`, `printf` |
| `concurrency.gyro` | threads, channels, `select`, `async` / `await` |
| `triangle.gyro` | a Gouraud-shaded triangle in an SDL window, through `@extern` |

## Architecture

The compiler is written in Go, with no dependencies.

```text
source
  → internal/lexer      scanner, automatic statement termination
  → internal/parser     recursive descent, precedence climbing
  → internal/loader     module graph, dependency order
  → internal/sema       name + type checking, all the safety analyses
  → internal/lower      Gyro → METALS IR
  → internal/metals     IR format, printer, parser, verifier, linker
  → internal/nasm       METALS → x86-64 NASM
                        linked against internal/crt, the C runtime
```

Everything the compiler can read becomes METALS, and everything that runs,
runs from METALS. The other producer is `internal/cc`, which reads C
directly and keeps its declared types, so an imported `struct Point *`
arrives as a pointer to a record rather than as a number. Editor support
lives here too: `tree-sitter-gyro/`, `tree-sitter-metals/` and
`internal/lsp`.

The parser is hand-written recursive descent with a precedence-climbing loop
for expressions. It buffers the whole token stream, which makes the bounded
backtracking needed to tell `a < b` from `f<T>(x)` a matter of resetting an
index. Three ambiguities shaped the design:

- **`f<T>(x)` versus `a < b`**, and `Array<i32>::new(heap())` with it — resolved by
  a speculative parse that commits only when the angle brackets close.
- **`ch <- v` versus `x < -1`** — the lexer scans `<-` greedily and the
  parser splits it back apart in binary-operator position. Sends are
  statements, so the two never collide.
- **`if Point { }`** — a struct literal cannot start at the top level of a
  condition, since the brace opens the body.

## Testing

**The implementation is not the specification.** Every program in the
execution corpus is built, run, and held to a golden file that is **written
by hand**. There is no flag that regenerates a golden from what the compiler
happened to do — deciding what a program should print is the work, and a
harness that copied the answer out of the compiler would be checking only
that the compiler is deterministic.

```sh
make test      # go test ./...
make check     # type-check every example
make vet
make bench     # the backend against the C, over tests/bench
make grammar   # regenerate and test both tree-sitter grammars
```

The corpora and what belongs in each are described in
[chapter 17.3](docs/ch17-03-testing.md).

## Contributing

The language design is still moving. Before implementing anything
substantial, open an issue — a decision recorded in the book is worth more
right now than code against a design that is about to change.

New syntax needs, in order: an entry in the book chapter that owns the topic
([`docs/SUMMARY.md`](docs/SUMMARY.md) indexes them), a parser test, an
example program that exercises it, corpus coverage, and an entry in
[`docs/decisions.md`](docs/decisions.md) recording what was rejected.

Gyro has no users, so nothing gets a migration path: when a spelling changes
it changes everywhere, and the old one is deleted rather than deprecated.

## License

MIT. See [LICENSE](LICENSE).
