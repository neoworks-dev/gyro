// Package tests holds the execution corpus: Gyro programs paired with the
// output they must produce.
//
// A program is run four ways — the interpreter, the METALS evaluator, a
// native binary from the assembler backend, and a native binary from the C
// backend — and all four must agree with the golden file. The tiers are
// meant to be indistinguishable, so a case written once is a case checked
// everywhere; before this corpus existed each tier kept its own
// hand-picked subset and a program was only cross-checked if someone
// remembered to paste it into all of them.
package tests

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/cgen"
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

// TestMain pins one environment variable for every case, so os.env has a
// value to observe deterministically: the in-process tiers read it
// directly and a native child inherits it. Cases run in parallel, which
// rules out t.Setenv.
func TestMain(m *testing.M) {
	os.Setenv("GYRO_TEST_ENV", "corpus")
	os.Exit(m.Run())
}

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
//	//gyro:skip <tier> <why>  a known gap: interp, metals, native or cnative
//	//gyro:args a b c         main's arguments, after a fixed argv[0]
//	//gyro:stdin <text>       one line of stdin, newline included; repeat
//	                          the directive for more lines
//	//gyro:exit <code>        the process exit code every tier must produce
//	                          (default 0; a panic case always exits 1)
type directives struct {
	panicFragment string
	unordered     bool
	skipped       map[string]string
	args          []string
	stdin         string
	exit          int
}

// tiers are the names //gyro:skip accepts.
var tiers = map[string]bool{"interp": true, "metals": true, "native": true, "cnative": true}

// entryName is argv[0] for every case. The three tiers would otherwise
// disagree — a source path, a temp binary — over something no program
// should depend on.
const entryName = "program"

func parseDirectives(t *testing.T, source string) directives {
	t.Helper()
	found := directives{skipped: map[string]string{}, args: []string{entryName}}
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
		case "args":
			found.args = append([]string{entryName}, strings.Fields(argument)...)
		case "stdin":
			found.stdin += argument + "\n"
		case "exit":
			code, err := strconv.Atoi(argument)
			if err != nil || code < 0 || code > 255 {
				t.Fatalf("//gyro:exit needs a code in 0..255, got %q", argument)
			}
			found.exit = code
		case "skip":
			tier, reason, _ := strings.Cut(argument, " ")
			if !tiers[tier] {
				t.Fatalf("//gyro:skip names an unknown tier %q", tier)
			}
			found.skipped[tier] = strings.TrimSpace(reason)
		default:
			t.Fatalf("unknown directive //gyro:%s", name)
		}
	}
	if found.panicFragment != "" && found.exit != 0 {
		t.Fatal("//gyro:exit cannot combine with //gyro:panic; a panic always exits 1")
	}
	return found
}

// wantExit is the exit code every tier must produce.
func (d directives) wantExit() int {
	if d.panicFragment != "" {
		return 1
	}
	return d.exit
}

// ---------------------------------------------------------------------------
// Running a case
// ---------------------------------------------------------------------------

// result is what one tier produced. exit is the process exit code: 0 on
// success, 1 for a panic, the given code after os.exit.
type result struct {
	stdout  string
	stderr  string
	exit    int
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
	if interpResult.exit != found.wantExit() {
		t.Errorf("interp exited with %d, the case expects %d", interpResult.exit, found.wantExit())
	}

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
		compare(t, "native", goldenBase, runBinary(t, module, found, nasm.Build), interpResult, found)
	})
	t.Run("cnative", func(t *testing.T) {
		if reason, skip := found.skipped["cnative"]; skip {
			t.Skip(reason)
		}
		if err := cgen.Toolchain(); err != nil {
			t.Skipf("skipping: %v", err)
		}
		if module == nil {
			t.Fatal("the program did not lower")
		}
		compare(t, "cnative", goldenBase, runBinary(t, module, found, cgen.Build), interpResult, found)
	})
}

// compare holds a tier to the golden, and to the interpreter for the parts
// the golden does not pin.
func compare(t *testing.T, tier, goldenBase string, got, want result, found directives) {
	t.Helper()
	if got.exit != want.exit {
		t.Errorf("%s exit code differs: interp exited %d, %s exited %d\n%s: %s",
			tier, want.exit, tier, got.exit, tier, got.message)
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
	err := interp.Run(program, found.args, strings.NewReader(found.stdin), &stdout, &stderr)
	got := result{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		got.message = err.Error()
	}

	var exit *interp.Exit
	if errors.As(err, &exit) {
		got.exit = exit.Code
		return got
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
	got.exit = 1
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
	_, skipCNative := found.skipped["cnative"]
	if skipMetals && skipNative && skipCNative {
		return nil
	}

	module, err := lower.Lower(program, info)
	if err != nil {
		if skipMetals || skipNative || skipCNative {
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
	err := meval.Run(module, found.args, strings.NewReader(found.stdin), &stdout, &stderr)
	got := result{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		got.message = err.Error()
		got.exit = 1
		var exit *meval.Exit
		if errors.As(err, &exit) {
			got.exit = exit.Code
		}
	}
	if found.panicFragment != "" {
		got.stderr = ""
	}
	return got
}

// runBinary compiles the module with one of the two native backends and
// runs what comes out. Both produce a Linux executable from the same
// verified module, so the harness only has to vary the builder.
func runBinary(t *testing.T, module *metals.Module, found directives,
	build func(*metals.Module, string) error) result {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "program")
	if err := build(module, binary); err != nil {
		t.Fatalf("build: %v", err)
	}

	// argv[0] is set explicitly: the temp binary's path would differ from
	// what the other two tiers see, over something no program should care
	// about.
	command := exec.Command(binary)
	command.Args = found.args
	command.Stdin = strings.NewReader(found.stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()

	got := result{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run: %v", err)
		}
		got.exit = exit.ExitCode()
		got.message = stderr.String()
	}
	if found.panicFragment != "" {
		got.stderr = ""
	}
	return got
}
