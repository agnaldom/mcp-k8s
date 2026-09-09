package tools

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agnaldom/mcp-k8s/internal/services"
)

type apiResourcesService interface {
	List(ctx context.Context, cluster string) ([]services.APIResourceEntry, error)
}

// RegisterAPIResourcesTool adds k8s_api_resources (spec §5.3): discovery
// with effective permissions, not just cluster capabilities.
func RegisterAPIResourcesTool(s *server.MCPServer, svc apiResourcesService) {
	s.AddTool(
		mcp.NewTool("k8s_api_resources",
			mcp.WithDescription("API resources with effective permissions (SelfSubjectAccessReview): verbs are what the cluster supports, allowed is what this identity can do, policyBlocked marks local-policy denial."),
			mcp.WithString("cluster", mcp.Required(), mcp.Description("Cluster name from k8s_cluster_list")),
			readOnlyAnnotation(),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			cluster := req.GetString("cluster", "")
			entries, err := svc.List(ctx, cluster)
			meta := Meta{Timestamp: start, DurationMs: time.Since(start).Milliseconds()}
			if err != nil {
				return result(NewErrorEnvelope(Cluster{Name: cluster}, mapError(err), meta))
			}
			return result(NewEnvelope(Cluster{Name: cluster}, map[string]any{"resources": entries}, meta))
		},
	)
}
