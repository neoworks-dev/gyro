# Design decisions

A record of what was decided and why. The book ([`SUMMARY.md`](SUMMARY.md))
describes what Gyro *is*; this file records why it is that and what was
rejected, so settled questions are not relitigated from scratch.

Status column: **done** means the parser implements it, **sema** means the
syntax exists but no stage checks it, **planned** means neither.

Three rounds of decisions: the first on 2026-07-31, when the compiler was
ported from TypeScript to Go and the Peggy grammar was dropped; the second
the same day, in a full design review that removed traits, made tuples
first-class, and made duck typing the language's one contract mechanism; and
the third on 2026-10-10, which brought traits back ("Traits") and replaced
the duck typing with them. Entries below marked *(revised)* changed in the
second or third round; the original choice and the reason it fell are kept,
because the reversal is itself a decision.

---

## One contract mechanism: traits *(revised)*

**Status: done.** Wherever Gyro needs a contract, a type states on purpose
that it keeps it:

- generic functions and types — bounds on the type parameters, which the
  body may use and nothing more
- iteration — `impl Iterator<T> for Type`
- printing — `impl Print for Type`, optionally with a `to_string` that
  replaces the derived text
- equality, hashing and ordering — `Eq`, `Hash` and `Ord`
- the operators — `Add`, `Sub`, `Mul`, `Div`, `Rem`, `Neg`
- indexing — `Index<T>`

This is one rule applied everywhere, replacing the duck typing that was
the unifying rule from the second round: "does the type have the method".
The reasoning is in "Traits". The one exception is `error`, which is still
any type with `message(): string`; folding it into traits is future work.

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

### The default type is range-checked like any other *(new)*

**Status: done.** `var x = 3000000000` is an error, and so is
`println(1 << 40)`: defaulting decides a type, and a value that does not
fit the type it is given is rejected wherever the decision is made.
Before this, `intFits` ran only in `adaptUntyped` — the path a *context*
type takes — and defaulting went around it, so an i32 could hold
3000000000 in every tier. The tiers agreed, which is why it was not a
divergence but a type claiming a value it cannot hold.

The check lives in one place, `defaultExpr`, which every site that
defaults an expression's type goes through: a `var` without an
annotation, a range endpoint, an array or tuple element with no context,
a `match` arm, a closure's expression body, an argument wrapped into
`any`, and an argument that infers a generic parameter. Sites that
default a *type* with no expression at hand (the receiver of a built-in
method, a destructured tuple element) keep plain `types.Default`; the
expression they came from was already checked where it was written.

Rejected silently widening the default to i64 when the value needs it:
the width of `var x = ...` would then depend on the magnitude of a
constant, and passing `x` to `fn f(v: i32)` would break for reasons
invisible at the call. Rejected wrapping to the i32 the type promises,
Go's answer for `int` conversions but not for constants — Gyro panics on
overflow at runtime, so silently overflowing at compile time would be
the one place arithmetic lies. Go rejects the same program
("constant 3000000000 overflows int"), and annotating is the fix in both.

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

**Status: done.** `else` takes a block, so `else if` does not parse.

One spelling for one construct. `elif` desugars to a nested `if` and leaves
no trace in the tree. The dedicated "use `elif`" error was removed: Gyro has
no users migrating from a version that accepted `else if`, and an error that
exists to catch a habit from another language is weight the grammar has not
earned.

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

**One end may be left off in an index position**, and only there: see
"A slice narrows; a string does not" below.

### An index is a place, on every container *(new)*

**Status: decided.** `xs[i]` works on an `Array<T>` as well as on a slice
and a fixed-size array, and on all three it is a *place*: `xs[i] = v`
assigns into the container and `&xs[i]` is the element's own address.

**The point is `get` returning a copy.** `Array<T>::get` answers with a
value, so `xs.get(0).x = 9` mutates a copy and nothing else — it compiles
clean and does nothing, which the md4c port called out as genuinely
error-prone where it held two handles into one table and mutated through
both. `xs[i]` lowers through a new `Array<T>::at(self, index): *T`
instead, so an index reaches the element where it lies.

`get` stays. A copy is what you want often enough, and it is the only one
of the two that is safe to hold across a `push` — which reallocates, and
therefore invalidates an element address exactly as `push_back`
invalidates `&vec[i]` in C. Nothing detects that, which is the hazard the
slice already has.

**It is also cheaper**, which was not the reason but is worth recording:
`gy_array_get` bounds-checks and then memcpys into a staging cell for the
caller to read; `gy_array_at` bounds-checks and returns the address,
after which the load is an ordinary load. One call either way, one copy
fewer.

**`index.ptr` grew a slice operand** to go with it, which is what makes
`&xs[i]` work on a slice too. That is one question — the checked address
of element `i` — with two places its bound comes from, the `N` in an
array's type or the length word a slice carries. It is deliberately not
the thing "An element behind a pointer is reached, not copied" rejected:
that was widening `index.get` to a second *storage model* while leaving
the write-back it exists to remove. This widens the extent's source, and
the alternative is `ptr.offset`, which cannot check at all.

### A slice narrows; a string does not *(new)*

**Status: decided.** `xs[lo..hi]` is a window onto storage `xs` already
views — half-open, bounds-checked, and free. Either end may be left off;
`xs[..]` is refused. A fixed-size array narrows to a `T[]`, and so does
an `Array<T>`, through the view it already hands out. A `string` does not
narrow at all.

This closes the gap md4c named as "the first thing a callback-driven
library asks for": a C API shaped `(pointer, size)` had to become a
`(base, beg, end)` triple threaded through every signature, with real
copies where md4c points into the document without copying.

**It is narrowing and not pointer arithmetic, and that is the whole
point.** A slice already carries its length, so a window is a bounds
computation on a thing that knows its own extent — checked, where
`ptr.offset` cannot be. The two are not competing answers to one
question: pointer arithmetic is how C's arrays work and slices are how
Gyro's do, and "Pointer arithmetic is three instructions, and unchecked"
already recorded why the second cannot borrow the first.

**A window is a value, not a place.** `xs[0..2] = ys` and `&xs[0..2]` are
both errors: the view is built at the expression and has no storage of
its own. Its *elements* are places, as they always were.

**`string` is left out on purpose.** Its length is in bytes and its
content is UTF-8, so `s[0..2]` could cut a code point in half and answer
with a `string` that is not text. Rejected **narrowing by bytes anyway**,
which makes the type system stop meaning what it says; rejected
**narrowing by runes**, which is O(n) in an operation that is O(1)
everywhere else and would make `s[a..b]` and `xs[a..b]` cost differently
for no visible reason. The question is recorded under "Still open".

**Both bounds are checked together, and the abort names all three
numbers.** `xs[2..1]` has both ends inside the length and is still wrong,
which is a case "index out of range" has nothing to say about — so the
message is `slice 2..1 out of range for length 3`.

`slice.of` grew a second operand form as a consequence: it takes the
*address* of an array as well as an array. Narrowing an addressable
fixed-size array through the value form would have loaded it first, and a
window onto that copy is storage nothing else can see. Same argument as
`index.ptr`, and the extent is static either way.

**The parser reads an index specially** rather than letting the binary
loop do it, because `xs[lo..]` has no right operand for `..` to take.
That is contained to the bracket: `for x in a..` still does not parse,
since a bare end there names no sequence.

### Integer division truncates toward zero *(new)*

**Status: planned.** `-7 / 2 == -3`, `-7 % 2 == -1` — the sign of `%`
follows the dividend, matching C, Go, Rust and the x86 `idiv` instruction.
Integer divide by zero panics; `i32::MIN / -1` panics as overflow. Float
division by zero is IEEE and does not panic — see Runtime behaviour.

Rejected floor division: nicer for modular math but costs correction
instructions on every division.

### A variadic parameter is `...T`, and `any` is what makes it heterogeneous

**Status: decided.** A final parameter written `name: ...T` collects the
remaining arguments and is a `T[]` slice inside the body. Mixed-type packs
are not a second mechanism: they are `...any`, one element type like every
other pack.

The reason is what asks for the feature. Nothing inside Gyro needs
variadics: `println<T>` takes one value and interpolation covers
formatting, which is why the language got this far without them. What asks
is C code being ported, and every one of those calls is `printf` — mixed
types in one call. So the heterogeneous case had to work; splitting it into
its own parameter form would have been two features where the type system
already offers one.

Rejected: a pack that is a compile-time tuple, monomorphizing the callee
per call-site shape (Zig's `anytype` plus `inline for`). It needs no runtime
type information at all, which is a genuine attraction in a language that
has none. It was rejected because it cannot index a pack with a computed
index, so the printf loop — walk the format string, consume the next
argument — has to become compile-time unrolling, and the pack stops being a
value you can pass around. `...any` keeps a pack an ordinary slice.

Rejected: making the pack the whole feature and leaving `...T` out. A
homogeneous pack is what falls out of the slice machinery for free, and
`sum(1, 2, 3)` is worth having; once `any` exists, supporting both is one
code path, not two.

Forwarding is a trailing `...` at the call site: `sum(rest...)` passes the
pack, `sum(rest)` tries to pass a slice as one element and fails. The
explicit spelling is required rather than inferred, because with a `...any`
parameter an `any[]` argument is ambiguous between the two readings and
nothing in the types can break the tie.

### `any` is a closed sum, not a runtime type registry

**Status: decided.** `any` holds one value and remembers its type. It is
first-class — variable, field, slice element, channel element. `x as any`
wraps and never fails; `v as i32` unwraps and panics on disagreement, the
same rule a narrowing conversion follows. `match` tests instead of
asserting, spelled exactly like the type test that already exists on an
`error`, and an `any` is an open subject there, so exhaustiveness does not
apply and falling off the end panics. Printing an `any` prints what is
inside it.

Packing into a `...any` parameter is the one implicit conversion in the
language. Assignment still refuses: `var x: any = 42` is an error and
`42 as any` is the fix. The exception is narrow on purpose — the call
constructs the pack, so the wrap is part of the call form rather than a
silent coercion between two typed values, and requiring `as any` on every
argument would put the noise exactly on the ported C code the feature is
for.

**Implementation, which is why this is cheap.** The earlier note here said a
heterogeneous pack "needs a dynamic value carrying a runtime type tag, in
all four tiers", with METALS `dyn` as the substrate. That was wrong, and
the reason is that Gyro compiles whole programs: the set of types any
`as any` in the program mentions is known at compile time, so `any` lowers
to a synthesized closed enum with one variant per wrapped type. This is not
a new mechanism — `error` is already lowered exactly this way, as a closed
sum over every concrete error struct in the program. Wrapping is `variant`,
the match test is `variant.tag`, unwrapping is a tag check plus
`variant.field`, and printing is a `switch` calling each type's existing
derived repr. No new IR instruction, no runtime type tags, no boxing, no
allocation, and nothing to implement in the evaluator or either native
backend — enums already work in all four tiers.

`dyn` keeps its own job, which is foreign dynamic languages crossing a
METALS boundary. Gyro's `any` is not that and does not use it.

What this costs, recorded because it is real: an `any` value is as wide as
the widest type the program ever wraps, since the payload is inline rather
than boxed — the same trade `error` already makes. Boxing aggregates was
considered and rejected: it would make every `...any` call allocate, and
allocation on `printf` is a worse deal than a wide value.

**A type test may name a type no path can spell.** `(i32, string)(pair)`,
`i32[](xs)`, `i32[3](fixed)` and `*Node(node)` are match arms, and the same
types work with `as`. A pattern that is a plain name stays what it was,
because `Point(p)` and `Shape::Circle(r)` are the same syntax until the
subject says which one is meant; only the forms that start with `(` or `*`,
or that carry a `[` after the path, are unambiguous enough to read as a
type. The alternative was to restrict an `any` to types that have a name,
which is simpler and was rejected because "wrap a struct around your tuple"
is a workaround for a limit the type system does not otherwise have.

This used to cost a tier divergence, and the tier that caused it is gone:
the tree-walking interpreter had no static types, so it answered a type
test by walking the value's own shape, and an empty container left no
trace of its element type — it accepted `v as string[]` and `v as i32[]`
alike for one. Every remaining tier runs monomorphized METALS and knows
the type statically.

**Equality on an `any` is enum equality**: the same type inside, holding
equal values. Two `any`s holding different types are unequal rather than an
error, because that is the question `==` was asked. Finding this turned up a
defect that predated the feature — a fixed-size array compared `false`
against an identical one in the METALS evaluator, while both native
backends derived the elementwise comparison. The evaluator was fixed to
agree with the backends; `tests/testdata/run/array-equality.gyro` pins
it.

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
imported; `ch.close()`, `xs.len()` and `s.len()` are methods on
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

Resolution: `./x` and `../x` are relative files, and a bare name is the
standard library, falling back to a package under `.stack/`. The loader
implements the first two; a bare name that is not bundled is an error until
the Stack package manager decides the on-disk layout. Two specifiers that
reach the same file load one module, and re-export paths
(`export { a } from "./m"`) are module dependencies like any import.

The standard library ships inside the compiler as declaration-only `.gyro`
files — real signatures, no bodies — so name resolution and later type
checking see honest interfaces before any runtime exists.

Specifier resolution sits behind a small interface in the loader. That is
deliberate: the long-term plan is a shared IR (**METALS**) that other
languages compile into, with `import` reaching across languages; a foreign
resolver has to be able to slot in without touching sema.

Rejected directory-as-module (Go), which would make `export`
directory-scoped while the import syntax stays file-shaped.

### The standard library is imported by bare name *(new)*

**Status: decided.** `import { println } from "stdlib"`, not
`"@gyro/stdlib"`. The prefix was pure ceremony on every import in every
file, and it made the standard library read like a third-party scope when
it is the one thing always present. Go, Python and Rust all spell their
own library plainly; only npm-style ecosystems carry a vendor scope, and
Gyro's library has no vendor to distinguish.

This takes the bare-name namespace, which had been reserved for `.stack/`
packages. A bare specifier now resolves to a bundled module first and a
package second, so the standard library shadows a package of the same
name. The rule that keeps the two apart is still open — Go's "no dot in
the first path element" is the obvious candidate, and it costs nothing to
adopt later since no package manager exists yet.

Module *identity* keeps a prefix even though the specifier does not: the
loader records a bundled module as `gyro:stdlib`, the METALS scheme for
Gyro modules. A specifier is what the author writes and a path is what the
compiler keys on; conflating them would make `mem` ambiguous with a file
called `mem` in the working directory. The backends ask
`stdlib.IsBundled(path)` rather than matching a prefix themselves.

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

**Status: planned.** Specific to `error`, whose value is a vtable pointer
plus a fixed inline buffer. Small error types are returned by value with no
allocation. A type exceeding the cap is a compile error naming the size, and
the author boxes it explicitly.

The alternative — a hidden heap allocation on every error path — contradicts
explicit `alloc`/`free`.

This is not how a trait value (`dyn Trait`) is represented. A trait value
borrows ("Trait values are a pointer and a table"); inline storage was
considered for them and rejected there, because a cap that rejects types
by size is the wrong rule for a general-purpose value and because it copies
the value, so mutating through one would mutate a copy.

---

## Definite assignment

### Every path to a read must have written the name *(new)*

**Status: done.** `var x: i32` with no initializer holds nothing, and reading
it before every path has written it is a compile error. There are no zero
values, so the alternative is reading storage that means nothing.

The analysis only ever adds to its set — once assigned, always assigned — so
it needs no repeated pass over a loop. Whatever a loop body writes cannot be
relied on afterwards anyway, because the body may not run. Branches are the
whole difficulty: two paths are walked separately and only the names both
wrote survive the join. The narrowing facts could not express that, which is
why this needed a state that merges rather than one that is pushed and
popped.

Rejected making it a warning. A read of unwritten storage has no meaning to
recover, unlike a leaked allocation, which an arena makes deliberate.

### `&x` counts as assigning x *(new)*

**Status: done.** Taking the address of a local that holds nothing is legal,
and it marks the local assigned from there on.

The storage is real and sized whether or not anything has been written to it;
`&x` names a place rather than reading one. Writing through the pointer is
the ordinary reason to take it, and `var p = &x; *p = 5; println(x)` has to
work. The analysis cannot follow what happens through a pointer, so the
address-of is where it stops tracking.

The cost is a hole: `var p = &x; println(*p)` reads storage nothing wrote,
and only the evaluator's runtime check catches it — the native tiers do
not poison. Accepted on the same terms as dangling slices.

Rejected Rust's rule, which forbids `&x` until x is initialized and offers
`MaybeUninit<T>` as the escape. It buys soundness with a second type and a
second set of rules for the same storage, and Gyro's arithmetic and bounds
checks are already runtime-checked rather than proved away.

### A two-name receive is guarded by its flag *(new)*

**Status: done.** `var v, open = <-ch` leaves `v` readable only where `open`
is true. A closed channel yields no value, not a zero value, so the flag is
the only thing that says whether the value exists.

The guard rides the same `&&` / `||` / `!` plumbing as null narrowing, so
`if !open { return }` leaves `v` readable below, exactly as `if x == null
{ return }` leaves x narrowed.

---

## Memory

### Manual allocation with a checked safety net

**Status: done.** `alloc<T>()` returns `*T`, `free(p)` releases it, `defer`
runs a call at scope exit. A sema pass tracks allocations and **warns** when
one can reach the end of its block unfreed and unescaped.

Warnings rather than errors, so arenas and process-lifetime allocations are
not blocked.

Escaping is read broadly on purpose: a return, a send, a call argument, a
struct or array literal field, and a `move` capture all settle the question,
because a callee may keep what it is handed and following it is exactly what
this pass cannot do. The bias is toward silence — a missed leak costs less
than a warning on correct code, which teaches people to ignore the category.

Only a bare name settles. `node.value` reads through the pointer and leaves
ownership where it was, so a block that only reads its allocation is still
reported.

A `return` is treated as an exit the check sees rather than a place to stop
looking. Checking only the closing brace missed `var node = alloc<Node>()`
followed by `return 0`, where the allocation is lost on the way out.

Rejected ownership-and-borrows, which is a large sema investment, and
garbage collection, which needs a runtime and conflicts with `@gpu`.

### Gyro's memory model is C's, and stays C's *(new)*

**Status: decided.** Gyro is and remains unmanaged: manual `alloc`/`free`,
addresses that never move, no collector. This is now a commitment, not an
accident of v1 — the goal it buys is full C compatibility: **any existing
C program, unmodified, can be imported directly into a Gyro program**
(`import { parse } from "./cJSON.c"`), a C struct and a Gyro struct can
be the same bytes in the same memory, and calls cross with no
marshalling.

Gyro already shares C's ownership and lifetime model literally: `gy_alloc`
wraps `calloc`, `free` is C's plus a header check, and a compiled binary
links a C runtime. What separated the two was not philosophy but two v1
shortcuts and one routing choice, and closing them is the roadmap:

1. ~~**A packed `@repr(C)` layout** beside the one-qword-per-scalar
   rule~~ — done, and not beside it: C's layout replaced it. See
   "Storage is C's layout, and there is only one" below.
2. **A wider FFI** — structs by value (done, same entry), callbacks and
   out-parameters (open).
3. **A direct C → METALS front end** emitting `ptr<T>`, so C shares Gyro's
   actual memory rather than a wasm sandbox, and the loader learns `.c`
   the way it knows `.lisp` — started, and running: `internal/cc` reads
   C, `import { add } from "./lib.c"` works on all three tiers, and a C
   function writes an out-parameter through a pointer Gyro allocated,
   which is the one thing a linked library could not do at all. The
   preprocessor, `typedef`, `enum`, `sizeof` and arrays with pointer
   arithmetic are in, so a C library with its own header, a quicksort
   over `int *` and a fixed table in module storage compiles and runs on
   all three tiers. Since then: string literals and file-scope
   initializers, `switch`, `static` locals, function pointers, a program
   made of as many `.c` files as it takes, `void *` with the casts that
   go with it, varargs, pointers to incomplete types — the way every
   library header hands out a handle — unions, bit-fields, and `goto`.
   "Any existing C program" is still the bar and is still a way off — the
   front end owes most of C11, VLAs, `_Generic`, and the relooper for the
   two `goto` shapes that need a control-flow graph, the piece
   `internal/wasm/clang.go` names as the hardest and currently
   outsources. See "The C front end emits types, the bridge emits
   bytes", "Pointer arithmetic is three instructions, and unchecked", "A
   callable is one word or two", "A C program is several files", "A
   `void *` is a pointer that forgot", "Varargs are an array a caller
   builds", "An incomplete type is a name with no shape", "A union is a
   shape", "A bit-field is bytes plus an offset", and "A goto is a branch
   out of a block".

   The IR rule that stood in the way is settled: a boundary is between
   runtimes and not between languages, so a `cc:` module and a `gyro:`
   one pass pointers outright. See "Values copy at runtime boundaries".

**Rejected: moving Gyro onto a traced heap** (the direction
`METALS_NEXT.md` §5 priced out). A collector solves the reclamation gap,
but the cost is permanent: a traced, potentially moving heap and raw C
addresses cannot be true of the same objects, so managed Gyro would leave
the C family and talk to C through handle tables forever after. It would
also delete the borrow and leak analyses' reason to exist — a meaningful
part of what Gyro is. If METALS ever grows a traced heap for managed
guests (Lua, Go), Gyro does not move onto it.

Consequence: the `memory reclamation` gap must be solved unmanaged —
arenas, a quarantining allocator, or stronger static analysis. The
answer is allocators as values, passed as explicit parameters; see
"Allocators are explicit parameters" below.

### Allocators are explicit parameters *(new)*

**Status: decided; `alloc`, `free` and `Arena` take one, the runtime's own
storage does not yet.** An allocator is an ordinary value of type
`Allocator` from `mem` — a procedure and a data pointer, Odin's shape —
and a function allocates exactly when it is handed one as a parameter.
`alloc<T>(a)` returns one zero-filled `T` from `a`, `free(a, p)` gives it
back, `heap()` is the process heap, and `Arena::new(a, capacity)` takes
its buffer from `a` and remembers it, so `Arena::free` returns the buffer
to the allocator it came from without being told which. Running out of
memory panics with the size. [Chapter 4.5](ch04-05-allocators.md) is the
specification.

**Why explicit.** The first design made the allocator a hidden trailing
parameter: the `alloc` effect decided which functions received it, and a
`with expr` statement swapped it for the rest of a block. The allocator a
call used could then not be read from the call. Code that reads correctly
could grow a buffer from one allocator and free it to another, because
nothing at either call said which one was in force. That is the failure
of JavaScript's `with`, and for the same reason: a name's meaning depends
on a binding established somewhere else, and a reader has to reconstruct
the scope to know what a line does. Gyro is meant to be verbose and easy
to read, and a parameter is the form of dependency that every reader
already knows how to follow. There is no effect for it either: whether a
function allocates is in its signature, as a parameter, where a row would
only have said it a second time.

**What went with it.** The `with` statement, the `alloc` effect and its
row entry, the `allocator()` intrinsic, the wrappers that captured the
current allocator into a function value, and the thunk that supplied the
heap to `&f`. A closure no longer carries an allocator it was never
handed, and `&f` on a function with an `Allocator` parameter is an
ordinary code pointer: the allocator is an argument the caller of the
pointer passes, like any other. `mem` is no longer loaded into every
program, since the lowering has no hidden parameter whose type it must
know. Interpolation counts as no effect.

**The leak analysis follows the heap.** A local bound to `alloc<T>(heap())`
is still tracked. A block from any other allocator is not: an arena
reclaims what it handed out, and so does an allocator the function was
passed, so the analysis cannot say the block is lost. Tracking every call
would have warned about the arena idiom, which was the reason `with`
silenced it in the first design.

**`offset` is an `unsafe` primitive.** An arena carves one block into many,
and Gyro has no pointer arithmetic on `*T`, so the arena is written over a
bodyless `offset` intrinsic that answers the address `bytes` past a base. It
started private to `mem`; it now lives in the `unsafe` module with the rest
of the raw memory (see "Unsafe code is marked on functions, and its
primitives are imported"), where `mem` imports it. Whether pointer
arithmetic belongs on `*T` is a separate question, and this decision does
not answer it. The arena's buffer is a block from its parent allocator rather
than an `Array<u8>`, since the parent has to be the one that supplied it.

**Rejected: the hidden parameter with `with`** (the design this replaces).
See above. **Rejected: an implicit context on every call** (Odin
verbatim), for the same reason, and it also changes the convention of every
function whether or not it allocates. **Rejected: `with expr { }` as a
block**: it narrows the scope and keeps the problem. **Rejected: four
`code` fields on the allocator** instead of one procedure and a mode. One
procedure is Odin's, wraps cleanly (a tracking allocator forwards one
call), and keeps the value two words.

**Not here.** Containers that remember the allocator they grew from are
the next entry. Closures that capture are issue #93, and formatting text
with `#{}` is issue #92.

### Containers remember their allocator *(new)*

**Status: decided; `Array<T>`, `Map<K, V>` and `String` do, as do the
functions that build text.** `Array<T>::new(a)` and
`Array<T>::with_capacity(a, n)` take an `Allocator` and record it in the
array's header. The header itself, the buffer, every growth of it and its
release go through the recorded allocator's procedure, with `Alloc`,
`Resize` and `Free`, so `push` and `free` take no allocator and cannot
disagree. `Map<K, V>::new(a)` and `String::new(a)` record theirs the same
way, in a field beside the arrays they are written over.

**Why record it.** An explicit allocator on `push` and `free` would repeat
the failure the previous entry removed: nothing at the call site checks
that the allocator a buffer grew from is the one it is freed to. Recording
it once, where the container is made, puts the choice in one place a
reader can see and makes the mismatch unwritable.

**The functions that build text take one.** `from_utf8`, `bytes`, `runes`,
`String::text`, `split`, `substring`, `trim`, `repeat`, `join`, `sprintf`
and the readers `env`, `read_stdin`, `readline`, `read_all` and `read_dir`
allocate a result the caller owns and has no container to record it in, so
the allocator is an argument, first after the receiver. The predicates
(`starts_with`, `ends_with`, `index_of`, `contains`) take none: they
compare the bytes in place through a private, non-copying `u8[]` view of
each string, `view_bytes` in `stdlib` (#109). A function allocates exactly
when it is handed an allocator, and a question about two strings has
nothing to build. The view is a private runtime intrinsic rather than an
exported function: it hands out a writable-typed slice over an immutable
string, which only a reader that never writes may hold. `bytes` and `runes` stop being part of the
type and become extensions of `string` in `stdlib`, imported like `trim`,
because the compiler's own surface has no way to name `Allocator` and an
ordinary declaration does.

**`Array<T>` stays in the runtime.** It could be Gyro, but when this was
decided a slice could not be built from a pointer and a length, so `slice()`
and indexing needed the runtime's handle. `slice_from_raw` (#110) removes
that obstacle; the array has not moved yet. What moves is the memory: `rt.c` calls the
recorded procedure where it used to call `malloc`, `realloc` and `free`.
The procedure takes its mode by address, because a Gyro enum crosses a
call like an aggregate.

**The header stays after `free`.** It is a block from the same allocator
and is not returned, so that a second `free` is caught instead of read
through a freed block. From `heap()` that is a leak of one header, as it
always was; in an arena it is reclaimed with the rest.

**What still uses the process heap.** The text of a `Format` piece that
is not a string, the message of an error the runtime reports, and a `to_string`, `hash` or
`message` the compiler calls on its own behalf while printing or hashing
(#92 and #3). `printf` and `eprintf` format through `heap()` because they
print what they build.

**The leak analysis does not follow containers.** An `Array` that comes
from `heap()` and is never freed does not warn, as before: the analysis
tracks `alloc<T>(heap())` and nothing else, and widening it to every
container would warn about each array a function returns. This is
conservative, and may be revisited once the library hands out fewer
long-lived containers.

**Rejected: an allocator argument on `push` and `free`** (Zig's managed
containers, unmanaged). It is what #91 removed. **Rejected: moving
`Array<T>` into Gyro** for now; the slice reason no longer holds.
**Rejected: allocating the header from the process heap** so that `free`
could release it, since the container would then use two allocators.

### A closure's environment lives in its frame, and keep copies it out *(new)*

**Status: decided, done.** A closure that captures something stores its
environment, a record of the captured values, in the stack frame of the
function that wrote it, like a local struct. Writing a closure allocates
nothing. The `fn` value stays two words, the code and a pointer to the
record; a closure that captures nothing, and a named function taken as a
value, have a null pointer. Every record begins with a header word holding
its size in bytes, so `keep(a, f)` in `mem` can copy it without knowing its
static type: it allocates that many bytes from `a`, copies, and answers a
value with the same code and the new record. `release(a, f)` gives the copy
back, and both do nothing for a null environment. They are ordinary Gyro
over two intrinsics, `env_of` and `with_env`, that read a `fn` value's
environment word and rebuild one around another. They began private to
`mem`; they are exported from the `unsafe` module now, where the raw
memory surface was designed ("Unsafe code is marked on functions, and its
primitives are imported"), and `keep` and `release` are `safe fn`s over them. `F` must be a function type, which
sema checks with a diagnostic. `spawn` takes an allocator, keeps the
closure it is given, and the thread releases it when the closure returns;
it moved to `mem` for the same import-cycle reason: `mem` already imports
`stdlib`, and `spawn` needs `Allocator`.

The closure then borrows the frame exactly as a slice of an array literal
does, so the existing provenance analysis rejects returning one and `keep`
is what ends the borrow. Objective-C blocks are the precedent: a block
literal is on the stack and `Block_copy` moves it to the heap.

**The gap it inherited, now closed.** A frame closure stored in a struct
field or pushed into a container was the problem of a view stored somewhere
that outlives its storage, and md4c hit it: a function returned a struct of
closures and crashed until each was wrapped in `keep`. "Provenance reaches
aggregates and stores" below closes it, for closures and slices together.

**Rejected: heap environments that nothing frees.** It is the hidden
allocation explicit allocators exist to remove, and a long-running program
leaks one record per closure written. **Rejected: Zig-style closures that
cannot capture.** It loses `map`, `filter` and `fold` with a captured
value, which is most of what closures are used for. **Rejected: a literal
syntax for allocating closures.** `spawn` receives a value, so a function
that copies a closure is needed anyway, and a second way to say the same
thing is worse than one. **Not here:** futures and channels keep using the
process heap (#51).

### Unsafe code is marked on functions, and its primitives are imported *(new)*

**Status: decided, done.** A function is `unsafe fn`, `safe fn` or a plain
`fn`. An `unsafe fn` is unsafe to call: it may only be called from another
`unsafe fn` or from a `safe fn`, and its body may use unchecked operations.
A `safe fn` may do the same in its body, and anyone may call it: the author
vouches. A plain `fn` may do neither, and the error suggests `safe fn` (vouch)
or `unsafe fn` (propagate). The unchecked operations are calls to unsafe
functions and reading a union member. `unsafe` and `safe` are contextual
words before `fn`, like `move` and `copy`, and stay ordinary identifiers.

The primitives are `unsafe fn` exports of a bundled `unsafe` module: `cast`,
`ptr_from_int`, `ptr_to_int`, `offset`, `copy_bytes`, the three `raw_`
heap functions, and `env_of` and `with_env`. Gyro has no universe scope, so
they had to come from some module, and naming it `unsafe` makes every file
that uses them findable from its imports. `mem` imports them and its
functions that use them are `safe fn`s, which keeps the import graph acyclic:
`unsafe` imports nothing.

`p as *T` on an `anyptr` is removed and `cast<T>(p)` replaces it. An
function imported from C, from a `.c` file or from a header, is unsafe to
call unless the import is written `import safe { ... }`. Vouching is for C, so
`import safe` of a Gyro module is an error. The vouch is a property of the
symbol, so a name re-exported from a vouching import stays vouched.
Types a C file declares need no vouching.

**Closures and methods.** A method is marked like a function. A closure
literal is part of the function it is written in and has that function's
freedom, since the author of a `safe fn` vouches for its whole body; a
closure is never unsafe to call, because a `fn` value carries no marker.
`&f` of an unsafe function needs an unsafe context, or the pointer would be
a way round the call.

**Borrow provenance.** `ptr_from_int`'s result has no origin the analysis can
see, which is how a call with no borrow in its row already reads, the same as
the result of a C function.

**Rejected: Rust-style `unsafe { }` blocks.** A keyword-scoped second dialect
inside a function, where a reader has to track which statements are inside.
Function granularity was the choice: the step that needs vouching for is
usually small enough to be a function of its own, and the function's name and
signature are where its contract gets stated.

**Rejected: an `unsafe` effect in rows.** An effect propagates to every caller
and has no way to stop, so a safe wrapper around an unsafe primitive would be
impossible, and the first useful one would give the language up.

**Rejected: a module-only marker.** It marks a whole file and cannot tell
"dangerous to call" from "uses danger inside", which are the two things a
reader needs to know apart.

**Rejected: one `unsafe fn` marker for both meanings.** Rust conflated them and
spent years splitting them, with `unsafe_op_in_unsafe_fn` and `unsafe extern`.
`unsafe fn` and `safe fn` are the two claims written separately from the
start.

**Not here.** Pointer arithmetic on a `*T` (#68) is open and would live in
`unsafe`. Removing `union` declarations from Gyro is #107, and header imports
replacing the old `@extern` are #108. Privileged instructions (#97), volatile
access (#99) and raw system calls (#103) add primitives to the module.

**Added beyond the first list:** `ptr_to_int`, because a program has no
other way to get an address to give `ptr_from_int`, and a test of the second
needs the first. METALS has no instruction between an integer and a pointer,
so both are runtime functions.

**`slice_from_raw` (#110)** makes a `T[]` from an address and a length, for
memory that comes from outside a Gyro value: a mapped buffer, an SDL surface.
Lowering answers it with a new METALS instruction, `slice.from`, over a
`ptr<T>` and a `usize`; it is not a runtime function, since a slice is a
pointer and a length and nothing needs the runtime to say so. Its result has
no origin, like `ptr_from_int`'s. **Rejected: widening `slice.of` to take a
length**, because its array operand carries a static extent and one
instruction would then have two meanings for the second word. **Rejected: a
checked form**, since nothing about a raw address says how much lives there.

### Storage is C's layout, and there is only one *(new)*

**Status: decided.** A value in memory occupies what `sizeof` would say
in C, at C's alignment, padded where C pads. A `u8` is a byte, an `i32`
is four, a struct of `{ u8; u32 }` is eight with the `u32` at four, and
`array<u8, 3>` is three bytes. A METALS *register* still carries a scalar
widened to a qword, and an `f32` as the double it widens to; the move
between the two representations widens on a load and narrows on a store.
[Appendix E](appendix-e-native-backend.md) has the table and the rule.

This replaces v1's "every scalar occupies one qword, nothing is packed",
which was a shortcut, and it is what makes the commitment above real: a
Gyro struct and a C struct of the same fields are now the same bytes, so
a struct crosses to C by value with nothing converted, and a `u32[]`
*is* a `const uint32_t *` — the repacking copy `ffi.pack` used to make is
gone, and so is `ffi.pack`.

**Rejected: `@repr(C)` as an opt-in layout beside the default**, which is
what this roadmap originally said. Two layouts means `ptr<i32>` has two
meanings — a four-byte cell inside a packed struct, an eight-byte cell
everywhere else — and the address of a field is not a corner case: `p.x`
on any addressable struct lowers to `field.ptr` and a `load`. The way out
would have been a second pointer type or a ban on taking a field's
address, and both are worse than having one layout. The decorator then
has nothing left to say, so there is no `@repr(C)`: every struct is one.

**Rejected: making a register narrow too**, which would have removed the
two representations entirely. C's integer promotions would then decide
the width of every arithmetic operation in the generated C, where signed
overflow is undefined rather than checked, and the assembler backend
would need a typed load at every operand read instead of at every
member access. The register model is not the data model, and this is the
line between them.

**Forced by the split, and worth stating:** what the runtime stores it
stores in storage representation — a channel's elements, an `Array`'s, a
future's result, an `any`'s payload. `Array<T>::slice` borrows the
Array's own elements, so the two have to agree on stride, and after that
one rule is the only coherent one. A narrow scalar therefore reaches the
runtime through a staging cell rather than as the address of its
register.

The layout arithmetic lives in `metals.Layout`, one implementation for
both backends, and the C backend asserts every answer against `cc`'s own
with a `_Static_assert` on each type's size and each member's offset. A
size assert alone would pass on two structs that pad differently and
total the same.

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

A slice outliving its backing memory is now caught at a return; see
"Borrows are named in the effect row" below. An array literal `[1, 2, 3]` is
a fixed-size array in the enclosing scope, so a slice of it borrows the frame
and cannot be returned.

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

### Traits *(revised — the big one, twice)*

**Status: done.** A trait lists method signatures, some with a default
body, and may require other traits and take type parameters. A type
implements it in a Rust-style block:

```gyro
trait Element {
  fn paint(self)
  fn describe(self): string { ... }
}

impl Element for Button {
  fn paint(self) { ... }
}
```

and a generic states what it needs: `fn bigger<T: Ord>(a: T, b: T): T`,
bounds joined with `&`. The body may use exactly what the bounds provide, so
a call that cannot work is reported at the call, naming the bound.

**History.** The first round designed nominal traits. The review on
2026-07-31 deleted the mechanism and made duck typing the one contract rule,
on the argument that Rust's "having the method isn't enough, you must declare
`impl`" bought protection from accidental satisfaction at the price of a
whole declaration layer, and that exact-signature collisions were rare
enough not to pay it. On 2026-10-10 (issue #111) the mechanism came back,
because a GUI's element tree needs what duck typing cannot give:

- **Values of different types behind one set of methods.** A list that holds
  buttons and labels and calls the right `paint` on each needs a promise the
  program can name, and a method table behind it. This is the reason; the rest
  follow from reversing the decision once it was needed.
- **Opting in on purpose.** `impl Element for Button` is a statement by the
  author, so nothing fits by accident.
- **A bad call is reported at the call.** With no bounds a generic is checked
  at each instantiation, and the error was reported inside the generic's body
  with a note at the call. The requirement was in no signature, and a reader had
  to read the body to know what it asked for.
- **Default bodies.** A method every implementer shares is written once.

A consequence worth recording: the compiler's own capabilities — `==`,
printing, hashing, ordering, the operators and `for..in` — were each
special-cased in the checker and the lowering. As traits they are one
mechanism, and `Map<K: Eq & Hash, V>` says what a key is.

**Rejected.** Go-style structural interfaces: a type satisfying one by
having the methods is exactly the accidental opt-in this reverses, and it is
what the duck typing was. Optional bounds — a generic that states some and
infers the rest: there should be one way to write a generic, and an
inferred requirement is back to errors in the wrong place. A type's own
methods inside an `impl Type { }` block: unchanged from "Methods are
free-standing", since an `impl` block here is how a type keeps a promise, not
a place for its own methods.

**Capability traits are explicit.** `Eq`, `Hash`, `Print`, `Ord` and the
operator traits are declared in the bundled `stdlib`, and a type the program
declares has one only by an `impl`. `impl Eq for Point` with no block takes
the implementation the compiler builds from the fields; a block replaces it.
The built-in types implement the ones that apply to them without any impl.
Rejected: deriving `Eq`, `Hash` and `Print` automatically for every
struct, as before. It made `==` on a struct that was never meant to compare
compile, and a `Point` with a pointer field print an address nobody asked
for; Rust's `#[derive]` made the opposite choice for the same reason. The
cost is one `impl` line per type that wants them, which is a line of intent.
The compiler knows these traits by where they are declared (the bundled
module), not by a name that universe scope would have to hold: they are
imported like anything else.

Things are intentionally small: a trait takes type parameters but has no
associated types, the operator traits are `Self -> Self -> Self`, impls are
for types the module declares, and an impl cannot ask more of a type's
parameters than the type does ([appendix G](appendix-g-known-gaps.md)).

**Settled while building it.**

- `trait` and `impl` are keywords; `dyn` is contextual, a trait value only
  where a name follows it in a type, so it stays usable as an identifier. `Self`
  is an ordinary name, bound inside a trait, an impl and any method.
- An impl's methods are ordinary methods of the type, in its one namespace: a
  trait method that shares a name with an own method, or with another trait's,
  is the existing duplicate-method error. There is no `Trait::method` call
  syntax to tell them apart, which is the price of a method being a method.
- An impl is for a struct, enum or union that **the same module declares**,
  and a generic type is named by its own parameters, `impl Eq for Pair<T>`,
  with no `impl<T>` list: the parameters and their bounds are the type's.
  Conditional impls and impls for built-in types are not built.
- **A type from a C file keeps the structural answer.** Its layout is someone
  else's, there is no module of ours to write an impl in, and printing or
  comparing a `SDL_Rect` worked before and keeps working. So do the types the
  bundled library declares.
- **`Print` is a hook, not a call.** A body bounded by `Print` cannot call
  `to_string` on the parameter, because the built-in types have no `to_string`
  to call: a number's text is made by the runtime, with nowhere to be
  allocated from.
- `a.eq(b)`, `a.add(b)` and the other methods of the capability traits exist
  on the built-in types, so a generic body that calls them works for the
  numbers as it does for a declared type.
- `Index<T>` is `at(*self, index: usize): *T`, the address of element `i`,
  because "An index is a place, on every container": `value[i]` lowers through
  it as `Array<T>` lowers through its own `at`.
- Default bodies need a trait without type parameters, and a trait value needs
  one too; both are what the tables and the monomorphization carry without
  substituting the trait's arguments in lowering.
- The bodies an impl brings are checked again for each concrete instance of
  a generic type, between declarations, because an operator or a method table
  reaches them with no call to name them.

### Trait values are a pointer and a table, and they borrow *(new)*

**Status: done.** `dyn Trait` is two words: a pointer to the value, and a
pointer to the table of the methods of its type for that trait. The table is
one global per type and trait in the program, written by the module's
initializer, and each entry is a thunk that puts the type back on the
address. A trait value is made from a pointer with `as`:
`&button as dyn Element`.

A trait value is a **view of its value**, the way a slice is a view of its
array: nothing is allocated to make one, and the borrow analysis follows it
with the slice's rules (a function returning one names where it came from,
and a view of this frame's storage cannot be returned).

**Rejected: inline storage with a cap.** The `error` mechanism, a buffer
inside the value. It would make `Array<dyn Element>` a fixed-size-element
array of whatever the biggest implementer is, reject types by their size,
and copy the value so that a `*self` method mutated a copy. **Rejected:
boxing into an allocator.** The value is allocated when the trait value is
made and nobody knows who frees it — hidden ownership, against explicit
allocation. **Rejected: an implicit conversion from a pointer.** Gyro has
none between typed values, and the cast keeps the borrow visible where it
begins.

Only methods are callable through a table: a trait with a generic method, a
method that mentions `Self` other than as its receiver, or type parameters of
its own is not a trait value, and the error names the method. The capability
traits are answered by the type at compile time and are not trait values
either.

### Operators dispatch to traits *(revised)*

**Status: done.** On a type the program declares, `+ - * / %` are `Add`,
`Sub`, `Mul`, `Div` and `Rem`, unary `-` is `Neg`, `== !=` are `Eq`, and
`< <= > >=` are `Ord`, which calls `compare` and tests the answer against
zero; `value[i]` is `Index`. The numbers have them built in, and nothing about
their overflow or NaN rules changes. Wrapping arithmetic, the bitwise operators
and shifts are not traits.

Replaces "Operators are not overloadable", which kept arithmetic on a user
type as a visible call (`a.add(b)`) on Go's reasoning. The calls are still
visible in the type's impl; the operator is the sugar Rust gives, and the
cost the review feared, hidden calls behind operators, is bounded by the
operators being the ones whose traits are written down. C++-style
well-known method names unlocking operators (a `compare` enabling `<`
without saying so) stays rejected, for the same reason as duck typing.

The operator traits take and give `Self`. A vector scaled by a number is a
method. Rust's `Add<Rhs>` with its `Output` would need trait parameters
with defaults and associated types.

### Methods are free-standing### Methods are free-standing `fn Type::name(self)` *(revised)*

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
for a type's own methods (a grouping construct that exists only to hold what
free functions can say alone). An `impl Trait for Type` block exists, and
does something free functions cannot: it says a type keeps a promise. Its
methods are ordinary methods of the type afterwards.

### Generics state their bounds; user types may be generic *(revised)*

**Status: done.** `fn max<T: Ord>(a: T, b: T): T`. A type parameter's bounds
are the traits its argument has to implement, joined with `&`, and the body
may use exactly what they provide. A parameter with no bounds can be stored,
passed and returned, and nothing else. `struct Pair<T>` and `fn
Pair<T>::swap(*self)` are legal with no bounds, and a type may bound its
parameters where it introduces them, `struct Map<K: Eq & Hash, V>`, which its
methods inherit. Instantiation stamps out concrete code per type argument.

The previous rule was the opposite: `fn max<T>` declared no bounds, and the
body's method calls were the requirement, checked at instantiation, C++-template
style. It is rejected for the reasons under "Traits". Every generic stating
its bounds also means the signature is the whole interface, which is what a
caller reads.

Bare angle brackets stay: `max<i32>(3, 7)`. Rejected turbofish. Cost: the
parser needs bounded backtracking to tell `f<T>(x)` from `a < b`, resolved
by a speculative parse that commits only when the brackets close and a call
follows. Type arguments live on path segments, since `Array<i32>::new`
parameterizes a non-final segment.

### Gyro does not declare unions *(new)*

**Status: decided.** There is no `union` declaration in Gyro, and `union`
is an ordinary identifier. A C `union` still arrives through an `import`
of a `.c` file, as the type C declared: every member at offset zero, the
size the widest member's, nothing recording which is live. Values of it
can be held, passed, built with a literal that names one member, and have
members written and read. It keeps its own `types.Union`, so equality,
hashing, printing and `any` refuse it by not having a case, exactly as
before.

**The principle: METALS supports everything C needs, and Gyro's surface
only gets a feature when Gyro code has to write it.** A union was added to
the surface so that a C union could be imported as a type, but the
import never needed Gyro to be able to *declare* one; it needed Gyro to be
able to *name* one. METALS has the shape ("A union is a shape, not a
record whose fields coincide"), and the loader's generated header, which
is the only text that declares an imported type, is the one place the
parser still reads the word. A Gyro program has no use for an untagged
union of its own: its sum type is the tagged `enum`, which knows its
variant, prints, compares, and cannot be read wrong. The `SDL_Event` out
parameter that the old chapter used as the reason to have one is a struct
with the same size and alignment, because Gyro only ever needs the bytes
and the tag.

**Rejected: keeping the declaration because C has it.** C having a
feature is a reason for METALS and the C front end to have it, not for the
language a Gyro programmer writes. The declaration came with a checker for
what may be a member, a rule about literals and a chapter of caveats, all
of it guarding a construct whose only user was the import.

**What went with it.** The `@extern union`, empty union, duplicate member
and "cannot be a union member" diagnostics, which only ever described a
Gyro-written declaration. The members of an imported union are C's types
and need no such check.

**Reading a member needs an unsafe context.** Nothing records which member
is live, so a read is the program's claim that the bytes are this member's,
which is the kind of claim `unsafe` marks ("Unsafe code is marked on
functions, and its primitives are imported"). Writing a member does not: it
says nothing about what the others now mean, and neither does a literal. A
store through a member that is storage in the union (`v.bytes[3] = 1`,
`outer.inner.i = 4`) is a write too; a compound assignment reads first, and
a member holding a pointer is read to be written through.

The fixed-size array member that now has a spelling in a generated header
stays: `struct { char name[16]; }` crosses when the element spells as one
word, since Gyro reads `*u8[8]` as a pointer to an array.

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

**Status: done.**

- A match over an enum must cover every variant or carry `_`; adding a
  variant later breaks every non-`_` match loudly, which is the point.
  A variant counts as covered only when the arm's payload patterns bind
  rather than test, so `Shape::Circle(0.0)` leaves `Circle` open.
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

### Iteration goes through `Iterator` *(revised)*

**Status: done.** `for x in ...` handles slices, arrays, ranges and
channels natively; any other type qualifies by implementing
`Iterator<T>`, whose one method is `next(*self): (T, bool)` and where
`false` ends the loop. Rejected a closed built-in set: user containers could
never be iterated.

The previous rule took any type with a `next` method, or an `iter()` that
returned one. The `iter()` form is dropped: with no associated types a trait
has no way to name what `iter` returns, and a collection that hands out an
iterator is one `fn Bag::iter(self): Walk` away from `for x in bag.iter()`.

### Printing is derived, opt-in, and overridable *(revised)*

**Status: done.** `println` and `#{}` take a value that implements
`Print`. The built-in types do; a struct or enum the program declares says so
with `impl Print for Point`, and gets a compiler-derived representation
(`Point{ x: 1, y: 2 }`), or supplies `to_string(): string` in a block to
replace it, which a derived representation that reaches the type also uses.
Primitives print natively.

Rejected primitives-only printing (debugging convenience matters more than
avoiding generated code) — the derived form is compiler-emitted, accepted as
the one place code generation is invisible. Rejected printing every type
whether or not it asked: a struct that holds something that should never be
shown printed it anyway, and the opt-in is the line of intent that says
the representation is fine to expose.

`to_string` on a type parameter is not callable. A number's text is made by
the runtime and has nowhere to be allocated from, so a body bounded by
`Print` hands the value to `println`.

### Interpolation is a format value, streamed or formatted into an allocator *(new)*

**Status: decided, done.** A string literal with a `#{}` in it is not a
`string`. It is a `Format`, from `mem`: a record of the literal pieces and
the values in them, built in the caller's frame, allocating nothing.
`print`, `println` and `eprintln` stream the pieces one after another, and
`format(allocator, value)` from `mem` builds them into the allocator it is
given and returns a `string`. A plain literal with no `#{}` stays a
`string`. A function may take a `Format` and pass it on, which is how a
logging helper forwards its message to `println`.

Following Rust's `format_args!`, a format value borrows what it names, so
it cannot outlive its statement. The rule that enforces it is the simplest
coherent one: a `Format` may only appear as a call argument. Binding,
returning, storing, sending, capturing and wrapping it into an `any` are
errors, and a `Format` where a `string` is wanted is a type error whose help
names `format`.

This is what explicit allocators needed. With an expression unable to
allocate without naming an allocator, `"#{i}. #{title}"` could no longer
quietly build a heap string nothing frees.

Rejected interpolation allocating a heap string implicitly: it is the hidden
allocation that every other allocating call has just been made to name, and
nothing frees the result. Rejected allowing interpolation only as
`println`'s argument with no type: a logging helper could not forward its
message, and every function that wanted a formatted line would have to
print it itself. Rejected dropping `#{}` for printf-style formatting: the
readable form is the one every example uses, and `fmt.sprintf` already
exists for the case where a format string is data.

`panic` keeps taking a `string`; a message with a hole in it is written
`panic(format(heap(), "..."))`. `format` lives in `mem` rather than
`stdlib`, since it takes an `Allocator` and `mem` already imports `stdlib`.

---

## Runtime behaviour

### Programmer errors abort

**Status: planned.** Out-of-bounds, divide by zero and null dereference
print a message with source position and terminate. Not returnable, not
catchable.

Keeps the `(T, error?)` channel meaningful — it carries conditions, not
bugs — and needs no unwinding machinery.

### Float division by zero is IEEE; NaN is unordered *(new)*

**Status: decided, done.** `1.0 / 0.0` is `+Inf`, `0.0 / 0.0` is `NaN`, and
neither panics. Integer division by zero still aborts. The split is the
point: IEEE 754 defines an answer for the float case, so it is a value, not
a bug, while integer division by zero has no answer to give.

This closed a three-against-one divergence rather than creating one. The
interpreter was the only tier that panicked; `fdiv` does not trap, so the
evaluator and both native backends already produced Inf and NaN. Making
them all panic would have meant emitting a zero test before every float
division in two backends, paid on every program, to reject a defined
operation.

NaN then had to be settled twice, because two orderings are wanted and they
disagree:

- The **operators** are IEEE. `<`, `<=`, `>`, `>=` are all false when
  either side is NaN, and `NaN == NaN` is false while `NaN != NaN` is true.
  The interpreter used to route `<=` and `>=` through its three-way
  compare, which answers 0 for a pair it cannot separate and so read
  "unordered" as "equal"; the METALS evaluator had the same bug. Both now
  compare directly.
- **`compare()` is a total order.** NaN sorts above every number and equals
  another NaN; `0.0` and `-0.0` compare equal. A three-way comparator that
  answered 0 for every NaN pair would be intransitive, and a sort built on
  it could order nothing correctly once a NaN was in the data. The
  synthesized compare in lowering carries the two `x != x` tests for float
  types only, so nothing else pays for them.

Rejected making `compare` IEEE-partial and returning some fourth
"unordered" answer: it would spread through every caller of a comparator to
handle a case that a total order simply defines away.

### Overflow panics; wrapping is explicit

**Status: done (operators), planned (the check).** `a + b` panics on
overflow; `a +% b`, `a -% b` and `a *% b` wrap deliberately. Narrowing `as`
conversions panic on lost value the same way.

Rejected Rust's debug-checked/release-wrapping split, where one expression
means different things in different build modes, and rejected always-wrap,
which optimizes for the rare intentional case.

### The process interface is a bundled `os` module *(new)*

**Status: decided.** Environment variables, stdin and the exit code live
in a module named `os`, as three intrinsics:

```gyro
export fn env(a: Allocator, name: string): string?
export fn read_stdin(a: Allocator): string
export fn exit(code: i32)
```

`env` returns null for an unset variable — absence is a nullable, not an
empty string, since an empty value is a value. `read_stdin` reads to end
of file in one shot; a second call finds the stream drained and returns
`""`. A line-buffered reader wants a design of its own (buffering state,
an EOF signal) and can arrive later without disturbing this surface.

`exit` is an ordinary `fn(i32)`. Gyro has no noreturn concept and this
does not introduce one: code after `exit` type-checks like code after any
other call, it just never runs. It terminates like a panic does — output
flushed, deferred calls not run — but silently and with the caller's
code. The evaluator runs in-process, so it models exit as a control-flow
signal handed back to the driver (`meval.Exit`) rather than calling
`os.Exit` under a test runner; a native binary and `gyro run` genuinely
exit with the code.

Rejected: `main` returning an exit code — `fn main()` /
`fn main(args: string[])` is settled, and threading a return through
every early exit is what `exit` is for. Rejected for now: `set_env`;
mutating process-global state buys little until something reads it back,
and spelling only the read keeps the module honest. Rejected: divergence
typing for `exit`; one library function does not justify a type-system
feature.

The test harness grew the matching directives: `//gyro:stdin <line>`
feeds a line of stdin to every tier, `//gyro:exit <code>` asserts the
process exit code across tiers (a panic case still exits 1), and the
corpus runs with `GYRO_TEST_ENV=corpus` pinned so `env` has a
deterministic value to observe.

### `os::readline` is the line reader *(new)*

**Status: decided.** The line-buffered reader the entry above deferred is
one more intrinsic beside `read_stdin`:

```gyro
export fn readline(): string? !{ io }
```

It answers the next line without its terminator, and null at end of file.
An empty line is `""` — absence is the null, the same rule `env` follows,
so a program that reads a blank line sees a blank line rather than a
stream that ended. A final line with no newline still comes back, because
a file that does not end in one still has that line in it.

Both readers work the stream, not a private buffer: `readline` takes a
line and `read_stdin` drains what is left, in either order. That is what
makes them one interface rather than two, and it is why there is no
handle, no `Reader` and no buffering state in the signature — the state
is the process's own stdin, which is already shared.

**This is what makes an interactive program possible at all.** Prompting
needs a read that returns before end of file, and `read_stdin` cannot:
`tests/testdata/run/rpn/` is a REPL and could not have been written without
this.

Rejected: `(string, bool)`, the shape `Array<T>::pop` uses. The flag is
there because a popped `T` may itself be nullable and `T??` is not a
type; a line is a `string` and cannot be, so the nullable says the same
thing with one value instead of two. Rejected: keeping the terminator, so
that `""` could mean end of file — that makes every caller strip it and
turns a blank line into a special case. Rejected for now: a `Reader`
abstraction over files, sockets and stdin; that waits on the interface
question (#3), and stdin is the one stream a program
always has.

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

### Which ready `select` arm runs is unspecified *(new)*

**Status: decided.** When several arms are ready at the same moment, the
one that runs is not defined by the language. A program whose output
depends on the choice is wrong.

This settles what was previously recorded as a tier divergence: the METALS
evaluator picks at random (it is Go's `reflect.Select`) and the native
runtime polls arms in source order. Both are now conforming. Specifying
random would put a PRNG in the definition and force the native backends
into a runtime-chosen dispatch before there is a reason; specifying source
order would promise something the language would have to keep forever, and
would bless starving a slow arm behind a busy one.

Leaving it unspecified is not the same as being happy with in-order
polling. Starvation is a runtime quality problem, and the fix — arms
emitted as a descriptor table the runtime chooses from, rather than
straight-line polls — is queued work, not a language change.

### Data races: the direct case warns *(new)*

**Status: done, partially.** A closure handed to `spawn` that captures a `*T`
or a `T[]` shares it with the thread that spawned it, because captures copy
and copying a reference copies the reference. That warns.

Full Send/Sync-style checking (Rust) stays rejected: it is a mechanism the
language chose not to afford, and it would need a trait system that was
removed on purpose.

Channels and `move` captures are exempt. A channel is the sanctioned way to
reach another thread, and a move hands the binding over rather than sharing
it, which is exactly the distinction the capture list exists to draw.

What the pass does not see: a pointer reached through a struct field or a
returned value, two spawns over one channel, anything a captured function's
callee does, and a `Mutex` or `Array` captured by copy — those are structs,
and telling the one that is meant to be shared from the one that is not
needs more than the type. Warning on the direct case and saying so beats
claiming a guarantee that does not hold.

The choice between reporting at the `spawn` call and at the capture: the
call, because that is where the decision to cross a thread boundary is
made, and a closure is only a race once something spawns it.

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

### A struct of function values is the callback shape *(new)*

**Status: decided — confirmed by porting, no compiler work needed.** A C
library that drives its caller through a table of function pointers ports
to a struct whose fields have function types, and the closures stored in
it capture what C threads through a `void* userdata` parameter. md4c's
`MD_PARSER` — five callbacks plus a `userdata` argument on every one —
became five function-valued fields on `md::Parser`, built by
`html::parser_for(render: *Render)`, whose closures all capture the same
`*Render`. The `userdata` parameter is simply gone from every signature.

Two existing decisions are what make it work, and both are worth stating
together because the combination is not obvious: a closure captures **by
copy**, and copying a `*T` copies the pointer, so five separate closures
mutate one renderer; and `Array<T>` is a runtime handle, so a captured
output buffer is shared without a pointer at all. It parsed, checked,
lowered, verified and ran identically on all four tiers at the first
attempt, over 652 CommonMark examples.

The limit is the recorded effects gap, not the mechanism: calling a
function value contributes nothing to the caller's row, so a
callback-driven parser is rowless and unconstrained until effect
polymorphism exists.

---

## Effects

### Borrows are named in the effect row *(new)*

**Status: done.** An entry written `&xs` says the result is a view into that
parameter. Always written out, no elision: a row with no `&` says the result
views nothing. The row is read at call sites as well as where it is written,
so what a call views reaches its caller and crosses module boundaries.

This reverses the rejection of ownership-and-borrows recorded under Manual
allocation. What was rejected there was *inference* — a borrow checker that
works out lifetimes from the body, which is the large sema investment. What
landed here is the opposite: the signature states the relationship and the
body is checked against it. The statement is the cheap part, and it is the
part that makes the fact usable across a call.

The spelling went through three rounds. `borrows xs` was too long for
something that belongs on most signatures returning a view, and `borrows a,
b` was ambiguous with effect names in a comma-separated row. `&xs` is short,
marks each source individually so the ambiguity disappears, and reads the
same way `&` reads everywhere else: a reference to the thing named.

Rejected putting borrows on the return type (`i32[]&xs`), which reads well
and is arguably more accurate — a borrow is a property of the type, not of
what the function does — but splits signature facts across two places.
Rejected Rust-style named lifetimes, which are more expressive and would add
a second kind of generic parameter to a language with no trait bounds.
Rejected eliding the single-candidate case, which is what makes Rust's
lifetimes bearable but conflicts with "Nothing is implicit."

Consequence worth stating: since a slice never owns, a function returning
`T[]` needs a `&` entry in practice. Handing ownership back means returning
`Array<T>`. That is not a new restriction, only a visible one.

A generic row is **conditional**: `&a` where `a: T` is load-bearing when `T`
is a view and vacuous when it is not, checked per instantiation and stated
once. Whether a source or a result is a view is therefore not judged on a
generic declaration — being vacuous at one instantiation is the arrangement,
not a mistake.

The cost is a row on every generic pass-through, including where it never
means anything: `fn identity<T>(x: T): T !{ &x }`. Rejected inferring generic
rows from the body, which would make the one place the annotation is hardest
to read also the one place it is invisible. Rejected leaving generics exempt,
which leaves generic containers — where views actually flow — unchecked.

`&self` names a method's receiver. The receiver is not a scope symbol, so it
gets a synthetic one; the alternative of making receivers implicitly borrow
was rejected because it would leave one borrow unstated and make a lending
method indistinguishable from an owning one at a glance.

A field chain roots at whatever holds it, which is what makes `&self` usable
at all: `self.items.slice()` is a view of the receiver, not of `items`. The
same rooting closes the struct-field case for named parameters. A struct
literal carries the origins of its fields, so frame storage behind a field is
still the frame's while a heap buffer is not — that distinction is why local
structs could not simply be treated as frame storage.

Reading the row at call sites is what makes it a real check rather than a
local one, and it is the reason the annotation earns its cost: the fact a
signature states is usable by everyone who calls it, including across a
module, without anyone re-deriving it from a body. It is also the machinery
the concrete effect uses — `io` is checked by the same "read
the callee's row, apply it here" step.

Frame storage propagates through a call: passing a local where the callee
borrows makes the result view the caller's frame. That falls out of mapping
`&param` onto the argument rather than being a separate rule.

### Provenance reaches aggregates and stores *(new)*

**Status: done.** Provenance used to recognise a slice, a string or an
optional of those as a returned view, so `fn f(xs: i32[]): any { return xs as
any }` was accepted, as was a struct of closures, and a view pushed into a
container went wherever the container went. Now a view is anything whose type
can hold one: a slice, a string, a closure, an `any`, and any struct, tuple,
enum, optional or container with one inside, through its fields and its type
arguments. The result of a function holding a view takes a `&` row exactly as
a slice result does, and a `!{ &xs }` row on it is legal and required. A
number read out of an aggregate carries nothing, and only a field whose
declared type can hold a view carries a view.

**Stores are judged where they happen.** A method taking `*self` is treated as
possibly keeping its arguments (`push`, `set`, `insert`, and a method a
program writes), and so is an assignment to a field, an element or through a
pointer. The target's root variable takes on the origin of what it was given,
so returning it later is checked like returning the view. If the root is a
parameter, the receiver or a module-level variable, it outlives the frame,
and frame storage or a closure environment going into it is an error at the
store (`E0630`) with `keep` as the help for a closure.

**A loop body is one place.** A closure environment or an array literal has
one slot in the frame, which a loop rewrites every iteration. Storing a view
of one into a variable declared outside the loop is rejected (`E0632`) even
when the container never leaves the function, because every element would
read the last iteration. This is a depth comparison, the loop depth where the
storage was written against the loop depth where the target was declared; it
needs no fixpoint and does not change what the flow analyses do.

**Rejected: a store of a view of a parameter into another parameter.** The row
describes a function's result, so there is no way to say that `out` now views
`xs`, and rejecting it would make every parser that pushes slices of its
input into a caller's token array a false positive. It is allowed and listed
in appendix G. **Rejected: following a view through a callee.** A call
`register(&hs, f)` stores in `register`, where `f` is an ordinary parameter;
following it needs rows on parameters, which is a different feature.
**Rejected: treating every `*self` method as a store only when its parameter
type can hold a view.** The receiver's type is what decides, not the
parameter's, because a container instantiated at a view type has `T` where
the parameter is declared. **Rejected: a separate check for aggregates beside
the slice one.** One origin walk now serves returns, stores and rows, so a new
view type (a trait object, when it lands) is added in one predicate.

The cost is rows in places that had none: `Format::piece` returns an `any`
over its receiver and says `!{ &self }`, and `fn same(p: Span): Span` needs
`!{ &p }`. `split` on an empty separator used to return the receiver itself in
the array, which the new check found: it copies now.

### An allocator is a borrow source *(new)*

**Status: done.** A value allocated from `arena.allocator()` borrows the arena
the allocator came from. `Arena::allocator` states `!{ &self }`, and the
functions that take an allocator state `!{ &a }`: `alloc`, `Array::new`,
`Map::new`, `String::new`, `format`, `keep`, `from_utf8`, `bytes`, `runes`,
the text functions, and the readers in `os` and `fs`. A value from `heap()`
borrows nothing, since the heap is never freed as a whole.

Three checks follow. Returning such a value from the function that made the
arena, or storing it into something that outlives the function, is an error
(`E0631`). Reading it after the arena's `free` is an error, and so is reading
it after `free_all` (`E0633`), where "after" is the order of the function's
own statements: each free carries a tick, each allocation records the tick it
was made at, and a path on which the arena was freed later than the value was
made has the value gone. The tick is part of the flow state and merges by
union, so a free on one side of an `if` counts afterwards; a loop's frees
survive the loop. An `Allocator` is exempt from the `free_all` rule, since a
reset arena allocates again.

**Where it is deliberately not gen-only.** Assigning a plain name replaces the
arenas it holds rather than adding to them. A name reassigned after a reset
holds a new value, and merging would reject every program that resets an arena
and reuses a variable, which is the arena's whole use. The rest of the
analysis, the views, still only adds.

**An allocator parameter is not required in the row.** What a function
allocates from an `Allocator` it was handed is the caller's, so returning it is
accepted without `&a`, and a function that writes `!{ &a }` passes the arena
on to its callers. Requiring the row would be consistent with every other
parameter and would put `!{ &a }` on every function that threads an allocator,
which the corpus does constantly. Rejected: requiring it, for that reason, and
rejected: dropping the exemption for the standard library alone, which leaves
the same function written by a program a different thing. The consequence is
that a wrapper without the row loses the thread, so a value from
`make(frame.allocator())` is not known to be in `frame`. Appendix G lists it.

**Rejected: only rejecting a return past the arena's scope.** It misses the
example the issue is about, a read after `free()` in the same function, and
the flow state it needed was already there for definite assignment. **Rejected:
tracking an arena by its address.** The variable is the identity the analysis
has, and an arena is almost always a named local or a field of one.

### Effect rows themselves

**Status: checked, v1.** Signatures may carry `!{ io }`, and sema
checks the concrete effects the same way it checks borrows: the signature
states the fact and the body is held to it.

**A declared row is a complete statement.** Any row — including one holding
only borrows, like `!{ &xs }` — promises that the body performs no concrete
effect the row does not name. The alternative, treating the effect list as
merely additive, was rejected: a row that cannot say "and nothing else"
states nothing a caller can rely on, and it would make `!{}` meaningless —
which is exactly the row `@gpu` and `@comptime` need. A function with no
row is unconstrained in v1.

**Inference reaches through imports** *(revised)*. A call contributes its
callee's declared row; when the callee states none, the effects are
inferred from its body, bottom-up and transitively — the lattice is one
bit, and a cycle guard makes recursion converge. The body is read no
matter which module it lives in.

This reverses the original ruling, which stopped inference at the module
boundary so that a dependency's private body never became part of its
interface. What that bought was interface stability; what it cost was
soundness — a cross-module call to a rowless function contributed
*nothing*, so a row could promise `!{}` while the body it called printed.
An answer that is silently wrong is worse than an interface that is
narrower than advertised, and the two candidate fixes were: assume the
worst for anything unstated, or read the body. Gyro compiles whole-program
and the loader holds every source, so reading it is exact and free, and
assuming the worst would have forced a row onto every export — the very
sweep this decision cancels.

The cost, accepted and stated: a library's private body is part of what its
callers can promise, so adding a `println` to an unrowed function can break
a caller's `!{}`. A library that wants an effect interface it controls
writes the row, and a written row is always taken at its word — the body is
only consulted where nothing was said.

**A violation reports the route, not just the callee** *(new)*. The error
names the call the body makes, and that call is frequently not what
performs the effect — inference reaches through imports and any depth of
call, so `!{}` could be rejected for a `println` several frames down in
another module with nothing on screen to say so. The diagnostic therefore
carries the route as notes, a hop per line, the last naming the performer:

```text
= note: shape calls emit (render.gyro:34:3)
= note: emit calls println, which does io (text.gyro:30:3)
```

Inference already walked exactly that path to reach its answer, so the
route costs a witness beside the two bits and nothing else.

**The route is drawn as related frames, not as notes.** A note would cite
each hop as a position and leave the reader to open it. But the whole
reason the route exists is that the code performing the effect is code the
reader cannot see from the call being reported — frequently in a module
they never opened — so citing it asks them to trust exactly the step they
could not check. A frame quotes the line. Notes were written first and
rejected on that: they cost fewer lines and deliver less, and the lines
they save are the source the diagnostic is about.

A related span was built to point at one other place — the earlier
declaration — rather than to sequence several, and using it for a route
stretches it. It survives the stretch because the labels carry the
narrative (`shape calls emit`, then `emit calls println, which does io`)
while the frames carry the evidence. What it costs is height: a three-hop
route is three frames, and that is the accepted price.

Writing the route also sent the renderer's own bug to the surface: every
frame resolved against the primary diagnostic's file, so a related span in
another module quoted that line number out of the wrong source, under a
path naming the right one. Nothing had hit it — the only related span in
the compiler pointed at a redeclaration in the same file — and it was fixed
rather than left for whatever became the second user, which is what this
route then immediately was. `diag.Render` takes a `diag.Files` resolver and
looks each frame up by its own name; a name the caller does not hold
degrades to the location instead of quoting something else.

The first route found wins, in source order. Preferring the shortest was
rejected as machinery for no gain — any route answers the question the
reader has, and source order is stable across runs, which a golden needs.
Two effects arriving along the same calls are explained once; along
different calls, separately. A callee that performs the effect itself has
no route and gets no notes, because the message already names it.

**A rowless intrinsic contributes every effect.** The one body that cannot
be read is the one that does not exist. A declaration with no body is a
foreign function, and with no row there is nothing to know it by, so it is
assumed to do `io`. That is what forced every intrinsic in the
standard library to state a row — `Array<T>::get(self, index: usize): T !{}`
down to `MutexGuard<T>::unlock(*self) !{}` — which is the right place for
the annotation burden to fall: the runtime boundary is where a row is the
only possible source of truth.

**The vocabulary is closed.** A row entry that is neither `io` nor a
`&name` borrow is an error (`E0612`). `!{ foo }` used to parse and be
ignored, so a misspelled row promised less than it appeared to.

The effect sources are calls and the stdlib's declared rows:
`print`/`println`/`eprintln` `!{ io }`; `fs`'s `open`/`read_all`/`read_dir`
and one-shot writes, `remove` and `exists`, and `File::close`, `!{ io }`;
`net::get` `!{ io }`; `os`'s `env`, `exit`, `read_stdin` and `readline`
`!{ io }`; all of `time` `!{ io }` — a clock reading is an observation of
the world, and marking it keeps `now()` out of anything `@comptime` will
one day accept. Everything that only allocates, from `Array::new` to
`from_utf8` and the text builders, states `!{}`, since allocation is not
an effect. `Array<T>::slice` keeps its borrow-only row — lending a view
allocates nothing.

A generic body's effects are judged per instantiation, mirroring borrows:
what a call on a type parameter reaches is only known once the parameter is
concrete. A finding at one site is reported once, not once per
instantiation. A *rowless* generic callee is read the same way — through
its recorded instantiations, unioned — because its body resolves no calls
until a type argument is known; before this it contributed nothing, which
was the same silent hole as the cross-module one.

**The built-in method surface performs no effect.** `runes()`, `bytes()`,
`len`, `close`, `hash`, `compare` and `message` have no declaration to hang
a row on, and none of them does io.

**A closure is held to its own row.** `fn(n: i32) !{} { println(n) }` is an
error. Calling a closure still contributes nothing to its caller, so a
closure's row is a local promise rather than something that travels; that
waits for effect polymorphism.

Still open, deliberately out of v1:

- **Row variables.** `!e` and `!{ io, ..e }` parse and leave the row open;
  an open row is not checked for completeness.
- **Function values.** Calling a closure or an `fn`-typed parameter
  contributes nothing, so higher-order functions under-report until effect
  polymorphism (the row variables above) gives them something to say.
  `spawn` carries `!{ io }` as a stand-in for its task's effects for
  exactly this reason.
- **Module-level `var` initializers.** They run before `main`, outside any
  signature, so no row covers them and they are unconstrained. Whatever
  `@comptime` settles for module-level evaluation is where this is
  revisited.
- **Channels and Mutex.** No effect in v1. What concurrency should be in
  the effect vocabulary is undecided — `async` was already rejected as an
  effect in favour of colouring.

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

### The type checker re-checks generic bodies per instantiation *(revised)*

**Status: done.** A generic body is first checked against its bounds, with
its type parameters opaque: a method call, a field, an operator or a
capability on a parameter has to come from a bound, and anything else is an
error in the body. It is then re-checked for every distinct set of type
arguments, which is what produces the code for them; because the first check
allows nothing the bounds do not give, the second does not fail for a type that
met them. Instantiations are cached by (declaration, arguments), recursion
converges through the cache, and a depth cap of 32 stops types that grow
forever. A type argument that does not meet a bound is reported at the call
that asked for it, and the instantiation is not attempted.

The previous rule checked the body only at instantiation, C++-template style,
with operations on an opaque parameter poisoning to Unknown, and an error
reported twice: in the body and at the call. Rejected checking against
inferred constraints — that is bounds without a place to write them.

Inference reads the arguments left to right, with one exception: an
argument that cannot be typed without knowing what is wanted — a closure
that leaves a parameter type or its result to context — waits until the
others have bound the type parameters. Otherwise `map(xs, fn(x) x * 2)`
would work and `twice(fn(x) x + 1, 10)` would not, which is an argument
order rule nobody could predict. A closure that spells its types out
still infers *for* the call, so `run(fn(): i32 { ... })` binds T as
before.

A fixed-size array unifies with a slice pattern, since it lends a view
of itself wherever a slice is wanted. Without that, a generic over `T[]`
took an `Array<T>::slice()` and refused `[1, 2, 3]`, while the same
non-generic signature accepted both.

### Narrowing is symbol-level and syntactic *(new)*

**Status: done (v1).** `x != null` narrows the local x inside the guarded
branch; the checker follows `&&`, `!`, `else`, and early exits, and an
assignment to x withdraws the narrowing. Closures inherit the facts in
scope — captures copy, so a narrowed copy stays non-null. Narrowing does
not track fields (`self.next != null` proves nothing); bind the field to a
local first. That is the same simplification Go's nil checks live with,
and it keeps the analysis obviously sound.

One asymmetry the cJSON port hit five times is *not* deliberate: the
checker follows `&&`'s short circuit but not `||`'s, so `if p != null &&
p.child != null` is accepted while the equally sound early-exit form `if
p == null || p.child == null` is E0510 at `.child`. The two are the same
fact under negation and the second is how C code guards, so a port
written the natural way is rejected. Listed under "Still open".

### Constant expressions fold during checking *(new)*

**Status: done.** Untyped literal arithmetic folds over big integers, so
`var x: u8 = 200 + 56` and `1 << 40` into an i32 are compile errors, not
runtime panics. Operands are typed without the outer context — the folded
result is what must fit. This is also the seed of `@comptime`, which needs
an evaluator anyway.

### Context reaches the operands, not just the expression *(new)*

**Status: done.** When an untyped expression takes a type from its
context, every untyped operand inside it takes that type too. Recording
the type of the outer node alone was not enough: lowering asks for the
type of each operand it emits, and one still carrying `UntypedInt` came
out at the i32 default, so `mask & (0xffffffff >> n)` with a `u32` mask
reached METALS as `shr i32, u32` and failed verification, while `~0`
against a `u32` reached it as `band u32, i32`.

A shift is where this matters most, because nothing local can decide it:
the count keeps its own type, so an untyped left operand has no
neighbour to adapt against and the surrounding context is the only
source. A folded expression is exempt — it emits its value and never its
operands, and only the folded value has to fit, which is what keeps
`var x: i8 = -128` legal.

This closed the recorded checker/interpreter divergence on untyped
operands: the interpreter now keeps `~` and an untyped shift untyped
instead of defaulting them to i32 mid-expression, so `var x: i64 = 1 <<
40` is an i64 shift in every tier. Rejected: making the interpreter read
the checker's recorded types for literals, which would have coupled the
spec tier to `Info` for one narrow case. Corpus:
`tests/testdata/run/untyped-operands.gyro`.

### A shift never panics *(new)*

**Status: done.** `<<` drops the bits that leave the operand's width,
the way `>>` drops the ones that leave the bottom, and a count at or
beyond the width clears the word (or fills a signed `>>` with the sign).
No shift is an overflow.

This resolves a divergence in which the interpreter panicked and the
other three tiers wrapped. The interpreter was the outlier, and it was
the outlier against the written spec too: the book's operator table
puts `<< >>` under *bitwise* and only says the *arithmetic* row panics.

The reasoning, beyond just picking the majority:

- A shift moves a bit pattern; it does not compute a value that could be
  wrong. `a + b` overflowing is nearly always a bug, which is what earns
  that check its keep. Bits leaving a `<<` is the ordinary case.
- The old rule was asymmetric with no stated reason: `>>` discarded low
  bits silently in every tier while `<<` aborted on high ones.
- It made a rotate unwritable. Both the MD5 and SHA-256 ports carried
  `shl(a, b) = (a & (0xffffffff >> b)) << b` purely to compute what the
  plain shift already would have — an extra mask and shift per rotate,
  six rotates a round, sixty-four rounds a block.

Rejected: keeping the check and adding a wrapping `<<%` beside `+% -%
*%`. It is the consistent reading of "an operation that loses a value
panics", but it costs a token, a METALS op, a verifier rule, four tier
implementations and both tree-sitter grammars — and it makes plain `<<`
start trapping in the three tiers that do not today, putting an overflow
test in the hot path of every shift in generated code. Also rejected:
wrapping at runtime but rejecting a *foldable* shift that provably drops
bits, which buys a partial guarantee that is awkward to state.

Corpus: `tests/testdata/run/shift-edges.gyro`.

### Built-in methods are a small fixed surface *(new)*

**Status: done.** `close` on channels; `len` on slices, arrays and
strings (`usize`); `runes(): rune[]` on strings; `compare(other): i32` on
the numeric primitives, `rune` and `string` — which is what lets
`max(3, 7)` and `max("pear", "apple")` both instantiate;
`message(): string` on `error`. There is no `append` on a
slice: `T[]` borrows memory it cannot grow, so growth lives in
`Array<T>::push`. The docs briefly said `xs.append(v)`; that was a relic
and was removed.

`hash` joined later still; it has its own entry below.

`compare` on `string` came later, for the same reason the numeric one
exists: without it a generic body that orders its operands has no
instantiation at `T = string`, and nothing in the language can sort
text. It orders lexicographically by byte, which for UTF-8 is code point
order. Numeric compare is synthesized into two IR instructions during
lowering; the string one walks bytes, so it is a runtime import beside
`runes` and `bytes` instead.

### `Hash` is derived, alongside `Eq` *(revised)*

**Status: decided.** `hash(): u64` is the trait `Hash`, derived over exactly
what `Eq` compares: the primitives by value, a struct by its fields, a tuple
by its elements, an enum by its tag and payload. A struct or enum the program
declares has it with `impl Hash for Point`, and a block with `hash` displaces
the derived one the way `to_string` displaces derived printing. A map's key
is `Map<K: Eq & Hash, V>`.

What forced it: a hash map cannot be written in Gyro without one.
`Map<K, V>` calls `key.hash()`, so `K` has to provide it — and the
alternatives both fail.
Shipping `hash` as stdlib extension methods would mean
`import { hash }` is a prerequisite for using a map, and every struct
used as a key needs a hand-written hash. Leaving it to the user is worse
still: `==` on a struct is already derived, so a hand-written hash that
disagrees with derived equality silently breaks every map it is a key
of. Deriving both from the same parts makes the pairing unbreakable by
construction.

A pointer is left out although `==` accepts one: `==` on a pointer is
identity and an address is not stable from run to run, so a derived hash
over one would differ between the three tiers. A struct holding a
pointer is therefore not hashable either, and `impl Hash for` it is refused
for that field.

The numbers are pinned rather than left to each tier: a scalar goes
through the splitmix64 finalizer, a string is FNV-1a over its bytes, and
a composite folds its parts with FNV's prime. The composites are
synthesized during lowering, one function per type, the way derived
printing already is; only the scalars are runtime imports, since there
is nothing to walk in them. `-0.0` hashes as `0.0`, because the two
compare equal.

### `Map<K, V>` is ordinary Gyro over `Array<T>` *(new)*

**Status: done.** The hash map is written in the standard library in the
language it serves: `Array<Array<(K, V)>>` buckets chained on the derived
`hash()`, growth by doubling past two entries per bucket. The runtime
supplies only the buffer; nothing map-shaped exists in any tier, which is
the point — one implementation cannot diverge three ways.

Chaining over open addressing, forced rather than chosen: Gyro has no
zero values, so an empty probe slot would need a fabricated `K` to
occupy it, while an empty bucket needs nothing. For the same reason
`get` answers `V?` instead of Go's `(V, bool)` — the miss case has no
`V` to return.

**An optional `V` is allowed** *(revised)*. Optionals do not nest, so
`Map<K, V?>::get` answers `null` for a miss and for a stored `null`
alike, and `contains(key)` is what separates them. Rejected rejecting the
instantiation: the disambiguator already exists, so forbidding the type
would remove a use that works. Rejected a dedicated miss signal — a new
option-or-entry type is the general nesting question, not a map question,
and it can arrive later without changing this surface.

Making it legal did need one fix under the hood: a signature written `T?`
instantiated with an already-optional `T` collapses to that same optional
(`types.NewOptional`) instead of building a nested one that METALS
rightly refuses to verify.

`insert` replaces the stored key as well as the value on an equal key
(either is defensible; replacing is simpler than Go's keep-the-first).
`keys()` snapshots in bucket order — deterministic across tiers because
the hash is pinned bit for bit, but not insertion order, and not
promised. Rejected: an iteration protocol on the map itself, which
should arrive with `next()` iterators rather than as a one-off.

Writing it surfaced two checker bugs now kept dead by
`tests/testdata/run/generic-nested-instantiation.gyro`: an untyped
literal against a generic operand defaulted to i32 instead of waiting
for instantiation, and the generic-instance cache keyed type parameters
by name, so two declarations both naming `K` shared one `Array<K>` whose
foreign parameter substitution could never rewrite.

### Tuples assign structurally *(new)*

**Status: done.** `(string, null)` goes where `(string, error?)` is
required: tuple assignability is elementwise, since a tuple is shape, not
identity. Everything else converts only by `as`.

### Runtime semantics the tree-walking interpreter forced

**Status: done.** Gyro's first execution tier was a tree-walking
interpreter over the checked syntax tree, and pinning every runtime rule
the docs asserted forced these decisions. The interpreter is gone (see
"The interpreter is removed" under Testing); the decisions are the
language's and still hold on every tier.

- **A closure owns one copy of its captures**, taken at creation. Its
  calls mutate that copy cumulatively (Rust's FnMut, C++'s mutable
  lambda); the original local never notices. Rejected copy-per-call,
  which would make a closure-held counter impossible.
- **`as` from float to integer truncates toward zero**, then applies the
  fits-or-panics rule. `2.9 as i32` is 2; `1e300 as i32` panics.
- **A closed channel yields no value.** The single-value receive form
  hands back an uninitialized value whose first read panics — the runtime
  shadow of the definite-assignment rule that will catch it statically.
- **alloc'd memory is zero-filled**, by the allocator's contract
  ("Allocators are explicit parameters"). Reading a field before
  writing it reads zero, and nothing detects it; the check belongs in a
  quarantining allocator, not in every block.
- **Panics do not run defers.** A panic aborts the process; there is no
  unwinding, so `defer` is reserved for orderly exits. This follows from
  "programmer errors abort" and keeps defer implementable without
  landing pads.

## METALS

The cross-language IR is specified in [appendix D](appendix-d-metals.md); the format
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

### `any` is the escape hatch for dynamic languages *(new)*

**Status: decided.** One dynamic value type with checked ops (`any.wrap`,
`any.unwrap`, `any.is`, `any.call`, `any.get`/`set`). Rejected
strict-only (unannotated Python could never be imported) and an untyped
core (throws away Gyro's checker exactly at the boundary).

It was called `dyn` until a second front end needed it. Renamed to `any`
so the IR and Gyro's surface use one word for one idea; the spelling is
the only thing that changed.

`any.is` was added at the same time. Without it an `any` could be
produced but not received: `any.unwrap` traps on a mismatch, and so does
every other way of looking inside one, so a module handed an `any` by a
producer it knows nothing about had no way to ask before committing.

**Representation: two words, a descriptor and a payload.** Descriptors
are static data, interned one per distinct type a program wraps or tests,
so identity is structural equality and `any.is` is a pointer comparison.
A one-word value rides in the payload, so wrapping a scalar allocates
nothing; anything wider is heap storage, and so is every record whatever
its width, so that `any.set` through one reference is visible through the
rest. Rejected an always-boxed value — that is an interpreter's
representation, and the cost would be paid by every program that touches
the boundary at all.

**A producer that can declare its types should.** `any` exists for the
languages that genuinely cannot: untyped Scheme, Python, JavaScript. A
front end that knows a function takes an integer and returns one declares
that, and the call from another language is an ordinary call — no
descriptor, no box, no test. The Lisp front end is the worked example:
its exports are declared, so `fib` costs a Gyro caller exactly what a
Gyro `fib` would. Rejected making the boundary uniformly dynamic, which
would have been simpler and would have made every cross-language call
pay for a freedom most callers do not use.

### METALS links; the linker is where signatures are compared *(new)*

**Status: decided.** Every consumer takes one module, so a program of
several is linked into one first: dependency order, root last.

The linker qualifies everything a non-root module owns with its module
name — functions, named types, globals — so two producers may both export
`fib` and both declare `Point`. The root keeps its names, which leaves
`@main` as `@main` and makes linking a one-module program the identity,
so a Gyro program with no foreign imports lowers to exactly what it did
before the linker existed.

Signature matching lives here because this is the only pass that sees
both sides: an importer declares what it expects, an exporter has actual
ones, and until now nothing compared them. Boundary rules stay in
`Verify`, which already runs on each module and needs only one side.

Rejected qualifying every module including the root, which would have
been more uniform and would have renamed `main`; and rejected checking
signatures in the verifier, which cannot see the exporter.

### A closure's environment is a pointer, never a record value *(new)*

**Status: decided.** `closure @f, %env` requires `%env` to be a
`ptr<record>`.

The verifier used to accept a record value, because Gyro's lowering only
ever emits `new` and nothing exercised the other case. The Lisp front end
built an environment as a value and both native backends miscompiled it:
a function value has one word to carry its environment in, and a closure
outlives the frame that built it, so the environment has to be heap
storage. The rule was always real and only the verifier did not know it.

*Amended by "A closure's environment lives in its frame, and keep copies it
out":* the pointer rule stands, but the environment no longer has to be heap
storage. Gyro's lowering points it into the writing function's frame, and
what the pointer addresses is the producer's to keep alive.

This is the first thing a second producer found, and it is the argument
for having one.

### The second producer is a statically typed Scheme *(new)*

**Status: reversed — see "One tier, one backend, two producers".**
`internal/lisp` is deleted; `internal/cc` is the second producer now, and
it is one people use rather than one kept only to break rules. The
closure-environment rule the Lisp found is still tighter for having been
found, which is why this entry stays.

`internal/lisp` compiled a Scheme into METALS, and a
Gyro program imported one by path: `import { fib } from "./fib.lisp"`.

It existed to keep the format honest. METALS is a cross-language IR with
one producer, which means every assumption Gyro's lowering happens to
satisfy is an assumption nobody has tested. The closure-environment rule
above was found within an hour of the Lisp emitting its first closure.

**Typed, not dynamic.** A declaration is a separate form, `(: fib (-> i64
i64))`, and everything else is inferred from it. This is the opposite of
what a Lisp usually is, and it is deliberate: an untyped Lisp would
exercise `any` and nothing else, where a typed one exercises the part of
METALS every backend actually implements, and its exports cost a Gyro
caller nothing. Rejected an untyped Scheme over `any`, which would have
been more authentically Lisp and would have tested less.

**The surface stays ordinary Scheme.** `(: name type)` is a list like any
other — it is Typed Racket's spelling — and internal `define`, `let`,
`let*`, `set!`, `cond`, `begin` and `lambda` mean what they mean
everywhere. A lambda takes its parameter types from context where there
is one, and where there is not, the answer is an internal `define` with a
declaration beside it, which is how a Scheme names a local function
anyway. There is a typed-lambda spelling, `(lambda ((v i64)) ...)`, for
the case that has neither; it is the one form here an ordinary Scheme
would not evaluate.

**A foreign module reaches the checker as declarations without bodies.**
The loader compiles the Lisp, then renders its exports as Gyro
declarations with no bodies — the shape a runtime intrinsic already has —
so name resolution and type checking needed no changes at all. Only
lowering knows the difference, where a body-less declaration in a foreign
module becomes a real cross-module import instead of a `gyro:rt` one.

Two consequences worth stating. A name that crosses has to be spellable
on both sides, so `scale-by` cannot be exported while `even-p` can stay
private with its hyphen. And a program with an imported Lisp module runs
on every tier without ceremony, because every tier reads METALS and the
Lisp module already is METALS by the time anything runs.

### Values copy at runtime boundaries *(new)*

**Status: decided.** No shared mutable memory between runtimes; `ptr`,
`chan` and `future` are banned from boundary signatures; long-lived
foreign state is an opaque `handle`. This is the WASM-component call,
and it is what lets manual memory and garbage collectors coexist.
Rejected one shared object model (forces every runtime onto one memory
model) and handles-only (even an i32 would feel like FFI).

**A boundary is between runtimes, not between languages.** The rule was
first written as "different scheme", because for a JS engine and a Python
interpreter the two are the same thing: a different language means a
different heap with a different collector, and that is what a pointer
must not cross. C is the first case where they come apart. A direct C
front end compiles *into* METALS, so its module is linked into the same
one, verified by the same verifier, and running on the same allocator —
the same relationship `internal/lisp` and `internal/wasm` already have.
Refusing a `ptr` there would refuse it between two halves of one heap,
and would make `import { parse } from "./cJSON.c"` mean marshalling,
which is the thing the C front end exists to stop needing (see "Gyro's
memory model is C's, and stays C's").

So the schemes that produce METALS — `gyro:`, `lisp:`, `wasm:`,
`metals:`, and `cc:` when it arrives — are one runtime, and a call among
them is an ordinary call. `c:` is deliberately not one: nothing has
verified a native library, and that is the same line "Linear memory is a
capability" draws between compiling a language in and linking it. An
unrecognized scheme is read as foreign.

**Revised: `c:` is a boundary, but not the kind this rule was written
about.** See "A native boundary is representation, not memory" below —
a linked library has no heap of its own, so a pointer crosses it.

**The export side moved to the linker.** `Verify` checked every exported
signature unconditionally, which is what actually blocked this: a C
module exporting `fn @parse(...) -> ptr<%Doc>` was refused before any
importer existed. A module knows its own name and its own imports and
cannot know who imports it, so the question belongs where both sides are
visible — the linker, which was already the pass that exists because
"only the linker sees both". It also catches strictly more: a record
redeclared across a boundary whose real shape holds a `ptr` passes the
importer's own verification and is refused at the link.

Rejected **two kinds of export in the IR** — a plain `export` for the
program and a second spelling that crosses anything — which is the
version that keeps a host embedding Gyro from outside the program
covered. It costs new syntax in the format, both grammars, and a decision
every producer has to make on every export, to protect a case that does
not exist yet; when it does, `handle` and the `c:` scheme are the shape
it will take. Rejected **naming C modules `gyro:`** so no boundary ever
appears — cheapest of all, and it throws away the scheme's one job of
saying what produced a module. Rejected **dropping `ptr` from the
forbidden list** outright, which fixes C by removing the protection in
the JS and Python case where it is the whole point.

### C is reached by importing its headers *(new)*

**Status: decided.** A C library is imported from the header that declares
it, and the header is the only description of the library there is:

```gyro
import safe { SDL_Init, SDL_CreateWindow, SDL_Event } from "pkg:sdl3/SDL3/SDL.h"
import safe { sqrt } from "<math.h>"
```

`pkg:<package>/<path>` names a pkg-config package and a header inside one
of its include directories: `pkg-config --cflags <package>` gives the
directories and definitions the header is read with, and `pkg-config --libs
<package>` gives what the program is linked against. `<path>` is a header on
the system compiler's include path — the C library's and the math
library's, which are always linked. This replaces `@extern`, the decorator
and the fieldless `@extern struct`, which are deleted.

**Why the header and not a declaration.** `@extern` made the author write
the signature of every function in Gyro, from memory or from the manual, and
nothing checked it: a symbol name is all a linker compares, so a declaration
that disagreed with the library about a width, a struct or a pointer linked
and misbehaved. A header is what the library's authors wrote, and the C
front end reads it exactly as it reads a `.c` file, so the types are the
real ones. `SDL_Event` is the union SDL3 declares, 128 bytes at its
alignment, and a program that wrote its own would be wrong by exactly the
amount the declaration drifted.

**What an import brings in.** Only what it names, and whatever those names'
signatures mention. A header like `<SDL3/SDL.h>` declares two thousand
things; the front end reads all of them, so a name means what the header
says it means, and keeps the ones that were asked for. Four kinds of name
cross.

- A function becomes an import from the native library, called in C's ABI
  through the shim the backend already generated for `@extern`. It is
  `unsafe` to call unless the import says `import safe`, which is the rule
  for a `.c` import and is stated once ([chapter 4.6](ch04-06-unsafe.md)).
- A struct or union, under its tag, or under the typedef name when the
  typedef names a struct (`typedef struct { ... } div_t`). A tag the header
  leaves incomplete is a fieldless struct, which is Gyro's spelling for a
  handle. A typedef of anything else has no Gyro name, because Gyro has no
  alias, and the message says to write the underlying type.
- An enumerator is a constant of type `i32`, which is the type C gives it.
- A macro is a constant when it is an object-like macro whose expansion is
  an integer constant expression, with the type C's arithmetic gives the
  expression: `0x20u` is `u32`, `1ULL << 40` is `u64`, an unsuffixed
  literal that fits an `int` is `i32`. A header is nearly useless without
  them, since flags are macros. A macro that is a string, a float, a call or
  a statement is not importable, and the message says which.

Everything else a name can be — a variable, a variadic function, a function
taking a `va_list` or a `long double`, a function with an assembler label, a
function whose body the header carries as an inline — is refused at the name
that asked for it, with the reason, and costs nothing for the names that did
not ask. `import * as` of a header is refused: it would be asking for the
two thousand.

**One module per library, not per header.** `<stdlib.h>` and
`<arpa/inet.h>` are read together, and so are two headers of one package,
because a C program's headers are one translation unit and a struct two of
them mention is one struct. Keyed by header, `inet_ntoa` from one and
`in_addr` from the other were two unrelated types, which is not what C says
and not what any program wants. The cost is that a name is found if any
header of the library declares it, whichever one the import said.

**Which compiler the headers are written for.** The front end predefines a
GCC x86-64 target and *does not* define `__GNUC__`. Defining it would make
glibc and SDL reach for `__attribute__`, `__builtin_*`, `typeof` and
statement expressions, which are exactly the extensions this front end does
not have; leaving it out sends them down the portable paths they keep for
other compilers. The extensions that remain are read and set aside:
`__attribute__((...))`, `__asm__` labels, `restrict`, `inline`,
`__extension__`, `_Static_assert` and `long double` as a type that cannot be
lowered. The attributes that change the layout of what they decorate
(`packed`, `aligned`, `vector_size`, `mode`) are refused by name, so the
declaration is dropped rather than laid out wrongly. The compiler's own
header directory is not searched; `<stddef.h>`, `<stdbool.h>`, `<stdarg.h>`,
`<limits.h>`, `<float.h>` and the vector intrinsics' headers are bundled,
because the compiler's are written over macros only that compiler defines.

**A header's bodies are not compiled.** An inline function in a header is
there for a C compiler to inline, and a library's authors wrote it for
theirs. The front end skips its body and says so if the function is named.
The same tolerance applies to a declaration it cannot read at all: it is
dropped, the tags it was defining are remembered so nothing mentions them
as an incomplete type, and the rest of the header is kept.

**`size_t` is `usize` in a header.** The two are the same width, and a
`strlen` that returned `u64` would make every length in a program a
conversion. The rest of unsigned long is `u64`.

**Rejected: hand-written declarations checked by nothing**, which is
`@extern`, for the reason above. **A build-file list of libraries**, the
shape every C project ends up with: the dependency is then stated away from
the import that needs it, and a program can name a function and not the
library that has it. The user chose pkg-config in the path, which keeps the
two in one line. **Bindgen-style generated Gyro files**: a second copy of
the header that drifts from it, in a file somebody has to regenerate and
check in, with the same trust problem one step removed.

**What it costs.** A program that imports a header reads it at compile time,
which for SDL3 is about a second and a half, and needs the system's C
compiler to say where its headers are and pkg-config for a package. Nothing checks what the library does with an address, which is C's own
arrangement; the checked route is a `.c` file the front end compiles.

### A native boundary is representation, not memory *(new)*

**Status: decided.** `*T`, `anyptr` and `code(...)` cross a native import.
So does a `union`. `chan`, `future`, `fn` and `any` still do not, at any
depth. Out-parameters and callbacks work, which puts `qsort`,
`SDL_PollEvent` and every C API built on either within reach of a linked
`.so`.

**The rule that changed is which question a boundary asks.** "Values copy
at runtime boundaries" banned `ptr` because the far side has a heap of
its own — its own lifetimes, its own collector, very likely one that
moves things — so an address means nothing there. That is true of a `js:`
or `py:` runtime embedded in the process. It is not true of a linked C
library, which runs on this thread, in this address space, over the same
libc allocator `gy_alloc` already wraps. There is no second heap to copy
into, and a native call never copied one: what the generated shim does is
convert *widths*, C's real ones against a qword register. So a native
boundary is a boundary of representation, and an address is the one thing
representation has nothing to say about.

Two of the three that stay refused stay refused for a different reason
than the heap, and stating it separately is the point: a `chan` and a
`future` are Gyro runtime objects and C has no representation for one at
any width. `any` is refused because it may cross *any* boundary, so what
may go into one is what the strictest boundary carries — which is also
why `any.wrap` asks the non-native question.

**`fn` is refused where `code` is accepted, and the sizes are the
argument.** A `fn` is code and the environment it captured, which is two
words; a C function pointer has room for one. Accepting a `fn` and then
refusing every value that captured something would be a type saying one
thing while the checker says another — the same reasoning that made
`code` its own type. The diagnostic names `&f`.

**A pointer is walked through rather than waved through.** `ptr<chan<i32>>`
is refused: it is the runtime object the rule exists for, with an address
in front of it. A `ptr<str>` is refused too, because Gyro's string is a
length-prefixed header and C reading through that pointer reads Gyro's
own bookkeeping.

**Rejected: keeping the ban and telling people to import the `.c`.** It
is the honest answer when you have the source and no answer at all when
you do not, which is the case a header import exists for — SDL3 is a `.so`.
Rejected **a decorator opting one declaration into unchecked pointers**,
which prices the general rule as an exception and leaves two spellings
for one boundary. Rejected **marshalling a `*T` into a copy C writes back
after the call**, which is what a real runtime boundary would have to do:
it is wrong for aliasing (two out-parameters into one object stop being
one object), wrong for lifetime (`SDL_CreateWindow` keeps what it is
given), and it would make the shim allocate on a call that currently
does not.

**What it costs, stated plainly.** Nothing checks what a library does
with an address, so a handed-out pointer can be kept past the call, freed
by the far side, or written through after the storage is gone. That is
C's own arrangement and this is the route whose whole premise is that the
library is not yours. The checked route is `import "./x.c"`, where the
linker compares the declaration against the C the front end read.

The C side of it lives in `internal/crt/shim.go`, which grew a real C
declarator emitter — `int (*)[4]` and `int (*)(const void *, const void
*)` are not a base type with a `*` glued on, and a wrapper that spelled
them wrong would compile to something else that also compiles.

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
symmetric with `any.wrap`/`any.unwrap`: `opt.wrap` makes `opt<T>` from
`T`, `opt.unwrap` extracts and aborts on null, null tests are `eq`/`ne`
against `const.null`. Optionals do not nest, matching the language.
Rejected encoding `T?` as `tuple<T, bool>` (needs a fabricated T and
there are no zero values) and as a generated two-variant enum (drags a
type declaration behind every optional).

### The METALS evaluator is the lowering oracle *(new)*

**Status: reversed twice — see "The interpreter is removed, and no tier
is the oracle", then "One tier, one backend, two producers".** It stopped
being the oracle when the goldens became hand-written, and
`internal/meval` is now deleted outright.

A tree-walking evaluator for verified METALS landed
with the lowering pass, so a lowering bug surfaced at the level the
lowering wrote rather than through generated assembly. Rejected
verifier+golden-text only, for exactly that reason.

### C gets a libc, bundled, over a three-import floor *(new)*

**Status: reversed — see "One tier, one backend, two producers".** A C
file now calls the system's own libc: a function it declares and nothing
defines is an import from the native library, resolved when the program is
linked, and its headers are the system's ("C is reached by importing its
headers").
`internal/clibc` is deleted with the bridge it was built for. The
three-import floor — `write_byte`, `read_byte`, `exit` and nothing else —
is the part worth keeping in mind: it is what stopped a linear address
from becoming a pointer at the boundary, and it is the shape any future
sandboxed producer should copy.

`internal/clibc` was a C standard library compiled in
beside every program the wasm bridge built: string.h, the malloc family
over `memory.grow`, stdio with a full printf, the numeric parsers,
qsort/bsearch, ctype, and the math.h names that are METALS instructions
underneath. Before it, the bridge linked freestanding, so a C program
that called `malloc` or `printf` had nothing to call.

**Rejected: adopting wasi-libc.** It is more library for less work —
musl above the syscall line, already built for wasm32, with about a dozen
`wasi_snapshot_preview1` imports underneath — and unmodified upstream C
would compile through it. It was rejected on one practical ground: it
needs a wasi sysroot installed, which is an uncommon package, so the
corpus case proving it worked would skip on nearly every machine
including the one it was written on. A bundled libc is testable wherever
clang is, which is the same bar the bridge already sets. Nothing here
forecloses it: the bridge refuses an unknown import module by name rather
than assuming there is one floor.

**The floor is scalars, and that is forced.** `write_byte(fd, byte)`,
`read_byte()` and the existing `exit(code)`. It cannot be `fd_write`,
because `fd_write` takes a pointer into the guest's memory and a linear
address is a `u32` — handing one to the runtime and asking it to read the
bytes behind it makes it a pointer at the boundary, which is the whole of
what the linear-memory decision above rests on. So stdio buffers inside
the guest and drains a byte at a time.

The cost, stated plainly: one runtime call per byte of i/o. It is paid on
output volume, not on formatting work, and buffering is what keeps a
formatted line from paying per conversion. **Rejected a bulk import**
taking an address and a length, which is the obvious fix and reintroduces
exactly the leak. If the cost ever shows, the answer is a bulk
*instruction* copying between a linear memory and an `Array<u8>` handle,
bounds-checked on both sides, with no address crossing anything.

**Returning from main is `exit(status)`,** which a buffered libc has to
see. The synthesized METALS entry calls `__gy_finish(status)` after main
when the module exports it; a freestanding program that does not is
unaffected. That is also what carries a C exit status out, which the
entry previously dropped.

**Known limit, recorded rather than hidden:** printf's `%f`, `%e` and
`%g` are exact — half-to-even, matching glibc byte for byte — while
`|v| * 10^precision` stays below 2^53, which covers everything printed
for a person to read. Above it the last digit can differ from glibc by
one. The tie cases work because the scaling uses an error-free product
(Dekker's algorithm, since wasm has no `fma` opcode and calling one would
need the libm this library is supposed to be), so `%.1f` of 0.25 is 0.2
and of 0.35 is 0.3. The exact algorithm for the whole range is already in
this repository as `tests/testdata/run/cjson/dtoa.gyro`; porting it is
the next step, and it is the same gap Gyro's own `fmt` has.

### The float intrinsics are instructions, not runtime imports *(new)*

**Status: done.** `fsqrt`, `fabs`, `fceil`, `ffloor`, `ftrunc`,
`fnearest`, `fmin`, `fmax` and `fcopysign` join `fadd`'s family as METALS
operators. `internal/stdlib/src/math.gyro` declares them for Gyro and the
lowering emits the instruction rather than a call; the wasm bridge maps
wasm's nine opcodes onto the same nine.

The alternative was a `gyro:rt` import, which is how every other runtime
service is reached. Rejected for two reasons. Each of these is one
machine instruction or a handful, so a call is most of the cost. And wasm
has them as *opcodes*, which means C has them: a C program that calls
`sqrt` compiles to `f64.sqrt` with nothing to link, so an import-based
answer would have the sandbox call out to round a number. Making them
instructions is what lets Gyro's `math::sqrt` and C's `sqrt` reach one
implementation.

Three definitions had to be chosen, and in all three the obvious C
function is the wrong one:

- **`fnearest` is roundTiesToEven**, not C's `round`, which is
  ties-away-from-zero. Ties-away biases a sum of rounded values upward;
  ties-to-even is what IEEE-754 specifies and what wasm emits.
- **`fmin` and `fmax` propagate NaN**, where C's `fmin`/`fmax` return
  the operand that is not NaN. Dropping a NaN hides the fact that the
  computation produced one. They also order the two zeros. This is why
  clang will not lower `__builtin_fmin` to `f64.min` and emits a call
  instead — correctly — so the corpus pins these two in
  `float-ops.metals` rather than in a C case.
- **The roundings return a float.** Rounding and converting are separate
  questions and `conv` is the one that can abort.

`rt.c` owns `min`, `max` and `nearest` for both backends, since those are
the three where a C library would answer differently; the rest are libm
calls in both, which is one implementation and not two.

**Found on the way, and fixed:** every tier computed `f32` arithmetic at
double precision. `f32` registers hold a double — every scalar is one
qword — and only `conv` and `mem.store.f32` rounded, so `a * b` could
overflow the single-precision range and stay finite, and `(1e20f *
1e20f) / 1e20f` answered `1e20` where C answers `+Inf`. Every operation
producing an `f32` now rounds through single precision. Rejected giving
`f32` its own narrower storage, which would break the one-qword layout
rule for one type.

Also fixed: a non-finite float constant had no spelling. `inf`, `-inf`
and `nan` are it — `inf` and `nan` scan as words the way `true` does and
`-inf` as a signed number, so the format did not have to change.

### Linear memory is a capability, not a second pointer *(new)*

**Status: decided.** METALS gains a `memory` declaration: a flat,
byte-addressed, bounds-checked region sized in 64 KiB pages, with six
instructions over it and `data` segments to initialize it. `ptr<T>` is
unchanged.

The reason is that METALS' typed pointer cannot hold C's memory model,
and C is meant to become a producer. Pointer arithmetic, unions,
bitfields, `&local` escaping its type, varargs, packed structs and
unaligned access are seven features in a list and one feature in
practice: a flat region and offsets into it. WebAssembly is the existing
proof — it is a typed, verified, structured-control-flow module IR with
copy semantics at its boundary, which is METALS' shape almost exactly,
and clang compiles all of C to it with linear memory as the only addition
METALS lacked.

**An address is a `u32`, and that is the whole design.** Not a `ptr<T>`,
not a new type. It is what lets this be added without touching the
boundary rules: a linear address cannot appear in a crossing signature
because it is not a pointer and makes no claim about what it points at, so
"values copy at the boundary" is as true after as before. The two memory
models coexist and no instruction accepts both.

**Every access is bounds-checked, and that is the whole point.** An
unsafe language compiled *into* METALS cannot reach storage outside the
region it declared; the same language linked as a native library can
reach anything. That difference is the argument for a C front end over
a native library, and it would be lost by any design that made a linear address
a real pointer.

Rejected **`ptr<T>` with arithmetic**, which is the obvious smaller
change: it would make every pointer in the IR unsafe to serve the one
producer that needs it, and Gyro's `ptr` would stop meaning what borrow
analysis assumes it means. Rejected **a `bytes` value type** — a slice of
`u8` with indexing — because Gyro's layout gives every scalar its own
qword, so a `slice<u8>` is eight bytes per element and reading a `u32`
out of four of them is not an operation it has. Rejected **making the
memory implicit**, one per module, always present: a module that declares
none should cost none, and a consumer should be able to tell by looking.
Rejected **letting a store narrow its value silently** — `mem.store.u8`
of an `i32` — because that is the one place a width mismatch would be
invisible; `trunc` says it instead.

`trunc` and `bitcast` land with it. `conv` is value-preserving and traps,
which is Gyro's `as` and is right for Gyro; a C cast wraps and a
signedness-per-operation language reinterprets, and neither should be
spelled by weakening `conv`. Rejected adding unsigned variants of `idiv`,
`irem`, `shr` and the comparisons, which is the other way to get the same
result: it doubles a table of instructions to avoid one that is free.

### The wasm bridge is a producer, and a temporary one *(new)*

**Status: done, and now removed as planned.** `internal/wasm` is deleted;
`internal/cc` is the destination it was reconnaissance for. Linear
memory, `trunc` and `bitcast` are what it left behind in the format, and
they stay ([appendix D](appendix-d-metals.md)).

`internal/wasm` read a linked wasm32 module and
translated it to METALS. `gyro cc file.c` ran clang's wasm32 target and
then the bridge, so C compiled and ran on all three METALS tiers.

It existed to answer a question about the IR before a C front end was
written to depend on the answer. wasm32 and METALS have the same
structured control flow — `block`, `loop`, `br` to an enclosing label,
and nothing else — so LLVM's relooper has already solved the hardest
piece of a C front end, and what arrives is real compiled C exercising
linear memory, the indirect-call table and the runtime import surface.
What it leaves behind is an oracle: the same C through two producers, one
evaluator, one answer.

It is not the destination. A C front end (`internal/cc`, chibicc-shaped)
is, and it will have its own scheme; this one is `wasm:`, which says what
wrote the module rather than what the source was.

Two things were decided rather than translated. **The entry point is
synthesized** — a wasm module has exports and a convention, not a `main`
— so the bridge emits one that runs the global initializers, then
`__wasm_call_ctors`, then the C `main`. **A table is translated away**:
the element section says statically which functions it holds, so
`call_indirect` becomes a switch over those indices and no table exists at
run time, which also means an index the table never held aborts instead of
jumping.

Rejected **mapping imports through a table in the bridge**: the wasm
import module string is used verbatim as the METALS module name, so C
declares the boundary itself with
`__attribute__((import_module("gyro:rt")))` and a symbol with no scheme
is refused by name. The claim belongs in the source that makes it.
Rejected **skipping straight to the front end**, which was the other
plan: it would have put the linear-memory design into three consumers on
the strength of an argument, with the first real test of it arriving
months later.

### The C front end emits types, the bridge emits bytes *(new)*

**Status: decided.** `internal/cc` compiles C into METALS directly. Both
routes stay, and which one a `.c` takes is decided by where it appears:
**a `.c` that is the program goes through the bridge, a `.c` a Gyro
program imports goes through the front end.** That is not a coin toss.
The bridge is the only one with a libc and a preprocessor, so it owns the
whole program; the front end is the only one that still has the source's
types by the time METALS sees it, so it owns the import. Through clang,
`struct Point *` is a `u32` and `add(int, int)` is `(i32, i32) -> i32`
with no evidence that the i32s were ints — correct to run and impossible
to import as anything. The same file through both must compute the same
thing, and the corpus is where that is held.

Three mappings were decided rather than derived.

**A C pointer is `opt<ptr<T>>`, not `ptr<T>`.** METALS' pointer is never
null and C's is null half the time, so the nullable type is the only
honest one; `NULL` has no other spelling, and `ptr<T>` would have to
either grow a null or be lied to. It costs an `opt.unwrap` per
dereference and buys the thing compiling C *in* is for: `*p` on a null
pointer aborts and says so instead of being undefined. Rejected
**`ptr<T>` plus a convention** that null is some address, which is how C
does it and is exactly the property METALS refuses to have.

It costs nothing in storage, because `opt<ptr<T>>` gets a **niche**: it
is laid out inside the pointer, with the null address as the absent case,
which is sound precisely because a `ptr<T>` in a verified module always
addresses an object METALS produced. So `sizeof(char *)` is 8, a struct
of pointers is the bytes C lays out, and a zeroed page reads back as a
page of nulls — which is what a C program compiled anywhere else already
assumes. Rejected **giving every optional a niche it happens to have**:
`opt<handle>` and `opt<chan<T>>` look identical from here and are not,
because what a runtime value may hold is the runtime's business and not
the format's. A `ptr<T>`'s non-nullness is a verifier rule, which is the
only kind of guarantee a layout may be built on.

Rejected too **leaving it at two words and calling `sizeof` a detail**.
Every program the front end compiles agreed with itself under that
layout, and none of them agreed with C — and C's own layout is the entire
reason to read C in rather than link it.

**Every local gets a `slot`.** A C local's address may be taken, and
deciding which ones escape is an analysis worth having later and in the
backends, which already do SSA construction. Correct first.

**Arithmetic is `wadd`, not `iadd`.** Signed overflow is undefined in C,
so trapping and wrapping are both defensible readings of it — and the
bridge already answers wrapping, because that is what wasm does, and two
producers of the same file disagreeing is the one outcome the two-route
rule exists to prevent. Rejected **trapping**, which is the reading that
fits the sandbox argument better and would have made `internal/cc` and
`internal/wasm` differ on the first program that overflowed.

Rejected **retiring the bridge** once the front end existed. It is the
reference producer: it is real compiled C with LLVM's relooper already
run on it, which is the answer key for the hardest thing the front end
has left to do.

### The front end has its own preprocessor *(new)*

**Status: decided.** `internal/cc` implements `#include`, `#define`,
`#if` and the rest itself, using Prosser's algorithm: each token carries
the set of macros it came out of, and a macro does not expand inside its
own output.

It could not have been skipped or borrowed. A header is how C says what a
library's types are, and `#include` is the only way to reach one — so
without a preprocessor an imported `.c` can only ever be a file written
for this compiler, which defeats the point of the route. And it cannot be
clang's: clang's is upstream of the bridge, which is the route that
throws the types away.

Rejected **shelling out to `cpp`**, which would make an imported `.c`
depend on a C toolchain being installed to compile a program that then
needs no C toolchain at all — the bridge already carries that dependency,
and having exactly one route that does is worth more than the code saved.

`#include` splices the header's tokens back into the stream the directive
came out of, rather than running it through a nested pass. That is what
makes a `#if` in the includer govern what is inside the header, and a
`#define` in the header visible to the next one.

**Since then it reads the system's headers.** The input is a queue with a
front to push onto, because copying the rest of the stream on every
expansion is quadratic and a libc is a hundred thousand tokens. Keywords
are identifiers until expansion is done, since a macro's parameter may be
spelled `signed`. A run like `32.h` is one preprocessing number that fails
only where something reads it as a number, so a branch that is not taken
costs nothing. `#include_next` finds the header a bundled one shadows. See
"C is reached by importing its headers".

### Pointer arithmetic is three instructions, and unchecked *(new)*

(Pointer arithmetic on a Gyro `*T` is still open, #68. When it lands it is
an `unsafe fn` in the `unsafe` module, the way `offset` on an `anyptr`
is.)

**Status: decided.** METALS grew `elem.ptr`, `ptr.offset` and `ptr.diff`.
`ptr<T>` was a pointer to one object; C's is a cursor into a run of them,
and no arrangement of the existing instructions expresses that.

The three split cleanly by what they answer. `elem.ptr` changes *what* a
pointer points at — `ptr<array<T, N>>` to the `ptr<T>` of its first
element, which is C's decay and costs nothing because an array's storage
begins where its first element does. `ptr.offset` changes *where*.
`ptr.diff` asks how far apart two of them are.

**The arithmetic is unchecked.** A bounds test needs the object's extent
at run time, which means a pointer stops being a machine word — and a
pointer being a machine word is most of what compiling C in rather than
sandboxing it buys. So the rule is C's: the result is a pointer while it
stays inside the object or one element past its end, and outside that the
program is undefined. Rejected **fat pointers** for that reason, and
rejected **routing C's arrays through `array<T>` and `slice<T>`**, which
are bounds-checked and know their length: a C pointer into the middle of
a struct's field is not a slice of anything, and a language whose arrays
are pointer arithmetic cannot borrow a language whose arrays are not.

The evaluator is stricter than the backends here, and deliberately: it
knows which object a pointer belongs to and aborts on a load through one
that has left it, while `nasm` and `cgen` compute the address. The tiers
agree on every defined program, and on an undefined one the evaluator is
the tier that can see it. That is the same asymmetry `uninitVal` already
has, and it is why the evaluator earns its keep.

Consequence: `internal/meval`'s pointer stopped being one Go cell and
became a cell run plus an index — the object is the extent, which is
exactly what C says arithmetic stays inside.

### An element behind a pointer is reached, not copied *(new)*

**Status: decided.** METALS grew a fourth pointer-shaped instruction,
`%q = index.ptr %p, %i`: `ptr<array<T, N>>` and a `usize` to the `ptr<T>`
of element `i`, bounds-checked against `N`.

`index.get` and `index.set` take their container **as a value**. For a
slice that costs nothing — it is two words carrying its own data pointer.
For a fixed-size array behind a pointer it costs everything: the array
has to be loaded first, so reading one byte of a `u8[64]` field copied
all 64, and writing one copied them out and back again. `md5::transform`
moved ~320 bytes per iteration to produce four, and that was most of the
gap between the C backend and the C the case was ported from — 9.62x on
`sha256`, which `index.ptr` took to 5.66x.

**It is a separate instruction rather than a wider `index.get` because
the checked and the unchecked question are different questions**, the
same reason `conv`, `trunc` and `bitcast` are three. `ptr.offset` cannot
check: a pointer does not carry the extent of what it points into.
`index.ptr`'s operand does — `N` is in the type — so the check is a
compare against a constant, and the abort is the one `index.get` already
had, which is what keeps the panic text identical across the tiers.

It also removed the write-back. `index.set` mutates a register, so an
array reached through a place had to be stored back afterwards, and the
lowering wrote `%m = copy %m` when the register was already its own
storage — a copy of every element to change nothing. Storing through an
element pointer needs no such thing.

Rejected **widening `index.get`/`index.set` to accept a
`ptr<array<T, N>>`**: smaller, but it overloads one operand with two
storage models and leaves the write-back untouched, since a `store` is
what removes it and `index.set` is not one. Rejected **emitting the
bounds test in the lowering** over `elem.ptr` and `ptr.offset`: those are
unchecked, so the lowering would have had to raise the abort itself, and
`metals.Abort` carries a static message where `gy_abort_bounds` formats
the index and the length. That is a panic whose text the tiers would have
had to agree on by hand, for no gain over an instruction.

Rejected **reaching slices this way too.** A slice already carries a data
pointer, so there is nothing to save and the extent is not static.

### Module storage is bytes, or it is a function *(new)*

**Status: decided.** A METALS global may carry a byte image — the storage
it starts with, exactly `sizeof` bytes — and a module may name one `init`
function the linker runs before `@main`. C's file-scope initializers
split across the two, and the split is C's own: an arithmetic constant
expression is bytes, an address constant is code.

The reason a global carried no initializer at all was **evaluation
order** — an initializer is an expression, expressions call things, and
the format has no opinion about when. A byte image has no evaluation
order, which is the same argument that already lets a `data` segment
write a linear memory before anything runs. So the rule is not "globals
now have initializers"; it is that static bytes were always allowed and
only memories had them.

What an image cannot hold is a relocation, and the verifier says so by
refusing an image for any type with a pointer anywhere inside it. That is
what leaves `static int *p = &table[1]` and `static const char *msg =
"hi"` as stores in `init`, which is exactly where a C compiler leaves
them.

Rejected **emitting every string literal as per-byte stores in an
initializer function**, which needs no format change and is what the
front end would otherwise have to do. It is O(bytes) in instructions for
something that is O(bytes) in the object file, and a real C program is
mostly string literals — the code would have been deleted the first time
one arrived.

Rejected **a typed constant-expression tree** in the IR (`= [104, 105,
0]`) rather than a byte string. It reads better and commits the format to
a second, parallel description of layout; bytes commit it to one, the
same one `metals.Layout` already computes, and the evaluator decodes them
through that table so no tier can disagree about where a member sits.

**The linker places the call, not the producer.** Gyro owns `@main` and
could prepend the call itself — it used to. A C translation unit is
imported by someone else's program and has no entry point to patch, so
there has to be one pass that can see the whole program, and there
already is. The consequence is that a program of one module is linked
too, which used to be the identity.

### A string literal is module storage *(new)*

**Status: decided.** A C string literal becomes an `array<i8, N>` global
carrying its own bytes, interned by content. Everything else about it
falls out of rules that already existed: naming it is a `global.ptr`, so
it decays like any other array, `sizeof "abc"` is 4 because the array
knows its length, and `"abc"[1]` is an index into storage.

Interning means two identical literals are one array and comparing their
addresses answers true. C permits either answer and every C compiler
interns; the alternative buys nothing and costs a copy per occurrence.

`char buf[] = "abc"` is the one place a literal is not an address — the
bytes are copied into the array — and that is a rule about the
initializer, not about the literal, which is why it lives with the other
initializer rules rather than as a special case in the expression
lowering.

### A callable is one word or two, and those are two types *(new)*

**Status: decided.** METALS grew `code(A, B) -> R` beside `fn(A, B) -> R`,
with `code.ref` to take one and `call.indirect` accepting either. A `fn`
is `{ code, env }` and is two words; a `code` is the address alone and is
one.

The difference is not a representation detail, it is what the type
guarantees. A closure has to keep what it captured somewhere, so a `fn`
needs the second word and `call.indirect` through one tests it and passes
it when it is there. A `code` has nothing to capture, so there is nothing
to test and one call to emit — which is also the call every other
compiler emits for `fp(x)`.

The reason to bother is C's ABI, again. `sizeof(int (*)(int))` is 8, a
struct of callbacks is three addresses and not six words, and a table of
them has C's stride. Rejected **giving C's function pointers the `fn`
type and calling `sizeof` a detail**, which is the same thing that was
rejected for `opt<ptr<T>>` and wrong for the same reason: a program can
be self-consistent about a layout that is not C's, and C's layout is the
entire reason to read C in rather than link it.

Rejected **making `fn` one word and putting the environment behind the
code pointer**, which would unify them at the cost of a trampoline on
every closure call — paying, in Gyro's common case, for C's.

The niche extends to it: `opt<code(...)>` is one word with the null
address as the absent case, sound for exactly the reason `opt<ptr<T>>`
is — every `code(...)` is the address of a function METALS emitted, so no
live one is null. A C function pointer is therefore `opt<code(...)>` and
never `opt<ptr<T>>`: there is no object at the other end to load from,
and `ptr<T>` addresses an object. Calling through a null callback aborts
with a message where C jumps to address zero.

A `code` cannot cross a runtime boundary, for the same reason a `ptr<T>`
cannot: it means an address in this program, and the other side of a
boundary does not share one.

### C's declarator is read as C reads it *(new)*

**Status: decided.** `internal/cc`'s declarator became recursive, which
is what function pointers require and what the flat version was
explicitly deferring.

C's rule is that a declarator reads like the expression that would use
the thing it declares: `int (*fp)(int)` says that `(*fp)(int)` is an int.
The consequence for a parser is that a parenthesised declarator has to be
read twice — the suffix after the closing paren binds tighter than the
`*` inside it, and when the inside is first reached that suffix has not
been seen. So the inside is skipped, the suffix is applied to the base
type, and the inside is read again against what that produced. An
abstract declarator is the same function with the identifier left out,
which is why one implementation now covers casts, `sizeof`, unnamed
parameters and `int (*table[3])(int, int)` alike.

Two smaller rules fall out and are C's, not choices: a parameter of
function type is a pointer to one, exactly as an array parameter is a
pointer to an element; and `*fp` is a function designator that decays
straight back to `fp`, which is why `(*fp)(x)` and `fp(x)` are the same
call and why `(**fp)(x)` is too.

A call's callee also stopped being a name and became an expression, since
`ops[i].apply(a, b)` designates a function and no name appears in it.

### A C program is several files, and one METALS module *(new)*

**Status: decided.** `import { f } from "./lib/entry.c"` names an entry
file. Whatever that file needs and does not define is looked for in the
`.c` files beside it; each file that supplies one is pulled in and asked
the same question, until nothing is missing. All of them lower into one
module.

The problem is that C has no answer of its own. A header declares and a
`.c` defines, and no C source anywhere names the file that defines a
symbol — the linker matches undefined names against defined ones across a
flat namespace, over a file list that a Makefile holds and the language
cannot express. Something outside the C has to supply that list, so the
choice was where to put it.

**Rejected: importing a directory** (`from "./lib/"`), which is the file
list written down. It makes every unrelated `.c` beside the program a
dependency — a file for another target, a scratch copy, a test — and it
gives the import no entry point to be about. The demand-driven search
gets the same set without naming it, and a file nothing needs is never
lowered, so it costs nothing to have sitting there. A sibling that does
not even parse is likewise not an error until something needs it.

**Rejected: one module per `.c`,** with an unresolved prototype becoming
a METALS import of the sibling module that exports it. It is closer to
C's own model and it does not work: two files that call each other are
ordinary C and have no dependency order, so they would be an import cycle
the loader refuses and the linker could not sequence. As one module they
are two ordinary calls.

Merging also buys the check C does not have. A C linker compares nothing
— a prototype that disagrees with the definition links and misbehaves at
run time, which is among the oldest bugs the language has. Here two
declarations of one name must agree before anything is emitted, and a
cross-file call is verified against the definition it reaches.

**The search is one directory, not a tree.** A rule that fits in a
diagnostic beats one that starts matching names it was never pointed at;
a library laid out in subdirectories is imported through a file in each.

`static` is the overlay the model needs, and it is C's own: internal
linkage means the name is that file's, so it becomes `file$name` and two
files may each keep one. A struct from a shared header is one record when
the declarations agree and an error when they do not — C calls that
undefined and says nothing, which is not a useful thing to reproduce.

### A `void *` is a pointer that forgot, not a pointer that is weaker *(new)*

**Status: decided.** METALS grew `anyptr`, the address of an object with
the object's type erased, and `ptr.erase` / `ptr.cast` to take a type off
and put one back. C's `void *` is `opt<anyptr>`, and a cast between two
pointer types is the two instructions in order.

The thing that makes this safe to add is that **an `anyptr` supports
almost nothing**. It compares, and it has a type put back. There is no
load, no store, no `field.ptr`, no `ptr.offset`, no `ptr.diff` — because
none of them has an answer without knowing what is at the address. That
is not a restriction invented for the IR: C already makes `*p`, `p + 1`,
`p[i]` and `sizeof *p` errors on a `void *`, so a C front end loses
nothing it had by saying so in the type. And it is one word, so
`sizeof(void *)` answers 8 and a struct with one in it is the bytes C
lays out.

**Rejected: `ptr<T>` where the producer lies about T**, which is how a C
front end would do this without a new type — declare `void *` as
`ptr<u8>` and cast where needed. It makes every pointer in the IR a
pointer that might be something else, which is the same objection that
kept pointer arithmetic off `ptr<T>` for linear memory. **Rejected: an
integer address**, which is what the wasm bridge uses and is right there:
it would make a pointer and a number the same thing at exactly the
boundary where the format's whole claim is that they are not.

**Erasing and restoring are two instructions, not one.** `conv`, `trunc`
and `bitcast` are three for the same reason: erasing is total and cannot
fail, restoring is a **claim**, and one instruction answering both
questions would let a producer confuse them. Optionality is carried
through rather than dropped and rebuilt — an `opt<ptr<T>>` erases to an
`opt<anyptr>` — because under the niche the two are the same machine
word, and making the producer branch to preserve a null would be a lie
about what the instruction does.

**The claim is checked the way `ptr.offset`'s is.** The natives move a
register; the evaluator still knows what is really in the cell and aborts
when a cast names something else. That is the same deliberate asymmetry:
the tiers agree on every defined program, and the evaluator is the tier
that can see an undefined one. It means **type punning compiles and runs
on two tiers** — reading an object's bytes as another type is undefined
in C too, `char *` aliasing included, and a byte-addressable object model
in the evaluator is what it would take to run it there. Rejected making
the evaluator wave the cast through: the failure would then be a
different answer on one tier rather than a message, which is the one
thing three tiers exist to prevent.

**A function pointer does not join in.** `void *` addresses storage;
`code(...)` is the address of a function. Converting between them is a
POSIX guarantee about a machine, not a C one about a program, and METALS
has two types here precisely so a producer cannot assume otherwise.

An `anyptr` still cannot cross a runtime boundary. Forgetting what is at
an address does not make the address portable — it makes it less
crossable, not more.

### Varargs are an array a caller builds, and METALS learned nothing *(new)*

**Status: decided.** A variadic C function takes two parameters beyond
the ones it declared: a `void **` to the addresses of the arguments after
the last named one, and a `long` count. `va_arg` walks that. The IR grew
no variadic call, no `valist` type and no instruction.

That is the whole argument. **What varies at a C call site is how many
arguments there are and what types they have; what the callee can do
with them is walk them in order, asking for one type at a time.** The
second half is small enough to express in what METALS already has, so it
belongs to the front end — implement semantics at the highest shared
layer, and a calling convention is not a property of an IR that has no
machine.

**The indirection is forced, and it is the interesting part.** The
obvious design is SysV's: a flat area of eight-byte slots. That cannot
be a METALS type, because a slot would have to hold an `int`, a `double`
and a `char *` at once — a pointer and a number sharing storage, which is
exactly what linear memory was kept away from `ptr<T>` to avoid. So the
array holds *addresses*, which are all one type, and each argument is
spilled into a cell of its own. One load more per argument.

**Rejected: a `slice<any>` pack.** `any` is METALS' dynamic value and
would carry the types for free — except `any.wrap` refuses a pointer,
because an `any` may cross a runtime boundary and a pointer may not. A
`%s` needs a pointer, so this ends at the first format string.
**Rejected: teaching the backends SysV varargs** — a register save area,
`gp_offset`/`fp_offset`, the `al` convention. It is the real ABI and it
buys nothing: nothing here calls a C variadic function that this compiler
did not produce, so the two backends would be implementing a convention
only they speak, and nasm would be implementing it twice over.

**What it buys back.** `va_arg` names a type, and that type reaches a
`ptr.cast`, so the evaluator catches a format string that lies. And the
count travels with the arguments, so **reading one argument too many is
an abort with a message on every tier** rather than whatever the stack
held — the oldest bug varargs has, and one C structurally cannot report,
because a `...` passes no count and the callee's only source of truth is
the format string that was already wrong.

`<stdarg.h>` is bundled, because it describes the compiler rather than a
library. `va_list` is an ordinary C struct in it — an array of one, for
glibc's reason: naming one decays to a pointer, so `vprintf(fmt, ap)`
advances the cursor its caller declared. Only `va_start` and `va_arg` are
builtins. `va_end` does nothing and `va_copy` is a struct assignment, and
C can already say both.

**An export the generated Gyro header cannot spell is left out rather
than made an error.** A variadic function is the case that forced it: a
`...` says nothing about what a call passes, so nothing on the far side
of a boundary could check one, and a C library is not written for this
importer. One unspellable helper should not make the other forty
unimportable. Naming a skipped export is still an error, reported where
it is imported, which is where it means something.

### An incomplete type is a name with no shape, not an erased pointer *(new)*

**Status: decided.** METALS grew `type %Name = opaque`: a name the module
knows exists and knows nothing else about. It has no size, no alignment
and no fields, so the only position it may be written in is a `ptr<T>`'s
target. C's incomplete type is that, and a `struct SDL_Window *` is a
`ptr<%SDL_Window>`.

**Rejected: erasing it to `anyptr`.** It was already there, it already
supports exactly the right operations, and it would have cost nothing to
implement — which is why it is worth saying why not. A header that writes
`struct SDL_Window;` and then `SDL_Window *` everywhere is not hiding the
type; it is hiding the *shape* and keeping the type, and the type is the
entire thing the caller gets in exchange for being told nothing. Erased
to `anyptr`, a window and a renderer become one address-shaped thing and
the header's only guarantee evaporates. **A pointer that forgot and a
pointer to something unknown are two different admissions, and only the
second one is still checkable.**

**The declaration is safe for the same reason `anyptr` is: it supports
almost nothing.** `load`, `store`, `ptr.offset`, `ptr.diff` and
`field.ptr` each refuse it and name the layout as the reason, and every
other position — a value, a member, an element, a parameter, a result, a
slot — is refused by the type checker before an instruction sees it. What
is left is a pointer that can be held, passed, stored, compared and cast.
That is exactly what C already permits on a pointer to an incomplete
type, so the front end loses nothing it had.

Two consequences fell out. **A member of an incomplete type is refused
where it is written**, which is C's own rule and also what rules out
`struct node { struct node self; }` without the layout table having to
notice a cycle — a better error, earlier, from a rule that had to exist
anyway. And **a conversion between pointers to two different types now
needs a cast**, which is C's constraint and which most C compilers only
warn about; through `void *` nothing is written in either direction,
which is C's own exception and the reason that type exists. Without it,
two opaque handles would assign to each other freely and the distinction
this whole entry is about would be decorative.

**Rejected: letting the tag stay an error.** Before this, a tag no file
completed failed the module — which is right for a program that needs the
size and wrong for every library header ever written, since the whole
technique is to never provide one.

### A union is a shape, not a record whose fields coincide *(new)*

**Status: decided.** METALS grew `type %Name = union { ... }`: every
member at offset zero, the size the widest member's rounded up to the
strictest member's alignment. `field.ptr` reaches a member the way it
reaches a record's field, and the offset is `metals.Layout`'s answer.

**Rejected: a record with a layout flag.** It would produce the right
address for every member and the wrong answer for every read, because
**the difference between a record and a union is not where the members
sit — it is that writing one unwrites the others**. That is a fact about
reads, and a consumer given a record would have no way to learn it.

**Rejected: reusing the enum payload.** An enum already overlaps its
variants, and the shape is right there. But an enum *knows* which variant
it holds and the format checks the tag; a union is C's, where nothing
records the live member and the program is expected to know. Bolting a
tag on would make every C union carry a word C does not, and `sizeof` is
the whole reason to read C in.

**The evaluator records what C throws away, and that is the interesting
part.** The natives get C's own union, so reading a member other than the
one written reinterprets bytes — which is what C leaves undefined, and no
tier can be called wrong for it. The evaluator has no bytes to
reinterpret. So it records which member is live, which is *strictly more
than C keeps*, and uses it to tell C's two cases apart:

- Reading across members that share an initial sequence is **defined**,
  and it is what every tagged union in C is built on: `event.kind` beside
  `event.key.kind`, in SDL, in X11, in every message queue anyone has
  written. Those are answered, and answered without disturbing the live
  member, because reading does not write in C either.
- Everything else is **reported**, with a sentence naming both members,
  rather than answered.

That is the same asymmetry `ptr.cast` already has, and it lands the same
way: the tiers agree on every defined program, and the evaluator is the
tier that can see an undefined one. `union-punning.metals` is that case
in the corpus, skipped on the two natives with the reason written down.

A union does not compare and does not cross a *runtime* boundary, both
because nothing records which member the storage means: equality has no
question to answer, and a copy has nothing the far side could read. A
record holding one inherits both refusals. It does cross a **native**
one — a linked C library reads it C's own way, and there is no copy to
have nothing in it (see "A native boundary is representation, not
memory").

Gyro declares none of its own; see "Gyro does not declare unions".

On the C side a tag names a struct or a union in one namespace, so
`struct P` beside `union P` is an error rather than two types — which is
C's rule and also what keeps one METALS name from meaning two things. A
braced initializer writes the first member and only the first, which is
all one storage has room for.

### A bit-field is bytes plus an offset, and the offset is the front end's *(new)*

**Status: decided.** A run of adjacent C bit-fields becomes one record
member of bytes, and each of C's members remembers which bits of it are
its own. METALS gained `record align N` / `union align N` and nothing
else.

**Rejected: a member measured in bits.** It is the obvious shape and it
would make every consumer bit-addressed — every backend shifting, every
`field.ptr` answering a question that has no address. The whole reason
storage is C's is that a member has a byte offset.

**Rejected: computing the layout in the front end.** The bit cursor has
to be, because METALS has nothing to compute it with. Everything else —
where the ordinary members sit, how wide each one is — is asked of
`metals.Layout`, which is where that arithmetic belongs. The dividing
line is exactly "what the format cannot be asked".

**The packing rule is x86-64's, not one this compiler made up.** A
bit-field starts at the next free bit and may not cross a boundary of
its own declared type's size; an unnamed `:0` moves the cursor to the
next such boundary and reserves nothing; the struct's alignment takes in
every bit-field's declared type. Every size in the corpus and the tests
was taken from a C compiler on this target before it was written down —
three tiers agreeing with each other says nothing about whether they
agree with C.

**`align N` is what made it expressible.** `struct { unsigned char a:1;
unsigned b:1; }` is one byte of storage inside four bytes of struct, and
the four come from a declared type that is not in the member list any
more. It is a layout *input*, not a consumer's override: `metals.Layout`
still computes every offset and this is one more number it reads. It is
also the knob a `@repr(align(N))` would need.

**A local holding a bit-field is zeroed before its first write.** Writing
one means reading the bytes it shares with its neighbours and putting its
own back, and there is no partial store to do it with — so the storage
has to be readable first. C says the other bits are indeterminate; the
native backends zero a slot anyway, so this is what makes the evaluator
agree rather than refuse a program C allows. The alternative was a
partial-store instruction, which is a bit-addressed store wearing a hat.

`&` and `sizeof` on a bit-field are refused, which is C's rule and the
reason it can be packed at all: an address is a byte's, and a bit-field
is not one. In a union every member begins where the union does, so its
bit-fields all start at bit zero and share one run — overlapping rather
than packing, which is what a union means.

Where the bits actually land is checked by
`tests/testdata/run/c-bitfield-bytes`, which reads the storage as bytes
and is therefore skipped on the evaluator: reading a struct's bytes is
type punning, and the evaluator has values rather than bytes. The values
themselves are checked on all three tiers in `c-bitfield`.

### A goto is a branch out of a block, and C's labels already nest *(new)*

**Status: decided.** A label jumped to from earlier gets a `block`
around everything before it in its statement list; one jumped to from
later gets a `loop` around everything from it onwards; one jumped to from
both gets both. METALS gained nothing.

**Rejected: the relooper.** It is what LLVM does for the wasm bridge and
what Emscripten wrote fifteen hundred lines for, and it is the right
answer for an arbitrary control-flow graph. But a C `goto` is not
arbitrary: what it is written for is a forward jump to a cleanup label, a
backward jump that is a loop, and breaking out of nested loops, and all
three are a label in the goto's own statement list or an enclosing one.
**C's labels nest because C's statements do**, and the two directions
each want one wrapper.

**Rejected: a dispatch loop** — every basic block a case of a `switch`
inside a `loop`, with a state variable. It is correct for any CFG
including irreducible ones, and it destroys the structure of every
function that contains a single `goto cleanup;`. A perf cliff on the
commonest use of the feature is worse than refusing the rarest.

**The peeling order is the whole design.** Wrappers come off one label at
a time, outermost first, and a forward one always goes outside a backward
one in the same region. That is what makes `again: ... goto out; ...
goto again; out:` work: the loop for `again` ends where the block for
`out` does, so falling off the loop leaves the block, which lands at
`out` — exactly what falling past the label did. A loop clipped by an
enclosing block is still the right loop.

**A function with labels reserves its storage at the top.** The verifier
discards everything assigned inside a `block`, because a branch can leave
from anywhere in it — so a local declared before a label would stop
being definite after it, and `int rc = 0; ... goto done; ... done: return
rc;` is the pattern the feature exists for. Hoisting the `slot`
instructions is what every compiler does with allocas, and it costs
nothing: it happens only in functions that have labels.

Two shapes are refused by name, and both are where a control-flow graph
would genuinely be needed: a **jump into a block**, where the label is in
a statement list the goto is not inside, and a **backward jump reaching
past a later forward label**, where the loop would have to cross out of
the block. A label inside a switch body is refused too — that body is a
list of case labels, a multiway branch into a linear run rather than
something a branch can leave.

### An importer names the exporter's type rather than redeclaring it *(new)*

**Status: decided.** A METALS `import` may declare types as well as
functions. The shape is written out in full, the module lays it out and
holds values of it, and `Link` resolves the name to the exporter's
declaration after comparing the two member for member. The linked program
has one declaration and converts nothing.

This is what let a C struct cross into Gyro. Everything else was already
there — layout is C's on both sides, one heap, one runtime — and the one
missing thing was **identity**. A type in METALS is nominal, so a Gyro
`struct Point` and a C `struct Point` were two types with the same bytes,
and passing one where the other was wanted was a name coincidence the
linker read as agreement.

**Rejected: structural type identity.** Two records with the same members
could simply *be* the same type, and then nothing needs declaring. It
would work, and it would mean a Gyro `Point` used for graphics is the
same type as a C `Point` in an unrelated library because both hold two
`i32`s. Nominal is the right default for a format whose whole premise is
that producers are not trusted, and a coincidence is not an agreement.

**Rejected: erasing the type at the boundary.** The generated header
could spell every C struct as bytes, or as `anyptr`, and hand the program
accessors. That is what the wasm bridge already does by accident — by the
time clang is finished, a `struct Point *` is a `u32` — and it is the
reason `internal/cc` exists at all. Losing the type is the thing this
front end was written not to do.

**Rejected: qualifying the name at the source.** Lowering could emit the
foreign module's qualified name directly and declare nothing. Then the
Gyro module alone does not verify — it mentions a type nothing declares —
and a module that only verifies as part of a program is exactly what the
format is arranged to avoid.

Two consequences fell out. **A signature that names a type has to mean
the same type**: within one runtime, importing a function whose signature
says `%Rect` without importing `%Rect` is now refused, because until this
existed there was no way to say which `%Rect` was meant and the linker was
matching spellings. And **a foreign name is spoken for**: an imported
type keeps the exporter's name, so a Gyro type that would take it is the
one that moves, to `gyro::Point`.

**An incomplete type crosses as a fieldless struct**, which is the
spelling Gyro already uses for a handle it may hold and not look inside —
the same shape as `struct File {}` in the standard library.

### `anyptr` is a Gyro type, and `as` forgets *(new)*

**Status: decided.** `anyptr` is a primitive type spelled like `usize` and
`rune`. `p as anyptr` forgets a pointer's target type. It has no other
operations and it cannot go into an `any`.

**Amended:** the title said `as` was both directions, and `p as *T` put a
type back. Remembering is a claim, and a claim belongs behind the unsafe
boundary: `cast<T>(p)` from the `unsafe` module replaces it, and `p as *T`
on an `anyptr` no longer exists. Forgetting cannot be wrong and stays an
`as`. See "Unsafe code is marked on functions, and its primitives are
imported".

It exists because C's `void *` had no Gyro spelling, and a C library that
holds what the caller gave it — every registry, callback table and
generic container in C — is written over one. METALS has had `anyptr`
since the C front end needed it; this is only the surface.

**Rejected: a handle struct for `void *`.** Gyro had a fieldless struct that
meant "an opaque address from a library". It is the wrong shape here for two
reasons: it is *nominal*, so a `void *` from one
library and a `void *` from another would be different types when C says
they are the same one; and it carries a library name, which an address
handed between two of this program's own functions does not have.

**Rejected: `*u8`.** It is a pointer to something, and METALS would have
to convert at every crossing. The point of `anyptr` is that no conversion
happens, because the address never stopped being an address.

**Rejected: an integer conversion.** `n as anyptr` would make a pointer
out of arithmetic, which is the thing this language does not have. The
address has to have come from somewhere. (`ptr_from_int` in `unsafe` is the
one way, for an address the program did not get from an allocation, and it
is an unsafe call rather than a conversion.)

**Printing it prints the word.** There is nothing at the far end this
program may read, so `println(p)` writes `anyptr` — decided in lowering,
so all three tiers say it, rather than left to a backend to refuse.

### A code pointer is its own type, and `&f` is how you get one *(new)*

**Status: decided.** `code(A, B): C` is the address of a function with no
environment beside it — METALS' `code(...)`, which is what C's function
pointer is. `&f` on a declared function produces one. `code` is a
contextual identifier: only a `(` after it in a type position makes the
type, so a program may still call a variable `code`.

**Rejected: reusing `fn`.** A Gyro `fn` is two words, code and the
environment it captured, and a C signature has nowhere to put the second.
Accepting a `fn` where C wants a callback and then refusing every value
that captured something would be a type that says one thing while the
checker says another. Two different things, two names.

**Rejected: `*fn(A, B): C`.** It reads well and it already means
something else: `*T` is `ptr<T>`, so that is a pointer to a closure
sitting in memory. Two spellings of one syntax is worse than one more
type.

**Rejected: context-typing a bare function name.** `sort(xs, compare)`
could make a code pointer out of `compare` because the parameter's type
says to. Sema's rule is that `want` types untyped literals and infers
closure parameters *and nothing else — typed values never convert*, and
that rule is worth more than the four saved characters. `&compare` says
the same thing and says it locally.

**A nullable one needs parentheses.** `code(i32): i32?` is a code pointer
returning a nullable, because the `?` binds to the result it follows.
`(code(i32): i32)?` is the nullable code pointer. A parenthesized result
list followed by `?` is read as grouping, since a result *list* has
nothing to be null — which also gives a nullable tuple result a spelling
it did not have.

## Native backend

Design in [appendix E](appendix-e-native-backend.md).

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
appendix E: aggregate arguments between generated functions pass by
pointer to a caller-owned value rather than by SysV classification —
the C boundary only ever sees scalars and pointers, so it stays exactly
ABI-correct where it matters. Rejected a custom internal convention:
simpler codegen, but every runtime call would need a shim and every
debugger would be blind.

### v1 codegen spilled everything, then got an allocator *(new)*

**Status: superseded by the entry below.** Every METALS register got a
stack slot; instructions loaded operands, computed, stored back. The
output was an order of magnitude slower than necessary and trivially
auditable, which was the correct trade while the bar was byte-identical
output against the evaluator. Register allocation became its own
milestone on a working foundation. Rejected linear-scan allocation from
day one: a much larger bug surface before the first example passes.

### The assembler allocates callee-saved registers only *(new)*

**Status: decided.** `internal/nasm/regalloc.go` colours an interference
graph over the callee-saved registers — `rbx`, `r12`..`r15` — and hands
the five of them to the hottest METALS registers in a function. Every
register keeps its frame slot; a home is an alternative to that slot,
not a replacement for the frame layout.

Callee-saved *only* is the decision, and it is what keeps the allocator
one decision per METALS register instead of one per program point: a
value in `rbx` survives every call the frame makes, so no instruction
spills around a call and none reloads afterwards. The cost is two
memory accesses per used register per call, paid in the prologue and
epilogue, which the weight heuristic (a mention inside a loop counts
ten times an outside one, and a register needs three mentions to earn a
home) is there to cover. Rejected using the caller-saved registers too:
it doubles the register file but requires a live-across-call analysis
and spill code at every call site, and the measured shortfall does not
justify it — of the twelve functions in the `regex` port, exactly one
wants more registers than the five supply.

A METALS register is a candidate when its type is one integer-class
word and no emitter needs its address. Aggregates are excluded by the
layout rules, floats because SysV has no callee-saved xmm register, and
the address-taken cases by an explicit roles table; `frame.addr` fails
the build rather than emitting wrong code if that table ever misses one.
Liveness is a real backward dataflow over the structured body, with a
fixpoint at each `loop` — a linear first-mention-to-last-mention
interval is *not* safe, because a value defined before a loop and read
inside it is live across the back edge where no linear interval says so.

Three things ride along with the allocation, because the allocator is
what exposed them:

- **Small integer constants become immediates.** A register one
  `const` defines and nothing else writes, whose value fits the
  sign-extended 32 bits an x86 immediate is, rides in the instructions
  that read it. Constants propagate through `copy` and through a `conv`
  the value provably survives, which is what makes the fold reach
  anything: the lowering writes both around every literal. A conversion
  the value would *not* survive keeps its runtime check.
- **A short aggregate move is written out.** `rep movsq` costs tens of
  cycles before it moves a byte, so up to eight words move as `mov`
  pairs. This was worth more than the allocator on the ported cases.
- **The destination is the working register.** An operation whose
  destination has a home computes in it, instead of through `rax` and
  back, and comparisons, tests and switches read their operand where it
  lies.

Measured on `tests/bench`, best of five, as a ratio to the C backend on
the same lowered module: `arith-checked` 5.70x to 1.66x, `regex` 13.64x
to 4.84x, `jsmn` 4.57x to 1.82x, `struct-array` 2.19x to 1.13x,
`call-fib` 1.61x to 1.10x, `sha256` 2.74x to 2.08x, `loop-index` 1.06x
to 0.98x.

**What remains is call overhead, and three ports now say so.** The
allocator's own note was that pressure is not the problem — of the twelve
functions in `regex`, one wants more than five registers — and that
`regex`'s residual 4.86x was `cc -O1` inlining `byte_at`, which the
assembler cannot. `md4c` at 3.42x and `perft` at 3.56x are the second and
third data points for the same cause, and they are not small programs
picked to flatter it: md4c is ~90 functions calling `Ctx::ch()` once per
byte examined. Everything in the suite that is *not* call-dominated sits
between 1.00x and 2.10x. Inlining, not a larger register file, is the
next codegen decision.

### Concurrency v1 is OS threads *(new)*

**Status: decided.** `spawn` is `pthread_create`, channels are a
mutex-and-condvar ring buffer in the C runtime (capacity 0 is a true
rendezvous), async calls start eagerly on a thread and `await` joins,
`select` polls its arms. Every example runs natively, so the diff tests
keep full coverage. Green threads with growable stacks remain the right
long-term answer for cheap spawning and are recorded as still open;
a sequential-only v1 was rejected because it leaves the concurrent
examples untested.

### A C emitter sits beside the assembler, sharing one runtime *(new)*

**Status: reversed — see "One tier, one backend, two producers".**
`internal/cgen` is deleted. The reasoning below is kept because the
reversal is itself a decision, and because the runtime split it forced —
`rt.c` living in `internal/crt` rather than inside a backend — outlived
it and is still right.

`internal/cgen` compiled the same verified METALS
module to one C translation unit and links it against the same
`rt.c`, and the execution corpus runs it as a fourth tier, `cnative`.
It is a sibling of `internal/nasm`, not a replacement.

Two jobs. It checks the lowering: when the two native tiers disagree on
one module, the bug is in codegen rather than in METALS, and the C is
readable where the assembly is not. And it gives us a second binary from
the same IR, which is what the spill-everything assembler is benchmarked
against — the gap between them is a measurement of how much the codegen
leaves on the table, not a guess.

Everything that defines behaviour is shared rather than reimplemented:
the layout table, the abort messages, the monomorphized-import mapping,
and above all the runtime itself, which moved to `internal/crt` so
neither backend owns it. What differs is only the spelling — registers
become C locals, aggregates pass by value because C's struct passing
already does what the assembler hand-rolls with sret, and register
allocation is `cc`'s problem.

Rejected: **replacing the assembler with the C emitter.** The point of a
native backend is to own the code generation; handing it to `cc` makes
the compiler a source-to-source translator and forfeits both the
learning and the ability to answer for what the binary does. Rejected:
**forking the runtime**, one copy per backend. Two copies of Go's float
formatter is two chances to diverge from the oracle, and the divergence
would show up as a backend bug in whichever copy was edited second.
Rejected: **a C backend for portability off x86-64.** That is a real
motivation and may become one later, but it is not why this exists, and
designing for it now would have meant giving up the byte-identical
layout that keeps `rt.c` shared.

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
[chapter 16.2](ch16-02-editors.md).

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
maintained table, so the hints cannot drift from what `stdlib` actually
exports.

A guess appears only within one edit for short names, two from five
characters up, and never more than half the shorter name — otherwise
`x` would be offered as the fix for `z`.

### Diagnostics render as a code frame, rustc's shape *(new)*

**Status: decided.** The CLI prints the headline with its code, the
location, the offending line, and the span underlined:

```
error[E0401]: undeclared name println
 --> hello.gyro:2:3
  |
2 |   println("hi")
  |   ^^^^^^^ not found in this scope
  |
  = help: "stdlib" exports println; add: import { println } from "stdlib"
```

Rejected the one-line `file:line:col: error: message` form: the reader
had to open the file to see what the position pointed at. `Diagnostic`
grew `Label` (beside the underline) and `Related` (a second frame,
"previous declaration here"). The span's width is measured from the
source at the offset rather than recorded at every reporting site,
because the AST records where nodes start and not where they end; a name
or a string literal underlines whole, anything else gets one caret.
Colour follows `NO_COLOR` and whether stderr is a terminal.

### Codes are assigned by a per-package table, not at the call site *(new)*

**Status: decided.** `internal/diag/codes.go` holds the catalogue —
E01xx lexical, E02xx syntax, E03xx modules, E04xx names, E05xx types,
E06xx statements — with an explanation each, which `gyro explain E0401`
prints. Each front-end package maps its own message formats to codes in
its `codes.go`, and its `errorf` looks the code up.

Rejected passing the code at every reporting site, rustc-style: that is
170 edits and a code invisible from any one place. The table's cost is
that a reworded message silently loses its code, so
`internal/diag/catalog_test.go` reads the sources with `go/ast` and fails
when a reported format has no entry, or an entry names a code with no
explanation.

### A message repeated at one position is reported once *(new)*

**Status: decided.** `diag.Bag` drops a diagnostic whose position,
severity and text match one it already holds. A function signature is
resolved twice — once when types are built, once when the body is
checked — so every error in a signature was reported twice. Deduplicating
in the bag fixes it for every pass at once, and two identical messages at
one position never told the reader anything the first did not.

### The stdlib is Gyro wherever it can be *(new)*

**Status: decided.** A bundled module is a declaration stub only where a
declaration has no body. `String` has one, and so do `split`, `join`,
`trim`, `contains`, `starts_with`, `ends_with`, `index_of`, `substring`
and `repeat`; the UTF-8 encoder in `push_rune` is Gyro too.

The runtime supplies `Array<T>`, the `bytes()` method on `string`, and
`from_utf8`. Everything above that is written in the language it serves,
which means it is auditable from Gyro, tested by the same corpus as user
code, and implemented once rather than once per tier.

The alternative — a native `String` beside `Array<T>` — would have meant
an implementation per tier (evaluator, C runtime) of a builder with no
interesting runtime requirement.

That applies to the runtime-backed types themselves: `Array<T>` is
declared like any other struct, so a method written in Gyro beside the
declaration — `is_empty` — is called exactly like the intrinsic ones and
exists once rather than three times. It travels with the type, the way
any struct's methods do, so importing `Array` brings it along; only an
extension of a *built-in* type is a module-scope name that has to be
imported by name.

### Built-in types are extensible; an extension is an ordinary name *(new)*

**Status: decided.** `fn string::trim(self): string` attaches a method to
a built-in type from ordinary Gyro, and any module may write one.

What forced the question: the stdlib's text helpers were free functions
while `len` and `runes` were methods, and the line between them was not a
language rule at all — it was "what the compiler already knew about"
versus "what could be added from Gyro". A reader has no way to predict
which is which, and the docs already said operations on built-in types
are methods on those types.

The obvious objection to letting anyone extend a built-in is that a
method declared anywhere becomes callable everywhere, which is the
universe scope the language rules out. The resolution is that an
extension is **not** attached to the type: it is a module-scope name
under its own method name, exported and imported like a function, and
callable exactly where that name is in scope. `line.trim()` works because
`trim` was imported.

That also keeps coherence trivial, and it is why a trait cannot be
implemented for a built-in type: the type would then gain methods from a
module nobody imported. Two modules may each define `string::trim`; importing both is the duplicate-name error
Gyro already reports, and importing one is unambiguous. Nothing is
global, so nothing can conflict globally.

The compiler's own surface stays separate and wins on a name clash: `len`
and `runes` are part of the type, not additions to it, and they need no
import. `void` and `error` are not extensible — nothing has type void,
and `error` is a contract met by shape rather than a type.

### `bytes()` and `from_utf8` both copy *(new)*

**Status: decided, reluctantly.** Neither direction between `string` and
`u8[]` can be a view, so neither carries a borrow row.

The reason is the v1 native layout: every scalar occupies a full word
([appendix E](appendix-e-native-backend.md)), so a `u8[]` slice is word-strided, while a
string's bytes are packed UTF-8. The two representations cannot alias.
The first attempt returned the string's own storage as a slice, which the
interpreter read correctly and a native binary read as garbage.

If the layout ever packs small scalars, both directions can become views
and the rows can come back.

This also retired a rule that was wrong from the start: the borrow
checker treated `s.runes()` as a view of its receiver. `runes()` decodes
into storage of its own and always did, so the rule only ever reported
borrows that were not there.

The copy has a price a renderer pays continuously, which the md4c port
measured: writing a constant tag into a byte buffer means `bytes()` on a
string literal, so `self.literal("<em>")` allocates every time it runs.
Over the ~35 constant tags md4c's HTML renderer emits that is 5.4% of the
hot region (129ms to 122ms with the copy removed) plus an allocation per
emission that nothing reclaims. Interning the literals would recover it
and was not worth the readability; a way to view a literal's bytes
without copying would. Listed under "Still open".

### Time is two i64 clocks and a sleep *(new)*

**Status: decided.** A bundled `time` module with three intrinsics —
every one needs the OS, so all three tiers implement each:

- `now(): i64` — the wall clock, milliseconds since the Unix epoch. It
  dates events; it can step when the system clock is adjusted.
- `monotonic(): i64` — nanoseconds since an arbitrary epoch, never
  stepping backwards. It measures; only differences mean anything. The
  nanosecond resolution is deliberate: this is the clock that benchmarks
  generated code, and native `clock_gettime(CLOCK_MONOTONIC)` delivers
  nanoseconds anyway.
- `sleep(millis: i64)` — blocks at least that long, on the calling
  thread only. Interpreter and evaluator use Go's `time.Sleep`; the
  native runtime loops `nanosleep` across `EINTR`. Spawned threads are
  goroutines or pthreads, so a sleeping thread never stalls the rest of
  the program.

Rejected: a `Time` struct or opaque handle. A reading with no methods on
it is just a number, and `i64` already subtracts, compares and prints;
a handle would add three tiers of plumbing to hide arithmetic the caller
wants. Rejected: one clock serving both jobs — a wall reading cannot
measure (it steps) and a monotonic reading cannot date (its epoch is
arbitrary); gluing them together is how other languages got timers that
misfire on NTP adjustments. Rejected: `f64` seconds, which silently
loses precision as the magnitude grows. Rejected: wall nanoseconds —
wall clocks are not trustworthy below a millisecond, and millisecond
timestamps are what the reading is for. Durations stay plain `i64`
until a units design earns its keep.
### fs writes are path-based one-shots *(new)*

**Status: decided.** The `fs` module grows `write_file`, `append_file`,
`remove`, `read_dir` and `exists`, all taking a path: each call opens,
acts and closes on its own. `File` stays what it was — a read handle for
`open`/`read_all`/`close` — and gains nothing.

The reason is the open drop/RAII question. A writable handle is a
resource that must be closed on every path, and Gyro has no destructors,
so every mode flag and `write` method added to `File` is another leak
shaped exactly like the one `MutexGuard::unlock` already carries. A
one-shot has no open state to forget. When the guard/resource-release
question settles, a streaming write API can be designed against it;
until then the path forms cover what a CLI needs.

`read_dir` returns `(Array<string>, error?)`: entry names only, sorted
bytewise, without `.` and `..` — the evaluator leans on Go's
`os.ReadDir` and the C runtime sorts the same way, so a listing is
deterministic on every tier. Rejected returning richer entries (name
plus kind and size): that shape wants a struct and a stat per entry,
and a caller who needs it can `open` the name. `exists` returns a bare
`bool`; the two-valued form `(bool, error?)` was rejected because the
boolean answer is the whole point — a path that cannot be stat'ed is
absent for every purpose a caller has.

Error strings mirror Go's `os` package byte for byte (`open <path>:
no such file or directory`, `remove <path>: ...`), the same rule
`gy_open` already followed: the wording is Go's because the evaluator
gets it from Go, and the C runtime reproduces it from `errno` rather
than inventing its own.

## Testing

### One execution corpus, run by every tier *(new)*

**Status: decided; the corpus stayed, the tiers did not.** A test program
is a file in `tests/testdata/run` beside the output it must produce.
There is one tier left to run it on — see "One tier, one backend, two
producers" — but the corpus itself is unchanged, and the reason it exists
is the reason it still does.

Before this, each tier kept its own programs as Go string literals, so a
case was only cross-checked if someone remembered to paste it into all
three test files. Most were not. Running the ported cases everywhere
immediately surfaced five divergences none of the per-tier tests could
have found: `println(null)` did not lower, a deferred call's arguments
were evaluated at exit rather than at registration, printing a tuple
needed the same synthesized repr as a struct, and the native runtime
detected neither use after free nor an uninitialized read.

A known gap is a case carrying `//gyro:skip <reason>` — a gap with a
program attached and a test that will fail the day it closes, rather than
a line in a table.

### The interpreter is removed, and no tier is the oracle *(new)*

**Status: decided.** `internal/interp`, the tree-walking interpreter, is
deleted. Three tiers remain, all of them over METALS: the evaluator and
the two native backends. `gyro run` survives as the command that runs a
program and now lowers first; since every input becomes METALS, it also
accepts `.metals`, `.lisp`, `.c` and `.wasm`, and the separate `gyro
meval` verb is gone rather than kept as a second spelling of one act.

**Goldens are written by hand.** `-update` is deleted with the tier it
regenerated from.

Two reasons, and the second is the load-bearing one.

The interpreter was falling behind. It walks Gyro syntax, so it could
never run a `.metals`, `.lisp`, `.c` or `.wasm` root at all, and as
METALS gained producers the share of the corpus it could execute kept
shrinking. It was also the only tier that computed `f32` arithmetic at
double precision — an `f32` multiply that must overflow to `+Inf` stayed
finite — which is the shape of bug a tier accumulates when it reimplements
semantics the other three get from one lowering.

The deeper problem is that an oracle tier writes its own exam. `-update`
regenerated goldens from interpreter output, so an interpreter bug became
the specification the moment someone ran it, and the other three tiers
were then held to that bug. A corpus is supposed to be the place a
divergence surfaces; making one tier definitionally correct removes it
from the set of things the corpus can catch. Removing the oracle makes
the book the only specification, which the documentation always claimed to
be.

The cost, accepted: writing a case now means deciding what it should
print, rather than running it and keeping the answer. That is slower and
it is the work. It also means a case whose output is tedious to derive by
hand — a long digest table, a 652-example spec suite — is more expensive
to add than it was; the ports that already exist keep their goldens, and
a new one of that size should carry a driver in another language whose
output can be diffed, which is what those ports already do.

`//gyro:panic` is now checked against every tier rather than against the
oracle alone. That immediately caught four cases whose fragment matched
only the interpreter's wording, and one real divergence: `gy_free` writes
its poison and then hands the block back to libc, which reuses those
bytes, so the C runtime can never distinguish a double free from a wild
pointer. Both messages became `free of memory that is not allocated`,
and the precision returns when `gy_free` keeps the block instead.

Rejected: **keeping the interpreter as a non-oracle fourth tier.** It
would still be a second implementation of every runtime rule, still
unable to run most producers' roots, and its disagreements would now be
bugs in a tier nobody ships. Rejected: **promoting `meval` to oracle.**
It has the same defect one level down — a lowering bug would become the
golden — and it is the tier the native backends are most likely to share
a mistake with, since all three read the same IR. Rejected: **deleting
`gyro run`** and making people lower explicitly; the shortest path to
running a program is worth a verb.

### One tier, one backend, two producers *(new)*

**Status: decided.** `internal/meval`, `internal/cgen`, `internal/lisp`
and `internal/wasm` are deleted, and `internal/clibc` with the bridge it
existed for. What is left is one consumer of METALS — `internal/nasm`
over `internal/crt` — and two producers, `internal/lower` and
`internal/cc`. The corpus runs each case once. `gyro run` builds into a
temporary directory and executes the binary, so it is a spelling of
`gyro build` and not a second way to execute a program; `gyro lisp` and
`gyro cgen` are gone, `gyro build --backend` is gone, and `gyro cc` now
prints what the direct C front end makes of a file.

**This gives up the cross-check, and that is the whole cost.** Three
tiers held to one golden caught a wrong answer that looked plausible;
one tier does not. What survives is the part that was always doing the
work: the golden is written by hand, before the program runs, and there
is still no flag that regenerates it. The tiers were a way of noticing
when that discipline had been skipped, not a substitute for it.

What is gone with the evaluator, recorded rather than quietly dropped:
use-after-free and read-of-uninitialized detection (it poisoned cells and
the C runtime frees for real), the abort on type punning across union
members with no common initial sequence, and the abort on a `ptr.offset`
or `ptr.cast` that has left its object. Each of those is now undefined
behaviour that runs. The corpus cases that only ever passed on the
evaluator — `panic-use-after-free`, `panic-uninitialized-local`,
`panic-uninitialized-field`, `panic-union-punning`, `union-punning.metals`
— were deleted rather than kept as programs nothing runs.
`//gyro:skip` loses its tier argument, since there is no tier to name.

The C bridge took the whole-program `.c` route with it. `internal/cc`
is a front end for libraries: what a Gyro program imports from a `.c` is
its declared types and functions, and C's `int main` returns a process
exit code that a METALS entry has nowhere to put. A `.c` root is refused
rather than run with its exit code silently dropped. Linear memory,
`trunc` and `bitcast` stay in the format — a flat `u32`-addressed region
is what a producer with C's memory model needs, and that model is not
going away — but no producer emits one today, and what exercises the
instructions is METALS written by hand in the corpus.

Rejected: **keeping `cgen` as the only backend and deleting `nasm`.**
That makes the compiler a source-to-source translator and forfeits both
the code generation and the ability to answer for what the binary does —
the same argument that put the assembler first when the C emitter was
added. Rejected: **keeping `meval` as a non-oracle checking tier.** It
is a second implementation of every runtime rule, and the reason to want
it back is a reason to want a sanitizer instead: what it caught was
memory misuse, and a poisoning allocator in `rt.c` catches that without
a second implementation of the language. Rejected: **keeping the Lisp
front end as the honesty check on the format.** `internal/cc` is a
second producer, it is not Gyro, and it is one people actually use;
a Scheme kept only to break rules is a Scheme nobody reads. Rejected:
**routing a whole-program `.c` through `internal/cc` anyway.** It
compiles, and it returns 0 no matter what `main` returns, which is a
wrong answer rather than a missing feature.

### A diagnostics fixture claims its own diagnostics *(new)*

**Status: decided.** A checker test is Gyro in
`internal/sema/testdata/check`, with a `//~` marker on the line each
diagnostic must land on. Every diagnostic must be claimed and every
marker must be satisfied.

The old form matched a substring anywhere in the bag, which asserts
nothing about position: a diagnostic pointing at the wrong line passed,
and so did an extra one nobody meant to emit. Writing the case as Gyro
also means it parses, highlights and reads like the language.

Fixtures go through the loader, so imports resolve for real. The
single-file `sema.Check` entry point existed only to serve the old tests
and bound imports opaquely; it is deleted.

## Still open

| question | why it matters |
| --- | --- |
| `error` as a trait | `error` is still any type with `message(): string`, the one contract met by shape, with inline storage for its value; folding it into the trait system is issue #113 |
| conditional impls and traits on built-in types | `impl Add for Pair<T>` cannot ask more of `T` than the type does, and a program cannot implement its own trait for `i32` or `string`; associated types, parameter defaults and a `Trait::method` call syntax are not built either |
| mutation rules | when a `*self` method may be called on what, and what `self` may not do |
| `@comptime` evaluation subset | which operations may run at compile time |
| guard/resource release | `MutexGuard::unlock` is explicit because Gyro has no destructors; whether a drop/RAII mechanism ever exists affects guards, `free`, and `close` alike |
| decorator execution model | the book has them mutating the AST on separate threads |
| `syntaxfn` parser extensions | the METALS-facing feature, entirely unspecified |
| METALS remainder | the format is implemented end to end (spec, verifier, linker, two producers, one backend); still open: what the name expands to, the binary encoding, foreign-runtime embedding, whether effect rows enter signatures |
| linear memory has no producer | `memory`, `trunc` and `bitcast` came in for the wasm bridge and outlived it; the corpus exercises them from hand-written METALS, and whether two modules may share one memory is still undecided |
| module-level `const` re-evaluates | a const has no identity to share, so it stays a synthesized getter and its initializer runs per use; `@comptime` folding will subsume this. Module-level `var` does have identity and lowers to METALS storage |
| views through a pointer | `&x` on a local gives a `*T` the analysis stops following, so a slice built from a dereferenced pointer has no traceable origin |
| narrowing across `\|\|` | the checker follows `&&`'s short circuit but not `\|\|`'s, so the early-exit guard `if p == null \|\| p.child == null` is rejected where the equivalent `&&` form is accepted |
| narrowing text | `xs[a..b]` narrows a slice, and a `string` is deliberately not among them: its length is in bytes and its content is UTF-8, so a byte range can cut a code point in half. Whether a text window counts bytes, runes or something else is undecided |
| formatting a float to a precision | `fmt` has no `%e`/`%f`/`%g` and nothing can read a double's bits, so no Gyro program can print a float to a fixed number of digits; the cJSON port needed 358 lines of exact decimal conversion to do what `printf("%.15g")` does |
| a `const` in a match pattern binds | a bare name is a binding, so `match piece { WQ: "q"  WR: "r" }` matches the first arm for every value — it compiles clean and answers wrong. Either warn when a binding shadows a module-scope `const`, or let a path pattern mean equality |
| what a match *expression* takes its type from | the first arm decides, so `Value::Text(s): s` beside `_: null` is E0516; a statement `match` writing into a pre-declared local is the workaround |
| the scope of leak settling | an allocation stored into a field *through a pointer* (`p.child = n`) is not settled, though a struct-literal field is, and tracking does not survive a constructor call, so `var n = new_node()` is untracked entirely. cJSON does real manual memory management and warns zero times, having first been false-positived on the linked-list idiom |
| viewing a literal's bytes | `bytes()` on a string literal allocates, so constant markup written into a byte buffer copies per emission |
| Stack package manager | resolution of bare import names depends on it; the loader rejects them until the `.stack/` layout is decided |
| derived `==`/repr for recursive types | field-wise derivation must handle `*Node?` cycles |
| receive vs tuple destructuring | on `chan<(A, B)>`, `var a, b = <-ch` could mean the (value, open) form or destructuring the element; currently it means (value, open), so the element is received whole and destructured afterwards |
| memory reclamation | native binaries free only what the program frees; strings and other implicit allocations leak until an ownership/GC decision exists |
| use-after-free and uninitialized reads | the runtime frees for real and leaves storage as the allocator returned it, so a binary silently reads garbage. The evaluator poisoned freed cells and unwritten storage and aborted on a read; it is deleted, and a poisoning allocator in `rt.c` is the replacement worth building. It cannot even name a double free today, since `gy_free` hands the block back to libc before a second free could read the poison |
| type punning is not diagnosed | reading across union members with no common initial sequence, or a struct's storage as bytes, reinterprets whatever is there; catching it needs something that records which member was written |
| a pointer that has left its object | `ptr.offset` past an extent and a `ptr.cast` naming the wrong type are a producer's claim and nothing verifies either |
| green threads | OS threads make `spawn` cost a full pthread; a userspace scheduler is the long-term answer |
| a fair `select` in the native runtime | the choice among ready arms is unspecified, so in-order polling conforms — but it can starve an arm behind a busy one, and fixing that means arms become a descriptor table the runtime chooses from |

### Debug information is line tables, from a `loc` instruction, always on *(new)*

**Status: decided, done (#112).** METALS has an optional `loc "file" N`
instruction naming the source position of what follows it. Lowering emits one
before every statement and at the top of every function; the native backend
writes it as NASM's `%line` and assembles with `-g -F dwarf`, so `gdb`
backtraces show Gyro file and line. Appendix E has the details, including the
two things the assembly does for the debugger.

**Why an instruction.** A position belongs to the code it describes and has to
survive linking, printing and parsing like the code does. An instruction does
that with nothing new: it nests in blocks, the printer and parser round-trip
it, and a producer without source (`.metals` by hand, the C front end) just
omits it. The verifier accepts it anywhere and checks only that it is
well-formed.

**Why always on.** The cost is a few rows per statement in the object and no
code, a flag would be a second configuration to test and a reason a crash
report is useless, and `gyro run` would have to thread it too. A stripped
binary is `strip`'s job.

**Rejected: a position field on every instruction.** It touches every
instruction type, the printer, the parser and the linker for something only
one consumer reads. **Rejected: a side table keyed by function and index.**
It breaks the moment a pass inserts an instruction, and the text format would
need a second section. **Not here:** variable locations (a value's register or
stack slot as a DWARF location) and a function-level DWARF description; line
rows and sized symbols are what a backtrace needs.
