package metals_test

import (
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// roundTrip parses, verifies, prints, reparses and reprints, and demands
// the two printings agree — the text form is canonical.
func roundTrip(t *testing.T, source string) *metals.Module {
	t.Helper()

	module, err := metals.Parse(source)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatalf("verify: %v", err)
	}

	printed := metals.Print(module)
	again, err := metals.Parse(printed)
	if err != nil {
		t.Fatalf("reparse of printed form: %v\n%s", err, printed)
	}
	if reprinted := metals.Print(again); reprinted != printed {
		t.Errorf("print is not canonical:\nfirst:\n%s\nsecond:\n%s", printed, reprinted)
	}
	return module
}

// verifyError expects parsing to succeed and verification to fail.
func verifyError(t *testing.T, source, fragment string) {
	t.Helper()
	module, err := metals.Parse(source)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = metals.Verify(module)
	if err == nil {
		t.Fatalf("expected a verification error containing %q", fragment)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Errorf("error %q does not contain %q", err, fragment)
	}
}

// ---------------------------------------------------------------------------
// Round trips
// ---------------------------------------------------------------------------

func TestFibonacciModule(t *testing.T) {
	roundTrip(t, `
module "gyro:test/fib"

import "gyro:rt" {
  fn @println_i32(i32)
}

export fn @fib(%n: i32) -> i32 {
  %one = const.i32 1
  %stop = le %n, %one
  if %stop {
    return %n
  }
  %a = isub %n, %one
  %fa = call @fib(%a)
  %two = const.i32 2
  %b = isub %n, %two
  %fb = call @fib(%b)
  %sum = iadd %fa, %fb
  return %sum
}

fn @main() {
  %n = const.i32 10
  %r = call @fib(%n)
  call @println_i32(%r)
  return
}
`)
}

func TestShapesModule(t *testing.T) {
	roundTrip(t, `
module "gyro:test/shapes"

type %Point = record { x: i32, y: i32 }
type %Shape = enum { Circle(f64), Rect(f64, f64), Dot }

fn @origin() -> %Point {
  %zero = const.i32 0
  %p = record %Point(%zero, %zero)
  %x = field.get %p, 0
  field.set %p, 1, %x
  return %p
}

fn @area(%s: %Shape) -> f64 {
  %tag = variant.tag %s
  %result = const.f64 0.0
  switch %tag {
    case 0 {
      %r = variant.field %s, 0, 0
      %pi = const.f64 3.14159
      %rr = fmul %r, %r
      %result = fmul %pi, %rr
    }
    case 1 {
      %w = variant.field %s, 1, 0
      %h = variant.field %s, 1, 1
      %result = fmul %w, %h
    }
    default {
    }
  }
  return %result
}

fn @rect(%w: f64, %h: f64) -> %Shape {
  %s = variant %Shape, 1(%w, %h)
  return %s
}

fn @dot() -> %Shape {
  %s = variant %Shape, 2
  return %s
}
`)
}

func TestLoopsAndMemoryModule(t *testing.T) {
	roundTrip(t, `
module "gyro:test/loops"

type %Node = record { value: i32 }

fn @sum_below(%n: i32) -> i32 {
	%sum = const.i32 0
	%i = const.i32 0
	%one = const.i32 1
	block $done {
		loop $top {
			%stop = ge %i, %n
			br_if %stop, $done
			%sum = iadd %sum, %i
			%i = iadd %i, %one
			br $top
		}
	}
	return %sum
}

fn @boxed(%v: i32) -> i32 {
	%p = new %Node
	%vp = field.ptr %p, 0
	store %vp, %v
	%out = load %vp
	free %p
	%cell = slot i32
	store %cell, %out
	%again = load %cell
	return %again
}

fn @pick(%xs: slice<i32>, %i: usize) -> i32 {
	%v = index.get %xs, %i
	return %v
}

fn @build() -> i32 {
	%a = const.i32 10
	%b = const.i32 20
	%arr = array<i32>(%a, %b)
	%s = slice.of %arr
	%n = len %s
	%zero = const.usize 0
	index.set %s, %zero, %b
	%first = call @pick(%s, %zero)
	return %first
}
`)
}

func TestConcurrencyModule(t *testing.T) {
	roundTrip(t, `
module "gyro:test/chans"

import "gyro:rt" {
  fn @spawn(fn())
}

async fn @first(%ch: chan<i32>) -> i32 {
  %t = recv %ch
  %v = tuple.get %t, 0
  %open = tuple.get %t, 1
  if %open {
    return %v
  }
  %zero = const.i32 0
  return %zero
}

async fn @relay(%ch: chan<i32>) -> i32 {
  %f = call @first(%ch)
  %v = await %f
  return %v
}

fn @pump(%vals: chan<i32>, %quit: chan<bool>) {
  %yes = const.bool true
  select {
    recv %v = %vals {
      chan.close %quit
      return
    }
    send %quit, %yes {
      return
    }
    default {
    }
  }
  %cap = const.usize 4
  %fresh = chan.new<i32> %cap
  %unbuffered = chan.new<bool>
  chan.close %fresh
  chan.close %unbuffered
  return
}
`)
}

func TestInteropModule(t *testing.T) {
	roundTrip(t, `
module "gyro:test/interop"

import "js:./widgets" {
  fn @render(dyn) -> dyn
  fn @screen() -> handle
}

export fn @show(%x: i32) -> i32 {
  %d = dyn.wrap %x
  %r = call @render(%d)
  %title = dyn.get %r, "title"
  dyn.set %r, "count", %d
  %v = dyn.unwrap %r -> i32
  return %v
}

export fn @greeting(%who: str) -> str {
  %prefix = const.str "hello, "
  %s = str.concat %prefix, %who
  return %s
}
`)
}

func TestClosuresModule(t *testing.T) {
	roundTrip(t, `
module "gyro:test/closures"

type %Env = record { factor: i32 }

fn @scale_impl(%env: %Env, %x: i32) -> i32 {
  %f = field.get %env, 0
  %r = imul %f, %x
  return %r
}

fn @apply(%g: fn(i32) -> i32, %x: i32) -> i32 {
  %r = call.indirect %g(%x)
  return %r
}

fn @use() -> i32 {
  %three = const.i32 3
  %env = record %Env(%three)
  %scale = closure @scale_impl, %env
  %seven = const.i32 7
  %r = call @apply(%scale, %seven)
  %direct = fn.ref @use
  %n = call.indirect %direct()
  %sum = iadd %r, %n
  return %sum
}
`)
}

func TestGlobalsAndConversions(t *testing.T) {
	roundTrip(t, `
module "gyro:test/misc"

global @MAX: i32 = 100
global @NAME: str = "gyro"

fn @mask(%x: i32) -> u8 {
	%ff = const.i32 255
	%low = band %x, %ff
	%b = conv %low -> u8
	return %b
}

fn @wrapping(%h: u32) -> u32 {
	%prime = const.u32 16777619
	%r = wmul %h, %prime
	%one = const.u32 1
	%r = wadd %r, %one
	return %r
}

fn @nullable(%x: i32) -> opt<i32> {
	%none = const.null opt<i32>
	%yes = const.bool true
	if %yes {
		return %none
	}
	return %none
}
`)
}

// ---------------------------------------------------------------------------
// Verifier
// ---------------------------------------------------------------------------

const header = "module \"gyro:test/v\"\n\n"

func TestVerifierCatchesTypeMismatch(t *testing.T) {
	verifyError(t, header+`fn @f(%a: i32, %b: f64) {
  %r = iadd %a, %b
  return
}
`, "matching operands")
}

func TestVerifierCatchesUndefinedRegister(t *testing.T) {
	verifyError(t, header+`fn @f() {
  %r = copy %ghost
  return
}
`, "%ghost is never assigned")
}

func TestVerifierFixesRegisterTypes(t *testing.T) {
	verifyError(t, header+`fn @f(%a: i32) {
  %r = copy %a
  %r = const.str "no"
  return
}
`, "fixed at first assignment")
}

func TestVerifierTracksDefinednessAcrossBranches(t *testing.T) {
	verifyError(t, header+`fn @f(%c: bool) -> i32 {
  if %c {
    %r = const.i32 1
  }
  return %r
}
`, "may not be assigned on this path")
}

func TestVerifierAcceptsBothBranchAssignment(t *testing.T) {
	roundTrip(t, header+`fn @f(%c: bool) -> i32 {
  if %c {
    %r = const.i32 1
  } else {
    %r = const.i32 2
  }
  return %r
}
`)
}

func TestVerifierCatchesUnknownLabel(t *testing.T) {
	verifyError(t, header+`fn @f() {
  block $a {
    br $b
  }
  return
}
`, "no enclosing label $b")
}

func TestVerifierCatchesAwaitOutsideAsync(t *testing.T) {
	verifyError(t, header+`async fn @g() -> i32 {
  %one = const.i32 1
  return %one
}

fn @f() -> i32 {
  %fut = call @g()
  %v = await %fut
  return %v
}
`, "await only works inside an async fn")
}

func TestVerifierEnforcesBoundaryRules(t *testing.T) {
	verifyError(t, header+`export fn @leak(%p: ptr<i32>) {
  return
}
`, "values copy at the boundary")

	verifyError(t, `module "gyro:test/v"

import "py:math" {
  fn @share(chan<i32>)
}
`, "values copy at the boundary")

	// The same signature is fine within one language.
	roundTrip(t, `module "gyro:test/v"

import "gyro:test/other" {
  fn @share(chan<i32>)
}
`)
}

func TestVerifierChecksSends(t *testing.T) {
	verifyError(t, header+`fn @f(%ch: chan<i32>, %s: str) {
  send %ch, %s
  return
}
`, "send of str on chan<i32>")
}

func TestVerifierChecksIndexType(t *testing.T) {
	verifyError(t, header+`fn @f(%xs: slice<i32>, %i: i32) {
  %v = index.get %xs, %i
  return
}
`, "indexes are usize")
}

func TestVerifierChecksRecordShape(t *testing.T) {
	verifyError(t, header+`type %P = record { x: i32, y: i32 }

fn @f(%a: i32) {
  %p = record %P(%a)
  return
}
`, "has 2 fields, the record sets 1")
}

func TestVerifierChecksCalls(t *testing.T) {
	verifyError(t, header+`fn @f() {
  call @ghost()
  return
}
`, "unknown function @ghost")

	verifyError(t, header+`fn @g(%a: i32) {
  return
}

fn @f(%s: str) {
  call @g(%s)
  return
}
`, "argument 1 is str, not i32")
}

func TestVerifierChecksNullConstants(t *testing.T) {
	verifyError(t, header+`fn @f() {
  %n = const.null i32
  return
}
`, "const.null wants an opt")
}

func TestVerifierChecksClosureEnv(t *testing.T) {
	verifyError(t, header+`type %Env = record { n: i32 }

fn @impl(%env: %Env, %x: i32) -> i32 {
  return %x
}

fn @f(%wrong: i32) {
  %c = closure @impl, %wrong
  return
}
`, "closure environment is i32")
}

func TestVerifierChecksDuplicateCases(t *testing.T) {
	verifyError(t, header+`fn @f(%t: i32) {
  switch %t {
    case 0 {
    }
    case 0 {
    }
  }
  return
}
`, "case 0 appears twice")
}

func TestVerifierChecksDynOperands(t *testing.T) {
	verifyError(t, header+`fn @f(%x: i32) {
  %r = dyn.call %x()
  return
}
`, "works on dyn values")

	verifyError(t, header+`fn @f(%p: ptr<i32>) {
  %d = dyn.wrap %p
  return
}
`, "cannot become dyn")
}
