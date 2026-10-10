# 17.2. How a program is compiled

```text
source
  → internal/lexer      scanner, automatic statement termination
  → internal/parser     recursive descent, precedence climbing
  → internal/loader     module graph, dependency order
  → internal/sema       name + type checking, all the safety analyses
  → internal/lower      Gyro → METALS IR
  → internal/metals     IR format, printer, parser, verifier
  → internal/nasm       METALS → x86-64 NASM
                        linked against internal/crt, the C runtime
```

**Everything the compiler can read becomes METALS, and everything that runs,
runs from METALS.** There is no second execution path.

## The front end

The lexer inserts statement terminators ([chapter 3.5](ch03-05-lines-and-comments.md)).
The parser is hand-written recursive descent with a precedence-climbing loop
for expressions; it buffers the whole token stream, which makes the bounded
backtracking needed to tell `a < b` from `f<T>(x)` a matter of resetting an
index.

The loader resolves the import graph before anything is checked, which is
what makes whole-program facts available to the checker
([chapter 7](ch07-00-modules.md)).

`internal/sema` is the bulk of the compiler: name resolution, type checking,
and every safety analysis — definite assignment, leak warnings, spawn race
warnings, borrow provenance and match exhaustiveness. Those analyses are
deliberate language properties, not optional linting, and each has a chapter
above.

## Lowering

`internal/lower` turns a checked program into **one self-contained METALS
module**:

- generics monomorphized under quoted names (`@"Pair<i32>::sum"`)
- closures lifted to top-level functions with heap environment records
- defers inlined at every exit
- `match` lowered to tag tests
- narrowing made explicit as `opt.unwrap`
- `error` as a closed program-wide enum
- stdlib natives as `gyro:rt` imports
- module-level `const` as a synthesized getter; module-level `var` as METALS
  storage, zero-initialized, with initializers run before main's own body
- derived printing synthesized here — one function per composite type,
  reached as an ordinary call

That last bullet is the pattern to imitate: implement a semantic once during
lowering rather than downstream in the backend and its runtime
([chapter 17.1](ch17-01-the-backend.md)).

`gyro metals file.gyro` prints the result.

## METALS

METALS is a **typed, verified, whole-program module IR** — the JVM/CLR
shape, not LLVM's. It keeps modules, imports and exports, function
signatures and declared type shapes, so interop is native to the format
rather than bolted on. [Appendix D](appendix-d-metals.md) is the
specification.

Two properties matter from the language side:

- **The verifier is real.** It type-checks every register and instruction,
  checks that every register is assigned before it is read on every path,
  and enforces the boundary rules. It does not trust its producers,
  including the ones in this repository.
- **Several modules link into one** before any consumer sees them, and that
  link step is the only pass that compares an importer's declared signature
  against the exporter's actual one.

## More than one producer

METALS has two producers:

| producer | what it reads |
| --- | --- |
| `internal/lower` | Gyro |
| `internal/cc` | C directly, emitting METALS' own types |

`internal/cc` is what a Gyro program reaches when it imports a `.c` file
([chapter 15](ch15-00-c-interop.md)). It is not only a convenience: it is
the reason a rule in the format is a rule rather than a description of what
Gyro's lowering happens to do. Anything the IR assumes about its input that
holds only because `internal/lower` satisfies it shows up here as a wrong
answer or a verifier complaint.

When a producer has broken a rule, the rule was **tightened**, not
loosened. That is the standing instruction: a producer that cannot be
compiled is a producer to fix, or a rule to make stricter, never a verifier
check to relax.

## The back end

```sh
gyro build app.gyro -o app    # assemble, then link with cc
```

`internal/nasm` emits x86-64 assembly, `internal/x86` assembles it into an
object, and `cc` links that against `internal/crt`, one file of C holding what has to exist at runtime:
formatting, strings, the allocator floor and the start of a thread behind
a plain C ABI. The runtime is C rather than assembly because none of it is
codegen, and reading it should not require reading assembly.

The rule that governs the boundary between them: **storage is C's** — a
value in memory is what `sizeof` says in C, at C's alignment, padded where
C pads — while **a register is one word**, carrying a scalar widened to a
qword and an `f32` as a double.
[Appendix E](appendix-e-native-backend.md) has the layout table and the
conversions.

## Where a bug tends to be

A wrong answer is usually a representation mismatch rather than a wrong
instruction: when anything crosses between generated code and the C
runtime carrying a slice or an element, the stride and the width are the
first things to check. `gyro metals file.gyro` prints what lowering
produced, and `gyro nasm file.gyro` prints what the backend made of it —
reading the METALS first says which of the two is wrong.
