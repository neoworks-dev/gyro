# 1.2. Installation

The compiler is one Go binary with no dependencies. Building it needs Go
1.24 or newer; running a program on the evaluator needs nothing else.

```sh
git clone https://github.com/gyro-lang/gyro
cd gyro
make build          # -> bin/gyro
```

`make install` puts the same binary on your `GOPATH`. The examples below
assume `./bin/gyro`.

## What else you need, and when

| you want to | you also need |
| --- | --- |
| `gyro run`, `gyro check` | nothing |
| `gyro build` (default backend) | `nasm` and a C compiler, on Linux x86-64 |
| `gyro build --backend c` | a C compiler, on Linux x86-64 |
| a program that imports a `.c` file | a C compiler for the link step |
| `gyro cc` on a `.c` through the wasm bridge | `clang` with a wasm32 target |
| regenerating a tree-sitter grammar | `npm`, once per grammar directory |

Both native backends emit Linux x86-64 and link a small C runtime; that is
the only target today ([appendix E](appendix-e-native-backends.md)).

## Checking the install

```sh
./bin/gyro version
./bin/gyro check examples/structs.gyro    # type-check, report diagnostics
./bin/gyro run examples/hello.gyro        # check, lower, evaluate
./bin/gyro build examples/hello.gyro -o hello && ./hello
```

`examples/` holds ten reference programs that the test suite keeps working.
They are the shortest route to seeing real syntax:

| example | shows |
| --- | --- |
| `hello.gyro` | bindings, interpolation, an effect row |
| `fibonacci.gyro` | recursion, loops, `@comptime` |
| `structs.gyro` | structs, free methods, enums, `match`, borrows |
| `errors.gyro` | `(T, error?)` returns and explicit handling |
| `memory.gyro` | `alloc`/`free`, `defer`, nullable pointers, `move` |
| `closures.gyro` | anonymous functions, inference, capture lists |
| `methods.gyro` | methods and extension methods |
| `variadics.gyro` | `...T` packs, `...any`, `printf` |
| `concurrency.gyro` | threads, channels, `select`, `async`/`await` |
| `triangle.gyro` | a Gouraud-shaded triangle in an SDL window, through `@extern` |

## The other make targets

```sh
make test      # go test ./...
make check     # type-check every example
make vet
make bench     # both native backends over tests/bench
make grammar   # regenerate and test both tree-sitter grammars
```

`make check`, `go test ./...` and `make vet` are what a change has to pass;
[chapter 17.3](ch17-03-testing.md) says what each of them covers.

## Editors

`gyro lsp` is a language server, and the repository ships tree-sitter
grammars for Gyro and for METALS. [Chapter 16.2](ch16-02-editors.md) has the
wiring, including a working Neovim setup under `editor/nvim/`.
