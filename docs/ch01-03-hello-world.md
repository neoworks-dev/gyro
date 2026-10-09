# 1.3. Hello, world

Put this in `hello.gyro`:

```gyro
import { println } from "stdlib"

fn main() {
  println("Hello, world!")
}
```

```sh
./bin/gyro run hello.gyro
```

```text
Hello, world!
```

Four things in four lines are worth naming, because each is a rule that
holds everywhere else in the language.

## `println` is imported

There is no universe scope. A name that is not part of the formal syntax has
to be imported, and `println` is an ordinary exported function in the
bundled `stdlib` module. A bare module name like `"stdlib"` is the standard
library; `"./util"` would be the file beside this one.
[Chapter 7](ch07-00-modules.md) covers imports properly.

Methods are the exception that proves it: `xs.len()` and `ch.close()` need
no import, because they are part of their type rather than names in scope.

## `fn main` is the one blessed name

The compiler finds `main` in the root module and the OS calls it, so it
needs no `export` — nothing imports it. It takes nothing, or the command
line:

```gyro
fn main(args: string[]) {
  for arg in args {
    println(arg)
  }
}
```

`args[0]` is the name the program was invoked as, so a program run with no
arguments still sees one element. `main` returns nothing — a failing program
prints and aborts rather than handing back a status — and any other
signature is an error, since nothing would supply the arguments. `async fn
main` is allowed; the runtime drives it.

## There are no semicolons

A newline ends a statement. Braces are always required, and conditions take
no parentheses. [Chapter 3.5](ch03-05-lines-and-comments.md) has the exact
rule, including how to break a long expression across lines.

## Running is not the same as building

```sh
./bin/gyro check hello.gyro           # type-check only
./bin/gyro run hello.gyro             # check, lower to METALS, evaluate
./bin/gyro build hello.gyro -o hello  # check, lower, compile to a binary
```

All three start the same way, because everything the compiler can read
becomes METALS and everything that runs, runs from METALS. `run` is `build`
into a temporary directory followed by the binary, so there is one thing
that decides what a program does — and
[chapter 17.1](ch17-01-the-backend.md) is about what keeps it honest.

## A slightly bigger hello

This is `examples/hello.gyro`, and it introduces the shapes the next
chapters unpack:

```gyro
import { println } from "stdlib"

// The row is a complete statement of what greet does: println is io, and
// the interpolated text allocates nothing.
fn greet(name: string) !{ io } {
  println("Hello, #{name}!")
}

fn main() {
  greet("World")
  println("Gyro compiler is alive.")

  var x: i32 = 6 * 7
  println(x)              // 42

  var flag = x > 40       // inferred bool
  println(flag)           // true

  const MAX: i32 = 100
  println("max is #{MAX}, half of that is #{MAX / 2}")
}
```

`#{...}` interpolates any Gyro expression into the text `println` writes.
`var` and `const` bind, `: i32` annotates and is optional where the type is
inferable, and `!{ io }` is an effect row — a promise about what the body does,
checked against the body. Effect rows are optional; a function with no row
is unconstrained. [Chapter 12](ch12-00-effect-rows.md) is where they get
their own treatment.

Next: [a first program](ch02-00-a-first-program.md) that does something.
