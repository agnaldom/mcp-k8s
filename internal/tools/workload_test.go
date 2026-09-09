package tools

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/agnaldom/mcp-k8s/internal/services"
	"github.com/agnaldom/mcp-k8s/internal/signals"
)

type stubWorkloadService struct {
	res *services.WorkloadResult
	err error
}

func (s *stubWorkloadService) Context(_ context.Context, _ services.WorkloadOptions) (*services.WorkloadResult, error) {
	return s.res, s.err
}

func TestWorkloadContextToolReturnsBundle(t *testing.T) {
	srv := server.NewMCPServer("test", "v0")
	RegisterWorkloadContextTool(srv, &stubWorkloadService{res: &services.WorkloadResult{
		Workload: &services.WorkloadBlock{
			Kind: "Deployment", Name: "payments-api", Namespace: "payments",
			Replicas: &services.ReplicaStatus{Desired: 3, Available: 1},
		},
		Pods: &services.PodsBlock{
			Total:       2,
			PhaseCounts: map[string]int{"Running": 1, "Pending": 1},
			Items: []services.PodDetail{{
				Name: "payments-api-x1", Phase: "Running", Node: "node-1",
			}},
			Truncated: 0,
		},
		Signals: []signals.Signal{{
			Type:     signals.WorkloadUnavailable,
			Severity: signals.SeverityCritical,
			Rule:     signals.Rule{Expression: "available < desired"},
			Observed: map[string]any{"desired": 3, "available": 1},
			Source:   "deployment/payments/payments-api .status (replicas)",
		}},
		Coverage: services.WorkloadCoverage{
			Events: services.CoverageInfo{Complete: true},
		},
	}})

	res := callTool(t, srv, "k8s_workload_context", map[string]any{
		"cluster": "prod", "namespace": "payments", "kind": "Deployment", "name": "payments-api",
		"includeMetrics": true, "eventLimit": 10,
	})
	env := decodeEnvelope(t, res)
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	data, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data: %T", env.Data)
	}
	if data["workload"] == nil || data["pods"] == nil || data["signals"] == nil || data["coverage"] == nil {
		t.Errorf("bundle blocks missing: %v", data)
	}
	sig, ok := data["signals"].([]any)
	if !ok || len(sig) != 1 {
		t.Fatalf("signals: %v", data["signals"])
	}
	first, ok := sig[0].(map[string]any)
	if !ok || first["type"] != signals.WorkloadUnavailable {
		t.Errorf("signal: %v", sig[0])
	}
}

func TestWorkloadContextToolMapsNotFound(t *testing.T) {
	srv := server.NewMCPServer("test", "v0")
	RegisterWorkloadContextTool(srv, &stubWorkloadService{err: services.ErrResourceNotFound})

	res := callTool(t, srv, "k8s_workload_context", map[string]any{
		"cluster": "prod", "namespace": "payments", "kind": "Deployment", "name": "gone",
	})
	env := decodeEnvelope(t, res)
	if env.Error == nil || env.Error.Code != ErrResourceNotFound {
		t.Errorf("error: %+v", env.Error)
	}
}
