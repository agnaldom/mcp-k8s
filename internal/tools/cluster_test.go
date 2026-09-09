package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
	"github.com/agnaldom/mcp-k8s/internal/services"
)

type stubClusterService struct {
	list []services.Summary
	err  error
	info *services.Info
}

func (s *stubClusterService) List(_ context.Context) ([]services.Summary, error) {
	return s.list, s.err
}

func (s *stubClusterService) Info(_ context.Context, _ string) (*services.Info, error) {
	return s.info, s.err
}

// callTool invokes a tool through the MCPServer dispatch, exactly as a
// stdio client would.
func callTool(t *testing.T, srv *server.MCPServer, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcp.NewRequestId(1),
		Method:  string(mcp.MethodToolsCall),
		Params:  mcp.CallToolParams{Name: tool, Arguments: args},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	resp := srv.HandleMessage(context.Background(), raw)
	var resultAny any
	switch r := resp.(type) {
	case *mcp.JSONRPCResponse:
		resultAny = r.Result
	case mcp.JSONRPCResponse:
		resultAny = r.Result
	default:
		if errMsg, isErr := resp.(mcp.JSONRPCError); isErr {
			eb, _ := json.Marshal(errMsg)
			t.Fatalf("JSON-RPC error: %s", eb)
		}
		t.Fatalf("unexpected response type %T", resp)
	}
	result, ok := resultAny.(*mcp.CallToolResult)
	if !ok {
		t.Fatalf("unexpected result type %T", resultAny)
	}
	return result
}

func decodeEnvelope(t *testing.T, res *mcp.CallToolResult) *Envelope {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	var env Envelope
	if err := json.Unmarshal([]byte(text.Text), &env); err != nil {
		t.Fatalf("envelope is not valid JSON: %v", err)
	}
	return &env
}

func newClusterTestServer(svc clusterService) *server.MCPServer {
	srv := server.NewMCPServer("mcp-k8s-test", "dev")
	RegisterClusterTools(srv, svc)
	return srv
}

func TestClusterListEnvelope(t *testing.T) {
	svc := &stubClusterService{list: []services.Summary{
		{Name: "prod", Default: true, Source: "kubeconfig"},
	}}
	res := callTool(t, newClusterTestServer(svc), "k8s_cluster_list", nil)
	env := decodeEnvelope(t, res)
	if env.APIVersion != APIVersion {
		t.Errorf("apiVersion: got %q", env.APIVersion)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	raw, _ := json.Marshal(env.Data)
	var data struct {
		Clusters []services.Summary `json:"clusters"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Clusters) != 1 || data.Clusters[0].Name != "prod" || !data.Clusters[0].Default {
		t.Errorf("clusters: %+v", data.Clusters)
	}
}

func TestClusterInfoMapsNotFound(t *testing.T) {
	svc := &stubClusterService{err: kubernetes.ErrClusterNotFound}
	res := callTool(t, newClusterTestServer(svc), "k8s_cluster_info", map[string]any{"cluster": "staging"})
	env := decodeEnvelope(t, res)
	if env.Error == nil || env.Error.Code != ErrClusterNotFound {
		t.Fatalf("expected CLUSTER_NOT_FOUND, got %+v", env.Error)
	}
	if env.Data != nil {
		t.Error("error envelope must not carry data")
	}
}

func TestClusterInfoSuccess(t *testing.T) {
	svc := &stubClusterService{info: &services.Info{
		Cluster: "prod",
		Version: &services.Version{GitVersion: "v1.37.0"},
	}}
	res := callTool(t, newClusterTestServer(svc), "k8s_cluster_info", map[string]any{"cluster": "prod"})
	env := decodeEnvelope(t, res)
	if env.Cluster.Name != "prod" {
		t.Errorf("cluster: got %+v", env.Cluster)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
}

func TestToolNamesSatisfyContract(t *testing.T) {
	for _, name := range []string{"k8s_cluster_list", "k8s_cluster_info"} {
		if !ValidName(name) {
			t.Errorf("%s violates the naming contract (spec §3.1)", name)
		}
	}
}
