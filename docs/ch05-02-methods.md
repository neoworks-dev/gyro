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

A type's own methods are not written in an `impl` block: each is a
free-standing `fn`, and writes its type's name out. An `impl` block is for
the methods a type provides to keep a promise to a **trait**
([chapter 10.1](ch10-01-traits.md)); those methods are ordinary methods of
the type afterwards. Inside a method, `Self` is another way to write the
owner's name, as in `fn Stack::new(): Self`.

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
unambiguous. A trait cannot be implemented for a built-in type, so there is
no second place a built-in type could have gained a method from.

Being a method, an extension cannot be called without a receiver.

Most of the standard library's string surface is exactly this: `split`,
`trim`, `contains`, `starts_with`, `ends_with`, `index_of`, `substring` and
`repeat` are ordinary Gyro extension methods on `string`
([chapter 8.2](ch08-02-strings.md)).

## The compiler's own surface wins

`len`, `runes`, `bytes`, `close`, `compare`, `hash`, `eq`, `message` — these
are part of the type rather than an addition to it. They need no import, and an
extension of the same name does not displace them.

`void` and `error` cannot be extended: nothing has type void, and `error` is
a contract satisfied by whatever carries `message()`, not a type.

## Methods and traits

A promise to other code is a trait, and the methods that keep it are written
in an `impl` block ([chapter 10.1](ch10-01-traits.md)):

```gyro
impl Ord for Version {
  fn compare(self, other: Version): i32 {
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
}
```

Having written it, `Version` is `Ord`, which means it works with `<`, and in
any generic function bounded by `Ord`:

```gyro
export fn max<T: Ord>(a: T, b: T): T !{ &a, &b } {
  if a.compare(b) > 0 {
    return a
  }
  return b
}

max(older, newer)    // fine
max(3, 7)            // fine: i32 is Ord
```

The same pattern runs through the language. `impl Iterator<T>` makes a type
work in `for..in`, `impl Print` with a `to_string` controls its own
printing, `impl Hash` with a `hash` replaces the derived one — and takes on
keeping it consistent with `Eq`. The one contract still met by shape is
`error`, which is any type with `message(): string`.

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
