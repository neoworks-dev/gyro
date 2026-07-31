// Package diag provides source positions and compiler diagnostics.
package diag

import (
	"fmt"
	"sort"
	"strings"
)

// Position is a resolved human-readable location in a source file.
// Line and Column are 1-based; Offset is a 0-based byte index.
type Position struct {
	File   string
	Offset int
	Line   int
	Column int
}

func (p Position) String() string {
	if p.File == "" {
		return fmt.Sprintf("%d:%d", p.Line, p.Column)
	}
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Column)
}

// IsValid reports whether the position refers to a real location.
func (p Position) IsValid() bool {
	return p.Line > 0
}

// Severity ranks a diagnostic. Warnings do not fail a build; errors do.
type Severity int

const (
	SeverityWarning Severity = iota
	SeverityError
)

func (s Severity) String() string {
	if s == SeverityWarning {
		return "warning"
	}
	return "error"
}

// Diagnostic is a single message produced by any compiler stage.
type Diagnostic struct {
	Severity Severity
	Pos      Position
	Message  string
	// Notes are secondary lines printed under the message, used for
	// "allocated here" style breadcrumbs and fix hints.
	Notes []string
}

func (d Diagnostic) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s: %s", d.Pos, d.Severity, d.Message)
	for _, note := range d.Notes {
		fmt.Fprintf(&b, "\n  %s", note)
	}
	return b.String()
}

// Bag collects diagnostics across a compilation.
type Bag struct {
	items []Diagnostic
}

func (b *Bag) Errorf(pos Position, format string, args ...any) {
	b.items = append(b.items, Diagnostic{
		Severity: SeverityError,
		Pos:      pos,
		Message:  fmt.Sprintf(format, args...),
	})
}

func (b *Bag) Warnf(pos Position, format string, args ...any) {
	b.items = append(b.items, Diagnostic{
		Severity: SeverityWarning,
		Pos:      pos,
		Message:  fmt.Sprintf(format, args...),
	})
}

// Add appends a fully constructed diagnostic, including its notes.
func (b *Bag) Add(d Diagnostic) {
	b.items = append(b.items, d)
}

// Items returns the collected diagnostics sorted by source position.
func (b *Bag) Items() []Diagnostic {
	sort.SliceStable(b.items, func(i, j int) bool {
		if b.items[i].Pos.File != b.items[j].Pos.File {
			return b.items[i].Pos.File < b.items[j].Pos.File
		}
		return b.items[i].Pos.Offset < b.items[j].Pos.Offset
	})
	return b.items
}

func (b *Bag) HasErrors() bool {
	for _, item := range b.items {
		if item.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (b *Bag) Len() int {
	return len(b.items)
}

// File tracks line offsets so a byte offset can be turned into a Position.
type File struct {
	Name string
	Src  string
	// lineStarts[i] is the byte offset at which line i+1 begins.
	lineStarts []int
}

func NewFile(name, src string) *File {
	file := &File{Name: name, Src: src, lineStarts: []int{0}}
	for offset := 0; offset < len(src); offset++ {
		if src[offset] == '\n' {
			file.lineStarts = append(file.lineStarts, offset+1)
		}
	}
	return file
}

// Position resolves a byte offset into a line/column pair.
func (f *File) Position(offset int) Position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(f.Src) {
		offset = len(f.Src)
	}

	// Binary search for the last line start at or before offset.
	line := sort.Search(len(f.lineStarts), func(i int) bool {
		return f.lineStarts[i] > offset
	}) - 1

	return Position{
		File:   f.Name,
		Offset: offset,
		Line:   line + 1,
		Column: offset - f.lineStarts[line] + 1,
	}
}

// Line returns the text of the given 1-based line, without its newline.
func (f *File) Line(line int) string {
	if line < 1 || line > len(f.lineStarts) {
		return ""
	}
	start := f.lineStarts[line-1]
	end := len(f.Src)
	if line < len(f.lineStarts) {
		end = f.lineStarts[line] - 1
	}
	return strings.TrimSuffix(f.Src[start:end], "\r")
}
