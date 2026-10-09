# 14.2. Printing and formatting

**Every type is printable.** `println(x)` and `#{x}` use a compiler-derived
representation unless the type has its own `to_string(): string` method,
which then wins.

```gyro
println(Point{ x: 1, y: 2 })    // Point{ x: 1, y: 2 }
```

Primitives print natively. A struct prints its fields, a tuple its elements,
an enum its variant and payload. An `any` prints as the value inside it, so
a `...any` pack prints element by element with nothing unwrapped
([chapter 14.1](ch14-01-any.md)).

## Overriding it

Declare `to_string`:

```gyro
export fn Version::to_string(self): string {
  return "v#{self.major}.#{self.minor}"
}
```

From then on `println(v)` and `#{v}` use it. That is the same
displacement rule `hash` has: write your own and the derived one steps
aside, along with the responsibility for keeping it sensible
([chapter 5.2](ch05-02-methods.md)).

## The three printers

From `stdlib`:

| function | goes to |
| --- | --- |
| `print(value)` | stdout, no newline |
| `println(value)` | stdout, newline |
| `eprintln(value)` | stderr, newline |

Each is generic over one value, so `println` takes anything.

## Interpolation

`#{expr}` embeds arbitrary Gyro in a string literal, printing the result the
way `println` would:

```gyro
println("max is #{MAX}, half of that is #{MAX / 2}")
```

Escape the sequence as `\#{` to write it literally. Interpolation builds new
text, which comes from the process heap. That is not an effect, so a
function that interpolates needs nothing in its effect row
([chapter 12](ch12-00-effect-rows.md)).

## `printf`, in the `fmt` module

```gyro
import { printf } from "fmt"

printf("%-6s has %2d items, %d%% of %x\n", "cart", 3, 40, 255)
```

| function | |
| --- | --- |
| `printf(format, args: ...any)` | to stdout |
| `eprintf(format, args: ...any)` | to stderr |
| `sprintf(format, args: ...any): string` | returns the text |

The verbs:

| verb | prints |
| --- | --- |
| `%v` | the value's own representation, whatever it holds |
| `%s` | text — a string, or any other value's representation |
| `%d` | a signed or unsigned integer, in decimal |
| `%x`, `%X` | an integer in lowercase / uppercase hex |
| `%c` | an integer as a code point, or a rune |
| `%b` | a bool |
| `%%` | a literal percent |

Between the `%` and the verb: an optional `-` to left-align, an optional `0`
to pad with zeros instead of spaces, and an optional decimal width. `%-4d`,
`%02x` and `%8s` all mean what they do in C.

Two deliberate behaviours:

- **A verb that does not fit falls back** to the value's own representation
  rather than panicking. A format string is data, and a mismatch in it
  should not take the program down.
- **A missing argument prints `%!(MISSING)`** rather than reading past the
  end of the pack.

`%x` of an `i32` `-1` is `ffffffff` — the value in two's complement masked
to its own width, the way C prints it, not the 64-bit form.

## `fmt` is ordinary Gyro

There is no formatting machinery in any tier. `sprintf` walks the format
string over an `...any` slice and reads each argument with a `match`, which
is the only way to get a concrete type back out of an `any`. The whole
formatter is one module, so no backend implements it and no two tiers can
disagree about it.

That is the general rule the standard library follows: implement in Gyro
whatever does not require runtime support, because two native
implementations of something with no interesting runtime requirement are two
places to diverge ([chapter 7](ch07-00-modules.md)).

## Formatting matches across tiers

Numbers print identically on every tier, down to the float formatting:
shortest round-trip, with Go's exponent thresholds (`12.56636`, `12`,
`1e+21`, `1e-05`). The evaluator gets that from Go's `strconv`, and the C
runtime reproduces it deliberately ([appendix E](appendix-e-native-backend.md)).
It is not a coincidence you may rely on by accident — it is a requirement
the corpus checks.
