package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A case is either a single file, `run/name.gyro` beside `run/name.out`, or
// a directory for a program that spans modules, `run/name/main.gyro` beside
// `run/name/expected.out`.
const (
	corpusDir     = "testdata/run"
	examplesDir   = "../examples"
	exampleGolden = "testdata/examples"
)

func TestCorpus(t *testing.T) {
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatal(err)
	}

	found := 0
	for _, entry := range entries {
		root, golden, ok := corpusCase(corpusDir, entry.Name(), entry.IsDir())
		if !ok {
			continue
		}
		found++
		t.Run(strings.TrimSuffix(entry.Name(), ".gyro"), func(t *testing.T) {
			t.Parallel()
			runCase(t, root, golden)
		})
	}
	if found == 0 {
		t.Fatalf("no cases found in %s", corpusDir)
	}
}

func corpusCase(dir, name string, isDir bool) (root, golden string, ok bool) {
	if isDir {
		return filepath.Join(dir, name, "main.gyro"), filepath.Join(dir, name, "expected"), true
	}
	if !strings.HasSuffix(name, ".gyro") {
		return "", "", false
	}
	base := strings.TrimSuffix(name, ".gyro")
	return filepath.Join(dir, name), filepath.Join(dir, base), true
}

// TestExamples holds the reference programs to the same standard. Their
// goldens live here rather than beside them, so `examples/` stays a
// directory of Gyro and nothing else.
func TestExamples(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(examplesDir, "*.gyro"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no examples found: %v", err)
	}
	for _, path := range paths {
		base := strings.TrimSuffix(filepath.Base(path), ".gyro")
		t.Run(base, func(t *testing.T) {
			t.Parallel()
			runCase(t, path, filepath.Join(exampleGolden, base))
		})
	}
}
