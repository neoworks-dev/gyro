# Design decisions

A record of what was decided and why. `language.md` describes what Gyro *is*;
this file records why it is that and what was rejected, so settled questions
are not relitigated from scratch.

Status column: **done** means the parser implements it, **sema** means the
syntax exists but no stage checks it, **planned** means neither.

All decisions below were taken on 2026-07-31, when the compiler was ported
from TypeScript to Go and the abandoned Peggy grammar was dropped.

---

## Surface syntax

### Declarations use `var` and `const`; types are inferred

**Status: done.** `var x: i32 = 0`, `var y = 42`, `const MAX: i32 = 100`.

Rejected `let` / `let mut`. `var` and `const` match what the book already
used, and immutability is not tracked yet.

### The return type follows a colon

**Status: done.** `fn add(a: i32, b: i32): i32`.

Rejected `->`. Using `:` keeps one marker for "the type of this thing" across
parameters, fields and results. It later paid off: closures reuse the same
form, so there is no second spelling to learn.

### No semicolons and no parentheses around conditions

**Status: done.** The lexer inserts a terminator at a newline when the
previous token could end a statement, and suppresses it when the next line
opens with `.`, `,`, `)` or `]`.

Rejected C-style `if (x) { ... };`. Consequence: a struct literal cannot
start at the top level of a condition, since the brace opens the body. Same
resolution as Rust.

### Primitives carry explicit widths

**Status: done.** `i8`–`i64`, `u8`–`u64`, `f32`, `f64`, `bool`, `string`,
`void`, `error`.

Rejected `int` / `float`. The x86-64 backend needs the width anyway, so
naming it avoids platform-dependent sizing.

### `elif`, never `else if`

**Status: done.** `else if` is rejected with an error pointing at `elif`.

One spelling for one construct. `elif` desugars to a nested `if` and leaves
no trace in the tree.

---

## Modules and visibility

### Visibility is explicit with `export`

**Status: done.** A declaration is module-private unless marked `export`.

Rejected Go's capitalization rule outright — it makes visibility a property
of spelling, which is invisible at the use site and impossible to grep for.

### If a name is not in the formal syntax, it must be imported

**Status: done (rule), sema (enforcement).**

There is no universe scope and no predeclared functions. `println` is
imported; `ch.close()`, `xs.len()` and `xs.append(v)` are methods on built-in
types, so they need no import and introduce no implicit names.

This rule has already changed one decision: `spawn` was a keyword until
closures made a library function possible.

### One file is one module

**Status: planned.** `import { x } from "./util"` loads `util.gyro` beside
the importing file. Circular imports are rejected with a cycle trace.

Resolution: `@gyro/...` is the bundled standard library, `./x` and `../x` are
relative files, a bare name is a package under `.stack/`.

Rejected directory-as-module (Go), which would make `export` directory-scoped
while the import syntax stays file-shaped.

### Named exports only — no `export default`

**Status: done.** Default *imports* were parsed briefly with nothing that
could declare a default export. Rather than complete the symmetry, the import
form was removed.

A default export lets the same value carry different names in different
files, which defeats grep and adds a second export mechanism for no gain.

---

## Errors

### Errors are values returned in a `(T, error)` pair

**Status: done (syntax), sema (the check).**

No exceptions, no unwinding, no propagation operator. The caller must handle
the error, and discarding it requires writing `_` explicitly, so ignoring a
failure is always visible in the source.

Rejected `throws`/`try`, which the book's decorator page used: unwinding
tables and landing pads are a large backend cost, and implicit propagation is
exactly what this design is trying to prevent.

### `error` is a built-in interface

**Status: planned.** Any type implementing `message(): string` is an error,
so library code defines error types carrying their own data and callers match
on the concrete kind.

This is what forced traits into the next milestone. A built-in struct was
considered and rejected: it cannot carry typed payloads, leaving callers to
parse strings.

### Trait objects use inline storage with a size cap

**Status: planned.** A trait object is a vtable pointer plus a fixed inline
buffer. Small error types are returned by value with no allocation. A type
exceeding the cap is a compile error naming the size, and the author boxes it
explicitly.

This is the same question the slice decision raised: the alternative — a
hidden heap allocation on every error path — contradicts explicit
`alloc`/`free`.

---

## Memory

### Manual allocation with a checked safety net

**Status: planned.** `alloc<T>()` returns `*T`, `free(p)` releases it, `defer`
runs a call at scope exit. A sema pass tracks allocations and **warns** when
one can reach the end of its scope unfreed and unescaped.

Warnings rather than errors, so arenas and process-lifetime allocations are
not blocked.

Rejected ownership-and-borrows, which is a large sema investment, and garbage
collection, which needs a runtime and conflicts with `@gpu`.

### Absence is expressed by nullable types, not a bare null

**Status: planned.** `*Node` can never be null; `*Node?` can. Dereferencing a
`T?` before checking it is a compile error, and the type narrows inside the
check.

Rejected an unrestricted `null`, which would reintroduce null dereference
through pointers after explicit errors had just been chosen to prevent silent
failure. Also rejected `Option<T>` everywhere: correct, but verbose at every
pointer and dependent on generics landing first.

Postfix `?` binds to the outermost type, so `*Node?` is a nullable pointer.
Write `*(Node?)` for a pointer to a nullable value.

### `T[]` borrows; `Array<T>` owns

**Status: planned.** A slice is a pointer and a length with no ownership, no
append and no reallocation. Growth lives in an owning `Array<T>`, freed
explicitly. `string` is the same arrangement over UTF-8 bytes, with `String`
as the owning builder.

An owning, growing `T[]` was rejected because `append` would emit an
allocation the programmer never wrote, assignment would have to choose
between aliasing and copying, and — decisively — `@gpu` and `@distributed`
cannot use a global heap, so a second array type would be needed regardless.

Known cost, accepted: a slice can outlive its backing memory, and nothing
currently detects that.

Terminology: `T[N]` is a **fixed-size array**, `Array<T>` is a **dynamic
array**. Both use the word; the qualifier disambiguates.

### `string` is a UTF-8 view

**Status: planned.** `len()` counts bytes, `runes()` iterates code points.
Building and concatenation use an owning `String`.

Rejected refcounted immutable strings, which are a runtime and hidden
allocation, and fixed-width runes, which cost four bytes per character and
transcode at every boundary.

---

## Types

### Nominal traits with `impl Trait for Type`, and generic bounds

**Status: planned.** A type satisfies a trait only where an `impl` says so.
Generics constrain with `<T: Ord>`. `Self` refers to the implementing type.

Rejected structural interfaces: implicit satisfaction contradicts explicit
visibility and explicit captures, and permits accidental satisfaction.

### Operators map to a fixed set of traits

**Status: planned.** `Ord` for `< > <= >=`, `Eq` for `== !=`, `Add`/`Sub`/
`Mul`/`Div` for arithmetic, `Index` for `[]`. Nothing else is overloadable.

Required by the decision above: `fn max<T: Ord>` uses `a > b`, so the
operator has to reach a trait method.

### Structs hold data, `impl` holds methods

**Status: done.** Rejected `class`, whose implied inheritance is a large
semantic commitment that fights a simple SSA IR.

`self` borrows immutably and never copies the receiver; `*self` is a mutable
pointer. A copying receiver was rejected because a large struct would incur an
invisible memcpy per call.

### A struct literal requires every field to be visible

**Status: planned.** A type with private fields is constructed only through
its own associated functions, which is what makes invariants enforceable.

### Generics use bare angle brackets

**Status: done (syntax).** `max<i32>(3, 7)` and `VectorAdd<1, 8>(A, B, C)`.

Rejected turbofish. Cost: the parser needs bounded backtracking to tell
`f<T>(x)` from `a < b`, resolved by a speculative parse that commits only when
the brackets close and a call follows.

### No tuple values

**Status: done.** `(T, error)` is a return-type form destructured by
`var a, b = f()`. `(expr)` stays unambiguously grouping, and structured data
belongs in named structs.

---

## Runtime behaviour

### Programmer errors abort

**Status: planned.** Out-of-bounds, divide by zero and null dereference print
a message with source position and terminate. Not returnable, not catchable.

Keeps the `(T, error)` channel meaningful — it carries conditions, not bugs —
and needs no unwinding machinery.

### Overflow panics; wrapping is explicit

**Status: done (operators), planned (the check).** `a + b` panics on
overflow; `a +% b`, `a -% b` and `a *% b` wrap deliberately.

Rejected Rust's debug-checked/release-wrapping split, where one expression
means different things in different build modes, and rejected always-wrap,
which optimizes for the rare intentional case.

---

## Concurrency

Three mechanisms answering three different questions: threads for
parallelism, channels for communication, async for IO concurrency.

### Channels and `select` are grammar; `spawn` is not

**Status: done.** `chan<T>`, `ch <- v`, `<-ch` and `select` are syntax.
`spawn` is an ordinary stdlib function taking a closure.

`spawn` was briefly a keyword, which was wrong: the keyword existed only
because there were no closures, and it broke the rule that anything outside
the formal syntax must be imported.

`select` arms use the `comm: body` shape of match, and bindings are ordinary
`var` declarations. Go's `:=` was explicitly rejected — an assignment is `=`,
and anything that binds is `var` or `const`.

Channels have no direction. `chan.send<T>` was considered; parameter naming
carries the intent instead.

### Async is a function colour

**Status: done (syntax).** `async fn` declares one, `await` resolves a call.

Rejected making `async` an effect, which would have avoided function
colouring, on the grounds that Rust and JavaScript familiarity is worth more
here than the composition benefit.

---

## Closures

**Status: done (syntax), sema (capture rules and inference).**

Spelled with the same `fn` and the same `:` as a declaration, so the language
has one word for "function". Types may be omitted where the expected type is
known, and a single-expression body needs no braces or `return`.

Captures are explicit: `fn[move node]()` transfers ownership, `fn[copy n]()`
duplicates, a bare name means copy. `move` and `copy` are contextual
identifiers, not keywords.

Explicit capture exists to serve the leak analysis: it makes an allocation's
escape into a closure visible in the source instead of inferred.

Rejected `(a: i32) -> i32 { }`, which would introduce a second return-type
marker and require backtracking on a leading `(`.

---

## Effects

**Status: sema.** Signatures may carry `!{ io, alloc }`. The parser records
these and nothing checks them.

Reserved now so that adding the checker later is not a breaking change to
every signature ever written. The intended rule: module-private functions
have their row inferred, exported ones must state it.

The motivation is the decorators. `@comptime` needs purity, `@gpu` forbids
allocation and syscalls, `@distributed` restricts what crosses a node
boundary. Each is currently a bespoke check; effect rows are how they become
one mechanism. METALS needs it more than Gyro does — a foreign function's
effects are the one thing that cannot be recovered from its source.

---

## Implementation

### Go, with a hand-written parser

**Status: done.** The TypeScript prototype and its Peggy grammar were dropped
at commit `efc8c13`.

The grammar was abandoned mid-design and described a different language from
the one intended, so it was not ported. A generated PEG parser was also the
wrong long-term choice: poor error messages, no recovery, packrat memory
cost, and a bad fit for the `syntaxfn` extensions the language is meant to
grow.

### `>>` is scanned as two tokens

**Status: done.** The lexer never merges consecutive `>`, so nested generics
such as `Result<Vec<i32>, error>` close cleanly. The parser rejoins them when
a shift is meant.

### `<-` is scanned greedily and split back apart

**Status: done.** `x<-1` is ambiguous with a comparison against a negative
number. The lexer always produces `<-`; the parser splits it into `<` and `-`
in binary-operator position. Sends are statements only, so the two readings
never collide.

---

## Still open

| question | why it matters |
| --- | --- |
| `@comptime` evaluation subset | which operations may run at compile time |
| decorator execution model | the book has them mutating the AST on separate threads |
| `syntaxfn` parser extensions | the METALS-facing feature, entirely unspecified |
| dangling slices | accepted cost of borrowing slices, currently undetected |
| Stack package manager | resolution of bare import names depends on it |
