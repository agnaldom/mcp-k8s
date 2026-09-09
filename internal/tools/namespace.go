package tools

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type namespaceService interface {
	List(ctx context.Context, cluster string) ([]string, error)
}

// RegisterNamespaceTools adds k8s_namespace_list (spec §13 step 08):
// namespaces after local policy.
func RegisterNamespaceTools(s *server.MCPServer, svc namespaceService) {
	s.AddTool(
		mcp.NewTool("k8s_namespace_list",
			mcp.WithDescription("List namespaces visible after local policy (spec §7)."),
			mcp.WithString("cluster", mcp.Required(), mcp.Description("Cluster name from k8s_cluster_list")),
			readOnlyAnnotation(),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			cluster := req.GetString("cluster", "")
			namespaces, err := svc.List(ctx, cluster)
			meta := Meta{Timestamp: start, DurationMs: time.Since(start).Milliseconds()}
			if err != nil {
				return result(NewErrorEnvelope(Cluster{Name: cluster}, mapError(err), meta))
			}
			return result(NewEnvelope(Cluster{Name: cluster}, map[string]any{"namespaces": namespaces}, meta))
		},
	)
}
