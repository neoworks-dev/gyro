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
	// Code names the kind of error, printed as error[E0401]. It may be
	// NoCode; see codes.go.
	Code    Code
	Pos     Position
	Message string
	// Length is the width of the underlined span in bytes. Zero means the
	// renderer measures the token at Pos, which is what most sites want.
	Length int
	// Label is the short text printed beside the underline.
	Label string
	// Related points at the other places that explain this one: the earlier
	// declaration, the call site an instantiation came from.
	Related []Span
	// Notes state a fact about the language; Help suggests a fix.
	Notes []string
	Help  []string
}

// Span is a secondary location under a diagnostic.
type Span struct {
	Pos    Position
	Length int
	Label  string
}

// String is the one-line form, without the source frame: what a test or a
// log wants. Rendering for a terminal is Render.
func (d Diagnostic) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", d.Pos, d.Severity)
	if d.Code != NoCode {
		fmt.Fprintf(&b, "[%s]", d.Code)
	}
	fmt.Fprintf(&b, ": %s", d.Message)
	for _, note := range d.Notes {
		fmt.Fprintf(&b, "\n  note: %s", note)
	}
	for _, help := range d.Help {
		fmt.Fprintf(&b, "\n  help: %s", help)
	}
	return b.String()
}

// Bag collects diagnostics across a compilation. Identical messages at the
// same position are collected once: a signature is resolved both when types
// are built and when the body is checked, and reporting its error twice only
// makes the output harder to read.
type Bag struct {
	items []Diagnostic
	seen  map[string]bool
	// files holds the sources the diagnostics point into, so rendering can
	// quote the offending line. A file the bag never saw renders without
	// its frame rather than failing.
	files map[string]*File
}

// Track registers a source file for rendering. Every stage that creates a
// diag.File calls it; the cost of forgetting is a frame-less diagnostic.
func (b *Bag) Track(file *File) {
	if file == nil {
		return
	}
	if b.files == nil {
		b.files = map[string]*File{}
	}
	b.files[file.Name] = file
}

// Render draws every diagnostic as a code frame, separated by blank lines.
func (b *Bag) Render(style Style) string {
	var out strings.Builder
	for _, item := range b.Items() {
		out.WriteString(Render(item, b.files[item.Pos.File], style))
		out.WriteString("\n\n")
	}
	return out.String()
}

func (b *Bag) Errorf(pos Position, format string, args ...any) {
	b.Add(Diagnostic{
		Severity: SeverityError,
		Pos:      pos,
		Message:  fmt.Sprintf(format, args...),
	})
}

func (b *Bag) Warnf(pos Position, format string, args ...any) {
	b.Add(Diagnostic{
		Severity: SeverityWarning,
		Pos:      pos,
		Message:  fmt.Sprintf(format, args...),
	})
}

// Add appends a fully constructed diagnostic, including its notes, unless
// the same message was already reported at the same position.
func (b *Bag) Add(d Diagnostic) {
	key := fmt.Sprintf("%s\x00%d\x00%s\x00%s", d.Pos, d.Severity, d.Code, d.Message)
	if b.seen[key] {
		return
	}
	if b.seen == nil {
		b.seen = map[string]bool{}
	}
	b.seen[key] = true
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
