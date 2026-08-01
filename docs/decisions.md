# Design decisions

A record of what was decided and why. `language.md` describes what Gyro *is*;
this file records why it is that and what was rejected, so settled questions
are not relitigated from scratch.

Status column: **done** means the parser implements it, **sema** means the
syntax exists but no stage checks it, **planned** means neither.

Two rounds of decisions, both on 2026-07-31: the first when the compiler was
ported from TypeScript to Go and the Peggy grammar was dropped; the second in
a full design review that removed traits, made tuples first-class, and made
duck typing the language's one contract mechanism. Entries below marked
*(revised)* changed in the second round; the original choice and the reason
it fell are kept, because the reversal is itself a decision.

---

## The unifying rule: duck typing

**Status: planned.** Wherever Gyro needs a contract, the test is "does the
type have the method", never "did the type declare membership":

- `error` — any type with `message(): string`
- generic functions — any `T` with the methods the body calls
- iteration — any type with `next(*self): (T, bool)` (or `iter()`)
- printing — `to_string(): string` overrides the derived representation

Nothing is ever declared to satisfy a contract. This replaced nominal traits
(below) and is deliberately one rule applied four times rather than four
mechanisms.

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

**Status: done (revised: continuation rules extended).** The lexer inserts a
terminator at a newline when the previous token could end a statement, and
suppresses it when the next line opens with `.`, `,`, `)`, `]` — or, since
the review, a binary operator that cannot begin a statement (`&&`, `+`,
`==`, …). Leading-operator continuation style therefore works alongside
trailing-operator style.

`-`, `*` and `&` are excluded from the leading set because a statement can
begin with them (`*p = 5`, `&x`, `-x`); they must trail the line.

`>` and `?` end statements *(new)*: the stdlib's declaration-only stubs
put types like `Array<T>` and `*Node?` at the end of a line, which no
other closing token follows. The trade-off is that `>` cannot trail a
split comparison — leading-operator style covers that case.

Rejected C-style `if (x) { ... };`. Consequence: a struct literal cannot
start at the top level of a condition, since the brace opens the body. Same
resolution as Rust.

### Primitives carry explicit widths; `usize` and `rune` exist *(revised)*

**Status: done (the names resolve), planned (their semantics).** `i8`–`i64`, `u8`–`u64`, `f32`, `f64`,
`bool`, `string`, `void`, `error` — plus, from the review:

- **`usize`**: pointer-width unsigned. `len()` returns it, `[]` indexes with
  it, `T[N]` sizes are it. The classic unsigned-underflow footgun is blunted
  because arithmetic panics on overflow: `xs.len() - 1` on an empty slice
  panics loudly instead of wrapping.
- **`rune`**: a 32-bit Unicode code point; the type of `'c'` literals and of
  `runes()` iteration. Rejected `u8` char literals ('é' would not fit) and
  removing char literals (byte-level code gets ugly).

Rejected `int` / `float`. The x86-64 backend needs the width anyway, so
naming it avoids platform-dependent sizing.

### Numeric literals are context-typed *(new)*

**Status: planned.** A literal adapts to the type the context requires
(`var h: u32 = 2166136261` needs no cast); with no context, integers are
`i32` and floats `f64`. A literal that does not fit is a compile error.
Typed values never convert implicitly — only literals adapt.

Rejected strict `i32`-always literals: large unsigned constants would not
even fit the default type, forcing casts on every line of bit-twiddling
code.

### Conversions use `as`, and narrowing panics *(new)*

**Status: done (syntax), planned (the panic check).** `x as i64`. Widening always succeeds; a narrowing
conversion that loses value panics, consistent with overflow panics.
Deliberate truncation is `(x & 0xff) as u8`.

Rejected conversion methods (`x.to_i64()` — verbose for something this
common) and type-name calls (`i64(x)` — makes type names callable, a new
expression form colliding with "construction is literals or `::new()`").
`as` already exists as a keyword in import renaming; the positions never
overlap.

### `elif`, never `else if`

**Status: done.** `else if` is rejected with an error pointing at `elif`.

One spelling for one construct. `elif` desugars to a nested `if` and leaves
no trace in the tree.

### `++` and `--` are gone *(revised)*

**Status: done.** Increment is `i += 1`, one way only; the lexer rejects
`++` with an error naming the spelling.

Originally C-style: prefix/postfix, usable in expressions. The review
removed them entirely: expression-position increment hides mutation
mid-expression (`a[i++] = i`), Go's statement-only compromise still leaves
two spellings for the same thing, and `+= 1` already exists. This also
shortens the lexer's statement-termination list.

### Ranges are half-open, one form only *(new)*

**Status: done (syntax), planned (semantics).** `0..10` visits 0 through 9, so
`0..xs.len()` visits every index. No inclusive `..=`; an inclusive walk is a
C-style `for` header.

Rejected inclusive-by-default (an off-by-one trap against zero-based
indexing) and a second `..=` operator (two range spellings for marginal
gain).

### Integer division truncates toward zero *(new)*

**Status: planned.** `-7 / 2 == -3`, `-7 % 2 == -1` — the sign of `%`
follows the dividend, matching C, Go, Rust and the x86 `idiv` instruction.
Divide by zero panics; `i32::MIN / -1` panics as overflow.

Rejected floor division: nicer for modular math but costs correction
instructions on every division.

---

## Modules and visibility

### Visibility is explicit with `export`

**Status: done.** A declaration is module-private unless marked `export`.

Rejected Go's capitalization rule outright — it makes visibility a property
of spelling, which is invisible at the use site and impossible to grep for.

### `main` is the one blessed name *(new)*

**Status: planned.** The compiler finds `fn main` in the root module; it
needs no `export` because nothing imports it — the OS calls it. `async fn
main` is allowed and the runtime drives it.

Rejected requiring `export fn main` (ceremony that documents nothing) and
rejecting async main (would force every async program to hand-start a
runtime).

### If a name is not in the formal syntax, it must be imported

**Status: done.** The name-resolution pass enforces it: an identifier that
is neither declared, imported, nor formal syntax is an error.

There is no universe scope and no predeclared functions. `println` is
imported; `ch.close()`, `xs.len()` and `s.runes()` are methods on
built-in types, so they need no import and introduce no implicit names.

This rule has already changed one decision: `spawn` was a keyword until
closures made a library function possible.

### Decorator names are a fixed set *(new)*

**Status: done.** `@comptime`, `@gpu`, `@concurrent`, `@distributed` are
known to the compiler; sema rejects an unknown `@name`. They are not
imported: they configure compilation itself, so there is no value an import
could bind.

This is a deliberate carve-out from no-universe-scope, on the grounds that a
decorator is an instruction to the compiler, not a name in the program.

### One file is one module

**Status: done.** `import { x } from "./util"` loads `util.gyro` beside
the importing file. Circular imports are rejected with a cycle trace.

Resolution: `@gyro/...` is the bundled standard library, `./x` and `../x`
are relative files, a bare name is a package under `.stack/`. The loader
implements the first two; bare names are an error until the Stack package
manager decides the on-disk layout. Two specifiers that reach the same file
load one module, and re-export paths (`export { a } from "./m"`) are module
dependencies like any import.

The standard library ships inside the compiler as declaration-only `.gyro`
files — real signatures, no bodies — so name resolution and later type
checking see honest interfaces before any runtime exists.

Specifier resolution sits behind a small interface in the loader. That is
deliberate: the long-term plan is a shared IR (**METALS**) that other
languages compile into, with `import` reaching across languages; a foreign
resolver has to be able to slot in without touching sema.

Rejected directory-as-module (Go), which would make `export`
directory-scoped while the import syntax stays file-shaped.

### Named exports only — no `export default`

**Status: done.** Default *imports* were parsed briefly with nothing that
could declare a default export. Rather than complete the symmetry, the
import form was removed.

A default export lets the same value carry different names in different
files, which defeats grep and adds a second export mechanism for no gain.

---

## Errors

### Errors are values returned in a `(T, error?)` tuple

**Status: done (syntax), sema (the check).** The error slot is explicitly
nullable: `null` means no error. Signatures reading `(T, error)` in older
drafts were wrong — a non-nullable `error` slot has no way to say "it
worked".

No exceptions, no unwinding, no propagation operator. The caller must handle
the error, and discarding it requires writing `_` explicitly, so ignoring a
failure is always visible in the source.

Rejected `throws`/`try`, which the book's decorator page used: unwinding
tables and landing pads are a large backend cost, and implicit propagation
is exactly what this design is trying to prevent.

### `error` is satisfied structurally *(revised)*

**Status: planned.** Any type with a `message(): string` method is an
error — nothing is declared. Library code defines error types carrying their
own data and callers match on the concrete kind with a type-test pattern:
`RangeError(e)`.

Originally `error` was a built-in *interface* implemented via
`impl error for RangeError`; that declaration form died with traits, and the
structural rule replaced it. A built-in error struct was considered and
rejected earlier: it cannot carry typed payloads, leaving callers to parse
strings. `(T, string?)` was rejected for the same reason.

### There is no `Result` *(new)*

**Status: planned.** One error mechanism: `(T, error?)` tuples, everywhere.
The concurrency example briefly used `Ok`/`Err` copied from Rust; both the
type and the mutex poisoning it modelled are gone. Poisoning cannot exist in
Gyro at all — it is Rust's answer to a panic *unwinding* past a held lock,
and Gyro panics abort the process. `Mutex::lock()` returns the guard
directly.

Rejected keeping Result as an ordinary stdlib enum: two competing error
styles in the wild for no gain.

### Trait objects use inline storage with a size cap

**Status: planned.** Now specific to `error`, the one dynamically-typed
value in the language: a vtable pointer plus a fixed inline buffer. Small
error types are returned by value with no allocation. A type exceeding the
cap is a compile error naming the size, and the author boxes it explicitly.

The alternative — a hidden heap allocation on every error path — contradicts
explicit `alloc`/`free`.

---

## Memory

### Manual allocation with a checked safety net

**Status: planned.** `alloc<T>()` returns `*T`, `free(p)` releases it,
`defer` runs a call at scope exit. A sema pass tracks allocations and
**warns** when one can reach the end of its scope unfreed and unescaped.

Warnings rather than errors, so arenas and process-lifetime allocations are
not blocked.

Rejected ownership-and-borrows, which is a large sema investment, and
garbage collection, which needs a runtime and conflicts with `@gpu`.

### `defer` runs at block exit *(new)*

**Status: planned.** A defer fires when its enclosing `{ }` ends (Zig's
model), in reverse order of registration, so a loop body's defers run every
iteration.

Rejected Go's function-exit model: `for { var p = alloc<T>(); defer free(p) }`
would pile allocations up until return, and the leak-warning pass reasons
about scopes, not functions.

### Definite assignment, no zero values *(new)*

**Status: planned.** `var x: i32` without an initializer is legal; reading
it before every path has assigned it is a compile error. A receive from a
closed channel yields no value — reading it without checking `open` is the
same error.

Rejected Go-style zero values (values springing into existence unwritten
cuts against everything else here being explicit) and mandatory
initializers (conditional initialization would force dummy values that hide
real bugs).

### Shadowing: inner scopes yes, same scope no *(new)*

**Status: done.** An inner block may redeclare an outer name; declaring
the same name twice in one block is an error. Standard C/Go behaviour, no
special rules. A local's initializer resolves before the name binds, so
`var x = x` reaches the outer x or fails.

### Absence is expressed by nullable types, not a bare null

**Status: planned.** `*Node` can never be null; `*Node?` can. Dereferencing
a `T?` before checking it is a compile error, and the type narrows inside
the check.

Rejected an unrestricted `null`, which would reintroduce null dereference
through pointers after explicit errors had just been chosen to prevent
silent failure. Also rejected `Option<T>` everywhere: correct, but verbose
at every pointer and dependent on generics landing first.

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
currently detects that. An array literal `[1, 2, 3]` is a fixed-size array
in the enclosing scope; slices of it borrow that storage and dangle the same
way if they escape.

Terminology: `T[N]` is a **fixed-size array**, `Array<T>` is a **dynamic
array**. Both use the word; the qualifier disambiguates.

### `string` is a UTF-8 view

**Status: planned.** `len()` counts bytes (returning `usize`), `runes()`
iterates code points as `rune`. Building and concatenation use an owning
`String`.

Rejected refcounted immutable strings, which are a runtime and hidden
allocation, and fixed-width runes, which cost four bytes per character and
transcode at every boundary.

---

## Types

### Traits were removed *(revised — the big one)*

**Status: done.** `trait` and `impl` are no longer keywords; the syntax is
gone from the grammar.

The first round designed nominal traits: `trait Ord`, `impl Ord for
Version`, bounds `<T: Ord & Display>`, operator traits, trait objects. The
review deleted the entire mechanism. What each piece became:

- **Contracts** → duck typing (see the unifying rule at the top). Rust-style
  "having the method isn't enough, you must declare `impl`" bought
  accidental-satisfaction protection at the price of a whole declaration
  layer; the review judged exact-signature collisions rare enough not to pay
  it.
- **Generic bounds** → nothing. `fn max<T>` declares no constraint; the
  body's method calls are the requirement, checked at instantiation (the
  C++-template model). The known cost: errors point into the generic's body
  rather than at the bad call. Considered and not taken: a fixed built-in
  constraint vocabulary (`Comparable`, …) for better error locations, and
  Go-style structural interfaces as constraints — both add a naming layer
  the language just deleted.
- **Operator overloading** → removed entirely (next entry).
- **Trait objects** → survive only as `error`'s representation.
- **`Self`, `impl`, bound-joining `&`** → gone from the grammar.

### Operators are not overloadable *(revised)*

**Status: planned.** Operators work on primitives only. Ordering a user type
is spelled `a.compare(b)`, arithmetic on one is `a.add(b)` — visible calls,
Go's position. `==`/`!=` work on structs, tuples and enums as
compiler-derived field-wise comparison, on pointers as identity, and not on
slices.

The first round mapped operators to a fixed trait set (`Ord`, `Eq`, `Add`…);
that died with traits. Also considered and rejected: C++-style well-known
method names unlocking operators (`compare` enabling `<`) — hidden calls
behind operators for modest sugar.

### Methods are free-standing `fn Type::name(self)` *(revised)*

**Status: done (syntax; sema checks the owner names a type in the module).**

```gyro
fn Stack::push(*self, v: i32) { ... }
export fn Stack::new(capacity: i32): Stack { ... }
```

Structs stay pure data; behaviour is declared beside it at top level, in any
file order. Receivers keep the first-round semantics: `self` borrows
immutably and never copies (a copying receiver would memcpy invisibly per
call), `*self` is a mutable pointer, no receiver means an associated
function called `Stack::new(16)`.

Rejected `class` (first round — implied inheritance fights a simple SSA IR),
methods-inside-struct (mixes data and code back together), and `impl` blocks
(a grouping construct that exists only to hold what free functions can say
alone).

### Generics are duck-typed; user types may be generic *(revised)*

**Status: done (syntax — a written bound is rejected with an error naming
duck typing), planned (instantiation checking).** `fn max<T>(a: T, b: T):
T` — no bounds exist. `struct Pair<T>` and `fn Pair<T>::swap(*self)` are
legal; instantiation stamps out concrete code per type argument.

Bare angle brackets stay: `max<i32>(3, 7)`. Rejected turbofish. Cost: the
parser needs bounded backtracking to tell `f<T>(x)` from `a < b`, resolved
by a speculative parse that commits only when the brackets close and a call
follows. Type arguments live on path segments, since `Array<i32>::new`
parameterizes a non-final segment.

### A struct literal requires every field to be visible

**Status: planned.** A type with private fields is constructed only through
its own associated functions, which is what makes invariants enforceable.
Consequence the examples initially got wrong: `Mutex{ 0 }` is illegal
outside the stdlib — it is `Mutex::new(0)`.

### Tuples are first-class *(revised)*

**Status: done (syntax), planned (semantics).** `(A, B)` is a real type:
usable in `chan<(string, error?)>`, fields, variables. Values are written `(a, b)`, read by
destructuring (`var a, b = pair`) or positional access (`pair.0`). No
one-element tuples; `(expr)` stays grouping. `return a, b` is sugar for
returning a tuple, so multi-value returns are no longer a special form.

This reverses the first round's "no tuple values", which held that `(T,
error)` was a return-only shape and structured data belongs in named
structs. What broke it: a channel could not carry a fallible result — every
worker thread needed a single-use wrapper struct. The iteration protocol
(`next(): (T, bool)`) and channel receive (`var v, open = <-ch`) fell out of
the same shape for free.

### Match is exhaustive, variants qualified, type-tests share the shape *(new)*

**Status: done (newline arms; unqualified variants fail name resolution;
local `Enum::Variant` paths are validated), planned (exhaustiveness).**

- A match over an enum must cover every variant or carry `_`; adding a
  variant later breaks every non-`_` match loudly, which is the point.
- Variant patterns are always written with the enum name:
  `Shape::Circle(r)`, never bare `Circle(r)`. Grep-friendly, and it keeps
  variant destructuring visually distinct from the other `Name(binding)`
  form.
- `RangeError(e)` where the scrutinee is an `error` is a type test binding
  the whole value. Same surface shape as variant destructuring, told apart
  by what the name resolves to. A distinct type-test syntax was considered
  and rejected as a second pattern form to learn.
- Arms end at the newline, not at a comma — same rule as every other
  statement, and it unifies match with `select`. The first round used
  commas.

### Iteration is duck-typed *(new)*

**Status: planned.** `for x in ...` handles slices, arrays, ranges and
channels natively; any other type qualifies by having `next(*self): (T,
bool)` (or `iter()` returning such a type). Rejected a closed built-in set:
user containers could never be iterated.

### Printing is derived, overridable *(new)*

**Status: planned.** Every type prints: `println` and `#{}` use a
compiler-derived representation (`Point{ x: 1, y: 2 }`) unless the type
defines `to_string(): string`. Primitives print natively.

Rejected primitives-only printing (debugging convenience matters more than
avoiding generated code) — the derived form is compiler-emitted, accepted as
the one place code generation is invisible.

---

## Runtime behaviour

### Programmer errors abort

**Status: planned.** Out-of-bounds, divide by zero and null dereference
print a message with source position and terminate. Not returnable, not
catchable.

Keeps the `(T, error?)` channel meaningful — it carries conditions, not
bugs — and needs no unwinding machinery.

### Overflow panics; wrapping is explicit

**Status: done (operators), planned (the check).** `a + b` panics on
overflow; `a +% b`, `a -% b` and `a *% b` wrap deliberately. Narrowing `as`
conversions panic on lost value the same way.

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

`select` arms use the `comm: body` shape of match — newline-terminated, like
match arms after the review. Bindings are ordinary `var` declarations. Go's
`:=` was explicitly rejected — an assignment is `=`, and anything that binds
is `var` or `const`.

Channels have no direction. `chan.send<T>` was considered; parameter naming
carries the intent instead. Channel creation stays call-shaped grammar
(`chan<i32>(64)`) because `chan` is already a type keyword, not a library
name.

### Data races: unchecked today, warning planned *(new)*

**Status: planned.** Nothing stops a captured `*T` from crossing `spawn` and
racing. A sema pass that warns when a pointer crosses a spawn boundary is
planned; full Send/Sync-style checking (Rust) was rejected as a mechanism
the language just chose not to afford. Until the pass lands, races sit next
to dangling slices in the accepted-costs column.

### Async is a function colour

**Status: done (syntax).** `async fn` declares one, `await` resolves a call.
The colour appears in the type system: `async fn(): i32` is a type, async
closures (`async fn() { ... }`) are its values, and `await` works on calls
to either. `async fn main` is allowed.

Rejected making `async` an effect, which would have avoided function
colouring, on the grounds that Rust and JavaScript familiarity is worth more
here than the composition benefit.

---

## Closures

**Status: done (syntax, including the move-only capture list; a move of an
unknown name is a resolution error), sema (capture semantics and
inference).**

Spelled with the same `fn` and the same `:` as a declaration, so the
language has one word for "function". Types may be omitted where the
expected type is known, and a single-expression body needs no braces or
`return`.

Captures: a closure copies the locals it uses, **implicitly**. The capture
list exists only for ownership transfer — `fn[move node]()` — which is the
one case the leak analysis must see in the source. *(Revised: the first
round required stating every capture and had a `copy` form; the review made
copy implicit and dropped `copy`, so brackets appear exactly when ownership
moves.)* `move` stays contextual — an ordinary identifier elsewhere.

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

**Status: done.** The TypeScript prototype and its Peggy grammar were
dropped at commit `efc8c13`.

The grammar was abandoned mid-design and described a different language from
the one intended, so it was not ported. A generated PEG parser was also the
wrong long-term choice: poor error messages, no recovery, packrat memory
cost, and a bad fit for the `syntaxfn` extensions the language is meant to
grow.

### `>>` is scanned as two tokens

**Status: done.** The lexer never merges consecutive `>`, so nested generics
such as `Result<Vec<i32>, error>` close cleanly. The parser rejoins them
when a shift is meant.

### `<-` is scanned greedily and split back apart

**Status: done.** `x<-1` is ambiguous with a comparison against a negative
number. The lexer always produces `<-`; the parser splits it into `<` and
`-` in binary-operator position. Sends are statements only, so the two
readings never collide.

---

### The type checker re-checks generic bodies per instantiation *(new)*

**Status: done.** A generic body is first name-resolved leniently —
operations involving an opaque type parameter poison to Unknown instead of
erroring — and then fully re-checked for every distinct set of type
arguments, C++-template style. That is the honest reading of "the body's
usage is the requirement": the requirement cannot be stated, only tested.
Instantiations are cached by (declaration, arguments), recursion converges
through the cache, and a depth cap of 32 stops types that grow forever.
The error appears twice on purpose: once inside the body where the missing
method is used, once at the call site that demanded the instantiation.

Rejected checking generic bodies once against inferred constraints — that
is traits by the back door, and traits were removed.

### Narrowing is symbol-level and syntactic *(new)*

**Status: done (v1).** `x != null` narrows the local x inside the guarded
branch; the checker follows `&&`, `!`, `else`, and early exits, and an
assignment to x withdraws the narrowing. Closures inherit the facts in
scope — captures copy, so a narrowed copy stays non-null. Narrowing does
not track fields (`self.next != null` proves nothing); bind the field to a
local first. That is the same simplification Go's nil checks live with,
and it keeps the analysis obviously sound.

### Constant expressions fold during checking *(new)*

**Status: done.** Untyped literal arithmetic folds over big integers, so
`var x: u8 = 200 + 56` and `1 << 40` into an i32 are compile errors, not
runtime panics. Operands are typed without the outer context — the folded
result is what must fit. This is also the seed of `@comptime`, which needs
an evaluator anyway.

### Built-in methods are a small fixed surface *(new)*

**Status: done.** `close` on channels; `len` on slices, arrays and
strings (`usize`); `runes(): rune[]` on strings; `compare(other): i32` on
the numeric primitives and `rune` — which is what lets `max(3, 7)`
instantiate; `message(): string` on `error`. There is no `append` on a
slice: `T[]` borrows memory it cannot grow, so growth lives in
`Array<T>::push`. The docs briefly said `xs.append(v)`; that was a relic
and was removed.

### Tuples assign structurally *(new)*

**Status: done.** `(string, null)` goes where `(string, error?)` is
required: tuple assignability is elementwise, since a tuple is shape, not
identity. Everything else converts only by `as`.

### The interpreter is the executable spec *(new)*

**Status: done.** A tree-walking interpreter (`gyro run`) executes checked
programs; every runtime rule the docs assert is now a test. Decisions it
forced:

- **A closure owns one copy of its captures**, taken at creation. Its
  calls mutate that copy cumulatively (Rust's FnMut, C++'s mutable
  lambda); the original local never notices. Rejected copy-per-call,
  which would make a closure-held counter impossible.
- **`as` from float to integer truncates toward zero**, then applies the
  fits-or-panics rule. `2.9 as i32` is 2; `1e300 as i32` panics.
- **A closed channel yields no value.** The single-value receive form
  hands back an uninitialized value whose first read panics — the runtime
  shadow of the definite-assignment rule that will catch it statically.
- **alloc'd memory starts uninitialized**, not zeroed: reading a field
  before writing it panics. `free` poisons the cell, so use-after-free
  and double free are runtime panics today, before any static analysis.
- **Panics do not run defers.** A panic aborts the process; there is no
  unwinding, so `defer` is reserved for orderly exits. This follows from
  "programmer errors abort" and keeps defer implementable without
  landing pads.

## METALS

The cross-language IR is sketched in [`metals.md`](metals.md); the format
lives in `internal/metals`. Eight decisions from the design review:

### METALS is a typed module IR *(new)*

**Status: decided.** Modules, imports/exports, signatures and type shapes
survive into the IR — the JVM/CLR shape. Rejected LLVM-style low-level
SSA: it erases exactly what cross-language `import` needs, which is why
C++ and Rust share LLVM and still cannot call each other without a C ABI
detour. An SSA layer will exist *inside* the native backend, privately.

### Bodies are registers plus structured control flow *(new)*

**Status: decided.** Virtual registers with fixed types (reassignable,
not SSA), and `block`/`loop`/`br`/`if`/`switch`/`select` for control
flow — no goto. Rejected a stack machine (harder to read and to consume)
and a flat CFG (source structure arrives shredded; verification is
harder).

### No generics in the IR *(new)*

**Status: decided.** Front ends monomorphize; METALS sees concrete types
only. Generic exports across a boundary become concrete instantiations
or `dyn`. Rejected CLR-style IR generics as one of the most complex
parts of that design, bought before a second front end exists.

### `dyn` is the escape hatch for dynamic languages *(new)*

**Status: decided.** One dynamic value type with checked ops (dyn.wrap,
dyn.unwrap, dyn.call, dyn.get/set). Rejected strict-only (unannotated
Python could never be imported) and an untyped core (throws away Gyro's
checker exactly at the boundary).

### Values copy at language boundaries *(new)*

**Status: decided.** No shared mutable memory between runtimes; `ptr`,
`chan` and `future` are banned from boundary signatures; long-lived
foreign state is an opaque `handle`. This is the WASM-component call,
and it is what lets manual memory and garbage collectors coexist.
Rejected one shared object model (forces every runtime onto one memory
model) and handles-only (even an i32 would feel like FFI).

### Concurrency is hybrid, matching the language *(new)*

**Status: decided.** `chan<T>` is an IR type; `send`, `recv`, `select`,
`await` are verified instructions; `spawn` and mutexes are runtime
imports. The same line the grammar draws — channels and select are
grammar, spawn is not.

### Verified from day one *(new)*

**Status: decided.** Third parties will emit METALS, so the IR trusts no
producer: full type checking of registers and instructions, definedness
on all paths, boundary-rule enforcement. Rejected verify-later; format
ambiguities surface at retrofit time, which is the expensive time.

### Text format first *(new)*

**Status: decided.** A canonical readable form with printer and parser,
so tests diff IR as text; the binary encoding comes later. Same road
LLVM (.ll) and WASM (.wat) took.

### Expanded names are quoted, not mangled *(new)*

**Status: decided.** Monomorphization and module qualification produce
one METALS function per instantiation, and its name is the source name:
`@"Pair<i32>::sum"`, `@"list/util::helper"`. A name after a sigil may be
a quoted string; plain names stay bare words. Rejected dot-mangling
(nested generics become ambiguous) and opaque names with a debug table
(kills the "text first, diffable" property).

### Lowering emits one whole-program module *(new)*

**Status: decided.** The Gyro→METALS lowering collapses the loader graph
into a single self-contained module: only `main` exported, imports only
from `gyro:rt`. Exactly the input the native backend needs. One METALS
module per Gyro module returns when the linking/loading model is
designed; it is recorded as still open.

### `opt.wrap` / `opt.unwrap` *(new)*

**Status: decided.** The IR gained the two optional conversions,
symmetric with `dyn.wrap`/`dyn.unwrap`: `opt.wrap` makes `opt<T>` from
`T`, `opt.unwrap` extracts and aborts on null, null tests are `eq`/`ne`
against `const.null`. Optionals do not nest, matching the language.
Rejected encoding `T?` as `tuple<T, bool>` (needs a fabricated T and
there are no zero values) and as a generated two-variant enum (drags a
type declaration behind every optional).

### The METALS evaluator is the lowering oracle *(new)*

**Status: decided.** A tree-walking evaluator for verified METALS lands
with the lowering pass; tests run every example as `gyro run` and as
lower→verify→evaluate and diff the output. The same executable-spec
discipline the interpreter gives the checker, and the reference the
native backend will be diffed against. Rejected verifier+golden-text
only: a lowering bug found later must be debugged through generated
assembly.

## Native backend

Design in [`native.md`](native.md).

### The runtime is C on libc *(new)*

**Status: decided.** `gyro:rt` is implemented as a small C file compiled
and linked alongside the generated assembly. libc supplies the
allocator, formatted output and pthreads; the produced binary depends on
libc the way Rust's and C's do by default. Rejected freestanding raw
syscalls (own allocator, own float formatter, threading via `clone` —
months before the first program runs) and a runtime authored in
assembly (a worse language for ring buffers and formatters, with no
offsetting benefit).

### System V AMD64 everywhere *(new)*

**Status: decided.** Every generated function uses the standard Linux
ABI, so a runtime call is a plain `call` and GDB, perf and valgrind
understand the frames. One deliberate deviation, documented in
`native.md`: aggregate arguments between generated functions pass by
pointer to a caller-owned value rather than by SysV classification —
the C boundary only ever sees scalars and pointers, so it stays exactly
ABI-correct where it matters. Rejected a custom internal convention:
simpler codegen, but every runtime call would need a shim and every
debugger would be blind.

### v1 codegen spills everything *(new)*

**Status: decided.** Every METALS register gets a stack slot;
instructions load operands, compute, store back. The output is an order
of magnitude slower than necessary and trivially auditable, which is
the correct trade while the bar is byte-identical output against the
evaluator. Register allocation becomes its own milestone on a working
foundation. Rejected linear-scan allocation from day one: a much larger
bug surface before the first example passes.

### Concurrency v1 is OS threads *(new)*

**Status: decided.** `spawn` is `pthread_create`, channels are a
mutex-and-condvar ring buffer in the C runtime (capacity 0 is a true
rendezvous), async calls start eagerly on a thread and `await` joins,
`select` polls its arms. Every example runs natively, so the diff tests
keep full coverage. Green threads with growable stacks remain the right
long-term answer for cheap spawning and are recorded as still open;
a sequential-only v1 was rejected because it leaves the concurrent
examples untested.

## Editor tooling

### Grammars and language server live in this repository *(new)*

**Status: decided.** `tree-sitter-gyro/` and `tree-sitter-metals/` are
top-level directories here, and the language server is `gyro lsp`, a
subcommand of the one compiler binary. Tooling versions in lockstep with
the language while the syntax is still moving; Neovim consumes local
paths and one command. Rejected separate repositories and a standalone
binary: the publishing convenience buys nothing while there is exactly
one user, and coordinated changes (new syntax, new grammar, new
highlighting) would need three synchronized commits. See
[`tooling.md`](tooling.md).

### The LSP layer is hand-rolled, zero dependencies *(new)*

**Status: decided.** `internal/lsp` implements Content-Length framing,
JSON-RPC dispatch, and only the protocol structs the server actually
uses; `go.mod` keeps zero external dependencies. The alternative —
`go.lsp.dev/protocol` or `glsp` — brings a huge generated type surface
for the dozen methods v1 speaks. The framing is ~100 lines; the cost was
an afternoon, the control is total.

### v1 features: diagnostics, hover, definition, symbols, completion *(new)*

**Status: decided.** Live parse+sema diagnostics, hover with the checked
type and the declared signature, cross-file go-to-definition through
`sema.Info.Refs`, document symbols, and completion (scope names, `.`
members, `::` namespaces, keywords). Completion resolves types against
the last analysis whose sema pass ran, because the buffer being
completed in rarely parses. METALS files get parse and verifier
diagnostics. Deferred, not rejected: rename, find-references, signature
help — they need position spans the AST does not record yet (nodes know
where they start, not where they end).

## Diagnostics

### An error states the fact; notes under it guess the fix *(new)*

**Status: decided.** A diagnostic message says only what is wrong
(`undeclared type String`). Everything about what the author probably
meant goes in `Diagnostic.Notes`, printed indented under the message and
folded into one message by the language server. Rejected: explaining the
language's rules in the message itself. `undeclared name X; there is no
universe scope, so it must be declared or imported` restated a design
decision to someone who was looking for a spelling or an import, and the
rule is the same in every occurrence of the error, so it carried no
information about *this* one.

### Undeclared names guess: spelling, primitive, import, method *(new)*

**Status: decided.** For an unresolved name the checker emits up to three
notes, in this order: the primitive it is a foreign spelling of (`String`
→ `string`, `Int` → `i32`); the import that would bind it — a name added
to an import already in the file, or a new import of a bundled module;
that it is a method on a built-in rather than a free function (`len(xs)`
→ `xs.len()`); and the nearest name in scope by edit distance. Member,
variant, associated-function and missing-export errors carry the same
"did you mean" note over their own candidate set.

The import and method notes need to know what the bundled modules
provide, so `stdlib.Lookup()` parses the declaration stubs once and
indexes them. It is the stubs themselves that are indexed, not a hand-
maintained table, so the hints cannot drift from what `@gyro/stdlib`
actually exports.

A guess appears only within one edit for short names, two from five
characters up, and never more than half the shorter name — otherwise
`x` would be offered as the fix for `z`.

### A message repeated at one position is reported once *(new)*

**Status: decided.** `diag.Bag` drops a diagnostic whose position,
severity and text match one it already holds. A function signature is
resolved twice — once when types are built, once when the body is
checked — so every error in a signature was reported twice. Deduplicating
in the bag fixes it for every pass at once, and two identical messages at
one position never told the reader anything the first did not.

## Still open

| question | why it matters |
| --- | --- |
| definite assignment | "no zero values" is only half-enforced: struct literals must set every field, but reading an unassigned `var x: i32` passes |
| match exhaustiveness | decided (exhaustive over enums), not yet implemented |
| mutation rules | when a `*self` method may be called on what, and what `self` may not do |
| `@comptime` evaluation subset | which operations may run at compile time |
| guard/resource release | `MutexGuard::unlock` is explicit because Gyro has no destructors; whether a drop/RAII mechanism ever exists affects guards, `free`, and `close` alike |
| decorator execution model | the book has them mutating the AST on separate threads |
| `syntaxfn` parser extensions | the METALS-facing feature, entirely unspecified |
| METALS remainder | the format is implemented end to end (spec, verifier, Gyro lowering, evaluator); still open: what the name expands to, the linking/loading model, the binary encoding, foreign-runtime embedding, whether effect rows enter signatures |
| mutable METALS globals | METALS globals are constant literals, so module-level `var` does not lower; module-level `const` re-evaluates per use through a synthesized getter until `@comptime` folding exists |
| checker/interpreter divergence on untyped operands | the checker folds `1 << 40` into an i64 context and adapts untyped bitwise operands, but the interpreter evaluates them as i32 at runtime and panics; found while diff-testing the lowering, which sides with the checker |
| shift overflow semantics | the interpreter panics when `<<` overflows the operand's type, but METALS `shl` wraps (Go semantics), so the evaluator and the native backend agree with each other and not with the interpreter; whether `<<` is checked or wrapping needs a language decision |
| dangling slices | accepted cost of borrowing slices, currently undetected |
| Stack package manager | resolution of bare import names depends on it; the loader rejects them until the `.stack/` layout is decided |
| derived `==`/repr for recursive types | field-wise derivation must handle `*Node?` cycles |
| receive vs tuple destructuring | on `chan<(A, B)>`, `var a, b = <-ch` could mean the (value, open) form or destructuring the element; currently it means (value, open), so the element is received whole and destructured afterwards |
| memory reclamation | native binaries free only what the program frees; strings, closure environments and other implicit allocations leak until an ownership/GC decision exists |
| printing composites natively | the lowering imports `println<T>` even for records and slices; per `metals.md` it should synthesize a per-type `-> str` function so the runtime only formats primitives — required before a program may print a composite natively |
| green threads | OS threads make `spawn` cost a full pthread; a userspace scheduler is the long-term answer |
| select fairness | the evaluator picks a ready arm at random (Go semantics); the native runtime polls arms in order — programs whose output depends on the choice among simultaneously-ready arms may diverge |
