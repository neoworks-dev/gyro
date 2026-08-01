# METALS

METALS is the shared intermediate representation that Gyro compiles into,
and that other language front ends (JavaScript, Python, ...) will compile
into, so that `import` can reach across languages. One format, many
producers, several consumers: the verifier, the METALS interpreter (to
come), and the native backend (to come).

This document is the source of truth for the format. The Go definitions
live in `internal/metals`; the text format below is what the printer emits
and the parser accepts, and every construct here exists in code.

## The eight decisions

Settled in review; recorded with rationale in
[`decisions.md`](decisions.md).

1. **Typed module IR.** METALS keeps modules, imports/exports, function
   signatures and declared type shapes — the JVM/CLR shape, not LLVM's.
   Interop is native to the format instead of bolted on.
2. **Registers + structured control flow.** Instructions write named
   virtual registers; control flow is `block`/`loop`/`br`/`if`/`switch`,
   never arbitrary goto. Registers are *not* SSA: reassignment is legal
   but a register's type is fixed at first assignment. The native backend
   SSA-constructs internally.
3. **No generics.** Front ends monomorphize before METALS; the IR only
   ever sees concrete types. A generic function crossing a language
   boundary is exported as concrete instantiations or with a `dyn`
   signature.
4. **`dyn` is the one escape hatch.** METALS is statically typed with a
   first-class dynamic value type and checked conversion ops. A Python
   function can export a typed signature or a `dyn` one.
5. **Copy at the boundary.** Values crossing between language runtimes
   are converted/copied; no shared mutable memory between runtimes.
   Pointers, channels and futures cannot appear in a cross-boundary
   signature. Long-lived foreign state is an opaque `handle`.
6. **Concurrency is hybrid**, the same line Gyro's grammar draws:
   `chan<T>` is an IR type and `send`/`recv`/`select`/`await` are
   verified instructions, while `spawn`, mutexes and the rest are runtime
   intrinsics — ordinary imported functions.
7. **Verified from day one.** METALS cannot trust its producers the way
   Gyro's backend trusts Gyro's checker; third parties will emit it. The
   verifier type-checks every register and instruction and enforces the
   boundary rules.
8. **Text first.** A canonical text form (this document's syntax) with a
   printer and parser, so IR is readable and diffable in tests. A binary
   encoding comes later and encodes the same tree.

## Module anatomy

```metals
module "gyro:examples/structs"

import "gyro:rt" {
  fn @spawn(fn())
  fn @println_str(str)
}
import "js:./widgets" {
  fn @render(dyn) -> dyn
}

type %Point = record { x: i32, y: i32 }
type %Shape = enum { Circle(f64), Rect(f64, f64), Point }

global @MAX: i32 = 100

export fn @add(%a: i32, %b: i32) -> i32 {
  %sum = iadd %a, %b
  return %sum
}
```

- A module has a name, imports grouped by source module, named type
  declarations, constant globals, and functions.
- `@name` is a function or global; `%name` is a register or a named type;
  `$name` is a control-flow label. The three sigils keep the namespaces
  from colliding.
- A name after a sigil is a bare word, or a quoted string when it carries
  characters a word cannot: monomorphization and module qualification
  produce names like `@"Pair<i32>::sum"`, `@"list/util::helper"` and
  `%"Pair<i32>"`. Both spellings name the same namespace; the printer
  quotes exactly when it must.
- Globals are constants (literals only). A front end lowers module-level
  initialization into an ordinary function it arranges to run first.
- Module names carry the producing language as a scheme prefix
  (`gyro:`, `js:`, `py:`); `metals:` is reserved. Runtime intrinsics are
  imports from runtime modules such as `gyro:rt`, not IR constructs.

## Types

| form | meaning |
| --- | --- |
| `i8 i16 i32 i64 u8 u16 u32 u64 usize` | integers |
| `f32 f64` | floats |
| `bool rune str` | other primitives; `str` is an immutable string value |
| `ptr<T>` | pointer, never null, never crosses a boundary |
| `opt<T>` | nullable |
| `slice<T>` | borrowed view |
| `array<T, N>` | fixed-size array value |
| `tuple<A, B, ...>` | tuple value |
| `chan<T>` | channel |
| `future<T>` | un-awaited async result |
| `fn(A, B) -> R`, `async fn(A) -> R` | function values |
| `%Name` | module-local record or enum |
| `dyn` | dynamically typed value, converted with checked ops |
| `handle` | opaque foreign reference; compares by identity only |

A function's result is absent for "returns nothing". Multiple results are
one tuple, matching Gyro.

Named types are module-local in v0. A record shape that must cross a
boundary is redeclared by the importer and matched structurally; the
linker (to come) enforces the match.

## Registers

- Parameters are registers, named in the signature.
- `%r = op ...` defines or reassigns a register. The first assignment
  fixes the type; every later assignment must produce the same type.
- A register must be assigned on every path before it is read. The
  verifier tracks definedness through the structured tree: after an
  `if`/`else`, only registers assigned in both branches count.

## Instructions

Every instruction is one line, except the structured ones, which nest.

### Constants and moves

```metals
%r = const.i32 42          ; const.<prim> for every primitive
%r = const.str "text"
%r = const.rune 'a'
%r = const.bool true
%r = const.null opt<i32>   ; the null of a specific optional type
%r = copy %x
```

### Arithmetic, bitwise, comparison

```metals
%r = iadd %a, %b     ; isub imul idiv irem — checked: overflow and /0 abort
%r = wadd %a, %b     ; wsub wmul — two's-complement wraparound
%r = fadd %a, %b     ; fsub fmul fdiv
%r = band %a, %b     ; bor bxor shl shr
%r = neg %x          ; bnot (integers), not (bool)
%r = eq %a, %b       ; ne — any comparable type, -> bool
%r = lt %a, %b       ; le gt ge — numeric and rune, -> bool
%r = conv %x -> u8   ; `as`: widening succeeds, narrowing aborts on loss
```

Integer operands must share one type; there are no implicit conversions in
the IR either.

### Memory

```metals
%p = slot i32        ; a stack cell, for locals whose address is taken
%p = new %Point      ; heap cell, uninitialized
free %p
%v = load %p
store %p, %v
%q = field.ptr %p, 1 ; ptr<record> -> ptr<field 1's type>
```

### Records, enums, tuples, arrays

```metals
%r = record %Point(%x, %y)        ; every field, in declared order
%v = field.get %r, 0
field.set %r, 0, %v               ; mutates the register's value in place
%e = variant %Shape, 1(%w, %h)    ; variant index, payload
%t = variant.tag %e               ; -> i32
%v = variant.field %e, 1, 0       ; variant index, payload index
%t = tuple(%a, %b)
%v = tuple.get %t, 0
%a = array<i32>(%x, %y, %z)
%s = slice.of %a                  ; borrow, shares storage
%v = index.get %s, %i             ; bounds-checked, aborts out of range
index.set %s, %i, %v
%n = len %s                       ; -> usize
%r = str.concat %a, %b
```

Derived printing is not an IR concept: the front end generates an
ordinary `-> str` function per type and calls it.

### Optionals

```metals
%o = opt.wrap %v            ; T -> opt<T>; optionals do not nest
%v = opt.unwrap %o          ; opt<T> -> T, aborts on null
```

A null test is `eq`/`ne` against a `const.null` of the same optional
type. A front end whose checker proves a value non-null (Gyro's
narrowing) still emits `opt.unwrap`; the abort is that guarantee made
explicit, in the same trapping style as `idiv` and `index.get`.

### Calls

```metals
%r = call @f(%a, %b)
%f = fn.ref @f
%c = closure @lifted, %env    ; binds the env record as @lifted's first
                              ; parameter; %c's type drops that parameter
%r = call.indirect %f(%a)
return %v                     ; plain `return` for a void function
abort "index out of range"
```

Closures are a lowering pattern, not magic: the front end lifts the body
to a top-level function taking an environment record, and `closure`
partially applies it.

### Control flow

```metals
if %c {
  ...
} else {
  ...
}

block $done {
  loop $top {
    br_if %stop, $done
    br $top
  }
}

switch %tag {
  case 0 { ... }
  case 1 { ... }
  default { ... }
}
```

`br` and `br_if` target an enclosing label: a `block` label exits past
its end, a `loop` label re-enters its top. No other jumps exist.

### Concurrency

```metals
%ch = chan.new<i32> %cap    ; omit %cap for unbuffered
send %ch, %v                ; aborts on a closed channel
%t = recv %ch               ; -> tuple<T, bool>: (element, open)
chan.close %ch
%v = await %fut             ; only inside an async fn

select {
  recv %v = %ch { ... }     ; %v is bound inside the arm
  send %ch2, %x { ... }
  default { ... }
}
```

`spawn`, mutexes and everything else library-shaped are imports from a
runtime module, called like any function.

### dyn

```metals
%d = dyn.wrap %v            ; any copyable value -> dyn
%v = dyn.unwrap %d -> i32   ; checked; aborts on the wrong shape
%r = dyn.call %d(%a, %b)    ; -> dyn
%v = dyn.get %d, "name"     ; -> dyn
dyn.set %d, "name", %v
```

## Boundary rules

A signature is *boundary-crossing* when it is exported, or imported from
a module with a different language scheme. The verifier enforces, on
every parameter and result of such a signature:

- allowed: primitives, `str`, `opt`, `tuple`, `array`, `slice`, records
  and enums, `fn` (the runtime shims it), `dyn`, `handle`;
- forbidden: `ptr`, `chan`, `future`, and any type containing one.

Crossing copies: the caller's runtime converts arguments into the
callee's world and the result back. `slice` crosses as a copy of its
contents. Shared mutable state across runtimes does not exist; a foreign
object you must keep alive is a `handle`, and its operations are imported
functions taking that handle.

## Verification

`metals.Verify` checks a module with no knowledge of its producer:

- every named type, function, global and label resolves;
- every register: assigned before read on all paths, one type forever;
- every instruction's operand and result types, per the rules above;
- `await` only inside `async fn`; `select`/`send`/`recv` only on channel
  types; `return` matches the declared result;
- boundary rules on every exported and cross-scheme imported signature.

Linking — matching an importer's declared signatures against the
exporter's actual ones, structurally matching shared record shapes — is a
separate pass that arrives with the METALS loader.

## Still open

- What METALS expands to. The name predates the design.
- The linking and loading model: how a program of mixed-language METALS
  modules is assembled, and where signature mismatches surface.
- The binary encoding.
- How foreign runtimes embed: a JS engine and the Gyro runtime in one
  process is a runtime problem the IR only gestures at via `dyn` and
  `handle`.
- Effect rows: Gyro parses them, nothing checks them; whether they
  belong in METALS signatures is undecided.
- `syntaxfn`, the METALS-facing parser-extension feature from the book.
