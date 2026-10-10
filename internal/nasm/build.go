package nasm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/crt"
	"github.com/gyro-lang/gyro/internal/metals"
)

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
	if err := os.WriteFile(runtimePath, []byte(crt.Source), 0o644); err != nil {
		return fmt.Errorf("nasm: %w", err)
	}

	if err := runTool("nasm", "-felf64", asmPath, "-o", objectPath); err != nil {
		return err
	}

	args := []string{"-no-pie", "-O1", objectPath, runtimePath}
	shim, libraries := crt.Shim(module)
	if shim != "" {
		shimPath := filepath.Join(workDir, "ffi.c")
		if err := os.WriteFile(shimPath, []byte(shim), 0o644); err != nil {
			return fmt.Errorf("nasm: %w", err)
		}
		args = append(args, shimPath)
	}

	args = append(args, "-o", output, "-lpthread", "-lm")
	for _, library := range libraries {
		flags, err := linkFlags(library)
		if err != nil {
			return err
		}
		args = append(args, flags...)
	}
	return runTool("cc", args...)
}

// linkFlags is what a native import's library adds to the link line. The
// import names a pkg-config package, so pkg-config is asked for its
// flags; "c" is the C library, which is linked anyway and has no package
// to ask.
func linkFlags(library string) ([]string, error) {
	if library == "c" {
		return nil, nil
	}
	out, err := exec.Command("pkg-config", "--libs", library).Output()
	if err != nil {
		return nil, fmt.Errorf("nasm: pkg-config --libs %s failed: %v", library, err)
	}
	return strings.Fields(string(out)), nil
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
