package cc

import (
	"fmt"
	"strings"
	"testing"
)

// The preprocessor is tested on its output tokens rather than on what
// they compile to. It is a language of its own — one whose whole job is
// to produce C rather than to mean anything — so what it produced is the
// thing to check, and a lowering bug cannot then look like an expansion
// bug.

func expands(t *testing.T, source, want string) {
	t.Helper()
	expandsOpt(t, nil, source, want)
}

func expandsOpt(t *testing.T, opt *Options, source, want string) {
	t.Helper()
	toks, err := preprocess("case.c", source, opt)
	if err != nil {
		t.Fatalf("preprocess: %v", err)
	}
	if got := spelled(dropEOF(toks)); got != want {
		t.Errorf("expanded to\n  %s\nwant\n  %s", got, want)
	}
}

func rejects(t *testing.T, source, fragment string) {
	t.Helper()
	rejectsOpt(t, nil, source, fragment)
}

func rejectsOpt(t *testing.T, opt *Options, source, fragment string) {
	t.Helper()
	toks, err := preprocess("case.c", source, opt)
	if err == nil {
		t.Fatalf("expected %q, it expanded to %s", fragment, spelled(dropEOF(toks)))
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("expected an error containing %q, got %v", fragment, err)
	}
}

// spelled joins tokens with one space each. The real spacing carries the
// source's, which is what `#` stringizes; a test wants the token
// sequence, so it is normalized here.
func spelled(toks []token) string {
	parts := make([]string, len(toks))
	for i, t := range toks {
		parts[i] = t.text
	}
	return strings.Join(parts, " ")
}

// headers builds an Options whose includes come out of a map instead of
// the filesystem, so a test can describe a header tree in one literal.
func headers(files map[string]string, dirs ...string) *Options {
	return &Options{
		Include: dirs,
		Read: func(path string) (string, error) {
			text, ok := files[path]
			if !ok {
				return "", fmt.Errorf("no such file")
			}
			return text, nil
		},
	}
}

// ---------------------------------------------------------------------------
// Macros
// ---------------------------------------------------------------------------

func TestObjectMacros(t *testing.T) {
	expands(t, "#define N 3\nint x = N;", "int x = 3 ;")
	expands(t, "#define A B\n#define B 7\nint x = A;", "int x = 7 ;")
	expands(t, "#define EMPTY\nint EMPTY x;", "int x ;")

	// A macro that names itself stops after one round. Without a hideset
	// this is the expansion that never returns.
	expands(t, "#define N N + 1\nint x = N;", "int x = N + 1 ;")
	expands(t, "#define A B\n#define B A\nint x = A;", "int x = A ;")

	expands(t, "#define N 3\n#undef N\nint x = N;", "int x = N ;")
}

func TestFunctionMacros(t *testing.T) {
	expands(t, "#define ADD(a, b) ((a) + (b))\nint x = ADD(1, 2);",
		"int x = ( ( 1 ) + ( 2 ) ) ;")

	// The name of a function-like macro with no parenthesis after it is
	// an ordinary identifier, which is what lets a program have a
	// variable called min beside a macro called min.
	expands(t, "#define min(a, b) 0\nint min = 1;", "int min = 1 ;")

	// An argument is expanded before it is substituted, unless it is
	// next to a # or a ##.
	expands(t, "#define N 3\n#define ID(x) x\nint y = ID(N);", "int y = 3 ;")

	// Nesting, and an argument that contains commas inside parentheses.
	expands(t, "#define ADD(a, b) ((a) + (b))\nint x = ADD(ADD(1, 2), 3);",
		"int x = ( ( ( ( 1 ) + ( 2 ) ) ) + ( 3 ) ) ;")

	expands(t, "#define f(x) x\nint y = f();", "int y = ;")

	// The recursion this is famous for: f expands to a call of f, and the
	// inner one is hidden.
	expands(t, "#define f(x) f(x)\nint y = f(1);", "int y = f ( 1 ) ;")

	rejects(t, "#define f(a, b) a\nint y = f(1);", "takes 2 argument(s)")
	rejects(t, "#define f(a) a\nint y = f(1;", "never closed")
}

func TestStringizeAndPaste(t *testing.T) {
	expands(t, "#define S(x) #x\nchar *s = S(a  b);", `char * s = "a b" ;`)

	// # takes the argument as written, before expansion. That is the
	// difference the two-level STR idiom exists to work around.
	expands(t, "#define N 3\n#define S(x) #x\nchar *s = S(N);", `char * s = "N" ;`)
	expands(t, "#define N 3\n#define S_(x) #x\n#define S(x) S_(x)\nchar *s = S(N);",
		`char * s = "3" ;`)

	expands(t, "#define CAT(a, b) a##b\nint CAT(x, y) = 1;", "int xy = 1 ;")
	expands(t, "#define CAT(a, b) a##b\nint x = CAT(1, 2);", "int x = 12 ;")
	expands(t, "#define CAT(a, b) a##b\nint x = CAT(, 2);", "int x = 2 ;")

	rejects(t, "#define BAD(a) ## a\nint x = BAD(1);", "## joins two tokens")
}

func TestVariadicMacros(t *testing.T) {
	expands(t, "#define f(a, ...) g(a, __VA_ARGS__)\nint x = f(1, 2, 3);",
		"int x = g ( 1 , 2 , 3 ) ;")

	// GNU's comma swallow, which enough real headers write that refusing
	// it would refuse them.
	expands(t, "#define f(a, ...) g(a , ## __VA_ARGS__)\nint x = f(1);",
		"int x = g ( 1 ) ;")
	expands(t, "#define f(a, ...) g(a , ## __VA_ARGS__)\nint x = f(1, 2);",
		"int x = g ( 1 , 2 ) ;")
}

func TestPredefinedMacros(t *testing.T) {
	expands(t, "int line = __LINE__;\nint next = __LINE__;", "int line = 1 ; int next = 2 ;")
	expands(t, "char *f = __FILE__;", `char * f = "case.c" ;`)
	expands(t, "#ifdef __linux__\nint linux = 1;\n#endif", "int linux = 1 ;")

	// -D, spelled the way a command line spells it.
	expandsOpt(t, &Options{Define: []string{"FEATURE", "SIZE=8"}},
		"int a = FEATURE; int b = SIZE;", "int a = 1 ; int b = 8 ;")
}

// ---------------------------------------------------------------------------
// Conditionals
// ---------------------------------------------------------------------------

func TestConditionals(t *testing.T) {
	expands(t, "#if 1\nint a;\n#else\nint b;\n#endif", "int a ;")
	expands(t, "#if 0\nint a;\n#else\nint b;\n#endif", "int b ;")
	expands(t, "#if 0\nint a;\n#elif 1\nint b;\n#else\nint c;\n#endif", "int b ;")

	// Only the first true branch runs, even when a later one is also
	// true.
	expands(t, "#if 1\nint a;\n#elif 1\nint b;\n#endif", "int a ;")

	expands(t, "#define N 3\n#if N > 2 && N < 10\nint a;\n#endif", "int a ;")
	expands(t, "#if defined(NOPE)\nint a;\n#else\nint b;\n#endif", "int b ;")
	expands(t, "#define N 1\n#if defined N\nint a;\n#endif", "int a ;")

	// An undefined name is zero, which is what makes a feature test work
	// on a build where nobody set the feature.
	expands(t, "#if FEATURE\nint a;\n#else\nint b;\n#endif", "int b ;")

	// Signedness at #if's one width: -1 is bigger than 0u.
	expands(t, "#if -1 < 0\nint a;\n#endif", "int a ;")
	expands(t, "#if -1 < 0u\nint a;\n#else\nint b;\n#endif", "int b ;")

	// The right side of a && the left side already decided is read and
	// dropped, which is what `#if defined(N) && N > 3` counts on.
	expands(t, "#if defined(N) && 100 / N > 3\nint a;\n#else\nint b;\n#endif", "int b ;")
	expands(t, "#if 1 ? 2 : (1/0)\nint a;\n#endif", "int a ;")
	expands(t, "#if (2 + 3) * 4 == 20\nint a;\n#endif", "int a ;")

	// A conditional inside a dead branch is dead whatever it says, so its
	// expression is never evaluated — which is what lets a guard protect
	// an expression that would not survive being read.
	expands(t, "#if 0\n#if 1/0\nint a;\n#endif\n#endif\nint b;", "int b ;")

	rejects(t, "#if 1\nint a;", "never closed")
	rejects(t, "#endif", "without a matching #if")
	rejects(t, "#if 1\n#else\n#else\n#endif", "after this conditional's #else")
	rejects(t, "#if 1/0\n#endif", "divides by zero")
	rejects(t, "#error this build is not supported", "#error this build is not supported")
}

func TestNestedConditionals(t *testing.T) {
	expands(t, `
#define OUTER 1
#if OUTER
#  if 0
int a;
#  else
int b;
#  endif
#else
int c;
#endif
`, "int b ;")
}

// ---------------------------------------------------------------------------
// #include
// ---------------------------------------------------------------------------

func TestIncludeQuotedAndAngled(t *testing.T) {
	opt := headers(map[string]string{
		"util.h":       "#define TWICE(x) ((x) * 2)\n",
		"inc/limits.h": "#define MAXVAL 127\n",
	}, "inc")

	expandsOpt(t, opt, "#include \"util.h\"\nint x = TWICE(3);", "int x = ( ( 3 ) * 2 ) ;")
	expandsOpt(t, opt, "#include <limits.h>\nint x = MAXVAL;", "int x = 127 ;")

	// The name may come out of a macro, which is how a build picks a
	// platform header.
	expandsOpt(t, opt, "#define HEADER \"util.h\"\n#include HEADER\nint x = TWICE(1);",
		"int x = ( ( 1 ) * 2 ) ;")

	rejectsOpt(t, opt, "#include \"nope.h\"", "cannot find \"nope.h\"")
	rejectsOpt(t, opt, "#include <nope.h>", "cannot find <nope.h>")
}

func TestIncludeGuardsAndPragmaOnce(t *testing.T) {
	guarded := headers(map[string]string{
		"a.h": "#ifndef A_H\n#define A_H\nint a;\n#endif\n",
	})
	expandsOpt(t, guarded, "#include \"a.h\"\n#include \"a.h\"", "int a ;")

	once := headers(map[string]string{
		"b.h": "#pragma once\nint b;\n",
	})
	expandsOpt(t, once, "#include \"b.h\"\n#include \"b.h\"", "int b ;")

	// A header with no guard that includes itself is the runaway the
	// counter is there to stop.
	loop := headers(map[string]string{"c.h": "#include \"c.h\"\n"})
	rejectsOpt(t, loop, "#include \"c.h\"", "including itself without a guard")
}

// A header included inside a conditional obeys it, which is the whole
// reason its tokens go back into the stream instead of through a
// separate run.
func TestIncludeInsideADeadBranch(t *testing.T) {
	opt := headers(map[string]string{"boom.h": "#error this header should not be read\n"})
	expandsOpt(t, opt, "#if 0\n#include \"boom.h\"\n#endif\nint a;", "int a ;")
}

// ---------------------------------------------------------------------------
// End to end
// ---------------------------------------------------------------------------

// A `#` that a macro produced is not a directive, and a macro may span
// lines. Both are easy to get wrong and neither shows up in a token
// count, so this one runs the program.
func TestPreprocessedProgramRuns(t *testing.T) {
	answers(t, `
#define SQUARE(x) ((x) * (x))
#define MAX(a, b) ((a) > (b) ? (a) : (b))
#define BIG \
    100

int result(void) {
    return MAX(SQUARE(3), 4) + BIG;
}
`, 109)
}

func TestProgramAcrossHeaders(t *testing.T) {
	opt := headers(map[string]string{
		"math.h": `#ifndef MATH_H
#define MATH_H
#define ABS(x) ((x) < 0 ? -(x) : (x))
static int scale(int v) { return v * 10; }
#endif
`,
	})
	answersOpt(t, opt, `
#include "math.h"
#include "math.h"

int result(void) {
    return scale(ABS(-4)) + 2;
}
`, 42)
}
