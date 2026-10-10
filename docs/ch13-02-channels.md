# 13.2. Channels and `select`

```gyro
var ch = chan<i32>(64) // buffered; chan<i32>() is unbuffered
ch <- value            // send, always a statement
var v = <-ch           // receive
var v, open = <-ch     // receive, reporting whether the channel is open
for v in ch {}         // drain until closed
ch.close()
```

A channel element can be any type a value can be, tuples included — which is
how a worker reports a fallible result without a wrapper struct:

```gyro
fn fetch_into(url: string, results: chan<(string, error?)>) {
    results <- (format(heap(), "stub body for #{url}"), null)
    results.close()
}
```

An unbuffered channel is a true rendezvous: the sender blocks until a
receiver takes the element. Sending on a closed channel aborts.

## A send is a statement

`ch <- v` is a statement, never an expression. That is what disarms the
lexical hazard `<-` creates: `x < -1` is a comparison and `x <- 1` is a
send, and since a send can only appear where a statement can, the parser
never has to guess ([chapter 3.3](ch03-03-operators-and-arithmetic.md)).

## The two-name receive

```gyro
var v, open = <-ch
```

`open` reports whether the channel was open. **Reading `v` when `open` came
back false is a definite-assignment error** — a closed channel yields no
value, not a zero value. The flag is what makes the value readable, and the
check follows `!` and `&&` the same way null narrowing does:

```gyro
var v, open = <-ch
if open {
    println(v) // fine
}

var w, more = <-ch
if !more {
    return
}
println(w) // fine: the branch that would contradict it returned
```

This is the same mechanism as [definite
assignment](ch03-01-variables-and-constants.md), not a special case bolted
onto channels.

## Draining

```gyro
for value in values {
    total += value
}
```

`for..in` over a channel receives until the channel is closed. The producer
closing when it has nothing more to send is what lets the receiver tell "no
value yet" from "no value ever".

## `select`

`select` waits on several operations and runs the arm that becomes ready
first. Arms are `comm: body`, one per line, like `match`. Bindings are
ordinary `var` declarations — there is no `:=`.

```gyro
select {
    var value = <-values: use(value)
    quit <- true: return
    else: idle() // makes the select non-blocking
}
```

An `else` arm makes the `select` non-blocking: if nothing is ready, it runs.

For definite assignment, a `select` behaves like a `match`: exactly one arm
runs, so what survives afterwards is what every arm wrote.

## Which ready arm runs is unspecified

When more than one arm is ready at the same moment, **which one runs is
unspecified**. A program that depends on the choice is wrong.

This is not a hole in the specification that someone will close later — the
tiers differ deliberately. The evaluator picks at random; the native runtime
polls in source order. Neither is the definition, and a program that can
tell them apart is relying on something the language does not promise.

A fair native runtime is queued work; it will not make the choice
specified.

## Under the hood

A channel is not a type METALS knows. `chan<T>` lowers to an address of
state the standard library keeps, and `<-`, `close`, `for..in` and `select`
lower to calls of Gyro functions in `stdlib` that move the bytes of an
element between the caller's frame and the channel ([appendix
C](appendix-c-stdlib.md)); `spawn` and `Mutex` are ordinary Gyro too. What
the format provides underneath is threads, atomics and the `futex` system
call ([appendix D](appendix-d-metals.md)).

A channel is a ring buffer under a lock, with capacity 0 as a true
rendezvous, and a thread that has to wait sleeps on a futex word rather than
spinning. A closed channel yields zeroed memory with `open` false — which is
exactly why the language makes reading that value a compile error.

A program that creates, sends on, receives from, closes or iterates a
channel, or writes a `select`, has to load `stdlib`, since the library is
what implements them. The compiler loads nothing on a program's behalf, so
a program that imports nothing from `stdlib` is rejected at the construct
(E0620), the way string interpolation is rejected when `mem` is not loaded.
