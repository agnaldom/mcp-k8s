package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func successResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.TextContent{
			Type: "text", Text: text,
		}},
	}
}

func callReq(tool string, args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: tool, Arguments: args},
	}
}

func TestMiddlewareRecordsSuccess(t *testing.T) {
	var buf bytes.Buffer
	rec := NewRecorder(&buf)
	handler := rec.Middleware()(func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return successResult(`{"apiVersion":"mcp-k8s/v1","data":{"ok":true}}`), nil
	})

	res, err := handler(context.Background(), callReq("k8s_resource_get", map[string]any{
		"cluster": "prod", "namespace": "payments", "name": "payments-api",
		"kind": "Deployment",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("handler result lost")
	}

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("audit line is not valid JSON: %v (%q)", err, buf.String())
	}
	if entry.Tool != "k8s_resource_get" || entry.Cluster != "prod" ||
		entry.Namespace != "payments" || entry.Resource != "payments-api" {
		t.Errorf("identity fields: %+v", entry)
	}
	if entry.Result != ResultSuccess || entry.ErrorCode != "" {
		t.Errorf("result: %+v", entry)
	}
	if entry.RequestID == "" {
		t.Error("requestId must be generated")
	}
	if entry.BytesOut == 0 || entry.DurationMs < 0 {
		t.Errorf("size/duration: %+v", entry)
	}
}

func TestMiddlewareRecordsEnvelopeError(t *testing.T) {
	var buf bytes.Buffer
	rec := NewRecorder(&buf)
	handler := rec.Middleware()(func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return successResult(`{"apiVersion":"mcp-k8s/v1","error":{"code":"POLICY_DENIED","message":"denied","retryable":false}}`), nil
	})

	_, err := handler(context.Background(), callReq("k8s_pod_logs", map[string]any{
		"cluster": "prod", "namespace": "payments", "name": "payments-api-x1",
	}))
	if err != nil {
		t.Fatal(err)
	}

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Result != ResultError || entry.ErrorCode != "POLICY_DENIED" {
		t.Errorf("error entry: %+v", entry)
	}
}

func TestMiddlewareRecordsHandlerError(t *testing.T) {
	var buf bytes.Buffer
	rec := NewRecorder(&buf)
	handler := rec.Middleware()(func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("boom")
	})

	if _, err := handler(context.Background(), callReq("k8s_cluster_list", map[string]any{})); err == nil {
		t.Fatal("expected handler error to propagate")
	}
	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Result != ResultError {
		t.Errorf("entry: %+v", entry)
	}
	if entry.Cluster != "" || entry.Namespace != "" || entry.Resource != "" {
		t.Errorf("identity fields must be empty when args are absent: %+v", entry)
	}
}

func TestEntryNeverCarriesContent(t *testing.T) {
	// The response text can be arbitrary (even something that looks
	// like a credential); the audit entry shape has no field that
	// could hold it.
	secretLooking := `{"token":"hunter2","data":{"kubeconfig":"LEAKED","log":"line1"}}`
	var buf bytes.Buffer
	rec := NewRecorder(&buf)
	handler := rec.Middleware()(func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return successResult(secretLooking), nil
	})
	if _, err := handler(context.Background(), callReq("k8s_pod_logs", map[string]any{"name": "x"})); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "hunter2") || strings.Contains(buf.String(), "LEAKED") || strings.Contains(buf.String(), "line1") {
		t.Fatalf("audit entry leaked response content: %s", buf.String())
	}
}

func TestRecordSerializesWrites(t *testing.T) {
	var buf bytes.Buffer
	rec := NewRecorder(&buf)
	done := make(chan struct{}, 2)
	go func() {
		rec.Record(Entry{Timestamp: time.Now(), RequestID: "a", Tool: "t1", Result: ResultSuccess})
		done <- struct{}{}
	}()
	go func() {
		rec.Record(Entry{Timestamp: time.Now(), RequestID: "b", Tool: "t2", Result: ResultSuccess})
		done <- struct{}{}
	}()
	<-done
	<-done
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), buf.String())
	}
}
