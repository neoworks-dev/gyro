# 9. Errors

Errors are ordinary values. A function that can fail returns `T | Error`, and
`?` takes the value out of one — or returns the error to the caller.
**There are no exceptions and no unwinding,** and every place an error
leaves a function is marked in the source.

```gyro
import { println, Error } from "stdlib"
import { to_i32 } from "convert"

fn parse_port(text: string): i32 | Error {
    let value = to_i32(text)?
    if value < 1 || value > 65535 {
        return Error::new("port out of range")
    }
    return value
}

fn main(): i32 | Error {
    let port = parse_port("8080")?
    println("listening on #{port}")
    return 0
}
```

`to_i32(text)?` is the number, or `parse_port` returning the error `to_i32`
found. Nothing else is hidden: a call without a `?` yields the result itself.

## The type `T | Error`

`T | Error` holds either a `T` or an error. It is the one union in the
language, so the right side is always `Error`, and writing any other union is
an error. The marker binds looser than `?`, so `i32 | Error?` is a union whose
right side is nullable, which is refused rather than read as a nullable
result.

A result is an ordinary type: a field, a parameter, a variable, a channel
element. A value of `T` and an error both go where one is expected, and
nothing converts between typed values otherwise:

```gyro
fn lookup(id: i32): User | Error {
    if id < 0 {
        return Error::new("no such user")    // an Error
    }
    return User{ id: id }                    // a User
}
```

## A function with no value

A function that has nothing to return when it succeeds returns `Error?`. The
nullable is the success case: `null` is "no error", and **reaching the end of
the body is success**, so there is no `return null` to write.

```gyro
fn save(path: string, text: string): Error? {
    write_file(path, text)?
    println("saved #{path}")
}
```

A bare `return` is success too, and `return err` or `return Error::new(...)`
is the failure. An `Error?` is a nullable like any other, so a caller can
check it with `!= null` ([chapter 4.3](ch04-03-nullability.md)) or send it on
with `?`.

## `?`

`x?` is a postfix operator on a `T | Error` or an `Error?`:

- on a `T | Error` it is the `T` when there is one, and otherwise it returns
  the error from the enclosing function;
- on an `Error?` it is nothing when the value is null, and otherwise it
  returns the error.

The enclosing function has to be able to return one: its own result is
`U | Error` or `Error?`, for any `U`. Applying `?` to anything else, or in a
function that returns something else, is a compile error. A closure is its own
function, so a `?` inside one returns from the closure.

`?` is a return, and acts like one: deferred calls run
([chapter 4.2](ch04-02-leaks-and-defer.md)), and an allocation the function
still owns is reported as it would be at any other exit. After `await` it
applies to the awaited value, so `await fetch(url)?` is `(await fetch(url))?`.

```gyro
fn load_config(path: string): string | Error {
    let handle = open(path)?
    defer handle.close()
    return read_all(heap(), handle)?
}
```

## `Error` is a trait

`Error` is a trait ([chapter 10.1](ch10-01-traits.md)) with one method,
`message`. A type is an error because it **says so**: nothing is an error by
having the method.

```gyro
export struct RangeError {
    value: i32
    low: i32
    high: i32
}

impl Error for RangeError {
    fn message(self): string {
        return format(heap(), "value #{self.value} is outside #{self.low}..#{self.high}")
    }
}

export fn check(value: i32): i32 | Error {
    if value < 1 || value > 65535 {
        return RangeError{ value: value, low: 1, high: 65535 }
    }
    return value
}
```

The trait is declared in `stdlib` and imported like any other name:
`import { Error } from "stdlib"`. The impl is for a struct or enum the same
module declares, with no type parameters of its own; an error value names its
type, and a generic type would be many.

`Error` is also a **type**: the owned value that holds any type implementing
the trait, with its data. Where an `Error` is wanted, a `RangeError` goes, and
`Error::new(message)` makes the plain one, which carries a message and nothing
else. The two roles share a name because they are the same idea — the trait
is what a type promises, the type is what holds a promise kept — and they are
told apart by position: `impl Error for T` and `T: Error` name the trait,
`fn f(): i32 | Error` and `let e: Error` the type.

An `Error` owns what it holds, so it can be returned up the stack without
pointing into a frame that is gone, and it allocates nothing: it is a value the
size of the largest error type the program declares. That also means the
message of a plain error is a string the program keeps alive itself.

## Handling an error without `?`

A match over a result names its two cases with type tests
([chapter 6.2](ch06-02-match.md)), `T(name)` for the value and `Error(name)`
for the error, and both have to be covered or the match ends in `_`:

```gyro
match parse_port(text) {
    i32(port): println("listening on #{port}")
    Error(e): eprintln("bad port: #{e.message()}")
}
```

Where the concrete kind of an error matters, a type test on the `Error` itself
matches when the error's type is the one named, and binds the whole value,
payload included:

```gyro
match e {
    RangeError(r): eprintln("port #{r.value} not in #{r.low}..#{r.high}")
    _: eprintln("bad port: #{e.message()}")
}
```

An `Error` is an open subject, so the match is never reported as partial — and
falling off the end panics, so write the `_`. A type that is not generic is
named by its path; a generic one by its arguments, `Array<string>(names)`.

## Ignoring a result is visible

Dropping a result is a **compile error**. The two ways to deal with it are `?`
and a match; discarding takes an explicit `_`:

```gyro
let _ = parse_port("not a number")
```

An un-awaited async call in statement position is an error for the same
reason ([chapter 13.3](ch13-03-async.md)).

## What `main` does with one

`main` may return nothing, an `i32`, or an `i32 | Error`. An `i32` is the
exit status. An error is written to standard error, as its message and a
newline, and the status is 1; output the program wrote before that stays
where it was written.

```gyro
fn main(): i32 | Error {
    let config = load_config("app.toml")?    // missing: "stat app.toml: no such file or directory", status 1
    println(config)
    return 0
}
```

Deferred calls in `main` run before the status is decided, as they do at any
return. A `main` that returns a value needs `stdlib` loaded, since what acts
on the result is code in it; one that returns `i32 | Error` has already
imported `Error` from there.

## The other kind of failure

Programmer errors are not errors in this sense at all. Out-of-bounds
indexing, integer divide by zero, dereferencing a null `T?`, integer
overflow and a narrowing `as` that loses the value are **bugs, not
conditions**. They print a message with a source position and **abort**:

```text
panic: index 7 out of range for length 3
  at main.gyro:12:11
```

They are neither returnable nor catchable. That is what keeps the result
meaningful — if any call could also fail invisibly, the type would stop being a
complete statement about what a function does — and it is what lets the
language have no unwinding machinery at all.

A panic aborts the process on the main thread, and kills only the offending
thread when it happens on a spawned one. That is also why `Mutex` has no
poisoning ([chapter 13.1](ch13-01-threads.md)).

## Errors from the standard library

Everything fallible in the bundled modules returns the same shapes: `T |
Error`, or `Error?` when there is no value.

```gyro
import { Error } from "stdlib"
import { open, read_all } from "fs"
import { heap } from "mem"

fn load_config(path: string): string | Error {
    let handle = open(path)?
    defer handle.close()
    return read_all(heap(), handle)?
}
```

Each call that can fail is checked at the point it is made, and the `?` is
the whole of the checking: that is the verbosity the design accepts in
exchange for having no hidden control flow, and it is one character.
