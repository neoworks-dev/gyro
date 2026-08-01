// Command gyro is the Gyro compiler driver.
package main

import (
	"fmt"
	"os"

	"github.com/gyro-lang/gyro/internal/ast"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/lexer"
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
  gyro check <file.gyro>    parse and name-check a file, reporting diagnostics
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
	file, ok := load(args)
	if !ok {
		return 2
	}

	var bag diag.Bag
	tree := parser.ParseFile(file, &bag)

	// Name resolution on a broken parse would double-report; the syntax
	// errors are the ones worth fixing first.
	if !bag.HasErrors() {
		sema.Check(tree, file, &bag)
	}

	code := report(&bag)
	if code == 0 && bag.Len() == 0 {
		fmt.Printf("%s: ok\n", file.Name)
	}
	return code
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
