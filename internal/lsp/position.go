package lsp

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// The protocol measures character offsets in UTF-16 code units, the
// JavaScript legacy every server inherits. These helpers convert between
// byte offsets into the source text and protocol positions.

// positionFor converts a byte offset to a protocol position.
func positionFor(src string, offset int) Position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(src) {
		offset = len(src)
	}

	lineStart := 0
	line := 0
	for i := 0; i < offset; i++ {
		if src[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}

	character := 0
	for _, r := range src[lineStart:offset] {
		character += utf16.RuneLen(r)
	}
	return Position{Line: line, Character: character}
}

// offsetFor converts a protocol position to a byte offset, clamping to the
// nearest valid location.
func offsetFor(src string, pos Position) int {
	offset := 0
	for line := 0; line < pos.Line; line++ {
		next := strings.IndexByte(src[offset:], '\n')
		if next < 0 {
			return len(src)
		}
		offset += next + 1
	}

	remaining := pos.Character
	for offset < len(src) && remaining > 0 {
		r, size := utf8.DecodeRuneInString(src[offset:])
		if r == '\n' {
			break
		}
		remaining -= utf16.RuneLen(r)
		offset += size
	}
	return offset
}

// before orders two positions the way the text reads.
func before(a, b Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Character < b.Character
}

// rangeFor converts a byte range to a protocol range.
func rangeFor(src string, start, end int) Range {
	return Range{Start: positionFor(src, start), End: positionFor(src, end)}
}

// pathFromURI turns a file:// URI into a filesystem path.
func pathFromURI(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return strings.TrimPrefix(uri, "file://")
	}
	return parsed.Path
}

// uriFromPath turns an absolute path into a file:// URI.
func uriFromPath(path string) string {
	path = filepath.ToSlash(path)
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// identifierEnd returns the end of the identifier starting at offset, or
// offset+1 when no identifier starts there, so a diagnostic always
// underlines at least one character.
func identifierEnd(src string, offset int) int {
	end := offset
	for end < len(src) {
		r, size := utf8.DecodeRuneInString(src[end:])
		if !isIdentifierPart(r) {
			break
		}
		end += size
	}
	if end == offset && offset < len(src) {
		return offset + 1
	}
	return end
}

func isIdentifierPart(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
