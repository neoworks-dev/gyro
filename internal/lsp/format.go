package lsp

import (
	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/format"
)

type formattingParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type textEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// handleFormatting answers with one edit replacing the whole document, or
// with no edits when the buffer does not parse or is already formatted. The
// editor's own tab and width settings are ignored: the layout has no
// options.
func (s *Server) handleFormatting(req *request) error {
	var params formattingParams
	if ok, err := decode(s, req, &params); !ok {
		return err
	}

	edits := []textEdit{}
	doc := s.documentFor(params.TextDocument.URI)
	if doc != nil && !doc.isMetals() {
		var bag diag.Bag
		formatted, err := format.Source(doc.path, doc.text, &bag)
		if err == nil && formatted != doc.text {
			edits = append(edits, textEdit{
				Range:   rangeFor(doc.text, 0, len(doc.text)),
				NewText: formatted,
			})
		}
	}
	return s.conn.respond(req.ID, edits)
}
