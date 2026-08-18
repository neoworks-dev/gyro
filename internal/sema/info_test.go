package sema

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/types"
)

// checkInfo checks src, requiring a clean bag, and returns the recorded
// Info: what sema hands the backends and the language server.
func checkInfo(t *testing.T, src string) *Info {
	t.Helper()

	path := filepath.Join(t.TempDir(), "main.gyro")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	var bag diag.Bag
	program, err := loader.Load(path, &bag)
	if err != nil {
		t.Fatal(err)
	}
	info := CheckProgram(program, &bag)
	for _, item := range bag.Items() {
		t.Errorf("unexpected diagnostic: %s", item)
	}
	return info
}

func TestInfoRecordsCalls(t *testing.T) {
	info := checkInfo(t, "fn add(a: i32, b: i32): i32 {\n  return a + b\n}\n\nfn t(): i32 {\n  return add(1, 2)\n}\n")

	if len(info.Calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(info.Calls))
	}
	for _, resolved := range info.Calls {
		if resolved.Method == nil || resolved.Method.Name != "add" {
			t.Errorf("the call did not resolve to add: %+v", resolved)
		}
		if resolved.Method != nil && resolved.Method.Decl == nil {
			t.Errorf("the resolved method carries no declaration")
		}
		if !types.Identical(resolved.Sig.Result, types.Int32) {
			t.Errorf("the call's result is %s, want i32", resolved.Sig.Result)
		}
	}
	if len(info.Types) == 0 {
		t.Errorf("no expression types were recorded")
	}
	if len(info.Refs) == 0 {
		t.Errorf("no references were recorded")
	}
}

func TestInfoRecordsInstances(t *testing.T) {
	info := checkInfo(t, "fn pick<T>(a: T, b: T): T {\n  if true {\n    return a\n  }\n  return b\n}\n\nfn t() {\n  var a = pick(1, 2)\n  var b = pick(1.5, 2.5)\n}\n")

	keys := map[string]*FuncInstance{}
	for _, byKey := range info.Instances {
		for key, instance := range byKey {
			keys[key] = instance
		}
	}
	if len(keys) != 2 {
		t.Fatalf("recorded %d instances, want 2: %v", len(keys), keys)
	}
	for _, want := range []string{"i32", "f64"} {
		instance, ok := keys[want]
		if !ok {
			t.Errorf("no instance recorded for %s", want)
			continue
		}
		if len(instance.Types) == 0 {
			t.Errorf("the %s instance recorded no expression types", want)
		}
	}
}

func TestInfoRecordsVariantConstruction(t *testing.T) {
	info := checkInfo(t, "enum Shape {\n  Circle(f64),\n  Point,\n}\n\nfn t(): Shape {\n  return Shape::Circle(1.0)\n}\n")

	if len(info.Calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(info.Calls))
	}
	for _, resolved := range info.Calls {
		if resolved.Enum == nil || resolved.Enum.Name != "Shape" {
			t.Errorf("the construction did not resolve to Shape: %+v", resolved)
		}
		if resolved.Variant != 0 {
			t.Errorf("the construction resolved to variant %d, want 0", resolved.Variant)
		}
	}
}
