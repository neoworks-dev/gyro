package interp_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/interp"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/sema"
)

// run type-checks and executes a program, returning what it printed and
// the panic that aborted it, if one did.
func run(t *testing.T, files map[string]string, root string) (string, *interp.Panic) {
	t.Helper()

	dir := t.TempDir()
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var bag diag.Bag
	program, err := loader.Load(filepath.Join(dir, root), &bag)
	if err != nil {
		t.Fatal(err)
	}
	if !bag.HasErrors() {
		sema.CheckProgram(program, &bag)
	}
	if bag.HasErrors() {
		for _, item := range bag.Items() {
			t.Errorf("%s", item)
		}
		t.Fatal("the program does not check; the interpreter only runs checked programs")
	}

	var stdout, stderr strings.Builder
	runErr := interp.Run(program, &stdout, &stderr)
	if runErr == nil {
		return stdout.String(), nil
	}

	var p *interp.Panic
	if !errors.As(runErr, &p) {
		t.Fatalf("run failed without a panic: %v", runErr)
	}
	return stdout.String(), p
}

// runMain wraps a body in fn main with the stdlib imported.
func runMain(t *testing.T, body string) (string, *interp.Panic) {
	t.Helper()
	source := "import { println, eprintln, print, spawn, Array, Mutex } from \"@gyro/stdlib\"\n\n" +
		"fn main() {\n" + body + "}\n"
	return run(t, map[string]string{"main.gyro": source}, "main.gyro")
}

// runSource runs a full file (for programs that need their own decls).
func runSource(t *testing.T, source string) (string, *interp.Panic) {
	t.Helper()
	return run(t, map[string]string{"main.gyro": source}, "main.gyro")
}

func wantOutput(t *testing.T, got string, p *interp.Panic, want string) {
	t.Helper()
	if p != nil {
		t.Fatalf("unexpected panic: %v", p)
	}
	if got != want {
		t.Errorf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

func wantPanic(t *testing.T, p *interp.Panic, fragment string) {
	t.Helper()
	if p == nil {
		t.Fatalf("expected a panic containing %q, the program finished", fragment)
	}
	if !strings.Contains(p.Message, fragment) {
		t.Errorf("panic %q does not contain %q", p.Message, fragment)
	}
}

// ---------------------------------------------------------------------------
// Printing
// ---------------------------------------------------------------------------

func TestPrintingPrimitives(t *testing.T) {
	out, p := runMain(t, `
  println(42)
  println(3.5)
  println(true)
  println("raw text")
  println(null)
  println((1, "a"))
`)
	wantOutput(t, out, p, "42\n3.5\ntrue\nraw text\nnull\n(1, \"a\")\n")
}

func TestDerivedStructRepr(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

struct Point {
  x: i32
  y: i32
}

fn main() {
  println(Point{ x: 1, y: 2 })
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "Point{ x: 1, y: 2 }\n")
}

func TestToStringOverridesRepr(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

struct Version {
  major: i32
  minor: i32
}

fn Version::to_string(self): string {
  return "v#{self.major}.#{self.minor}"
}

fn main() {
  println(Version{ major: 1, minor: 4 })
  println("inline: #{Version{ major: 2, minor: 0 }}")
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "v1.4\ninline: v2.0\n")
}

func TestEnumRepr(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

enum Shape {
  Circle(f64),
  Point,
}

fn main() {
  println(Shape::Circle(2.5))
  println(Shape::Point)
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "Shape::Circle(2.5)\nShape::Point\n")
}

// ---------------------------------------------------------------------------
// Arithmetic semantics
// ---------------------------------------------------------------------------

func TestOverflowPanics(t *testing.T) {
	_, p := runMain(t, `
  var x: i32 = 2147483647
  var y = x + 1
`)
	wantPanic(t, p, "integer overflow")
}

func TestUnsignedUnderflowPanics(t *testing.T) {
	_, p := runMain(t, `
  var x: u8 = 0
  var y: u8 = 1
  var z = x - y
`)
	wantPanic(t, p, "integer overflow")
}

func TestWrappingOperators(t *testing.T) {
	out, p := runMain(t, `
  var h: u32 = 4294967295
  println(h +% 1)
  var b: u8 = 200
  println(b *% 2)
  var s: i8 = -128
  println(s -% 1)
`)
	wantOutput(t, out, p, "0\n144\n127\n")
}

func TestDivisionSemantics(t *testing.T) {
	out, p := runMain(t, `
  var a: i32 = -7
  var b: i32 = 2
  println(a / b)
  println(a % b)
`)
	wantOutput(t, out, p, "-3\n-1\n")
}

func TestDivisionByZeroPanics(t *testing.T) {
	_, p := runMain(t, `
  var a: i32 = 1
  var b: i32 = 0
  var c = a / b
`)
	wantPanic(t, p, "division by zero")
}

func TestMinDividedByMinusOnePanics(t *testing.T) {
	_, p := runMain(t, `
  var a: i32 = -2147483648
  var b: i32 = -1
  var c = a / b
`)
	wantPanic(t, p, "integer overflow")
}

// ---------------------------------------------------------------------------
// Casts
// ---------------------------------------------------------------------------

func TestCasts(t *testing.T) {
	out, p := runMain(t, `
  var x: i32 = 300
  println(x as i64)
  println((x & 0xff) as u8)
  var f: f64 = 2.9
  println(f as i32)
  var r: rune = 'A'
  println(r as u32)
`)
	wantOutput(t, out, p, "300\n44\n2\n65\n")
}

func TestNarrowingCastPanics(t *testing.T) {
	_, p := runMain(t, `
  var x: i32 = 300
  var b = x as u8
`)
	wantPanic(t, p, "does not fit u8")
}

func TestNegativeToUnsignedPanics(t *testing.T) {
	_, p := runMain(t, `
  var x: i32 = -1
  var u = x as u32
`)
	wantPanic(t, p, "does not fit u32")
}

// ---------------------------------------------------------------------------
// Values and assignment
// ---------------------------------------------------------------------------

func TestStructsCopyOnAssignment(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

struct Point {
  x: i32
  y: i32
}

fn main() {
  var a = Point{ x: 1, y: 2 }
  var b = a
  b.x = 99
  println(a.x)
  println(b.x)
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "1\n99\n")
}

func TestTupleDestructuring(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

fn pair(): (i32, string) {
  return 7, "seven"
}

fn main() {
  var n, s = pair()
  println(n)
  println(s)
  var t = pair()
  println(t.1)
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "7\nseven\nseven\n")
}

func TestUninitializedReadPanics(t *testing.T) {
	_, p := runMain(t, `
  var x: i32
  println(x)
`)
	wantPanic(t, p, "uninitialized")
}

// ---------------------------------------------------------------------------
// Pointers and memory
// ---------------------------------------------------------------------------

const memoryPrelude = `import { println } from "@gyro/stdlib"
import { alloc, free } from "@gyro/mem"

struct Node {
  value: i32
  next: *Node?
}

`

func TestAllocFreeAndPointers(t *testing.T) {
	src := memoryPrelude + `fn main() {
  var node = alloc<Node>()
  node.value = 41
  node.value = node.value + 1
  node.next = null
  println(node.value)

  var address = &node.value
  *address = 7
  println(node.value)
  free(node)
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "42\n7\n")
}

func TestUseAfterFreePanics(t *testing.T) {
	src := memoryPrelude + `fn main() {
  var node = alloc<Node>()
  node.value = 1
  free(node)
  println(node.value)
}
`
	_, p := runSource(t, src)
	wantPanic(t, p, "use after free")
}

func TestDoubleFreePanics(t *testing.T) {
	src := memoryPrelude + `fn main() {
  var node = alloc<Node>()
  node.value = 1
  free(node)
  free(node)
}
`
	_, p := runSource(t, src)
	wantPanic(t, p, "double free")
}

func TestUninitializedFieldPanics(t *testing.T) {
	src := memoryPrelude + `fn main() {
  var node = alloc<Node>()
  println(node.value)
}
`
	_, p := runSource(t, src)
	wantPanic(t, p, "uninitialized")
}

func TestNullableChainWalk(t *testing.T) {
	src := memoryPrelude + `fn length(head: *Node): i32 {
  var count: i32 = 0
  var current: *Node? = head
  while current != null {
    count += 1
    current = current.next
  }
  return count
}

fn link(value: i32, next: *Node?): *Node {
  var node = alloc<Node>()
  node.value = value
  node.next = next
  return node
}

fn main() {
  var chain = link(1, link(2, link(3, null)))
  println(length(chain))
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "3\n")
}

// ---------------------------------------------------------------------------
// Defer
// ---------------------------------------------------------------------------

func TestDeferRunsAtBlockExitInReverse(t *testing.T) {
	out, p := runMain(t, `
  {
    defer println("first registered")
    defer println("second registered")
    println("body")
  }
  println("after the block")
`)
	wantOutput(t, out, p, "body\nsecond registered\nfirst registered\nafter the block\n")
}

func TestDeferRunsPerLoopIteration(t *testing.T) {
	out, p := runMain(t, `
  for i in 0..2 {
    defer println("exit #{i}")
    println("body #{i}")
  }
`)
	wantOutput(t, out, p, "body 0\nexit 0\nbody 1\nexit 1\n")
}

func TestDeferRunsOnEarlyReturn(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

fn f(early: bool): i32 {
  defer println("cleanup")
  if early {
    return 1
  }
  return 2
}

fn main() {
  println(f(true))
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "cleanup\n1\n")
}

func TestDeferCapturesArgumentsAtRegistration(t *testing.T) {
	out, p := runMain(t, `
  var x = 1
  {
    defer println(x)
    x = 2
    println(x)
  }
`)
	wantOutput(t, out, p, "2\n1\n")
}

// ---------------------------------------------------------------------------
// Closures
// ---------------------------------------------------------------------------

func TestClosuresCaptureByCopy(t *testing.T) {
	out, p := runMain(t, `
  var count = 1
  var show = fn() { println(count) }
  count = 99
  show()
  println(count)
`)
	wantOutput(t, out, p, "1\n99\n")
}

func TestClosureMutationStaysInside(t *testing.T) {
	out, p := runMain(t, `
  var count = 1
  var bump = fn() {
    count += 1
    println(count)
  }
  bump()
  bump()
  println(count)
`)
	// The closure owns one copy, taken at creation: its calls mutate that
	// copy cumulatively, and the original never notices.
	wantOutput(t, out, p, "2\n3\n1\n")
}

func TestClosureParametersAndExpressionBody(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

fn apply(v: i32, f: fn(i32): i32): i32 {
  return f(v)
}

fn main() {
  println(apply(21, fn(x) x * 2))
  var add = fn(a: i32, b: i32): i32 { return a + b }
  println(add(2, 3))
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "42\n5\n")
}

func TestMoveCapture(t *testing.T) {
	src := memoryPrelude + `fn make_release(value: i32): fn() {
  var node = alloc<Node>()
  node.value = value
  return fn[move node]() {
    println(node.value)
    free(node)
  }
}

fn main() {
  var release = make_release(7)
  release()
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "7\n")
}

// ---------------------------------------------------------------------------
// Methods
// ---------------------------------------------------------------------------

const stackSource = `import { println } from "@gyro/stdlib"

struct Counter {
  n: i32
}

fn Counter::bump(*self) {
  self.n += 1
}

fn Counter::peek(self): i32 {
  return self.n
}

fn Counter::broken_bump(self) {
  self.n += 1
}

`

func TestPointerReceiverMutates(t *testing.T) {
	src := stackSource + `fn main() {
  var c = Counter{ n: 0 }
  c.bump()
  c.bump()
  println(c.peek())
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "2\n")
}

func TestValueReceiverGetsACopy(t *testing.T) {
	src := stackSource + `fn main() {
  var c = Counter{ n: 0 }
  c.broken_bump()
  println(c.peek())
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "0\n")
}

func TestAssociatedFunction(t *testing.T) {
	src := stackSource + `fn Counter::at(n: i32): Counter {
  return Counter{ n: n }
}

fn main() {
  var c = Counter::at(5)
  println(c.peek())
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "5\n")
}

// ---------------------------------------------------------------------------
// Generics
// ---------------------------------------------------------------------------

func TestGenericFunctionAndBuiltinCompare(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

fn max<T>(a: T, b: T): T {
  if a.compare(b) > 0 {
    return a
  }
  return b
}

fn main() {
  println(max(3, 7))
  println(max(2.5, 1.5))
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "7\n2.5\n")
}

func TestGenericStruct(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

struct Pair<T> {
  first: T
  second: T
}

fn Pair<T>::swap(*self) {
  var tmp = self.first
  self.first = self.second
  self.second = tmp
}

fn main() {
  var pair = Pair<i32>{ 1, 2 }
  pair.swap()
  println(pair.first)
  var words = Pair{ first: "a", second: "b" }
  words.swap()
  println(words.first)
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "2\nb\n")
}

// ---------------------------------------------------------------------------
// Enums and match
// ---------------------------------------------------------------------------

const shapeSource = `import { println } from "@gyro/stdlib"

enum Shape {
  Circle(f64),
  Rect(f64, f64),
  Point,
}

`

func TestMatchDestructuresVariants(t *testing.T) {
	src := shapeSource + `fn area(s: Shape): f64 {
  return match s {
    Shape::Circle(r): 3.0 * r * r
    Shape::Rect(w, h): w * h
    Shape::Point: 0.0
  }
}

fn main() {
  println(area(Shape::Circle(2.0)))
  println(area(Shape::Rect(3.0, 4.0)))
  println(area(Shape::Point))
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "12\n12\n0\n")
}

func TestMatchLiteralsAndWildcard(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

fn classify(n: i32): string {
  return match n {
    0: "zero"
    1: "one"
    _: "many"
  }
}

fn main() {
  println(classify(0))
  println(classify(1))
  println(classify(7))
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "zero\none\nmany\n")
}

func TestMatchWithoutMatchingArmPanics(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

fn main() {
  var n: i32 = 5
  var s = match n {
    0: "zero"
  }
  println(s)
}
`
	_, p := runSource(t, src)
	wantPanic(t, p, "no match arm applies")
}

func TestReturnInsideMatchArm(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

fn pick(n: i32): string {
  var label = match n {
    0: return "early zero"
    _: "other"
  }
  return label
}

fn main() {
  println(pick(0))
  println(pick(3))
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "early zero\nother\n")
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

func TestErrorValuesFlow(t *testing.T) {
	src := `import { println, eprintln } from "@gyro/stdlib"
import { to_i32 } from "@gyro/convert"

struct RangeError {
  export value: i32
}

fn RangeError::message(self): string {
  return "value #{self.value} is out of range"
}

fn parse_port(text: string): (i32, error?) {
  var value, err = to_i32(text)
  if err != null {
    return 0, err
  }
  if value < 1 || value > 65535 {
    return 0, RangeError{ value: value }
  }
  return value, null
}

fn describe(text: string) {
  var port, err = parse_port(text)
  if err != null {
    match err {
      RangeError(e): println("range: #{e.value}")
      _: println("bad: #{err.message()}")
    }
    return
  }
  println("ok: #{port}")
}

fn main() {
  describe("8080")
  describe("70000")
  describe("nonsense")
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "ok: 8080\nrange: 70000\nbad: \"nonsense\" is not an i32\n")
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestChannelsAndSpawn(t *testing.T) {
	src := `import { println, spawn } from "@gyro/stdlib"

fn produce(out: chan<i32>, count: i32) {
  for var i = 0; i < count; i += 1 {
    out <- i * i
  }
  out.close()
}

fn main() {
  var values = chan<i32>(16)
  spawn(fn() produce(values, 5))

  var total: i32 = 0
  for v in values {
    total += v
  }
  println(total)
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "30\n")
}

func TestReceiveReportsClosed(t *testing.T) {
	out, p := runMain(t, `
  var ch = chan<i32>(1)
  ch <- 5
  ch.close()
  var v, open = <-ch
  println(v)
  println(open)
  var w, still = <-ch
  println(still)
`)
	wantOutput(t, out, p, "5\ntrue\nfalse\n")
}

func TestSendOnClosedChannelPanics(t *testing.T) {
	_, p := runMain(t, `
  var ch = chan<i32>(1)
  ch.close()
  ch <- 1
`)
	wantPanic(t, p, "closed channel")
}

func TestSelect(t *testing.T) {
	out, p := runMain(t, `
  var ready = chan<i32>(1)
  ready <- 42
  select {
    var v = <-ready: println("got #{v}")
    else: println("nothing")
  }
  select {
    var v = <-ready: println("got #{v}")
    else: println("nothing")
  }
`)
	wantOutput(t, out, p, "got 42\nnothing\n")
}

func TestMutex(t *testing.T) {
	src := `import { println, spawn, Mutex } from "@gyro/stdlib"

fn main() {
  var total = Mutex::new(0)
  var done = chan<bool>()

  spawn(fn() {
    for i in 0..100 {
      var guard = total.lock()
      defer guard.unlock()
      guard.set(guard.get() + 1)
    }
    done <- true
  })
  for i in 0..100 {
    var guard = total.lock()
    defer guard.unlock()
    guard.set(guard.get() + 1)
  }

  var _ = <-done
  var guard = total.lock()
  println(guard.get())
  guard.unlock()
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "200\n")
}

// ---------------------------------------------------------------------------
// Async
// ---------------------------------------------------------------------------

func TestAsyncAwait(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

async fn compute(n: i32): i32 {
  return n * 2
}

async fn main() {
  var doubled = await compute(21)
  println(doubled)

  var later = async fn(): i32 { return 7 }
  println(await later())
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "42\n7\n")
}

// ---------------------------------------------------------------------------
// Iteration
// ---------------------------------------------------------------------------

func TestIterationSources(t *testing.T) {
	out, p := runMain(t, `
  var sum: i32 = 0
  for x in [1, 2, 3] {
    sum += x
  }
  for i in 0..3 {
    sum += i
  }
  println(sum)
`)
	wantOutput(t, out, p, "9\n")
}

func TestDuckTypedIteration(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

struct Counter {
  n: i32
  limit: i32
}

fn Counter::next(*self): (i32, bool) {
  if self.n >= self.limit {
    return 0, false
  }
  self.n += 1
  return self.n, true
}

struct Bag {
  size: i32
}

fn Bag::iter(self): Counter {
  return Counter{ n: 0, limit: self.size }
}

fn main() {
  for x in (Counter{ n: 0, limit: 3 }) {
    println(x)
  }
  var total: i32 = 0
  for x in (Bag{ size: 4 }) {
    total += x
  }
  println(total)
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "1\n2\n3\n10\n")
}

func TestBreakAndContinue(t *testing.T) {
	out, p := runMain(t, `
  for i in 0..10 {
    if i == 2 {
      continue
    }
    if i == 5 {
      break
    }
    println(i)
  }
`)
	wantOutput(t, out, p, "0\n1\n3\n4\n")
}

func TestStringRunes(t *testing.T) {
	out, p := runMain(t, `
  var s = "héllo"
  println(s.len())
  var count: i32 = 0
  for r in s.runes() {
    count += 1
  }
  println(count)
`)
	wantOutput(t, out, p, "6\n5\n")
}

// ---------------------------------------------------------------------------
// Indexing and slices
// ---------------------------------------------------------------------------

func TestIndexingAndBounds(t *testing.T) {
	out, p := runMain(t, `
  var xs: u8[] = [10, 20, 30]
  println(xs[1])
  xs[1] = 25
  println(xs[1])
`)
	wantOutput(t, out, p, "20\n25\n")
}

func TestOutOfBoundsPanics(t *testing.T) {
	src := `fn main() {
  var xs = [1, 2, 3]
  var v = xs[7]
}
`
	_, p := runSource(t, src)
	wantPanic(t, p, "index 7 out of range for length 3")
}

func TestArrayStdlib(t *testing.T) {
	out, p := runMain(t, `
  var xs = Array<i32>::new()
  xs.push(4)
  xs.push(5)
  xs.set(0, 3)
  println(xs.get(0))
  println(xs.len())
  var total: i32 = 0
  for v in xs.slice() {
    total += v
  }
  println(total)
  xs.free()
`)
	wantOutput(t, out, p, "3\n2\n8\n")
}

func TestArrayDoubleFreePanics(t *testing.T) {
	_, p := runMain(t, `
  var xs = Array<i32>::new()
  xs.free()
  xs.free()
`)
	wantPanic(t, p, "double free")
}

// ---------------------------------------------------------------------------
// Modules
// ---------------------------------------------------------------------------

func TestCrossModuleExecution(t *testing.T) {
	out, p := run(t, map[string]string{
		"main.gyro": `import { println } from "@gyro/stdlib"
import { double, BASE } from "./lib"

fn main() {
  println(double(BASE))
}
`,
		"lib.gyro": `export const BASE: i32 = 21

export fn double(n: i32): i32 {
  return n * 2
}
`,
	}, "main.gyro")
	wantOutput(t, out, p, "42\n")
}

func TestModuleValuesInitializeInOrder(t *testing.T) {
	src := `import { println } from "@gyro/stdlib"

fn triple(n: i32): i32 {
  return n * 3
}

const BASE: i32 = 5
const SCALED: i32 = 15

fn main() {
  println(triple(BASE))
  println(SCALED)
}
`
	out, p := runSource(t, src)
	wantOutput(t, out, p, "15\n15\n")
}

// ---------------------------------------------------------------------------
// Panic reporting
// ---------------------------------------------------------------------------

func TestPanicCarriesPosition(t *testing.T) {
	src := `fn main() {
  var xs = [1, 2, 3]
  var v = xs[7]
}
`
	_, p := runSource(t, src)
	if p == nil {
		t.Fatal("expected a panic")
	}
	if p.Position.Line != 3 {
		t.Errorf("panic position line = %d, want 3", p.Position.Line)
	}
}

// TestExamplesRun executes every reference program end to end.
func TestExamplesRun(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.gyro"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no example programs found")
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			var bag diag.Bag
			program, err := loader.Load(path, &bag)
			if err != nil {
				t.Fatal(err)
			}
			sema.CheckProgram(program, &bag)
			if bag.HasErrors() {
				t.Fatal(bag.Items())
			}

			var stdout, stderr strings.Builder
			if err := interp.Run(program, &stdout, &stderr); err != nil {
				t.Fatalf("%v\nstdout:\n%s", err, stdout.String())
			}
			if stdout.Len() == 0 {
				t.Error("the example printed nothing")
			}
		})
	}
}
