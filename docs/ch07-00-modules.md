# 7. Modules and imports

**One file is one module.** There is no separate module declaration, no
package statement, and no directory-level unit.

## Visibility

Visibility is always explicit. A declaration is private to its module unless
marked `export`. Nothing about a name's spelling changes that — there is no
capitalization rule.

```gyro
fn helper() {}     // module-private
export fn run() {} // visible to importers

export { a, b as c }    // standalone export list
export { a } from "./m" // re-export
export * from "./m"
```

Struct fields carry their own `export` ([chapter 5.1](ch05-01-structs.md)).

A trait is exported and imported like a type, by name. An `impl` is not
exported: it belongs to the type it is for, and every module that sees the
type sees what it implements. The capability traits — `Eq`, `Hash`, `Print`,
`Ord` and the rest — are ordinary exports of `stdlib` that a module imports to
write `impl Eq for Point` ([chapter 10.1](ch10-01-traits.md)).

## Imports

```gyro
import { println } from "stdlib"
import { open as fopen } from "fs"
import * as ast from "ast"
import "./side-effects"
```

`./util` resolves to `util.gyro` beside the importing file. A **bare name**
is the standard library, falling back to a package under `.stack/` once the
Stack package manager exists. Circular imports are rejected, with a trace of
the cycle.

There is **no default export**, and therefore no `import name from "path"`.
A name is spelled the same where it is defined and everywhere it is used —
renaming is possible, but it has to be written as `as`.

`from` is contextual, the way `move` and `copy` are: it separates an import
list from its path and is an ordinary identifier everywhere else, so
`String::from` and `var from = 1` both parse.

## No universe scope

**If a name is not part of the formal syntax, it has to be imported.** There
are no predeclared functions.

Operations on built-in types are *methods* on those types, so `ch.close()`,
`xs.len()` and `s.len()` need no import, while `println`, `alloc` and
`spawn` do. Extension methods sit on the imported side of that line: they
are module-scope names that happen to take a receiver
([chapter 5.2](ch05-02-methods.md)).

Importing a name a module does not export is an error, and so is importing
two modules that export the same name — which is exactly what makes two
competing `string::trim` extensions a diagnostic rather than a silent
winner.

## The entry point

`fn main` in the root module is the one blessed name: the compiler finds it
and the OS calls it, so it needs no `export` — nothing imports it.
[Chapter 1.3](ch01-03-hello-world.md) has its signatures.

## Whole-program compilation

Gyro compiles whole programs. `gyro check` loads the entire import graph
before checking anything, and that has consequences you can rely on:

- a rowless imported function's effects are **read from its body**, in
  whatever module that body lives in, transitively — not assumed
  ([chapter 12](ch12-00-effect-rows.md))
- the set of types any `as any` in the program mentions is known at compile
  time, so `any` is a closed set rather than a runtime type registry
  ([chapter 14.1](ch14-01-any.md))
- generics are monomorphized: every instantiation is a separate function in
  the output ([chapter 10](ch10-00-generics.md))

## Module-level state

A module-level `const` is a compile-time constant. A module-level `var` is
program storage, zero-initialized, whose initializer runs before `main` — in
declaration order within a module, and in dependency order across modules,
so a module's storage is written after everything it imports has written its
own.

Those initializers run outside any effect row.

## The standard library

The bundled modules are `stdlib`, `mem`, `unsafe`, `fs`, `net`, `convert`,
`time`, `os`, `fmt` and `math`. They are ordinary Gyro source compiled into the
compiler binary, and the rule inside them is the same as anywhere: a
declaration **with a body** is ordinary Gyro, and one **without** is a
runtime intrinsic the tiers implement.

That split is deliberate — most of the standard library is Gyro, because two
native implementations of something with no interesting runtime requirement
are two places to diverge. `String`, `Map<K, V>`, the text methods and the
whole of `fmt` are Gyro over `Array<T>` and one intrinsic.
[Appendix C](appendix-c-stdlib.md) lists the surface.
