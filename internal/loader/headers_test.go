package loader_test

import (
	"os/exec"
	"strings"
	"testing"
)

// A header import reads the system's headers, so these need a C compiler
// to say where they are.
func needCompiler(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("skipping: no C compiler to ask where the system headers are")
	}
}

func TestAHeaderImportIsOneModulePerLibrary(t *testing.T) {
	needCompiler(t)
	program, bag := load(t, map[string]string{
		"main.gyro": `
import safe { sqrt } from "<math.h>"
import safe { div, div_t } from "<stdlib.h>"
import safe { strlen } from "<string.h>"

fn main() {
}
`,
	}, "main.gyro")
	if bag.HasErrors() {
		t.Fatalf("unexpected errors: %v", bag.Items())
	}

	headers := 0
	for _, module := range program.Modules {
		if module.Header {
			headers++
		}
	}
	if headers != 1 {
		t.Fatalf("three headers of the C library are %d modules, want the one", headers)
	}
	root := program.Root
	if root.Deps["<math.h>"] != root.Deps["<stdlib.h>"] || root.Deps["<stdlib.h>"] != root.Deps["<string.h>"] {
		t.Error("the specifiers of one library resolve to different modules")
	}
}

func TestAHeaderOfAnUnknownPackageIsReportedAtTheImport(t *testing.T) {
	needCompiler(t)
	if _, err := exec.LookPath("pkg-config"); err != nil {
		t.Skip("skipping: no pkg-config")
	}
	_, bag := load(t, map[string]string{
		"main.gyro": `
import safe { thing } from "pkg:no-such-package/thing.h"

fn main() {
}
`,
	}, "main.gyro")
	wantError(t, bag, "pkg-config does not know package no-such-package")
}

func TestAPackageSpecifierNeedsAPathInsideThePackage(t *testing.T) {
	_, bag := load(t, map[string]string{
		"main.gyro": `
import safe { thing } from "pkg:sdl3"

fn main() {
}
`,
	}, "main.gyro")
	if !strings.Contains(bag.Items()[0].Message, "does not name a header inside a package") {
		t.Fatalf("got %v", bag.Items())
	}
}
