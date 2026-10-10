package nasm_test

import (
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/nasm"
)

// The line table a debugger reads starts as NASM's %line directive: it
// credits the assembly after it to a source line. The positions come from
// the module's loc instructions, which a hand-written module can carry
// just as lowering does.
func TestLocBecomesALineDirective(t *testing.T) {
	module, err := metals.Parse(`module "metals:debug"

export fn @main() {
  loc "/src/prog.gyro" 3
  %a = const.i64 1
  loc "/src/prog.gyro" 7
  %b = iadd %a, %a
  loc "/src/prog.gyro" 7
  return
}
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := metals.Verify(module); err != nil {
		t.Fatal(err)
	}
	asm, err := nasm.Generate(module)
	if err != nil {
		t.Fatal(err)
	}

	first := strings.Index(asm, "%line 3+0 \"/src/prog.gyro\"\n")
	second := strings.Index(asm, "%line 7+0 \"/src/prog.gyro\"\n")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("the assembly does not map lines 3 then 7:\n%s", asm)
	}
	if strings.Count(asm, "%line 7+0") != 1 {
		t.Errorf("a repeated position should be written once:\n%s", asm)
	}
	if !strings.Contains(asm, "global gy_user_main:function") {
		t.Errorf("a function should be a sized symbol so a debugger can name its frames:\n%s", asm)
	}
}

// A module with no positions produces no line directives at all.
func TestNoLocNoLineDirective(t *testing.T) {
	module, err := metals.Parse(`module "metals:plain"

export fn @main() {
  return
}
`)
	if err != nil {
		t.Fatal(err)
	}
	asm, err := nasm.Generate(module)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(asm, "%line") {
		t.Errorf("unexpected line directive:\n%s", asm)
	}
}
