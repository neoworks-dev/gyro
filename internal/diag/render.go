package diag

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Rendering draws a diagnostic as a code frame: the headline with its code,
// the location, the offending line with the span underlined, and the notes
// under it.
//
//	error[E0401]: undeclared name println
//	 --> hello.gyro:2:3
//	  |
//	2 |   println("hi")
//	  |   ^^^^^^^ not found in this scope
//	  |
//	  = help: "@gyro/stdlib" exports println; add: import { println } from "@gyro/stdlib"
//
// Without the source the frame degrades to the headline and the location,
// which is what the one-line String() form prints.

// Style controls the decoration of rendered diagnostics.
type Style struct {
	Color bool
}

// AutoStyle enables color when the stream is a terminal and the environment
// does not ask for plain output.
func AutoStyle(stream *os.File) Style {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return Style{}
	}
	info, err := stream.Stat()
	if err != nil {
		return Style{}
	}
	return Style{Color: info.Mode()&os.ModeCharDevice != 0}
}

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiRed    = "\x1b[1;31m"
	ansiYellow = "\x1b[1;33m"
	ansiBlue   = "\x1b[1;34m"
	ansiCyan   = "\x1b[1;36m"
)

func (s Style) paint(color, text string) string {
	if !s.Color {
		return text
	}
	return color + text + ansiReset
}

// Render draws one diagnostic. file supplies the source line and may be nil.
func Render(d Diagnostic, file *File, style Style) string {
	var b strings.Builder
	b.WriteString(renderHeadline(d, style))

	if file == nil || !d.Pos.IsValid() {
		b.WriteString("\n --> " + d.Pos.String())
		return b.String()
	}

	gutter := len(strconv.Itoa(d.Pos.Line))
	for _, related := range d.Related {
		gutter = max(gutter, len(strconv.Itoa(related.Pos.Line)))
	}

	b.WriteString("\n")
	b.WriteString(renderFrame(frame{
		pos:    d.Pos,
		length: d.Length,
		label:  d.Label,
		caret:  '^',
	}, file, gutter, style))

	for _, related := range d.Related {
		b.WriteString(renderFrame(frame{
			pos:    related.Pos,
			length: related.Length,
			label:  related.Label,
			caret:  '-',
		}, file, gutter, style))
	}

	b.WriteString(renderNotes(d, gutter, style))
	return strings.TrimRight(b.String(), "\n")
}

func renderHeadline(d Diagnostic, style Style) string {
	color := ansiRed
	if d.Severity == SeverityWarning {
		color = ansiYellow
	}

	headline := d.Severity.String()
	if d.Code != NoCode {
		headline += "[" + string(d.Code) + "]"
	}
	return style.paint(color, headline+":") + " " + style.paint(ansiBold, d.Message)
}

// frame is one underlined source location: the primary span of a diagnostic
// or one of its related spans.
type frame struct {
	pos    Position
	length int
	label  string
	caret  rune
}

func renderFrame(f frame, file *File, gutter int, style Style) string {
	source := file.Line(f.pos.Line)
	if source == "" && f.pos.Column <= 1 {
		return indent(gutter) + style.paint(ansiBlue, "--> ") + f.pos.String() + "\n"
	}

	length := f.length
	if length <= 0 {
		length = file.SpanAt(f.pos.Offset)
	}
	length = min(length, max(1, len(source)-f.pos.Column+1))

	bar := style.paint(ansiBlue, "|")
	number := style.paint(ansiBlue, strconv.Itoa(f.pos.Line))

	var b strings.Builder
	fmt.Fprintf(&b, "%s%s%s\n", indent(gutter), style.paint(ansiBlue, "--> "), f.pos.String())
	fmt.Fprintf(&b, "%s %s\n", indent(gutter), bar)
	fmt.Fprintf(&b, "%s%s %s %s\n", number, strings.Repeat(" ", gutter-len(strconv.Itoa(f.pos.Line))), bar, source)

	underline := strings.Repeat(string(f.caret), length)
	color := ansiRed
	if f.caret == '-' {
		color = ansiCyan
	}
	marker := style.paint(color, underline)
	if f.label != "" {
		marker += " " + style.paint(color, f.label)
	}
	fmt.Fprintf(&b, "%s %s %s%s\n", indent(gutter), bar, leadingSpace(source, f.pos.Column), marker)
	return b.String()
}

func renderNotes(d Diagnostic, gutter int, style Style) string {
	if len(d.Notes) == 0 && len(d.Help) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", indent(gutter), style.paint(ansiBlue, "|"))
	for _, note := range d.Notes {
		fmt.Fprintf(&b, "%s %s %s %s\n", indent(gutter), style.paint(ansiBlue, "="),
			style.paint(ansiBold, "note:"), note)
	}
	for _, help := range d.Help {
		fmt.Fprintf(&b, "%s %s %s %s\n", indent(gutter), style.paint(ansiBlue, "="),
			style.paint(ansiCyan, "help:"), help)
	}
	return b.String()
}

func indent(width int) string {
	return strings.Repeat(" ", width)
}

// leadingSpace reproduces the source's indentation up to column so the
// underline lands under the span even when the line is indented with tabs.
func leadingSpace(source string, column int) string {
	var b strings.Builder
	for i, ch := range source {
		if i >= column-1 {
			break
		}
		if ch == '\t' {
			b.WriteByte('\t')
			continue
		}
		b.WriteByte(' ')
	}
	return b.String()
}

// SpanAt measures the token at an offset, so a diagnostic underlines the
// name it is about without every reporting site having to record a length.
func (f *File) SpanAt(offset int) int {
	if offset < 0 || offset >= len(f.Src) {
		return 1
	}
	if isWordByte(f.Src[offset]) {
		length := 0
		for offset+length < len(f.Src) && isWordByte(f.Src[offset+length]) {
			length++
		}
		return length
	}
	if f.Src[offset] == '"' {
		return stringSpan(f.Src[offset:])
	}
	return 1
}

// stringSpan measures a string literal from its opening quote, so an import
// specifier is underlined whole.
func stringSpan(src string) int {
	for i := 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		case '\n':
			return i
		}
	}
	return len(src)
}

func isWordByte(ch byte) bool {
	switch {
	case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		return true
	case ch == '_', ch >= 0x80:
		return true
	}
	return false
}
