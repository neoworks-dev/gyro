package lsp

// The subset of Language Server Protocol types this server speaks. Field
// names and casing follow the specification; only what the server actually
// reads or writes is declared.

// Position is a zero-based line and UTF-16 character offset.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

const (
	severityError   = 1
	severityWarning = 2
)

type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
	// Code is the diagnostic code, E0401 and friends. Editors show it beside
	// the message and let the user search for it.
	Code string `json:"code,omitempty"`
}

type publishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// --- initialize -------------------------------------------------------------

type initializeParams struct {
	RootURI string `json:"rootUri"`
}

type serverCapabilities struct {
	TextDocumentSync       textDocumentSyncOptions `json:"textDocumentSync"`
	HoverProvider          bool                    `json:"hoverProvider"`
	DefinitionProvider     bool                    `json:"definitionProvider"`
	DocumentSymbolProvider bool                    `json:"documentSymbolProvider"`
	CompletionProvider     *completionOptions      `json:"completionProvider,omitempty"`
}

type textDocumentSyncOptions struct {
	OpenClose bool `json:"openClose"`
	// Change 1 is full-document sync: the client resends the whole text on
	// every edit. Gyro files are small; simplicity wins.
	Change int `json:"change"`
}

type completionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
	ServerInfo   serverInfo         `json:"serverInfo"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// --- text document sync ------------------------------------------------------

type textDocumentItem struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
	Text    string `json:"text"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type contentChange struct {
	Text string `json:"text"`
}

type didChangeParams struct {
	TextDocument   textDocumentIdentifier `json:"textDocument"`
	ContentChanges []contentChange        `json:"contentChanges"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

// --- positional requests -----------------------------------------------------

type textDocumentPositionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type markupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type hoverResult struct {
	Contents markupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

// --- document symbols ---------------------------------------------------------

const (
	symbolKindMethod   = 6
	symbolKindField    = 8
	symbolKindEnum     = 10
	symbolKindFunction = 12
	symbolKindVariable = 13
	symbolKindConstant = 14
	symbolKindStruct   = 23
	symbolKindEnumCase = 22
)

type documentSymbolParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type documentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []documentSymbol `json:"children,omitempty"`
}

// --- completion ----------------------------------------------------------------

const (
	completionKindMethod   = 2
	completionKindFunction = 3
	completionKindField    = 5
	completionKindVariable = 6
	completionKindStruct   = 7
	completionKindModule   = 9
	completionKindEnum     = 13
	completionKindKeyword  = 14
	completionKindConstant = 21
	completionKindEnumCase = 20
)

type completionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type completionItem struct {
	Label  string `json:"label"`
	Kind   int    `json:"kind"`
	Detail string `json:"detail,omitempty"`
}
