# Gyro language reference

This file is the source of truth for Gyro's surface syntax. It replaces the
old `gyro.peggy` grammar, which was abandoned mid-design and did not describe
the language as it now stands.

Everything below is implemented by the parser unless it is marked
**reserved**, which means the syntax is accepted and stored in the tree but no
later stage checks it yet.

---

## Lexical structure

### Statement termination

Gyro has no statement-terminating semicolons in normal source. The lexer
inserts a terminator at a newline when the preceding token could end a
statement — an identifier, a literal, `)`, `]`, `}`, `++`, `--`, or one of
`return`, `break`, `continue`, `self`, `true`, `false`, `null`, `_`.

A newline does *not* terminate the statement when the next line begins with
`.`, `,`, `)` or `]`, so method chains and multi-line argument lists work
without escapes:

```gyro
var total = values
  .filter(is_even)
  .sum()
```

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
```

Strings interpolate with `#{expr}`, where the expression is arbitrary Gyro.
Escape it as `\#{` to write the sequence literally. Escapes are `\n`, `\t`,
`\r`, `\0`, `\\`, `\'`, `\"`, `\#`, `\xNN` and `\u{...}`.

### Operators

| class | operators |
| --- | --- |
| arithmetic | `+ - * / %` — these panic on overflow |
| wrapping | `+% -% *%` — deliberate two's-complement wraparound |
| increment | `++ --` (prefix and postfix, usable in expressions) |
| comparison | `== != < <= > >=` |
| logical | `&& \|\| !` |
| bitwise | `& \| ^ ~ << >>` |
| assignment | `= += -= *= /= %= &= \|= ^= <<= >>=` |
| access | `.` field, `::` namespace, `[]` index |
| type | `?` nullable, `&` joins trait bounds |
| other | `..` range, `@` decorator, `<-` channel, `!{}` effect row |

`>>` is deliberately scanned as two `>` tokens so that nested generic
arguments such as `Result<Vec<i32>, error>` close cleanly; the parser rejoins
them when a shift is meant.

`<-` is scanned greedily. That makes `x<-1` ambiguous with a comparison
against a negative number, so the parser splits the token back apart whenever
it appears in binary-operator position. Written with a space, `x < -1` is
never ambiguous in the first place.

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
import { println } from "@gyro/stdlib"
import { open as fopen } from "@gyro/fs"
import * as ast from "@gyro/ast"
import "./side-effects"
```

One file is one module. `./util` resolves to `util.gyro` beside the importing
file, `@gyro/...` is the bundled standard library, and a bare name is a
package under `.stack/`. Circular imports are rejected.

There is **no default export**, and therefore no `import name from "path"`.
A name is spelled the same where it is defined and everywhere it is used.

**If a name is not part of the formal syntax, it has to be imported.** There
is no universe scope and no predeclared functions: operations on built-in
types are methods on those types, so `ch.close()`, `xs.len()` and
`xs.append(v)` need no import, while `println` does.

### Variables

```gyro
var x: i32 = 0
var y = 42            // inferred
const MAX: i32 = 100  // compile-time constant
var value, err = f()  // multi-value binding
var _, err = f()      // `_` discards explicitly
```

### Functions

```gyro
fn add(a: i32, b: i32): i32 {
  return a + b
}

fn nothing() {}                      // no annotation means no result
fn load(p: string): (string, error)  // multiple results
fn max<T>(a: T, b: T): T             // generic
async fn fetch(u: string): (string, error)
fn apply(f: fn(i32): bool) {}        // function-valued parameter
```

The return type follows a colon. Parameters may carry defaults:
`fn f(n: i32 = 4)`.

### Closures

A closure is an anonymous function, written with the same `fn` and the same
`:` result marker. There is one spelling of "function" in the language.

```gyro
var double = fn(a: i32): i32 { return a * 2 }
var greet  = fn() { println("hi") }
```

Two shorthands apply where they are unambiguous. Parameter and result types
may be omitted when the expected function type is known from context, and a
body consisting of one expression needs neither braces nor `return`:

```gyro
xs.map(fn(x) x * 2)
xs.filter(fn(x) x > 0)
```

A closure states what it captures. `copy` duplicates the binding, `move`
transfers ownership into the closure, and a bare name means `copy`. Being
explicit is what lets the leak analysis see an allocation escape:

```gyro
var scale = fn[copy factor](x: i32): i32 { return x * factor }

var node = alloc<Node>()
var release = fn[move node]() { free(node) }   // ownership moves in
```

`move` and `copy` are contextual — they are ordinary identifiers everywhere
else.

The type a closure inhabits is the function type that already exists:
`fn(i32): i32`. In type position `fn(A): B` is a type; in expression position
`fn(a: A): B { ... }` is a value. The two never collide.

### Structs and impl blocks

Data and behaviour are declared separately.

```gyro
export struct Stack {
  export capacity: i32
  top: i32
}

impl Stack {
  export fn new(capacity: i32): Stack {
    return Stack{ capacity: capacity, top: 0 }
  }

  fn is_empty(self): bool { return self.top == 0 }
  fn push(*self, v: i32) { self.top++ }
}
```

`self` borrows the receiver immutably and never copies it, however large the
struct is. `*self` takes a pointer and may mutate. A function in an impl
block with no receiver is an associated function, called as `Stack::new(16)`;
for a generic type that is `Array<i32>::new()`.

A struct literal is only legal where every field is visible, so a type with
private fields is constructed exclusively through its own associated
functions. That is what makes invariants enforceable.

Struct literals come in two forms, named and positional. A literal is one or
the other, never a mix:

```gyro
Point{ x: 1, y: 2 }   // named
Mutex{ 0 }            // positional, in declaration order
```

### Enums

```gyro
export enum Shape {
  Circle(f64),
  Rect(f64, f64),
  Point,
}
```

### Traits

A trait is a set of method signatures a type can promise to provide.
Satisfaction is **nominal**: a type implements a trait only where an `impl`
says so, never by accident. `Self` refers to the implementing type.

```gyro
export trait Ord {
  fn compare(self, other: Self): i32
}

impl Ord for Version {
  fn compare(self, other: Version): i32 { ... }
}
```

Generics constrain with bounds. Several bounds are an intersection, so they
are joined with `&`:

```gyro
fn max<T: Ord>(a: T, b: T): T { ... }
fn describe<T: Ord & Display>(a: T, b: T): string { ... }
```

Operators reach traits through a fixed, closed set — nothing else is
overloadable:

| operators | trait |
| --- | --- |
| `< > <= >=` | `Ord` |
| `== !=` | `Eq` |
| `+ - * /` | `Add` `Sub` `Mul` `Div` |
| `[]` | `Index` |

A value whose concrete type is not known at the call site — such as an
`error` — is a trait object: a vtable pointer plus a fixed inline buffer.
Small types travel inside it with no allocation. A type larger than the cap
is a compile error naming the size, and must be boxed explicitly.

### Decorators

`@name` or `@name(args)`, attachable to any declaration. Arguments may be
named.

```gyro
@comptime const FIB_10: i32 = fib(10)
@gpu fn kernel(a: *f32) {}
@concurrent(threads = 8) fn work() {}
```

---

## Types

| form | meaning |
| --- | --- |
| `i8 i16 i32 i64` | signed integers |
| `u8 u16 u32 u64` | unsigned integers |
| `f32 f64` | floats |
| `bool string void error` | other primitives |
| `*T` | pointer, never null |
| `T?` | nullable |
| `T[]` | slice — a borrowed view |
| `T[N]` | fixed-size array |
| `chan<T>` | channel |
| `(A, B)` | multi-value return |
| `fn(A, B): C` | function |
| `ast::Literal` | namespaced name |
| `Result<T, E>` | generic instantiation |

### Nullability

A `*T` can never be null; only a `T?` can. The compiler refuses to dereference
a nullable value until it has been checked, and narrows the type inside the
check.

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

`string` is the same arrangement over UTF-8 bytes: a borrowed view, with
`String` as the owning builder. `len()` counts bytes and `runes()` iterates
code points.

Known cost, accepted: a slice can outlive the memory behind it, and nothing
currently detects that.

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
  i++
}

for var i = 0; i < 10; i++ {}
for value in 0..10 {}
for value in collection {}
```

`elif` is sugar and desugars to a nested `if`, leaving no trace in the tree.
It is the only way to chain conditions: **`else if` is rejected**, so there is
exactly one spelling.

Because a condition is followed by `{`, a bare struct literal cannot appear
at the top level of a condition — `if Point { }` reads `Point` as a value.
Parenthesize to disambiguate: `if (Point{ x: 1 }).x > 0 { }`.

### Match

`match` is an expression. Arms are `pattern: body`, separated by commas, and
a body may be an expression, a block, or a control-flow statement.

```gyro
var name = match n {
  0: "zero",
  1: "one",
  _: "other",
}

var guard = match m.lock() {
  Ok(g): g,
  Err(poisoned): poisoned.into_inner(),
}
```

Patterns are `_`, literals, bindings, and variant destructuring such as
`Ok(value)` or `Shape::Circle(r)`.

An arm body is delimited by the comma that ends it, so a `return` inside an
arm yields at most one value; returning several needs a block.

---

## Errors

Errors are ordinary values. A fallible function returns a `(T, error?)` pair
and the caller has to handle it — there is no propagation operator, no
exceptions and no unwinding.

`error` is a built-in **interface**: any type implementing `message(): string`
is an error, so a library defines its own error type carrying its own data and
callers match on the concrete kind.

```gyro
export struct RangeError {
  export value: i32
}

impl error for RangeError {
  fn message(self): string { return "out of range" }
}

fn parse_port(text: string): (i32, error?) {
  if bad {
    return 0, RangeError{ value: 70000 }
  }
  return value, null
}

match err {
  RangeError(e): report(e.value),
  _: println(err.message()),
}
```

Discarding an error requires writing `_`, so ignoring one is always visible
in the source.

### Programmer errors

Out-of-bounds indexing, divide by zero and dereferencing a null `T?` are bugs,
not conditions. They print a message with source position and **abort**. They
are neither returnable nor catchable, which keeps the `(T, error?)` channel
meaningful and needs no unwinding machinery.

```
panic: index 7 out of range for length 3
  at main.gyro:12:11
```

Integer overflow is in the same category: `a + b` panics. Wrapping is
available where it is actually wanted, spelled `+%`, `-%` and `*%`.

---

## Memory

Memory is manual, with a compiler-checked safety net.

- `alloc<T>()` returns `*T`, `free(p)` releases it. Both are imported.
- `&x` takes an address, `*p` dereferences.
- `defer expr` runs a call at scope exit, in reverse order of registration.
- A sema pass tracks each allocation and **warns** when one can reach the end
  of its scope without being freed and without escaping through a return
  value. Warnings, not errors, so arena and process-lifetime patterns are not
  blocked.

```gyro
fn f(): i32 {
  var node = alloc<Node>()
  defer free(node)
  return node.value
}
```

A pointer that may be absent is `*T?`, so a linked list's tail is
`next: *Node?` and the null case has to be checked before use.

---

## Concurrency

Three separate mechanisms answering three separate questions.

### Threads — parallelism

`spawn` starts a call on another thread. It is an ordinary stdlib function
taking a closure, not grammar — the closure is what carries the arguments
across:

```gyro
import { spawn } from "@gyro/stdlib"

spawn(fn() produce(values, 10))
```

`@concurrent(threads = N)` configures a parallel region. `Mutex` and friends
come from the standard library.

### Channels — communication

```gyro
var ch = chan<i32>(64)   // buffered; chan<i32>() is unbuffered
ch <- value              // send, always a statement
var v = <-ch             // receive
var v, open = <-ch       // receive, reporting whether the channel is open
for v in ch {}           // drain until closed
ch.close()
```

`select` waits on several operations and runs the arm that becomes ready
first. It reuses the `comm: body` shape of match, and bindings are ordinary
`var` declarations — there is no `:=`.

```gyro
select {
  var value = <-values: use(value)
  quit <- true: return
  else: idle()            // makes the select non-blocking
}
```

### Async — IO concurrency

`async fn` declares an asynchronous function and `await` resolves the value
of a call to one. Async is a function colour, as in Rust and JavaScript.

```gyro
export async fn fetch_all(urls: string[]): (string[], error) {
  var body, err = await get(urls[0])
}
```

---

## Effect rows — reserved

Signatures may carry an effect row describing what the function does. The
parser records these; **no stage checks them yet.**

```gyro
fn add(a: i32, b: i32): i32                       // no row: inferred
export fn log(msg: string) !{ io }
export fn load(p: string): (string, error) !{ io, alloc }
fn map<T, U>(xs: T[], f: fn(T): U !e): U[] !e     // effect-polymorphic
@gpu fn kernel(a: *f32) !{}                       // empty row
```

Forms: `!{}`, `!{ a, b }`, `!e` for a bare row variable, and `!{ a, ..e }` for
concrete effects plus a row variable.

The intended rule is that module-private functions have their row inferred
from the body, while anything `export`ed must state it. The decorators are
the motivation: `@comptime` needs purity, `@gpu` forbids allocation and
syscalls, and `@distributed` restricts what may cross a node boundary. Today
each of those is a bespoke check; effect rows are how they become one
mechanism.

---

## Reserved but not yet implemented

| feature | state |
| --- | --- |
| effect rows | parsed, unchecked |
| nullable types | parsed; narrowing needs flow analysis in sema |
| traits and bounds | parsed; no satisfaction checking or dispatch |
| operator traits | specified, not wired to the operators |
| trait object inline storage | specified, no layout yet |
| closure capture semantics | parsed; `move` vs `copy` unenforced until sema |
| closure type inference | parsed; needs bidirectional inference in sema |
| module resolution | specified, no loader |
| overflow and bounds panics | operators parse; no checks emitted |
| `Array<T>` `String` | stdlib types, no stdlib yet |
| `->` `=>` tokens | reserved, unused |
| generics | parsed; no instantiation or inference |
| `syntaxfn` parser extensions | designed in the book, not parsed |

See [`decisions.md`](decisions.md) for why each of these is the way it is,
and what was rejected.
