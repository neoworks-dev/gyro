package nasm

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

//go:embed runtime/rt.c
var runtimeSource string

// Toolchain reports what `gyro build` needs from the system.
func Toolchain() error {
	for _, tool := range []string{"nasm", "cc"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("nasm: %s is not installed", tool)
		}
	}
	return nil
}

// Build compiles a verified module into an executable: generate NASM,
// assemble it, and link it with the embedded C runtime.
func Build(module *metals.Module, output string) error {
	asm, err := Generate(module)
	if err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", "gyro-build-*")
	if err != nil {
		return fmt.Errorf("nasm: %w", err)
	}
	defer os.RemoveAll(workDir)

	asmPath := filepath.Join(workDir, "program.asm")
	objectPath := filepath.Join(workDir, "program.o")
	runtimePath := filepath.Join(workDir, "rt.c")
	if err := os.WriteFile(asmPath, []byte(asm), 0o644); err != nil {
		return fmt.Errorf("nasm: %w", err)
	}
	if err := os.WriteFile(runtimePath, []byte(runtimeSource), 0o644); err != nil {
		return fmt.Errorf("nasm: %w", err)
	}

	if err := runTool("nasm", "-felf64", asmPath, "-o", objectPath); err != nil {
		return err
	}
	return runTool("cc", "-no-pie", "-O1", objectPath, runtimePath, "-o", output, "-lpthread", "-lm")
}

func runTool(name string, args ...string) error {
	command := exec.Command(name, args...)
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("nasm: %s failed: %v\n%s", name, err, stderr.String())
	}
	return nil
}
