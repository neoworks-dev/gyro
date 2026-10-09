package sema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/loader"
)

// The diagnostics corpus. A fixture is a Gyro file that says what it
// expects of itself: every diagnostic the checker reports has to be
// claimed by a `//~` marker on its line, and every marker has to be
// satisfied. A file with no markers must check clean.
//
// Writing the case as Gyro rather than as a Go string literal is what
// makes the position part of the assertion. The old helpers matched a
// substring anywhere in the bag, so a diagnostic pointing at the wrong
// line passed.
//
//	var x: u8 = 300        //~ E0301 does not fit u8
//	return xs              //~ E0609 which the row does not name
//	                       //~ help: say so in the row: !{ &xs }
//
// `note:` and `help:` claim the lines under a diagnostic; `span:` claims
// one of its related frames, by the frame's label or by its location, so a
// fixture can assert both what a frame says and that it points into another
// file.
//
// A marker may claim a line other than its own with `^`, one per line
// back, for a diagnostic that lands where a marker would not fit.
//
// Fixtures live in testdata/check. A case that needs a second module is a
// directory with main.gyro in it.

const fixtureDir = "testdata/check"

func TestDiagnosticFixtures(t *testing.T) {
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}

	found := 0
	for _, entry := range entries {
		root, ok := fixtureRoot(entry)
		if !ok {
			continue
		}
		found++
		markerFiles := []string{root}
		if entry.IsDir() {
			markerFiles = nil
		}
		t.Run(strings.TrimSuffix(entry.Name(), ".gyro"), func(t *testing.T) {
			t.Parallel()
			runFixture(t, root, markerFiles)
		})
	}
	if found == 0 {
		t.Fatalf("no fixtures found in %s", fixtureDir)
	}
}

func fixtureRoot(entry os.DirEntry) (string, bool) {
	if entry.IsDir() {
		return filepath.Join(fixtureDir, entry.Name(), "main.gyro"), true
	}
	if !strings.HasSuffix(entry.Name(), ".gyro") {
		return "", false
	}
	return filepath.Join(fixtureDir, entry.Name()), true
}

// ---------------------------------------------------------------------------
// Expectations
// ---------------------------------------------------------------------------

// expectation is one `//~` marker: something that must appear in a
// diagnostic reported on a given line.
type expectation struct {
	// file is the base name of the fixture file the marker is written in.
	file string
	line int
	// kind is "message", "note", "help" or "span".
	kind string
	// code is the diag.Code the diagnostic must carry, or "" for any.
	code string
	text string
	// met records whether a diagnostic satisfied this marker.
	met bool
}

func parseExpectations(t *testing.T, file, source string) []expectation {
	t.Helper()
	var found []expectation
	for index, line := range strings.Split(source, "\n") {
		_, marker, ok := strings.Cut(line, "//~")
		if !ok {
			continue
		}
		expected := parseMarker(t, file, index+1, marker)
		expected.file = file
		found = append(found, expected)
	}
	return found
}

func parseMarker(t *testing.T, file string, lineNumber int, marker string) expectation {
	t.Helper()
	// Leading carets walk the claimed line backwards.
	for strings.HasPrefix(marker, "^") {
		lineNumber--
		marker = marker[1:]
	}
	marker = strings.TrimSpace(marker)
	if marker == "" {
		t.Fatalf("%s:%d: an empty //~ marker claims nothing", file, lineNumber)
	}

	found := expectation{line: lineNumber, kind: "message"}
	if rest, ok := strings.CutPrefix(marker, "help:"); ok {
		found.kind, marker = "help", strings.TrimSpace(rest)
	} else if rest, ok := strings.CutPrefix(marker, "note:"); ok {
		found.kind, marker = "note", strings.TrimSpace(rest)
	} else if rest, ok := strings.CutPrefix(marker, "span:"); ok {
		found.kind, marker = "span", strings.TrimSpace(rest)
	}

	if code, rest, ok := strings.Cut(marker, " "); ok && isDiagnosticCode(code) {
		found.code, marker = code, strings.TrimSpace(rest)
	}
	found.text = marker
	if found.text == "" {
		t.Fatalf("%s:%d: a //~ marker needs text to match", file, lineNumber)
	}
	return found
}

// isDiagnosticCode reports the E0301 / W0701 shape.
func isDiagnosticCode(word string) bool {
	if len(word) != 5 || (word[0] != 'E' && word[0] != 'W') {
		return false
	}
	for _, digit := range word[1:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Running one fixture
// ---------------------------------------------------------------------------

// runFixture checks the program rooted at rootPath. markerFiles are the
// files whose markers speak for it; nil means every file in its directory,
// which is what a multi-module fixture wants — a generic's requirement is
// reported in the body that has it, and that body may be a module away.
func runFixture(t *testing.T, rootPath string, markerFiles []string) {
	t.Helper()

	if markerFiles == nil {
		markerFiles = fixtureFiles(t, filepath.Dir(rootPath))
	}
	expected := collectExpectations(t, markerFiles)

	var bag diag.Bag
	program, err := loader.Load(rootPath, &bag)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if bag.HasErrors() {
		t.Fatalf("the fixture does not parse cleanly: %v", bag.Items())
	}
	CheckProgram(program, &bag)

	for _, item := range bag.Items() {
		if !claim(expected, item) {
			t.Errorf("unclaimed diagnostic: %s", item)
		}
	}
	for _, want := range expected {
		if !want.met {
			t.Errorf("%s:%d: nothing matched the %s marker %q", want.file, want.line, want.kind, want.text)
		}
	}
}

func fixtureFiles(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.gyro"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func collectExpectations(t *testing.T, paths []string) []expectation {
	t.Helper()
	var found []expectation
	for _, path := range paths {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		found = append(found, parseExpectations(t, filepath.Base(path), string(source))...)
	}
	return found
}

// claim marks every marker on the diagnostic's line that it satisfies, and
// reports whether it satisfied any. A diagnostic with a note and two help
// lines is one diagnostic and up to three markers.
func claim(expected []expectation, item diag.Diagnostic) bool {
	claimed := false
	for index := range expected {
		want := &expected[index]
		if want.line != item.Pos.Line || want.file != filepath.Base(item.Pos.File) {
			continue
		}
		if !satisfies(*want, item) {
			continue
		}
		want.met = true
		claimed = true
	}
	return claimed
}

func satisfies(want expectation, item diag.Diagnostic) bool {
	if want.code != "" && string(item.Code) != want.code {
		return false
	}
	switch want.kind {
	case "help":
		return containsText(item.Help, want.text)
	case "note":
		return containsText(item.Notes, want.text)
	case "span":
		return matchesSpan(item.Related, want.text)
	}
	return strings.Contains(item.Message, want.text)
}

// matchesSpan claims a related frame by what it says or by where it points.
// The location is worth asserting on its own: a frame that resolves against
// the wrong file still carries the right label.
func matchesSpan(spans []diag.Span, want string) bool {
	for _, span := range spans {
		if strings.Contains(span.Label, want) || strings.Contains(span.Pos.String(), want) {
			return true
		}
	}
	return false
}

func containsText(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}
