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
| `hash()` | types that are `Hash`: those, and tuples, and structs and enums that implement it | `u64` |
| `eq(other)` | types that are `Eq` | `bool` |
| `add`, `sub`, `mul`, `div`, `rem`, `neg` | the numeric primitives | the same type |
| `message()` | `error` | `string` |

The last two rows are the methods of the capability traits below, present on
the built-in types so a generic body may call `a.add(b)` where `a + b` would
do. `to_string` is not on the list: a type that implements `Print` with a
block has its own, and no built-in type does, since a number's text is made
by the runtime.

`len`, `close`, `hash`, `eq`, `compare` and `message` do nothing observable, so
they contribute no effects; `runes()` and `bytes()` copy, so they do `alloc`
([chapter 12](ch12-00-effect-rows.md)).

Indexing is syntax rather than a method, and reaches three built-in
containers: `xs[i]` on a slice, on a fixed-size array, and on an `Array<T>`,
and any type that implements `Index<T>`. Every one is a **place**, so
`xs[i] = v` and `&xs[i]` work. Narrowing — `xs[lo..hi]`
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
export fn print<T: Print>(value: T) !{ io }
export fn println<T: Print>(value: T) !{ io }
export fn eprint<T: Print>(value: T) !{ io }
export fn eprintln<T: Print>(value: T) !{ io }
```

The capability traits ([chapter 10.1](ch10-01-traits.md)). The compiler gives
each its meaning by where it is declared; the built-in types implement the
ones that apply to them, and a type a program declares implements one with
`impl Trait for Type`.

```gyro
export trait Eq { fn eq(self, other: Self): bool }
export trait Hash { fn hash(self): u64 }
export trait Print { fn to_string(self): string }
export trait Ord: Eq { fn compare(self, other: Self): i32 }
export trait Add { fn add(self, other: Self): Self }
export trait Sub { fn sub(self, other: Self): Self }
export trait Mul { fn mul(self, other: Self): Self }
export trait Div { fn div(self, other: Self): Self }
export trait Rem { fn rem(self, other: Self): Self }
export trait Neg { fn neg(self): Self }
export trait Iterator<T> { fn next(*self): (T, bool) }
export trait Index<T> { fn at(*self, index: usize): *T }
```

`Array<T>` — owns its buffer, grows, freed explicitly. It is made with an
`Allocator` from `mem` and records it, and `push` and `free` go through
the recorded one ([chapter 8.1](ch08-01-array.md)):

```gyro
export fn Array<T>::new(a: Allocator): Array<T> !{}
export fn Array<T>::with_capacity(a: Allocator, capacity: usize): Array<T> !{}
export fn Array<T>::push(*self, value: T) !{}
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
export fn from_utf8(a: Allocator, bytes: u8[]): string !{}
export fn string::bytes(self, a: Allocator): u8[] !{}
export fn string::runes(self, a: Allocator): rune[] !{}

export fn String::new(a: Allocator): String !{}
export fn String::with_capacity(a: Allocator, capacity: usize): String !{}
export fn String::of(a: Allocator, text: string): String !{}
export fn String::push(*self, text: string) !{}
export fn String::push_rune(*self, value: rune) !{}
export fn String::len(self): usize !{}
export fn String::text(self, a: Allocator): string !{}
export fn String::to_string(self): string !{}
export fn String::free(*self) !{}
```

Extension methods on `string`, which have to be imported to be callable:

```gyro
export fn string::starts_with(self, prefix: string): bool !{}
export fn string::ends_with(self, suffix: string): bool !{}
export fn string::index_of(self, needle: string): usize? !{}
export fn string::contains(self, needle: string): bool !{}
export fn string::substring(self, a: Allocator, from: usize, to: usize): string !{}
export fn string::trim(self, a: Allocator): string !{}
export fn string::split(self, a: Allocator, sep: string): Array<string> !{}
export fn string::repeat(self, a: Allocator, times: i32): string !{}

export fn join(a: Allocator, parts: string[], sep: string): string !{}
```

`Map<K: Eq & Hash, V>` ([chapter 8.3](ch08-03-map.md)):

```gyro
export fn Map<K, V>::new(a: Allocator): Map<K, V> !{}
export fn Map<K, V>::insert(*self, key: K, value: V) !{}
export fn Map<K, V>::get(self, key: K): V? !{}
export fn Map<K, V>::contains(self, key: K): bool !{}
export fn Map<K, V>::remove(*self, key: K): bool !{}
export fn Map<K, V>::len(self): usize !{}
export fn Map<K, V>::is_empty(self): bool !{}
export fn Map<K, V>::keys(self): Array<K> !{}
export fn Map<K, V>::free(*self) !{}
```

Mutexes ([chapter 13.1](ch13-01-threads.md)):

```gyro
export fn Mutex<T>::new(value: T): Mutex<T> !{}
export fn Mutex<T>::lock(*self): MutexGuard<T> !{}
export fn MutexGuard<T>::get(self): T !{}
export fn MutexGuard<T>::set(*self, value: T) !{}
export fn MutexGuard<T>::unlock(*self) !{}
```

### `mem`

The allocator model is [chapter 4.5](ch04-05-allocators.md). `alloc`,
`free`, `heap` and `Arena` are ordinary Gyro over the procedure an
`Allocator` carries; `size`, `align` and `panic` are the intrinsics under
them. The raw memory they stand on, the process heap, `copy_bytes`, `offset`
and a closure's environment, belongs to the `unsafe` module below, which
`mem` imports. The functions here that use it are `safe fn`s: `alloc`,
`heap`'s procedure, `Arena`'s, `keep`, `release` and the thread entry under
`spawn`.

```gyro
export enum Mode { Alloc, Free, FreeAll, Resize }

export struct Allocator {
  export proc: code(anyptr?, Mode, usize, usize, anyptr?, usize): anyptr?
  export data: anyptr?
}

export fn heap(): Allocator !{}

export safe fn keep<F>(a: Allocator, f: F): F !{}
export safe fn release<F>(a: Allocator, f: F) !{}
export fn spawn(a: Allocator, body: fn()) !{ io }

export safe fn alloc<T>(a: Allocator): *T !{}
export fn free<T>(a: Allocator, pointer: *T) !{}
export fn size<T>(): usize !{}
export fn align<T>(): usize !{}

export fn panic(message: string) !{}

export fn format(a: Allocator, value: Format): string !{}

export struct Arena { ... }
export fn Arena::new(a: Allocator, capacity: usize): Arena !{}
export fn Arena::allocator(*self): Allocator !{}
export fn Arena::free_all(*self) !{}
export fn Arena::free(*self) !{}
```

`format` shows each piece of a `Format` and joins them into a `string`
whose bytes come from `a`. The text of a piece that is not already a string
is still produced by the runtime on the process heap on the way through, so
only the result is in `a` ([appendix G](appendix-g-known-gaps.md)).

`Format` is what a string literal containing `#{}` is: its pieces, recorded,
and nothing allocated ([chapter 14.2](ch14-02-printing.md)). The printers in
`stdlib` take one as they take any value and stream it, and `format` builds
it into an allocator. It lives in `mem` beside `format`, because `stdlib`
imports `Allocator` from `mem` and the two cannot import each other.

```gyro
export struct Format { pieces: any[] }

export fn Format::len(self): usize !{}
export fn Format::piece(self, index: usize): any !{}
export fn Format::text(self, index: usize): string !{}
```

`piece` is the value a hole held, or a literal segment as a `string`. `text`
shows it the way `println` would. A `Format` may only be passed as an
argument.

`keep` copies a closure's environment out of the frame that wrote it into
`a`, and `release` gives the copy back; a closure with no environment passes
through both untouched ([chapter 11](ch11-00-closures.md)). `F` has to be a
function type. `spawn` starts a thread, calling `keep(a, body)` first and
releasing the copy when the thread's closure returns; it lives in `mem`
beside `keep`, which it is written over. `keep` and `release` are written over two private intrinsics that
read a `fn` value's environment word and rebuild one with another; neither
is exported, since raw memory is not part of the language's surface.

`panic` aborts the process with the message, the way an index out of
range does. It is a function rather than a keyword because a program that
has decided to stop needs nothing from the grammar to say so — but the
checker knows it does not return, so `if p == null { panic("...") }`
narrows `p` below it exactly as a `return` would
([chapter 4.3](ch04-03-nullability.md)).

### `fmt`

Entirely ordinary Gyro over an `...any` pack
([chapter 14.2](ch14-02-printing.md)):

```gyro
export fn printf(format: string, args: ...any) !{ io }
export fn eprintf(format: string, args: ...any) !{ io }
export fn sprintf(a: Allocator, format: string, args: ...any): string !{}
```

### `fs`

```gyro
export struct File { ... }

export fn open(path: string): (File, error?) !{ io }
export fn read_all(a: Allocator, file: File): (string, error?) !{ io }
export fn File::close(*self) !{ io }

export fn write_file(path: string, text: string): error? !{ io }
export fn append_file(path: string, text: string): error? !{ io }
export fn remove(path: string): error? !{ io }
export fn read_dir(a: Allocator, path: string): (Array<string>, error?) !{ io }
export fn exists(path: string): bool !{ io }
```

The one-shot operations take a path rather than a handle: each opens, acts
and closes in one call. `write_file` creates or truncates; `append_file`
creates or extends. `read_dir` lists an existing directory's entry names,
sorted, without `.` and `..`.

### `os`

```gyro
export fn env(a: Allocator, name: string): string? !{ io }
export fn read_stdin(a: Allocator): string !{ io }
export fn readline(a: Allocator): string? !{ io }
export fn exit(code: i32) !{ io }
```

`env` answers null when the variable is unset. `read_stdin` reads to end of
file in one shot: a second call finds the stream drained and returns `""`.
`exit` ends the process after flushing stdout and stderr — **deferred calls
do not run**; exit is immediate, like a panic.

`readline` takes the next line, without its terminator, and answers null at
end of file — an empty line is `""`, the way an empty environment variable
is, so absence is the null and nothing else. A final line with no newline
still comes back. Both readers share the stream: a program may take a line
and then drain what is left.

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

Numbers from text, written in Gyro. One function for each integer width,
`to_i8` through `to_usize`, and `to_f64`:

```gyro
export fn to_i32(text: string, radix: u32 = 10): (i32, error?) !{}
export fn to_u8(text: string, radix: u32 = 10): (u8, error?) !{}
export fn to_f64(text: string): (f64, error?) !{}
export struct ParseError { export text: string }
```

An integer is an optional sign, then digits of the radix, which is 2 to 36
(a letter is a digit above nine, in either case); white space around it is
dropped. There is no prefix and no separator, and an unsigned type takes no
minus sign except on zero. A value that does not fit the width is an error,
never a wrapped number: `"300" is out of range for u8`. Text that is not a
number is `"abc" is not an i32`. A radix outside 2 to 36 is a bug in the
caller and panics. Every failure allocates its message on the heap, and the
error is a `ParseError`.

### `unsafe`

The operations the compiler cannot check, each an `unsafe fn`
([chapter 4.6](ch04-06-unsafe.md)). Calling one needs an `unsafe fn` or a
`safe fn` around it.

```gyro
export unsafe fn cast<T>(p: anyptr): *T !{}
export unsafe fn ptr_from_int<T>(address: usize): *T !{}
export unsafe fn ptr_to_int(p: anyptr): usize !{}
export unsafe fn offset(base: anyptr, bytes: usize): anyptr !{}
export unsafe fn copy_bytes(to: anyptr, from: anyptr, bytes: usize) !{}
export unsafe fn slice_from_raw<T>(p: anyptr, len: usize): T[] !{}

export unsafe fn raw_alloc(size: usize, align: usize): anyptr? !{}
export unsafe fn raw_free(pointer: anyptr) !{}
export unsafe fn raw_resize(pointer: anyptr, old_size: usize, size: usize, align: usize): anyptr? !{}

export unsafe fn env_of<F>(f: F): anyptr? !{}
export unsafe fn with_env<F>(f: F, env: anyptr?): F !{}

export unsafe fn syscall0(number: usize): isize !{ io }
export unsafe fn syscall1<A>(number: usize, a: A): isize !{ io }
export unsafe fn syscall2<A, B>(number: usize, a: A, b: B): isize !{ io }
export unsafe fn syscall3<A, B, C>(number: usize, a: A, b: B, c: C): isize !{ io }
export unsafe fn syscall4<A, B, C, D>(number: usize, a: A, b: B, c: C, d: D): isize !{ io }
export unsafe fn syscall5<A, B, C, D, E>(number: usize, a: A, b: B, c: C, d: D, e: E): isize !{ io }
export unsafe fn syscall6<A, B, C, D, E, F>(number: usize, a: A, b: B, c: C, d: D, e: E, f: F): isize !{ io }
```

`cast`, `slice_from_raw`, `env_of`/`with_env` and the `syscall` functions
are answered by lowering; each `syscall` is the METALS `syscall`
instruction, and its arguments must be integers, `bool`s or pointers. `ptr_from_int` is
Gyro over `cast` and a private intrinsic, `ptr_to_int` and the rest are
runtime functions: METALS has no instruction that makes a pointer of an
integer.

### `cstring`

C's text is a pointer to bytes that end at a NUL; Gyro's is a length and its
bytes. Written in Gyro over `unsafe`, and the whole crossing
([chapter 15](ch15-00-c-interop.md)):

```gyro
export fn c_string(a: Allocator, text: string): Array<u8> !{}
export unsafe fn c_data(bytes: Array<u8>): *i8 !{}
export unsafe fn from_c(a: Allocator, text: *i8?): string !{}
```

`c_string` copies the text and a NUL into an array that owns them, `c_data`
is the address of its first byte as the `char *` a C header spells `*i8?`,
and `from_c` copies a `char *` up to its NUL into a string made from `a`; a
null pointer is the empty string.

### `sys`

The Linux system calls the library is written over, with nothing between
them and the kernel. `fs`, `os` and `time` call these; a program may too.

```gyro
export safe fn open(path: string, flags: usize, mode: usize): isize !{ io }
export safe fn close(fd: isize) !{ io }
export safe fn read(fd: isize, buffer: u8[]): isize !{ io }
export safe fn write_all(fd: isize, bytes: u8[]): isize !{ io }
export safe fn write_text(fd: isize, text: string): isize !{ io }
export safe fn access(path: string): isize !{ io }
export safe fn unlink(path: string): isize !{ io }
export fn errno_text(code: isize): string !{}
export fn fail(call: string, path: string, result: isize): SysError !{}
export struct SysError { export text: string }
```

Each call answers the kernel's result: zero or more on success, a negated
error number on failure. `errno_text` words an error number the way the C
library's `strerror` does with the first letter lowered, and `fail` builds
the error `fs` reports from it: `open /tmp/x: no such file or directory`.
The module also exports the system call numbers and flag values it uses.

### `net`

```gyro
export async fn get(url: string): (string, error?) !{ io }
```

## What is runtime-backed and what is Gyro

Runtime intrinsics: `print`/`println`/`eprint`/`eprintln`, a private
`start_thread` under `spawn`, `Mutex<T>`/`MutexGuard<T>`, `mem`'s `size`,
`align` and `panic`, `Format`'s `show`, the `unsafe` module's `raw_alloc`,
`raw_free`, `raw_resize`, `copy_bytes`, `offset`, `ptr_to_int`, `text_seal`
and `text_bytes` (`cast`, `slice_from_raw`, `env_of`, `with_env` and the
`syscall` functions are lowering's), `to_f64`, the `os` and `time` surfaces
and `net::get`. The `math` functions are METALS instructions, and so are
the float conversions: the backend emits them in line.

Ordinary Gyro: `Array<T>`, `String` and its UTF-8 encoder, `from_utf8`,
`bytes` and `runes`, every text method, `Format`'s methods and `mem`'s
`format`, `Map<K, V>`, all of `fmt`, `mem`'s `alloc`, `free`, `heap` and
`Arena`, the integer parsers of `convert`, all of `fs` and `sys`, and the
leaves lowering reaches for a number shown, a string compared and a word
hashed.

**Prefer Gyro.** Something with no interesting runtime requirement written
into the C runtime instead is a second language to maintain it in, for
nothing ([chapter 17.1](ch17-01-the-backend.md)).
