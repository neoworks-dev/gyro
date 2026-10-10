package cc

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
)

// What reading a header means is checked on headers written here, so the
// answers do not depend on which libc the machine has; the last tests read
// the machine's own, because that is the claim the feature makes.

// readOf reads a header that lives in memory.
func readOf(t *testing.T, header string, names ...string) *Header {
	t.Helper()
	opt := &Options{
		Include: []string{"/inc"},
		Read: func(path string) (string, error) {
			if path == "/inc/probe.h" {
				return header, nil
			}
			return "", fmt.Errorf("no such file %s", path)
		},
	}
	h, err := ReadHeader(HeaderRequest{
		Module:  "cc:probe",
		Source:  "#include <probe.h>\n",
		Names:   names,
		Options: opt,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := metals.Verify(h.Module); err != nil {
		t.Fatalf("the module does not verify: %v\n%s", err, metals.Print(h.Module))
	}
	return h
}

func funcSig(t *testing.T, h *Header, name string) string {
	t.Helper()
	for _, fn := range h.Funcs {
		if fn.Name == name {
			return fn.Sig.String()
		}
	}
	t.Fatalf("%s is not among the functions (problems: %v)", name, h.Problems)
	return ""
}

func TestGNUDecorationsAreSkipped(t *testing.T) {
	h := readOf(t, `
#define NORETURN __attribute__((__noreturn__))
extern __inline int twice(int x) { return 2 * x; }
extern int copy(char *__restrict to, const char *restrict from, unsigned short int n)
    __attribute__((__nonnull__(1, 2))) __asm__("copy");
extern __extension__ long long wide(void);
NORETURN void stop(int code);
_Static_assert(sizeof(int) == 4, "int is four bytes");
struct tagged { int a; _Static_assert(1, "inside"); char b; };
extern struct tagged *make(void) __attribute__((malloc));
`, "copy", "wide", "stop", "make", "tagged")

	if got := funcSig(t, h, "copy"); got != "fn(opt<ptr<i8>>, opt<ptr<i8>>, u16) -> i32" {
		t.Errorf("copy is %s", got)
	}
	if got := funcSig(t, h, "wide"); got != "fn() -> i64" {
		t.Errorf("wide is %s", got)
	}
	funcSig(t, h, "stop")
	funcSig(t, h, "make")
	if len(h.Types) != 1 || h.Types[0] != "tagged" {
		t.Errorf("types are %v", h.Types)
	}
	if len(h.Problems) != 0 {
		t.Errorf("problems: %v", h.Problems)
	}
}

func TestAnInlineBodyInAHeaderIsNotCompiled(t *testing.T) {
	h := readOf(t, `static inline int twice(int x) { return x * 2; }
extern inline int thrice(int x) { return x * 3; }`, "twice", "thrice")
	for _, name := range []string{"twice", "thrice"} {
		if !strings.Contains(h.Problems[name], "defined in the header as an inline function") {
			t.Errorf("%s: %q", name, h.Problems[name])
		}
	}
}

func TestLayoutAttributesAreRefusedByName(t *testing.T) {
	h := readOf(t, `struct __attribute__((packed)) wire { char tag; int length; };
struct plain { char tag; int length; };
int use(struct wire *w);
int fine(struct plain *p);`, "wire", "use", "fine")
	if !strings.Contains(h.Problems["wire"], "not declared") {
		t.Errorf("a packed struct is declared: %v %v", h.Problems, h.Types)
	}
	if _, bad := h.Problems["fine"]; bad {
		t.Errorf("fine was refused: %v", h.Problems)
	}
	// use() names the packed struct, whose declaration was dropped, so the
	// prototype cannot be read either.
	if _, dropped := h.Problems["use"]; !dropped {
		funcSig(t, h, "use")
	}
}

func TestADeclarationItCannotReadDoesNotSpoilTheOthers(t *testing.T) {
	h := readOf(t, `
double _Complex z(void);
int before(void);
int __bad_thing = ;
int after(void);
`, "before", "after", "z")
	funcSig(t, h, "before")
	funcSig(t, h, "after")
	if len(h.Skipped) == 0 {
		t.Error("nothing was reported as skipped")
	}
	if !strings.Contains(h.Problems["z"], "not declared") {
		t.Errorf("z: %q", h.Problems["z"])
	}
}

func TestWhatANameMayBe(t *testing.T) {
	h := readOf(t, `
#include <stddef.h>
#include <stdarg.h>
int takes(size_t n, long double x);
int ok(size_t n, void *p);
int variadic(const char *fmt, ...);
int renamed(int) __asm__("other");
int vlist(const char *fmt, __gnuc_va_list ap);
extern int counter;
int value = 3;
typedef unsigned int flags_t;
typedef struct { int x, y; } point_t;
typedef struct node { struct node *next; } node_t;
#define NAME "text"
#define CALL(x) (x)
#define PI 3.14
`, "takes", "ok", "variadic", "renamed", "vlist", "counter", "value", "flags_t", "point_t", "node_t", "NAME", "CALL", "PI")

	if got := funcSig(t, h, "ok"); got != "fn(usize, opt<anyptr>) -> i32" {
		t.Errorf("size_t in a header is usize: %s", got)
	}
	for name, fragment := range map[string]string{
		"takes":    "long double",
		"variadic": "is variadic",
		"renamed":  `assembler label "other"`,
		"vlist":    "va_list",
		"counter":  "variable the library owns",
		"value":    "is a variable",
		"flags_t":  "typedef for unsigned int",
		"node_t":   "imported under that tag",
		"NAME":     "not an integer constant",
		"CALL":     "function-like macro",
		"PI":       "not an integer constant",
	} {
		if !strings.Contains(h.Problems[name], fragment) {
			t.Errorf("%s: want a problem containing %q, got %q", name, fragment, h.Problems[name])
		}
	}
	found := false
	for _, tag := range h.Types {
		found = found || tag == "point_t"
	}
	if !found {
		t.Errorf("point_t is a typedef for an anonymous struct and is that struct's name: %v %v", h.Types, h.Problems)
	}
}

func TestMacroConstantsKeepTheTypeCGivesThem(t *testing.T) {
	h := readOf(t, `
#define MASK 0x2FFF0000u
#define BIT (1ULL << 40)
#define NEG (-1)
#define BOTH (MASK | 5)
#define BIG 0x80000000
#define SMALL (unsigned char)300
#define PICK (1 ? 7 : 8L)
#define AREA (3 * sizeof(int))
enum { FIRST = 4, SECOND };
#define LATER (SECOND + 1)
`, "MASK", "BIT", "NEG", "BOTH", "BIG", "SMALL", "PICK", "AREA", "FIRST", "SECOND", "LATER")

	want := map[string]string{
		"MASK": "u32 805240832", "BIT": "u64 1099511627776", "NEG": "i32 -1",
		"BOTH": "u32 805240837", "BIG": "u32 2147483648", "SMALL": "u8 44",
		"PICK": "i64 7", "AREA": "u64 12", "FIRST": "i32 4", "SECOND": "i32 5",
		"LATER": "i32 6",
	}
	got := map[string]string{}
	for _, c := range h.Consts {
		got[c.Name] = fmt.Sprintf("%s %d", c.Type, c.Value)
		if c.Unsigned {
			got[c.Name] = fmt.Sprintf("%s %d", c.Type, uint64(c.Value))
		}
	}
	for name, text := range want {
		if got[name] != text {
			t.Errorf("%s is %q, want %q", name, got[name], text)
		}
	}
	if len(h.Problems) != 0 {
		t.Errorf("problems: %v", h.Problems)
	}
}

// A struct reached by a prototype is declared in the module even though
// the request never named it, and one that nothing reaches is not.
func TestOnlyWhatARequestReachesIsDeclared(t *testing.T) {
	h := readOf(t, `
struct inner { int a; };
struct outer { struct inner in; struct inner *link; };
struct unused { double d; };
struct opaque;
int take(struct outer *o, struct opaque *q);
`, "take")
	var names []string
	for _, decl := range h.Module.Types {
		names = append(names, decl.Name)
	}
	if strings.Join(names, " ") != "inner outer opaque" {
		t.Errorf("declared %v", names)
	}
}

func TestHeaderFunctionsAreImportsOfTheNamedLibrary(t *testing.T) {
	opt := &Options{
		Include: []string{"/inc"},
		Package: "gfx",
		Read: func(path string) (string, error) {
			return "int draw(int x);\n", nil
		},
	}
	h, err := ReadHeader(HeaderRequest{Module: "cc:gfx", Source: "#include <g.h>\n", Names: []string{"draw"}, Options: opt})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Module.Imports) != 1 || h.Module.Imports[0].Module != "c:gfx" {
		t.Fatalf("draw is not an import from the package:\n%s", metals.Print(h.Module))
	}
	if len(h.Module.Funcs) != 0 {
		t.Error("a header module defines no function of its own")
	}
}

func TestNothingBundledShadowsAQuotedInclude(t *testing.T) {
	// The compiler's own headers answer an angled include only; a quoted
	// one beside the program is the program's.
	opt := &Options{Read: func(path string) (string, error) {
		switch path {
		case "/work/stdbool.h":
			return "int mine(void);\n", nil
		case "/work/case.h":
			return "#include \"stdbool.h\"\n", nil
		}
		return "", fmt.Errorf("no such file %s", path)
	}}
	h, err := ReadHeader(HeaderRequest{Module: "cc:t", Source: "#include \"/work/case.h\"\n", Names: []string{"mine", "bool"}, Options: opt})
	if err != nil {
		t.Fatal(err)
	}
	funcSig(t, h, "mine")
}

func TestStddefAnswersEachNeedAlone(t *testing.T) {
	// glibc asks for one name at a time. size_t arrives and wchar_t does not.
	h := readOf(t, `
#define __need_size_t
#include <stddef.h>
size_t measure(void);
wchar_t letter(void);
`, "measure", "letter")
	funcSig(t, h, "measure")
	if _, wrong := h.Problems["letter"]; wrong {
		return
	}
	t.Error("wchar_t was defined by a request for size_t")
}

func TestTheSystemCompilersOwnHeadersAreNotSearched(t *testing.T) {
	for _, dir := range []string{
		"/usr/lib/gcc/x86_64-pc-linux-gnu/16/include",
		"/usr/lib/clang/19/include",
		"/usr/lib64/gcc/x86_64-suse-linux/13/include",
	} {
		if !ownsHeaders(dir) {
			t.Errorf("%s is the compiler's", dir)
		}
	}
	for _, dir := range []string{"/usr/include", "/usr/local/include", "/usr/include/x86_64-linux-gnu"} {
		if ownsHeaders(dir) {
			t.Errorf("%s is the system's", dir)
		}
	}
}

// ---------------------------------------------------------------------------
// The machine's own headers
// ---------------------------------------------------------------------------

// systemOptions is a compilation against the machine's own headers, or a
// skipped test on a machine that has none to read.
func systemOptions(t *testing.T) *Options {
	t.Helper()
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("skipping: no C compiler to ask where the system headers are")
	}
	dirs, err := SystemInclude()
	if err != nil {
		t.Skipf("skipping: %v", err)
	}
	return &Options{Include: dirs}
}

func TestReadsTheSystemHeaders(t *testing.T) {
	opt := systemOptions(t)
	for _, header := range []string{
		"stdio.h", "string.h", "stdlib.h", "math.h", "stdint.h", "limits.h", "errno.h",
		"time.h", "ctype.h", "unistd.h", "pthread.h", "signal.h", "sys/stat.h", "dirent.h",
		"wchar.h", "fcntl.h", "inttypes.h", "setjmp.h", "assert.h", "locale.h",
	} {
		t.Run(header, func(t *testing.T) {
			toks, err := preprocess("probe.c", "#include <"+header+">\n", opt)
			if err != nil {
				t.Fatal(err)
			}
			u, err := parse("probe.c", toks, true)
			if err != nil {
				t.Fatal(err)
			}
			// A header's own declarations all read: what a tolerant parse
			// drops is for a header this front end is not meant to read.
			for _, s := range u.skipped {
				t.Errorf("%s:%d: dropped: %s", s.pos.file, s.pos.line, s.reason)
			}
		})
	}
}

func TestSystemHeadersGiveTheirFunctionsToACall(t *testing.T) {
	opt := systemOptions(t)
	h, err := ReadHeader(HeaderRequest{
		Module:  "cc:libc",
		Source:  "#include <math.h>\n#include <string.h>\n#include <stdlib.h>\n",
		Names:   []string{"sqrt", "strlen", "qsort", "div", "div_t", "EXIT_FAILURE", "RAND_MAX", "M_PI"},
		Options: opt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := funcSig(t, h, "sqrt"); got != "fn(f64) -> f64" {
		t.Errorf("sqrt is %s", got)
	}
	if got := funcSig(t, h, "strlen"); got != "fn(opt<ptr<i8>>) -> usize" {
		t.Errorf("strlen is %s", got)
	}
	if got := funcSig(t, h, "qsort"); got != "fn(opt<anyptr>, usize, usize, opt<code(opt<anyptr>, opt<anyptr>) -> i32>)" {
		t.Errorf("qsort is %s", got)
	}
	consts := map[string]int64{}
	for _, c := range h.Consts {
		consts[c.Name] = c.Value
	}
	if consts["EXIT_FAILURE"] != 1 || consts["RAND_MAX"] != 2147483647 {
		t.Errorf("constants: %v", consts)
	}
	if !strings.Contains(h.Problems["M_PI"], "not an integer constant") {
		t.Errorf("M_PI: %q", h.Problems["M_PI"])
	}
}

// ---------------------------------------------------------------------------
// What the lexer and the preprocessor owe a real header
// ---------------------------------------------------------------------------

func TestAPreprocessingNumberOnlyFailsWhereItIsRead(t *testing.T) {
	// `32.h` is one token to C, and a branch that is not taken never asks
	// what number it is.
	if _, err := preprocess("case.c", "#if 0\n# include <gnu/stubs-32.h>\n#endif\nint x;\n", nil); err != nil {
		t.Fatalf("a dead branch was read: %v", err)
	}
	toks, err := preprocess("case.c", "int 32.h;\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parse("case.c", toks, false); err == nil || !strings.Contains(err.Error(), "is not a number") {
		t.Fatalf("got %v", err)
	}
}

func TestAMacroParameterMayBeSpelledLikeAKeyword(t *testing.T) {
	// SDL's SDL_DEFINE_AUDIO_FORMAT(signed, bigendian, flt, size).
	answers(t, "#define PACK(signed, size) ((signed) << 15 | (size))\nint result(void) { return PACK(1, 16) == 32784; }", 1)
}

func TestAStringMayContinueOnTheNextLine(t *testing.T) {
	answers(t, "int result(void) { const char *s = \"ab\\\ncd\"; return s[2] == 'c'; }", 1)
}

func TestAHugeFloatLiteralIsInfinity(t *testing.T) {
	// <math.h> writes HUGE_VAL as 1e10000 when it has no builtin to use.
	if _, err := preprocess("case.c", "double d = 1e10000;\n", nil); err != nil {
		t.Fatal(err)
	}
}

func TestIncludeNextFindsTheHeaderItShadows(t *testing.T) {
	opt := &Options{
		Include: []string{"/first", "/second"},
		Read: func(path string) (string, error) {
			switch path {
			case "/first/wrap.h":
				return "int outer(void);\n#include_next <wrap.h>\n", nil
			case "/second/wrap.h":
				return "int inner(void);\n", nil
			}
			return "", fmt.Errorf("no such file %s", path)
		},
	}
	h, err := ReadHeader(HeaderRequest{Module: "cc:t", Source: "#include <wrap.h>\n", Names: []string{"outer", "inner"}, Options: opt})
	if err != nil {
		t.Fatal(err)
	}
	funcSig(t, h, "outer")
	funcSig(t, h, "inner")
}

func TestAnIncludeBeyondTheSearchPathNamesWhatItLookedIn(t *testing.T) {
	_, err := ReadHeader(HeaderRequest{Module: "cc:t", Source: "#include <missing.h>\n", Options: &Options{Include: []string{"/nowhere"}}})
	if err == nil || !strings.Contains(err.Error(), "cannot find <missing.h>") || !strings.Contains(err.Error(), "/nowhere/missing.h") {
		t.Fatalf("got %v", err)
	}
}
