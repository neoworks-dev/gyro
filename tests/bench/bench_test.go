// Package bench compares the two native backends — and, where the case
// is a port, the C original — on the same programs.
//
// The corpus in tests/ says the tiers agree; this says what they cost.
// Each program isolates one codegen decision — call sequences, bounds
// checks, checked arithmetic, aggregate copies, allocation — so the
// column that matters is the spread between the cases, not the average.
// A backend that is 1.1x behind everywhere has one problem; a backend
// that is 1.0x on four cases and 2.1x on the fifth has a specific one.
//
// A case named NAME may carry a C reference beside it as NAME.c: the
// program the Gyro was ported from, doing the same work over the same
// input, compiled with the flags the C backend itself uses. That turns
// the ratio into the number that actually matters — what the language
// costs against the C it replaces — and the three binaries must agree on
// stdout, so the comparison is only ever between programs that computed
// the same thing.
//
// The suite is off by default because it takes seconds per case:
//
//	make bench                 the whole table
//	make bench BENCH=struct    only the cases whose name contains "struct"
//
// Each program times its own hot region with time.monotonic — clock_gettime
// in the C references — and reports it on stderr as `gyro:bench
// <nanoseconds>`, so process start, runtime init and the setup loop stay
// out of the number. Process wall time is reported separately, and the
// startup case is that column's baseline.
package bench

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gyro-lang/gyro/internal/cgen"
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/lower"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/nasm"
	"github.com/gyro-lang/gyro/internal/sema"
)

var (
	enabled = flag.Bool("gyro.bench", false, "run the codegen benchmark suite")
	repeats = flag.Int("gyro.bench.repeat", 5, "how many times to run each binary; the fastest run counts")
	filter  = flag.String("gyro.bench.filter", "", "only run cases whose name contains this")
)

// referenceName is the column the C original occupies. It is not a
// backend: nothing lowers into it, and only the cases with a NAME.c
// beside them have one.
const referenceName = "cc"

// backends are the two things always compared. The order here is the
// column order in the report, and the first one is the numerator of the
// ratio: >1 means nasm is behind.
var backends = []struct {
	name  string
	build func(*metals.Module, string) error
	check func() error
}{
	{"nasm", nasm.Build, nasm.Toolchain},
	{"cgen", cgen.Build, cgen.Toolchain},
}

// measurement is what one backend produced for one program.
type measurement struct {
	hot    time.Duration // the program's own timing of its hot region
	wall   time.Duration // the whole process, start to exit
	build  time.Duration // lowered module to executable
	stdout string        // the checksum, compared across backends
}

func TestBench(t *testing.T) {
	if !*enabled {
		t.Skip("codegen benchmarks are off; run `make bench` or pass -gyro.bench=true")
	}
	for _, backend := range backends {
		if err := backend.check(); err != nil {
			t.Skipf("%s backend unavailable: %v", backend.name, err)
		}
	}

	programs := findPrograms(t)
	if len(programs) == 0 {
		t.Fatalf("no benchmark programs match %q", *filter)
	}

	results := make([]map[string]measurement, len(programs))
	for i, program := range programs {
		results[i] = runCase(t, program)
	}
	report(programs, results)
}

// findPrograms lists the cases, in name order so the table is stable. A
// case is either NAME.gyro or a NAME/ directory holding main.gyro — the
// same two shapes the execution corpus uses, and for the same reason: a
// port is more than one module.
func findPrograms(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join("testdata", name, "main.gyro")); err != nil {
				continue
			}
		} else if strings.HasSuffix(name, ".gyro") {
			name = strings.TrimSuffix(name, ".gyro")
		} else {
			continue
		}
		if *filter == "" || strings.Contains(name, *filter) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// programPath is the entry module of a case, whichever shape it has.
func programPath(name string) string {
	if directory := filepath.Join("testdata", name); isDirectory(directory) {
		return filepath.Join(directory, "main.gyro")
	}
	return filepath.Join("testdata", name+".gyro")
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// runCase lowers one program once and measures it under every backend,
// and under the C original when the case has one. The binaries are built
// up front and then run interleaved, so a machine that slows down halfway
// through penalizes all of them equally.
func runCase(t *testing.T, name string) map[string]measurement {
	t.Helper()
	module := lowerProgram(t, programPath(name))

	workDir := t.TempDir()
	measured := map[string]measurement{}
	binaries := map[string]string{}
	var columns []string
	for _, backend := range backends {
		binary := filepath.Join(workDir, name+"."+backend.name)
		start := time.Now()
		if err := backend.build(module, binary); err != nil {
			t.Fatalf("%s: %s build: %v", name, backend.name, err)
		}
		binaries[backend.name] = binary
		measured[backend.name] = measurement{build: time.Since(start)}
		columns = append(columns, backend.name)
	}
	if sources := referenceSources(t, name); len(sources) > 0 {
		binary := filepath.Join(workDir, name+"."+referenceName)
		start := time.Now()
		if err := buildReference(sources, binary); err != nil {
			t.Fatalf("%s: %s build: %v", name, referenceName, err)
		}
		binaries[referenceName] = binary
		measured[referenceName] = measurement{build: time.Since(start)}
		columns = append(columns, referenceName)
	}

	for run := 0; run < *repeats; run++ {
		for _, column := range columns {
			hot, wall, stdout := runBinary(t, name, column, binaries[column])
			best := measured[column]
			if best.stdout == "" {
				best.stdout = stdout
			} else if best.stdout != stdout {
				t.Fatalf("%s: %s is not deterministic: %q then %q",
					name, column, best.stdout, stdout)
			}
			if best.hot == 0 || hot < best.hot {
				best.hot = hot
			}
			if best.wall == 0 || wall < best.wall {
				best.wall = wall
			}
			measured[column] = best
		}
	}

	// Comparing two programs only means something when they computed the
	// same thing. For a port that is the whole claim: the Gyro and the C
	// it came from print the same bytes.
	first := columns[0]
	for _, column := range columns[1:] {
		if measured[column].stdout != measured[first].stdout {
			t.Fatalf("%s: %s printed %q but %s printed %q",
				name, first, measured[first].stdout, column, measured[column].stdout)
		}
	}
	return measured
}

// referenceSources lists the C of a case: reference.c, the driver that
// mirrors main.gyro, plus the upstream library files beside it, copied in
// unmodified so the column really is the original and not a rewrite of
// it. A case with no reference.c is a hand-written codegen probe with
// nothing to port from, and returns nothing.
func referenceSources(t *testing.T, name string) []string {
	t.Helper()
	directory := filepath.Join("testdata", name)
	if !isDirectory(directory) {
		return nil
	}
	if _, err := os.Stat(filepath.Join(directory, "reference.c")); err != nil {
		return nil
	}
	sources, err := filepath.Glob(filepath.Join(directory, "*.c"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(sources)
	return sources
}

// buildReference compiles the C original with the flags the C backend
// uses on its own output, so the column is a compiler comparison and not
// an optimization-level one.
func buildReference(sources []string, binary string) error {
	args := append([]string{"-O1", "-fno-strict-aliasing"}, sources...)
	args = append(args, "-o", binary, "-lpthread", "-lm")
	command := exec.Command("cc", args...)
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("cc failed: %v\n%s", err, stderr.String())
	}
	return nil
}

func lowerProgram(t *testing.T, path string) *metals.Module {
	t.Helper()
	var bag diag.Bag
	program, err := loader.Load(path, &bag)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	if bag.HasErrors() {
		t.Fatalf("%s does not parse cleanly: %v", path, bag.Items())
	}
	info := sema.CheckProgram(program, &bag)
	if bag.HasErrors() {
		t.Fatalf("%s does not check cleanly: %v", path, bag.Items())
	}
	module, err := lower.Lower(program, info)
	if err != nil {
		t.Fatalf("lower %s: %v", path, err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatalf("%s fails verification: %v", path, err)
	}
	return module
}

// runBinary runs one binary once and returns its self-reported hot time,
// its process wall time and its checksum output.
func runBinary(t *testing.T, name, backend, binary string) (time.Duration, time.Duration, string) {
	t.Helper()
	command := exec.Command(binary)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	start := time.Now()
	err := command.Run()
	wall := time.Since(start)
	if err != nil {
		t.Fatalf("%s: %s run: %v\n%s", name, backend, err, stderr.String())
	}
	return parseHot(t, name, backend, stderr.String()), wall, stdout.String()
}

// parseHot reads the `gyro:bench <nanoseconds>` line the program prints.
func parseHot(t *testing.T, name, backend, stderr string) time.Duration {
	t.Helper()
	for _, line := range strings.Split(stderr, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gyro:bench ")
		if !ok {
			continue
		}
		nanoseconds, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			t.Fatalf("%s: %s reported an unreadable timing %q", name, backend, rest)
		}
		return time.Duration(nanoseconds)
	}
	t.Fatalf("%s: %s printed no `gyro:bench <nanoseconds>` line; got %q", name, backend, stderr)
	return 0
}

// ---------------------------------------------------------------------------
// The report
// ---------------------------------------------------------------------------

// report prints the table to stdout rather than through t.Log, so it
// arrives without a file:line prefix on every row. The C column and the
// ratios against it appear only when at least one case is a port.
func report(programs []string, results []map[string]measurement) {
	width := len("benchmark")
	for _, name := range programs {
		if len(name) > width {
			width = len(name)
		}
	}

	ported := false
	for _, result := range results {
		if _, ok := result[referenceName]; ok {
			ported = true
		}
	}

	first, second := backends[0].name, backends[1].name
	fmt.Printf("\ncodegen benchmarks — hot region, best of %d runs\n\n", *repeats)
	fmt.Printf("%-*s %10s %10s %10s", width, "benchmark", first, second, first+"/"+second)
	if ported {
		fmt.Printf(" %10s %10s %10s", referenceName,
			first+"/"+referenceName, second+"/"+referenceName)
	}
	fmt.Println()
	for i, name := range programs {
		a := results[i][first]
		b := results[i][second]
		fmt.Printf("%-*s %10s %10s %10s", width, name,
			showDuration(a.hot), showDuration(b.hot), showRatio(a.hot, b.hot))
		if ported {
			c, ok := results[i][referenceName]
			if !ok {
				fmt.Printf(" %10s %10s %10s", "—", "—", "—")
			} else {
				fmt.Printf(" %10s %10s %10s", showDuration(c.hot),
					showRatio(a.hot, c.hot), showRatio(b.hot, c.hot))
			}
		}
		fmt.Println()
	}

	fmt.Printf("\nprocess wall time and build time, best of %d runs\n\n", *repeats)
	fmt.Printf("%-*s %10s %10s %10s %10s %10s\n", width, "benchmark",
		"wall "+first, "wall "+second, "ratio",
		"build "+first, "build "+second)
	for i, name := range programs {
		a := results[i][first]
		b := results[i][second]
		fmt.Printf("%-*s %10s %10s %10s %10s %10s\n", width, name,
			showDuration(a.wall), showDuration(b.wall), showRatio(a.wall, b.wall),
			showDuration(a.build), showDuration(b.build))
	}
	fmt.Printf("\na ratio is the left column over the right: above 1.00x means the\n"+
		"left one is slower. %s is the C the case was ported from, built with\n"+
		"the flags the C backend uses on its own output.\n\n", referenceName)
}

func showDuration(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	default:
		return fmt.Sprintf("%.1fµs", float64(d)/float64(time.Microsecond))
	}
}

func showRatio(first, second time.Duration) string {
	if second == 0 {
		return "—"
	}
	return fmt.Sprintf("%.2fx", float64(first)/float64(second))
}
