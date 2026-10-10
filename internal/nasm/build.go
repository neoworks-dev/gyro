package nasm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/crt"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/x86"
)

// Toolchain reports what `gyro build` needs from the system: a C
// compiler, which compiles the runtime and links. The assembler is
// internal/x86.
func Toolchain() error {
	if _, err := exec.LookPath("cc"); err != nil {
		return fmt.Errorf("nasm: cc is not installed")
	}
	return nil
}

// Build compiles a verified module into an executable: generate the
// assembly, assemble it into an object, and link that with the embedded
// C runtime.
func Build(module *metals.Module, output string) error {
	asm, err := Generate(module)
	if err != nil {
		return err
	}
	// The line table credits what no source position covers to the
	// assembly's own lines, under the name `gyro nasm` output would
	// sensibly be saved as; a temporary path would make every build
	// differ.
	object, err := x86.Assemble(asm, filepath.Base(output)+".asm")
	if err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", "gyro-build-*")
	if err != nil {
		return fmt.Errorf("nasm: %w", err)
	}
	defer os.RemoveAll(workDir)

	objectPath := filepath.Join(workDir, "program.o")
	runtimePath := filepath.Join(workDir, "rt.c")
	if err := os.WriteFile(objectPath, object, 0o644); err != nil {
		return fmt.Errorf("nasm: %w", err)
	}
	if err := os.WriteFile(runtimePath, []byte(crt.Source), 0o644); err != nil {
		return fmt.Errorf("nasm: %w", err)
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
