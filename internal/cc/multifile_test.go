package cc

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Several .c files as one program.
//
// C files never include one another: a header declares, a .c defines,
// and nothing in the source says which .c defines what. The list of
// files is the build system's in C, and is recovered here from the entry
// file's unmet names — so these cases are mostly about what gets pulled
// in, what does not, and what two files are allowed to disagree about.

// program builds an Options whose Read and List serve a fixed set of
// files, so a case is written inline rather than on disk. Names are
// flat: `case.c` is always the entry.
func program(files map[string]string) *Options {
	return &Options{
		Read: func(path string) (string, error) {
			if text, ok := files[filepath.Base(path)]; ok {
				return text, nil
			}
			return "", fmt.Errorf("no such file: %s", path)
		},
		List: func(dir string) ([]string, error) {
			var out []string
			for name := range files {
				if filepath.Ext(name) == ".c" {
					out = append(out, name)
				}
			}
			return out, nil
		},
	}
}

func TestCallsAcrossFiles(t *testing.T) {
	files := map[string]string{
		"case.c":  `int other(int x); int result(void) { return other(40) + 2; }`,
		"other.c": `int other(int x) { return x; }`,
	}
	answersOpt(t, program(files), files["case.c"], 42)
}

// The set grows until nothing new is needed: case.c needs mid, mid needs
// deep, and deep is two links from the file that was named.
func TestPullsInTransitively(t *testing.T) {
	files := map[string]string{
		"case.c": `int mid(int x); int result(void) { return mid(20); }`,
		"mid.c":  `int deep(int x); int mid(int x) { return deep(x) + 2; }`,
		"deep.c": `int deep(int x) { return x * 2; }`,
	}
	answersOpt(t, program(files), files["case.c"], 42)
}

// `static` is internal linkage: the name belongs to the file that wrote
// it. Two files may each have one, and neither can see the other's.
func TestStaticsAreFileScoped(t *testing.T) {
	files := map[string]string{
		"case.c": `
int fromOther(void);
static int helper(int x) { return x * 10; }
int result(void) { return helper(2) + fromOther(); }
`,
		"other.c": `
static int helper(int x) { return x + 100; }
int fromOther(void) { return helper(-78); }
`,
	}
	answersOpt(t, program(files), files["case.c"], 42)
}

// Two file-scope statics of one name are two objects, and each file
// reaches its own.
func TestStaticGlobalsAreFileScoped(t *testing.T) {
	files := map[string]string{
		"case.c": `
int otherCount(void);
static int count = 40;
int result(void) { return count + otherCount(); }
`,
		"other.c": `
static int count = 2;
int otherCount(void) { return count; }
`,
	}
	answersOpt(t, program(files), files["case.c"], 42)
}

// A directory is not a build target. A file nothing needs is never
// lowered, so this one — which the front end would refuse — costs
// nothing to have sitting beside the program.
func TestUnneededSiblingIsNotCompiled(t *testing.T) {
	files := map[string]string{
		"case.c":  `int result(void) { return 42; }`,
		"spare.c": `union overlap { int a; long b; }; int spare(void) { return 0; }`,
	}
	answersOpt(t, program(files), files["case.c"], 42)
}

// The same header in two files declares the same struct twice, and the
// program has one record.
func TestSharedHeaderMakesOneRecord(t *testing.T) {
	files := map[string]string{
		"shape.h": `struct box { int w; int h; };`,
		"case.c": `
#include "shape.h"
int otherSide(void);
int result(void) {
    struct box b;
    b.w = 6;
    b.h = 6;
    return b.w * b.h + otherSide();
}
`,
		"other.c": `
#include "shape.h"
int otherSide(void) {
    struct box b;
    b.w = 2;
    b.h = 3;
    return b.w * b.h;
}
`,
	}
	module, err := Compile("case.c", files["case.c"], program(files))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	seen := 0
	for _, decl := range module.Types {
		if decl.Name == "box" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("%%box is declared %d times, want 1; the module declares %v",
			seen, typeNames(module.Types))
	}
	answersOpt(t, program(files), files["case.c"], 42)
}

// The check a C linker does not have. It matches an undefined symbol
// against a defined one by name and never compares the two, so a
// prototype that disagrees with the definition links and then
// misbehaves. Two declarations of one name have to agree to get here.
func TestRefusesDisagreeingDeclarations(t *testing.T) {
	files := map[string]string{
		"case.c":  `long other(long x); int result(void) { return (int)other(42); }`,
		"other.c": `long other(int x) { return x; }`,
	}
	refusedOpt(t, program(files), files["case.c"], "two different signatures")
}

func TestRefusesTwoDefinitionsOfOneName(t *testing.T) {
	files := map[string]string{
		"case.c":   `int shared(void); int result(void) { return shared(); }`,
		"one.c":    `int shared(void) { return 1; }`,
		"two.c":    `int shared(void) { return 2; }`,
		"unused.h": ``,
	}
	refusedOpt(t, program(files), files["case.c"], "both define shared")
}

func TestRefusesAStructDeclaredTwoWays(t *testing.T) {
	files := map[string]string{
		"case.c": `
struct box { int w; int h; };
int otherSide(void);
int result(void) { struct box b; b.w = 1; b.h = 2; return b.w + otherSide(); }
`,
		"other.c": `
struct box { int w; long h; };
int otherSide(void) { struct box b; b.w = 1; b.h = 2; return b.w; }
`,
	}
	refusedOpt(t, program(files), files["case.c"], "declared differently")
}

// A name nothing supplies is C's undefined symbol. The front end cannot
// know where it lives, so it becomes an import from the native library
// and the link is where a name nobody has is reported, as C's is.
func TestAnUnresolvedNameIsANativeImport(t *testing.T) {
	files := map[string]string{
		"case.c": `int missing(int x); int result(void) { return missing(1); }`,
	}
	module, err := Compile("case.c", files["case.c"], program(files))
	if err != nil {
		t.Fatal(err)
	}
	if len(module.Imports) != 1 || module.Imports[0].Module != "c:c" ||
		len(module.Imports[0].Funcs) != 1 || module.Imports[0].Funcs[0].Name != "missing" {
		t.Fatalf("the call was not an import of missing from the C library:\n%s", metals.Print(module))
	}
}

// What a native callee reads from registers cannot be built from the
// array a variadic caller here makes, so the call is refused rather than
// made wrongly.
func TestAVariadicNativeCallIsRefused(t *testing.T) {
	files := map[string]string{
		"case.c": `int printf(const char *format, ...); int result(void) { return printf("x"); }`,
	}
	refusedOpt(t, program(files), files["case.c"], "is variadic and defined outside this program")
}
