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

**Status: planned.** Now specific to `error`, the one dynamically-typed
value in the language: a vtable pointer plus a fixed inline buffer. Small
error types are returned by value with no allocation. A type exceeding the
cap is a compile error naming the size, and the author boxes it explicitly.

The alternative — a hidden heap allocation on every error path — contradicts
explicit `alloc`/`free`.

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
   which is the one thing `@extern` could not do at all. The
   preprocessor, `typedef`, `enum`, `sizeof` and arrays with pointer
   arithmetic are in, so a C library with its own header, a quicksort
   over `int *` and a fixed table in module storage compiles and runs on
   all three tiers. Since then: string literals and file-scope
   initializers, `switch`, `static` locals, function pointers, and a
   program made of as many `.c` files as it takes. "Any existing C
   program" is still the bar and is still a way off — the front end owes
   unions and bitfields, varargs, `void *` and casts between pointer
   types, most of C11, VLAs, `_Generic`, and the relooper for irreducible
   `goto`, the piece `internal/wasm/clang.go` names as the hardest and
   currently outsources. See "The C front end emits types, the bridge
   emits bytes", "Pointer arithmetic is three instructions, and
   unchecked", "A callable is one word or two", and "A C program is
   several files".

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
arenas, a quarantining allocator, or stronger static analysis. Which one
is still open.

### Storage is C's layout, and there is only one *(new)*

**Status: decided.** A value in memory occupies what `sizeof` would say
in C, at C's alignment, padded where C pads. A `u8` is a byte, an `i32`
is four, a struct of `{ u8; u32 }` is eight with the `u32` at four, and
`array<u8, 3>` is three bytes. A METALS *register* still carries a scalar
widened to a qword, and an `f32` as the double it widens to; the move
between the two representations widens on a load and narrows on a store.
`docs/native.md` has the table and the rule.

This replaces v1's "every scalar occupies one qword, nothing is packed",
which was a shortcut, and it is what makes the commitment above real: a
Gyro struct and a C struct of the same fields are now the same bytes, so
a struct crosses `@extern` by value with nothing converted, and a `u32[]`
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
export fn env(name: string): string?
export fn read_stdin(): string
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
the concrete effects use — `io` and `alloc` are checked by the same "read
the callee's row, apply it here" step.

Frame storage propagates through a call: passing a local where the callee
borrows makes the result view the caller's frame. That falls out of mapping
`&param` onto the argument rather than being a separate rule.

### Effect rows themselves

**Status: checked, v1.** Signatures may carry `!{ io, alloc }`, and sema
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
inferred from its body, bottom-up and transitively — the lattice is two
bits, and a cycle guard makes recursion converge. The body is read no
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

**A rowless intrinsic contributes every effect.** The one body that cannot
be read is the one that does not exist. A declaration with no body is a
foreign function, and with no row there is nothing to know it by, so it is
assumed to do `io` and `alloc`. That is what forced every intrinsic in the
standard library to state a row — `Array<T>::get(self, index: usize): T !{}`
down to `MutexGuard<T>::unlock(*self) !{}` — which is the right place for
the annotation burden to fall: the runtime boundary is where a row is the
only possible source of truth.

**The vocabulary is closed.** A row entry that is neither `io`, `alloc` nor
a `&name` borrow is an error (`E0612`). `!{ foo }` used to parse and be
ignored, so a misspelled row promised less than it appeared to.

The effect sources are calls, string interpolation (building text
allocates), and the stdlib's declared rows: `print`/`println`/`eprintln`
`!{ io }`; `mem`'s `alloc` `!{ alloc }`; `Array::new`/`with_capacity`/
`push` `!{ alloc }` (push may grow; pop, get, set, len and free do not
allocate); `from_utf8` and the text builders (`String`'s appenders,
`substring`, `trim`, `split`, `join`, `repeat`) `!{ alloc }`; `fs`'s
`open`/`read_all` `!{ io, alloc }` and `File::close` `!{ io }`; `net::get`
`!{ io, alloc }`. The modules that landed alongside the checker follow the
same scheme: `fs`'s one-shot writes, `remove` and `exists` are `!{ io }`
and `read_dir` `!{ io, alloc }`; `os`'s `env` and `exit` are `!{ io }` and
`read_stdin` `!{ io, alloc }`; all of `time` is `!{ io }` — a clock
reading is an observation of the world, and marking it keeps `now()` out
of anything `@comptime` will one day accept. `Array<T>::slice` keeps its
borrow-only row — lending a
view allocates nothing.

A generic body's effects are judged per instantiation, mirroring borrows:
what a call on a type parameter reaches is only known once the parameter is
concrete. A finding at one site is reported once, not once per
instantiation. A *rowless* generic callee is read the same way — through
its recorded instantiations, unioned — because its body resolves no calls
until a type argument is known; before this it contributed nothing, which
was the same silent hole as the cross-module one.

**The built-in method surface answers from a fixed table.** `runes()` and
`bytes()` copy into new storage, so both are `alloc`; `len`, `close`,
`hash`, `compare` and `message` are effect-free. They have no declaration
to hang a row on, and inventing one for them was rejected — the table is
three lines and the surface is deliberately closed.

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
  `spawn` carries `!{ io, alloc }` as a stand-in for its task's effects for
  exactly this reason.
- **Closure creation.** A capturing closure allocates its environment in
  the native tiers, and that is not counted as `alloc`.
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
the outlier against the written spec too: `language.md`'s operator table
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

### `hash` is derived, alongside `==` *(new)*

**Status: decided.** `hash(): u64` is part of the built-in method
surface, derived over exactly what `==` compares: the primitives by
value, a struct by its fields, a tuple by its elements, an enum by its
tag and payload. A type may declare its own `hash`, which displaces the
derived one the way `to_string` displaces derived printing.

What forced it: a hash map cannot be written in Gyro without one.
Generics are duck-typed, so `Map<K, V>` calling `key.hash()` means `K` is
a key only if it has that method — and the alternatives both fail.
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
pointer is therefore not hashable either, which is the duck-typed error
the checker already knows how to report.

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

This is the first thing a second producer found, and it is the argument
for having one.

### The second producer is a statically typed Scheme *(new)*

**Status: decided.** `internal/lisp` compiles a Scheme into METALS, and a
Gyro program imports one by path: `import { fib } from "./fib.lisp"`.

It exists to keep the format honest. METALS is a cross-language IR with
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
them is an ordinary call. `c:` is deliberately not one: a native library
is exactly the case the rule exists for, since nothing has verified what
it does with an address it is handed, and that is the same line
"Linear memory is a capability" draws between compiling a language in and
linking it. An unrecognized scheme is read as foreign.

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

### C is reached through `@extern`, not a new declaration form *(new)*

**Status: decided (v1).** A foreign function is a declaration with no body
carrying `@extern("SDL2")`. The library is the argument, the symbol is the
declaration's name unless `symbol =` overrides it, and the row is
mandatory.

Every piece of this already existed. Sema has treated a bodyless
declaration as a foreign function since effect rows landed — that is what
"a rowless intrinsic contributes every effect" is about — so the checker
needed no new concept. `lower.ensureForeignImport` already routes an import
to a named module of any scheme, which is how a Lisp module's exports are
called; a C library is one more scheme, `c:SDL2`. The verifier already
treats an import out of another runtime as boundary-crossing and enforces
the copy rule on it, so the ban on `ptr`, `chan` and `future` in an FFI
signature falls out of a rule written for a different reason. And a fieldless struct
is already lowered to `handle`, the METALS type whose whole job is an
opaque foreign reference.

Rejected an `extern "SDL2" { ... }` block, which reads better and groups
the library name once, because it buys a keyword, a declaration form, an
AST node and two tree-sitter grammars' worth of change to say what an
existing decorator says — and the language's own rule is to prefer a
mechanism it already has. Rejected `import { SDL_Init } from "c:SDL2"` with
a sidecar binding file: C has no signatures to read, so the sidecar is
Gyro declarations under another extension, which is a new file kind and a
new resolution rule for no gain over writing them in Gyro directly.

Rejected also the obvious first thought, an `sdl` module in the standard
library with runtime-supplied bodies. It would have been faster to the same
demo and wrong afterwards: every function costs an entry in `rt.c`, two
backend plan tables and two Go tiers, and none of it generalizes to the
next library. `@extern` is the same work done once.

**What crosses.** Primitives by value; `string` as a NUL-terminated copy;
`T[]` as a pointer to its own elements; a struct whose fields all cross,
by value; a fieldless `@extern` struct as `void *`. Callbacks,
out-parameters and `any` are still out.

v1 also repacked `T[]` and refused structs by value, both because the
layout rule then gave every scalar its own qword. Storage is C's now (see
"Storage is C's layout, and there is only one"), so neither restriction
survives: a `u32[]` and a `const uint32_t *` are the same bytes, and so
are a Gyro struct and a C struct. The one copy left is `string`, and that
one is forced by the representation rather than by the layout — C wants a
NUL where Gyro has a length.

**The cost, stated plainly.** A program that calls a foreign function runs
on the two native tiers only. The evaluator is Go with zero dependencies
and no cgo, so it cannot make the call and refuses rather than answering.
This does not fix the tier problem that an `sdl`
intrinsic module would have had — it relocates it and makes it general. The
precedent is one tier away already: a Gyro program importing a Lisp module
skips `interp`, because the tree-walker has no syntax to walk. Rejected a
headless fallback where the Go tiers emulate the library, which keeps all
four tiers running while making them do different things, which is the one
thing the four-tier oracle exists to prevent.

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

**Status: decided.** A tree-walking evaluator for verified METALS lands
with the lowering pass, so a lowering bug surfaces at the level the
lowering wrote rather than through generated assembly. Rejected
verifier+golden-text only, for exactly that reason.

### C gets a libc, bundled, over a three-import floor *(new)*

**Status: done.** `internal/clibc` is a C standard library compiled in
beside every program the wasm bridge builds: string.h, the malloc family
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
`@extern`, and it would be lost by any design that made a linear address
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

**Status: decided.** `internal/wasm` reads a linked wasm32 module and
translates it to METALS. `gyro cc file.c` runs clang's wasm32 target and
then the bridge, so C compiles and runs on all three METALS tiers.

It exists to answer a question about the IR before a C front end is
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

### Pointer arithmetic is three instructions, and unchecked *(new)*

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

**Status: decided.** `internal/cgen` compiles the same verified METALS
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

That also disposes of coherence without a trait system. Two modules may
each define `string::trim`; importing both is the duplicate-name error
Gyro already reports, and importing one is unambiguous. Nothing is
global, so nothing can conflict globally.

The compiler's own surface stays separate and wins on a name clash: `len`
and `runes` are part of the type, not additions to it, and they need no
import. `void` and `error` are not extensible — nothing has type void,
and `error` is a duck-typed contract rather than a type.

### `bytes()` and `from_utf8` both copy *(new)*

**Status: decided, reluctantly.** Neither direction between `string` and
`u8[]` can be a view, so neither carries a borrow row.

The reason is the v1 native layout: every scalar occupies a full word
([`native.md`](native.md)), so a `u8[]` slice is word-strided, while a
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

**Status: decided.** A test program is a file in `tests/testdata/run`
beside the output it must produce, and it runs through the METALS
evaluator and both native backends, all held to the same golden.

Before this, each tier kept its own programs as Go string literals, so a
case was only cross-checked if someone remembered to paste it into all
three test files. Most were not. Running the ported cases everywhere
immediately surfaced five divergences none of the per-tier tests could
have found: `println(null)` did not lower, a deferred call's arguments
were evaluated at exit rather than at registration, printing a tuple
needed the same synthesized repr as a struct, and the native runtime
detected neither use after free nor an uninitialized read.

A known gap is a case carrying `//gyro:skip <tier> <reason>` — a gap with
a program attached and a test that will fail the day it closes, rather
than a line in a table.

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
`docs/language.md` the only specification, which it always claimed to be.

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
| mutation rules | when a `*self` method may be called on what, and what `self` may not do |
| `@comptime` evaluation subset | which operations may run at compile time |
| guard/resource release | `MutexGuard::unlock` is explicit because Gyro has no destructors; whether a drop/RAII mechanism ever exists affects guards, `free`, and `close` alike |
| decorator execution model | the book has them mutating the AST on separate threads |
| `syntaxfn` parser extensions | the METALS-facing feature, entirely unspecified |
| METALS remainder | the format is implemented end to end (spec, verifier, Gyro lowering, evaluator); still open: what the name expands to, the linking/loading model, the binary encoding, foreign-runtime embedding, whether effect rows enter signatures |
| module-level `const` re-evaluates | a const has no identity to share, so it stays a synthesized getter and its initializer runs per use; `@comptime` folding will subsume this. Module-level `var` does have identity and lowers to METALS storage |
| views through a pointer | `&x` on a local gives a `*T` the analysis stops following, so a slice built from a dereferenced pointer has no traceable origin |
| narrowing across `\|\|` | the checker follows `&&`'s short circuit but not `\|\|`'s, so the early-exit guard `if p == null \|\| p.child == null` is rejected where the equivalent `&&` form is accepted |
| narrowing a slice | there is no `xs[a..b]`, so a C API shaped `(pointer, size)` forces either a copy or a `(base, beg, end)` triple through every signature; distinct from pointer arithmetic, and the first thing a callback-driven library asks for |
| formatting a float to a precision | `fmt` has no `%e`/`%f`/`%g` and nothing can read a double's bits, so no Gyro program can print a float to a fixed number of digits; the cJSON port needed 358 lines of exact decimal conversion to do what `printf("%.15g")` does |
| a `const` in a match pattern binds | a bare name is a binding, so `match piece { WQ: "q"  WR: "r" }` matches the first arm for every value — it compiles clean and answers wrong. Either warn when a binding shadows a module-scope `const`, or let a path pattern mean equality |
| what a match *expression* takes its type from | the first arm decides, so `Value::Text(s): s` beside `_: null` is E0516; a statement `match` writing into a pre-declared local is the workaround |
| the scope of leak settling | an allocation stored into a field *through a pointer* (`p.child = n`) is not settled, though a struct-literal field is, and tracking does not survive a constructor call, so `var n = new_node()` is untracked entirely. cJSON does real manual memory management and warns zero times, having first been false-positived on the linked-list idiom |
| viewing a literal's bytes | `bytes()` on a string literal allocates, so constant markup written into a byte buffer copies per emission |
| Stack package manager | resolution of bare import names depends on it; the loader rejects them until the `.stack/` layout is decided |
| derived `==`/repr for recursive types | field-wise derivation must handle `*Node?` cycles |
| receive vs tuple destructuring | on `chan<(A, B)>`, `var a, b = <-ch` could mean the (value, open) form or destructuring the element; currently it means (value, open), so the element is received whole and destructured afterwards |
| memory reclamation | native binaries free only what the program frees; strings, closure environments and other implicit allocations leak until an ownership/GC decision exists |
| native use-after-free and uninitialized reads | the evaluator poisons freed cells and unwritten storage and aborts on a read; the C runtime frees for real and leaves storage as the allocator returned it, so a native binary silently reads garbage where the evaluator panics. It cannot even name a double free, since `gy_free` hands the block back to libc before a second free could read the poison |
| green threads | OS threads make `spawn` cost a full pthread; a userspace scheduler is the long-term answer |
| a fair `select` in the native runtime | the choice among ready arms is unspecified, so in-order polling conforms — but it can starve an arm behind a busy one, and fixing that means arms become a descriptor table the runtime chooses from |
