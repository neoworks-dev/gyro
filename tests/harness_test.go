// Package tests holds the execution corpus: Gyro programs paired with the
// output they must produce.
//
// A program is run three ways — the interpreter, the METALS evaluator, and
// a native binary — and all three must agree with the golden file. The
// three tiers are meant to be indistinguishable, so a case written once is
// a case checked everywhere; before this corpus existed each tier kept its
// own hand-picked subset and a program was only cross-checked if someone
// remembered to paste it into all three test files.
package tests

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/interp"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/lower"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/meval"
	"github.com/gyro-lang/gyro/internal/nasm"
	"github.com/gyro-lang/gyro/internal/sema"
)

var update = flag.Bool("update", false, "rewrite the golden files from what the interpreter produces")

// ---------------------------------------------------------------------------
// Directives
// ---------------------------------------------------------------------------

// directives are `//gyro:` lines in the root file. They say what a case
// expects beyond its output.
//
//	//gyro:panic <fragment>   the program must abort; fragment matches the
//	                          interpreter's message, and the golden holds
//	                          whatever reached stdout first
//	//gyro:unordered          concurrent output; the tiers must all succeed
//	                          but their output is not compared
//	//gyro:skip <tier> <why>  a known gap: interp, metals or native
type directives struct {
	panicFragment string
	unordered     bool
	skipped       map[string]string
}

func parseDirectives(t *testing.T, source string) directives {
	t.Helper()
	found := directives{skipped: map[string]string{}}
	for _, line := range strings.Split(source, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "//gyro:")
		if !ok {
			continue
		}
		name, argument, _ := strings.Cut(rest, " ")
		argument = strings.TrimSpace(argument)
		switch name {
		case "panic":
			found.panicFragment = argument
		case "unordered":
			found.unordered = true
		case "skip":
			tier, reason, _ := strings.Cut(argument, " ")
			if tier != "interp" && tier != "metals" && tier != "native" {
				t.Fatalf("//gyro:skip names an unknown tier %q", tier)
			}
			found.skipped[tier] = strings.TrimSpace(reason)
		default:
			t.Fatalf("unknown directive //gyro:%s", name)
		}
	}
	return found
}

// ---------------------------------------------------------------------------
// Running a case
// ---------------------------------------------------------------------------

// result is what one tier produced.
type result struct {
	stdout  string
	stderr  string
	failed  bool
	message string
}

// runCase runs one program through every tier and compares each against the
// golden files. goldenBase is the path of the .out file, without extension.
func runCase(t *testing.T, rootPath, goldenBase string) {
	t.Helper()

	source, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	found := parseDirectives(t, string(source))

	program, info := compile(t, rootPath)
	interpResult := runInterp(t, program, found)

	if *update {
		writeGolden(t, goldenBase, interpResult, found)
	}
	if !found.unordered {
		compareGolden(t, "interp", goldenBase, interpResult)
	}

	module := lowerVerified(t, program, info, found)
	t.Run("metals", func(t *testing.T) {
		if reason, skip := found.skipped["metals"]; skip {
			t.Skip(reason)
		}
		if module == nil {
			t.Fatal("the program did not lower")
		}
		compare(t, "metals", goldenBase, runMetals(t, module, found), interpResult, found)
	})
	t.Run("native", func(t *testing.T) {
		if reason, skip := found.skipped["native"]; skip {
			t.Skip(reason)
		}
		if err := nasm.Toolchain(); err != nil {
			t.Skipf("skipping: %v", err)
		}
		if module == nil {
			t.Fatal("the program did not lower")
		}
		compare(t, "native", goldenBase, runNative(t, module, found), interpResult, found)
	})
}

// compare holds a tier to the golden, and to the interpreter for the parts
// the golden does not pin.
func compare(t *testing.T, tier, goldenBase string, got, want result, found directives) {
	t.Helper()
	if got.failed != want.failed {
		t.Errorf("%s failure state differs: interp failed=%v, %s failed=%v\n%s: %s",
			tier, want.failed, tier, got.failed, tier, got.message)
	}
	if found.unordered {
		return
	}
	compareGolden(t, tier, goldenBase, got)
}

func compareGolden(t *testing.T, tier, goldenBase string, got result) {
	t.Helper()
	wantOut := readGolden(t, goldenBase+".out")
	if got.stdout != wantOut {
		t.Errorf("%s stdout differs from %s.out\n got: %q\nwant: %q", tier, filepath.Base(goldenBase), got.stdout, wantOut)
	}
	// A case with no .err file expects nothing on stderr.
	wantErr := readGolden(t, goldenBase+".err")
	if got.stderr != wantErr {
		t.Errorf("%s stderr differs from %s.err\n got: %q\nwant: %q", tier, filepath.Base(goldenBase), got.stderr, wantErr)
	}
}

func readGolden(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// writeGolden records what the interpreter produced. The interpreter is the
// specification, so it is the tier allowed to define the answer.
func writeGolden(t *testing.T, goldenBase string, got result, found directives) {
	t.Helper()
	if found.unordered {
		return
	}
	writeOrRemove(t, goldenBase+".out", got.stdout)
	writeOrRemove(t, goldenBase+".err", got.stderr)
}

func writeOrRemove(t *testing.T, path, content string) {
	t.Helper()
	if content == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// The tiers
// ---------------------------------------------------------------------------

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

func runInterp(t *testing.T, program *loader.Program, found directives) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := interp.Run(program, &stdout, &stderr)
	got := result{stdout: stdout.String(), stderr: stderr.String(), failed: err != nil}
	if err != nil {
		got.message = err.Error()
	}

	if found.panicFragment == "" {
		if err != nil {
			t.Fatalf("the interpreter aborted: %v", err)
		}
		return got
	}

	var abort *interp.Panic
	if !errors.As(err, &abort) {
		t.Fatalf("expected a panic containing %q, got %v", found.panicFragment, err)
	}
	if !strings.Contains(abort.Message, found.panicFragment) {
		t.Errorf("panic %q does not contain %q", abort.Message, found.panicFragment)
	}
	// The panic text carries a source position and is not part of the
	// golden, since the other tiers word a few of them differently.
	got.stderr = ""
	return got
}

// lowerVerified lowers a checked program, verifies it, and pins the text
// round trip. It returns nil when the case declares lowering a known gap.
func lowerVerified(t *testing.T, program *loader.Program, info *sema.Info, found directives) *metals.Module {
	t.Helper()
	_, skipMetals := found.skipped["metals"]
	_, skipNative := found.skipped["native"]
	if skipMetals && skipNative {
		return nil
	}

	module, err := lower.Lower(program, info)
	if err != nil {
		if skipMetals || skipNative {
			return nil
		}
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

func runMetals(t *testing.T, module *metals.Module, found directives) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := meval.Run(module, &stdout, &stderr)
	got := result{stdout: stdout.String(), stderr: stderr.String(), failed: err != nil}
	if err != nil {
		got.message = err.Error()
	}
	if found.panicFragment != "" {
		got.stderr = ""
	}
	return got
}

func runNative(t *testing.T, module *metals.Module, found directives) result {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "program")
	if err := nasm.Build(module, binary); err != nil {
		t.Fatalf("build: %v", err)
	}

	command := exec.Command(binary)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run: %v", err)
		}
	}

	got := result{stdout: stdout.String(), stderr: stderr.String(), failed: err != nil}
	if err != nil {
		got.message = stderr.String()
	}
	if found.panicFragment != "" {
		got.stderr = ""
	}
	return got
}
