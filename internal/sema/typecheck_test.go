package sema

import "testing"

// The type checker's tests use the same helpers as the resolution tests:
// checkOK and checkError for single files, programOK and programError for
// cross-module behaviour.

// ---------------------------------------------------------------------------
// Literals and context typing
// ---------------------------------------------------------------------------

func TestLiteralsAdaptToContext(t *testing.T) {
	checkOK(t, "fn t() {\n  var h: u32 = 2166136261\n}\n")
	checkOK(t, "fn t() {\n  var b: u8[] = [104, 105]\n}\n")
	checkOK(t, "fn t() {\n  var f: f64 = 1\n}\n")
	checkOK(t, "fn t(n: i64 = 4) {\n}\n")
	checkOK(t, "fn t() {\n  var x: i8 = -128\n}\n")
}

func TestLiteralsMustFit(t *testing.T) {
	checkError(t, "fn t() {\n  var x: u8 = 300\n}\n", "does not fit u8")
	checkError(t, "fn t() {\n  var x: i8 = -129\n}\n", "does not fit i8")
	checkError(t, "fn t() {\n  var b: u8[] = [104, 300]\n}\n", "does not fit u8")
	checkError(t, "fn t() {\n  var x: i32 = 1.5\n}\n", "a float literal cannot become i32")
}

func TestConstantExpressionsFold(t *testing.T) {
	checkOK(t, "fn t() {\n  var x: u8 = 200 + 55\n}\n")
	checkError(t, "fn t() {\n  var x: u8 = 200 + 56\n}\n", "does not fit u8")
	checkOK(t, "fn t() {\n  var x: u64 = 1 << 40\n}\n")
	checkError(t, "fn t() {\n  var x: i32 = 1 << 40\n}\n", "does not fit i32")
	checkError(t, "fn t() {\n  var x = 1 / 0\n}\n", "division by zero")
}

func TestLiteralDefaults(t *testing.T) {
	// An integer literal defaults to i32, a float literal to f64.
	checkOK(t, "fn want32(v: i32) {\n}\n\nfn t() {\n  var x = 1\n  want32(x)\n}\n")
	checkError(t, "fn want64(v: i64) {\n}\n\nfn t() {\n  var x = 1\n  want64(x)\n}\n", "cannot use i32 as i64")
	checkOK(t, "fn wantF(v: f64) {\n}\n\nfn t() {\n  var x = 1.5\n  wantF(x)\n}\n")
}

func TestTypedValuesNeverConvert(t *testing.T) {
	checkError(t, "fn t() {\n  var a: i32 = 1\n  var b: i64 = a\n}\n", "cannot use i32 as i64")
	checkError(t, "fn t() {\n  var a: u8 = 1\n  var b: u16 = a\n}\n", "cannot use u8 as u16")
	checkError(t, "fn t() {\n  var f: f32 = 1.0\n  var g: f64 = f\n}\n", "cannot use f32 as f64")
}

func TestOtherLiterals(t *testing.T) {
	checkOK(t, "fn t() {\n  var r: rune = 'a'\n  var s: string = \"hi\"\n  var b: bool = true\n}\n")
	checkError(t, "fn t() {\n  var r: i32 = 'a'\n}\n", "cannot use rune as i32")
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

// A literal that cannot take the other operand's type is one error. The
// poisoned type it leaves behind used to produce a second, blaming
// <unknown> for a mismatch the author never wrote.
func TestABadLiteralOperandReportsOnce(t *testing.T) {
	bag := check(t, "fn t(x: i32) {\n  var c = 1.5 + x\n}\n")
	if count := len(bag.Items()); count != 1 {
		t.Fatalf("expected one diagnostic, got %d: %v", count, bag.Items())
	}
}

func TestArithmeticNeedsMatchingTypes(t *testing.T) {
	checkOK(t, "fn t(a: i32, b: i32): i32 {\n  return a + b * 2\n}\n")
	checkError(t, "fn t(a: i32, b: u8) {\n  var x = a + b\n}\n", "operator + needs matching numeric operands")
	checkError(t, "fn t(a: i32, b: i64) {\n  var x = a * b\n}\n", "matching numeric operands")
	checkError(t, "fn t(s: string) {\n  var x = s + s\n}\n", "interpolate instead")
	checkError(t, "fn t(b: bool) {\n  var x = b + b\n}\n", "matching numeric operands")
}

func TestWrappingAndBitwiseAreIntegerOnly(t *testing.T) {
	checkOK(t, "fn t(h: u32): u32 {\n  return h *% 16777619 +% 1\n}\n")
	checkError(t, "fn t(f: f64) {\n  var x = f +% f\n}\n", "matching integer operands")
	checkError(t, "fn t(f: f64) {\n  var x = f % f\n}\n", "matching integer operands")
	checkOK(t, "fn t(a: u8, b: u8): u8 {\n  return (a & b) | (a ^ b)\n}\n")
}

func TestShifts(t *testing.T) {
	checkOK(t, "fn t(x: u32): u32 {\n  return x << 3\n}\n")
	checkError(t, "fn t(f: f64) {\n  var x = f << 1\n}\n", "shifts integers")
	checkError(t, "fn t(x: i32, f: f64) {\n  var y = x << f\n}\n", "a shift count is an integer")
}

func TestComparisons(t *testing.T) {
	checkOK(t, "fn t(a: i32, b: i32): bool {\n  return a < b\n}\n")
	checkOK(t, "fn t(a: rune): bool {\n  return a >= 'a'\n}\n")
	checkError(t, "fn t(a: string, b: string) {\n  var x = a < b\n}\n", "a.compare(b)")
	checkError(t, "fn t(a: i32, b: i64) {\n  var x = a < b\n}\n", "orders numeric types")
}

func TestEquality(t *testing.T) {
	checkOK(t, "fn t(a: i32): bool {\n  return a == 3\n}\n")
	checkOK(t, "struct P {\n  x: i32\n}\n\nfn t(a: P, b: P): bool {\n  return a == b\n}\n")
	checkOK(t, "fn t(a: (i32, bool), b: (i32, bool)): bool {\n  return a != b\n}\n")
	checkError(t, "fn t(a: i32[], b: i32[]) {\n  var x = a == b\n}\n", "slices do not compare")
	checkError(t, "fn t(a: i32, b: string) {\n  var x = a == b\n}\n", "cannot compare i32 with string")
	checkError(t, "fn t(a: i32) {\n  var x = a == null\n}\n", "can never be null")
}

func TestLogicalOperators(t *testing.T) {
	checkOK(t, "fn t(a: bool, b: bool): bool {\n  return a && !b || true\n}\n")
	checkError(t, "fn t(a: i32) {\n  if a && true {\n  }\n}\n", "a condition is a bool")
	checkError(t, "fn t(a: i32) {\n  var x = !a\n}\n", "operator ! needs a bool")
	checkError(t, "fn t(f: f64) {\n  var x = ~f\n}\n", "inverts integers")
	checkError(t, "fn t(s: string) {\n  var x = -s\n}\n", "negates numeric values")
}

func TestConditionsAreBool(t *testing.T) {
	checkError(t, "fn t() {\n  if 1 {\n  }\n}\n", "a condition is a bool")
	checkError(t, "fn t(n: i32) {\n  while n {\n  }\n}\n", "a condition is a bool")
}

// ---------------------------------------------------------------------------
// Nullability and narrowing
// ---------------------------------------------------------------------------

const nodePrelude = "struct Node {\n  value: i32\n  next: *Node?\n}\n\n"

func TestNullableMustBeChecked(t *testing.T) {
	checkError(t, nodePrelude+"fn t(n: *Node?) {\n  var v = n.value\n}\n", "may be null")
	checkError(t, nodePrelude+"fn t(n: *Node?) {\n  var v = *n\n}\n", "may be null")
	checkOK(t, nodePrelude+"fn t(n: *Node?) {\n  if n != null {\n    var v = n.value\n  }\n}\n")
}

func TestNullOnlyFitsOptional(t *testing.T) {
	checkOK(t, nodePrelude+"fn t(n: *Node) {\n  var p: *Node? = n\n  p = null\n}\n")
	checkError(t, "fn t() {\n  var x: i32 = null\n}\n", "cannot use null as i32")
	checkOK(t, "fn t() {\n  var x: i32? = 5\n}\n")
}

func TestNarrowingFollowsControlFlow(t *testing.T) {
	// An early exit leaves the survivor narrowed.
	checkOK(t, nodePrelude+"fn t(n: *Node?): i32 {\n  if n == null {\n    return 0\n  }\n  return n.value\n}\n")

	// The else branch knows the check failed.
	checkOK(t, nodePrelude+"fn t(n: *Node?): i32 {\n  if n == null {\n    var x = 1\n  } else {\n    return n.value\n  }\n  return 0\n}\n")

	// while narrows its body.
	checkOK(t, nodePrelude+"fn t(n: *Node?) {\n  while n != null {\n    n = n.next\n  }\n}\n")

	// && applies the left check to the right operand.
	checkOK(t, nodePrelude+"fn t(n: *Node?): bool {\n  return n != null && n.value > 0\n}\n")

	// ! flips the branches.
	checkOK(t, nodePrelude+"fn t(n: *Node?): i32 {\n  if !(n == null) {\n    return n.value\n  }\n  return 0\n}\n")
}

func TestAssignmentKillsNarrowing(t *testing.T) {
	src := nodePrelude +
		"fn find(): *Node? {\n  return null\n}\n\n" +
		"fn t(n: *Node?) {\n  if n != null {\n    n = find()\n    var v = n.value\n  }\n}\n"
	checkError(t, src, "may be null")
}

func TestNarrowingDoesNotLeakOutOfTheCheck(t *testing.T) {
	src := nodePrelude + "fn t(n: *Node?) {\n  if n != null {\n  }\n  var v = n.value\n}\n"
	checkError(t, src, "may be null")
}

// ---------------------------------------------------------------------------
// Functions, returns, calls
// ---------------------------------------------------------------------------

func TestCallArity(t *testing.T) {
	checkError(t, "fn f(a: i32) {\n}\n\nfn t() {\n  f()\n}\n", "takes 1 argument(s), got 0")
	checkError(t, "fn f(a: i32) {\n}\n\nfn t() {\n  f(1, 2)\n}\n", "takes 1 argument(s), got 2")
	checkOK(t, "fn f(a: i32, b: i32 = 4) {\n}\n\nfn t() {\n  f(1)\n  f(1, 2)\n}\n")
	checkError(t, "fn f(a: i32, b: i32 = 4) {\n}\n\nfn t() {\n  f()\n}\n", "takes 1 to 2 arguments, got 0")
}

func TestCallArgumentTypes(t *testing.T) {
	checkError(t, "fn f(a: i32) {\n}\n\nfn t() {\n  f(\"no\")\n}\n", "cannot use string as i32 in argument 1 to f")
	checkError(t, "fn f(a: i32) {\n}\n\nfn t() {\n  f(a = 1)\n}\n", "arguments are positional")
	checkError(t, "fn t(x: i32) {\n  x(1)\n}\n", "not callable")
}

func TestReturnChecks(t *testing.T) {
	checkOK(t, "fn f(): i32 {\n  return 3\n}\n")
	checkError(t, "fn f(): i32 {\n  return \"three\"\n}\n", "cannot use string as i32 in return")
	checkError(t, "fn f(): i32 {\n  return\n}\n", "returns i32; the return gives nothing")
	checkError(t, "fn f() {\n  return 3\n}\n", "returns nothing")
	checkOK(t, "fn f(): (i32, error?) {\n  return 0, null\n}\n")
	checkError(t, "fn f(): (i32, error?) {\n  return null, 0\n}\n", "cannot use null as i32 in return")
	checkError(t, "fn f(): i32 {\n  return 1, 2\n}\n", "the return gives 2 values")
}

func TestDefaultParameterValueIsChecked(t *testing.T) {
	checkError(t, "fn f(n: i32 = \"x\") {\n}\n", "cannot use string as i32")
}

// ---------------------------------------------------------------------------
// Methods
// ---------------------------------------------------------------------------

const stackPrelude = "struct Stack {\n  top: i32\n}\n\n" +
	"fn Stack::new(): Stack {\n  return Stack{ top: 0 }\n}\n\n" +
	"fn Stack::push(*self, v: i32) {\n  self.top += 1\n}\n\n" +
	"fn Stack::depth(self): i32 {\n  return self.top\n}\n\n"

func TestMethodCalls(t *testing.T) {
	checkOK(t, stackPrelude+"fn t() {\n  var s = Stack::new()\n  s.push(1)\n  var d: i32 = s.depth()\n}\n")
	checkError(t, stackPrelude+"fn t() {\n  var s = Stack::new()\n  s.push(\"one\")\n}\n", "cannot use string as i32")
	checkError(t, stackPrelude+"fn t() {\n  var s = Stack::new()\n  s.fly()\n}\n", "Stack has no method fly")
	checkError(t, stackPrelude+"fn t() {\n  var s = Stack::new()\n  s.new()\n}\n", "associated function")
	checkError(t, stackPrelude+"fn t() {\n  Stack::push(1)\n}\n", "takes a receiver")
	checkError(t, stackPrelude+"fn t() {\n  Stack::misc()\n}\n", "Stack has no associated function misc")
}

func TestMethodsResolveThroughPointers(t *testing.T) {
	checkOK(t, stackPrelude+"fn t(s: *Stack) {\n  s.push(1)\n  var v = s.top\n}\n")
}

func TestBuiltinMethods(t *testing.T) {
	checkOK(t, "fn t(ch: chan<i32>) {\n  ch.close()\n}\n")
	checkOK(t, "fn t(xs: i32[]): usize {\n  return xs.len()\n}\n")
	checkOK(t, "fn t(s: string): usize {\n  return s.len()\n}\n")
	checkOK(t, "fn t(s: string) {\n  for r in s.runes() {\n    var x: rune = r\n  }\n}\n")
	checkOK(t, "fn t(a: i32, b: i32): i32 {\n  return a.compare(b)\n}\n")
	checkOK(t, "fn t(e: error): string {\n  return e.message()\n}\n")
	checkError(t, "fn t(a: i32, b: i64) {\n  var x = a.compare(b)\n}\n", "cannot use i64 as i32")
}

func TestSelfType(t *testing.T) {
	checkOK(t, "struct S {\n  v: i32\n}\n\nfn S::get(self): i32 {\n  return self.v\n}\n")
	checkError(t, "struct S {\n  v: i32\n}\n\nfn S::get(self): string {\n  return self.v\n}\n", "cannot use i32 as string")
	checkError(t, "struct S {\n  v: i32\n}\n\nfn S::get(self): i32 {\n  return self.missing\n}\n", "no field or method missing")
}

func TestDuplicateMethod(t *testing.T) {
	checkError(t, "struct S {\n  v: i32\n}\n\nfn S::m(self) {\n}\n\nfn S::m(self) {\n}\n", "already has a method m")
}

// ---------------------------------------------------------------------------
// Struct literals
// ---------------------------------------------------------------------------

const pointPrelude = "struct Point {\n  x: i32\n  y: i32\n}\n\n"

func TestStructLiterals(t *testing.T) {
	checkOK(t, pointPrelude+"fn t(): Point {\n  return Point{ x: 1, y: 2 }\n}\n")
	checkOK(t, pointPrelude+"fn t(): Point {\n  return Point{ 1, 2 }\n}\n")
	checkError(t, pointPrelude+"fn t() {\n  var p = Point{ x: 1 }\n}\n", "misses the field y")
	checkError(t, pointPrelude+"fn t() {\n  var p = Point{ x: 1, y: 2, z: 3 }\n}\n", "no field z")
	checkError(t, pointPrelude+"fn t() {\n  var p = Point{ x: 1, x: 2, y: 3 }\n}\n", "set twice")
	checkError(t, pointPrelude+"fn t() {\n  var p = Point{ 1 }\n}\n", "has 2 fields, the literal provides 1")
	checkError(t, pointPrelude+"fn t() {\n  var p = Point{ x: \"one\", y: 2 }\n}\n", "cannot use string as i32")
}

func TestFieldAccess(t *testing.T) {
	checkOK(t, pointPrelude+"fn t(p: Point): i32 {\n  return p.x\n}\n")
	checkError(t, pointPrelude+"fn t(p: Point) {\n  var v = p.z\n}\n", "no field or method z")
	checkError(t, pointPrelude+"fn t(p: Point) {\n  var v: string = p.x\n}\n", "cannot use i32 as string")
}

func TestCrossModuleFieldVisibility(t *testing.T) {
	shared := "export struct Config {\n  export host: string\n  secret: i32\n}\n\n" +
		"export fn Config::new(): Config {\n  return Config{ host: \"h\", secret: 1 }\n}\n"

	// `string` is a view, so handing a field back borrows the struct.
	programOK(t, map[string]string{
		"main.gyro": "import { Config } from \"./lib\"\n\nfn t(c: Config): string !{ &c } {\n  return c.host\n}\n",
		"lib.gyro":  shared,
	}, "main.gyro")

	programError(t, map[string]string{
		"main.gyro": "import { Config } from \"./lib\"\n\nfn t(c: Config): i32 {\n  return c.secret\n}\n",
		"lib.gyro":  shared,
	}, "main.gyro", "private to its module")

	// A literal is only legal where every field is visible.
	programError(t, map[string]string{
		"main.gyro": "import { Config } from \"./lib\"\n\nfn t(): Config {\n  return Config{ host: \"h\", secret: 1 }\n}\n",
		"lib.gyro":  shared,
	}, "main.gyro", "construct it with an associated function")
}

// ---------------------------------------------------------------------------
// Generics: duck typing at instantiation
// ---------------------------------------------------------------------------

const maxPrelude = "fn max<T>(a: T, b: T): T {\n  if a.compare(b) > 0 {\n    return a\n  }\n  return b\n}\n\n"

func TestDuckTypedGenerics(t *testing.T) {
	// i32 has a built-in compare.
	checkOK(t, maxPrelude+"fn t(): i32 {\n  return max(3, 7)\n}\n")

	// A type with the method works.
	withCompare := "struct V {\n  n: i32\n}\n\nfn V::compare(self, other: V): i32 {\n  return 0\n}\n\n"
	checkOK(t, maxPrelude+withCompare+"fn t(a: V, b: V): V {\n  return max(a, b)\n}\n")

	// A type without it fails at the instantiation, naming the call site.
	withoutCompare := "struct M {\n  n: i32\n}\n\n"
	checkError(t, maxPrelude+withoutCompare+"fn t(a: M, b: M) {\n  var x = max(a, b)\n}\n", "M has no method compare")
	checkError(t, maxPrelude+withoutCompare+"fn t(a: M, b: M) {\n  var x = max(a, b)\n}\n", "in the instantiation max<M>")
}

func TestGenericInference(t *testing.T) {
	checkOK(t, "fn first<T>(a: T, b: T): T {\n  return a\n}\n\nfn t(): i32 {\n  return first(1, 2)\n}\n")
	checkError(t, "fn first<T>(a: T, b: T): T {\n  return a\n}\n\nfn t() {\n  var x = first(1, \"two\")\n}\n", "cannot use string as i32")
	checkError(t, "fn none<T>(): T? {\n  return null\n}\n\nfn t() {\n  var x = none()\n}\n", "cannot infer T")
}

func TestGenericExplicitArguments(t *testing.T) {
	checkOK(t, "fn zero<T>(v: T): T {\n  return v\n}\n\nfn t(): i64 {\n  return zero<i64>(3)\n}\n")
	checkError(t, "fn zero<T>(v: T): T {\n  return v\n}\n\nfn t() {\n  var x = zero<i32>(\"s\")\n}\n", "cannot use string as i32")
	checkError(t, "fn zero<T>(v: T): T {\n  return v\n}\n\nfn t() {\n  var x = zero<i32, i32>(1)\n}\n", "takes 1 type argument(s), got 2")
	checkError(t, "fn f(v: i32) {\n}\n\nfn t() {\n  f<i32>(1)\n}\n", "takes 0 type argument(s), got 1")
}

func TestGenericFunctionsAreCalledNotReferenced(t *testing.T) {
	checkError(t, "fn id<T>(v: T): T {\n  return v\n}\n\nfn t() {\n  var f = id\n}\n", "must be called")
}

const pairPrelude = "struct Pair<T> {\n  first: T\n  second: T\n}\n\n" +
	"fn Pair<T>::swap(*self) {\n  var tmp = self.first\n  self.first = self.second\n  self.second = tmp\n}\n\n"

func TestGenericStructs(t *testing.T) {
	checkOK(t, pairPrelude+"fn t() {\n  var p = Pair<i32>{ 1, 2 }\n  p.swap()\n  var x: i32 = p.first\n}\n")
	checkError(t, pairPrelude+"fn t() {\n  var p = Pair<i32>{ 1, \"two\" }\n}\n", "cannot use string as i32")
	checkError(t, pairPrelude+"fn t() {\n  var p = Pair<i32>{ 1, 2 }\n  var x: string = p.first\n}\n", "cannot use i32 as string")
	checkError(t, pairPrelude+"fn t(p: Pair<i32, bool>) {\n}\n", "takes 1 type argument(s), got 2")
}

func TestGenericStructLiteralInference(t *testing.T) {
	checkOK(t, pairPrelude+"fn t(): i32 {\n  var p = Pair{ 1, 2 }\n  return p.first\n}\n")
	checkOK(t, pairPrelude+"fn t(s: string) {\n  var p = Pair{ first: s, second: s }\n}\n")
}

func TestGenericOwnerInferenceThroughAssociatedFunction(t *testing.T) {
	box := "struct Box<T> {\n  value: T\n}\n\n" +
		"fn Box<T>::new(value: T): Box<T> {\n  return Box{ value: value }\n}\n\n" +
		"fn Box<T>::get(self): T {\n  return self.value\n}\n\n"
	checkOK(t, box+"fn t(): i32 {\n  var b = Box::new(1)\n  return b.get()\n}\n")
	checkError(t, box+"fn t(): string {\n  var b = Box::new(1)\n  return b.get()\n}\n", "cannot use i32 as string")
}

func TestGenericMethodDuckCheck(t *testing.T) {
	src := "struct Holder<T> {\n  value: T\n}\n\n" +
		"fn Holder<T>::shout(self) {\n  self.value.speak()\n}\n\n" +
		"struct Silent {\n  n: i32\n}\n\n" +
		"fn t(h: Holder<Silent>) {\n  h.shout()\n}\n"
	checkError(t, src, "Silent has no method speak")
}

func TestRecursiveGenericConverges(t *testing.T) {
	checkOK(t, "fn count<T>(v: T, n: i32): i32 {\n  if n <= 0 {\n    return 0\n  }\n  return count(v, n - 1) + 1\n}\n\nfn t(): i32 {\n  return count(\"x\", 3)\n}\n")
}

func TestSelfReferentialGenericStruct(t *testing.T) {
	src := "struct Tree<T> {\n  value: T\n  left: *Tree<T>?\n}\n\n" +
		"fn t(root: Tree<i32>): i32 {\n  if root.left != null {\n  }\n  return root.value\n}\n"
	checkOK(t, src)
}

// ---------------------------------------------------------------------------
// Tuples
// ---------------------------------------------------------------------------

func TestTupleAccess(t *testing.T) {
	checkOK(t, "fn t() {\n  var pair = (200, \"ok\")\n  var code: i32 = pair.0\n  var text: string = pair.1\n}\n")
	checkError(t, "fn t() {\n  var pair = (200, \"ok\")\n  var x = pair.2\n}\n", "no element 2")
	checkError(t, "fn t(x: i32) {\n  var v = x.0\n}\n", "needs a tuple")
}

func TestTupleDestructuring(t *testing.T) {
	checkOK(t, "fn f(): (i32, error?) {\n  return 0, null\n}\n\nfn t() {\n  var value, err = f()\n  var v: i32 = value\n}\n")
	checkError(t, "fn f(): (i32, bool) {\n  return 0, true\n}\n\nfn t() {\n  var a, b, c = f()\n}\n", "has 2 elements, the declaration binds 3")
	checkError(t, "fn t(x: i32) {\n  var a, b = x\n}\n", "needs a tuple")
}

func TestTupleAssignability(t *testing.T) {
	checkOK(t, "fn t(ch: chan<(string, error?)>) {\n  ch <- (\"body\", null)\n}\n")
	checkError(t, "fn t(ch: chan<(string, error?)>) {\n  ch <- (1, null)\n}\n", "cannot use")
}

// ---------------------------------------------------------------------------
// Borrows
// ---------------------------------------------------------------------------

// A slice never owns, so a function handing one back states where the view
// came from.
func TestReturnedViewNeedsItsSourceNamed(t *testing.T) {
	checkOK(t, "fn first(xs: i32[]): i32[] !{ &xs } {\n  return xs\n}\n")
	checkError(t, "fn first(xs: i32[]): i32[] {\n  return xs\n}\n",
		"this returns a view into xs, which the row does not name")
	checkError(t, "fn pick(a: i32[], b: i32[]): i32[] !{ &a } {\n  return b\n}\n",
		"this returns a view into b, which the row does not name")
	checkOK(t, "fn spans(a: i32[], b: i32[], f: bool): i32[] !{ &a, &b } {\n  if f {\n    return a\n  }\n  return b\n}\n")
}

// An array literal is storage in the enclosing frame, and the caller
// outlives the frame.
func TestViewOfLocalStorageCannotEscape(t *testing.T) {
	checkError(t, "fn t(): i32[] {\n  var xs = [1, 2, 3]\n  return xs\n}\n",
		"view into storage that ends with this function")
	checkError(t, "fn t(): i32[] {\n  return [1, 2, 3]\n}\n",
		"view into storage that ends with this function")
}

// The row's own entries are checked: the name is a parameter, that
// parameter is a view, and the result is one too.
func TestBorrowRowNamesAViewParameter(t *testing.T) {
	checkError(t, "fn t(xs: i32[]): i32[] !{ &nope } {\n  return xs\n}\n",
		"&nope names no parameter of this function")
	checkError(t, "fn t(n: i32, xs: i32[]): i32[] !{ &n } {\n  return xs\n}\n",
		"&n borrows from i32, which is not a view")
	checkError(t, "fn t(xs: i32[]): usize !{ &xs } {\n  return xs.len()\n}\n",
		"this row borrows, but the result is not a view")
}

// A result that is not a view needs no row, and a string literal is not
// frame storage.
func TestNonViewResultsAreNotBorrowChecked(t *testing.T) {
	checkOK(t, "fn t(xs: i32[]): usize {\n  return xs.len()\n}\n")
	checkOK(t, "fn t(): string {\n  return \"a literal is static\"\n}\n")
}

// A generic row is conditional: load-bearing where T is a view, vacuous
// where it is not, and stated once for every instantiation.
const genericPick = "fn pick<T>(a: T, b: T, f: bool): T !{ &a, &b } {\n  if f {\n    return a\n  }\n  return b\n}\n\n"

func TestGenericBorrowsAreConditional(t *testing.T) {
	// Vacuous at i32: the row says nothing and costs nothing.
	checkOK(t, genericPick+"fn t(): i32 {\n  return pick(1, 2, true)\n}\n")
	// Load-bearing at string, which is a view.
	checkOK(t, genericPick+"fn t(x: string, y: string): string !{ &x, &y } {\n  return pick(x, y, true)\n}\n")
	checkError(t, genericPick+"fn t(x: string, y: string): string {\n  return pick(x, y, true)\n}\n",
		"this returns a view into x, which the row does not name")
	// A generic body returning a parameter still needs its row, and the
	// instantiation at a view type is what surfaces it.
	checkError(t, "fn identity<T>(x: T): T {\n  return x\n}\n\nfn t(s: string): string !{ &s } {\n  return identity(s)\n}\n",
		"this returns a view into x, which the row does not name")
	checkOK(t, "fn identity<T>(x: T): T !{ &x } {\n  return x\n}\n\nfn t(s: string): string !{ &s } {\n  return identity(s)\n}\n")
}

// A callee's row answers what its result views, which is what carries the
// fact across a call rather than stopping at each definition.
const pickPrelude = "fn pick(a: i32[], b: i32[]): i32[] !{ &a } {\n  return a\n}\n\n" +
	"fn both(a: i32[], b: i32[]): i32[] !{ &a, &b } {\n  return a\n}\n\n"

func TestCallResultCarriesTheCalleesBorrows(t *testing.T) {
	checkOK(t, pickPrelude+"fn t(xs: i32[], ys: i32[]): i32[] !{ &xs } {\n  return pick(xs, ys)\n}\n")
	checkError(t, pickPrelude+"fn t(xs: i32[], ys: i32[]): i32[] {\n  return pick(xs, ys)\n}\n",
		"this returns a view into xs, which the row does not name")
	// The caller must account for every source the callee names.
	checkOK(t, pickPrelude+"fn t(xs: i32[], ys: i32[]): i32[] !{ &xs, &ys } {\n  return both(xs, ys)\n}\n")
	checkError(t, pickPrelude+"fn t(xs: i32[], ys: i32[]): i32[] !{ &xs } {\n  return both(xs, ys)\n}\n",
		"this returns a view into ys, which the row does not name")
}

// Frame storage travels through a call: passing a local where the callee
// borrows means the result views this frame.
func TestLocalStorageTravelsThroughACall(t *testing.T) {
	checkError(t, pickPrelude+"fn t(ys: i32[]): i32[] !{ &ys } {\n  var local = [1, 2, 3]\n  return pick(local, ys)\n}\n",
		"view into storage that ends with this function")
}

// A local bound to a call keeps what the call viewed.
func TestBorrowSurvivesALocalBinding(t *testing.T) {
	checkOK(t, pickPrelude+"fn t(xs: i32[], ys: i32[]): i32[] !{ &xs } {\n  var v = pick(xs, ys)\n  return v\n}\n")
	checkError(t, pickPrelude+"fn t(xs: i32[], ys: i32[]): i32[] {\n  var v = pick(xs, ys)\n  return v\n}\n",
		"this returns a view into xs, which the row does not name")
}

// `&self` names a method's receiver, and a field chain roots at whatever
// holds it, so `self.items.slice()` is a view of self.
func TestMethodsBorrowTheirReceiver(t *testing.T) {
	stack := "struct Stack {\n  items: i32[]\n}\n\n"

	checkOK(t, stack+"fn Stack::view(self): i32[] !{ &self } {\n  return self.items\n}\n")
	checkError(t, stack+"fn Stack::view(self): i32[] {\n  return self.items\n}\n",
		"this returns a view into self, which the row does not name")
	// The same rooting works for a named parameter holding storage.
	checkOK(t, stack+"fn contents(s: Stack): i32[] !{ &s } {\n  return s.items\n}\n")
	checkError(t, stack+"fn contents(s: Stack): i32[] {\n  return s.items\n}\n",
		"this returns a view into s, which the row does not name")
	// &self on a plain function has no receiver to name.
	checkError(t, stack+"fn loose(s: Stack): i32[] !{ &self } {\n  return s.items\n}\n",
		"&self names no parameter of this function")
}

// A struct carries what its fields carry: frame storage behind a field
// still ends with the frame, while a heap buffer does not.
func TestStructsCarryTheirFieldsStorage(t *testing.T) {
	view := "struct View {\n  data: i32[]\n}\n\n"
	checkError(t, view+"fn t(): i32[] {\n  var xs = [1, 2, 3]\n  var v = View{ data: xs }\n  return v.data\n}\n",
		"view into storage that ends with this function")
	checkOK(t, view+"fn t(xs: i32[]): i32[] !{ &xs } {\n  var v = View{ data: xs }\n  return v.data\n}\n")
}

// The row travels with the declaration, so an imported function's borrows
// reach its caller.
func TestBorrowsCrossModuleBoundaries(t *testing.T) {
	files := map[string]string{
		"util.gyro": "export fn pick(a: i32[], b: i32[]): i32[] !{ &a } {\n  return a\n}\n",
		"main.gyro": "import { pick } from \"./util\"\n\nfn t(xs: i32[], ys: i32[]): i32[] {\n  return pick(xs, ys)\n}\n",
	}
	programError(t, files, "main.gyro", "this returns a view into xs, which the row does not name")

	files["main.gyro"] = "import { pick } from \"./util\"\n\nfn t(xs: i32[], ys: i32[]): i32[] !{ &xs } {\n  return pick(xs, ys)\n}\n"
	programOK(t, files, "main.gyro")
}

// ---------------------------------------------------------------------------
// Spawn races
// ---------------------------------------------------------------------------

const racePrelude = "import { println, spawn } from \"stdlib\"\n" +
	"import { alloc, free } from \"mem\"\n\n" +
	"struct Node {\n  value: i32\n}\n\n"

func raceSource(body string) map[string]string {
	return map[string]string{"main.gyro": racePrelude + body}
}

// A capture copies, and copying a reference shares what it points at.
func TestSpawnedClosureSharingAReferenceWarns(t *testing.T) {
	programWarning(t, raceSource("fn t(node: *Node) {\n  spawn(fn() {\n    node.value = 1\n  })\n}\n"),
		"main.gyro", "captures node, so both threads reach the same memory")
	programWarning(t, raceSource("fn t(xs: i32[]) {\n  spawn(fn() {\n    println(xs.len())\n  })\n}\n"),
		"main.gyro", "captures xs, so both threads reach the same elements")
	// A nullable pointer is still a pointer.
	programWarning(t, raceSource("fn t(node: *Node?) {\n  spawn(fn() {\n    if node != null {\n      println(node.value)\n    }\n  })\n}\n"),
		"main.gyro", "captures node, so both threads reach the same memory")
}

// Copying a value shares nothing, a channel is the sanctioned way across, and
// a move hands the binding over rather than sharing it.
func TestSpawnedClosureWithoutSharingIsQuiet(t *testing.T) {
	programOK(t, raceSource("fn t(n: i32) {\n  spawn(fn() {\n    println(n)\n  })\n}\n"), "main.gyro")
	programOK(t, raceSource("fn t(ch: chan<i32>) {\n  spawn(fn() {\n    ch <- 1\n  })\n}\n"), "main.gyro")
	programOK(t, raceSource("fn t() {\n  var node = alloc<Node>()\n  spawn(fn[move node]() {\n    free(node)\n  })\n}\n"), "main.gyro")
	programOK(t, raceSource("fn t() {\n  spawn(fn() {\n    println(\"hi\")\n  })\n}\n"), "main.gyro")
}

// Only a spawned closure is reported: an ordinary one runs on this thread.
func TestUnspawnedClosureIsNotARace(t *testing.T) {
	programOK(t, raceSource("fn t(xs: i32[]): fn() {\n  return fn() {\n    println(xs.len())\n  }\n}\n"), "main.gyro")
}

// The pointer reaches the spawned closure through an intermediate one, so
// both of them capture it.
func TestCaptureIsSeenThroughNestedClosures(t *testing.T) {
	programWarning(t, raceSource("fn t(node: *Node) {\n  var outer = fn() {\n    spawn(fn() {\n      node.value = 1\n    })\n  }\n  outer()\n}\n"),
		"main.gyro", "captures node")
}

// ---------------------------------------------------------------------------
// Leak analysis
// ---------------------------------------------------------------------------

// The leak analysis needs the real `mem` module behind the import, so these
// go through the loader rather than checkOK.
const leakPrelude = "import { alloc, free } from \"mem\"\n\n" +
	"struct Node {\n  value: i32\n}\n\n" +
	"struct Holder {\n  node: *Node\n}\n\n"

func leakSource(body string) map[string]string {
	return map[string]string{"main.gyro": leakPrelude + body}
}

func TestAllocationReachingBlockEndWarns(t *testing.T) {
	programWarning(t, leakSource("fn t() {\n  var node = alloc<Node>()\n}\n"),
		"main.gyro", "the allocation in node reaches the end of its block unfreed")
}

// free settles it, and so does every way of handing it on.
func TestSettledAllocationsAreQuiet(t *testing.T) {
	programOK(t, leakSource("fn t() {\n  var node = alloc<Node>()\n  defer free(node)\n}\n"), "main.gyro")
	programOK(t, leakSource("fn t() {\n  var node = alloc<Node>()\n  free(node)\n}\n"), "main.gyro")
	programOK(t, leakSource("fn t(): *Node {\n  var node = alloc<Node>()\n  return node\n}\n"), "main.gyro")
	programOK(t, leakSource("fn t(): Holder {\n  var node = alloc<Node>()\n  return Holder{ node: node }\n}\n"), "main.gyro")
	programOK(t, leakSource("fn t(ch: chan<*Node>) {\n  var node = alloc<Node>()\n  ch <- node\n}\n"), "main.gyro")
	programOK(t, leakSource("fn t(sink: fn(*Node)) {\n  var node = alloc<Node>()\n  sink(node)\n}\n"), "main.gyro")
}

// A move capture is the case the analysis cannot see any other way: the
// closure takes the binding with it.
func TestMoveCaptureSettlesAnAllocation(t *testing.T) {
	programOK(t, leakSource("fn t() {\n  var node = alloc<Node>()\n  var release = fn[move node]() {\n    free(node)\n  }\n}\n"), "main.gyro")
}

// Falling out of the bottom is not the only way to reach a block's end. A
// return that does not carry the allocation loses it right there.
func TestReturnIsAnExitTheLeakCheckSees(t *testing.T) {
	programWarning(t, leakSource("fn t(): i32 {\n  var node = alloc<Node>()\n  return 0\n}\n"),
		"main.gyro", "reaches the end of its block unfreed")
	programWarning(t, leakSource("fn t(c: bool) {\n  var node = alloc<Node>()\n  if c {\n    return\n  }\n  free(node)\n}\n"),
		"main.gyro", "reaches the end of its block unfreed")
}

// A return inside a closure leaves the closure, so it says nothing about the
// allocations of the function the closure was written in.
func TestClosureReturnDoesNotBlameItsEnclosingFunction(t *testing.T) {
	programOK(t, leakSource("fn t() {\n  var node = alloc<Node>()\n  defer free(node)\n  var f = fn(): i32 {\n    return 1\n  }\n}\n"), "main.gyro")
}

// Freeing on one path is not freeing.
func TestFreeMustReachEveryPath(t *testing.T) {
	programWarning(t, leakSource("fn t(c: bool) {\n  var node = alloc<Node>()\n  if c {\n    free(node)\n  }\n}\n"),
		"main.gyro", "reaches the end of its block unfreed")
	programOK(t, leakSource("fn t(c: bool) {\n  var node = alloc<Node>()\n  if c {\n    free(node)\n  } else {\n    free(node)\n  }\n}\n"), "main.gyro")
}

// ---------------------------------------------------------------------------
// Definite assignment
// ---------------------------------------------------------------------------

func TestReadBeforeAssignment(t *testing.T) {
	checkError(t, "fn t(): i32 {\n  var x: i32\n  return x\n}\n", "x is read before it is assigned")
	checkOK(t, "fn t(): i32 {\n  var x: i32\n  x = 1\n  return x\n}\n")
	// A compound assignment reads its target first.
	checkError(t, "fn t(): i32 {\n  var x: i32\n  x += 1\n  return x\n}\n", "x is read before it is assigned")
}

// A name counts as assigned below a branch only when every path wrote it.
func TestAssignmentMustReachEveryPath(t *testing.T) {
	checkError(t, "fn t(c: bool): i32 {\n  var x: i32\n  if c {\n    x = 1\n  }\n  return x\n}\n", "read before it is assigned")
	checkOK(t, "fn t(c: bool): i32 {\n  var x: i32\n  if c {\n    x = 1\n  } else {\n    x = 2\n  }\n  return x\n}\n")
	// A branch that cannot fall through leaves the other one's write standing.
	checkOK(t, "fn t(c: bool): i32 {\n  var x: i32\n  if c {\n    return 0\n  }\n  x = 5\n  return x\n}\n")
}

// A loop body may not run, so nothing it writes survives it — unless the
// loop cannot fall out of its condition, where `break` is the only exit.
func TestLoopsDoNotAssignOnTheirOwn(t *testing.T) {
	checkError(t, "fn t(c: bool): i32 {\n  var x: i32\n  while c {\n    x = 1\n  }\n  return x\n}\n", "read before it is assigned")
	checkError(t, "fn t(xs: i32[]): i32 {\n  var x: i32\n  for v in xs {\n    x = v\n  }\n  return x\n}\n", "read before it is assigned")
	checkOK(t, "fn t(): i32 {\n  var x: i32\n  while true {\n    x = 1\n    break\n  }\n  return x\n}\n")
}

// One arm of a match or select runs, so what survives is what all of them
// wrote.
func TestArmsMergeTheirAssignments(t *testing.T) {
	checkOK(t, shapePrelude+"fn t(s: Shape): i32 {\n  var x: i32\n  match s {\n    Shape::Circle(r): { x = 1 }\n    Shape::Rect(w, h): { x = 2 }\n    Shape::Point: { x = 3 }\n  }\n  return x\n}\n")
	checkError(t, shapePrelude+"fn t(s: Shape): i32 {\n  var x: i32\n  match s {\n    Shape::Circle(r): { x = 1 }\n    Shape::Rect(w, h): { x = 2 }\n    Shape::Point: { }\n  }\n  return x\n}\n", "read before it is assigned")
}

// Taking an address is a write: the storage is real and sized, and what
// happens through the pointer cannot be followed here.
func TestAddressOfCountsAsAssignment(t *testing.T) {
	checkOK(t, "fn t(): i32 {\n  var x: i32\n  var p = &x\n  *p = 7\n  return x\n}\n")
}

// A closure runs at an unknown time: it may read only what is already
// assigned, and what it writes does not count outside.
func TestClosuresNeitherReadNorWriteAcrossTheBoundary(t *testing.T) {
	checkError(t, "fn t() {\n  var x: i32\n  var f = fn(): i32 x\n}\n", "read before it is assigned")
	checkError(t, "fn t(): i32 {\n  var x: i32\n  var f = fn() {\n    x = 1\n  }\n  return x\n}\n", "read before it is assigned")
}

// The value of a two-name receive is only there when the flag is true, and
// the check follows ! the same way narrowing does.
func TestReceiveValueNeedsItsFlagChecked(t *testing.T) {
	checkError(t, "fn t(ch: chan<i32>): i32 {\n  var v, open = <-ch\n  return v\n}\n", "v holds a value only where open is true")
	checkOK(t, "fn t(ch: chan<i32>): i32 {\n  var v, open = <-ch\n  if open {\n    return v\n  }\n  return 0\n}\n")
	checkOK(t, "fn t(ch: chan<i32>): i32 {\n  var v, open = <-ch\n  if !open {\n    return 0\n  }\n  return v\n}\n")
}

// ---------------------------------------------------------------------------
// Enums and match
// ---------------------------------------------------------------------------

const shapePrelude = "enum Shape {\n  Circle(f64),\n  Rect(f64, f64),\n  Point,\n}\n\n"

func TestVariantConstruction(t *testing.T) {
	checkOK(t, shapePrelude+"fn t(): Shape {\n  return Shape::Circle(2.0)\n}\n")
	checkOK(t, shapePrelude+"fn t(): Shape {\n  return Shape::Point\n}\n")
	checkError(t, shapePrelude+"fn t(): Shape {\n  return Shape::Circle(2.0, 3.0)\n}\n", "carries 1 value(s), got 2")
	checkError(t, shapePrelude+"fn t(): Shape {\n  return Shape::Circle(\"big\")\n}\n", "cannot use string as f64")
	checkError(t, shapePrelude+"fn t(): Shape {\n  return Shape::Circle\n}\n", "carries a payload")
	checkError(t, shapePrelude+"fn t(): Shape {\n  return Shape::Point()\n}\n", "carries no payload")
}

func TestMatchTyping(t *testing.T) {
	checkOK(t, shapePrelude+"fn area(s: Shape): f64 {\n  return match s {\n    Shape::Circle(r): 3.14 * r * r\n    Shape::Rect(w, h): w * h\n    Shape::Point: 0.0\n  }\n}\n")
	checkError(t, shapePrelude+"fn t(s: Shape) {\n  var x = match s {\n    Shape::Circle(r): r\n    Shape::Point: \"zero\"\n    _: 0.0\n  }\n}\n", "match arms disagree")
	checkError(t, shapePrelude+"fn t(s: Shape) {\n  var x = match s {\n    Shape::Circle(r): r > 0\n    _: false\n  }\n  var y: string = x\n}\n", "cannot use bool as string")
}

func TestMatchPatternTypes(t *testing.T) {
	checkOK(t, "fn t(n: i32): string {\n  return match n {\n    0: \"zero\"\n    _: \"more\"\n  }\n}\n")
	checkError(t, "fn t(n: i32) {\n  var x = match n {\n    \"zero\": 1\n    _: 0\n  }\n}\n", "can never match i32")
	checkError(t, shapePrelude+"fn t(n: i32) {\n  var x = match n {\n    Shape::Point: 1\n    _: 0\n  }\n}\n", "can never match i32")
	checkError(t, shapePrelude+"fn t(s: Shape) {\n  var x = match s {\n    Shape::Circle(r, extra): r\n    _: 0.0\n  }\n}\n", "carries 1 value(s), the pattern binds 2")
}

func TestMatchBindsPayloadTypes(t *testing.T) {
	checkError(t, shapePrelude+"fn t(s: Shape) {\n  var x = match s {\n    Shape::Circle(r): r && true\n    _: false\n  }\n}\n", "a condition is a bool")
}

func TestMatchOverAnEnumIsExhaustive(t *testing.T) {
	checkOK(t, shapePrelude+"fn t(s: Shape): f64 {\n  return match s {\n    Shape::Circle(r): r\n    Shape::Rect(w, h): w\n    Shape::Point: 0.0\n  }\n}\n")
	checkError(t, shapePrelude+"fn t(s: Shape): f64 {\n  return match s {\n    Shape::Circle(r): r\n  }\n}\n", "leaves Shape::Rect, Shape::Point uncovered")
	checkError(t, shapePrelude+"fn t(s: Shape): f64 {\n  return match s {\n    Shape::Circle(r): r\n    Shape::Rect(w, h): w\n  }\n}\n", "leaves Shape::Point uncovered")
}

// `_` and a plain binding both match everything left over, so either one
// closes a match on its own.
func TestIrrefutableArmClosesAMatch(t *testing.T) {
	checkOK(t, shapePrelude+"fn t(s: Shape): f64 {\n  return match s {\n    Shape::Point: 0.0\n    _: 1.0\n  }\n}\n")
	checkOK(t, shapePrelude+"fn t(s: Shape): f64 {\n  return match s {\n    other: 1.0\n  }\n}\n")
}

// A variant only counts as covered when its payload patterns bind rather
// than test: Circle(0.0) still leaves every other radius unmatched.
func TestRefutablePayloadDoesNotCoverAVariant(t *testing.T) {
	checkError(t, shapePrelude+"fn t(s: Shape): f64 {\n  return match s {\n    Shape::Circle(0.0): 1.0\n    Shape::Rect(w, h): w\n    Shape::Point: 0.0\n  }\n}\n", "leaves Shape::Circle uncovered")
	checkOK(t, shapePrelude+"fn t(s: Shape): f64 {\n  return match s {\n    Shape::Circle(0.0): 1.0\n    Shape::Circle(r): r\n    Shape::Rect(w, h): w\n    Shape::Point: 0.0\n  }\n}\n")
}

// Only enums are enumerable. A match on an open type — an integer, or an
// `error` whose concrete type is unknown — is never reported as partial.
func TestExhaustivenessOnlyAppliesToEnums(t *testing.T) {
	checkOK(t, "fn t(n: i32): string {\n  return match n {\n    0: \"zero\"\n  }\n}\n")
	checkOK(t, "struct RangeError {\n  value: i32\n}\n\nfn RangeError::message(self): string {\n  return \"bad\"\n}\n\nfn t(err: error): i32 {\n  return match err {\n    RangeError(e): e.value\n  }\n}\n")
}

func TestTypeTestPattern(t *testing.T) {
	rangeError := "struct RangeError {\n  value: i32\n}\n\n" +
		"fn RangeError::message(self): string {\n  return \"out of range\"\n}\n\n"

	checkOK(t, rangeError+"fn t(err: error): i32 {\n  return match err {\n    RangeError(e): e.value\n    _: 0\n  }\n}\n")
	checkError(t, rangeError+"fn t(n: i32) {\n  var x = match n {\n    RangeError(e): 1\n    _: 0\n  }\n}\n", "needs an error subject")

	noMessage := "struct Quiet {\n  value: i32\n}\n\n"
	checkError(t, noMessage+"fn t(err: error): i32 {\n  return match err {\n    Quiet(e): e.value\n    _: 0\n  }\n}\n", "no message(): string method")
}

// ---------------------------------------------------------------------------
// Errors as values
// ---------------------------------------------------------------------------

func TestStructuralErrors(t *testing.T) {
	withMessage := "struct E {\n  code: i32\n}\n\nfn E::message(self): string {\n  return \"e\"\n}\n\n"
	checkOK(t, withMessage+"fn f(): (i32, error?) {\n  return 0, E{ code: 1 }\n}\n")

	without := "struct Q {\n  code: i32\n}\n\n"
	checkError(t, without+"fn f(): (i32, error?) {\n  return 0, Q{ code: 1 }\n}\n", "cannot use Q as error?")
}

func TestErrorsMustBeHandled(t *testing.T) {
	fallible := "fn f(): (i32, error?) {\n  return 0, null\n}\n\n"
	checkError(t, fallible+"fn t() {\n  f()\n}\n", "the error result is dropped here")
	checkOK(t, fallible+"fn t() {\n  var _, _ = f()\n}\n")
	checkOK(t, fallible+"fn t() {\n  var v, err = f()\n  if err != null {\n    var m = err.message()\n  }\n}\n")
}

// ---------------------------------------------------------------------------
// Async
// ---------------------------------------------------------------------------

const asyncPrelude = "async fn fetch(url: string): (string, error?) {\n  return \"\", null\n}\n\n"

func TestAsyncCalls(t *testing.T) {
	checkOK(t, asyncPrelude+"async fn t() {\n  var body, err = await fetch(\"u\")\n  var s: string = body\n}\n")
	checkError(t, asyncPrelude+"fn t() {\n  var x = await fetch(\"u\")\n}\n", "await only works inside an async fn")
	checkError(t, asyncPrelude+"async fn t() {\n  fetch(\"u\")\n}\n", "does nothing until it is awaited")
	checkError(t, asyncPrelude+"async fn t() {\n  var body, err = fetch(\"u\")\n}\n", "needs a tuple, not awaitable<(string, error?)>")
	checkError(t, "fn f(): i32 {\n  return 1\n}\n\nasync fn t() {\n  var x = await f()\n}\n", "await needs a call to an async fn")
}

func TestAsyncClosures(t *testing.T) {
	checkOK(t, "fn t() {\n  var later = async fn(): i32 { return 1 }\n}\n")
	checkOK(t, "async fn t(): i32 {\n  var later = async fn(): i32 { return 1 }\n  return await later()\n}\n")
}

// ---------------------------------------------------------------------------
// Channels and select
// ---------------------------------------------------------------------------

func TestChannelOperations(t *testing.T) {
	checkOK(t, "fn t() {\n  var ch = chan<i32>(64)\n  ch <- 1\n  var v: i32 = <-ch\n  var w, open = <-ch\n  var b: bool = open\n}\n")
	checkError(t, "fn t(ch: chan<i32>) {\n  ch <- \"one\"\n}\n", "cannot use string as i32 in the send")
	checkError(t, "fn t(x: i32) {\n  x <- 1\n}\n", "send needs a channel")
	checkError(t, "fn t(x: i32) {\n  var v = <-x\n}\n", "receive needs a channel")
	checkError(t, "fn t() {\n  var ch = chan<i32>(\"big\")\n}\n", "capacity is an integer")
}

func TestTupleChannelReceive(t *testing.T) {
	// A two-name binding on a receive means (element, open); the tuple
	// element comes out whole.
	// The value is only there when the channel was open, so the read is
	// guarded; definite assignment is what demands the check.
	src := "fn t(results: chan<(string, error?)>) {\n" +
		"  var result, open = <-results\n" +
		"  if open {\n" +
		"    var body: string = result.0\n" +
		"  }\n" +
		"  var flag: bool = open\n}\n"
	checkOK(t, src)
}

func TestSelectArms(t *testing.T) {
	src := "fn t(values: chan<i32>, quit: chan<bool>) {\n" +
		"  select {\n" +
		"    var v = <-values: v.compare(0)\n" +
		"    quit <- true: return\n" +
		"    else: return\n" +
		"  }\n}\n"
	checkOK(t, src)

	bad := "fn t(values: chan<i32>) {\n" +
		"  select {\n" +
		"    var v = <-values: {\n      var s: string = v\n    }\n" +
		"  }\n}\n"
	checkError(t, bad, "cannot use i32 as string")
}

// ---------------------------------------------------------------------------
// Closures
// ---------------------------------------------------------------------------

func TestClosureTypes(t *testing.T) {
	checkOK(t, "fn t() {\n  var double = fn(a: i32): i32 { return a * 2 }\n  var x: i32 = double(21)\n}\n")
	checkError(t, "fn t() {\n  var double = fn(a: i32): i32 { return a * 2 }\n  double(\"x\")\n}\n", "cannot use string as i32")
	checkError(t, "fn t() {\n  var f = fn(a: i32): i32 { return \"no\" }\n}\n", "cannot use string as i32 in return")
}

func TestClosureInferenceFromContext(t *testing.T) {
	apply := "fn apply(v: i32, f: fn(i32): i32): i32 {\n  return f(v)\n}\n\n"
	checkOK(t, apply+"fn t(): i32 {\n  return apply(3, fn(x) x * 2)\n}\n")
	checkError(t, apply+"fn t(): i32 {\n  return apply(3, fn(x) x && true)\n}\n", "a condition is a bool")
	checkError(t, "fn t() {\n  var f = fn(x) x\n}\n", "needs a type")
	checkError(t, "fn apply(f: fn(i32): i32) {\n}\n\nfn t() {\n  apply(fn(a, b) a)\n}\n", "takes 2 parameter(s)")
}

func TestClosureExpressionBody(t *testing.T) {
	checkOK(t, "fn run(f: fn()) {\n}\n\nfn produce(n: i32) {\n}\n\nfn t() {\n  run(fn() produce(10))\n}\n")
}

// ---------------------------------------------------------------------------
// Iteration
// ---------------------------------------------------------------------------

func TestIterationSources(t *testing.T) {
	checkOK(t, "fn t(xs: i32[]) {\n  for x in xs {\n    var v: i32 = x\n  }\n}\n")
	checkOK(t, "fn t(xs: i32[4]) {\n  for x in xs {\n    var v: i32 = x\n  }\n}\n")
	checkOK(t, "fn t(ch: chan<string>) {\n  for s in ch {\n    var v: string = s\n  }\n}\n")
	checkOK(t, "fn t() {\n  for i in 0..10 {\n    var v: i32 = i\n  }\n}\n")
	checkOK(t, "fn t(xs: i32[]) {\n  for i in 0..xs.len() {\n    var v: usize = i\n  }\n}\n")
	checkError(t, "fn t(x: i32) {\n  for v in x {\n  }\n}\n", "not iterable")
	checkError(t, "fn t(s: string) {\n  for v in s {\n  }\n}\n", "use .runes()")
	checkError(t, "fn t(a: i32, b: i64) {\n  for v in a..b {\n  }\n}\n", "matching integer endpoints")
}

func TestDuckTypedIteration(t *testing.T) {
	counter := "struct Counter {\n  n: i32\n  limit: i32\n}\n\n" +
		"fn Counter::next(*self): (i32, bool) {\n  if self.n >= self.limit {\n    return 0, false\n  }\n  self.n += 1\n  return self.n, true\n}\n\n"
	checkOK(t, counter+"fn t() {\n  for x in (Counter{ 0, 5 }) {\n    var v: i32 = x\n  }\n}\n")

	viaIter := counter + "struct Bag {\n  size: i32\n}\n\n" +
		"fn Bag::iter(self): Counter {\n  return Counter{ 0, self.size }\n}\n\n"
	checkOK(t, viaIter+"fn t(b: Bag) {\n  for x in b {\n    var v: i32 = x\n  }\n}\n")
}

// ---------------------------------------------------------------------------
// Indexing and slices
// ---------------------------------------------------------------------------

func TestIndexing(t *testing.T) {
	checkOK(t, "fn t(xs: i32[]): i32 {\n  return xs[0]\n}\n")
	checkOK(t, "fn t(xs: i32[], i: usize): i32 {\n  return xs[i]\n}\n")
	checkError(t, "fn t(xs: i32[], i: i32) {\n  var v = xs[i]\n}\n", "indexes are usize")
	checkError(t, "fn t(x: i32) {\n  var v = x[0]\n}\n", "cannot be indexed")
}

func TestArraysBorrowAsSlices(t *testing.T) {
	checkOK(t, "fn total(xs: i32[]): i32 {\n  return 0\n}\n\nfn t(): i32 {\n  return total([1, 2, 3])\n}\n")
	checkError(t, "fn total(xs: i64[]): i64 {\n  return 0\n}\n\nfn t() {\n  var xs: i32[3] = [1, 2, 3]\n  var x = total(xs)\n}\n", "cannot use i32[3] as i64[]")
	checkError(t, "fn t() {\n  var xs = [1, \"two\"]\n}\n", "mixes i32 and string")
}

// ---------------------------------------------------------------------------
// Casts
// ---------------------------------------------------------------------------

func TestCasts(t *testing.T) {
	checkOK(t, "fn t(x: i32): i64 {\n  return x as i64\n}\n")
	checkOK(t, "fn t(b: u8): u32 {\n  return (b & 0xff) as u32\n}\n")
	checkOK(t, "fn t(r: rune): u32 {\n  return r as u32\n}\n")
	checkError(t, "fn t(s: string) {\n  var x = s as i32\n}\n", "as converts between numeric types and rune")
	checkError(t, "fn t(b: bool) {\n  var x = b as i32\n}\n", "as converts between numeric types and rune")
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

func TestAssignments(t *testing.T) {
	checkOK(t, "fn t() {\n  var x = 1\n  x = 2\n  x += 3\n}\n")
	checkError(t, "fn t() {\n  var x = 1\n  x = \"two\"\n}\n", "cannot use string as i32 in assignment")
	checkError(t, "fn t() {\n  var x = 1\n  x += \"two\"\n}\n", "matching numeric operands")
	checkError(t, "fn t(s: string) {\n  s += s\n}\n", "matching numeric operands")
	checkError(t, "fn t() {\n  1 = 2\n}\n", "cannot assign to this expression")
	checkOK(t, nodePrelude+"fn t(n: *Node) {\n  n.value = 3\n  (*n).value = 4\n}\n")
}

func TestDeferTakesACall(t *testing.T) {
	// The parser rejects a non-call defer; sema types the call normally.
	checkOK(t, "fn f() {\n}\n\nfn t() {\n  defer f()\n}\n")
	checkError(t, "fn f(v: i32) {\n}\n\nfn t() {\n  defer f(\"x\")\n}\n", "cannot use string as i32")
}

func TestDeclarationForms(t *testing.T) {
	checkOK(t, "fn t() {\n  var x: i32\n}\n")
}

// ---------------------------------------------------------------------------
// Module-level values
// ---------------------------------------------------------------------------

func TestModuleLevelValues(t *testing.T) {
	checkOK(t, "const MAX: i32 = 100\n\nfn t(): i32 {\n  return MAX / 2\n}\n")
	checkError(t, "const MAX: i32 = \"lots\"\n\nfn t() {\n}\n", "cannot use string as i32")
	checkOK(t, "fn fib(n: i32): i32 {\n  return n\n}\n\n@comptime const F: i32 = fib(10)\n\nfn t() {\n}\n")
	checkOK(t, "var counter = 0\n\nfn t(): i32 {\n  return counter\n}\n")
}

// ---------------------------------------------------------------------------
// Cross-module typing
// ---------------------------------------------------------------------------

func TestImportedSignaturesAreChecked(t *testing.T) {
	lib := "export fn add(a: i32, b: i32): i32 {\n  return a + b\n}\n"
	programOK(t, map[string]string{
		"main.gyro": "import { add } from \"./lib\"\n\nfn t(): i32 {\n  return add(1, 2)\n}\n",
		"lib.gyro":  lib,
	}, "main.gyro")
	programError(t, map[string]string{
		"main.gyro": "import { add } from \"./lib\"\n\nfn t() {\n  var x = add(1, \"two\")\n}\n",
		"lib.gyro":  lib,
	}, "main.gyro", "cannot use string as i32")
	programError(t, map[string]string{
		"main.gyro": "import { add } from \"./lib\"\n\nfn t() {\n  var x: string = add(1, 2)\n}\n",
		"lib.gyro":  lib,
	}, "main.gyro", "cannot use i32 as string")
}

func TestImportedGenericInstantiatesAcrossModules(t *testing.T) {
	lib := "export fn max<T>(a: T, b: T): T {\n  if a.compare(b) > 0 {\n    return a\n  }\n  return b\n}\n"
	programOK(t, map[string]string{
		"main.gyro": "import { max } from \"./lib\"\n\nfn t(): i32 {\n  return max(3, 7)\n}\n",
		"lib.gyro":  lib,
	}, "main.gyro")
	programError(t, map[string]string{
		"main.gyro": "import { max } from \"./lib\"\n\nstruct M {\n  n: i32\n}\n\nfn t(a: M, b: M) {\n  var x = max(a, b)\n}\n",
		"lib.gyro":  lib,
	}, "main.gyro", "in the instantiation max<M>")
}

func TestStdlibSignaturesAreReal(t *testing.T) {
	programOK(t, map[string]string{
		"main.gyro": "import { println, Array } from \"stdlib\"\n\nfn main() {\n" +
			"  var xs = Array<i32>::new()\n  xs.push(4)\n  var n: usize = xs.len()\n  println(n)\n  xs.free()\n}\n",
	}, "main.gyro")
	programError(t, map[string]string{
		"main.gyro": "import { Array } from \"stdlib\"\n\nfn main() {\n" +
			"  var xs = Array<i32>::new()\n  xs.push(\"four\")\n}\n",
	}, "main.gyro", "cannot use string as i32")
}
