// Command gyro is the Gyro compiler driver.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/interp"
	"github.com/gyro-lang/gyro/internal/lexer"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/lower"
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
  gyro build <file.gyro> [-o out]
                            compile a program to a native executable
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
	case "build":
		os.Exit(runBuild(args))
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
func runProgram(args []string) int {
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
	if !bag.HasErrors() {
		sema.CheckProgram(program, &bag)
	}
	if code := report(&bag); code != 0 {
		return code
	}

	if err := interp.Run(program, os.Stdout, os.Stderr); err != nil {
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

// runBuild compiles a program to a native executable.
func runBuild(args []string) int {
	var inputs []string
	output := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "-o" && i+1 < len(args) {
			output = args[i+1]
			i++
			continue
		}
		inputs = append(inputs, args[i])
	}
	if len(inputs) != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	if output == "" {
		base := filepath.Base(inputs[0])
		output = strings.TrimSuffix(base, ".gyro")
	}

	if err := nasm.Toolchain(); err != nil {
		fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
		return 1
	}
	module, code := lowerFile(inputs[0])
	if module == nil {
		return code
	}
	if err := nasm.Build(module, output); err != nil {
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

// report prints every diagnostic and returns the process exit code.
func report(bag *diag.Bag) int {
	for _, item := range bag.Items() {
		fmt.Fprintln(os.Stderr, item)
	}
	if bag.HasErrors() {
		return 1
	}
	return 0
}
