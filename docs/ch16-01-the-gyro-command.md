# 16.1. The `gyro` command

One binary, no subcommand plugins, no build files. A program is the file you
name plus whatever it imports.

```text
gyro ast <file.gyro>      parse a file and print its syntax tree
gyro tokens <file.gyro>   print the token stream, terminators included
gyro check <file.gyro>    load, parse and name-check a program, reporting diagnostics
gyro run <module>...      link the given modules, root last, build them and
                          run the result (.gyro and .metals, in dependency order)
gyro metals <file.gyro>   check a program and print its lowered METALS module
gyro cc <file.c>          compile a C file and print the METALS it becomes
gyro nasm <file.gyro>     lower a program and print its x86-64 assembly
gyro build <file.gyro> [-o out]
                          compile a program to a native executable
gyro lsp                  run the language server over stdio
gyro explain [CODE]       explain a diagnostic code, or list them all
gyro version              print the compiler version
```

## The three you will use

```sh
gyro check app.gyro                 # does it compile?
gyro run app.gyro a b c             # build it somewhere temporary and run it
gyro build app.gyro -o app          # a native binary
```

`gyro run` is `gyro build` into a temporary directory followed by the
binary, and the directory goes away afterwards. It is a convenience, not a
second way of executing a program: there is one thing that decides what a
program does ([chapter 17.1](ch17-01-the-backend.md)).

It passes everything after the module along to the program, so
`gyro run app.gyro a b` gives `main` the arguments `["app.gyro", "a", "b"]` —
matching what the built binary sees ([chapter 1.3](ch01-03-hello-world.md)).

`gyro build` emits x86-64 assembly, assembles it with NASM and links it
against the C runtime with a C compiler
([appendix E](appendix-e-native-backend.md)).

## Diagnostics

Errors print as a code frame with the span underlined and the fix suggested
under it:

```text
error[E0401]: undeclared name println
 --> hello.gyro:2:3
  |
2 |   println("hi")
  |   ^^^^^^^ not found in this scope
  |
  = help: "stdlib" exports println; add: import { println } from "stdlib"
```

Every diagnostic carries a code, and `gyro explain E0401` says more about
it. [Appendix F](appendix-f-diagnostics.md) covers the code ranges and how
notes are used.

## Looking inside the compiler

These are for understanding what your program became, and for debugging the
compiler itself:

```sh
gyro tokens app.gyro     # the token stream, inserted terminators included
gyro ast app.gyro        # the syntax tree
gyro metals app.gyro     # the whole program as one METALS module
gyro nasm app.gyro       # x86-64 assembly
```

`gyro tokens` is the fastest way to settle an argument about where a
statement ended ([chapter 3.5](ch03-05-lines-and-comments.md)). `gyro metals`
shows monomorphized generics, lifted closures, inlined defers and match
lowered to tag tests ([appendix D](appendix-d-metals.md)).

## Running things that are not Gyro

`gyro run` takes several modules, root last, and they do not all have to be
Gyro:

```sh
gyro run util.metals main.gyro
```

Everything the compiler can read becomes METALS, and what runs is METALS
either way; only the producer in front differs
([chapter 17.2](ch17-02-the-pipeline.md)). A `.metals` is the format read
back as itself, which is how a test reaches an instruction no lowering
happens to emit.

A `.c` is not a root. C reaches Gyro by being *imported*
([chapter 15](ch15-00-c-interop.md)), and what the front end makes of one
is what `gyro cc file.c` prints — a library, not a program: C's entry is an
`int main` whose result is the process exit code, and a METALS entry has
nowhere to put that.

## Make targets

```sh
make build     # -> bin/gyro
make test      # go test ./...
make check     # type-check every example
make vet
make bench     # the backend against the C, over tests/bench
make grammar   # regenerate and test both tree-sitter grammars
make fmt
make install
```
