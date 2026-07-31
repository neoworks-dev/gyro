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
| arithmetic | `+ - * / %` |
| increment | `++ --` (prefix and postfix, usable in expressions) |
| comparison | `== != < <= > >=` |
| logical | `&& \|\| !` |
| bitwise | `& \| ^ ~ << >>` |
| assignment | `= += -= *= /= %= &= \|= ^= <<= >>=` |
| access | `.` field, `::` namespace, `[]` index |
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
import defaults from "./config"
import "./side-effects"
```

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

A method takes `self` to read the receiver or `*self` to mutate it. A
function in an impl block with no receiver is an associated function, called
as `Stack::new(16)`.

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
| `*T` | pointer |
| `T[]` | slice |
| `T[N]` | fixed-size array |
| `chan<T>` | channel |
| `(A, B)` | tuple |
| `fn(A, B): C` | function |
| `ast::Literal` | namespaced name |
| `Result<T, E>` | generic instantiation |

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

Errors are ordinary values. A fallible function returns a `(T, error)` pair
and the caller has to handle it — there is no propagation operator, no
exceptions and no unwinding.

```gyro
fn parse_port(text: string): (i32, error) {
  var value, err = to_i32(text)
  if err != null {
    return 0, err
  }
  return value, null
}
```

Discarding an error requires writing `_`, so ignoring one is always visible
in the source.

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

---

## Concurrency

Three separate mechanisms answering three separate questions.

### Threads — parallelism

`spawn call` starts the call on another thread. It is grammar rather than a
library function because, without closures, there is no way to pass an
uncalled function together with its arguments.

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
| `->` token | reserved for closure syntax |
| `=>` token | reserved |
| `?` token | reserved |
| generics | parsed; no instantiation or inference |
| `syntaxfn` parser extensions | designed in the book, not parsed |
