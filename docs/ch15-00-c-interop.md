# 15. C interoperability

There are two ways to reach C, and one front end behind both: Gyro reads C
itself, with its own preprocessor and parser, and what it reads becomes the
same METALS everything else becomes.

| route | what it is | when |
| --- | --- | --- |
| `import { f } from "pkg:sdl3/SDL3/SDL.h"` | read a library's header and call the library it declares | the source is not yours: `libc`, `libm`, SDL3 |
| `import "./file.c"` | compile the C **into your program** | the source is yours, or vendored |

The first calls across a boundary of *representation*: the library is a
shared object, its functions are called in C's ABI, and its types are the
ones its header declares. The second has no boundary to cross: the C is
compiled and linked into the same module as your Gyro code, so a call is an
ordinary call.

## Importing a header

```gyro
import safe { SDL_Init, SDL_Quit, SDL_INIT_VIDEO } from "pkg:sdl3/SDL3/SDL.h"
import safe { sqrt, ldexp } from "<math.h>"
import { strlen } from "<string.h>"
```

The specifier is the header. `pkg:<package>/<path>` names a
[pkg-config](https://www.freedesktop.org/wiki/Software/pkg-config/) package
and a header inside one of its include directories. The directories and
definitions the header is read with come from `pkg-config --cflags
<package>`, and the library the program is linked against comes from
`pkg-config --libs <package>`, so the program states what it depends on in
the line that uses it. `<path>` is a header on the system compiler's
include path, which is where the C library's headers are; libc and libm are
always linked, so it needs no package.

There is nothing to declare. The header is the declaration: the front end
reads it as it would a `.c` file, and the import receives the signatures
and types the header says. A program that calls `SDL_CreateWindow` with the
wrong number of arguments is told what SDL3's header says it takes.

### What an import brings in

An import names what it uses, and gets those names and whatever their
signatures mention. A header like `<SDL3/SDL.h>` declares two thousand
things; the rest are read and dropped. Four kinds of name cross.

| the header declares | the import receives |
| --- | --- |
| a function | an `unsafe fn` with the header's signature, called in C's ABI |
| a struct or a union, by tag or by the typedef that names it | that type, with the header's members in the header's layout |
| a struct tag the header never completes | a fieldless struct: a handle you may hold and pass back, and not look inside |
| an enumerator | a constant of type `i32`, which is the type C gives it |
| an object-like macro that is an integer constant expression | a constant of the type C's arithmetic gives the expression |

`SDL_INIT_VIDEO` is a macro, `0x00000020u`, and arrives as a `u32` equal to
32. `SDL_EVENT_QUIT` is an enumerator and arrives as an `i32` equal to 256,
so comparing it with the `u32` tag of an `SDL_Event` takes an `as`. C's
`size_t` and `uintptr_t` are spelled `usize`, which is the same width.

What does not cross is refused at the name that asked for it, with the
reason: a variable, a variadic function, a function taking a `va_list` or a
`long double`, a function whose header gives it a different assembler
symbol, a function the header defines as an inline, a typedef of a scalar,
and a macro that is not an integer constant. `import * as` of a header is
refused as well, since it would ask for everything. A name the header does
not declare at all is the same error.

All the headers of one library are one translation unit, as they are to a C
compiler. `<stdlib.h>` and `<arpa/inet.h>` are read together, so a struct
both of them mention is one type, and so are the headers of one package. A
name is found if any header of the library declares it, whichever one the
import said.

### Calling it is unsafe unless the import vouches

A function imported from a header is **unsafe to call**, as one from a `.c`
file is: the front end checks the signature, and nothing about what the C
does with it. `import safe` vouches for every name the line binds, and a
call then needs no `unsafe fn` around it ([chapter 4.6](ch04-06-unsafe.md)):

```gyro
import safe { sqrt } from "<math.h>"   // no f64 misuses it
import { strlen } from "<string.h>"    // reads through an address it is trusted with
import { heap } from "mem"
import { c_string, c_data } from "cstring"

safe fn length_of(text: string): usize {
  var bytes = c_string(heap(), text)   // from "cstring", below
  defer bytes.free()
  return strlen(c_data(bytes))
}
```

The types a header declares need no vouching.

### What crosses

A linked library is **not a second heap**. It runs on this thread, in this
address space, over the same allocator the Gyro runtime itself calls. What
separates it from Gyro is *representation* — C has real widths where a
METALS register is a qword — and converting that is all the boundary does.
So an address crosses as itself, a struct crosses as the same bytes, and
what does not cross is a Gyro runtime object, which C has no spelling for at
any width.

Because the signatures are C's, the types are C's:

| C | Gyro |
| --- | --- |
| `char`, `short`, `int`, `long`, and their unsigned forms | `i8`, `i16`, `i32`, `i64`, `u8`, `u16`, `u32`, `u64` |
| `_Bool`, `float`, `double` | `bool`, `f32`, `f64` |
| `size_t` | `usize` |
| `T *` | `*T?`, a pointer that may be null |
| `void *` | `anyptr?` |
| `const char *` | `*i8?`, a pointer to a byte |
| `struct S`, `union U` | the struct or union, by value |
| `R (*)(A, B)` | `code(A, B): R?` |

A Gyro `string` is not a `char *`, and C's text is not a Gyro `string`: one
is a length and its bytes and the other is bytes up to a NUL. The `cstring`
module is the whole crossing, written in Gyro ([appendix C](appendix-c-stdlib.md)):
`c_string` copies a string and its NUL into an array, `c_data` is the
address C reads, and `from_c` copies a `char *` back out.

```gyro
import { heap } from "mem"
import { c_string, c_data, from_c } from "cstring"
import safe { SDL_CreateWindow, SDL_GetError } from "pkg:sdl3/SDL3/SDL.h"

safe fn open_window(title: string): *SDL_Window? {
  var bytes = c_string(heap(), title)
  defer bytes.free()
  return SDL_CreateWindow(c_data(bytes), 800, 600, 0)
}

safe fn failure(): string {
  return from_c(heap(), SDL_GetError())   // a copy: SDL reuses its buffer
}
```

A returned `char *` the library owns is only good until the library's next
call, which is why `from_c` copies; one the caller owns is freed by whatever
the library says frees it. **Nothing checks what a library does with an
address**, which is the same thing that is true of C.

### Out-parameters and callbacks

A pointer is the address itself, so an out-parameter is a pointer to a Gyro
variable, and a callback is the address of a Gyro function:

```gyro
import safe { frexp } from "<math.h>"
import safe { qsort } from "<stdlib.h>"

safe fn ascending(a: anyptr?, b: anyptr?): i32 {
  if a == null || b == null { return 0 }
  var left = cast<i32>(a)
  var right = cast<i32>(b)
  if *left < *right { return 0 - 1 }
  if *left > *right { return 1 }
  return 0
}

var exponent: i32 = 0
var fraction = frexp(1024.0, &exponent)    // 0.5, and exponent is 11

var xs = [5, 3, 9, 1, 7]
qsort(&xs[0] as anyptr, 5, 4, &ascending)  // C's loop, this program's order
```

`&xs[0]` is the element's own address, and the stride C walks is the one
Gyro laid out: an `i32` is four bytes of storage
([chapter 4.1](ch04-01-manual-memory.md)). A `fn` is refused where a `code`
is accepted, because a `fn` is code *and* the environment it captured,
which is two words, and a C function pointer has room for one. `&f` on a
declared function is the code pointer.

### Structs, unions and handles

A struct crosses by value, in both directions, because the header's struct
and the Gyro struct the import declares are one type: the same members in
the same order, laid out the way C lays them out. Nothing is converted, and
`div` returns the `div_t` `<stdlib.h>` declares.

A union crosses as the type C declared, as described [below](#importing-a-c-file):
every member at offset zero, built with a literal that names one member
(`SDL_Event{ type: 0 }`), and read inside an unsafe function.

A struct the header declares and never defines, as SDL does for `SDL_Window`,
arrives as a fieldless struct and is only ever held through a pointer. A
`SDL_Window *` and an `SDL_Renderer *` are different types, so handing a
renderer to a function that wants a window is an error here and a crash in C.

### The reference

`examples/triangle.gyro` is the reference: a Gouraud-shaded triangle
rasterized in pure Gyro and put in an SDL3 window, with SDL reached only
through `pkg:sdl3/SDL3/SDL.h`.

### What it costs

A header is read when the program is compiled, which for SDL3 is about a
second and a half, and reading it needs the system's C compiler (to say where
the headers are) and, for a package, `pkg-config`. The front end targets
GCC's x86-64 without defining `__GNUC__`, so glibc and SDL take the
portable paths they keep for other compilers; a construct it cannot read is
dropped from the header and reported if a name asked for it. Attributes
that change a layout (`packed`, `aligned`) are refused by name.

A function the header defines as an inline cannot be imported, because a
header's bodies are not compiled; a variadic function cannot be called,
because a native callee reads its extra arguments from registers and the
stack and a Gyro call has no way to lay them out
([appendix G](appendix-g-known-gaps.md)).

## Importing a `.c` file

```gyro
import safe { add, divmod } from "./mathlib.c"
```

The loader files `mathlib.c` as a foreign module the way it does a Gyro one:
Gyro's own C front end reads the C — its own preprocessor included, so
`#include` works and a header is a header — and emits METALS with C's
declared types. `struct Point *` arrives as a pointer to a `%Point`, not as
an integer.

`#include <stdlib.h>` finds the system's header, through the include path
the system's C compiler reports, so the file may call `malloc` and `sqrt`
with the signatures libc declares.

A function imported from a `.c` file is **unsafe to call** unless the import
vouches for it, as `safe` does above: the C behind it can corrupt memory
even though its signature is checked. One word on the import line keeps the
trust visible without marking every call, and it covers every name the line
binds ([chapter 4.6](ch04-06-unsafe.md)). Without it a call needs an
`unsafe fn` or a `safe fn` around it. `import safe * as lib from "./x.c"`
vouches for the namespace, and `safe` on an import of a Gyro module is an
error, since a Gyro function already says for itself whether it is unsafe.
The types a C file declares need no vouching.

A C file may call functions it declares and does not define. A prototype
for `malloc`, `memcpy` or `sqrt` is C's undefined symbol, and it is
resolved the way C resolves one: by name, when the program is linked,
against libc and libm. The call is made in C's ABI, so a variadic callee is
the one thing it cannot reach.

Because there is no boundary here, the restrictions above do not apply.
**Pointers cross**, so out-parameters work:

```gyro
import { alloc, free, heap } from "mem"
import safe { divmod } from "./mathlib.c"

var whole = alloc<i32>(heap())
var rest = alloc<i32>(heap())
defer free(heap(), whole)
defer free(heap(), rest)
divmod(47, 10, whole, rest)
println(*whole)
```

A C struct crosses as a Gyro struct — there is one record in the linked
program, not two that agree — and an incomplete type (`struct Session;`)
arrives as a fieldless struct, which is Gyro's spelling for a handle you may
keep and not look inside.

A C pointer is nullable where Gyro's is not, so dereferencing one that is
null **aborts with a message** where C leaves it undefined.

A C `union` crosses as the type C declared: every member at offset zero,
the size of the widest member at the strictest member's alignment. Gyro has
no `union` declaration of its own, so this is the only way one reaches a
program. It can be held, passed, and built with a literal that names
exactly one member (`Value{ i: 42 }`); members are written and read by
name, and writing one is writing the same storage as the others. Reading a
member needs an unsafe context ([chapter 4.6](ch04-06-unsafe.md)), because
nothing says which member is live; writing one, and writing a literal, are
safe.

**Nothing records which member is live**, which decides what else it does:
`==` and hashing have nothing to derive from, so a union is not a `Map`
key, and a struct holding one inherits both refusals. Printing one prints
the word `union`, and it cannot go into an `any`. Reading a member other
than the one last written reinterprets the bytes exactly as C does,
except across members that share an initial sequence, which is the read
every C tagged union is built on and which C defines.

A C `enum` does not cross as a type: Gyro's is a tagged sum and the two are
different things. A header's enumerators arrive as integer constants
([above](#what-an-import-brings-in)).

An export the generated header cannot spell is left out rather than made an
error, so one unspellable helper does not make a library unimportable;
naming a skipped one is an error at the import. A variadic C function is the
shape that cannot cross today ([appendix G](appendix-g-known-gaps.md)).

## `anyptr` — C's `void *`

`anyptr` is an address whose target's type has been forgotten. It is the
only pointer in Gyro that is not a pointer *to* something, so it has none of
a pointer's operations: nothing to dereference, nothing to step, nothing to
order, nothing to read. What it has is identity, and `as` in one direction:

```gyro
import { cast } from "unsafe"
import safe { Cell, keep, held } from "./store.c"

safe fn value_at(address: anyptr): i32 {
  return cast<Cell>(address).value   // remembering: this program's claim
}

var c = Cell{ value: 7 }
keep(0, &c as anyptr)          // forgetting: always true, never fails

var back = held(0)             // C's `void *` arrives as anyptr?
if back != null {
  println(value_at(back))
}
```

Forgetting is free and cannot be wrong, so it is an ordinary `as`.
**Remembering is a claim** about what is at the address, the same claim C's
own cast makes, and the native tiers do what C does when it is wrong. It is
`cast<T>(p)` from the `unsafe` module, an unsafe function, so a program
makes the claim inside an `unsafe fn` or a `safe fn`
([chapter 4.6](ch04-06-unsafe.md)). `p as *T` on an `anyptr` is not a
conversion in the language.

Only a pointer converts to an `anyptr`; an integer does not. A pointer made
from an integer is `ptr_from_int` in the same module, for the addresses a
program did not get from an allocation.

An `anyptr` cannot go into an `any`: an `any` promises to give the concrete
type back, and this one has thrown it away. It does cross to a native
function, because a linked library shares the address space the address
belongs to. Printing one prints the word `anyptr`.

It exists for C. Every registry, callback table and generic container in C
is written over `void *`, and until there was a spelling for one, none of
those functions could be imported at all.

## `code(..)` — C's function pointer

`code(A, B): C` is the address of a function and nothing beside it. `&f` on
a declared function is one:

```gyro
import { count_kept, fold } from "./filter.c"

fn big(v: i32): i32 {
  if v > 5 { return 1 }
  return 0
}

fn sum(acc: i32, v: i32): i32 { return acc + v }

var kept = count_kept(&big)      // C's loop, this program's decision
var total = fold(&sum, 0)
```

It is not a `fn` with a flag — [chapter 11](ch11-00-closures.md) explains
why the two are separate types, and which functions have no single address
to take.

A code pointer is called like anything else, compares by identity, and
cannot go into an `any`. Printing one prints the word `code`. It works in
both directions and by both routes: C's loop can run a Gyro function it was
handed, and a pointer C returns can be called from Gyro.

A function that allocates takes an `Allocator` as an ordinary parameter
([chapter 4.5](ch04-05-allocators.md)), so `&f` on one is an ordinary code
pointer whose type has that parameter in it. C has no way to pass an
`Allocator`, so a callback C is to call is written without one, and gets
the allocator it needs from wherever the program keeps it.

## Which route to use

Use a header import when the library is a `.so` you are linking against,
and `import "./x.c"` when you have the source.

The `.c` route is the stronger one, for reasons that have nothing to do
with what crosses: the call is **checked** — the linker compares your
declaration against the C the front end actually read — and the program
needs nothing installed to run it, because the C became METALS like
everything else ([appendix D](appendix-d-metals.md)). A header import
trusts that the library on the machine is the one the header describes,
which is what C itself trusts.
