package lsp

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Server holds the state of one editor session: the open documents and the
// most recent analysis of each.
type Server struct {
	conn    *conn
	version string
	// documents maps URI to the current buffer state.
	documents map[string]*document
	// shutdown records that the client asked to stop; exit then ends Run.
	shutdown bool
	// workspace caches the analyses of files that are not open, by path,
	// for the requests that search beyond the open documents. Any edit
	// to an open document empties it.
	workspace map[string]*workspaceFile
}

// document is one open file plus its latest analysis. The analysis is kept
// even when it carries errors: hover and completion answer from the last
// state that produced a tree.
type document struct {
	uri  string
	path string
	text string
	// analysis is nil until the first analyze pass and stays at the last
	// successful parse when the buffer is currently broken.
	analysis *analysis
	// typed is the most recent analysis whose sema pass ran; completion
	// resolves types against it when the current buffer does not parse.
	typed *analysis
}

func (d *document) isMetals() bool {
	return strings.HasSuffix(d.path, ".metals")
}

// Serve runs the language server over the given streams until the client
// disconnects or sends exit. version is reported in the initialize
// handshake.
func Serve(reader io.Reader, writer io.Writer, version string) error {
	server := &Server{
		conn:      newConn(reader, writer),
		version:   version,
		documents: map[string]*document{},
	}
	return server.run()
}

func (s *Server) run() error {
	for {
		req, err := s.conn.read()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if req.Method == "exit" {
			return nil
		}
		if err := s.dispatch(req); err != nil {
			return err
		}
	}
}

func (s *Server) dispatch(req *request) error {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "initialized", "$/cancelRequest", "$/setTrace",
		"textDocument/didSave", "workspace/didChangeConfiguration":
		return nil
	case "shutdown":
		s.shutdown = true
		return s.conn.respond(req.ID, nil)
	case "textDocument/didOpen":
		return s.handleDidOpen(req)
	case "textDocument/didChange":
		return s.handleDidChange(req)
	case "textDocument/didClose":
		return s.handleDidClose(req)
	case "textDocument/hover":
		return s.handleHover(req)
	case "textDocument/definition":
		return s.handleDefinition(req)
	case "textDocument/references":
		return s.handleReferences(req)
	case "textDocument/documentSymbol":
		return s.handleDocumentSymbol(req)
	case "textDocument/completion":
		return s.handleCompletion(req)
	case "textDocument/inlayHint":
		return s.handleInlayHint(req)
	}

	if req.isNotification() {
		return nil
	}
	return s.conn.respondError(req.ID, codeMethodNotFound,
		fmt.Sprintf("method %q is not supported", req.Method))
}

// decode unmarshals request params, reporting a protocol error on failure.
func decode[T any](s *Server, req *request, params *T) (bool, error) {
	if err := json.Unmarshal(req.Params, params); err != nil {
		if req.isNotification() {
			return false, nil
		}
		return false, s.conn.respondError(req.ID, codeInvalidParams, err.Error())
	}
	return true, nil
}

func (s *Server) handleInitialize(req *request) error {
	var params initializeParams
	if ok, err := decode(s, req, &params); !ok {
		return err
	}

	return s.conn.respond(req.ID, initializeResult{
		Capabilities: serverCapabilities{
			TextDocumentSync:       textDocumentSyncOptions{OpenClose: true, Change: 1},
			HoverProvider:          true,
			DefinitionProvider:     true,
			ReferencesProvider:     true,
			DocumentSymbolProvider: true,
			InlayHintProvider:      true,
			CompletionProvider: &completionOptions{
				TriggerCharacters: []string{".", ":"},
			},
		},
		ServerInfo: serverInfo{Name: "gyro lsp", Version: s.version},
	})
}

func (s *Server) handleDidOpen(req *request) error {
	var params didOpenParams
	if ok, err := decode(s, req, &params); !ok {
		return err
	}

	uri := params.TextDocument.URI
	s.documents[uri] = &document{
		uri:  uri,
		path: pathFromURI(uri),
		text: params.TextDocument.Text,
	}
	return s.publishDiagnostics(s.documents[uri])
}

func (s *Server) handleDidChange(req *request) error {
	var params didChangeParams
	if ok, err := decode(s, req, &params); !ok {
		return err
	}

	doc := s.documents[params.TextDocument.URI]
	if doc == nil || len(params.ContentChanges) == 0 {
		return nil
	}
	// Full sync: the last change carries the complete text.
	doc.text = params.ContentChanges[len(params.ContentChanges)-1].Text
	return s.publishDiagnostics(doc)
}

func (s *Server) handleDidClose(req *request) error {
	var params didCloseParams
	if ok, err := decode(s, req, &params); !ok {
		return err
	}

	delete(s.documents, params.TextDocument.URI)
	// Clear the file's diagnostics so nothing stale lingers in the editor.
	return s.conn.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{
		URI:         params.TextDocument.URI,
		Diagnostics: []Diagnostic{},
	})
}

func (s *Server) publishDiagnostics(doc *document) error {
	diagnostics := s.analyze(doc)
	if diagnostics == nil {
		diagnostics = []Diagnostic{}
	}
	return s.conn.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{
		URI:         doc.uri,
		Diagnostics: diagnostics,
	})
}

// documentFor finds the open document a positional request refers to.
func (s *Server) documentFor(uri string) *document {
	return s.documents[uri]
}
