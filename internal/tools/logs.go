package tools

import (
	"context"
	"errors"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agnaldom/mcp-k8s/internal/services"
)

type logsService interface {
	Logs(ctx context.Context, opts services.PodLogsOptions) (*services.PodLogsResult, error)
}

// RegisterLogsTool adds k8s_pod_logs (spec §5.4).
func RegisterLogsTool(s *server.MCPServer, svc logsService) {
	s.AddTool(
		mcp.NewTool("k8s_pod_logs",
			mcp.WithDescription("Container logs. One container is auto-selected; several require container or allContainers. previous=true shows the crashed container's prior logs."),
			mcp.WithString("cluster", mcp.Required(), mcp.Description("Cluster name from k8s_cluster_list")),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace")),
			mcp.WithString("pod", mcp.Required(), mcp.Description("Pod name")),
			mcp.WithString("container", mcp.Description("Container name (required when several)")),
			mcp.WithBoolean("previous", mcp.Description("Logs of the previous terminated container (CrashLoopBackOff/OOMKilled)")),
			mcp.WithBoolean("allContainers", mcp.Description("Aggregate logs of all containers")),
			mcp.WithString("since", mcp.Description("Window like 30m, 1h (overrides nothing if unset)")),
			mcp.WithNumber("tailLines", mcp.Description("Default 500, max 5000")),
			mcp.WithBoolean("timestamps", mcp.Description("Include timestamps (default true)")),
			readOnlyAnnotation(),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			cluster := req.GetString("cluster", "")
			opts := services.PodLogsOptions{
				Cluster:       cluster,
				Namespace:     req.GetString("namespace", ""),
				Pod:           req.GetString("pod", ""),
				Container:     req.GetString("container", ""),
				Previous:      req.GetBool("previous", false),
				AllContainers: req.GetBool("allContainers", false),
				TailLines:     int64(req.GetFloat("tailLines", 0)),
				Timestamps:    req.GetBool("timestamps", true),
			}
			if since := req.GetString("since", ""); since != "" {
				d, err := time.ParseDuration(since)
				if err != nil {
					return result(NewErrorEnvelope(Cluster{Name: cluster},
						NewError(ErrInvalidArgument, `since must be a duration like "30m"`), Meta{Timestamp: start}))
				}
				secs := int64(d.Seconds())
				opts.SinceSeconds = &secs
			}
			res, err := svc.Logs(ctx, opts)
			meta := Meta{Timestamp: start, DurationMs: time.Since(start).Milliseconds()}
			if err != nil {
				return result(NewErrorEnvelope(Cluster{Name: cluster}, mapLogsError(err), meta))
			}
			meta.Truncated = res.Truncated
			meta.DroppedLines = res.DroppedLines
			return result(NewEnvelope(Cluster{Name: cluster}, res, meta))
		},
	)
}

func mapLogsError(err error) *Error {
	switch {
	case errors.Is(err, services.ErrInvalidArgument):
		return NewError(ErrInvalidArgument, err.Error())
	default:
		return mapError(err)
	}
}
