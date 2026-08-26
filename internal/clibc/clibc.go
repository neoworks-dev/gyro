// Package clibc is the C standard library the wasm bridge compiles in.
//
// It exists because the bridge links freestanding: clang can compile all
// of C, but a C program that calls malloc or printf has nothing to call.
// The alternative was to adopt wasi-libc and implement the WASI syscall
// floor, which is more library for less work — and it was rejected for
// one reason: it needs a wasi sysroot installed, which almost nobody
// has, so the corpus case that proves it works would skip on almost
// every machine. A libc bundled into the compiler is testable everywhere
// clang is. wasi-libc stays possible later; the bridge refuses an
// unknown import module by name rather than assuming there is only one.
//
// The floor underneath is three imports and no more: `write_byte`,
// `read_byte` and `exit`, all taking scalars. It cannot be `fd_write`,
// because that takes a pointer into the guest's memory and a linear
// address is a u32 — handing one to the runtime would make it a pointer
// at the boundary, which is exactly what the sandbox rests on not doing.
// So stdio buffers on the guest's side and drains a byte at a time. That
// is a call per byte of output, which is the price of the boundary being
// real; it is paid on i/o volume, not on formatting work.
//
// What is here is what a C program is likely to reach for, not all of
// C99: string.h, the malloc family over `memory.grow`, the integer and
// string parts of printf, the numeric parsers, qsort/bsearch, ctype and
// the math.h names that are METALS instructions underneath. What is not
// here is listed in the package's README section of docs/appendix-d-metals.md, and
// the largest single gap is printf's floating-point conversions.
package clibc

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed src/*.c src/include/*.h
var sources embed.FS

// Write lays the bundled sources into dir and returns the .c files to
// compile and the include directory to search. The bridge hands both to
// clang alongside the user's own file.
func Write(dir string) (files []string, include string, err error) {
	include = filepath.Join(dir, "include")
	if err := os.MkdirAll(include, 0o755); err != nil {
		return nil, "", err
	}

	err = fs.WalkDir(sources, "src", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := sources.ReadFile(path)
		if err != nil {
			return err
		}

		target := filepath.Join(dir, strings.TrimPrefix(path, "src/"))
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return err
		}
		if strings.HasSuffix(path, ".c") {
			files = append(files, target)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return files, include, nil
}
