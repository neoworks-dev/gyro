# 16.1. The `gyro` command

One binary, no subcommand plugins, no build files. A program is the file you
name plus whatever it imports.

```text
gyro ast <file.gyro>      parse a file and print its syntax tree
gyro tokens <file.gyro>   print the token stream, terminators included
gyro check <file.gyro>    load, parse and name-check a program, reporting diagnostics
gyro run <module>...      link the given modules, root last, and run main
                          (.gyro, .metals, .lisp, .c and .wasm, in dependency order)
gyro metals <file.gyro>   check a program and print its lowered METALS module
gyro lisp <file.lisp>     compile a Lisp module and print its METALS
gyro cc <file.c|.wasm>    compile C through clang's wasm32 target, or read a
                          wasm module, and print the METALS it becomes
gyro nasm <file.gyro>     lower a program and print its x86-64 assembly
gyro cgen <file.gyro>     lower a program and print its generated C
gyro build <file.gyro> [-o out] [--backend nasm|c]
                          compile a program to a native executable
gyro lsp                  run the language server over stdio
gyro explain [CODE]       explain a diagnostic code, or list them all
gyro version              print the compiler version
```

## The three you will use

```sh
gyro check app.gyro                 # does it compile?
gyro run app.gyro a b c             # run it on the evaluator
gyro build app.gyro -o app          # a native binary
```

`gyro run` passes everything after the module along to the program, so
`gyro run app.gyro a b` gives `main` the arguments `["app.gyro", "a", "b"]` —
matching what the built binary sees ([chapter 1.3](ch01-03-hello-world.md)).

`gyro build` defaults to the assembler backend (NASM plus a C compiler for
the link); `--backend c` emits one C file and hands it to `cc`. Both link the
same runtime and must produce the same behaviour
([appendix E](appendix-e-native-backends.md)).

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
gyro cgen app.gyro       # the generated C
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
gyro run program.lisp
gyro run program.c
```

Everything the compiler can read becomes METALS, and what runs is METALS
either way; only the producer in front differs
([chapter 17.2](ch17-02-the-pipeline.md)). `.lisp` is a statically typed
Scheme, `.c` goes through the wasm bridge as a whole program, and a `.c`
that a Gyro program *imports* takes a different route
([chapter 15](ch15-00-c-interop.md)).

## Make targets

```sh
make build     # -> bin/gyro
make test      # go test ./...
make check     # type-check every example
make vet
make bench     # both native backends over tests/bench
make grammar   # regenerate and test both tree-sitter grammars
make fmt
make install
```
