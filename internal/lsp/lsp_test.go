package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// client drives a server over in-memory pipes the way an editor would.
type client struct {
	t      *testing.T
	writer io.Writer
	reader *bufio.Reader
	nextID int
	// notifications collects server-initiated messages seen while waiting
	// for responses.
	notifications []clientMessage
	done          chan error
}

type clientMessage struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
	Result json.RawMessage  `json:"result"`
	Error  *responseError   `json:"error"`
}

func newClient(t *testing.T) *client {
	t.Helper()
	clientToServer, serverIn := io.Pipe()
	serverOut, serverToClient := io.Pipe()

	c := &client{
		t:      t,
		writer: serverIn,
		reader: bufio.NewReader(serverOut),
		done:   make(chan error, 1),
	}
	go func() {
		c.done <- Serve(clientToServer, serverToClient, "test")
	}()
	t.Cleanup(func() {
		serverIn.Close()
		select {
		case <-c.done:
		case <-time.After(time.Second):
			t.Error("the server did not stop")
		}
	})
	return c
}

func (c *client) send(message string) {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.writer, "Content-Length: %d\r\n\r\n%s", len(message), message); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

func (c *client) notify(method string, params string) {
	c.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":%s}`, method, params))
}

// call sends a request and reads messages until its response arrives.
func (c *client) call(method string, params string) json.RawMessage {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params))

	for {
		message := c.read()
		if message.ID != nil && string(*message.ID) == fmt.Sprint(id) {
			if message.Error != nil {
				c.t.Fatalf("%s failed: %s", method, message.Error.Message)
			}
			return message.Result
		}
		c.notifications = append(c.notifications, message)
	}
}

func (c *client) read() clientMessage {
	c.t.Helper()
	contentLength := 0
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			c.t.Fatalf("read: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		fmt.Sscanf(line, "Content-Length: %d", &contentLength)
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(c.reader, body); err != nil {
		c.t.Fatalf("read body: %v", err)
	}
	var message clientMessage
	if err := json.Unmarshal(body, &message); err != nil {
		c.t.Fatalf("bad message %q: %v", body, err)
	}
	return message
}

// waitDiagnostics reads messages until a publishDiagnostics notification
// for the URI arrives.
func (c *client) waitDiagnostics(uri string) []Diagnostic {
	c.t.Helper()
	check := func(message clientMessage) ([]Diagnostic, bool) {
		if message.Method != "textDocument/publishDiagnostics" {
			return nil, false
		}
		var params publishDiagnosticsParams
		if err := json.Unmarshal(message.Params, &params); err != nil || params.URI != uri {
			return nil, false
		}
		return params.Diagnostics, true
	}

	for i, pending := range c.notifications {
		if diagnostics, ok := check(pending); ok {
			c.notifications = append(c.notifications[:i], c.notifications[i+1:]...)
			return diagnostics
		}
	}
	for {
		if diagnostics, ok := check(c.read()); ok {
			return diagnostics
		}
	}
}

// initialize performs the handshake and opens a document.
func (c *client) initialize() {
	c.t.Helper()
	c.call("initialize", `{"rootUri":null}`)
	c.notify("initialized", `{}`)
}

func (c *client) open(uri, text string) {
	c.t.Helper()
	body, _ := json.Marshal(text)
	c.notify("textDocument/didOpen", fmt.Sprintf(
		`{"textDocument":{"uri":%q,"languageId":"gyro","version":1,"text":%s}}`, uri, body))
}

func (c *client) change(uri, text string) {
	c.t.Helper()
	body, _ := json.Marshal(text)
	c.notify("textDocument/didChange", fmt.Sprintf(
		`{"textDocument":{"uri":%q,"version":2},"contentChanges":[{"text":%s}]}`, uri, body))
}

// tempURI writes content next to nothing in a temp dir and returns its URI
// and path.
func tempURI(t *testing.T, name, content string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return uriFromPath(path), path
}

// position computes the protocol position of a needle in text.
func position(t *testing.T, text, needle string) string {
	t.Helper()
	at := strings.Index(text, needle)
	if at < 0 {
		t.Fatalf("needle %q not found", needle)
	}
	pos := positionFor(text, at)
	return fmt.Sprintf(`{"line":%d,"character":%d}`, pos.Line, pos.Character)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

const cleanSource = `import { println } from "stdlib"

struct Point {
  x: i32
  y: i32
}

fn total(p: Point): i32 {
  return p.x + p.y
}

fn main() {
  var origin = Point{ x: 1, y: 2 }
  println(total(origin))
}
`

func TestDiagnosticsLifecycle(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", cleanSource)

	c.open(uri, cleanSource)
	if diagnostics := c.waitDiagnostics(uri); len(diagnostics) != 0 {
		t.Fatalf("clean file has diagnostics: %+v", diagnostics)
	}

	broken := strings.Replace(cleanSource, "total(origin)", "total(origin, 2)", 1)
	c.change(uri, broken)
	diagnostics := c.waitDiagnostics(uri)
	if len(diagnostics) == 0 {
		t.Fatal("expected a diagnostic for the extra argument")
	}
	if diagnostics[0].Severity != severityError {
		t.Fatalf("expected an error, got %+v", diagnostics[0])
	}

	c.change(uri, cleanSource)
	if diagnostics := c.waitDiagnostics(uri); len(diagnostics) != 0 {
		t.Fatalf("diagnostics did not clear: %+v", diagnostics)
	}
}

func TestSyntaxErrorDiagnostic(t *testing.T) {
	c := newClient(t)
	c.initialize()
	source := "import println from \"stdlib\"\n\nfn main() {\n}\n"
	uri, _ := tempURI(t, "main.gyro", source)

	c.open(uri, source)
	diagnostics := c.waitDiagnostics(uri)
	if len(diagnostics) == 0 {
		t.Fatal("expected a syntax diagnostic")
	}
	if !strings.Contains(diagnostics[0].Message, "no default exports") {
		t.Fatalf("unexpected message %q", diagnostics[0].Message)
	}
}

func TestHover(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", cleanSource)
	c.open(uri, cleanSource)
	c.waitDiagnostics(uri)

	result := c.call("textDocument/hover", fmt.Sprintf(
		`{"textDocument":{"uri":%q},"position":%s}`, uri, position(t, cleanSource, "origin))")))
	var hover hoverResult
	if err := json.Unmarshal(result, &hover); err != nil {
		t.Fatalf("bad hover %s: %v", result, err)
	}
	if !strings.Contains(hover.Contents.Value, "origin: Point") {
		t.Fatalf("hover missed the type: %q", hover.Contents.Value)
	}
}

func TestHoverFunctionSignature(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", cleanSource)
	c.open(uri, cleanSource)
	c.waitDiagnostics(uri)

	result := c.call("textDocument/hover", fmt.Sprintf(
		`{"textDocument":{"uri":%q},"position":%s}`, uri, position(t, cleanSource, "total(origin)")))
	var hover hoverResult
	if err := json.Unmarshal(result, &hover); err != nil {
		t.Fatalf("bad hover %s: %v", result, err)
	}
	if !strings.Contains(hover.Contents.Value, "fn total(p: Point): i32") {
		t.Fatalf("hover missed the signature: %q", hover.Contents.Value)
	}
}

func TestDefinition(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", cleanSource)
	c.open(uri, cleanSource)
	c.waitDiagnostics(uri)

	result := c.call("textDocument/definition", fmt.Sprintf(
		`{"textDocument":{"uri":%q},"position":%s}`, uri, position(t, cleanSource, "total(origin)")))
	var location Location
	if err := json.Unmarshal(result, &location); err != nil {
		t.Fatalf("bad location %s: %v", result, err)
	}
	if location.URI != uri {
		t.Fatalf("definition in the wrong file: %q", location.URI)
	}
	want := positionFor(cleanSource, strings.Index(cleanSource, "total(p"))
	if location.Range.Start != want {
		t.Fatalf("definition at %+v, want %+v", location.Range.Start, want)
	}
}

func TestDefinitionInAssociatedTypes(t *testing.T) {
	src := `trait Marker {
  fn mark(self): i32
}

trait Container {
  type Item: Marker
  fn first(self): Self::Item
}

struct Id {
  n: i32
}

impl Marker for Id {
  fn mark(self): i32 {
    return self.n
  }
}

struct Bag {
  n: i32
}

impl Container for Bag {
  type Item = Id
  fn first(self): Id {
    return Id{ n: self.n }
  }
}
`
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", src)
	c.open(uri, src)
	c.waitDiagnostics(uri)

	for probe, target := range map[string]string{
		"Id\n  fn first(self): Id {": "Id {",
		"Marker\n  fn first(self)":   "Marker {",
	} {
		want := positionFor(src, strings.Index(src, target))
		result := c.call("textDocument/definition", fmt.Sprintf(
			`{"textDocument":{"uri":%q},"position":%s}`, uri, position(t, src, probe)))
		var location Location
		if err := json.Unmarshal(result, &location); err != nil {
			t.Fatalf("bad location %s: %v", result, err)
		}
		if location.Range.Start != want {
			t.Errorf("definition of %q at %+v, want %+v", probe, location.Range.Start, want)
		}
	}
}

func TestDefinitionAcrossFiles(t *testing.T) {
	c := newClient(t)
	c.initialize()

	library := "export fn helper(): i32 {\n  return 4\n}\n"
	directory := t.TempDir()
	libraryPath := filepath.Join(directory, "lib.gyro")
	if err := os.WriteFile(libraryPath, []byte(library), 0o644); err != nil {
		t.Fatal(err)
	}
	source := "import { helper } from \"./lib\"\n\nfn main() {\n  var x = helper()\n}\n"
	mainPath := filepath.Join(directory, "main.gyro")
	if err := os.WriteFile(mainPath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	uri := uriFromPath(mainPath)
	c.open(uri, source)
	c.waitDiagnostics(uri)

	result := c.call("textDocument/definition", fmt.Sprintf(
		`{"textDocument":{"uri":%q},"position":%s}`, uri, position(t, source, "helper()")))
	var location Location
	if err := json.Unmarshal(result, &location); err != nil {
		t.Fatalf("bad location %s: %v", result, err)
	}
	if location.URI != uriFromPath(libraryPath) {
		t.Fatalf("definition should be in lib.gyro, got %q", location.URI)
	}
}

func TestDocumentSymbols(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", cleanSource)
	c.open(uri, cleanSource)
	c.waitDiagnostics(uri)

	result := c.call("textDocument/documentSymbol", fmt.Sprintf(
		`{"textDocument":{"uri":%q}}`, uri))
	var symbols []documentSymbol
	if err := json.Unmarshal(result, &symbols); err != nil {
		t.Fatalf("bad symbols %s: %v", result, err)
	}
	var names []string
	for _, symbol := range symbols {
		names = append(names, symbol.Name)
	}
	if strings.Join(names, ",") != "Point,total,main" {
		t.Fatalf("unexpected outline: %v", names)
	}
	if len(symbols[0].Children) != 2 {
		t.Fatalf("Point should list its fields: %+v", symbols[0].Children)
	}
}

func TestCompletionMembers(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", cleanSource)
	c.open(uri, cleanSource)
	c.waitDiagnostics(uri)

	// Simulate typing `origin.` inside main.
	edited := strings.Replace(cleanSource, "println(total(origin))",
		"println(total(origin))\n  origin.", 1)
	c.change(uri, edited)
	c.waitDiagnostics(uri)

	at := strings.Index(edited, "origin.") + len("origin.")
	// The needle appears twice; take the one we inserted, which is last.
	at = strings.LastIndex(edited, "origin.") + len("origin.")
	pos := positionFor(edited, at)

	result := c.call("textDocument/completion", fmt.Sprintf(
		`{"textDocument":{"uri":%q},"position":{"line":%d,"character":%d}}`,
		uri, pos.Line, pos.Character))
	var items []completionItem
	if err := json.Unmarshal(result, &items); err != nil {
		t.Fatalf("bad completion %s: %v", result, err)
	}
	labels := map[string]bool{}
	for _, item := range items {
		labels[item.Label] = true
	}
	if !labels["x"] || !labels["y"] {
		t.Fatalf("field completion missed x/y: %+v", items)
	}
}

func TestCompletionScope(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", cleanSource)
	c.open(uri, cleanSource)
	c.waitDiagnostics(uri)

	at := strings.Index(cleanSource, "println(total(origin))")
	pos := positionFor(cleanSource, at)
	result := c.call("textDocument/completion", fmt.Sprintf(
		`{"textDocument":{"uri":%q},"position":{"line":%d,"character":%d}}`,
		uri, pos.Line, pos.Character))
	var items []completionItem
	if err := json.Unmarshal(result, &items); err != nil {
		t.Fatalf("bad completion %s: %v", result, err)
	}
	labels := map[string]bool{}
	for _, item := range items {
		labels[item.Label] = true
	}
	for _, want := range []string{"origin", "total", "main", "Point", "println", "var"} {
		if !labels[want] {
			t.Fatalf("scope completion missed %q: have %d items", want, len(items))
		}
	}
}

const hintSource = `import { println } from "stdlib"

struct Point {
  x: i32
  y: i32
}

fn scaled(p: Point, factor: i32): i32 {
  return (p.x + p.y) * factor
}

fn apply(value: i32, f: fn(i32): i32): i32 {
  return f(value)
}

fn main() {
  var origin = Point{ x: 1, y: 2 }
  var factor = 3
  var values = [1, 2]
  for value in values {
    println(scaled(origin, apply(value, fn(n) n * 2)))
  }
  println(scaled(origin, factor + 1))
  println(values.len())
}
`

// annotate reinserts hints into the source, so an expectation reads like
// what the editor draws.
func annotate(t *testing.T, text string, hints []inlayHint) string {
	t.Helper()
	annotated := text
	// Later offsets first, so the earlier ones stay valid.
	for i := len(hints) - 1; i >= 0; i-- {
		label := hints[i].Label
		if hints[i].PaddingRight {
			label += " "
		}
		at := offsetFor(annotated, hints[i].Position)
		annotated = annotated[:at] + label + annotated[at:]
	}
	return annotated
}

func (c *client) inlayHints(uri, text string, span Range) []inlayHint {
	c.t.Helper()
	result := c.call("textDocument/inlayHint", fmt.Sprintf(
		`{"textDocument":{"uri":%q},"range":{"start":{"line":%d,"character":%d},`+
			`"end":{"line":%d,"character":%d}}}`,
		uri, span.Start.Line, span.Start.Character, span.End.Line, span.End.Character))
	var hints []inlayHint
	if err := json.Unmarshal(result, &hints); err != nil {
		c.t.Fatalf("bad hints %s: %v", result, err)
	}
	return hints
}

// wholeFile is the range a client would ask about when the whole document
// is on screen.
func wholeFile(text string) Range {
	return Range{End: positionFor(text, len(text))}
}

func TestInlayHints(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", hintSource)
	c.open(uri, hintSource)
	if diagnostics := c.waitDiagnostics(uri); len(diagnostics) != 0 {
		t.Fatalf("the hint source should check cleanly: %+v", diagnostics)
	}

	hints := c.inlayHints(uri, hintSource, wholeFile(hintSource))
	want := `fn main() {
  var origin: Point = Point{ x: 1, y: 2 }
  var factor: i32 = 3
  var values: i32[2] = [1, 2]
  for value: i32 in values {
    println(value: scaled(p: origin, factor: apply(value, f: fn(n: i32) n * 2)))
  }
  println(value: scaled(p: origin, factor: factor + 1))
  println(value: values.len())
}
`
	annotated := annotate(t, hintSource, hints)
	if !strings.Contains(annotated, want) {
		t.Fatalf("hints render as:\n%s\nwant to contain:\n%s", annotated, want)
	}
}

func TestInlayHintsHonourRange(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", hintSource)
	c.open(uri, hintSource)
	c.waitDiagnostics(uri)

	// Ask only about the line declaring origin.
	at := positionFor(hintSource, strings.Index(hintSource, "var origin"))
	span := Range{Start: at, End: Position{Line: at.Line, Character: 999}}
	hints := c.inlayHints(uri, hintSource, span)
	if len(hints) != 1 {
		t.Fatalf("one line should yield one hint, got %+v", hints)
	}
	if hints[0].Label != ": Point" || hints[0].Position.Line != at.Line {
		t.Fatalf("unexpected hint %+v", hints[0])
	}
}

func TestInlayHintsWaitForACleanCheck(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", hintSource)
	c.open(uri, hintSource)
	c.waitDiagnostics(uri)

	broken := strings.Replace(hintSource, "var origin = Point{ x: 1, y: 2 }", "var origin = ", 1)
	c.change(uri, broken)
	c.waitDiagnostics(uri)

	if hints := c.inlayHints(uri, broken, wholeFile(broken)); len(hints) != 0 {
		t.Fatalf("a buffer that does not check should have no hints: %+v", hints)
	}
}

// A generic body carries no types of its own: sema records one set per
// instantiation, so the hints exist only while a single instantiation pins
// them.
const genericHintSource = `import { println, Ord } from "stdlib"

fn largest<T: Ord>(a: T, b: T): T {
  var winner = a
  if b > a {
    winner = b
  }
  return winner
}

fn main() {
  println(largest(1, 2))
}
`

func TestInlayHintsInGenericBody(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", genericHintSource)
	c.open(uri, genericHintSource)
	if diagnostics := c.waitDiagnostics(uri); len(diagnostics) != 0 {
		t.Fatalf("the generic source should check cleanly: %+v", diagnostics)
	}

	hints := c.inlayHints(uri, genericHintSource, wholeFile(genericHintSource))
	if !strings.Contains(annotate(t, genericHintSource, hints), "var winner: i32 = a") {
		t.Fatalf("the sole instantiation should type the body:\n%s",
			annotate(t, genericHintSource, hints))
	}

	// A second instantiation makes the body ambiguous.
	twice := strings.Replace(genericHintSource, "  println(largest(1, 2))",
		"  println(largest(1, 2))\n  println(largest(1.5, 2.5))", 1)
	c.change(uri, twice)
	c.waitDiagnostics(uri)

	hints = c.inlayHints(uri, twice, wholeFile(twice))
	if strings.Contains(annotate(t, twice, hints), "var winner:") {
		t.Fatalf("two instantiations should leave the body unannotated:\n%s",
			annotate(t, twice, hints))
	}
}

func TestMetalsDiagnostics(t *testing.T) {
	c := newClient(t)
	c.initialize()

	good := "module \"m\"\n\nfn @main() {\n  return\n}\n"
	uri, _ := tempURI(t, "out.metals", good)
	c.open(uri, good)
	if diagnostics := c.waitDiagnostics(uri); len(diagnostics) != 0 {
		t.Fatalf("clean module has diagnostics: %+v", diagnostics)
	}

	bad := "module \"m\"\n\nfn @main() {\n  %x = iadd %a, %b\n  return\n}\n"
	c.change(uri, bad)
	diagnostics := c.waitDiagnostics(uri)
	if len(diagnostics) == 0 {
		t.Fatal("the verifier should reject the unassigned registers")
	}
}

const referencesSource = `import { println } from "stdlib"

struct Counter {
  count: i32
}

fn Counter::bump(*self, by: i32) {
  self.count += by
}

trait Shape {
  fn area(self): i32
}

struct Square {
  side: i32
}

impl Shape for Square {
  fn area(self): i32 {
    var length = self.side
    return length * length
  }
}

fn main() {
  var counter = Counter{ count: 0 }
  counter.bump(1)
  counter.bump(2)
  var square = Square{ side: 3 }
  println(square.area())
  println(counter.count)
}
`

// references asks for the references at needle and returns where they
// start, as byte offsets into text.
func (c *client) references(uri, text, needle string, includeDeclaration bool) map[string][]int {
	c.t.Helper()
	result := c.call("textDocument/references", fmt.Sprintf(
		`{"textDocument":{"uri":%q},"position":%s,"context":{"includeDeclaration":%t}}`,
		uri, position(c.t, text, needle), includeDeclaration))
	var locations []Location
	if err := json.Unmarshal(result, &locations); err != nil {
		c.t.Fatalf("bad references %s: %v", result, err)
	}
	found := map[string][]int{}
	for _, location := range locations {
		found[location.URI] = append(found[location.URI], offsetFor(text, location.Range.Start))
	}
	return found
}

// offsetsOf is where each needle starts in text.
func offsetsOf(t *testing.T, text string, needles ...string) []int {
	t.Helper()
	var offsets []int
	for _, needle := range needles {
		at := strings.Index(text, needle)
		if at < 0 {
			t.Fatalf("needle %q not found", needle)
		}
		offsets = append(offsets, at)
	}
	return offsets
}

func TestReferences(t *testing.T) {
	c := newClient(t)
	c.initialize()
	uri, _ := tempURI(t, "main.gyro", referencesSource)
	c.open(uri, referencesSource)
	c.waitDiagnostics(uri)

	cases := []struct {
		name, needle       string
		includeDeclaration bool
		want               []string
	}{
		{"a method from its declaration", "bump(*self", true,
			[]string{"bump(*self", "bump(1)", "bump(2)"}},
		{"a method from a call", "bump(2)", false,
			[]string{"bump(1)", "bump(2)"}},
		{"a parameter", "by: i32", true,
			[]string{"by: i32", "by\n"}},
		{"a local in an impl body", "length * length", true,
			[]string{"length = self", "length * length", "length\n  }"}},
		{"a trait method called on its implementer", "area())", true,
			[]string{"area(self): i32 {", "area())"}},
		{"a struct, not its fields", "Square{", true,
			[]string{"Square {\n  side", "Square {\n  fn", "Square{"}},
		{"a trait", "Shape for", true,
			[]string{"Shape {", "Shape for"}},
		{"a field, read, written and initialised", "count: i32", true,
			[]string{"count: i32", "count += by", "count: 0 }", "count)"}},
		{"a field from a read", "count)", false,
			[]string{"count += by", "count: 0 }", "count)"}},
		{"a struct, including the owner of its methods", "Counter {", true,
			[]string{"Counter {", "Counter::bump", "Counter{ count"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := c.references(uri, referencesSource, test.needle, test.includeDeclaration)
			want := offsetsOf(t, referencesSource, test.want...)
			if fmt.Sprint(got[uri]) != fmt.Sprint(want) || len(got) != 1 {
				t.Fatalf("references at %q: got %v, want %v", test.needle, got, want)
			}
		})
	}
}

func TestReferencesAcrossOpenFiles(t *testing.T) {
	c := newClient(t)
	c.initialize()

	directory := t.TempDir()
	library := "export fn helper(): i32 {\n  return 4\n}\n"
	libraryPath := filepath.Join(directory, "lib.gyro")
	source := "import { helper } from \"./lib\"\n\nfn main() {\n  var x = helper()\n  var y = helper()\n}\n"
	mainPath := filepath.Join(directory, "main.gyro")
	for path, text := range map[string]string{libraryPath: library, mainPath: source} {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	libraryURI, mainURI := uriFromPath(libraryPath), uriFromPath(mainPath)
	c.open(libraryURI, library)
	c.waitDiagnostics(libraryURI)
	c.open(mainURI, source)
	c.waitDiagnostics(mainURI)

	result := c.call("textDocument/references", fmt.Sprintf(
		`{"textDocument":{"uri":%q},"position":%s,"context":{"includeDeclaration":true}}`,
		libraryURI, position(t, library, "helper")))
	var locations []Location
	if err := json.Unmarshal(result, &locations); err != nil {
		t.Fatalf("bad references %s: %v", result, err)
	}
	var got []string
	for _, location := range locations {
		got = append(got, fmt.Sprintf("%s:%d:%d", filepath.Base(pathFromURI(location.URI)),
			location.Range.Start.Line, location.Range.Start.Character))
	}
	want := []string{"lib.gyro:0:10", "main.gyro:3:10", "main.gyro:4:10"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReferencesInFilesThatAreNotOpen(t *testing.T) {
	c := newClient(t)
	c.initialize()

	directory := t.TempDir()
	library := "export fn helper(): i32 {\n  var local = 4\n  return local\n}\n"
	libraryPath := filepath.Join(directory, "lib.gyro")
	user := "import { helper } from \"./lib\"\n\nfn main() {\n  var local = helper()\n}\n"
	stranger := "fn main() {\n  var local = 1\n}\n"
	for name, text := range map[string]string{"lib.gyro": library, "user.gyro": user, "stranger.gyro": stranger} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Only the library is open; user.gyro has to be found on disk.
	libraryURI := uriFromPath(libraryPath)
	c.open(libraryURI, library)
	c.waitDiagnostics(libraryURI)

	describe := func(needle string) []string {
		result := c.call("textDocument/references", fmt.Sprintf(
			`{"textDocument":{"uri":%q},"position":%s,"context":{"includeDeclaration":true}}`,
			libraryURI, position(t, library, needle)))
		var locations []Location
		if err := json.Unmarshal(result, &locations); err != nil {
			t.Fatalf("bad references %s: %v", result, err)
		}
		var got []string
		for _, location := range locations {
			got = append(got, fmt.Sprintf("%s:%d:%d", filepath.Base(pathFromURI(location.URI)),
				location.Range.Start.Line, location.Range.Start.Character))
		}
		return got
	}

	if got, want := describe("helper"), []string{"lib.gyro:0:10", "user.gyro:3:14"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("an exported function: got %v, want %v", got, want)
	}
	// A local is visible only in its own file, whatever other files call
	// their own locals.
	if got, want := describe("local\n"), []string{"lib.gyro:1:6", "lib.gyro:2:9"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("a local: got %v, want %v", got, want)
	}
}

func (c *client) formatting(uri string) []textEdit {
	c.t.Helper()
	result := c.call("textDocument/formatting", fmt.Sprintf(
		`{"textDocument":{"uri":%q},"options":{"tabSize":2,"insertSpaces":true}}`, uri))
	var edits []textEdit
	if err := json.Unmarshal(result, &edits); err != nil {
		c.t.Fatalf("bad edits %s: %v", result, err)
	}
	return edits
}

func TestFormatting(t *testing.T) {
	c := newClient(t)
	capabilities := string(c.call("initialize", `{"rootUri":null}`))
	if !strings.Contains(capabilities, `"documentFormattingProvider":true`) {
		t.Fatalf("formatting is not advertised: %s", capabilities)
	}
	c.notify("initialized", `{}`)

	const messy = "fn main() {\n  var x = 1+2   // sum\n}\n"
	const tidy = "fn main() {\n    var x = 1 + 2 // sum\n}\n"

	uri, _ := tempURI(t, "main.gyro", messy)
	c.open(uri, messy)
	c.waitDiagnostics(uri)

	edits := c.formatting(uri)
	if len(edits) != 1 {
		t.Fatalf("want one edit, got %+v", edits)
	}
	if edits[0].NewText != tidy {
		t.Fatalf("formatted to %q, want %q", edits[0].NewText, tidy)
	}
	wantRange := Range{Start: Position{0, 0}, End: Position{3, 0}}
	if edits[0].Range != wantRange {
		t.Fatalf("edit covers %+v, want the whole document %+v", edits[0].Range, wantRange)
	}

	// Already formatted: nothing to do.
	c.change(uri, tidy)
	c.waitDiagnostics(uri)
	if edits := c.formatting(uri); len(edits) != 0 {
		t.Fatalf("formatted text got edits: %+v", edits)
	}

	// A buffer that does not parse is left alone.
	broken := "fn main( {\n  var x =\n"
	c.change(uri, broken)
	c.waitDiagnostics(uri)
	if edits := c.formatting(uri); len(edits) != 0 {
		t.Fatalf("broken text got edits: %+v", edits)
	}
}
