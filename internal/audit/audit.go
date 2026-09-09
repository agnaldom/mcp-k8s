// Package audit records one JSON entry per tool invocation (spec §10),
// even read-only ones. An entry is metadata only: tool, cluster,
// namespace, resource identity, result, duration, and response size.
// Content — log lines, tokens, kubeconfig, Secrets, Authorization
// headers — can never reach the audit trail because the Entry shape
// has no field for it and the middleware only reads tool arguments
// that name resources, never their values.
package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Entry is one audit record (spec §10).
type Entry struct {
	Timestamp  time.Time `json:"timestamp"`
	RequestID  string    `json:"requestId"`
	Tool       string    `json:"tool"`
	Cluster    string    `json:"cluster,omitempty"`
	Namespace  string    `json:"namespace,omitempty"`
	Resource   string    `json:"resource,omitempty"`
	Result     string    `json:"result"` // success | error
	ErrorCode  string    `json:"errorCode,omitempty"`
	DurationMs int64     `json:"durationMs"`
	BytesOut   int       `json:"bytesOut"`
}

// Result values for Entry.Result.
const (
	ResultSuccess = "success"
	ResultError   = "error"
)

// Recorder appends entries to a writer as JSON lines. Writes are
// serialized; a failing write is reported once on stderr and never
// breaks the tool call being audited.
type Recorder struct {
	mu sync.Mutex
	w  io.Writer
}

func NewRecorder(w io.Writer) *Recorder {
	return &Recorder{w: w}
}

// Record writes one JSON line for e.
func (r *Recorder) Record(e Entry) {
	line, err := json.Marshal(e)
	if err != nil {
		return // Entry is always marshalable; defensive only.
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, "%s\n", line)
}

// Middleware wraps every tool handler so each invocation produces
// exactly one audit entry, success or error.
func (r *Recorder) Middleware() server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			res, err := next(ctx, req)

			entry := Entry{
				Timestamp:  start.UTC(),
				RequestID:  newRequestID(),
				Tool:       req.Params.Name,
				Cluster:    req.GetString("cluster", ""),
				Namespace:  req.GetString("namespace", ""),
				Resource:   req.GetString("name", ""),
				Result:     ResultSuccess,
				DurationMs: time.Since(start).Milliseconds(),
			}
			if res != nil {
				entry.BytesOut = textBytes(res)
				if code, isErr := envelopeErrorCode(res); isErr {
					entry.Result = ResultError
					entry.ErrorCode = code
				}
			}
			if err != nil {
				entry.Result = ResultError
			}
			r.Record(entry)
			return res, err
		}
	}
}

// textBytes sums the serialized size of every text content block —
// the response size the client received.
func textBytes(res *mcp.CallToolResult) int {
	n := 0
	for _, c := range res.Content {
		if t, ok := c.(mcp.TextContent); ok {
			n += len(t.Text)
		}
	}
	return n
}

// envelopeErrorCode reports whether the result carries an mcp-k8s
// error envelope, and its code. Tool handlers return error envelopes
// as regular results (result envelope contract, spec §3.6), so the
// MCP IsError flag alone is not the signal.
func envelopeErrorCode(res *mcp.CallToolResult) (string, bool) {
	if len(res.Content) == 0 {
		return "", false
	}
	t, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		return "", false
	}
	var env struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(t.Text), &env); err != nil || env.Error == nil {
		return "", false
	}
	return env.Error.Code, true
}

// newRequestID returns a random hex ID for the invocation. The MCP
// JSON-RPC id is transport-scoped and not visible at the tool layer,
// so the audit trail uses its own identifier.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
