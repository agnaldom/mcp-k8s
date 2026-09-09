package tools

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agnaldom/mcp-k8s/internal/services"
)

type signalsService interface {
	Evaluate(ctx context.Context, opts services.SignalOptions) (*services.SignalResult, error)
}

// RegisterSignalsTool adds k8s_signals (spec §5.2, §13 step 16):
// deterministic signals, each carrying the rule that fired (with the
// effective threshold), the observed value, and the exact source field.
// A signal is a fact plus a declared, configurable rule — never a bare
// judgment.
func RegisterSignalsTool(s *server.MCPServer, svc signalsService) {
	s.AddTool(
		mcp.NewTool("k8s_signals",
			mcp.WithDescription("Deterministic signals for a workload: each signal carries the rule that fired (with the effective threshold), the observed value, and the source field. Evaluate the facts yourself if you disagree with a threshold — it is in the response (spec §5.2)."),
			mcp.WithString("cluster", mcp.Required(), mcp.Description("Cluster name from k8s_cluster_list")),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace of the workload")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload Kind: Pod, Deployment, StatefulSet, DaemonSet, Job, CronJob")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name")),
			readOnlyAnnotation(),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			cluster := req.GetString("cluster", "")
			res, err := svc.Evaluate(ctx, services.SignalOptions{
				Cluster:   cluster,
				Namespace: req.GetString("namespace", ""),
				Kind:      req.GetString("kind", ""),
				Name:      req.GetString("name", ""),
			})
			meta := Meta{Timestamp: start, DurationMs: time.Since(start).Milliseconds()}
			if err != nil {
				return result(NewErrorEnvelope(Cluster{Name: cluster}, mapResourceError(err), meta))
			}
			return result(NewEnvelope(Cluster{Name: cluster}, map[string]any{
				"workload":    res.Workload,
				"signals":     res.Signals,
				"unavailable": res.Unavailable,
			}, meta))
		},
	)
}
