package tools

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agnaldom/mcp-k8s/internal/services"
)

type workloadContextService interface {
	Context(ctx context.Context, opts services.WorkloadOptions) (*services.WorkloadResult, error)
}

// RegisterWorkloadContextTool adds k8s_workload_context (spec §5.1, §13
// step 17): one call, the full troubleshooting context of a workload —
// state, pods, relationships, events, metrics, and deterministic
// signals. Logs are deliberately not included: k8s_pod_logs exists and
// this tool says which pods are worth reading.
func RegisterWorkloadContextTool(s *server.MCPServer, svc workloadContextService) {
	s.AddTool(
		mcp.NewTool("k8s_workload_context",
			mcp.WithDescription("One call, the full troubleshooting context of a workload: state, conditions, replicas, pods (state, restarts, requests/limits, node), relationships (owners, Services, EndpointSlices, HPA, PDB, PVC), normalized events with coverage, per-container metrics when asked, and deterministic signals with declared thresholds. No logs — use k8s_pod_logs on the pods this tool flags."),
			mcp.WithString("cluster", mcp.Required(), mcp.Description("Cluster name from k8s_cluster_list")),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace of the workload")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Pod, Deployment, StatefulSet, DaemonSet, Job, or CronJob")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name")),
			mcp.WithBoolean("includeMetrics", mcp.Description("Include per-container consumption from the metrics API (default false)")),
			mcp.WithNumber("eventLimit", mcp.Description("Cap the newest events returned (default: server maximum)")),
			readOnlyAnnotation(),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			cluster := req.GetString("cluster", "")
			res, err := svc.Context(ctx, services.WorkloadOptions{
				Cluster:        cluster,
				Namespace:      req.GetString("namespace", ""),
				Kind:           req.GetString("kind", ""),
				Name:           req.GetString("name", ""),
				IncludeMetrics: req.GetBool("includeMetrics", false),
				EventLimit:     int(req.GetFloat("eventLimit", 0)),
			})
			meta := Meta{Timestamp: start, DurationMs: time.Since(start).Milliseconds()}
			if err != nil {
				return result(NewErrorEnvelope(Cluster{Name: cluster}, mapResourceError(err), meta))
			}
			return result(NewEnvelope(Cluster{Name: cluster}, map[string]any{
				"workload":      res.Workload,
				"pods":          res.Pods,
				"relationships": res.Relationships,
				"events":        res.Events,
				"metrics":       res.Metrics,
				"signals":       res.Signals,
				"coverage":      res.Coverage,
				"unavailable":   res.Unavailable,
			}, meta))
		},
	)
}
