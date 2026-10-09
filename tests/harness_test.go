// Package tests holds the execution corpus: programs paired with the
// output they must produce.
//
// A program is built by the assembler backend and run, and what it prints
// must be the golden file. The golden is the oracle and the backend is
// not: a golden says what the program prints and is written by hand,
// deliberately, the way one states an expectation, and there is no flag
// that regenerates it from what the backend happened to do. That is the
// property that stops the backend from quietly redefining the language it
// implements — deciding what a program should print is the work, and a
// harness that copied the answer out of the implementation would be
// checking only that the implementation is deterministic.
package tests

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/lower"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/nasm"
	"github.com/gyro-lang/gyro/internal/sema"
)

// TestMain pins one environment variable for every case, so os.env has a
// value to observe deterministically: the native child inherits it. Cases
// run in parallel, which rules out t.Setenv.
func TestMain(m *testing.M) {
	os.Setenv("GYRO_TEST_ENV", "corpus")
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Directives
// ---------------------------------------------------------------------------

// directives are `//gyro:` lines in the root file — `;gyro:` in a root
// whose language comments with a semicolon. They say what a case expects
// beyond its output.
//
//	//gyro:panic <fragment>   the program must abort; its message must
//	                          contain the fragment, and the golden holds
//	                          whatever reached stdout first
//	//gyro:unordered          concurrent output; the program must succeed
//	                          but its output is not compared
//	//gyro:skip <why>         a known gap, with the reason it is one
//	//gyro:args a b c         main's arguments, after a fixed argv[0]
//	//gyro:stdin <text>       one line of stdin, newline included; repeat
//	                          the directive for more lines
//	//gyro:exit <code>        the process exit code the program must
//	                          produce (default 0; a panic case exits 1)
type directives struct {
	panicFragment string
	unordered     bool
	skipped       string
	skip          bool
	args          []string
	stdin         string
	exit          int
}

// entryName is argv[0] for every case, rather than the path of the
// temporary binary, which no program should depend on.
const entryName = "program"

func parseDirectives(t *testing.T, source string) directives {
	t.Helper()
	found := directives{args: []string{entryName}}
	for _, line := range strings.Split(source, "\n") {
		// `//` is Gyro's comment and `;` is METALS'; a directive reads
		// the same either way.
		trimmed := strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(trimmed, "//gyro:")
		if !ok {
			rest, ok = strings.CutPrefix(trimmed, ";gyro:")
		}
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
			if argument == "" {
				t.Fatal("//gyro:skip needs the reason the case is a known gap")
			}
			found.skip, found.skipped = true, argument
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

// runCase builds one program, runs it and compares it against the golden
// files. goldenBase is the path of the .out file, without extension.
func runCase(t *testing.T, rootPath, goldenBase string) {
	t.Helper()

	source, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	found := parseDirectives(t, string(source))
	if found.skip {
		t.Skip(found.skipped)
	}
	if err := nasm.Toolchain(); err != nil {
		t.Skipf("skipping: %v", err)
	}

	// One module, however it was written: a .gyro is loaded, checked and
	// lowered, and anything else comes through its own producer.
	var module *metals.Module
	if filepath.Ext(rootPath) == ".gyro" {
		program, info := compile(t, rootPath)
		module = lowerVerified(t, program, info)
	} else {
		module = foreignRoot(t, rootPath, string(source))
	}
	compare(t, goldenBase, runBinary(t, module, found), found)
}

// compare holds the program to what the case declares: the exit code, the
// panic text when one is expected, and the golden files. The case is the
// authority — nothing here reads anything the implementation produced
// except the output being judged.
func compare(t *testing.T, goldenBase string, got result, found directives) {
	t.Helper()
	if got.exit != found.wantExit() {
		t.Errorf("the program exited with %d, the case expects %d\n%s",
			got.exit, found.wantExit(), got.message)
	}
	if found.panicFragment != "" && !strings.Contains(got.message, found.panicFragment) {
		t.Errorf("the program aborted with %q, which does not contain %q",
			got.message, found.panicFragment)
	}
	if found.unordered {
		return
	}
	compareGolden(t, goldenBase, got)
}

func compareGolden(t *testing.T, goldenBase string, got result) {
	t.Helper()
	wantOut := readGolden(t, goldenBase+".out")
	if got.stdout != wantOut {
		t.Errorf("stdout differs from %s.out\n got: %q\nwant: %q", filepath.Base(goldenBase), got.stdout, wantOut)
	}
	// A case with no .err file expects nothing on stderr.
	wantErr := readGolden(t, goldenBase+".err")
	if got.stderr != wantErr {
		t.Errorf("stderr differs from %s.err\n got: %q\nwant: %q", filepath.Base(goldenBase), got.stderr, wantErr)
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

// ---------------------------------------------------------------------------
// Building and running
// ---------------------------------------------------------------------------

// foreignRoot produces the module for a case that is METALS written by
// hand. It is the same path `gyro build` takes for such a file, verifier
// included: the format trusts no producer, and a hand-written root is how
// the corpus reaches instructions no lowering happens to emit.
func foreignRoot(t *testing.T, rootPath, source string) *metals.Module {
	t.Helper()

	if filepath.Ext(rootPath) != ".metals" {
		t.Fatalf("no producer for %s", rootPath)
	}
	module, err := metals.Parse(source)
	if err != nil {
		t.Fatalf("%s: %v", filepath.Base(rootPath), err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatalf("%s does not verify: %v\n%s", filepath.Base(rootPath), err, metals.Print(module))
	}

	// A program of one module is still linked, because that is also where
	// a module's initializer becomes the call at the top of main.
	linked, err := metals.Link([]*metals.Module{module})
	if err != nil {
		t.Fatalf("%s: %v", filepath.Base(rootPath), err)
	}
	if err := metals.Verify(linked); err != nil {
		t.Fatalf("%s does not verify once linked: %v\n%s",
			filepath.Base(rootPath), err, metals.Print(linked))
	}
	return linked
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

// lowerVerified lowers a checked program, verifies it, and pins the text
// round trip.
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

// runBinary builds the module and runs what comes out.
func runBinary(t *testing.T, module *metals.Module, found directives) result {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "program")
	if err := nasm.Build(module, binary); err != nil {
		t.Fatalf("build: %v", err)
	}

	// argv[0] is set explicitly: the temp binary's path changes from run
	// to run, over something no program should care about.
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
