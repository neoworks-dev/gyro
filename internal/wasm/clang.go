package wasm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/clibc"
	"github.com/gyro-lang/gyro/internal/metals"
)

// Getting from C to wasm is clang's job, not this package's.
//
// The alternative was to link LLVM or to write a C front end first, and
// both miss the point of the bridge: what is being tested is METALS'
// ability to receive a C-shaped program, and the fastest honest way to
// produce one is to ask the compiler that already does it. What comes
// back has had the relooper run on it, which is the single hardest piece
// of a real C front end and the one worth having a reference answer for.

// Toolchain reports what compiling C needs from the system.
func Toolchain() error {
	if _, err := exec.LookPath("clang"); err != nil {
		return fmt.Errorf("wasm: clang is not installed")
	}
	if _, err := exec.LookPath("wasm-ld"); err != nil {
		return fmt.Errorf("wasm: wasm-ld is not installed")
	}
	return nil
}

// CompileC compiles one C file to wasm32 and translates the result.
//
// The link is still freestanding — there is no host libc — but the
// bundled one in internal/clibc is compiled in beside the program, so
// malloc, printf and string.h are there. Underneath it are three
// imports and no more, and a program is free to declare its own with
// __attribute__((import_module("gyro:rt"))) instead: the boundary stays
// visible in the C source rather than hidden in a mapping table.
func CompileC(path string) (*metals.Module, error) {
	if err := Toolchain(); err != nil {
		return nil, err
	}

	workDir, err := os.MkdirTemp("", "gyro-wasm-*")
	if err != nil {
		return nil, fmt.Errorf("wasm: %w", err)
	}
	defer os.RemoveAll(workDir)

	libcFiles, includeDir, err := clibc.Write(workDir)
	if err != nil {
		return nil, fmt.Errorf("wasm: %w", err)
	}

	binaryPath := filepath.Join(workDir, "module.wasm")
	args := append([]string{}, ClangFlags...)
	// -nostdlibinc drops the host's headers, so a program that includes
	// <stdio.h> gets the bundled one and not glibc's — which would
	// compile and then fail to link, with a message about a symbol
	// rather than about the header.
	args = append(args, "-nostdlibinc", "-isystem", includeDir)
	args = append(args, path)
	args = append(args, libcFiles...)
	args = append(args, "-o", binaryPath)

	command := exec.Command("clang", args...)
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("wasm: clang failed: %v\n%s", err, stderr.String())
	}

	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("wasm: %w", err)
	}
	return Compile(path, binary)
}

// ClangFlags is the freestanding wasm32 link this bridge expects.
//
//   - --no-entry, because there is no _start: the METALS entry is
//     synthesized from the exports.
//   - --export-all, because a C function with no export attribute would
//     otherwise be invisible, and the translated module's exports are
//     what a Gyro program will import.
//   - -fno-builtin, because clang otherwise turns a written-out memcpy
//     into a call to a memcpy that a freestanding link does not have.
//   - -O1 rather than -O0, because -O0 leans on the shadow stack for
//     every local and buries what the translation is doing; the shape at
//     -O1 is what a front end would emit anyway.
//   - --allow-undefined, so that an unresolved symbol becomes an import
//     rather than a link error. The bridge then refuses it by name and
//     says how to declare it, which is a better answer than wasm-ld's.
var ClangFlags = []string{
	"--target=wasm32",
	"-O1",
	"-nostdlib",
	"-ffreestanding",
	"-fno-builtin",
	"-Wl,--no-entry",
	"-Wl,--export-all",
	"-Wl,--allow-undefined",
}
