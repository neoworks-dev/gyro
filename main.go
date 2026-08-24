// Command gyro is the Gyro compiler driver.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/cgen"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/interp"
	"github.com/gyro-lang/gyro/internal/lexer"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/lower"
	"github.com/gyro-lang/gyro/internal/lsp"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/nasm"
	"github.com/gyro-lang/gyro/internal/parser"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/token"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const usage = `gyro - the Gyro compiler

usage:
  gyro ast <file.gyro>      parse a file and print its syntax tree
  gyro tokens <file.gyro>   print the token stream, terminators included
  gyro check <file.gyro>    load, parse and name-check a program, reporting diagnostics
  gyro run <file.gyro>      check a program and run its fn main in the interpreter
  gyro metals <file.gyro>   check a program and print its lowered METALS module
  gyro nasm <file.gyro>     lower a program and print its x86-64 assembly
  gyro cgen <file.gyro>     lower a program and print its generated C
  gyro build <file.gyro> [-o out] [--backend nasm|c]
                            compile a program to a native executable
  gyro lsp                  run the language server over stdio
  gyro explain [CODE]       explain a diagnostic code, or list them all
  gyro version              print the compiler version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	command := os.Args[1]
	args := os.Args[2:]

	switch command {
	case "ast":
		os.Exit(runAST(args))
	case "tokens":
		os.Exit(runTokens(args))
	case "check":
		os.Exit(runCheck(args))
	case "run":
		os.Exit(runProgram(args))
	case "metals":
		os.Exit(runMetals(args))
	case "nasm":
		os.Exit(runNasm(args))
	case "cgen":
		os.Exit(runCgen(args))
	case "build":
		os.Exit(runBuild(args))
	case "explain":
		os.Exit(runExplain(args))
	case "lsp":
		if err := lsp.Serve(os.Stdin, os.Stdout, version); err != nil {
			fmt.Fprintf(os.Stderr, "gyro lsp: %v\n", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Println("gyro", version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "gyro: unknown command %q\n\n%s", command, usage)
		os.Exit(2)
	}
}

// load reads a source file and turns it into a diag.File.
func load(args []string) (*diag.File, bool) {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, usage)
		return nil, false
	}

	source, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
		return nil, false
	}
	return diag.NewFile(args[0], string(source)), true
}

func runAST(args []string) int {
	file, ok := load(args)
	if !ok {
		return 2
	}

	var bag diag.Bag
	tree := parser.ParseFile(file, &bag)
	fmt.Print(ast.Dump(tree))
	return report(&bag)
}

func runCheck(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	var bag diag.Bag
	program, err := loader.Load(args[0], &bag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
		return 2
	}

	// Name resolution on a broken parse would double-report; the syntax
	// errors are the ones worth fixing first.
	if !bag.HasErrors() {
		sema.CheckProgram(program, &bag)
	}

	code := report(&bag)
	if code == 0 && bag.Len() == 0 {
		fmt.Printf("%s: ok\n", program.Root.File.Name)
	}
	return code
}

// runProgram checks a program and executes it in the interpreter.
// Anything after the file is the program's own command line, with the
// file standing in for argv[0] the way the binary's path would.
func runProgram(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	var bag diag.Bag
	program, err := loader.Load(args[0], &bag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
		return 2
	}
	if !bag.HasErrors() {
		sema.CheckProgram(program, &bag)
	}
	if code := report(&bag); code != 0 {
		return code
	}

	if err := interp.Run(program, args, os.Stdout, os.Stderr); err != nil {
		// A panic already printed itself; anything else needs a voice.
		if _, isPanic := err.(*interp.Panic); !isPanic {
			fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
		}
		return 1
	}
	return 0
}

// runMetals checks a program, lowers it to METALS and prints the module.
func runMetals(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	module, code := lowerFile(args[0])
	if module == nil {
		return code
	}
	fmt.Print(metals.Print(module))
	return 0
}

// lowerFile loads, checks, lowers and verifies a program, the shared
// front half of the metals, nasm and build commands.
func lowerFile(path string) (*metals.Module, int) {
	var bag diag.Bag
	program, err := loader.Load(path, &bag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
		return nil, 2
	}
	var info *sema.Info
	if !bag.HasErrors() {
		info = sema.CheckProgram(program, &bag)
	}
	if code := report(&bag); code != 0 {
		return nil, code
	}

	module, err := lower.Lower(program, info)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
		return nil, 1
	}
	if err := metals.Verify(module); err != nil {
		fmt.Fprintf(os.Stderr, "gyro: internal: the lowered module fails verification: %v\n", err)
		return nil, 1
	}
	return module, 0
}

// runNasm lowers a program and prints its generated assembly.
func runNasm(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	module, code := lowerFile(args[0])
	if module == nil {
		return code
	}
	asm, err := nasm.Generate(module)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
		return 1
	}
	fmt.Print(asm)
	return 0
}

// runCgen lowers a program and prints its generated C.
func runCgen(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	module, code := lowerFile(args[0])
	if module == nil {
		return code
	}
	source, err := cgen.Generate(module)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
		return 1
	}
	fmt.Print(source)
	return 0
}

// runBuild compiles a program to a native executable. The backend is the
// x86-64 assembler by default; `--backend c` goes through the C emitter
// instead, which is what the two are benchmarked against each other with.
func runBuild(args []string) int {
	var inputs []string
	output := ""
	backend := "nasm"
	for i := 0; i < len(args); i++ {
		if args[i] == "-o" && i+1 < len(args) {
			output = args[i+1]
			i++
			continue
		}
		if args[i] == "--backend" && i+1 < len(args) {
			backend = args[i+1]
			i++
			continue
		}
		inputs = append(inputs, args[i])
	}
	if len(inputs) != 1 || (backend != "nasm" && backend != "c") {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	if output == "" {
		base := filepath.Base(inputs[0])
		output = strings.TrimSuffix(base, ".gyro")
	}

	toolchain, build := nasm.Toolchain, nasm.Build
	if backend == "c" {
		toolchain, build = cgen.Toolchain, cgen.Build
	}
	if err := toolchain(); err != nil {
		fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
		return 1
	}
	module, code := lowerFile(inputs[0])
	if module == nil {
		return code
	}
	if err := build(module, output); err != nil {
		fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
		return 1
	}
	return 0
}

func runTokens(args []string) int {
	file, ok := load(args)
	if !ok {
		return 2
	}

	var bag diag.Bag
	for _, tok := range lexer.Tokens(file, &bag) {
		fmt.Printf("%-14s %s\n", file.Position(tok.Offset), tok)
		if tok.Kind == token.EOF {
			break
		}
	}
	return report(&bag)
}

// report prints every diagnostic as a code frame and returns the process
// exit code.
func report(bag *diag.Bag) int {
	fmt.Fprint(os.Stderr, bag.Render(diag.AutoStyle(os.Stderr)))
	if bag.HasErrors() {
		return 1
	}
	return 0
}

// runExplain prints what a diagnostic code means, `gyro explain E0401`.
func runExplain(args []string) int {
	if len(args) != 1 {
		fmt.Println("the codes with an explanation:")
		for _, code := range diag.Codes() {
			title, _, _ := diag.Explain(code)
			fmt.Printf("  %s  %s\n", code, title)
		}
		return 0
	}

	code := diag.Code(strings.ToUpper(args[0]))
	title, body, found := diag.Explain(code)
	if !found {
		fmt.Fprintf(os.Stderr, "gyro: no such diagnostic code %q\n", args[0])
		return 2
	}
	fmt.Printf("%s: %s\n\n%s\n", code, title, body)
	return 0
}
