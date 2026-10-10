# 10.2. Trait values

A bound makes a function generic: it is compiled for each type it is called
with. When the type is only known at run time — a tree of elements of
different kinds, a list of handlers — a **trait value** stands for it.
`dyn Trait` is a value of some type that implements `Trait`, known only by
its methods.

```gyro
var button = Button{ label: "ok" }
var label = Label{ text: "hello", size: 12 }

var elements = Array<dyn Element>::new(heap())
elements.push(&button as dyn Element)
elements.push(&label as dyn Element)

for i in 0..elements.len() {
    elements.get(i).paint()
}
```

## What it is

A trait value is **two words**: a pointer to the value, and a pointer to the
table of that type's methods. The table is built once for each pair of type
and trait in the program, lives as long as the program does, and a call through a trait value
is one indirect call.

There is no hidden allocation. A trait value does not own what it points
at, nothing is boxed to make one, and no buffer inside it holds a copy. That
makes it small and cheap to put in an array, and it makes the next rule the
one that matters.

## It borrows

**A trait value is a view of the value it was made from, as a slice is a view
of its array.** Making one takes a pointer, `&button as dyn Element`, and the
result is only as long-lived as what the pointer points at. The borrow
analysis follows it like a slice ([chapter 4.4](ch04-04-slices-and-views.md)):
a function returning one says where it came from, and a view of this
frame's storage cannot be returned.

```gyro
fn pass(p: *Button): dyn Element !{ &p } {
    return p as dyn Element
}

fn frame(): dyn Element {
    var b = Button{ label: "frame" }
    return &b as dyn Element // error: a view into storage that ends with this function
}
```

To keep elements past a frame, allocate them: `alloc<Button>(heap())` and
make the trait value from that pointer.

## Making one

The conversion is the cast `pointer as dyn Trait`, from a pointer to a type
that implements the trait. It is explicit, in the same way every conversion
in Gyro is. The pointer may be any `*T` — the address of a local, a
parameter, an allocation — and the cast changes nothing about what it
points at.

## Calling

A method of the trait is called on a trait value as on any value:
`element.paint()`. A `*self` method mutates the value behind the pointer.
A trait value of a trait that requires another carries the methods of both,
and a trait value satisfies its own trait as a bound, so it can go to a
generic function:

```gyro
fn announce<T: Element>(item: T) {
    println(item.name())
}

announce(elements.get(0))
```

A trait value has no fields and no operators. Only the methods of its trait
are reachable through it, and it is not `Eq`, `Print` or anything else the
trait does not require.

## Which traits can be trait values

A method table has one entry per method, and the caller does not know the
type behind the value. So a trait is usable as `dyn` when every method:

- has no type parameters of its own
- does not mention `Self`, other than as its receiver

and the trait itself takes no type parameters and declares no associated
types ([chapter 10.1](ch10-01-traits.md)), since the type behind the value
would have to say what they are. A trait that does not qualify
is an error where `dyn Trait` is written, naming the method. The capability
traits — `Eq`, `Ord`, the operators — are not trait values either: they
are answered by the type at compile time.

## Default methods

A default method is in the table like any other, compiled for the type that
takes it, so a trait value of `Button` calls `Button`'s own `describe` if it
wrote one and the trait's if it did not.

## `dyn` is contextual

`dyn` is not a keyword. It starts a trait value type only when a name follows
it, so `dyn` remains an ordinary identifier everywhere else, as `move` and
`from` do.
