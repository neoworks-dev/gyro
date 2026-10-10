package lsp

import (
	"errors"
	"strings"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/loader"
	"github.com/gyro-lang/gyro/internal/metals"
	"github.com/gyro-lang/gyro/internal/sema"
)

// analysis is one completed compiler pass over an open document, rooted at
// that document.
type analysis struct {
	// text is the buffer content the analysis saw.
	text    string
	program *loader.Program
	// info is nil when parsing failed; sema only runs on a clean parse.
	info *sema.Info
	// idx is the position index, built lazily by the first positional
	// request.
	idx *index
}

// analyze runs the appropriate front end over the document, stores the
// result for the positional requests, and returns the diagnostics to
// publish for this file.
func (s *Server) analyze(doc *document) []Diagnostic {
	if doc.isMetals() {
		return analyzeMetals(doc.text)
	}
	return s.analyzeGyro(doc)
}

// analyzeGyro loads the program rooted at the document, with every open
// buffer overlaid on the file system, and checks it.
func (s *Server) analyzeGyro(doc *document) []Diagnostic {
	// An edit can change what any other module resolves to.
	s.workspace = nil

	var bag diag.Bag
	analyzed, err := s.check(doc.path, doc.text, &bag)
	if err != nil {
		return []Diagnostic{{
			Range:    rangeFor(doc.text, 0, 1),
			Severity: severityError,
			Source:   "gyro",
			Message:  err.Error(),
		}}
	}
	program, info := analyzed.program, analyzed.info
	doc.analysis = analyzed
	if info != nil {
		// The most recent fully typed analysis backs completion while the
		// buffer is mid-edit and does not parse.
		doc.typed = doc.analysis
	}

	// Only this file's diagnostics are published here; a dependency shows
	// its own when it is opened.
	rootName := program.Root.File.Name
	var diagnostics []Diagnostic
	for _, item := range bag.Items() {
		if item.Pos.File != rootName {
			continue
		}
		severity := severityError
		if item.Severity == diag.SeverityWarning {
			severity = severityWarning
		}
		// LSP has one message field, so the notes follow the message on
		// their own lines.
		message := item.Message
		for _, note := range item.Notes {
			message += "\nnote: " + note
		}
		for _, help := range item.Help {
			message += "\nhelp: " + help
		}
		diagnostics = append(diagnostics, Diagnostic{
			Range:    rangeFor(doc.text, item.Pos.Offset, identifierEnd(doc.text, item.Pos.Offset)),
			Severity: severity,
			Source:   "gyro",
			Message:  message,
			Code:     string(item.Code),
		})
	}
	return diagnostics
}

// check loads the program rooted at path, with every open buffer overlaid
// on the file system, and checks it when it parses.
func (s *Server) check(path, text string, bag *diag.Bag) (*analysis, error) {
	overlay := map[string]string{}
	for _, open := range s.documents {
		if !open.isMetals() {
			overlay[open.path] = open.text
		}
	}
	program, err := loader.LoadFromSource(loader.Overlay(overlay), path, text, bag)
	if err != nil {
		return nil, err
	}
	var info *sema.Info
	if !bag.HasErrors() {
		info = sema.CheckProgram(program, bag)
	}
	return &analysis{text: text, program: program, info: info}, nil
}

// analyzeMetals parses and verifies a METALS module.
func analyzeMetals(text string) []Diagnostic {
	module, err := metals.Parse(text)
	if err != nil {
		return []Diagnostic{metalsDiagnostic(text, err)}
	}
	if err := metals.Verify(module); err != nil {
		return []Diagnostic{metalsDiagnostic(text, err)}
	}
	return nil
}

// metalsDiagnostic places an error on the line a ParseError names, or on
// the first line when the error carries no position (the verifier's do).
func metalsDiagnostic(text string, err error) Diagnostic {
	line := 1
	var parseErr *metals.ParseError
	if errors.As(err, &parseErr) {
		line = parseErr.Line
	}

	start := 0
	for current := 1; current < line && start < len(text); {
		next := strings.IndexByte(text[start:], '\n')
		if next < 0 {
			break
		}
		start += next + 1
		current++
	}
	end := len(text)
	if next := strings.IndexByte(text[start:], '\n'); next >= 0 {
		end = start + next
	}

	return Diagnostic{
		Range:    rangeFor(text, start, end),
		Severity: severityError,
		Source:   "metals",
		Message:  err.Error(),
	}
}
