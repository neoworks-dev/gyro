package nasm_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/interp"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/lower"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/nasm"
	"github.com/gyro-lang/gyro/internal/sema"
)

// The native oracle: a program built to machine code must produce the
// same stdout, stderr and failure state as `gyro run`.

func requireToolchain(t *testing.T) {
	t.Helper()
	if err := nasm.Toolchain(); err != nil {
		t.Skipf("skipping: %v", err)
	}
}

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

// buildBinary lowers, verifies and compiles a program to an executable.
func buildBinary(t *testing.T, rootPath string) string {
	t.Helper()
	program, info := compile(t, rootPath)
	module, err := lower.Lower(program, info)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatalf("verify: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "program")
	if err := nasm.Build(module, binary); err != nil {
		t.Fatalf("build: %v", err)
	}
	return binary
}

func runNative(t *testing.T, binary string) (stdout, stderr string, failed bool) {
	t.Helper()
	command := exec.Command(binary)
	var outBuf, errBuf bytes.Buffer
	command.Stdout = &outBuf
	command.Stderr = &errBuf
	err := command.Run()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			t.Fatalf("run: %v", err)
		}
	}
	return outBuf.String(), errBuf.String(), err != nil
}

func runInterp(t *testing.T, rootPath string) (stdout, stderr string, failed bool) {
	t.Helper()
	program, _ := compile(t, rootPath)
	var outBuf, errBuf bytes.Buffer
	err := interp.Run(program, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), err != nil
}

func diffNative(t *testing.T, rootPath string) {
	t.Helper()
	requireToolchain(t)
	wantOut, wantErr, wantFailed := runInterp(t, rootPath)
	binary := buildBinary(t, rootPath)
	gotOut, gotErr, gotFailed := runNative(t, binary)

	if gotOut != wantOut {
		t.Errorf("stdout differs\ninterp:\n%s\nnative:\n%s", wantOut, gotOut)
	}
	if gotErr != wantErr {
		t.Errorf("stderr differs\ninterp:\n%s\nnative:\n%s", wantErr, gotErr)
	}
	if gotFailed != wantFailed {
		t.Errorf("failure state differs: interp %v, native %v", wantFailed, gotFailed)
	}
}

func sourceFile(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.gyro")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func diffNativeSource(t *testing.T, source string) {
	t.Helper()
	diffNative(t, sourceFile(t, source))
}

// diffNativePanic demands both sides fail with identical stdout; the
// panic texts themselves are not compared, since the interpreter and
// the runtime word a few of them differently.
func diffNativePanic(t *testing.T, source string) {
	t.Helper()
	requireToolchain(t)
	rootPath := sourceFile(t, source)
	wantOut, _, wantFailed := runInterp(t, rootPath)
	if !wantFailed {
		t.Fatalf("the interpreter did not panic")
	}
	binary := buildBinary(t, rootPath)
	gotOut, _, gotFailed := runNative(t, binary)
	if !gotFailed {
		t.Fatalf("the native binary did not panic")
	}
	if gotOut != wantOut {
		t.Errorf("stdout before the panic differs\ninterp:\n%s\nnative:\n%s", wantOut, gotOut)
	}
}

// ---------------------------------------------------------------------------
// The examples
// ---------------------------------------------------------------------------

// TestExamplesRunNative diffs every example against the interpreter.
// closures.gyro races by design (an unsynchronized spawn println);
// TestClosuresSynchronizedNative covers the same ground.
func TestExamplesRunNative(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.gyro"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no examples found: %v", err)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		if name == "closures.gyro" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			diffNative(t, path)
		})
	}
}

func TestClosuresSynchronizedNative(t *testing.T) {
	diffNativeSource(t, `
import { println, spawn, Array } from "@gyro/stdlib"

fn apply(values: i32[], f: fn(i32): i32): Array<i32> {
  var out = Array<i32>::new()
  for value in values {
    out.push(f(value))
  }
  return out
}

fn main() {
  var double = fn(a: i32): i32 { return a * 2 }
  println(double(21))

  var doubled = apply([1, 2, 3], fn(x) x * 2)
  defer doubled.free()
  println(doubled.len())
  println(doubled.get(2))

  var factor: i32 = 3
  var scale = fn(x: i32): i32 { return x * factor }
  println(scale(5))

  var done = chan<bool>()
  spawn(fn() {
    println("on another thread")
    done <- true
  })
  var _ = <-done

  var greet = fn() { println("hello") }
  greet()
}
`)
}

// ---------------------------------------------------------------------------
// Focused semantics
// ---------------------------------------------------------------------------

// TestFloatFormattingNative pins the runtime's Go-style shortest
// round-trip float printing.
func TestFloatFormattingNative(t *testing.T) {
	diffNativeSource(t, `
import { println } from "@gyro/stdlib"

fn main() {
  println(12.0)
  println(3.14159 * 2.0 * 2.0)
  println(0.1 + 0.2)
  println(1.0 / 3.0)
  println(10000000000000000000000.0)
  println(0.00001)
  println(1.5e300 * 1.5e300)
  println(0.0 - 12.5)
  println(100000.0)
}
`)
}

func TestShiftEdgesNative(t *testing.T) {
	diffNativeSource(t, `
import { println } from "@gyro/stdlib"

fn main() {
  var one: u64 = 1
  var wide: u64 = 70
  println(one << wide)
  var word: u64 = 12345
  println(word >> wide)
  var negative: i64 = -8
  var two: i64 = 2
  println(negative >> two)
  var byte: u8 = 10
  var shift: u8 = 3
  println(byte << shift)
}
`)
}

func TestSelectReadyChannelNative(t *testing.T) {
	diffNativeSource(t, `
import { println } from "@gyro/stdlib"

fn main() {
  var values = chan<i32>(4)
  values <- 7
  select {
    var value = <-values: println("got #{value}")
    else: println("nothing ready")
  }
  select {
    var value = <-values: println("got #{value}")
    else: println("nothing ready")
  }
}
`)
}

func TestMutexNative(t *testing.T) {
	diffNativeSource(t, `
import { println, Mutex } from "@gyro/stdlib"

fn main() {
  var total = Mutex::new(10)
  var guard = total.lock()
  guard.set(guard.get() + 32)
  var value = guard.get()
  guard.unlock()
  println(value)
}
`)
}

func TestArrayOperationsNative(t *testing.T) {
	diffNativeSource(t, `
import { println, Array } from "@gyro/stdlib"

fn main() {
  var values = Array<i32>::new()
  defer values.free()
  values.push(1)
  values.push(2)
  values.push(3)
  values.set(0, 9)
  println(values.get(0))
  var last, ok = values.pop()
  println("#{last} #{ok}")
  println(values.len())
  var total = 0
  for value in values.slice() {
    total += value
  }
  println(total)
}
`)
}

func TestRunesNative(t *testing.T) {
	diffNativeSource(t, `
import { println } from "@gyro/stdlib"

fn main() {
  var text = "héllo"
  println(text.len())
  for r in text.runes() {
    println(r)
  }
}
`)
}

// ---------------------------------------------------------------------------
// Panics
// ---------------------------------------------------------------------------

func TestOverflowPanicsNative(t *testing.T) {
	diffNativePanic(t, `
import { println } from "@gyro/stdlib"

fn main() {
  println("before")
  var big: i32 = 2000000000
  var sum = big + big
  println(sum)
}
`)
}

func TestIndexOutOfRangePanicsNative(t *testing.T) {
	diffNativePanic(t, `
import { println } from "@gyro/stdlib"

fn main() {
  println("before")
  var values: i32[] = [1, 2, 3]
  var at: usize = 5
  println(values[at])
}
`)
}

func TestDivisionByZeroPanicsNative(t *testing.T) {
	diffNativePanic(t, `
import { println } from "@gyro/stdlib"

fn main() {
  println("before")
  var zero: i32 = 0
  println(10 / zero)
}
`)
}
