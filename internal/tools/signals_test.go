package tools

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/agnaldom/mcp-k8s/internal/services"
	"github.com/agnaldom/mcp-k8s/internal/signals"
)

type stubSignalsService struct {
	res *services.SignalResult
	err error
}

func (s *stubSignalsService) Evaluate(_ context.Context, _ services.SignalOptions) (*services.SignalResult, error) {
	return s.res, s.err
}

func TestSignalsToolReturnsSignals(t *testing.T) {
	srv := server.NewMCPServer("test", "v0")
	RegisterSignalsTool(srv, &stubSignalsService{res: &services.SignalResult{
		Workload: "payments/Deployment/payments-api",
		Signals: []signals.Signal{{
			Type:     signals.ContainerCrashLoop,
			Severity: signals.SeverityCritical,
			Rule:     signals.Rule{Expression: `state.waiting.reason == "CrashLoopBackOff"`},
			Observed: map[string]any{"container": "api", "restartCount": 17},
			Source:   "pod/payments/payments-api-x1 .status.containerStatuses[api].state.waiting.reason",
		}},
	}})

	res := callTool(t, srv, "k8s_signals", map[string]any{
		"cluster": "prod", "namespace": "payments", "kind": "Deployment", "name": "payments-api",
	})
	env := decodeEnvelope(t, res)
	data, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data: %T", env.Data)
	}
	if data["workload"] != "payments/Deployment/payments-api" {
		t.Errorf("workload: %v", data["workload"])
	}
	items, ok := data["signals"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("signals: %v", data["signals"])
	}
	sig, ok := items[0].(map[string]any)
	if !ok || sig["type"] != signals.ContainerCrashLoop {
		t.Errorf("signal: %v", items[0])
	}
}

func TestSignalsToolMapsErrors(t *testing.T) {
	srv := server.NewMCPServer("test", "v0")
	RegisterSignalsTool(srv, &stubSignalsService{err: services.ErrResourceNotFound})

	res := callTool(t, srv, "k8s_signals", map[string]any{
		"cluster": "prod", "namespace": "payments", "kind": "Deployment", "name": "gone",
	})
	env := decodeEnvelope(t, res)
	if env.Error == nil || env.Error.Code != ErrResourceNotFound {
		t.Errorf("error: %+v", env.Error)
	}
}
