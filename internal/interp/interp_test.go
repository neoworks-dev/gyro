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

// What the interpreter computes is checked by the corpus in tests/, which
// holds it and the other two tiers to one golden file. What is left here
// is the interpreter's own surface: the Panic value it hands back, which
// no amount of comparing output would pin.

// run type-checks and executes a program, returning what it printed and
// the panic that aborted it, if one did.
func run(t *testing.T, source string) (string, *interp.Panic) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "main.gyro")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	var bag diag.Bag
	program, err := loader.Load(path, &bag)
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

	var abort *interp.Panic
	if !errors.As(runErr, &abort) {
		t.Fatalf("run failed without a panic: %v", runErr)
	}
	return stdout.String(), abort
}

// A panic names the line it happened on, which is the whole reason it is a
// value rather than a string.
func TestPanicCarriesPosition(t *testing.T) {
	_, abort := run(t, `fn main() {
  var xs = [1, 2, 3]
  var v = xs[7]
}
`)
	if abort == nil {
		t.Fatal("expected a panic")
	}
	if abort.Position.Line != 3 {
		t.Errorf("panic position line = %d, want 3", abort.Position.Line)
	}
	if !strings.Contains(abort.Message, "index 7 out of range for length 3") {
		t.Errorf("unexpected panic message %q", abort.Message)
	}
}

// A panic inside a called function reports where the fault is, not where
// the call was written.
func TestPanicPositionIsTheFaultingFrame(t *testing.T) {
	_, abort := run(t, `fn at(xs: i32[], i: usize): i32 {
  return xs[i]
}

fn main() {
  var xs = [1, 2, 3]
  var v = at(xs, 9)
}
`)
	if abort == nil {
		t.Fatal("expected a panic")
	}
	if abort.Position.Line != 2 {
		t.Errorf("panic position line = %d, want 2", abort.Position.Line)
	}
}
