package tools

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agnaldom/mcp-k8s/internal/services"
)

type eventsService interface {
	Events(ctx context.Context, opts services.EventsOptions) (*services.EventsResult, error)
}

// RegisterEventsTool adds k8s_events_list (spec §3.3, §13 step 13):
// normalized events with a declared coverage window.
func RegisterEventsTool(s *server.MCPServer, svc eventsService) {
	s.AddTool(
		mcp.NewTool("k8s_events_list",
			mcp.WithDescription("Normalized events with a coverage window. coverage.complete=false means absence must be treated as unknown, not as 'no failures' (spec §3.3)."),
			mcp.WithString("cluster", mcp.Required(), mcp.Description("Cluster name from k8s_cluster_list")),
			mcp.WithString("namespace", mcp.Description("Namespace (omit for all)")),
			mcp.WithString("since", mcp.Description("Window like 30m, 1h")),
			readOnlyAnnotation(),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			cluster := req.GetString("cluster", "")
			opts := services.EventsOptions{
				Cluster:   cluster,
				Namespace: req.GetString("namespace", ""),
			}
			if since := req.GetString("since", ""); since != "" {
				d, err := time.ParseDuration(since)
				if err != nil {
					return result(NewErrorEnvelope(Cluster{Name: cluster},
						NewError(ErrInvalidArgument, `since must be a duration like "30m"`), Meta{Timestamp: start}))
				}
				opts.Since = &d
			}
			res, err := svc.Events(ctx, opts)
			meta := Meta{Timestamp: start, DurationMs: time.Since(start).Milliseconds()}
			if err != nil {
				return result(NewErrorEnvelope(Cluster{Name: cluster}, mapError(err), meta))
			}
			env := NewEnvelope(Cluster{Name: cluster}, map[string]any{"events": res.Items}, meta)
			env.Coverage = &Coverage{
				From:     res.Coverage.From,
				To:       res.Coverage.To,
				Complete: res.Coverage.Complete,
				Reason:   res.Coverage.Reason,
			}
			return result(env)
		},
	)
}
