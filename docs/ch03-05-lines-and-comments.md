# 3.5. Lines, comments and literals

Gyro has no statement-terminating semicolons in normal source. This chapter
is the exact rule, because it is the one piece of the syntax that can
surprise you at a line break rather than at a keyword.

## Where a statement ends

The lexer inserts a terminator at a newline when the preceding token **could
end a statement** — an identifier, a literal, `)`, `]`, `}`, `>`, `?`, or
one of `return`, `break`, `continue`, `self`, `true`, `false`, `null`, `_`.

`>` and `?` are in that set because a type can end a line in a
signature-only declaration:

```gyro
fn Array<T>::new(a: Allocator): Array<T>
fn find(): *Node?
```

The consequence is that **a trailing `>` cannot split an expression**. Break
before the operator instead, which is what the continuation rule below
already prefers.

## Where it does not

A newline does *not* terminate the statement when the next line begins with
`.`, `,`, `)`, `]`, or a binary operator that cannot begin a statement:
`&&`, `||`, `==`, `!=`, `<=`, `>=`, `<`, `>`, `+`, `/`, `%`, `|`, `^`, `<<`,
`>>`, `..`, `as`.

Both continuation styles therefore work:

```gyro
var total = values
  .filter(is_even)
  .sum()

var ok = user.is_active
  && user.age >= 18
```

`-`, `*`, `&` and `<-` are **not** continuation tokens, because a statement
can begin with each of them — `*p = 5` assigns through a pointer, `&x` takes
an address, `-x` negates, `<-ch` receives. Those operators must trail the
line they belong to:

```gyro
var total = a +
  b -            // fine: the operators trail
  c

var wrong = a
  - b            // two statements: `a`, then `-b`
```

A literal `;` is still accepted, which is what makes the C-style `for`
header work and lets several statements share a line.

## Comments

`// line` and `/* block */`. Block comments nest, so commenting out a region
that already contains a block comment does what you meant.

## Blocks and conditions

Braces are always required and conditions take no parentheses:

```gyro
if x < 0 {
} elif x == 0 {
} else {
}
```

Because a condition is followed by `{`, a bare struct literal cannot appear
at the top level of a condition — `if Point { }` reads `Point` as a value
and `{` as the body. Parenthesize to disambiguate:

```gyro
if (Point{ x: 1 }).x > 0 { }
```

The same rule applies to the head of a `for ... in` loop.

## Why it is this way

An automatic terminator that looked only at the *next* line would make an
expression's meaning depend on how it was wrapped; one that looked only at
the previous token would break method chains. Looking at both, with the
asymmetric treatment of the four tokens that can start a statement, is what
lets both styles above be unambiguous. The tree-sitter grammar replicates
this rule in its external scanner, because no context-free grammar can
express it ([chapter 16.2](ch16-02-editors.md)).
