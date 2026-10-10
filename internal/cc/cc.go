// Package cc compiles C into METALS directly, without going through a
// sandbox.
//
// A bridge came first, asking clang for wasm32 and translating that. It
// proved METALS can receive a C-shaped program, and it lost the thing the
// program was written with: by the time a struct reaches wasm it is an
// offset into a flat region, and a `Point *` is a `u32`. Nothing about
// that can be imported into Gyro as the type it started as, so the bridge
// was reconnaissance for this and is deleted.
//
// This front end reads C itself and emits METALS' own types. A C
// struct becomes a record, a `T *` becomes `ptr<T>`, a tag nothing
// defines becomes an opaque declaration, and the storage under all of
// them is C's already — that is the layout rule the native backends have
// kept from the beginning. Gyro and C end up as one linked
// module over one heap, which is why a pointer may cross between them:
// a boundary is between runtimes, and there is only one runtime here.
// See docs/decisions.md, "Values copy at runtime boundaries".
//
// A program here is as many files as it takes. C has no way to say which
// `.c` defines a symbol, so the set is found rather than declared:
// resolve.go starts at the file Gyro imported and follows what it needs.
// They lower into one module, because a linkage unit is what a METALS
// module is.
//
// What it does not do yet is listed in docs/appendix-d-metals.md rather than
// apologized for here. What is left of goto — a jump into a block, and a
// backward jump reaching past a later forward label — is where a relooper
// would have to be written; the bridge got that from LLVM for free.
package cc

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Compile turns a C program into a METALS module. path names the entry
// file; the rest of the program is whatever that file needs and does not
// define, found among the `.c` files beside it — see resolve.go. opt may
// be nil, which is a compilation with no header search path and nothing
// defined ahead of the first line.
func Compile(path, source string, opt *Options) (*metals.Module, error) {
	units, err := gather(path, source, opt)
	if err != nil {
		return nil, err
	}
	return lower(ModuleName(path), units, opt.library())
}

// ModuleName is the METALS name of the module a C file becomes. The
// scheme is the front end's own: `c:` already means a native library
// nobody compiled, and the difference between the two is the whole
// argument for this package.
func ModuleName(path string) string {
	base := filepath.Base(path)
	return "cc:" + strings.TrimSuffix(base, filepath.Ext(base))
}

// errorAt is every diagnostic this package produces. C's own compilers
// spell a position this way and so does the rest of Gyro, so an editor
// jumps to it either way.
func errorAt(pos position, format string, args ...any) error {
	return fmt.Errorf("%s:%d:%d: %s", pos.file, pos.line, pos.col,
		fmt.Sprintf(format, args...))
}
