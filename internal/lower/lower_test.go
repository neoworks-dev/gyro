package lower_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/interp"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/lower"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/meval"
	"github.com/gyro-lang/gyro/internal/sema"
	"github.com/gyro-lang/gyro/internal/stdlib"
)

// compile loads and checks a program, failing the test on any diagnostic.
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

// diffProgram runs a program through the interpreter and through
// lower+eval and demands identical stdout and stderr.
func diffProgram(t *testing.T, rootPath string) {
	t.Helper()
	program, info := compile(t, rootPath)

	var wantOut, wantErr bytes.Buffer
	if err := interp.Run(program, &wantOut, &wantErr); err != nil {
		t.Fatalf("interp: %v", err)
	}

	module := lowerVerified(t, program, info)
	var gotOut, gotErr bytes.Buffer
	if err := meval.Run(module, &gotOut, &gotErr); err != nil {
		t.Fatalf("meval: %v\n%s", err, metals.Print(module))
	}

	if gotOut.String() != wantOut.String() {
		t.Errorf("stdout differs\ninterp:\n%s\nmetals:\n%s", wantOut.String(), gotOut.String())
	}
	if gotErr.String() != wantErr.String() {
		t.Errorf("stderr differs\ninterp:\n%s\nmetals:\n%s", wantErr.String(), gotErr.String())
	}
}

// diffSource is diffProgram for an inline program.
func diffSource(t *testing.T, source string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.gyro")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	diffProgram(t, path)
}

// diffPanic demands that both sides fail at runtime.
func diffPanic(t *testing.T, source string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.gyro")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	program, info := compile(t, path)

	var sink bytes.Buffer
	interpErr := interp.Run(program, &sink, &sink)
	if interpErr == nil {
		t.Fatalf("the interpreter did not panic")
	}
	module := lowerVerified(t, program, info)
	if err := meval.Run(module, &sink, &sink); err == nil {
		t.Fatalf("the evaluator did not abort")
	}
}

// ---------------------------------------------------------------------------
// The examples, both ways
// ---------------------------------------------------------------------------

// TestExamplesMatch is the oracle: every example computes the same
// output lowered as interpreted. closures.gyro is excluded because it
// prints from an unsynchronized spawn, which races in both runtimes;
// TestClosuresSynchronized covers the same ground deterministically.
func TestExamplesMatch(t *testing.T) {
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
			diffProgram(t, path)
		})
	}
}

func TestClosuresSynchronized(t *testing.T) {
	diffSource(t, `
import { println, spawn, Array } from "stdlib"

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

func TestClosureOwnsItsCaptures(t *testing.T) {
	// FnMut semantics: one captured copy, mutations accumulate across
	// calls, the original stays untouched.
	diffSource(t, `
import { println } from "stdlib"

fn main() {
  var count: i32 = 0
  var bump = fn(): i32 {
    count += 1
    return count
  }
  println(bump())
  println(bump())
  println(count)
}
`)
}

func TestDefersRunAtBlockExitInReverse(t *testing.T) {
	diffSource(t, `
import { println } from "stdlib"

fn work(early: bool): i32 {
  defer println("f1")
  defer println("f2")
  if early {
    defer println("branch")
    return 1
  }
  {
    defer println("inner")
    println("in the block")
  }
  println("after the block")
  return 2
}

fn main() {
  println(work(true))
  println(work(false))
}
`)
}

func TestNarrowingLowersToUnwrap(t *testing.T) {
	diffSource(t, `
import { println } from "stdlib"

fn describe(x: i32?): i32 {
  if x == null {
    return -1
  }
  return x + 1
}

fn main() {
  var present: i32? = 41
  var absent: i32? = null
  println(describe(present))
  println(describe(absent))
  if present != null && present > 40 {
    println("narrowed in a condition")
  }
}
`)
}

func TestGenericsMonomorphize(t *testing.T) {
	diffSource(t, `
import { println } from "stdlib"

struct Pair<T> {
  first: T
  second: T
}

fn Pair<T>::sum(self): T {
  return self.first
}

fn pick<T>(a: T, b: T, takeFirst: bool): T {
  if takeFirst {
    return a
  }
  return b
}

fn main() {
  println(pick(1, 2, true))
  println(pick(1.5, 2.5, false))
  println(pick("yes", "no", false))

  var ints = Pair<i32>{ 3, 4 }
  var floats = Pair<f64>{ 1.25, 2.5 }
  println(ints.sum())
  println(floats.sum())
}
`)
}

func TestErrorsWrapAndDispatch(t *testing.T) {
	diffSource(t, `
import { println, eprintln } from "stdlib"

struct TooBig {
  limit: i32
}

fn TooBig::message(self): string {
  return "value above #{self.limit}"
}

fn check(v: i32): (i32, error?) {
  if v > 10 {
    return 0, TooBig{ limit: 10 }
  }
  return v, null
}

fn main() {
  var ok, err = check(7)
  println(ok)
  println(err == null)

  var _, bad = check(99)
  if bad != null {
    println(bad.message())
    match bad {
      TooBig(e): println(e.limit)
      _: eprintln("unexpected")
    }
  }
}
`)
}

func TestMatchValueAndStatement(t *testing.T) {
	diffSource(t, `
import { println } from "stdlib"

enum Shape {
  Circle(f64),
  Rect(f64, f64),
  Dot,
}

fn area(s: Shape): f64 {
  return match s {
    Shape::Circle(r): 3.0 * r * r
    Shape::Rect(w, h): w * h
    Shape::Dot: 0.0
  }
}

fn main() {
  println(area(Shape::Circle(2.0)))
  println(area(Shape::Rect(3.0, 4.0)))
  println(area(Shape::Dot))

  match 3 {
    0: println("zero")
    3: println("three")
    _: println("many")
  }

  var name = match "b" {
    "a": "first"
    "b": "second"
    _: "other"
  }
  println(name)
}
`)
}

func TestWrappingArithmeticAndCasts(t *testing.T) {
	diffSource(t, `
import { println } from "stdlib"

fn main() {
  var bytes: u8[] = [104, 105]
  var hash: u32 = 2166136261
  for byte in bytes {
    hash = hash *% 16777619 +% (byte as u32)
  }
  println(hash)

  var shifted: i32 = 1 << 10
  println(shifted)
  var mask: u32 = 96
  println(mask >> 5)
  println(mask & 32)
  println(mask | 1)
  println(mask ^ 2)

  var narrow = 300 as u16
  println(narrow)
  println(65535 as u16)

  var truncated = 3.9 as i32
  println(truncated)
  println(-3.9 as i32)
}
`)
}

func TestTuplesAndCompoundTargets(t *testing.T) {
	diffSource(t, `
import { println } from "stdlib"

fn pair(): (i32, string) {
  return 4, "four"
}

fn main() {
  var n, word = pair()
  println(n)
  println(word)

  var t = (1, 2)
  t.0 = 10
  println(t.0 + t.1)

  var xs = [1, 2, 3]
  xs[1] += 40
  println(xs[1])

  var i: i32 = 0
  while true {
    i += 1
    if i == 2 {
      continue
    }
    if i > 3 {
      break
    }
    println(i)
  }
}
`)
}

func TestSelectWithReadyChannel(t *testing.T) {
	diffSource(t, `
import { println } from "stdlib"

fn main() {
  var ready = chan<i32>(1)
  ready <- 7
  select {
    var v = <-ready: println(v)
    else: println("nothing")
  }
  select {
    var v = <-ready: println(v)
    else: println("nothing")
  }
}
`)
}

func TestPointersAndMemory(t *testing.T) {
	diffSource(t, `
import { println } from "stdlib"
import { alloc, free } from "mem"

struct Node {
  value: i32
  next: *Node?
}

fn main() {
  var head = alloc<Node>()
  head.value = 1
  head.next = null

  var second = alloc<Node>()
  second.value = 2
  second.next = null
  head.next = second

  var current: *Node? = head
  var total: i32 = 0
  while current != null {
    total += current.value
    var next = current.next
    free(current)
    current = next
  }
  println(total)

  var local: i32 = 5
  var address = &local
  println(*address)
}
`)
}

func TestUseAfterFreePanicsBothWays(t *testing.T) {
	diffPanic(t, `
import { alloc, free } from "mem"
import { println } from "stdlib"

struct Box {
  value: i32
}

fn main() {
  var box = alloc<Box>()
  box.value = 1
  free(box)
  println(box.value)
}
`)
}

func TestModuleConstLowersToGetter(t *testing.T) {
	diffSource(t, `
import { println } from "stdlib"

fn triple(n: i32): i32 {
  return n * 3
}

const BASE: i32 = triple(7)

fn main() {
  println(BASE)
  println("twice #{BASE * 2}")
}
`)
}

func TestInterpolationUsesToString(t *testing.T) {
	diffSource(t, `
import { println } from "stdlib"

struct Version {
  major: i32
  minor: i32
}

fn Version::to_string(self): string {
  return "v#{self.major}.#{self.minor}"
}

fn main() {
  var v = Version{ major: 1, minor: 9 }
  println(v)
  println("running #{v} now")
  println("flag: #{1 < 2}")
}
`)
}

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
	diffProgram(t, path)

	program, info := compile(t, path)
	module := lowerVerified(t, program, info)
	printed := metals.Print(module)
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
	if err := meval.Run(module, &out, &out); err != nil {
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
