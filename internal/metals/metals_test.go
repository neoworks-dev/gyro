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

// A float constant carries its literal text, and a small or large one is
// written with a signed exponent. The scanner has to keep the sign inside
// the number, or the line comes apart into two tokens and the printed
// module does not reparse.
func TestSignedExponentModule(t *testing.T) {
	roundTrip(t, `
module "gyro:test/exponents"

fn @constants() -> f64 {
  %tiny = const.f64 1e-09
  %huge = const.f64 1.23e+129
  %neg = const.f64 -1.23e-126
  %plain = const.f64 2.5
  %sum = fadd %tiny, %huge
  %rest = fadd %neg, %plain
  %total = fadd %sum, %rest
  return %total
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
  fn @render(any) -> any
  fn @screen() -> handle
}

export fn @show(%x: i32) -> i32 {
  %d = any.wrap %x
  %r = call @render(%d)
  %title = any.get %r, "title"
  any.set %r, "count", %d
  %v = any.unwrap %r -> i32
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

fn @scale_impl(%env: ptr<%Env>, %x: i32) -> i32 {
  %fp = field.ptr %env, 0
  %f = load %fp
  %r = imul %f, %x
  return %r
}

fn @apply(%g: fn(i32) -> i32, %x: i32) -> i32 {
  %r = call.indirect %g(%x)
  return %r
}

fn @use() -> i32 {
  %three = const.i32 3
  %env = new %Env
  %fp = field.ptr %env, 0
  store %fp, %three
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

global @counter: i32
global @name: str

fn @bump() -> i32 {
	%p = global.ptr @counter
	%n = load %p
	%one = const.i32 1
	%n = iadd %n, %one
	store %p, %n
	return %n
}

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

func TestOptOpsAndQuotedNames(t *testing.T) {
	roundTrip(t, `
module "gyro:test/optnames"

type %"Pair<i32>" = record { first: i32, second: i32 }

fn @"Pair<i32>::sum"(%self: %"Pair<i32>") -> i32 {
  %a = field.get %self, 0
  %b = field.get %self, 1
  %r = iadd %a, %b
  return %r
}

fn @"util::pick"(%o: opt<i32>) -> i32 {
  %null = const.null opt<i32>
  %missing = eq %o, %null
  if %missing {
    %zero = const.i32 0
    return %zero
  }
  %v = opt.unwrap %o
  return %v
}

fn @wrap(%x: i32) -> opt<i32> {
  %pair = record %"Pair<i32>"(%x, %x)
  %sum = call @"Pair<i32>::sum"(%pair)
  %o = opt.wrap %sum
  return %o
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
	verifyError(t, `module "gyro:test/v"

import "py:math" {
  fn @share(chan<i32>)
}
`, "values copy at the boundary")

	// A runtime with a heap of its own is what the rule is about, and a
	// pointer is the whole of what it refuses.
	verifyError(t, `module "gyro:test/v"

import "py:math" {
  fn @keep(ptr<i32>)
}
`, "values copy at the boundary")

	// A linked C library is a boundary of representation and not of
	// memory: it dereferences this address on this thread, over this
	// allocator, so an out-parameter and a callback both cross.
	roundTrip(t, `module "gyro:test/v"

import "c:SDL2" {
  fn @SDL_FreeSurface(ptr<i32>)
  fn @SDL_free(anyptr)
  fn @qsort(anyptr, u64, u64, code(anyptr, anyptr) -> i32)
}
`)

	// What it does not carry either way is a Gyro runtime object, which
	// C has no representation for at all.
	verifyError(t, `module "gyro:test/v"

import "c:SDL2" {
  fn @SDL_FreeSurface(ptr<chan<i32>>)
}
`, "values copy at the boundary")

	// The same signature is fine within one language.
	roundTrip(t, `module "gyro:test/v"

import "gyro:test/other" {
  fn @share(chan<i32>)
}
`)

	// And between two languages that compile into METALS, because the
	// program they become is one module on one heap.
	roundTrip(t, `module "gyro:test/v"

import "lisp:test/other" {
  fn @cell() -> ptr<i32>
}
`)

	// What a module exports is nobody's business until something imports
	// it, and only the linker knows out of which runtime.
	roundTrip(t, header+`export fn @cell() -> ptr<i32> {
  %p = slot i32
  return %p
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

func TestVerifierChecksOptOps(t *testing.T) {
	verifyError(t, header+`fn @f(%x: i32) {
  %v = opt.unwrap %x
  return
}
`, "opt.unwrap wants an opt")

	verifyError(t, header+`fn @f(%o: opt<i32>) {
  %oo = opt.wrap %o
  return
}
`, "optionals do not nest")
}

// ptr.erase and ptr.cast are the two halves of C's `void *`: taking a
// target's type off, which cannot fail, and putting one back, which is a
// claim. What the verifier holds is the shape of the pair — that only a
// pointer erases, that only an erased pointer is cast, and that
// optionality is carried through rather than gained or lost.
func TestAnyPtrRoundTrip(t *testing.T) {
	roundTrip(t, `
module "metals:test/erase"

type %Point = record { x: i32, y: i32 }

fn @keep(%p: anyptr) -> anyptr {
  return %p
}

export fn @main() {
  %n = slot i64
  %v = const.i64 7
  store %n, %v

  %e = ptr.erase %n
  %held = call @keep(%e)
  %back = ptr.cast %held -> ptr<i64>
  %got = load %back

  %pt = slot %Point
  %ep = ptr.erase %pt
  %same = eq %e, %ep

  %w = opt.wrap %n
  %ew = ptr.erase %w
  %none = const.null opt<anyptr>
  %present = ne %ew, %none
  %rw = ptr.cast %ew -> opt<ptr<i64>>
  return
}
`)
}

func TestSliceFromRoundTripsAndVerifies(t *testing.T) {
	roundTrip(t, header+`fn @view(%p: ptr<i32>, %n: usize) -> slice<i32> {
  %s = slice.from %p, %n
  return %s
}
`)

	verifyError(t, header+`fn @f(%x: i32, %n: usize) {
  %s = slice.from %x, %n
  return
}
`, "slice.from wants a pointer to the first element")

	// A pointer to an array is slice.of's operand: its extent is static.
	verifyError(t, header+`fn @f(%p: ptr<array<i32, 4>>, %n: usize) {
  %s = slice.from %p, %n
  return
}
`, "slice.from wants a pointer to an element")

	verifyError(t, header+`fn @f(%p: ptr<i32>, %n: i32) {
  %s = slice.from %p, %n
  return
}
`, "slice.from length is usize")
}

func TestIsizeIsASignedPointerWidthInteger(t *testing.T) {
	roundTrip(t, header+`fn @back(%a: isize, %b: isize) -> bool {
  %d = isub %a, %b
  %zero = const.isize 0
  %r = lt %d, %zero
  return %r
}
`)
	verifyError(t, header+`fn @f(%a: isize, %b: i64) {
  %d = isub %a, %b
  return
}
`, "isub wants matching operands, got isize and i64")
}

func TestSyscallRoundTripsAndVerifies(t *testing.T) {
	roundTrip(t, header+`fn @write(%n: usize, %fd: i32, %p: ptr<u8>, %len: u8, %maybe: opt<anyptr>) -> isize {
  %r = syscall %n(%fd, %p, %len, %maybe)
  return %r
}
`)
	roundTrip(t, header+`fn @getpid(%n: usize) -> isize {
  %r = syscall %n()
  return %r
}
`)
	// An isize is an integer like any other, and a file descriptor or a
	// directory handle such as AT_FDCWD (-100) is one.
	roundTrip(t, header+`fn @close(%n: usize, %fd: isize) -> isize {
  %r = syscall %n(%fd)
  return %r
}
`)

	verifyError(t, header+`fn @f(%n: i64) {
  %r = syscall %n()
  return
}
`, "syscall number is usize")

	// The kernel reads each argument from one register.
	verifyError(t, header+`fn @f(%n: usize, %x: f64) {
  %r = syscall %n(%x)
  return
}
`, "a syscall argument is one word")
	verifyError(t, header+`fn @f(%n: usize, %s: slice<u8>) {
  %r = syscall %n(%s)
  return
}
`, "a syscall argument is one word")
	verifyError(t, header+`fn @f(%n: usize, %x: opt<i64>) {
  %r = syscall %n(%x)
  return
}
`, "a syscall argument is one word")

	verifyError(t, header+`fn @f(%n: usize, %a: i64) {
  %r = syscall %n(%a, %a, %a, %a, %a, %a, %a)
  return
}
`, "syscall takes at most six arguments")
}

func TestLocRoundTripsAndVerifies(t *testing.T) {
	roundTrip(t, header+`fn @f() {
  loc "/src/a b.gyro" 12
  return
}
`)
	verifyError(t, header+`fn @f() {
  loc "a.gyro" 0
  return
}
`, "loc lines count from 1")
	verifyError(t, header+`fn @f() {
  loc "" 3
  return
}
`, "loc wants a file name")
}

func TestVerifierChecksPointerErasure(t *testing.T) {
	verifyError(t, header+`fn @f(%x: i32) {
  %e = ptr.erase %x
  return
}
`, "ptr.erase wants a ptr<T> or an opt<ptr<T>>")

	// A code pointer is not an object pointer, and there is nothing at
	// the other end of one to have a type erased.
	verifyError(t, header+`fn @f(%c: code(i32) -> i32) {
  %e = ptr.erase %c
  return
}
`, "ptr.erase wants a ptr<T> or an opt<ptr<T>>")

	verifyError(t, header+`fn @f(%p: ptr<i32>) {
  %q = ptr.cast %p -> ptr<i64>
  return
}
`, "wants an anyptr")

	verifyError(t, header+`fn @f(%p: ptr<i32>) {
  %e = ptr.erase %p
  %q = ptr.cast %e -> i64
  return
}
`, "is not one")

	// Optionality is carried, not conjured: an erased bare pointer does
	// not become nullable on the way back.
	verifyError(t, header+`fn @f(%p: ptr<i32>) {
  %e = ptr.erase %p
  %q = ptr.cast %e -> opt<ptr<i32>>
  return
}
`, "wants an opt<anyptr>")
}

// A union is one storage with several readings of it. What the format
// carries is the members and their types; where they sit is the layout
// table's answer, and it is the same answer for all of them.
func TestUnionRoundTrip(t *testing.T) {
	roundTrip(t, `
module "cc:test/union"

type %Key = record { kind: u32, code: u32 }
type %Event = union { kind: u32, key: %Key }

export fn @tag(%e: ptr<%Event>) -> u32 {
  %p = field.ptr %e, 0
  %k = load %p
  return %k
}
`)
}

func TestUnionLayoutIsTheWidestMember(t *testing.T) {
	module, err := metals.Parse(`module "cc:test/u"

type %Wide = record { a: i64, b: i64 }
type %Small = union { n: i32, wide: %Wide, pad: array<u8, 3> }
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	layout := metals.NewLayout(module, func(format string, args ...any) {
		t.Fatalf("layout: "+format, args...)
	})
	union := &metals.Named{Name: "Small"}
	if got := layout.Size(union); got != 16 {
		t.Errorf("size of the union is %d, and its widest member is 16", got)
	}
	if got := layout.Align(union); got != 8 {
		t.Errorf("alignment of the union is %d, and its strictest member wants 8", got)
	}
	// Every member begins where the union does. That is the whole of
	// what a union is at this level.
	decl := module.Types[1]
	for index := range decl.Members() {
		if got := layout.MemberOffset(decl, index); got != 0 {
			t.Errorf("member %d of the union sits at %d, and a union's members all sit at 0", index, got)
		}
	}
}

// An alignment beyond what the members require. It exists for C's
// bit-fields, whose storage is bytes and whose alignment came from
// declared types no longer in the member list.
func TestDeclaredAlignment(t *testing.T) {
	roundTrip(t, `
module "cc:test/align"

type %Flags = record align 4 { bits.0: array<u8, 1>, tail: i8 }
type %Small = union align 8 { bits.0: array<u8, 2> }
`)

	module, err := metals.Parse(`module "cc:test/a"

type %Flags = record align 4 { bits.0: array<u8, 1>, tail: i8 }
type %Plain = record { bits.0: array<u8, 1>, tail: i8 }
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	layout := metals.NewLayout(module, func(format string, args ...any) {
		t.Fatalf("layout: "+format, args...)
	})
	// The members are two bytes either way; the alignment is what rounds
	// the whole up and moves nothing.
	if got := layout.Size(&metals.Named{Name: "Plain"}); got != 2 {
		t.Errorf("without an alignment the record is %d bytes, and its members are 2", got)
	}
	if got := layout.Size(&metals.Named{Name: "Flags"}); got != 4 {
		t.Errorf("aligned to 4 the record is %d bytes, and 2 rounds up to 4", got)
	}
	if got := layout.Align(&metals.Named{Name: "Flags"}); got != 4 {
		t.Errorf("the record's alignment is %d, and it was declared as 4", got)
	}
	if got := layout.FieldOffset(module.Types[0].Record, 1); got != 1 {
		t.Errorf("the second field is at %d; an alignment rounds the whole and moves no member", got)
	}
}

func TestVerifierRefusesABadAlignment(t *testing.T) {
	verifyError(t, header+`type %R = record align 3 { a: i8 }

fn @f() {
  return
}
`, "an alignment is a power of two up to 8")

	verifyError(t, header+`type %R = record align 16 { a: i8 }

fn @f() {
  return
}
`, "an alignment is a power of two up to 8")
}

func TestVerifierChecksUnions(t *testing.T) {
	const shapes = header + "type %U = union { n: i32, f: f64 }\ntype %R = record { u: %U }\n\n"

	// Comparing storage nothing records the meaning of has no answer,
	// and a record holding a union is the same question one level down.
	verifyError(t, shapes+`fn @f(%a: %U, %b: %U) {
  %r = eq %a, %b
  return
}
`, "nothing records which member %U holds")

	verifyError(t, shapes+`fn @f(%a: %R, %b: %R) {
  %r = eq %a, %b
  return
}
`, "nothing records which member %U holds")

	// A union is storage with nothing saying which member wrote it, so
	// what would arrive on the other side of a boundary is a shape the
	// far side has no way to read.
	verifyError(t, `module "gyro:test/v"

type %U = union { n: i32, f: f64 }

import "js:./widgets" {
  fn @render(%U)
}
`, "values copy at the boundary")

	// The members are reached the way a record's fields are, and the
	// shapes that have no members say so.
	verifyError(t, shapes+`fn @f(%p: ptr<%U>) {
  %m = field.ptr %p, 5
  return
}
`, "field 5 out of range")

	verifyError(t, shapes+`fn @f(%u: %U) {
  %m = field.get %u, 0
  return
}
`, "%U is a union, not a record")

	verifyError(t, header+`type %U = union { }

fn @f() {
  return
}
`, "union of nothing")
}

// An opaque declaration says a name exists and nothing else. What that
// buys is a pointer that keeps a type it cannot look inside; what it
// costs is every operation that would measure the object, which is the
// same list C refuses on a pointer to an incomplete type.
func TestOpaqueTypeRoundTrip(t *testing.T) {
	roundTrip(t, `
module "cc:test/opaque"

type %Session = opaque
type %Guts = record { seed: i64 }

export fn @open(%g: ptr<%Guts>) -> ptr<%Session> {
  %e = ptr.erase %g
  %h = ptr.cast %e -> ptr<%Session>
  return %h
}

export fn @same(%a: opt<ptr<%Session>>, %b: opt<ptr<%Session>>) -> bool {
  %r = eq %a, %b
  return %r
}
`)
}

func TestVerifierRefusesAnOpaqueTypeWithALayout(t *testing.T) {
	const opaque = header + "type %S = opaque\n\n"

	// Every position but a pointer's target: a value, a member, an
	// element, a result. None of them has a size to reserve.
	verifyError(t, opaque+`fn @f(%s: %S) {
  return
}
`, "only a pointer to it has a layout")

	verifyError(t, opaque+`type %R = record { s: %S }

fn @f() {
  return
}
`, "only a pointer to it has a layout")

	verifyError(t, opaque+`fn @f() {
  %s = slot array<%S, 4>
  return
}
`, "only a pointer to it has a layout")

	verifyError(t, opaque+`fn @f() {
  %s = slot %S
  return
}
`, "only a pointer to it has a layout")

	// And the four instructions that measure what a pointer addresses.
	verifyError(t, opaque+`fn @f(%p: ptr<%S>) {
  %v = load %p
  return
}
`, "load needs %S's layout, and it is opaque")

	verifyError(t, opaque+`fn @f(%p: ptr<%S>, %q: ptr<%S>) {
  %n = const.i64 1
  %r = ptr.offset %p, %n
  return
}
`, "ptr.offset needs %S's layout, and it is opaque")

	verifyError(t, opaque+`fn @f(%p: ptr<%S>, %q: ptr<%S>) {
  %d = ptr.diff %p, %q
  return
}
`, "ptr.diff needs %S's layout, and it is opaque")

	verifyError(t, opaque+`fn @f(%p: ptr<%S>) {
  %v = field.ptr %p, 0
  return
}
`, "opaque, so it has no fields to reach")
}

// An address is this program's address space whether or not the format
// still knows what is at it. Forgetting the target's type makes one less
// crossable rather than more.
func TestVerifierRefusesAnyPtrAtABoundary(t *testing.T) {
	// An address forgets its target's type; it does not stop being an
	// address in this process, so a runtime with a heap of its own has
	// nothing to do with one.
	verifyError(t, `module "gyro:test/v"

import "py:math" {
  fn @free(anyptr)
}
`, "values copy at the boundary")
}

func TestVerifierChecksAnyOperands(t *testing.T) {
	verifyError(t, header+`fn @f(%x: i32) {
  %r = any.call %x()
  return
}
`, "works on any values")

	verifyError(t, header+`fn @f(%p: ptr<i32>) {
  %d = any.wrap %p
  return
}
`, "cannot become any")
}

// ---------------------------------------------------------------------------
// Linear memory
// ---------------------------------------------------------------------------

func TestLinearMemoryModule(t *testing.T) {
	roundTrip(t, `
module "csrc:test/mem"

memory @heap 1, 16
data @heap, 8, "hi\x00\xff"

export fn @sum(%n: u32) -> i32 {
  %total = const.i32 0
  %i = const.u32 0
  %four = const.u32 4
  block $done {
    loop $top {
      %atEnd = ge %i, %n
      br_if %atEnd, $done
      %off = wmul %i, %four
      %v = mem.load.i32 @heap, %off
      %total = iadd %total, %v
      %one = const.u32 1
      %i = wadd %i, %one
      br $top
    }
  }
  return %total
}

export fn @scribble(%at: u32, %v: i32) {
  %byte = trunc %v -> u8
  mem.store.u8 @heap, %at, %byte
  %len = const.u32 4
  mem.fill @heap, %at, %byte, %len
  %from = const.u32 0
  mem.copy @heap, %at, %from, %len
  return
}

export fn @sizes() -> i32 {
  %pages = mem.size @heap
  %grown = mem.grow @heap, %pages
  return %grown
}

export fn @bits(%x: i32) -> u32 {
  %u = bitcast %x -> u32
  %shifted = shr %u, %u
  return %shifted
}
`)
}

func TestVerifierChecksMemoryOperands(t *testing.T) {
	const mem = "module \"csrc:test/v\"\n\nmemory @heap 1\n\n"

	verifyError(t, mem+`fn @f(%a: i32) {
  %v = mem.load.i32 @heap, %a
  return
}
`, "addresses memory with a u32")

	verifyError(t, mem+`fn @f(%a: u32) {
  %v = mem.load.i32 @nowhere, %a
  return
}
`, "no memory @nowhere")

	// A store of a wider register must say which bits it keeps.
	verifyError(t, mem+`fn @f(%a: u32, %v: i32) {
  mem.store.u8 @heap, %a, %v
  return
}
`, "trunc says which bits to keep")

	verifyError(t, mem+`fn @f(%a: u32, %b: u8) {
  mem.copy @heap, %a, %b, %a
  return
}
`, "counts bytes in u32")
}

func TestVerifierChecksPtrAddrAndPtrFrom(t *testing.T) {
	// The accepted forms, round-tripped through the printer.
	roundTrip(t, header+`fn @f(%p: ptr<i64>) -> opt<anyptr> {
  %a = ptr.erase %p
  %n = ptr.addr %a
  %b = ptr.from %n -> anyptr
  %c = ptr.cast %b -> ptr<i64>
  %none = const.null opt<anyptr>
  %z = ptr.addr %none
  %o = ptr.from %z -> opt<anyptr>
  return %o
}
`)

	// A typed pointer is erased first, so the number never carries a type.
	verifyError(t, header+`fn @f(%p: ptr<i64>) {
  %n = ptr.addr %p
  return
}
`, "ptr.addr wants an anyptr or an opt<anyptr>, not ptr<i64>")

	// A linear memory's address is a u32, and it is not taken as one.
	verifyError(t, header+`fn @f(%n: u32) {
  %a = ptr.from %n -> anyptr
  return
}
`, "ptr.from wants a usize, not u32")

	// Where and what are two claims, and ptr.from makes only the first.
	verifyError(t, header+`fn @f(%n: usize) {
  %a = ptr.from %n -> ptr<i64>
  return
}
`, "ptr.from makes an anyptr or an opt<anyptr>, not ptr<i64>")

	// A function that reaches a memory may not claim an address, in either
	// order, or an offset into the memory becomes a pointer through conv.
	const mem = "module \"csrc:test/v\"\n\nmemory @heap 1\n\n"
	verifyError(t, mem+`fn @f(%a: u32) {
  %v = mem.load.u32 @heap, %a
  %w = conv %v -> usize
  %p = ptr.from %w -> anyptr
  return
}
`, "ptr.from and a linear memory in one function")
	verifyError(t, mem+`fn @f(%a: u32) {
  %w = conv %a -> usize
  %p = ptr.from %w -> anyptr
  %v = mem.load.u32 @heap, %a
  return
}
`, "ptr.from and a linear memory in one function")

	// The rule is the function's, not the module's, so it survives
	// linking: a module may have both, in two functions.
	roundTrip(t, mem+`fn @g(%a: u32) -> u32 {
  %v = mem.load.u32 @heap, %a
  return %v
}

fn @h(%n: usize) -> anyptr {
  %p = ptr.from %n -> anyptr
  return %p
}
`)
}

func TestVerifierChecksTruncAndBitcast(t *testing.T) {
	verifyError(t, header+`fn @f(%x: u8) {
  %r = trunc %x -> i64
  return
}
`, "widens; conv is the widening one")

	verifyError(t, header+`fn @f(%x: i32) {
  %r = bitcast %x -> i64
  return
}
`, "changes width")

	verifyError(t, header+`fn @f(%x: f64) {
  %r = trunc %x -> i32
  return
}
`, "trunc narrows integers")
}

func TestVerifierChecksDataSegments(t *testing.T) {
	verifyError(t, "module \"csrc:test/v\"\n\nmemory @heap 1\ndata @other, 0, \"x\"\n",
		"names no memory @other")

	verifyError(t, "module \"csrc:test/v\"\n\nmemory @heap 1\ndata @heap, 65535, \"ab\"\n",
		"does not fit memory @heap")
}

// An import may declare a type as well as a function. The shape is
// written out in full, because a module holding one has to lay it out
// and because Link has to have something to compare.
func TestImportedTypeRoundTrip(t *testing.T) {
	roundTrip(t, `
module "gyro:main"

import "cc:shapes" {
  type %Point = record { x: i32, y: i32 }
  type %Line = record { a: %Point, b: %Point }
  type %Arena = opaque
  fn @span(%Line) -> i32
  fn @open() -> opt<ptr<%Arena>>
}

export fn @main() {
  %s = slot %Line
  %l = load %s
  %n = call @span(%l)
  return
}
`)
}

// An imported type is laid out here like any other: the module reserves
// storage for one, so it has to know how big one is.
func TestImportedTypeHasALayout(t *testing.T) {
	module, err := metals.Parse(`
module "gyro:main"

import "cc:shapes" {
  type %Point = record { x: i32, y: i32 }
  fn @flip(%Point) -> %Point
}

export fn @main() {
  return
}
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	layout := metals.NewLayout(module, func(format string, args ...any) {
		t.Fatalf("layout: "+format, args...)
	})
	if size := layout.Size(&metals.Named{Name: "Point"}); size != 8 {
		t.Errorf("an imported record of two i32 is %d bytes, want 8", size)
	}
}

func TestVerifierRefusesATypeBothDeclaredAndImported(t *testing.T) {
	verifyError(t, `
module "gyro:main"

type %Point = record { x: i32 }

import "cc:shapes" {
  type %Point = record { x: i32 }
  fn @zero() -> i32
}

export fn @main() {
  return
}
`, "type %Point is declared twice")
}

func TestVerifierRefusesATypeImportedTwice(t *testing.T) {
	verifyError(t, `
module "gyro:main"

import "cc:shapes" {
  type %Point = record { x: i32 }
  fn @zero() -> i32
}

import "cc:other" {
  type %Point = record { x: i32 }
  fn @one() -> i32
}

export fn @main() {
  return
}
`, "type %Point is imported twice")
}
