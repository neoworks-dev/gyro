# Appendix A. Keywords and contextual identifiers

## Keywords

These are reserved: they are tokens the lexer produces, and none may be used
as an identifier.

```text
var    const   fn      struct  union
enum   return  if      elif    else
while  for     in      match   break
continue defer async   await   chan
select import  as      export  self
with   true    false   null    _
```

`_` is in the list because it is not a name: it discards.

There is no `impl`, no `trait`, no `switch`, no `goto`, no `loop`, no `type`
and no `int`. Traits and `impl` blocks were removed
([chapter 1.1](ch01-01-what-is-gyro.md)); the rest never existed.

## Contextual identifiers

`move`, `copy` and `from` are **not** keywords. Each is recognized by the
parser in exactly one position and is an ordinary identifier everywhere
else:

| word | its one position |
| --- | --- |
| `move` | a closure capture list: `fn[move node]() { ... }` |
| `from` | between an import list and its path: `import { x } from "m"` |

`copy` is in the same family and has no special position at all: capture
lists hold only `move name` entries, because copies are implicit and never
listed ([chapter 11](ch11-00-closures.md)). It is an ordinary identifier,
and must stay one.

So `var from = 1` and `fn String::from(...)` both parse. If a word only
means something in one place, it belongs here rather than in the token set —
this has been broken twice.

## Reserved but unused

`->` and `=>` are reserved tokens with no meaning today.

## Names that are not keywords and look like they might be

None of these is part of the grammar. Each is either an imported name or a
method on a built-in type:

| name | what it is |
| --- | --- |
| `println`, `print`, `eprintln` | imported from `stdlib` |
| `alloc`, `free`, `size`, `align`, `allocator`, `heap`, `Arena` | imported from `mem` |
| `spawn` | imported from `stdlib` — a function taking a closure, not grammar |
| `Array`, `String`, `Map`, `Mutex` | imported types |
| `len`, `runes`, `bytes`, `close`, `compare`, `hash`, `message`, `to_string` | the built-in method surface |
| `trim`, `split`, `contains`, … | extension methods, imported from `stdlib` |

**If a name is not part of the formal syntax, it has to be imported**
([chapter 7](ch07-00-modules.md)). There is no universe scope and no
predeclared functions.

## Decorators

`@name` or `@name(args)`, attachable to any declaration; arguments may be
named. The names are a **fixed set known to the compiler**, because they
configure compilation itself and there is nothing meaningful to import. An
unknown decorator name is a compile error.

| decorator | state |
| --- | --- |
| `@extern("lib")`, `@extern("lib", symbol = "name")` | implemented ([chapter 15](ch15-00-c-interop.md)) |
| `@comptime` | typed, not evaluated |
| `@gpu` | parsed and typed |
| `@concurrent(threads = N)` | parsed and typed |
| `@distributed` | parsed and typed |

```gyro
@comptime const FIB_10: i32 = fib(10)
@gpu fn kernel(a: *f32) {}
@concurrent(threads = 8) fn work() {}
@extern("m") fn sqrt(x: f64): f64 !{}
```
