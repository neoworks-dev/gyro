# Gyro language reference

This file is the source of truth for Gyro's surface syntax. It replaces the
old `gyro.peggy` grammar, which was abandoned mid-design and did not describe
the language as it now stands.

The design was revised on 2026-07-31 (traits removed, tuples added, methods
made free-standing, and more — see `decisions.md`), and the parser, lexer
and examples implement the revised syntax. Semantic analysis exists as a
name-resolution pass; the table at the end lists what is decided but not
yet checked.

One principle shows up everywhere and is worth naming once: **Gyro is
duck-typed wherever a contract is needed.** A type is an `error` by having
`message(): string`, is iterable by having `next()`, works in a generic
function by having whatever methods that function calls. Nothing is ever
declared to satisfy a contract; having the right methods is the whole test.

---

## Lexical structure

### Statement termination

Gyro has no statement-terminating semicolons in normal source. The lexer
inserts a terminator at a newline when the preceding token could end a
statement — an identifier, a literal, `)`, `]`, `}`, `>`, `?`, or one of
`return`, `break`, `continue`, `self`, `true`, `false`, `null`, `_`.

`>` and `?` are in the set because a type can end a line in a
signature-only declaration: `fn Array<T>::new(): Array<T>` and
`fn find(): *Node?`. The consequence is that a trailing `>` cannot split
an expression; break before the operator instead, like the continuation
rule below already prefers.

A newline does *not* terminate the statement when the next line begins with
`.`, `,`, `)`, `]`, or a binary operator that cannot begin a statement —
`&&`, `||`, `==`, `!=`, `<=`, `>=`, `<`, `>`, `+`, `/`, `%`, `|`, `^`, `<<`,
`>>`, `..`, `as`. Both continuation styles therefore work:

```gyro
var total = values
  .filter(is_even)
  .sum()

var ok = user.is_active
  && user.age >= 18
```

`-`, `*`, `&` and `<-` are **not** continuation tokens, because a statement
can begin with them (`*p = 5` assigns through a pointer, `&x` takes an
address, `-x` negates, `<-ch` receives). Those operators must trail the
line they belong to.

A literal `;` is still accepted, which is what makes the C-style `for` header
work and lets several statements share a line.

### Comments

`// line` and `/* block */`. Block comments nest.

### Literals

```gyro
42        1_000_000     0xff      0b1010    0o755
3.14      1e9           1.5e-3
"text"    "hi #{name}"  'c'
true      false         null
(1, "a")                            // tuple
```

A numeric literal is **context-typed**: it takes the type the context
requires, so `var h: u32 = 2166136261` needs no cast and `var bytes: u8[] =
[104, 105]` element literals become `u8`. With no context, an integer literal
is `i32` and a float literal is `f64`. A literal that does not fit the
required type is a compile error, and the default is a requirement like
any other: `var x = 3000000000` is rejected, because the type it would
give `x` cannot hold the value. Write `var x: i64 = 3000000000` instead.
Context never converts *typed* values — only literals adapt.

The context reaches through an expression built only from literals, so
the operands adapt too, and only the value that is finally *used* has to
fit: `var x: i8 = -128` is legal even though `128` alone is not an `i8`,
and `var x: i64 = 1 << 40` is an i64 shift rather than an i32 one that
overflowed. A shift is the case where nothing else can decide: the count
keeps its own type, so an untyped left operand takes the width from
outside. In `mask & (0xffffffff >> n)` with a `u32` mask, `0xffffffff` is
a `u32`; `~0` is likewise all ones at the width of whatever it meets.

A character literal such as `'c'` has type `rune`.

Strings interpolate with `#{expr}`, where the expression is arbitrary Gyro.
Escape it as `\#{` to write the sequence literally. Escapes are `\n`, `\t`,
`\r`, `\0`, `\\`, `\'`, `\"`, `\#`, `\xNN` and `\u{...}`.

### Operators

| class | operators |
| --- | --- |
| arithmetic | `+ - * / %` — these panic on overflow |
| wrapping | `+% -% *%` — deliberate two's-complement wraparound |
| comparison | `== != < <= > >=` |
| logical | `&& \|\| !` |
| bitwise | `& \| ^ ~ << >>` — these never panic |
| assignment | `= += -= *= /= %= &= \|= ^= <<= >>=` |
| access | `.` field, `::` namespace, `[]` index |
| conversion | `as` |
| type | `?` nullable |
| other | `..` range, `@` decorator, `<-` channel, `!{}` effect row |

There is no `++` or `--`; increment is `i += 1`, one way only.

Integer division truncates toward zero and `%` takes the sign of the
dividend: `-7 / 2 == -3`, `-7 % 2 == -1`. Integer divide by zero panics, and
`i32::MIN / -1` panics as overflow.

Floats are IEEE 754 and float division by zero does not panic: `1.0 / 0.0`
is `+Inf`, `-1.0 / 0.0` is `-Inf`, and `0.0 / 0.0` is `NaN`. IEEE defines
those answers, so they are values rather than bugs — unlike integer
division by zero, which has no answer to give.

NaN is unordered. `==`, `!=` aside, every comparison against NaN is false,
including `NaN == NaN`; `NaN != NaN` is true. The three-way `compare()`
method is different on purpose: it is a **total** order, so that a
comparator stays consistent. NaN compares above every number and equal to
another NaN, and `0.0` and `-0.0` compare equal.

```gyro
var nan = 0.0 / 0.0
nan <= 1.0            // false — unordered
nan.compare(1.0)      // 1     — total order, NaN is above
nan.compare(nan)      // 0
```

A shift moves a bit pattern rather than computing a value, so bits that
leave the operand's width are simply gone — `<<` drops the high ones as
quietly as `>>` drops the low ones, and neither is an overflow. A count
at or beyond the width clears the word, or fills a signed `>>` with the
sign; it is defined, not undefined. That is what makes a rotate ordinary
Gyro:

```gyro
fn rotl(a: u32, b: u32): u32 {
  return (a << b) | (a >> (32 - b))
}
```

`>>` is deliberately scanned as two `>` tokens so that nested generic
arguments such as `Result<Vec<i32>, error>` close cleanly; the parser rejoins
them when a shift is meant.

`<-` is scanned greedily. That makes `x<-1` ambiguous with a comparison
against a negative number, so the parser splits the token back apart whenever
it appears in binary-operator position. Written with a space, `x < -1` is
never ambiguous in the first place.

### Conversions

`as` converts between numeric types (and `rune`): `x as i64`. Widening always
succeeds. A narrowing conversion whose value does not fit the target
**panics**, consistent with arithmetic overflow. Deliberate truncation is
spelled with a mask first: `(x & 0xff) as u8`.

`as` is also how a value enters and leaves an `any`: `x as any` never fails,
and `v as i32` on an `any` panics when it holds something else — the same
shape as a narrowing conversion that does not fit.

There are no implicit conversions between typed values, with one exception:
an argument to a `...any` parameter is packed without writing `as any`.

---

## Declarations

### Visibility

Visibility is always explicit. A declaration is private to its module unless
marked `export`. Nothing about a name's spelling changes that — there is no
capitalization rule.

```gyro
fn helper() {}            // module-private
export fn run() {}        // visible to importers

export { a, b as c }      // standalone export list
export { a } from "./m"   // re-export
export * from "./m"
```

### Imports

```gyro
import { println } from "stdlib"
import { open as fopen } from "fs"
import * as ast from "ast"
import "./side-effects"
```

One file is one module. `./util` resolves to `util.gyro` beside the importing
file, and a bare name is the standard library, falling back to a package
under `.stack/`. Circular imports are rejected.

There is **no default export**, and therefore no `import name from "path"`.
A name is spelled the same where it is defined and everywhere it is used.

`from` is contextual, the way `move` and `copy` are: it separates an import
list from its path and is an ordinary identifier everywhere else, so
`String::from` and `var from = 1` both parse.

**If a name is not part of the formal syntax, it has to be imported.** There
is no universe scope and no predeclared functions: operations on built-in
types are methods on those types, so `ch.close()`, `xs.len()` and
`s.runes()` need no import, while `println` does. (There is no `append` on
a slice: `T[]` borrows memory it cannot grow. Growth lives in
`Array<T>::push`.)

### The entry point

`fn main` in the root module is the one blessed name: the compiler finds it
and the OS calls it, so it needs no `export` — nothing imports it. `async fn
main` is allowed; the runtime drives it.

It takes nothing, or the command line:

```gyro
fn main(args: string[]) {
  for arg in args { ... }
}
```

`args[0]` is the name the program was invoked as, so a program run with no
arguments still sees one element. `gyro run app.gyro a b` passes
`["app.gyro", "a", "b"]`, matching what the built binary sees. main returns
nothing — a failing program prints and aborts rather than handing back a
status — and any other signature is an error, since nothing would supply
the arguments.

### Variables

```gyro
var x: i32 = 0
var y = 42            // inferred
const MAX: i32 = 100  // compile-time constant
var value, err = f()  // tuple destructuring
var _, err = f()      // `_` discards explicitly
```

`var x: i32` with no initializer is legal, but reading `x` before every path
to the read has assigned it is a compile error (**definite assignment**).
There are no zero values; nothing springs into existence unwritten.

Assigning in one branch is not enough — every path has to write it:

```gyro
var x: i32
if cond {
  x = 1
}
println(x)        // error: not assigned when cond was false

if cond { x = 1 } else { x = 2 }
println(x)        // fine
```

A branch that cannot fall through does not have to assign, so an early
`return` leaves the other branch's write standing. A loop body may run zero
times, so nothing it writes counts afterwards; the exception is a loop that
cannot fall out of its condition, where `break` is the only exit and the
states at the breaks are what survive. A `match` or `select` runs exactly one
arm, so what survives is what every arm wrote.

Taking an address counts as assigning: `&x` names storage that is real and
sized whether or not anything has been written there, and writes through the
pointer cannot be followed. Reading through such a pointer before a write
panics at runtime rather than failing the build.

A closure may read only what is already assigned where it is written, and
what it assigns does not count outside it — it runs at a time this analysis
cannot pin down.

An inner block may shadow an outer name; redeclaring a name in the **same**
block is an error.

```gyro
var x = 1
if cond {
  var x = 2      // ok: inner scope
}
var x = 3        // error: already declared in this block
```

### Functions

```gyro
fn add(a: i32, b: i32): i32 {
  return a + b
}

fn nothing() {}                      // no annotation means no result
fn load(p: string): (string, error?) // returns a tuple
fn max<T>(a: T, b: T): T             // generic
async fn fetch(u: string): (string, error?)
fn apply(f: fn(i32): bool) {}        // function-valued parameter
fn sum(xs: ...i32): i32              // variadic
fn printf(fmt: string, args: ...any) // variadic over mixed types
```

The return type follows a colon. Parameters may carry defaults:
`fn f(n: i32 = 4)`.

### Variadic parameters

A final parameter written `name: ...T` collects the remaining arguments.
Inside the body it is an ordinary `T[]` slice — `len()`, indexing and
`for..in` all work, and it borrows storage the caller owns, so it must not
outlive the call.

```gyro
fn sum(xs: ...i32): i32 {
  var total = 0
  for x in xs {
    total += x
  }
  return total
}

sum()           // 0
sum(1, 2, 3)    // 6
```

Only the last parameter may be variadic, it takes no default, and a call
site may supply zero arguments for it. An existing slice is forwarded with
a trailing `...`, which is the only way to pass one as the whole pack:

```gyro
fn log_all(rest: ...i32) {
  sum(rest...)    // forwards the pack
  sum(rest)       // error: i32[] is not an i32
}
```

The heterogeneous form is `...any`, which is what `printf`-shaped code
wants — see `any` under Types.

### Methods

A method is a free-standing function whose name is qualified by its type.
Structs stay pure data; behaviour lives beside it at top level. `self`
borrows the receiver immutably and never copies it, however large the struct
is. `*self` takes a pointer and may mutate. A function with no receiver is an
associated function, called as `Stack::new(16)`.

```gyro
export struct Stack {
  export capacity: i32
  top: i32
}

export fn Stack::new(capacity: i32): Stack {
  return Stack{ capacity: capacity, top: 0 }
}

fn Stack::is_empty(self): bool {
  return self.top == 0
}

fn Stack::push(*self, v: i32) {
  self.top += 1
}
```

Methods on a generic type name the parameter in the qualifier:
`fn Pair<T>::first(self): T`.

A built-in type can be extended the same way:

```gyro
export fn string::shout(self): string {
  return "#{self}!"
}
```

An extension method is an **ordinary module-scope name**: it is exported
and imported like a function, and it is callable exactly where that name
is in scope. That is what keeps it out of universe scope — `line.trim()`
works because `trim` was imported, not because some module somewhere
declared it. Two modules may both extend `string` with `trim`; importing
both is the collision Gyro already reports, and importing one is
unambiguous. Being a method, it cannot be called without a receiver.

The compiler's own surface — `len`, `runes`, `bytes`, `close`, `compare`,
`hash`, `message` — is part of the type rather than an addition to it. It
needs no import and an extension of the same name does not displace it.

`void` and `error` cannot be extended: nothing has type void, and `error`
is a contract satisfied by whatever carries `message()`, not a type.

There is no `impl` block, no trait, and no `Self`; a method writes its type's
name out.

### Generics

Generics are **duck-typed**. A type parameter declares no bounds; the body's
usage is the requirement, checked when `T` becomes concrete. Any type with
the methods the body calls works; any type without them is a compile error at
instantiation.

```gyro
fn max<T>(a: T, b: T): T {
  if a.compare(b) > 0 {
    return a
  }
  return b
}

max(3, 7)        // ok: i32 has a built-in compare
max(v1, v2)      // ok: Version has compare
max(m1, m2)      // error: Mutex has no method compare
```

User structs may be generic, instantiated the same way:

```gyro
struct Pair<T> {
  first: T
  second: T
}

fn Pair<T>::swap(*self) {
  var tmp = self.first
  self.first = self.second
  self.second = tmp
}

var p = Pair<i32>{ 1, 2 }
```

Type arguments live on path segments — `Array<i32>::new` parameterizes a
non-final segment.

### Closures

A closure is an anonymous function, written with the same `fn` and the same
`:` result marker. There is one spelling of "function" in the language.

```gyro
var double = fn(a: i32): i32 { return a * 2 }
var greet  = fn() { println("hi") }
var later  = async fn(): i32 { return 1 }
```

Two shorthands apply where they are unambiguous. Parameter and result types
may be omitted when the expected function type is known from context, and a
body consisting of one expression needs neither braces nor `return`:

```gyro
xs.map(fn(x) x * 2)
xs.filter(fn(x) x > 0)
```

"Known from context" includes a generic call, where the context is the
instantiation. A closure argument is typed after the arguments that fix
the type parameters, so it does not matter which side of the call the
closure sits on:

```gyro
fn apply<T>(x: T, f: fn(T): T): T !{ &x }
fn twice<T>(f: fn(T): T, x: T): T !{ &x }

apply(5, fn(v) v * 3)      // T is i32, from the 5
twice(fn(v) v + 1, 10)     // likewise, though the closure comes first
```

A closure captures the locals it uses by **copy**, implicitly. The capture
list exists for one purpose: transferring ownership. `fn[move node]()` moves
the binding into the closure, which is what lets the leak analysis see an
allocation escape. `move` is contextual — an ordinary identifier everywhere
else.

```gyro
var node = alloc<Node>()
var release = fn[move node]() { free(node) }   // ownership moves in
```

The type a closure inhabits is the function type that already exists:
`fn(i32): i32`, or `async fn(): i32` for an async closure. In type position
`fn(A): B` is a type; in expression position `fn(a: A): B { ... }` is a
value. The two never collide.

### Structs

A struct declares data and nothing else.

```gyro
export struct Stack {
  export capacity: i32
  top: i32
}
```

A struct literal is only legal where every field is visible, so a type with
private fields is constructed exclusively through its own associated
functions. That is what makes invariants enforceable — `Mutex::new(0)`, never
a literal of a type whose fields you cannot see.

Struct literals come in two forms, named and positional. A literal is one or
the other, never a mix:

```gyro
Point{ x: 1, y: 2 }   // named
Pair<i32>{ 1, 2 }     // positional, in declaration order
```

### Enums

```gyro
export enum Shape {
  Circle(f64),
  Rect(f64, f64),
  Point,
}
```

### Decorators

`@name` or `@name(args)`, attachable to any declaration. Arguments may be
named. The decorator names are a **fixed set known to the compiler** —
`@comptime`, `@gpu`, `@concurrent`, `@distributed`, `@extern` — because they
configure compilation itself; there is nothing meaningful to import. An
unknown decorator name is a compile error.

```gyro
@comptime const FIB_10: i32 = fib(10)
@gpu fn kernel(a: *f32) {}
@concurrent(threads = 8) fn work() {}
@extern("m") fn sqrt(x: f64): f64 !{}
```

`@extern` is the only one with an implementation behind it today; see
[foreign functions](#foreign-functions).

---

## Types

| form | meaning |
| --- | --- |
| `i8 i16 i32 i64` | signed integers |
| `u8 u16 u32 u64` | unsigned integers |
| `usize` | pointer-width unsigned; lengths, indexes, `T[N]` sizes |
| `f32 f64` | floats |
| `rune` | a Unicode code point (32-bit) |
| `bool string void error` | other primitives |
| `any` | a value of any single type, carrying which |
| `*T` | pointer, never null |
| `T?` | nullable |
| `T[]` | slice — a borrowed view |
| `T[N]` | fixed-size array |
| `chan<T>` | channel |
| `(A, B)` | tuple |
| `fn(A, B): C` | function |
| `async fn(A): B` | async function |
| `ast::Literal` | namespaced name |
| `Pair<i32>` | generic instantiation |

### Any

`any` holds a value of exactly one other type and remembers which. It is a
first-class type: a variable, a field, a slice element, a channel element.
It exists so that a `...any` pack can carry mixed types.

A value enters an `any` with `as`, which never fails, and leaves it with
`as` back to a concrete type, which **panics** when the type disagrees —
the same rule narrowing conversions follow.

```gyro
var v: any = 42 as any
var n = v as i32         // 42
var s = v as string      // panics: any holds i32, not string
```

`match` tests the type instead of asserting it, spelled like the type test
on an `error`. `any` is an open subject like `error` and `i32`, so a match
on one is never reported as partial and falling off the end panics.

```gyro
var text = match v {
  i32(n):    "int #{n}"
  string(s): "text #{s}"
  _:         "something else"
}
```

A type an ordinary path cannot spell is written out in the arm:

```gyro
var text = match v {
  (i32, string)(pair): "pair #{pair.0}"
  i32[](xs):           "#{xs.len()} numbers"
  i32[3](fixed):       "three numbers"
  *Node(node):         "node #{node.value}"
  _:                   "something else"
}
```

An `any` holds any type except the ones that could not answer for
themselves afterwards: `void`, `null`, another `any`, an `error` or a `T?`
(both of which hide the concrete type the any is meant to be sure of), a
channel, a future, a function value, and the runtime-owned handles such as
`Array` and `Mutex`.

`println(v)` prints whatever is inside, using that type's derived
representation, so nothing has to be unwrapped just to show it.

The only implicit conversion in the language is packing: an argument to a
`...any` parameter is wrapped without writing `as any`, because the call is
what constructs the pack. Assignment never wraps — `var x: any = 42` is an
error and `42 as any` is the fix.

Gyro compiles whole programs, so the set of types any `as any` in the
program mentions is known at compile time; `any` is that closed set, not a
runtime type registry. It is not an escape from the type system: there is
no way to call a method on an `any` without first naming the type it holds.

### Tuples

`(A, B)` is a first-class type: it can be a field, a variable, a channel
element. Values are written `(a, b)`, read by destructuring or positional
field access:

```gyro
var pair = (200, null)
var code = pair.0
var a, b = pair

var results = chan<(string, error?)>()
results <- (body, null)
```

A multi-value `return a, b` is sugar for returning a tuple. `(expr)` with one
element stays plain grouping; there are no one-element tuples.

### Nullability

A `*T` can never be null; only a `T?` can. The compiler refuses to
dereference a nullable value until it has been checked, and narrows the type
inside the check.

```gyro
fn find(id: i32): *Node? { ... }

var node = find(3)
// node.value          // error: node may be null
if node != null {
  println(node.value)  // narrowed to *Node here
}
```

The marker binds to the whole type, so `*Node?` is a nullable pointer. Write
`*(Node?)` for a pointer to a nullable value.

### Slices, arrays and strings

`T[]` is a **slice**: a pointer and a length that borrows memory it does not
own. Passing one allocates nothing, and it can view a fixed-size array, an
owning collection, a literal, or GPU device memory.

Growth lives in `Array<T>`, which owns its buffer and is freed explicitly.
`T[N]` is a *fixed-size array*, `Array<T>` a *dynamic array*.

```gyro
fn total(values: i32[]): i32 { ... }   // borrows

var buf = Array<i32>::new()
defer buf.free()
buf.push(4)

total(buf.slice())
total([1, 2, 3])
```

An array literal such as `[1, 2, 3]` is a fixed-size array value in the
enclosing scope; a slice of it borrows that storage.

`len()` returns `usize` and `[]` indexes with `usize`. Because arithmetic
panics on overflow, `xs.len() - 1` on an empty slice panics loudly instead of
wrapping around.

`string` is the same arrangement over UTF-8 bytes: a borrowed view, with
`String` as the owning builder. `len()` counts bytes, `runes()` iterates
code points as `rune`, and `bytes()` yields the bytes as `u8[]`. Both
`runes()` and `bytes()` copy — a string is immutable and shared and a
slice is neither, and a rune is four bytes where UTF-8 is not — so
neither borrows its receiver.

```gyro
var out = String::new()
defer out.free()
out.push("hello")
out.push_rune('!')
println(out.text())
```

`String` and the text methods — `split`, `trim`, `contains`,
`starts_with`, `ends_with`, `index_of`, `substring`, `repeat` — are
ordinary Gyro in the standard library, written on `Array<u8>`, `bytes()`
and one intrinsic, `from_utf8(bytes: u8[]): string`. A bundled module is
only a declaration stub where a declaration has no body. `join(parts,
sep)` stays a function: its receiver would be the slice, not a string.

Known cost, accepted: `from_utf8` and `text()` copy, so building text
allocates, and nothing frees those allocations yet.

`Map<K, V>` is the hash map, ordinary Gyro over `Array<T>` in the same
spirit: buckets of `(K, V)` pairs chained on the derived `hash()`. Any
key that supports `==` works, because `hash()` is derived wherever `==`
is and over the same parts. `insert` replaces on an equal key, `get`
answers `V?`, `remove` reports whether the key was present, and `keys()`
snapshots into a fresh `Array<K>` in deterministic bucket order. Like
`Array<T>`, a map is freed explicitly with `free()`.

A `V` that is itself optional is allowed. Optionals do not nest, so
`get()` on a `Map<K, V?>` answers `null` both for a miss and for a stored
`null`; `contains(key)` is the signal that separates them.

```gyro
var ages = Map<string, i32>::new()
defer ages.free()
ages.insert("ada", 36)
var age = ages.get("ada")
if age != null {
  println(age)
}
```

Known cost, accepted: a slice can outlive the memory behind it, and nothing
currently detects that.

### Equality

`==` and `!=` work on primitives, pointers (identity), tuples and enums, and
on structs as compiler-derived field-wise comparison. Slices do not compare
with `==` (compare contents explicitly).

`hash(): u64` is derived wherever `==` is, and over the same parts: a
struct hashes its fields, a tuple its elements, an enum its tag and
payload. Two values that compare equal therefore hash equal, without
anything being written down. A type that wants a different rule declares
its own `hash`, which displaces the derived one the way `to_string`
displaces derived printing — and then it owns keeping the two agreed.
Pointers are the one thing `==` accepts and `hash` does not: `==` on a
pointer is identity, and an address is not the same from run to run.

Comparison operators `< <= > >=`
work on numeric primitives and `rune` only; ordering anything else is
spelled `a.compare(b)`, which returns -1, 0 or 1. `string` carries one,
ordering lexicographically by byte — the order its code points are in —
and a user type orders by writing its own.

---

## Statements

### Control flow

Conditions take no parentheses; braces are always required.

```gyro
if x < 0 {
} elif x == 0 {
} else {
}

while i * i <= n {
  i += 1
}

for var i = 0; i < 10; i += 1 {}
for value in 0..10 {}
for value in collection {}
```

`elif` is sugar and desugars to a nested `if`, leaving no trace in the tree.
It is the only way to chain conditions; `else` takes a block, so there is
exactly one spelling.

Because a condition is followed by `{`, a bare struct literal cannot appear
at the top level of a condition — `if Point { }` reads `Point` as a value.
Parenthesize to disambiguate: `if (Point{ x: 1 }).x > 0 { }`.

### Ranges and iteration

`a..b` is a half-open range: `0..10` visits 0 through 9, so `0..xs.len()`
visits every index. There is no inclusive form; an inclusive walk is a
C-style `for` header.

`for x in ...` iterates slices, fixed-size arrays, ranges and channels
natively. Any other type is iterable by duck typing: a type with
`next(*self): (T, bool)` — or an `iter()` returning such a type — works in
`for..in`. `false` ends the loop.

```gyro
fn Counter::next(*self): (i32, bool) {
  if self.n >= self.limit {
    return 0, false
  }
  self.n += 1
  return self.n, true
}

for x in (Counter{ 0, 5 }) { println(x) }
```

The parentheses are the same rule as conditions: a brace after the iterable
opens the loop body, so a bare struct literal cannot sit at the top level
of a `for..in` head.

```gyro
```

### Match

`match` is an expression. Arms are `pattern: body`, one per line — an arm
ends at the newline, like every other statement. A body may be an
expression, a block, or a control-flow statement.

```gyro
var name = match n {
  0: "zero"
  1: "one"
  _: "other"
}
```

Patterns are `_`, literals, bindings, enum variant destructuring, and
struct type-tests:

- `Shape::Circle(r)` destructures a variant, binding its payload. Variant
  patterns are **always qualified** with the enum name.
- `RangeError(e)` on a value whose concrete type is not statically known (an
  `error`) is a **type test**: it matches when the value is a `RangeError`
  and binds the whole struct to `e`. An `any` subject takes the same form
  and any type may name itself there, primitives included: `i32(n)`,
  `string(s)`, `Point(p)`. A type no path can spell is written as itself:
  `(i32, string)(pair)`, `i32[](xs)`, `*Node(node)`.

A match over an enum is checked for **exhaustiveness**: every variant must be
covered or a `_` arm present; adding a variant later breaks every non-`_`
match loudly, which is the point. A plain binding closes a match the same way
`_` does, since it matches whatever is left.

An arm covers a variant only when its payload patterns *bind* rather than
*test*. `Shape::Circle(r)` covers `Circle`; `Shape::Circle(0.0)` matches one
radius, so `Circle` stays uncovered and the match needs a further arm.

Only enums are enumerable. A match on an integer, a string, an `any`, or an
`error` whose concrete type is unknown is never reported as partial — there is no
finite variant set to compare against. Falling off the end of such a match
panics at runtime.

An arm body is delimited by the newline that ends it, so a `return` inside an
arm yields at most one value; returning several needs a block.

---

## Errors

Errors are ordinary values. A fallible function returns a `(T, error?)` tuple
and the caller has to handle it — there is no propagation operator, no
exceptions and no unwinding.

`error` is a built-in contract, satisfied by duck typing: **any type with a
`message(): string` method is an error.** Nothing is declared; having the
method is the whole test. A library defines its own error type carrying its
own data and callers match on the concrete kind.

```gyro
export struct RangeError {
  export value: i32
}

fn RangeError::message(self): string {
  return "out of range"
}

fn parse_port(text: string): (i32, error?) {
  if bad {
    return 0, RangeError{ value: 70000 }
  }
  return value, null
}

match err {
  RangeError(e): report(e.value)
  _: println(err.message())
}
```

An `error` value whose concrete type is unknown at the call site is stored as
a vtable pointer plus a fixed inline buffer. Small types travel inside it
with no allocation; a type larger than the cap is a compile error naming the
size, and must be boxed explicitly.

Discarding an error requires writing `_`, so ignoring one is always visible
in the source.

### Programmer errors

Out-of-bounds indexing, integer divide by zero and dereferencing a null
`T?` are bugs, not conditions. They print a message with source position and
**abort**. They are neither returnable nor catchable, which keeps the
`(T, error?)` channel meaningful and needs no unwinding machinery.

```
panic: index 7 out of range for length 3
  at main.gyro:12:11
```

Integer overflow is in the same category: `a + b` panics. Wrapping is
available where it is actually wanted, spelled `+%`, `-%` and `*%`. A
narrowing `as` conversion that loses value panics the same way. The
bitwise operators are not in this category at all — losing bits is what
they are for, so `<<` never panics and needs no wrapping twin.

---

## Printing

Every type is printable: `println(x)` and `#{x}` use a compiler-derived
representation (`Point{ x: 1, y: 2 }`) unless the type has its own
`to_string(): string` method, which then wins. Primitives print natively.

An `any` prints as the value inside it, so a `...any` pack prints element by
element with no unwrapping. `printf` in the standard library is an ordinary
Gyro function over such a pack.

---

## Memory

Memory is manual, with a compiler-checked safety net.

- `alloc<T>()` returns `*T`, `free(p)` releases it. Both are imported.
- `&x` takes an address, `*p` dereferences.
- `defer expr` runs a call at **block exit** — when the enclosing `{ }` ends,
  in reverse order of registration. A loop body's defers run every iteration.
- A sema pass tracks each allocation and **warns** when one can reach the end
  of its block with nothing having freed or taken it. Warnings, not errors,
  so arena and process-lifetime patterns are not blocked.

```gyro
fn f(): i32 {
  var node = alloc<Node>()
  defer free(node)
  return node.value
}
```

An allocation is settled by `free`, and equally by anything that hands it
on: returning it, sending it, storing it in a struct or array literal,
passing it to a call, or moving it into a closure with `fn[move node]`. Only
a bare name counts — `node.value` reads through the pointer and leaves
ownership where it was.

Freeing on one path is not freeing. A `return` is an exit the check sees, so
an allocation the return does not carry with it is reported there rather than
at the closing brace:

```gyro
fn g(cond: bool) {
  var node = alloc<Node>()   // warns: the early return loses it
  if cond {
    return
  }
  free(node)
}
```

A pointer that may be absent is `*T?`, so a linked list's tail is
`next: *Node?` and the null case has to be checked before use.

---

## Foreign functions

A declaration with no body is a foreign function — that is already how the
standard library's intrinsics are written. `@extern` says which foreign
world one belongs to, and the library it is linked from:

```gyro
@extern("m")
export fn sqrt(x: f64): f64 !{}

@extern("SDL2")
export fn SDL_Init(flags: u32): i32 !{ io }

@extern("SDL2", symbol = "SDL_Delay")
export fn delay(millis: u32) !{ io }
```

The string is the library, spelled as the linker wants it without the `lib`
prefix — `"m"` is `-lm`, `"SDL2"` is `-lSDL2`. `"c"` names libc, which is
always linked anyway. The symbol is the declaration's own name unless
`symbol =` gives another, which is how a C name that is not a legal Gyro
identifier, or one you would rather not repeat at every call, gets in.

A declaration under `@extern` **must have no body** and **must state its
effect row**: there is no source to read, so an unstated row would be
assumed to do everything.

### What can cross

The rule is the one METALS already enforces on every language boundary:
**values copy at the boundary**, so nothing shared and mutable crosses.

| Gyro | C | how |
| --- | --- | --- |
| `i8`..`u64`, `usize`, `f32`, `f64`, `bool`, `rune` | the same width | by value |
| `string` | `const char *` | copied and NUL-terminated for the call |
| `T[]` | `const T *` | the slice's own elements, borrowed for the call |
| a struct whose fields all cross | the same struct | by value; the same bytes on both sides |
| a fieldless `@extern` struct | `void *` | an opaque handle, by identity |
| `T?` of a handle | a pointer that may be `NULL` | null is `NULL` |
| `void` | `void` | |

A returned `string` is a copy of what the pointer addressed, because the
bytes belong to the library and it may reuse them on the next call. A
returned null pointer becomes `""`: a Gyro string is never absent, and
`string?` is not a type that crosses.

A struct crosses by value, in both directions, because Gyro lays one out
the way C does — same widths, same alignment, same padding. So a Gyro
struct and a C struct of the same fields are the same bytes and nothing
is converted:

```gyro
struct Div {
  quot: i32
  rem: i32
}

@extern("c")
fn div(numerator: i32, denominator: i32): Div !{}
```

What a struct may hold is narrower than what an argument may be, because
a field is bytes C reads out of the struct rather than a value converted
for the call. Scalars, fixed arrays and other such structs are members; a
`string`, a slice and a nullable are not, and the diagnostic names the
field's type rather than the struct's.

Rejected, with a diagnostic naming the type: `*T`, `chan<T>`, futures,
function values, `any`, tuples, enums, and `T?` of anything but a handle.
Several results are rejected too — a C function returns one value, and
`(T, error?)` is Gyro's shape, not C's.

The one copy left is what C's own representation forces, not a
convenience: a Gyro string is a length-prefixed header and C wants
NUL-terminated bytes, so the call passes a copy that lives only as long
as it does. A slice is passed as it stands. A foreign function that keeps
either pointer keeps a dangling one.

An opaque handle is a fieldless struct marked `@extern`. It has no
literal, no fields and no operations of its own — it is a `void *` you can
only pass back to the library that gave it to you:

```gyro
@extern("SDL2")
export struct Window {}

@extern("SDL2")
export fn SDL_CreateWindow(
  title: string, x: i32, y: i32, w: i32, h: i32, flags: u32,
): Window? !{ io, alloc }
```

### What it costs

A program that calls a foreign function **runs only on the native tiers**.
The METALS evaluator is Go with no C in it, so it cannot make the call,
and it refuses the program rather than inventing an answer.

---

## Concurrency

Three separate mechanisms answering three separate questions.

### Threads — parallelism

`spawn` starts a call on another thread. It is an ordinary stdlib function
taking a closure, not grammar — the closure is what carries the arguments
across:

```gyro
import { spawn } from "stdlib"

spawn(fn() produce(values, 10))
```

`@concurrent(threads = N)` configures a parallel region. `Mutex` and friends
come from the standard library. `Mutex::new(0)` constructs one and `lock()`
returns the guard directly — there is no poisoning, because a Gyro panic
aborts the process rather than unwinding past a held lock. Releasing is
explicit: `guard.unlock()`, usually as `defer guard.unlock()`. (Gyro has no
destructors, so nothing can release a guard implicitly; if a drop-based
design ever lands, the explicit call becomes sugar.)

A closure captures by copy, which is safe for a value and not for a
reference: copying a `*T` copies the pointer, and copying a `T[]` copies a
pointer and a length over storage it does not own. Both threads then reach
the same memory. Handing a reference to `spawn` that way **warns**:

```gyro
var node = alloc<Node>()
spawn(fn() { node.value = 1 })      // warns: both threads reach node

spawn(fn[move node]() { ... })      // fine: the binding goes with it
spawn(fn() { ch <- 1 })             // fine: a channel is built for this
```

A channel capture is never reported, since carrying work between threads is
what a channel is for, and a `move` capture hands the binding over rather
than sharing it. A closure that is not spawned is not reported either.

This does not prove the absence of a race. A pointer reached through a struct
field, and two spawns over one channel, are both invisible to it; so is
anything the callee of a captured function does. It catches the direct case.

### Channels — communication

```gyro
var ch = chan<i32>(64)   // buffered; chan<i32>() is unbuffered
ch <- value              // send, always a statement
var v = <-ch             // receive
var v, open = <-ch       // receive, reporting whether the channel is open
for v in ch {}           // drain until closed
ch.close()
```

Reading `v` when `open` came back `false` is a definite-assignment error —
a closed channel yields no value, not a zero value. The flag is what makes
the value readable, and the check follows `!` and `&&` the same way null
narrowing does:

```gyro
var v, open = <-ch
if open {
  println(v)      // fine
}

var w, more = <-ch
if !more {
  return
}
println(w)        // fine: the branch that would contradict it returned
```

`select` waits on several operations and runs the arm that becomes ready
first. Arms are `comm: body`, one per line, like match. Bindings are
ordinary `var` declarations — there is no `:=`.

```gyro
select {
  var value = <-values: use(value)
  quit <- true: return
  else: idle()            // makes the select non-blocking
}
```

When more than one arm is ready at the same moment, **which one runs is
unspecified**. A program that depends on the choice is wrong: the tiers
differ deliberately, one picking at random and another polling in source
order, and neither is the definition.

### Async — IO concurrency

`async fn` declares an asynchronous function and `await` resolves the value
of a call to one. Async is a function colour, as in Rust and JavaScript, and
it appears in the type: `async fn(): i32` is the type of an async closure.

```gyro
export async fn fetch_all(urls: string[]): (Array<string>, error?) {
  var body, err = await get(urls[0])
}
```

---

## Effect rows

Signatures may carry an effect row describing what the function does and
what its result borrows. Both kinds of entry are checked: the concrete
effects `io` and `alloc` against what the body performs, and the borrow
entries against what the result views.

```gyro
fn add(a: i32, b: i32): i32                       // no row: unconstrained
export fn log(msg: string) !{ io }
export fn load(p: string): (string, error?) !{ io, alloc }
fn map<T, U>(xs: T[], f: fn(T): U !e): U[] !e     // effect-polymorphic
@gpu fn kernel(a: *f32) !{}                       // empty row
```

Forms: `!{}`, `!{ a, b }`, `!e` for a bare row variable, and `!{ a, ..e }`
for concrete effects plus a row variable.

The decorators are the motivation: `@comptime` needs purity, `@gpu` forbids
allocation and syscalls, and `@distributed` restricts what may cross a node
boundary. Today each of those is a bespoke check; effect rows are how they
become one mechanism.

### Concrete effects

**A declared row is a complete statement.** A function that writes any row
— even one holding only borrows — promises that its body performs no
concrete effect the row does not name: a row without `io` promises no io,
and a row without `alloc` promises no allocation. A function with no row is
unconstrained, exported or not: a row is how a signature *states* an
interface, never the only way to know what a function does.

The vocabulary is closed. `io` and `alloc` are the concrete effects and
`&name` is a borrow entry; anything else in a row is an error rather than
an entry that quietly promises nothing.

```gyro
fn quiet(n: i32) !{} {
  println(n)         // error: println does io, which the row does not name
}

fn loud(n: i32) !{ io, alloc } {
  println("n = #{n}")   // fine: the row names both effects
}
```

What a body performs is judged from its sources:

- A call contributes the callee's declared row when it states one. When the
  callee states no row, its effects are inferred from its body — in
  whatever module that body lives, transitively. Gyro compiles
  whole-program, so a rowless import is read, not assumed.
- An **intrinsic** — a declaration with no body anywhere — is the one thing
  that cannot be read. Without a row it is assumed to do `io` and `alloc`,
  which is why every intrinsic in the standard library writes one, down to
  `Array<T>::get(self, index: usize): T !{}`.
- String interpolation `#{...}` builds new text, which allocates.
- The built-in method surface answers for itself: `runes()` and `bytes()`
  copy into new storage and so do `alloc`; `len`, `close`, `hash`,
  `compare` and `message` do nothing.
- The standard library states rows on its surface: `println` does
  `!{ io }`, `Array<T>::push` does `!{ alloc }` (it may grow the buffer),
  `read_all` does `!{ io, alloc }`, and so on.

A generic body's effects are judged per instantiation, like its borrows:
what a call on a type parameter reaches is only known once the parameter is
concrete. A rowless generic callee is read through its instantiations and
contributes the union of what they reach.

A closure may write its own row, and its body is checked against it:

```gyro
var quiet = fn(n: i32) !{} {
  println(n)           // error: println does io, which the row does not name
}
```

Outside v1, deliberately: a row variable (`!e`, `!{ io, ..e }`) leaves the
row open and unchecked; *calling* a function value — a closure or an `fn`
parameter — contributes nothing to the caller, and creating a capturing
closure is not counted as `alloc` even though the environment is real
storage; module-level `var` initializers run before `main` outside any row;
and channel operations and `Mutex` carry no effect. `spawn` declares
`!{ io, alloc }`, which covers starting the thread and stands in for the
task's own effects until a row can travel with a function value.

### Borrows

An entry written `&name` says the result is a view into that parameter.
`T[]` and `string` borrow storage they do not own, so a function returning
one has to say where it came from:

```gyro
fn first(xs: i32[]): i32[] !{ &xs }
fn pick(a: i32[], b: i32[]): i32[] !{ &a }
fn spans(a: i32[], b: i32[]): i32[] !{ &a, &b }
fn head(s: string): string !{ io, &s }        // beside an effect
fn make(): i32[]                              // no &: owns its result
```

`&` reads the same way it does everywhere else — a reference to the thing
named. Each source carries its own `&`, so borrows and effect names never
run together in one list.

**This is always written out.** There is no elision: a row without any `&`
entry says the result is a view into nothing.

```gyro
fn good(xs: i32[]): i32[] !{ &xs } {
  return xs        // fine: the row says the result views xs
}

fn bad(xs: i32[]): i32[] {
  return xs        // error: the row names no source
}

fn worse(): i32[] {
  var xs = [1, 2, 3]
  return xs        // error: that storage ends with this function
}
```

Because a view never owns, **a function returning `T[]` or `string` needs a
`&` entry in practice.** There is no way to hand back storage through one; a
function that means to give the caller ownership returns `Array<T>`, or
`String` rather than `string`. The exception is a view of something static,
like a string literal, which no frame owns.

This reaches further than it first looks. `string` is a view, so an accessor
handing back a string field borrows the struct it came from:

```gyro
fn host(c: Config): string !{ &c } {
  return c.host
}
```

That is not a quirk of the checker — the caller really does have to keep `c`
alive to use the result. It is the price of `string` being a borrowed view
and of borrows being written out rather than inferred.

The name must be a parameter of the function, and that parameter has to be a
view itself — there is nothing to borrow from an `i32`. A borrow entry on a
result that is not a view is an error rather than a no-op.

A row is read at call sites, not only where it is written, so what a call
views reaches the caller — across a module boundary as well:

```gyro
fn pick(a: i32[], b: i32[]): i32[] !{ &a } { return a }

fn caller(xs: i32[], ys: i32[]): i32[] !{ &xs } {
  return pick(xs, ys)      // views xs, because pick's row says so
}

fn bad(ys: i32[]): i32[] !{ &ys } {
  var local = [1, 2, 3]
  return pick(local, ys)   // error: the result views this frame
}
```

Frame storage travels through a call the same way. A caller has to account
for every source the callee names: `both(xs, ys)` with `!{ &a, &b }` on
`both` needs `!{ &xs, &ys }`.

### Receivers and fields

`&self` names a method's receiver, and a field chain roots at whatever holds
it — `self.items` is the receiver's storage, `s.items` is `s`'s:

```gyro
export fn Stack::view(self): i32[] !{ &self } {
  return self.items.slice()
}

fn contents(s: Stack): i32[] !{ &s } {
  return s.items.slice()
}
```

A struct carries what its fields carry, so frame storage behind a field is
still the frame's:

```gyro
struct View { data: i32[] }

fn bad(): i32[] {
  var xs = [1, 2, 3]
  var v = View{ data: xs }
  return v.data          // error: xs ends with this function
}
```

A heap buffer does not — `Stack{ items: Array<i32>::new() }` holds storage
that outlives the frame, and lending a view of it is fine.

### Borrows on a generic

A generic row is **conditional**: load-bearing where `T` turns out to be a
view, vacuous where it does not. It is stated once and serves every
instantiation.

```gyro
fn pick<T>(a: T, b: T, f: bool): T !{ &a, &b } {
  if f {
    return a
  }
  return b
}

pick(1, 2, true)        // vacuous: i32 is not a view
pick("a", "b", false)   // checked: string is
```

The cost is that a generic returning one of its parameters needs the row even
where it never means anything — `fn identity<T>(x: T): T !{ &x }`. The
alternative was inferring generic rows from the body, which would make the
one place the annotation is hardest to read also the one place it is invisible.

---

## Implemented and not

The revised syntax is fully parsed: free methods, tuples and `.0` access,
`as`, newline-terminated arms, `move`-only capture lists, leading-operator
continuation, and the removals (`impl`, `trait`, bounds, `++`/`--`, arm
commas) all error with pointers at the current spelling.

Sema resolves every name under lexical scoping — no universe scope, so an
unresolved name is an error — and **types every expression**:

- Literals are context-typed with i32/f64 defaults; constant expressions
  fold, so `var x: u8 = 200 + 56` is caught at compile time. There are no
  implicit conversions between typed values, anywhere.
- Operators enforce matching operand types; `+%`-style wrapping and the
  bitwise operators are integer-only; `<` on a user type points at
  `a.compare(b)`; slices refuse `==`.
- A nullable value cannot be dereferenced, selected through, indexed or
  iterated until a null check **narrows** it. The checker follows
  `!= null` / `== null` through `&&`, `!`, `else`, and early exits
  (`if x == null { return }` leaves x narrowed); assignment un-narrows.
- Generics are checked by **instantiation**: a generic body is re-checked
  for every distinct set of type arguments, which is where `max(m1, m2)`
  fails with "Mutex has no method compare" plus a pointer at the call
  site. Owner arguments infer through associated functions
  (`Mutex::new(0)` is a `Mutex<i32>`), struct literals infer from their
  fields (`Pair{ 1, 2 }`), instances are canonical, and instantiation is
  cached and depth-capped.
- Struct literals must set every field and are only legal where every
  field is visible; cross-module use of a private field is an error.
- Dropping an error result is an error; discarding takes an explicit
  `var _, _ =`. An un-awaited async call in statement position is too.
- Built-in types carry a small fixed method surface, no import needed:
  `ch.close()`, `xs.len(): usize`, `s.len()`, `s.runes(): rune[]`,
  `a.compare(b)` on the numeric primitives, `rune` and `string`,
  `v.hash(): u64` on those and on tuples, structs and enums of them,
  `err.message()`.
- `for..in` handles slices, arrays, ranges, channels, and the duck-typed
  `next(*self): (T, bool)` / `iter()` contract.
- A match over an enum is **exhaustive**: the missing variants are named
  at the match, and a payload pattern that tests rather than binds leaves
  its variant uncovered. Open subjects — integers, strings, `error` — are
  exempt.
- **Spawn races** warn: a closure handed to `spawn` that captures a `*T`
  or a `T[]` shares it with the thread that spawned it, since captures
  copy. Channels and `move` captures are exempt.
- **Leak analysis** warns when an `alloc<T>()` reaches the end of its
  block with nothing having freed or taken it. `free`, a return, a send, a
  call argument, a literal field and a `move` capture all settle it; a
  `return` that does not carry it is reported at the return.
- **Definite assignment** runs: a `var x: i32` with no initializer is
  tracked until every path to a read has written it. Branches merge, a
  branch that cannot fall through is ignored, loops assign nothing unless
  `break` is their only exit, and `match`/`select` merge their arms. `&x`
  counts as a write; a closure reads the state where it is written and
  its own writes stay inside.

The **module loader** is real: `gyro check` loads the whole import graph,
`./x` resolves beside the importing file, a bare name comes from the
declaration-only stub files bundled into the compiler, cycles are rejected
with a trace, and an import of a name the target module does not export is
an error. A bare name that is not bundled errors until the Stack package
manager exists.

**Programs run**: `gyro run file.gyro` checks a program, lowers it to
METALS and evaluates it; the same command takes a `.metals`, `.lisp`,
`.c` or `.wasm` module, because what runs is METALS either way. The
runtime semantics this document pins, and every tier must produce:
overflow/underflow panics per width, wrapping via `+% -% *%`, truncating
division with `-7 / 2 == -3`, narrowing `as` panics, bounds and null and
uninitialized-read panics with source positions, defer at block exit in
reverse (per loop iteration included, with arguments captured at
registration), capture-by-copy closures, value-semantics structs, derived
printing with `to_string` override, spawn/channels/select on real threads,
async as futures. The stdlib stubs have native bodies: `println`,
`Array<T>`, `Mutex` (explicit `guard.unlock()`), `alloc`/`free` with
use-after-free and double-free detection, `fs`, `convert`, `time` (wall
and monotonic clocks, `sleep`), and `os` (`env`, `read_stdin`, `exit`).

**Programs lower**: `gyro metals file.gyro` compiles a checked program
into one self-contained METALS module — monomorphized generics under
quoted names (`@"Pair<i32>::sum"`), lifted closures with heap
environment records, defers inlined at every exit, match as tag tests,
narrowing as explicit `opt.unwrap`, `error` as a closed program-wide
enum, stdlib natives as `gyro:rt` imports. The module verifies, and
`internal/meval` — a METALS evaluator — executes it. Module-level `const`
lowers to a synthesized getter; module-level `var` lowers to METALS
storage, zero-initialized, with the initializers run before main's own
body in declaration order. Derived printing is synthesized here rather
than in each backend: one function per composite type, which every tier
reaches as an ordinary call.

**Programs compile**: `gyro build file.gyro -o prog` takes the verified
module to a native Linux x86-64 executable — NASM assembly linked with
a small C runtime ([`native.md`](native.md)); channels, `spawn` and
`async` run on OS threads. `gyro nasm file.gyro` prints the assembly.

**The three tiers are one corpus.** `tests/testdata/run` holds programs
paired with the output they must produce, and every case runs through the
evaluator and both native backends, all held to the same golden file —
which is written by hand, because this document is the specification and
no tier is. A gap is a case carrying a `//gyro:skip <tier>` with its
reason attached. The checker has the same arrangement in
`internal/sema/testdata/check`, where a fixture claims each diagnostic
with a `//~` marker on its line.

Still to come:

| feature | state |
| --- | --- |
| mutation rules | `*self` vs `self`, assigning through a value receiver — unchecked |
| `@comptime` evaluation | typed, not evaluated |
| foreign functions | `@extern`: primitives, `string`, slices, structs by value and opaque handles cross; callbacks and out-parameters do not, and a program that calls one runs on the native tiers only |
| concrete effects (`io`, `alloc`) | checked against declared rows; unstated rows, row variables, function values and spawn/channel effects remain unchecked |
| module resolution | done; `.stack/` packages await the Stack package manager |
| `->` `=>` tokens | reserved, unused |
| `syntaxfn` parser extensions | designed in the book, not parsed |
| use-after-free and uninitialized reads | caught by the evaluator; the C runtime frees for real and does not poison, so a native binary misses both |
| METALS | specified and produced: [`metals.md`](metals.md); `internal/metals` (format, verifier), `internal/lower` (Gyro→METALS), `internal/meval` (evaluator) |
| native backend | v1 done: [`native.md`](native.md); `internal/nasm` emits NASM and links a C runtime; still open: register allocation, memory reclamation, green threads |
| editor tooling | v1 done: [`tooling.md`](tooling.md); tree-sitter grammars for Gyro and METALS, `gyro lsp` with diagnostics, hover, definition, symbols, completion |

See [`decisions.md`](decisions.md) for why each of these is the way it is,
and what was rejected.
