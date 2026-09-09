package tools

import (
	"context"
	"errors"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agnaldom/mcp-k8s/internal/services"
)

type metricsService interface {
	Pods(ctx context.Context, cluster, namespace string) ([]services.MetricsEntry, error)
	Nodes(ctx context.Context, cluster string) ([]services.MetricsEntry, error)
}

// RegisterMetricsTools adds k8s_metrics_pods and k8s_metrics_nodes
// (spec §13 step 14). Missing metrics-server is METRICS_UNAVAILABLE — a
// normal cluster state, not a failure (spec §3.6).
func RegisterMetricsTools(s *server.MCPServer, svc metricsService) {
	s.AddTool(
		mcp.NewTool("k8s_metrics_pods",
			mcp.WithDescription("Per-pod resource consumption (metrics API). METRICS_UNAVAILABLE means no metrics-server — continue without it."),
			mcp.WithString("cluster", mcp.Required(), mcp.Description("Cluster name from k8s_cluster_list")),
			mcp.WithString("namespace", mcp.Description("Namespace (omit for all)")),
			readOnlyAnnotation(),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			cluster := req.GetString("cluster", "")
			entries, err := svc.Pods(ctx, cluster, req.GetString("namespace", ""))
			meta := Meta{Timestamp: start, DurationMs: time.Since(start).Milliseconds()}
			if err != nil {
				return result(NewErrorEnvelope(Cluster{Name: cluster}, mapMetricsError(err), meta))
			}
			return result(NewEnvelope(Cluster{Name: cluster}, map[string]any{"pods": entries}, meta))
		},
	)
	s.AddTool(
		mcp.NewTool("k8s_metrics_nodes",
			mcp.WithDescription("Per-node resource consumption (metrics API)."),
			mcp.WithString("cluster", mcp.Required(), mcp.Description("Cluster name from k8s_cluster_list")),
			readOnlyAnnotation(),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			cluster := req.GetString("cluster", "")
			entries, err := svc.Nodes(ctx, cluster)
			meta := Meta{Timestamp: start, DurationMs: time.Since(start).Milliseconds()}
			if err != nil {
				return result(NewErrorEnvelope(Cluster{Name: cluster}, mapMetricsError(err), meta))
			}
			return result(NewEnvelope(Cluster{Name: cluster}, map[string]any{"nodes": entries}, meta))
		},
	)
}

func mapMetricsError(err error) *Error {
	if errors.Is(err, services.ErrMetricsUnavailable) {
		return NewError(ErrMetricsUnavailable, err.Error())
	}
	return mapError(err)
}
