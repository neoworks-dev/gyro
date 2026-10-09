# 9. Errors

Errors are ordinary values. A fallible function returns a `(T, error?)`
tuple and the caller has to handle it — **there is no propagation operator,
no exceptions and no unwinding.**

```gyro
fn parse_port(text: string): (i32, error?) {
  ...
  return value, null
}

var port, err = parse_port("8080")
if err != null {
  eprintln(err.message())
  return
}
println("listening on #{port}")
```

## `error` is duck-typed

`error` is a built-in contract, satisfied structurally: **any type with a
`message(): string` method is an error.** Nothing is declared; having the
method is the whole test.

So a library defines its own error type, carrying its own data, and callers
match on the concrete kind:

```gyro
export struct RangeError {
  export value: i32
  export low: i32
  export high: i32
}

fn RangeError::message(self): string {
  return format(heap(), "value #{self.value} is outside #{self.low}..#{self.high}")
}

export fn parse_port(text: string): (i32, error?) {
  var value, err = to_i32(text)
  if err != null {
    return 0, err
  }
  if value < 1 || value > 65535 {
    return 0, RangeError{ value: value, low: 1, high: 65535 }
  }
  return value, null
}
```

`error` is not a type you can extend with methods, and there is no `Result`:
the tuple is the shape, and `error?` is a nullable of a contract.

## Matching on the concrete kind

Where the concrete type is not statically known, a pattern is a **type
test** ([chapter 6.2](ch06-02-match.md)):

```gyro
match err {
  RangeError(e): eprintln("port #{e.value} not in #{e.low}..#{e.high}")
  _: eprintln("bad port: #{err.message()}")
}
```

`RangeError(e)` matches when the error's concrete type is `RangeError` and
binds the whole struct, payload included. An `error` is an open subject, so
the match is never reported as partial — and falling off the end panics, so
write the `_`.

## Ignoring an error is visible

Dropping an error result is a **compile error**. Discarding takes an
explicit `_`:

```gyro
var _, _ = parse_port("not a number")
```

An un-awaited async call in statement position is an error for the same
reason ([chapter 13.3](ch13-03-async.md)).

## How an error is stored

An `error` value whose concrete type is unknown at the call site is a vtable
pointer plus a fixed inline buffer. Small types travel inside it with no
allocation; **a type larger than the cap is a compile error naming the
size**, and must be boxed explicitly.

That is the tradeoff written down: the error path allocates nothing for the
error types people actually write, and the ones that would have allocated
say so at compile time instead of at run time.

## The other kind of failure

Programmer errors are not errors in this sense at all. Out-of-bounds
indexing, integer divide by zero, dereferencing a null `T?`, integer
overflow and a narrowing `as` that loses the value are **bugs, not
conditions**. They print a message with a source position and **abort**:

```text
panic: index 7 out of range for length 3
  at main.gyro:12:11
```

They are neither returnable nor catchable. That is what keeps the
`(T, error?)` channel meaningful — if any call could also fail invisibly,
the tuple would stop being a complete statement about what a function does —
and it is what lets the language have no unwinding machinery at all.

A panic aborts the process on the main thread, and kills only the offending
thread when it happens on a spawned one. That is also why `Mutex` has no
poisoning ([chapter 13.1](ch13-01-threads.md)).

## Errors from the standard library

Everything fallible in the bundled modules returns the same shape:

```gyro
import { open, read_all } from "fs"
import { to_i32 } from "convert"
import { heap } from "mem"

fn load_config(path: string): (string, error?) {
  var handle, err = open(path)
  if err != null {
    return "", err
  }
  defer handle.close()

  var contents, read_err = read_all(heap(), handle)
  if read_err != null {
    return "", read_err
  }
  return contents, null
}
```

Note the two error names. There is no shadowing trick and no `?`: each call
that can fail gets its own binding, and each one is checked. That is the
verbosity the design accepts in exchange for having no hidden control flow.
