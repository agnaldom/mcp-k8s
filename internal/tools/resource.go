package tools

import (
	"context"
	"errors"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
	"github.com/agnaldom/mcp-k8s/internal/services"
)

type resourceListService interface {
	List(ctx context.Context, opts services.ResourceListOptions) (*services.ResourceListResult, error)
}

// paginationBlock mirrors the pagination contract (spec §3.5): a list
// larger than the limit pages normally and returns pagination.continue.
type paginationBlock struct {
	Continue string `json:"continue,omitempty"`
}

// RegisterResourceListTool adds k8s_resource_list (spec §13 step 09).
func RegisterResourceListTool(s *server.MCPServer, svc resourceListService) {
	s.AddTool(
		mcp.NewTool("k8s_resource_list",
			mcp.WithDescription("List any Kind via the dynamic client, with pagination and views."),
			mcp.WithString("cluster", mcp.Required(), mcp.Description("Cluster name from k8s_cluster_list")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Kind to list, e.g. Deployment, Pod, CronJob")),
			mcp.WithString("namespace", mcp.Description("Namespace (omit for all namespaces)")),
			mcp.WithString("view", mcp.Description("Projection: summary (default) or full"), mcp.Enum(services.ViewSummary, services.ViewFull)),
			mcp.WithNumber("limit", mcp.Description("Page size (server clamps to its maximum)")),
			mcp.WithString("continue", mcp.Description("Continuation token from a previous page")),
			readOnlyAnnotation(),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			cluster := req.GetString("cluster", "")
			view := req.GetString("view", services.ViewSummary)
			if view != services.ViewSummary && view != services.ViewFull {
				meta := Meta{Timestamp: start}
				return result(NewErrorEnvelope(Cluster{Name: cluster},
					NewError(ErrInvalidArgument, `view must be "summary" or "full"`), meta))
			}
			opts := services.ResourceListOptions{
				Cluster:       cluster,
				Kind:          req.GetString("kind", ""),
				Namespace:     req.GetString("namespace", ""),
				View:          view,
				Limit:         int(req.GetFloat("limit", 0)),
				ContinueToken: req.GetString("continue", ""),
			}
			res, err := svc.List(ctx, opts)
			meta := Meta{Timestamp: start, DurationMs: time.Since(start).Milliseconds(), View: view}
			if err != nil {
				return result(NewErrorEnvelope(Cluster{Name: cluster}, mapResourceError(err), meta))
			}
			data := map[string]any{"items": res.Items}
			if res.ContinueToken != "" {
				data["pagination"] = paginationBlock{Continue: res.ContinueToken}
			}
			return result(NewEnvelope(Cluster{Name: cluster}, data, meta))
		},
	)
}

// mapResourceError extends the shared error mapping with resource-list
// specific sentinels.
func mapResourceError(err error) *Error {
	switch {
	case errors.Is(err, services.ErrResourceTypeNotFound):
		return NewError(ErrResourceTypeNotFound, err.Error())
	default:
		if sizeErr := (*kubernetes.SizeError)(nil); errors.As(err, &sizeErr) {
			return NewError(ErrResponseTooLarge, sizeErr.Error())
		}
		return mapError(err)
	}
}
