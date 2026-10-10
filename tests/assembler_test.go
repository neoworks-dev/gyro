package tests

import (
	"bytes"
	"debug/dwarf"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/nasm"
	"github.com/gyro-lang/gyro/internal/x86"
)

// TestAssemblerMatchesNasm holds internal/x86 to the assembler it
// replaced. Every program the corpus, the examples and the benchmark
// suite build is generated once and assembled twice, by nasm and by
// x86.Assemble, and the two objects must agree: the bytes of every
// program section, its relocations, its symbols and the rows of the line
// table. nasm is the reference here and nowhere else, so the test skips
// where it is not installed.
//
// Two things nasm writes are deliberately not compared. A local data
// label gets an OBJECT type and a size nasm guesses from the directive
// after it, which no tool reads; and the debug sections hold only what a
// debugger uses — a unit, its address range and the line table — rather
// than nasm's empty .debug_frame, .debug_loc and .debug_pubnames and its
// placeholder subprogram entry, so they are compared by the rows they
// decode to, not by their bytes.
func TestAssemblerMatchesNasm(t *testing.T) {
	if _, err := exec.LookPath("nasm"); err != nil {
		t.Skip("nasm is not installed")
	}

	type program struct{ name, root string }
	var programs []program
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		root, _, ok := corpusCase(corpusDir, entry.Name(), entry.IsDir())
		if ok {
			programs = append(programs, program{"run/" + entry.Name(), root})
		}
	}
	examples, _ := filepath.Glob(filepath.Join(examplesDir, "*.gyro"))
	for _, path := range examples {
		programs = append(programs, program{"examples/" + filepath.Base(path), path})
	}
	benches, err := os.ReadDir("bench/testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range benches {
		root := filepath.Join("bench/testdata", entry.Name())
		if entry.IsDir() {
			root = filepath.Join(root, "main.gyro")
		} else if !strings.HasSuffix(entry.Name(), ".gyro") {
			continue
		}
		programs = append(programs, program{"bench/" + entry.Name(), root})
	}

	for _, p := range programs {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			source, err := os.ReadFile(p.root)
			if err != nil {
				t.Fatal(err)
			}
			if found := parseDirectives(t, string(source)); found.skip {
				t.Skip(found.skipped)
			}
			var module *metals.Module
			if filepath.Ext(p.root) == ".gyro" {
				program, info := compile(t, p.root)
				module = lowerVerified(t, program, info)
			} else {
				module = foreignRoot(t, p.root, string(source))
			}
			asm, err := nasm.Generate(module)
			if err != nil {
				t.Fatal(err)
			}
			compareAssemblers(t, asm)
		})
	}
}

func compareAssemblers(t *testing.T, asm string) {
	t.Helper()
	dir := t.TempDir()
	asmPath := filepath.Join(dir, "program.asm")
	objectPath := filepath.Join(dir, "program.o")
	if err := os.WriteFile(asmPath, []byte(asm), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("nasm", "-g", "-F", "dwarf", "-felf64", asmPath, "-o", objectPath)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("nasm: %v\n%s", err, out)
	}
	want, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := x86.Assemble(asm, asmPath)
	if err != nil {
		t.Fatal(err)
	}

	wantFile, err := elf.NewFile(bytes.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	gotFile, err := elf.NewFile(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("x86.Assemble wrote an unreadable object: %v", err)
	}

	for _, name := range []string{".text", ".rodata", ".data", ".bss"} {
		compareSection(t, name, wantFile, gotFile)
	}
	compareSymbols(t, wantFile, gotFile)
	compareLines(t, wantFile, gotFile)
}

func compareSection(t *testing.T, name string, want, got *elf.File) {
	t.Helper()
	w, g := want.Section(name), got.Section(name)
	if w == nil || g == nil {
		if w != g {
			t.Errorf("%s: nasm has it %v, x86 has it %v", name, w != nil, g != nil)
		}
		return
	}
	if w.Type != g.Type || w.Flags != g.Flags || w.Addralign != g.Addralign || w.Size != g.Size {
		t.Errorf("%s: header differs: nasm %v %v align %d size %d, x86 %v %v align %d size %d",
			name, w.Type, w.Flags, w.Addralign, w.Size, g.Type, g.Flags, g.Addralign, g.Size)
	}
	if w.Type != elf.SHT_NOBITS {
		wb, _ := w.Data()
		gb, _ := g.Data()
		if at := firstDifference(wb, gb); at >= 0 {
			t.Errorf("%s: bytes differ at offset %#x: nasm % x, x86 % x",
				name, at, window(wb, at), window(gb, at))
		}
	}
	wr, gr := relocations(t, want, name), relocations(t, got, name)
	if !slices.Equal(wr, gr) {
		t.Errorf("%s: relocations differ\nnasm: %v\n x86: %v", name, firstDiff(wr, gr), firstDiff(gr, wr))
	}
}

func firstDifference(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return min(len(a), len(b))
	}
	return -1
}

func window(b []byte, at int) []byte {
	return b[max(at-8, 0):min(at+8, len(b))]
}

func firstDiff(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
			if len(out) == 5 {
				break
			}
		}
	}
	return out
}

// relocations describes a section's relocations by what they say rather
// than by symbol index: offset, type, the symbol or section they name,
// and the addend.
func relocations(t *testing.T, file *elf.File, name string) []string {
	t.Helper()
	section := file.Section(".rela" + name)
	if section == nil {
		return nil
	}
	data, err := section.Data()
	if err != nil {
		t.Fatal(err)
	}
	symbols, err := file.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for at := 0; at+24 <= len(data); at += 24 {
		offset := file.ByteOrder.Uint64(data[at:])
		info := file.ByteOrder.Uint64(data[at+8:])
		addend := int64(file.ByteOrder.Uint64(data[at+16:]))
		sym := symbols[info>>32-1] // Symbols omits the null entry
		target := sym.Name
		if elf.ST_TYPE(sym.Info) == elf.STT_SECTION {
			target = file.Sections[sym.Section].Name
		}
		out = append(out, fmt.Sprintf("%#x %v %s%+d", offset, elf.R_X86_64(info&0xffffffff), target, addend))
	}
	slices.Sort(out)
	return out
}

// compareSymbols compares every named symbol: binding, section and value
// for all, and type and size for the globals, which are what a debugger
// names frames by.
func compareSymbols(t *testing.T, want, got *elf.File) {
	t.Helper()
	describe := func(file *elf.File) []string {
		symbols, err := file.Symbols()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, sym := range symbols {
			kind := elf.ST_TYPE(sym.Info)
			if kind == elf.STT_SECTION || kind == elf.STT_FILE {
				continue
			}
			section := "UND"
			if sym.Section != elf.SHN_UNDEF {
				section = file.Sections[sym.Section].Name
			}
			line := fmt.Sprintf("%s %v %s %#x", sym.Name, elf.ST_BIND(sym.Info), section, sym.Value)
			if elf.ST_BIND(sym.Info) == elf.STB_GLOBAL {
				line += fmt.Sprintf(" %v %d", kind, sym.Size)
			}
			out = append(out, line)
		}
		slices.Sort(out)
		return out
	}
	w, g := describe(want), describe(got)
	if !slices.Equal(w, g) {
		t.Errorf("symbols differ\nnasm: %v\n x86: %v", firstDiff(w, g), firstDiff(g, w))
	}
}

// compareLines decodes both line tables, relocations applied, and
// compares their rows. They differ in one way, on purpose: nasm starts a
// row only when the line number changes, so where one file's line N is
// followed by another file's line N it keeps crediting the first file.
// x86 starts that row. Each such row is skipped, and nothing else may
// differ.
func compareLines(t *testing.T, want, got *elf.File) {
	t.Helper()
	w, g := lineRows(t, want), lineRows(t, got)
	i, j, skipped := 0, 0, 0
	for i < len(w) && j < len(g) {
		if w[i] == g[j] {
			i, j = i+1, j+1
			continue
		}
		if j > 0 && !g[j].end && !g[j-1].end && g[j].line == g[j-1].line && g[j].file != g[j-1].file {
			j++
			skipped++
			continue
		}
		break
	}
	if i != len(w) || j != len(g) {
		t.Errorf("line tables differ at row %d of %d (x86 %d of %d)\nnasm: %v\n x86: %v",
			i, len(w), j, len(g), w[i:min(i+3, len(w))], g[j:min(j+3, len(g))])
	}
	if skipped > 0 {
		t.Logf("%d rows where nasm keeps the previous file", skipped)
	}
}

type lineRow struct {
	address uint64
	file    string
	line    int
	end     bool
}

func lineRows(t *testing.T, file *elf.File) []lineRow {
	t.Helper()
	data, err := file.DWARF()
	if err != nil {
		t.Fatal(err)
	}
	var out []lineRow
	reader := data.Reader()
	for {
		unit, err := reader.Next()
		if err != nil {
			t.Fatal(err)
		}
		if unit == nil {
			return out
		}
		if unit.Tag != dwarf.TagCompileUnit {
			reader.SkipChildren()
			continue
		}
		lines, err := data.LineReader(unit)
		if err != nil {
			t.Fatal(err)
		}
		for lines != nil {
			var row dwarf.LineEntry
			if err := lines.Next(&row); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if row.EndSequence {
				out = append(out, lineRow{address: row.Address, end: true})
				continue
			}
			out = append(out, lineRow{address: row.Address, file: row.File.Name, line: row.Line})
		}
		reader.SkipChildren()
	}
}
