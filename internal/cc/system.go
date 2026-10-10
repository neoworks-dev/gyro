package cc

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Where a system's headers are.
//
// The directories `#include <...>` searches are the system compiler's to
// say: a distribution puts libc's headers, the multiarch ones and the
// ones a package installed in places no list in this repository could
// predict. So they are asked for, the way a build system asks, with
// `cc -E -v` on an empty file.
//
// The compiler's own directory is left out. It holds the headers that
// describe the compiler — <stddef.h>, <stdint.h>, <limits.h> — and they
// are written over macros only that compiler predefines. This front end
// supplies those itself (see bundled.go), the way it always has supplied
// <stdarg.h>, so that the target it describes is the one it lays out.

var (
	systemOnce sync.Once
	systemDirs []string
	systemErr  error
)

// SystemInclude is the directory list `#include <...>` searches on this
// machine, in order.
func SystemInclude() ([]string, error) {
	systemOnce.Do(func() {
		systemDirs, systemErr = askCompiler()
	})
	return systemDirs, systemErr
}

func askCompiler() ([]string, error) {
	compiler, err := exec.LookPath("cc")
	if err != nil {
		return nil, fmt.Errorf("reading a system header needs a C compiler to ask where they are, and there is no cc on the path")
	}
	command := exec.Command(compiler, "-x", "c", "-E", "-v", "-")
	command.Stdin = strings.NewReader("")
	out, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s -E -v failed: %v", compiler, err)
	}

	var dirs []string
	listing := false
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "#include <...> search starts here"):
			listing = true
		case strings.HasPrefix(line, "End of search list"):
			listing = false
		case listing && strings.HasPrefix(line, " "):
			dir := filepath.Clean(strings.TrimSpace(line))
			if ownsHeaders(dir) {
				continue
			}
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("%s -E -v listed no include directories", compiler)
	}
	return dirs, nil
}

// ownsHeaders recognizes the directory a compiler keeps for itself:
// <prefix>/lib/gcc/<triple>/<version>/include and the clang equivalent.
func ownsHeaders(dir string) bool {
	slashed := filepath.ToSlash(dir)
	return strings.Contains(slashed, "/lib/gcc/") || strings.Contains(slashed, "/lib/clang/") ||
		strings.Contains(slashed, "/lib64/gcc/")
}
