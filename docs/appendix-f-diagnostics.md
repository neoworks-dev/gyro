# Appendix F. Diagnostics

## The shape of an error

```text
error[E0401]: undeclared name println
 --> hello.gyro:2:3
  |
2 |   println("hi")
  |   ^^^^^^^ not found in this scope
  |
  = help: "stdlib" exports println; add: import { println } from "stdlib"
```

A headline with its code, the location, the offending line, and the span
underlined. A second frame appears for a related position ("previous
declaration here"). Colour follows `NO_COLOR` and whether stderr is a
terminal.

**The message states the fact; the notes under it guess the fix.** The
message says only what is wrong — `undeclared type String` — and everything
about what you probably meant goes underneath. Explaining the language's
rules in the message itself was rejected: the rule is the same in every
occurrence of the error, so it carries no information about *this* one.

## What the notes guess

For an unresolved name, up to three notes in this order:

1. the primitive it is a foreign spelling of — `String` → `string`, `Int` →
   `i32`
2. the import that would bind it: a name added to an import already in the
   file, or a new import of a bundled module
3. that it is a method on a built-in rather than a free function — `len(xs)`
   → `xs.len()`
4. the nearest name in scope by edit distance

Member, variant, associated-function and missing-export errors carry the
same "did you mean" note over their own candidate set.

The import and method notes read the bundled module stubs directly rather
than a hand-maintained table, so a hint cannot drift from what `stdlib`
actually exports. A spelling guess appears only within one edit for short
names, two from five characters up, and never more than half the shorter
name — otherwise `x` would be offered as the fix for `z`.

A message repeated at one position is reported once.

## `gyro explain`

```sh
gyro explain E0401     # what one code means
gyro explain           # the whole catalogue
```

## The code ranges

| range | area |
| --- | --- |
| E01xx | lexical |
| E02xx | syntax |
| E03xx | modules and imports |
| E04xx | names |
| E05xx | types |
| E06xx | statements, borrows, effects, foreign functions |
| W07xx | memory (leaks) |
| W08xx | concurrency (races) |

Codes are **stable**: what fails with E0401 today fails with E0401 tomorrow.

A sample, so the ranges have a shape:

| code | meaning |
| --- | --- |
| E0105 | no such operator — there is no `++` or `--` |
| E0209 | generic bounds — there are none to write |
| E0218 | a function marked both `unsafe` and `safe` |
| E0303 | circular import |
| E0305 | not exported by that module |
| E0307 | a name that cannot be imported from a C header, with the reason |
| E0401 | undeclared name |
| E0501 | mismatched types |
| E0502 | literal out of range for its type |
| E0510 | a nullable used before it was checked |
| E0517 | an error result dropped |
| E0521 | a match over an enum that is not exhaustive |
| E0522 | a read of a variable no path has written |
| E0523 | a `main` with a signature nothing can call |
| E0607 | a private field used from another module |
| E0609 | a returned view, or an aggregate holding one, whose source the row does not name |
| E0610 | a returned view of this frame's storage |
| E0612 | an unknown entry in an effect row |
| E0630 | a view of this frame stored where it outlives the frame |
| E0631 | a value returned or stored past the arena it was allocated from |
| E0632 | a view written in a loop body stored outside the loop |
| E0633 | a value used after its arena was freed or reset |
| E0618 | an unsafe call or union read in a plain `fn` |
| E0619 | `import safe` of a Gyro module |
| W0701 | a possible leak |
| W0801 | a spawned closure sharing a reference |

The two `W` codes are **warnings on purpose**. An arena and a
process-lifetime allocation are both deliberate and neither frees, so a leak
cannot be an error; and the race analysis catches the direct case only, so
it reports rather than refuses ([chapter 13.1](ch13-01-threads.md)).

## Cascading diagnostics are real

One malformed construct legitimately produces more than one error. A
rejected row entry leaves the row empty, so the return is unaccounted for
too — two diagnostics, both correct. `return null, 0` against
`(i32, error?)` is two mismatches. These are not suppressed.

Errors from a generic land **in the generic's body**, with a related "in the
instantiation required here" note at the call site
([chapter 10](ch10-00-generics.md)).

## For contributors

Every new front-end diagnostic needs a `diag.Code`, mapped from its message
format in the emitting package's own `codes.go`. The catalogue and the
`gyro explain` text live in `internal/diag/codes.go`, and a test reads the
sources and fails when a reported message format has no entry — so
**rewording a message means updating the mapping and the catalogue**.

Error-recovery paths must still return structurally valid data: never return
a malformed or partially initialized structure because a diagnostic was
already emitted, because downstream stages must not panic on invalid source.

Diagnostics are exercised by the fixtures in
`internal/sema/testdata/check/`, where every emitted diagnostic must be
claimed by a marker and every marker must match
([chapter 17.3](ch17-03-testing.md)).
