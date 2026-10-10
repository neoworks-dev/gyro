package cc

import (
	"os/exec"
	"strings"
	"testing"
)

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
	for _, header := range []string{"stdio.h", "string.h", "stdlib.h", "math.h", "stdint.h", "limits.h", "errno.h", "time.h", "ctype.h"} {
		t.Run(header, func(t *testing.T) {
			o := *opt
			o.Declarations = true
			_, err := Compile("probe.c", "#include <"+header+">\nint x;\n", &o)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	_ = strings.Contains
}
