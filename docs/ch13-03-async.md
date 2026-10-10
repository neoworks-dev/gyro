# 13.3. Async

`async fn` declares an asynchronous function and `await` resolves the value
of a call to one. Async is a **function colour**, as in Rust and JavaScript,
and it appears in the type: `async fn(): i32` is the type of an async
closure.

```gyro
export async fn fetch_all(urls: string[]): (Array<string>, error?) {
    var bodies = Array<string>::new(heap())

    for url in urls {
        var body, err = await get(url)
        if err != null {
            return bodies, err
        }
        bodies.push(body)
    }
    return bodies, null
}
```

`async fn main` is allowed; the runtime drives it.

```gyro
async fn main() {
    var bodies, err = await fetch_all(["https://example.com"])
    defer bodies.free()
    ...
}
```

An async closure is written the same way and inhabits the same type:

```gyro
var later = async fn(): i32 { return 1 }
```

## An un-awaited call is an error

An async call in statement position that is never awaited is a compile
error, in the same family as dropping an error result: a value that
represents work in flight cannot be discarded silently
([chapter 9](ch09-00-errors.md)).

## What `await` actually does today

Async is **eager**. Calling an async function starts the work immediately —
a thread on the native tiers — and returns a future; `await` joins it. An
async `main` is simply run synchronously by the entry point, which is what
eager-start-plus-immediate-await reduces to.

That is a real implementation with real thread costs, not a scheduler: there
are no green threads yet, and `async` is not currently cheaper than `spawn`
([appendix G](appendix-g-known-gaps.md)).

## Async against the other two mechanisms

Async exists for **IO concurrency** — holding many operations that are
waiting on something outside the process. It is not the tool for using
several cores; that is `spawn` ([chapter 13.1](ch13-01-threads.md)). And it
is not the tool for handing values between concurrent tasks; that is a
channel ([chapter 13.2](ch13-02-channels.md)).

A function that is `async` cannot have its address taken as a `code(..)`
pointer, because an async function is *started* by this program's scheduler
rather than *called* by whoever holds the address
([chapter 11](ch11-00-closures.md)).
