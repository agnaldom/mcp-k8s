package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
	"github.com/agnaldom/mcp-k8s/internal/policy"
	"github.com/agnaldom/mcp-k8s/internal/services"
)

// clusterService is the slice of the services layer these tools need.
type clusterService interface {
	List(ctx context.Context) ([]services.Summary, error)
	Info(ctx context.Context, cluster string) (*services.Info, error)
}

// readOnlyAnnotation marks every tool in this server as read-only and
// non-destructive (spec §2): no tool here mutates anything, ever.
func readOnlyAnnotation() mcp.ToolOption {
	tru, fls := true, false
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    &tru,
		DestructiveHint: &fls,
		IdempotentHint:  &tru,
	})
}

// RegisterClusterTools adds k8s_cluster_list and k8s_cluster_info
// (spec §4, §13 step 07).
func RegisterClusterTools(s *server.MCPServer, svc clusterService) {
	s.AddTool(
		mcp.NewTool("k8s_cluster_list",
			mcp.WithDescription("List available clusters/contexts, with the default flagged."),
			readOnlyAnnotation(),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			clusters, err := svc.List(ctx)
			meta := Meta{Timestamp: start, DurationMs: time.Since(start).Milliseconds()}
			if err != nil {
				return result(NewErrorEnvelope(Cluster{}, mapError(err), meta))
			}
			return result(NewEnvelope(Cluster{Name: "*"}, map[string]any{"clusters": clusters}, meta))
		},
	)

	s.AddTool(
		mcp.NewTool("k8s_cluster_info",
			mcp.WithDescription("Kubernetes version for the named cluster."),
			mcp.WithString("cluster", mcp.Required(), mcp.Description("Cluster name from k8s_cluster_list")),
			readOnlyAnnotation(),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			cluster := req.GetString("cluster", "")
			info, err := svc.Info(ctx, cluster)
			meta := Meta{Timestamp: start, DurationMs: time.Since(start).Milliseconds()}
			if err != nil {
				return result(NewErrorEnvelope(Cluster{Name: cluster}, mapError(err), meta))
			}
			return result(NewEnvelope(Cluster{Name: cluster}, info, meta))
		},
	)
}

// result serializes an envelope as the tool's text payload.
func result(env *Envelope) (*mcp.CallToolResult, error) {
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(raw)), nil
}

// mapError translates transport-neutral sentinel errors from the
// kubernetes/services layers into the API error catalog (spec §3.6).
func mapError(err error) *Error {
	switch {
	case errors.Is(err, kubernetes.ErrClusterNotFound):
		return NewError(ErrClusterNotFound, err.Error())
	case errors.Is(err, kubernetes.ErrExecPluginBlocked):
		return NewError(ErrPolicyDenied, err.Error())
	case errors.Is(err, policy.ErrDenied):
		return NewError(ErrPolicyDenied, err.Error())
	default:
		return NewError(ErrInternal, err.Error())
	}
}
