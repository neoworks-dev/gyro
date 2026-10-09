# 5.2. Methods

A method is a **free-standing function whose name is qualified by its
type**. Structs stay pure data; behaviour lives beside it at top level, in
any file order.

```gyro
export struct Stack {
  export capacity: i32
  top: i32
}

export fn Stack::new(capacity: i32): Stack {
  return Stack{ capacity: capacity, top: 0 }
}

fn Stack::is_empty(self): bool {
  return self.top == 0
}

fn Stack::push(*self, v: i32) {
  self.top += 1
}
```

There is no `impl` block, no `trait`, and no `Self`: a method writes its
type's name out.

## Receivers

- `self` borrows the receiver immutably and **never copies it**, however
  large the struct is.
- `*self` takes a pointer and may mutate.
- No receiver at all makes it an **associated function**, called as
  `Stack::new(16)`.

Methods on a generic type name the parameter in the qualifier:
`fn Pair<T>::first(self): T`.

## Built-in types can be extended

```gyro
export fn string::shout(self): string {
  return format(heap(), "#{self}!")
}
```

An extension method is an **ordinary module-scope name**. It is exported and
imported like a function, and it is callable exactly where that name is in
scope:

```gyro
import { trim, split } from "stdlib"
import { heap } from "mem"

var clean = line.trim(heap())      // works because `trim` was imported
```

That is what keeps extensions out of universe scope. `line.trim(heap())` works
because `trim` is in scope here, not because some module somewhere declared
it. Two modules may both extend `string` with `trim`; importing both is the
duplicate-name collision Gyro already reports, and importing one is
unambiguous. This is also how coherence gets settled without traits.

Being a method, an extension cannot be called without a receiver.

Most of the standard library's string surface is exactly this: `split`,
`trim`, `contains`, `starts_with`, `ends_with`, `index_of`, `substring` and
`repeat` are ordinary Gyro extension methods on `string`
([chapter 8.2](ch08-02-strings.md)).

## The compiler's own surface wins

`len`, `runes`, `bytes`, `close`, `compare`, `hash`, `message` — these are
part of the type rather than an addition to it. They need no import, and an
extension of the same name does not displace them.

`void` and `error` cannot be extended: nothing has type void, and `error` is
a contract satisfied by whatever carries `message()`, not a type.

## Methods are how contracts are expressed

Operators never touch user types, so a comparison is a method:

```gyro
export fn Version::compare(self, other: Version): i32 {
  if self.major != other.major {
    if self.major < other.major {
      return -1
    }
    return 1
  }
  if self.minor < other.minor { return -1 }
  if self.minor > other.minor { return 1 }
  return 0
}
```

Having written it, `Version` is now ordered — which means it works in any
generic function whose body calls `compare`, with nothing declared:

```gyro
export fn max<T>(a: T, b: T): T {
  if a.compare(b) > 0 {
    return a
  }
  return b
}

max(older, newer)    // fine
max(3, 7)            // fine: i32 has a built-in compare
```

The same pattern runs through the language. Write `message(): string` and
your type is an `error`. Write `next(*self): (T, bool)` and it works in
`for..in`. Write `to_string(): string` and it controls its own printing.
Write `hash(): u64` and you have displaced the derived one — and taken on
keeping it consistent with `==`.

## Receivers in effect rows

`&self` names a method's receiver in an effect row, and a field chain roots
at whatever holds it:

```gyro
export fn Stack::view(self): i32[] !{ &self } {
  return self.items.slice()
}
```

`self.items` is the receiver's storage, so the row says `&self`, not
`&items` ([chapter 12](ch12-00-effect-rows.md)).
