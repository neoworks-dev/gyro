package cgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/crt"
	"github.com/gyro-lang/gyro/internal/metals"
)

// Toolchain reports what a C build needs from the system. There is no
// assembler in this path, so a C compiler is the whole list.
func Toolchain() error {
	if _, err := exec.LookPath("cc"); err != nil {
		return fmt.Errorf("cgen: cc is not installed")
	}
	return nil
}

// Build compiles a verified module into an executable: generate the C,
// write it next to the runtime, and hand both to the C compiler in one
// invocation.
//
// -fno-strict-aliasing is not optional. Aggregates move through void
// pointers all over the generated code — array elements, channel
// payloads, future cells — and the type-based aliasing rules would let
// the optimizer assume those accesses cannot touch the same storage.
func Build(module *metals.Module, output string) error {
	source, err := Generate(module)
	if err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", "gyro-cbuild-*")
	if err != nil {
		return fmt.Errorf("cgen: %w", err)
	}
	defer os.RemoveAll(workDir)

	programPath := filepath.Join(workDir, "program.c")
	runtimePath := filepath.Join(workDir, "rt.c")
	if err := os.WriteFile(programPath, []byte(source), 0o644); err != nil {
		return fmt.Errorf("cgen: %w", err)
	}
	if err := os.WriteFile(runtimePath, []byte(crt.Source), 0o644); err != nil {
		return fmt.Errorf("cgen: %w", err)
	}

	return runTool("cc", "-O1", "-fno-strict-aliasing", programPath, runtimePath,
		"-o", output, "-lpthread", "-lm")
}

func runTool(name string, args ...string) error {
	command := exec.Command(name, args...)
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("cgen: %s failed: %v\n%s", name, err, stderr.String())
	}
	return nil
}
