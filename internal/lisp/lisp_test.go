package lisp

import (
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// compiled compiles a source and insists the result verifies. A producer
// that emits a module the verifier rejects is the failure this package
// exists to catch, so every success case checks it.
func compiled(t *testing.T, source string) *metals.Module {
	t.Helper()
	module, err := Compile("test.lisp", source)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatalf("the module does not verify: %v\n%s", err, metals.Print(module))
	}
	return module
}

func refused(t *testing.T, source, fragment string) {
	t.Helper()
	_, err := Compile("test.lisp", source)
	if err == nil {
		t.Fatalf("expected an error containing %q, the source compiled", fragment)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("expected an error containing %q, got %v", fragment, err)
	}
}

const fib = `
(export fib)
(: fib (-> i64 i64))
(define (fib n)
  (if (< n 2) n (+ (fib (- n 1)) (fib (- n 2)))))
`

func TestModuleNameCarriesTheProducer(t *testing.T) {
	module := compiled(t, fib)
	// The scheme is what makes a call from another language a boundary
	// crossing, so the verifier applies the boundary rules to the export.
	if module.Name != "lisp:test" {
		t.Errorf("module name is %q, want lisp:test", module.Name)
	}
}

func TestOnlyDeclaredExportsAreExported(t *testing.T) {
	module := compiled(t, `
(export shown)
(: shown (-> i64 i64))
(define (shown n) (hidden n))
(: hidden (-> i64 i64))
(define (hidden n) (+ n 1))
`)
	for _, fn := range module.Funcs {
		if fn.Name == "hidden" && fn.Export {
			t.Error("hidden is exported, but no export form names it")
		}
		if fn.Name == "shown" && !fn.Export {
			t.Error("shown is not exported, but the export form names it")
		}
	}
}

// Definitions may call each other in any order: every signature is
// collected before any body is compiled.
func TestMutualRecursion(t *testing.T) {
	compiled(t, `
(export even-p)
(: even-p (-> i64 bool))
(define (even-p n) (if (= n 0) #t (odd-p (- n 1))))
(: odd-p (-> i64 bool))
(define (odd-p n) (if (= n 0) #f (even-p (- n 1))))
`)
}

func TestReaderAcceptsSchemeSurface(t *testing.T) {
	// Line comments, nested block comments, bracket lists, escapes, a
	// bare `-` as a name rather than a number, and #t/#f.
	module := compiled(t, `
; a line comment
#| a block comment #| nested |# still a comment |#
(export go)
(: go (-> bool str))
(define (go flag)
  (if flag "yes\n\t\"quoted\"" (pick 1 2)))
(: pick (-> i64 i64 str))
(define (pick a b)
  [if (< a b) "less" "more"])
`)
	if len(module.Funcs) != 2 {
		t.Errorf("expected two functions, got %d", len(module.Funcs))
	}
}

func TestNaryAndChainedForms(t *testing.T) {
	compiled(t, `
(export go)
(: go (-> i64 bool))
(define (go n)
  (and (< 1 2 3) (or (> n 100) (= (+ 1 2 3) 6))))
`)
}

// A lambda in a checking position needs no annotation, which is what
// keeps the surface ordinary Scheme.
func TestLambdaTakesItsTypeFromContext(t *testing.T) {
	compiled(t, `
(export go)
(: apply-i64 (-> (-> i64 i64) i64 i64))
(define (apply-i64 f x) (f x))
(: go (-> i64 i64))
(define (go n) (apply-i64 (lambda (v) (* v n)) 2))
`)
}

// An internal define is how a Scheme names a local function, and it is
// the spelling that needs no annotation the language does not already
// have.
func TestInternalDefine(t *testing.T) {
	compiled(t, `
(export go)
(: go (-> i64 i64))
(define (go n)
  (: shift (-> i64 i64))
  (define (shift v) (+ v n))
  (shift 2))
`)
}

func TestInternalDefineNeedsItsDeclaration(t *testing.T) {
	refused(t, `
(export go)
(: go (-> i64 i64))
(define (go n)
  (define (shift v) (+ v n))
  (shift 2))
`, "shift has no type declaration")
}

// Captures are copies, as they are in Gyro. Assigning to one would write
// the copy, so it is refused rather than done quietly.
func TestAssigningToACaptureIsRefused(t *testing.T) {
	refused(t, `
(export go)
(: apply-i64 (-> (-> i64 i64) i64 i64))
(define (apply-i64 f x) (f x))
(: go (-> i64 i64))
(define (go n) (apply-i64 (lambda (v) (begin (set! n v) n)) 2))
`, "was captured by this lambda")
}

func TestLambdaWithoutContextIsRefused(t *testing.T) {
	refused(t, `
(export go)
(: go (-> i64 i64))
(define (go n)
  (let ((f (lambda (v) (* v n))))
    (f 2)))
`, "nothing here says what this lambda's parameters are")
}

func TestDefinitionNeedsADeclaration(t *testing.T) {
	refused(t, "(define (go n) n)", "go has no type declaration")
}

func TestDeclaredArityMustMatch(t *testing.T) {
	refused(t, `
(: go (-> i64 i64 i64))
(define (go n) n)
`, "declared with 2 parameter(s) but defined with 1")
}

func TestResultTypeIsChecked(t *testing.T) {
	refused(t, `
(: go (-> i64 str))
(define (go n) n)
`, "expected str, found i64")
}

func TestBranchesMustAgree(t *testing.T) {
	refused(t, `
(: go (-> bool i64))
(define (go flag) (if flag 1 "two"))
`, "expected i64, found str")
}

func TestUnknownNameIsRefused(t *testing.T) {
	refused(t, `
(: go (-> i64 i64))
(define (go n) (missing n))
`, "missing is not defined")
}

func TestUnknownTypeIsRefused(t *testing.T) {
	refused(t, "(: go (-> widget i64))", "unknown type widget")
}

func TestVoidIsOnlyAResult(t *testing.T) {
	refused(t, "(: go (-> void i64))", "void is a result")
}

// A literal takes its type from the context, and one that does not fit
// is an error rather than a silent truncation.
func TestLiteralMustFitItsContext(t *testing.T) {
	refused(t, `
(: go (-> i32 i32))
(define (go n) (+ n 3000000000))
`, "does not fit i32")
}

func TestUnbalancedSourceIsRefused(t *testing.T) {
	refused(t, "(export go", "unterminated list")
	refused(t, "(export go))", `unbalanced ")"`)
	refused(t, `(: go (-> str str))`+"\n"+`(define (go s) "open)`, "unterminated string")
}

func TestErrorsCarryAPosition(t *testing.T) {
	_, err := Compile("test.lisp", "\n\n(define (go n) n)\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.HasPrefix(err.Error(), "test.lisp:3:") {
		t.Errorf("error does not name its line: %v", err)
	}
}
