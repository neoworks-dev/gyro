# 2. A first program

This chapter builds one small program end to end: a word counter that reads
text on standard input and prints how often each word appeared. It is short
enough to hold in your head and long enough to hit most of what makes Gyro
different — imports, inference, owning collections against borrowed views,
`defer`, nullability, and the fact that every allocation is yours.

Nothing here is explained in full. Each step links to the chapter that does.

## Reading the input

```gyro
import { println } from "stdlib"
import { read_stdin } from "os"

fn main() {
  var text = read_stdin()
  println("read #{text.len()} bytes")
}
```

```sh
$ printf 'a b\nc\n' | ./bin/gyro run words.gyro
read 6 bytes
```

`read_stdin` comes from the bundled `os` module and reads to end of file in
one shot. `var text` needs no annotation: the type is the function's result,
a `string`. `text.len()` counts **bytes**, not characters, and needs no
import because it is part of the type rather than a name in scope.

## Splitting it up

```gyro
import { println, split, trim } from "stdlib"
import { read_stdin } from "os"

fn main() {
  var text = read_stdin()

  var words = 0
  var lines = text.split("\n")
  defer lines.free()

  for line in lines.slice() {
    var parts = line.trim().split(" ")
    defer parts.free()
    for word in parts.slice() {
      if word.len() > 0 {
        words += 1
      }
    }
  }
  println("#{words} words")
}
```

Three new things, and they are the ones people trip over first.

**`split` and `trim` are imported.** They are *extension methods* on
`string` — ordinary module-scope names that happen to take a receiver. They
work here because they were imported, not because some module somewhere
declared them. That is what keeps the built-in types extensible without a
universe scope ([chapter 5.2](ch05-02-methods.md)).

**`split` returns an owning collection, and the loop wants a view.**
`Array<string>` owns a buffer; `string[]` is a slice, a pointer and a length
borrowing storage it does not own. `.slice()` hands out the view. The
distinction is not decoration — an owner is freed and a view never is
([chapter 4.4](ch04-04-slices-and-views.md)).

**`defer` runs at block exit**, in reverse order of registration. The
`defer parts.free()` inside the loop runs on *every iteration*, which is
exactly what you want for a value created every iteration
([chapter 4.2](ch04-02-leaks-and-defer.md)).

Leave one of those `free` calls out and the program still runs. Nothing
detects the leak at run time yet; the compiler's leak analysis warns about
`alloc<T>(heap())`, not about library types that own storage.

## Counting

```gyro
import { println, Array, Map, split, trim } from "stdlib"
import { read_stdin } from "os"
import { printf } from "fmt"

fn main() {
  var text = read_stdin()

  var counts = Map<string, i32>::new()
  defer counts.free()

  var lines = text.split("\n")
  defer lines.free()

  for line in lines.slice() {
    var words = line.trim().split(" ")
    defer words.free()

    for word in words.slice() {
      if word.len() == 0 {
        continue
      }
      var seen = counts.get(word)
      if seen == null {
        counts.insert(word, 1)
      } else {
        counts.insert(word, seen + 1)
      }
    }
  }

  var keys = counts.keys()
  defer keys.free()

  for key in keys.slice() {
    var count = counts.get(key)
    if count != null {
      printf("%-12s %d\n", key, count)
    }
  }
}
```

```sh
$ printf 'the quick brown fox\nthe lazy dog the end\n' | ./bin/gyro run words.gyro
dog          1
end          1
the          3
quick        1
fox          1
brown        1
lazy         1
```

`Map<K, V>::get` answers `V?` — a nullable `i32` here — because a miss has
to be expressible. And a nullable value cannot be used until it has been
checked:

```gyro
var seen = counts.get(word)
counts.insert(word, seen + 1)     // error: seen may be null
```

The check is what narrows it. Inside `else`, `seen` is an `i32` and the
addition compiles; the compiler follows `!= null`, `== null`, `&&`, `!`,
`else` and early returns to work out where the narrowing holds
([chapter 4.3](ch04-03-nullability.md)). That is why the final loop asks
`if count != null` for a key that certainly exists: the checker does not
know it exists, and there is no way to assert it away.

`printf` is in the `fmt` module and is **ordinary Gyro** — it walks the
format string over a `...any` pack and reads each argument with a `match`.
No tier implements it ([chapter 14.2](ch14-02-printing.md)).

## What the compiler stopped you from doing

The program above is unremarkable. The interesting part is the set of
neighbouring programs that do not compile:

- returning `words.slice()` out of the loop's block — a view of storage that
  is about to be freed. A function handing back a `string[]` or a `string`
  has to name where the view came from, in an effect row
  ([chapter 12](ch12-00-effect-rows.md)).
- `var total: i32` used before every path assigned it — there are no zero
  values ([chapter 3.1](ch03-01-variables-and-constants.md)).
- `counts.get(word) + 1` — nullable, unchecked.
- `words += 1` overflowing an `i32` — that panics rather than wrapping, and
  wrapping is spelled `+%` ([chapter 3.3](ch03-03-operators-and-arithmetic.md)).

## Run it

```sh
./bin/gyro run words.gyro              # build into a temp dir, then run it
./bin/gyro build words.gyro -o words   # keep the binary
```

What it prints is what this book says it prints. If the two disagree, the
compiler is the thing that is wrong — that is not a figure of speech, it is
how the tests are written ([chapter 17.1](ch17-01-the-backend.md)).
