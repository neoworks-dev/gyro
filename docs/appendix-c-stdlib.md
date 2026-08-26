# Appendix C. The built-in method surface and the standard library

Two different things live here, and the difference matters: the **built-in
method surface** is part of a type and needs no import, while everything in
the **standard library** is an ordinary name that has to be imported
([chapter 7](ch07-00-modules.md)).

## The built-in method surface

Small, fixed, and not extensible past itself: an extension method of the
same name does not displace one of these.

| method | on | result |
| --- | --- | --- |
| `len()` | slices, arrays, `string` | `usize` |
| `runes()` | `string` | `rune[]` — a copy |
| `bytes()` | `string` | `u8[]` — a copy |
| `close()` | `chan<T>` | |
| `compare(other)` | numeric primitives, `rune`, `string` | `i32`: -1, 0 or 1 |
| `hash()` | those, and tuples, structs and enums of them | `u64` |
| `message()` | `error` | `string` |
| `to_string()` | any type that declares one | `string` |

`len`, `close`, `hash`, `compare` and `message` do nothing observable, so
they contribute no effects; `runes()` and `bytes()` copy, so they do `alloc`
([chapter 12](ch12-00-effect-rows.md)).

Indexing is syntax rather than a method, and reaches three containers:
`xs[i]` on a slice, on a fixed-size array, and on an `Array<T>`. Every one
is a **place**, so `xs[i] = v` and `&xs[i]` work. Narrowing — `xs[lo..hi]`
— reaches all three too, and answers with a slice
([chapter 4.4](ch04-04-slices-and-views.md)).

Positional tuple access — `pair.0` — is syntax, not a method.

`void` and `error` cannot be extended: nothing has type void, and `error` is
a contract rather than a type.

## The standard library

Bundled Gyro sources compiled into the compiler binary. **A declaration with
no body is a runtime intrinsic; everything with a body is ordinary Gyro,
compiled like any other module.**

### `stdlib`

Printing:

```gyro
export fn print<T>(value: T) !{ io }
export fn println<T>(value: T) !{ io }
export fn eprintln<T>(value: T) !{ io }
```

Threads:

```gyro
export fn spawn(task: fn()) !{ io, alloc }
```

`Array<T>` — owns its buffer, grows, freed explicitly
([chapter 8.1](ch08-01-array.md)):

```gyro
export fn Array<T>::new(): Array<T> !{ alloc }
export fn Array<T>::with_capacity(capacity: usize): Array<T> !{ alloc }
export fn Array<T>::push(*self, value: T) !{ alloc }
export fn Array<T>::pop(*self): (T, bool) !{}
export fn Array<T>::get(self, index: usize): T !{}
export fn Array<T>::set(*self, index: usize, value: T) !{}
export fn Array<T>::at(self, index: usize): *T !{}
export fn Array<T>::len(self): usize !{}
export fn Array<T>::is_empty(self): bool !{}
export fn Array<T>::slice(self): T[] !{ &self }
export fn Array<T>::free(*self) !{}
```

Text ([chapter 8.2](ch08-02-strings.md)):

```gyro
export fn from_utf8(bytes: u8[]): string !{ alloc }

export fn String::new(): String !{ alloc }
export fn String::with_capacity(capacity: usize): String !{ alloc }
export fn String::of(text: string): String !{ alloc }
export fn String::push(*self, text: string) !{ alloc }
export fn String::push_rune(*self, value: rune) !{ alloc }
export fn String::len(self): usize !{}
export fn String::text(self): string !{ alloc }
export fn String::to_string(self): string !{ alloc }
export fn String::free(*self) !{}
```

Extension methods on `string`, which have to be imported to be callable:

```gyro
export fn string::starts_with(self, prefix: string): bool !{ alloc }
export fn string::ends_with(self, suffix: string): bool !{ alloc }
export fn string::index_of(self, needle: string): usize? !{ alloc }
export fn string::contains(self, needle: string): bool !{ alloc }
export fn string::substring(self, from: usize, to: usize): string !{ alloc }
export fn string::trim(self): string !{ alloc }
export fn string::split(self, sep: string): Array<string> !{ alloc }
export fn string::repeat(self, times: i32): string !{ alloc }

export fn join(parts: string[], sep: string): string !{ alloc }
```

`Map<K, V>` ([chapter 8.3](ch08-03-map.md)):

```gyro
export fn Map<K, V>::new(): Map<K, V> !{ alloc }
export fn Map<K, V>::insert(*self, key: K, value: V) !{ alloc }
export fn Map<K, V>::get(self, key: K): V? !{}
export fn Map<K, V>::contains(self, key: K): bool !{}
export fn Map<K, V>::remove(*self, key: K): bool !{}
export fn Map<K, V>::len(self): usize !{}
export fn Map<K, V>::is_empty(self): bool !{}
export fn Map<K, V>::keys(self): Array<K> !{ alloc }
export fn Map<K, V>::free(*self) !{}
```

Mutexes ([chapter 13.1](ch13-01-threads.md)):

```gyro
export fn Mutex<T>::new(value: T): Mutex<T> !{ alloc }
export fn Mutex<T>::lock(*self): MutexGuard<T> !{}
export fn MutexGuard<T>::get(self): T !{}
export fn MutexGuard<T>::set(*self, value: T) !{}
export fn MutexGuard<T>::unlock(*self) !{}
```

### `mem`

```gyro
export fn alloc<T>(): *T !{ alloc }
export fn free<T>(pointer: *T) !{}
export fn size<T>(): usize !{}
```

### `fmt`

Entirely ordinary Gyro over an `...any` pack
([chapter 14.2](ch14-02-printing.md)):

```gyro
export fn printf(format: string, args: ...any) !{ io, alloc }
export fn eprintf(format: string, args: ...any) !{ io, alloc }
export fn sprintf(format: string, args: ...any): string !{ alloc }
```

### `fs`

```gyro
export struct File {}

export fn open(path: string): (File, error?) !{ io, alloc }
export fn read_all(file: File): (string, error?) !{ io, alloc }
export fn File::close(*self) !{ io }

export fn write_file(path: string, text: string): error? !{ io }
export fn append_file(path: string, text: string): error? !{ io }
export fn remove(path: string): error? !{ io }
export fn read_dir(path: string): (Array<string>, error?) !{ io, alloc }
export fn exists(path: string): bool !{ io }
```

The one-shot operations take a path rather than a handle: each opens, acts
and closes in one call. `write_file` creates or truncates; `append_file`
creates or extends. `read_dir` lists an existing directory's entry names,
sorted, without `.` and `..`.

### `os`

```gyro
export fn env(name: string): string? !{ io, alloc }
export fn read_stdin(): string !{ io, alloc }
export fn exit(code: i32) !{ io }
```

`env` answers null when the variable is unset. `read_stdin` reads to end of
file in one shot: a second call finds the stream drained and returns `""`.
`exit` ends the process after flushing stdout and stderr — **deferred calls
do not run**; exit is immediate, like a panic.

### `time`

```gyro
export fn now(): i64 !{ io }         // wall clock, ms since the Unix epoch
export fn monotonic(): i64 !{ io }   // measuring clock, ns, never steps back
export fn sleep(millis: i64) !{ io }
```

`now` can jump when the system clock is adjusted, so it dates events; it
does not measure them. Only differences of `monotonic` mean anything.

### `math`

`f64` only, and each one is a METALS instruction rather than a runtime
call — every tier answers it with the machine's own float operation, so
there is no library to agree with and nothing to diverge.

```gyro
export fn sqrt(x: f64): f64 !{}
export fn abs(x: f64): f64 !{}
export fn ceil(x: f64): f64 !{}
export fn floor(x: f64): f64 !{}
export fn trunc(x: f64): f64 !{}
export fn round(x: f64): f64 !{}
export fn min(x: f64, y: f64): f64 !{}
export fn max(x: f64, y: f64): f64 !{}
export fn copysign(x: f64, y: f64): f64 !{}
```

Three definitions differ from C's library **on purpose**:

- `sqrt` of a negative operand is NaN, not a panic — IEEE defines an answer.
- `round` is **ties-to-even**: `round(0.5)` is `0.0` and `round(1.5)` and
  `round(2.5)` are both `2.0`. C's `round` is ties-away-from-zero, which is
  the rule that makes a sum of rounded values drift upward.
- `min` and `max` **propagate NaN** rather than answering the other operand,
  so a NaN in the input is visible in the output. The two zeros are ordered:
  `min(-0.0, 0.0)` is `-0.0`.

### `convert`

```gyro
export fn to_i32(text: string): (i32, error?) !{ alloc }
export fn to_f64(text: string): (f64, error?) !{ alloc }
```

Both allocate, because the failure carries a message.

### `net`

```gyro
export async fn get(url: string): (string, error?) !{ io, alloc }
```

## What is runtime-backed and what is Gyro

Runtime intrinsics: `print`/`println`/`eprintln`, `spawn`, `Array<T>`,
`Mutex<T>`/`MutexGuard<T>`, `from_utf8`, `alloc`/`free`/`size`,
`open`/`read_all`/`File::close` and the rest of `fs`, `to_i32`/`to_f64`,
`net::get`, the `os` and `time` surfaces. The `math` functions are METALS
instructions.

Ordinary Gyro: `String` and its UTF-8 encoder, every text method,
`Map<K, V>`, `Array<T>::is_empty`, and all of `fmt`.

**Prefer Gyro.** Something with no interesting runtime requirement written
into the C runtime instead is a second language to maintain it in, for
nothing ([chapter 17.1](ch17-01-the-backend.md)).
