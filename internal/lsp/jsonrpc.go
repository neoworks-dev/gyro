// Package lsp implements the Gyro language server: a JSON-RPC 2.0 server
// speaking the Language Server Protocol over stdio. It serves both Gyro
// source files and METALS IR files.
//
// The protocol layer is written by hand. The server needs a dozen methods,
// and the framing is trivial: each message is a Content-Length header, a
// blank line, and a JSON body.
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// request is an incoming call or notification. Notifications carry no ID.
type request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

func (r *request) isNotification() bool { return r.ID == nil }

// responseError is a JSON-RPC error object.
type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// conn frames messages onto a stream. Writes are serialized so that
// diagnostics published mid-request cannot interleave with the response.
type conn struct {
	reader *bufio.Reader
	writer io.Writer
	mutex  sync.Mutex
}

func newConn(reader io.Reader, writer io.Writer) *conn {
	return &conn{reader: bufio.NewReader(reader), writer: writer}
}

// read blocks until the next complete message arrives.
func (c *conn) read() (*request, error) {
	contentLength := -1
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, value, found := strings.Cut(line, ":"); found {
			if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
				contentLength, err = strconv.Atoi(strings.TrimSpace(value))
				if err != nil {
					return nil, fmt.Errorf("bad Content-Length: %v", err)
				}
			}
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("message without Content-Length")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(c.reader, body); err != nil {
		return nil, err
	}

	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("bad message: %v", err)
	}
	return &req, nil
}

func (c *conn) write(message any) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()
	if _, err := fmt.Fprintf(c.writer, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.writer.Write(body)
	return err
}

// respond sends a success response. A nil result marshals as JSON null,
// which is what the protocol wants for empty answers.
func (c *conn) respond(id *json.RawMessage, result any) error {
	return c.write(struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id"`
		Result  any              `json:"result"`
	}{"2.0", id, result})
}

func (c *conn) respondError(id *json.RawMessage, code int, message string) error {
	return c.write(struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id"`
		Error   *responseError   `json:"error"`
	}{"2.0", id, &responseError{Code: code, Message: message}})
}

// notify sends a server-initiated notification.
func (c *conn) notify(method string, params any) error {
	return c.write(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", method, params})
}
