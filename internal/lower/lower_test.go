package lower_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/lower"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/meval"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/stdlib"
)

// What a lowered program computes is checked by the corpus in tests/,
// which runs every case through the interpreter, the evaluator and a
// native binary and demands they agree. What is left here is the shape of
// the module itself: the naming and the record layout a program's output
// would not reveal.

func compile(t *testing.T, rootPath string) (*loader.Program, *sema.Info) {
	t.Helper()
	var bag diag.Bag
	program, err := loader.Load(rootPath, &bag)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if bag.HasErrors() {
		t.Fatalf("the program does not parse cleanly: %v", bag.Items())
	}
	info := sema.CheckProgram(program, &bag)
	if bag.HasErrors() {
		t.Fatalf("the program does not check cleanly: %v", bag.Items())
	}
	return program, info
}

// lowerVerified lowers a checked program, verifies the module and pins
// the text round trip.
func lowerVerified(t *testing.T, program *loader.Program, info *sema.Info) *metals.Module {
	t.Helper()
	module, err := lower.Lower(program, info)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatalf("the lowered module fails verification: %v\n%s", err, metals.Print(module))
	}
	printed := metals.Print(module)
	again, err := metals.Parse(printed)
	if err != nil {
		t.Fatalf("the lowered module does not reparse: %v\n%s", err, printed)
	}
	if metals.Print(again) != printed {
		t.Fatalf("the lowered module does not round-trip")
	}
	return module
}

// A METALS module is flat, so every name from every source module has to
// survive in it unambiguously.
func TestCrossModuleNamesAreQualified(t *testing.T) {
	dir := t.TempDir()
	util := `
import { println } from "stdlib"

export fn shout(text: string) {
  println("#{text}!")
}

export struct Counter {
  export n: i32
}

export fn Counter::bump(*self): i32 {
  self.n += 1
  return self.n
}
`
	main := `
import { shout, Counter } from "./util"
import { println } from "stdlib"

fn main() {
  shout("hey")
  var c = Counter{ n: 0 }
  println(c.bump())
}
`
	if err := os.WriteFile(filepath.Join(dir, "util.gyro"), []byte(util), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "main.gyro")
	if err := os.WriteFile(path, []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}

	program, info := compile(t, path)
	printed := metals.Print(lowerVerified(t, program, info))
	for _, want := range []string{`@"util::shout"`, `@"util::Counter::bump"`, `%"util::Counter"`} {
		if !bytes.Contains([]byte(printed), []byte(want)) {
			t.Errorf("the module does not mention %s:\n%s", want, printed)
		}
	}
}

// A bundled module may hold ordinary Gyro. What makes a declaration a
// runtime intrinsic is the missing body, not the module it lives in, and
// what makes a struct an opaque handle is having no fields. Both are
// exercised here through a module served under a bundled path.
func TestBundledModulesMayCarryGyroCode(t *testing.T) {
	const bundled = `
export struct Counter {
  export n: i32
}

export fn Counter::new(): Counter {
  return Counter{ n: 0 }
}

export fn Counter::bump(*self): i32 {
  self.n += 1
  return self.n
}

export fn double(value: i32): i32 {
  return value * 2
}
`
	const main = `import { println } from "stdlib"
import { Counter, double } from "counters"

fn main() {
  var c = Counter::new()
  println(c.bump())
  println(double(21))
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "main.gyro")
	if err := os.WriteFile(path, []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}

	var bag diag.Bag
	resolver := bundledResolver{name: "counters", source: bundled}
	program, err := loader.LoadWith(resolver, path, &bag)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	info := sema.CheckProgram(program, &bag)
	if bag.HasErrors() {
		t.Fatalf("the program does not check cleanly: %v", bag.Items())
	}

	module := lowerVerified(t, program, info)
	printed := metals.Print(module)
	// The record has to exist: an opaque handle cannot be constructed.
	if !bytes.Contains([]byte(printed), []byte(`%"counters::Counter"`)) {
		t.Errorf("the bundled struct did not lower to a record:\n%s", printed)
	}
	// A bodied bundled function keeps its module tag; only bodyless ones
	// become bare runtime symbols.
	if !bytes.Contains([]byte(printed), []byte(`@"counters::double"`)) {
		t.Errorf("the bundled function lost its module tag:\n%s", printed)
	}

	var out bytes.Buffer
	if err := meval.Run(module, []string{"program"}, &out, &out); err != nil {
		t.Fatalf("meval: %v\n%s", err, printed)
	}
	if out.String() != "1\n42\n" {
		t.Errorf("unexpected output %q", out.String())
	}
}

// bundledResolver serves one module under a bundled path, so a test can
// put Gyro code where the standard library lives. Everything else falls
// through to the ordinary rules.
type bundledResolver struct {
	name   string
	source string
}

func (r bundledResolver) Resolve(fromDir, specifier string) (string, string, error) {
	if specifier == r.name {
		return stdlib.Path(r.name), r.source, nil
	}
	return loader.Overlay(nil).Resolve(fromDir, specifier)
}
