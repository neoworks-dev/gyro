# 17.2. How a program is compiled

```text
source
  → internal/lexer      scanner, automatic statement termination
  → internal/parser     recursive descent, precedence climbing
  → internal/loader     module graph, dependency order
  → internal/sema       name + type checking, all the safety analyses
  → internal/lower      Gyro → METALS IR
  → internal/metals     IR format, printer, parser, verifier
  ├─→ internal/meval    METALS evaluator
  ├─→ internal/nasm     METALS → x86-64 NASM
  └─→ internal/cgen     METALS → C
                        both link internal/crt, the shared C runtime
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
- derived printing synthesized here — one function per composite type, which
  every tier reaches as an ordinary call

That last bullet is the pattern to imitate: implement a semantic once during
lowering rather than three times downstream
([chapter 17.1](ch17-01-three-tiers.md)).

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
  and enforces the boundary rules. It does not trust its producers, because
  Gyro is not the only one.
- **Several modules link into one** before any consumer sees them, and that
  link step is the only pass that compares an importer's declared signature
  against the exporter's actual one.

## More than one producer

METALS has four producers, and the extra ones exist to keep the format
honest — anything the IR assumes about its input that only holds because
Gyro's lowering happens to satisfy it shows up elsewhere as a wrong answer
or a verifier complaint.

| producer | what it reads |
| --- | --- |
| `internal/lower` | Gyro |
| `internal/lisp` | a statically typed Scheme |
| `internal/wasm` | wasm32, which is what clang makes of C |
| `internal/cc` | C directly, emitting METALS' own types |

The wasm bridge is what stopped the format being polite: C's memory model
needed linear memory, `trunc` and `bitcast`, and each is a thing the IR
arguably should always have had for a producer that is not Gyro. When a
producer broke a rule, the rule was **tightened**, not loosened — the
closure-environment rule got stricter when the Lisp front end broke it, and
the third producer got linear memory added rather than `ptr<T>` weakened.

`internal/cc` is what a Gyro program reaches when it imports a `.c` file
([chapter 15](ch15-00-c-interop.md)).

## The back ends

Two native backends, siblings over the same runtime:

```sh
gyro build app.gyro -o app              # nasm + cc
gyro build app.gyro --backend c -o app  # cc alone
```

The C backend exists for two reasons, neither of them replacing the
assembler: a disagreement between the two native tiers on the same module is
a codegen bug and the C one is far easier to read, and it gives a second
binary from the same IR to benchmark the assembler's output against.

The rule that governs both: **storage is C's** — a value in memory is what
`sizeof` says in C, at C's alignment, padded where C pads — while **a
register is one word**, carrying a scalar widened to a qword and an `f32` as
a double. [Appendix E](appendix-e-native-backends.md) has the layout table
and the conversions.

## Where a bug tends to be

The evaluator is the tier closest to what the lowering emitted, so a
lowering bug shows up there first. A representation mismatch in a backend
produces *correct evaluator output* and native garbage, which reads like a
backend bug and often is not one — when anything crosses the Go/C boundary
carrying a slice or an element, the stride and the width are the first
things to check.
