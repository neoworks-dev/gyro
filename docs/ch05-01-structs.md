# 5.1. Structs and unions

A struct declares data and nothing else. Behaviour lives beside it, as
free-standing methods ([chapter 5.2](ch05-02-methods.md)).

```gyro
export struct Stack {
  export capacity: i32
  top: i32
  items: Array<i32>
}
```

Visibility is per declaration **and** per field. `export` on the struct
makes the type visible to importers; `export` on a field makes that field
visible. A field without it is private to the module, whatever the struct's
own visibility is.

## Literals come in two forms

```gyro
Point{ x: 1, y: 2 }   // named
Pair<i32>{ 1, 2 }     // positional, in declaration order
```

A literal is one or the other, never a mix, and it must set **every** field.

## Private fields are what make invariants enforceable

**A struct literal is only legal where every field is visible.** A type with
a private field can therefore only be constructed through its own associated
functions:

```gyro
export fn Stack::new(capacity: i32): Stack {
  return Stack{ capacity: capacity, top: 0, items: Array<i32>::new(heap()) }
}
```

Outside the module, `Stack{ ... }` does not compile, so `Stack::new` is the
only way in and whatever it establishes holds for every `Stack` that exists.
That is the whole mechanism — there are no constructors, no privacy
modifiers beyond `export`, and no way around it.

## Structs are values

Assignment copies. Passing one to a function copies it, unless the parameter
is a pointer or the receiver is `self` — which borrows and never copies,
however large the struct is.

Equality is compiler-derived, field by field, and so is `hash()`
([chapter 3.2](ch03-02-data-types.md)). Printing is derived too:
`println(p)` on a `Point` prints `Point{ x: 1, y: 2 }` unless the type
declares its own `to_string(): string`
([chapter 14.2](ch14-02-printing.md)).

## Generic structs

```gyro
struct Pair<T> {
  first: T
  second: T
}

fn Pair<T>::swap(*self) {
  var tmp = self.first
  self.first = self.second
  self.second = tmp
}

var p = Pair<i32>{ 1, 2 }
```

Type arguments infer from the fields where they can, so `Pair{ 1, 2 }` also
works ([chapter 10](ch10-00-generics.md)).

## A struct in a condition

Because a condition is followed by `{`, a bare struct literal cannot appear
at the top level of an `if`, `while` or `for..in` head. Parenthesize:

```gyro
if (Point{ x: 1 }).x > 0 { }
for x in (Counter{ 0, 5 }) { }
```

## Structs and borrows

A struct **carries the origins of its fields**. A struct built over a local
array holds frame storage, and a view of its field is a view of this frame:

```gyro
struct View { data: i32[] }

fn bad(): i32[] {
  var xs = [1, 2, 3]
  var v = View{ data: xs }
  return v.data          // error: xs ends with this function
}
```

A heap buffer does not. `Stack{ items: Array<i32>::new(heap()) }` holds storage
that outlives the frame, and lending a view of it is fine — provided the row
says whose it is ([chapter 12](ch12-00-effect-rows.md)).

## Unions

A `union` is **one storage with several names for it**. Every member begins
where the union does, so it is the size of its widest member, and writing
one member unwrites the others:

```gyro
union Value {
  i: i64
  f: f64
  bytes: u8[8]
}

var v = Value{ i: 42 }    // a literal sets exactly one member
println(v.i)              // 42
v.f = 3.5                 // and this is the same eight bytes
```

**Nothing records which member is live.** That is not an omission — it is
what a union is, it is what C's is, and it is why a union is eight bytes
where an enum with payloads would be sixteen. It also decides everything
else about one:

- **`==` does not work**, because there is no question to answer. Neither
  does hashing, so a union is not a `Map` key. A struct holding one
  inherits both refusals.
- **Printing one prints the word `union`.** There is no value here to
  render, and printing the first member would be a guess that reads as an
  answer.
- **It cannot go into an `any`**, which promises the concrete value comes
  back out.
- **A literal names exactly one member.** `Value{}` and
  `Value{ i: 1, f: 2.5 }` are both errors, and so is the positional form:
  one storage has room for one of them, and position would not say which.
- **A union is not generic.** Its size is its widest member's, and a
  parameter would make that depend on an argument.

### What may be a member

A member is **bytes**. Reading a member that was never written reinterprets
whatever is there, so a value the runtime tracks *beside* its bytes cannot
be one — a `string` is a pointer to Gyro's own header, a slice is a pointer
and a length the runtime trusts, an `Array<T>` is a runtime handle, an
enum's tag has to name a real variant, a nullable's flag has to match its
payload. Each is refused with the reason.

Scalars, fixed-size arrays, structs of those, other unions, `*T`, `anyptr`
and `code(..)` are members. A raw pointer is deliberately in that list:
`union { i: i64, p: *Node }` is what half of C's tagged unions are, and
nothing about a `*T` is kept anywhere but in the bytes.

### The shape it exists for

The reason to have unions at all is the tagged union every C message queue
is built on — `SDL_Event`, `XEvent`, every one anyone has written:

```gyro
struct KeyEvent { kind: i32  code: i32 }
struct MouseEvent { kind: i32  x: i32  y: i32 }

union Event {
  kind: i32
  key: KeyEvent
  mouse: MouseEvent
}

fn dispatch(e: *Event): i32 {
  if e.kind == 1 { return e.key.code }
  if e.kind == 2 { return e.mouse.x }
  return 0
}
```

`e.kind` reads across members, and it works because they **share an initial
sequence**. That is the one cross-member read C defines, and Gyro defines
it too.

### Reading across members that do not

Everything else is what C leaves undefined, and Gyro leaves it undefined
for the same reason: a union is one storage with several names for it, and
nothing records which of them is live. The compiled program has C's own
union under it, so it reinterprets the bytes, exactly as C does. Nothing
diagnoses it — the information needed to catch it was never stored
([appendix G](appendix-g-known-gaps.md)).

### When not to use one

Most of the time, **don't**. An `enum` with payloads is checked, prints,
compares, and cannot be read wrong ([chapter 6.1](ch06-01-enums.md)); the
cJSON port turned C's union into one and it read better than the original.

A `union` is for the two cases an enum cannot cover: **C's layout**, where
the bytes have to be the bytes a library reads, and **the tagged-union
idiom above**, where the discriminant is inside the storage rather than
beside it. A C `union` crossing into Gyro is spelled with this
([chapter 15](ch15-00-c-interop.md)).

## What a struct cannot do

There is no inheritance, no embedding, no `impl` block, no operator
overloading, and no interface a struct declares it satisfies. A struct is
data; anything a contract is needed for is duck-typed on the methods that
exist ([chapter 1.1](ch01-01-what-is-gyro.md)).

A fieldless struct marked `@extern` is a special case: an opaque handle to
something a C library owns ([chapter 15](ch15-00-c-interop.md)).
