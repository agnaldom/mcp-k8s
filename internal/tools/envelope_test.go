package tools

import (
	"encoding/json"
	"testing"
	"time"
)

func TestErrorRetryableFlags(t *testing.T) {
	cases := []struct {
		code      string
		retryable bool
	}{
		{ErrK8sTimeout, true},
		{ErrClusterUnavailable, true},
		{ErrRateLimited, true},
		{ErrInvalidArgument, false},
		{ErrClusterNotFound, false},
		{ErrClusterAuthFailed, false},
		{ErrK8sForbidden, false},
		{ErrPolicyDenied, false},
		{ErrResponseTooLarge, false},
		{ErrInternal, false},
	}
	for _, c := range cases {
		if got := NewError(c.code, "msg").Retryable; got != c.retryable {
			t.Errorf("%s: retryable=%v, want %v", c.code, got, c.retryable)
		}
	}
}

func TestErrorImplementsError(t *testing.T) {
	err := NewError(ErrPolicyDenied, "namespace kube-system denied by policy")
	if err.Error() != "POLICY_DENIED: namespace kube-system denied by policy" {
		t.Errorf("unexpected Error() string: %q", err.Error())
	}
}

func TestValidName(t *testing.T) {
	valid := []string{"k8s_pod_logs", "k8s_cluster_list", "k8s_workload_context", "k8s_metrics_nodes"}
	for _, name := range valid {
		if !ValidName(name) {
			t.Errorf("%s should be valid", name)
		}
	}
	invalid := []string{"k8s.pod.logs", "pod_logs", "k8s_", "k8s_PodLogs", "K8S_pod_logs", "k8s_pod-logs"}
	for _, name := range invalid {
		if ValidName(name) {
			t.Errorf("%s should be invalid (spec §3.1)", name)
		}
	}
}

func TestMustValidNamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for invalid tool name")
		}
	}()
	mustValidName("k8s.bad.name")
}

func TestEnvelopeSerialization(t *testing.T) {
	now := time.Date(2026, 9, 9, 15, 31, 0, 0, time.UTC)
	env := NewEnvelope(Cluster{ID: "ctx-1", Name: "prod"}, map[string]any{"ok": true}, Meta{
		Timestamp:  now,
		DurationMs: 81,
		View:       "summary",
	})
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["apiVersion"] != "mcp-k8s/v1" {
		t.Errorf("apiVersion: got %v", decoded["apiVersion"])
	}
	if decoded["error"] != nil {
		t.Errorf("success envelope must not carry error: %v", decoded["error"])
	}
	cluster := decoded["cluster"].(map[string]any)
	if cluster["name"] != "prod" {
		t.Errorf("cluster.name: got %v", cluster["name"])
	}
	meta := decoded["meta"].(map[string]any)
	if meta["durationMs"] != float64(81) || meta["view"] != "summary" {
		t.Errorf("meta: got %v", meta)
	}
}

func TestErrorEnvelopeSerialization(t *testing.T) {
	env := NewErrorEnvelope(Cluster{Name: "prod"}, NewError(ErrK8sForbidden, "RBAC denies get"), Meta{
		Timestamp: time.Now(),
	})
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["data"] != nil {
		t.Errorf("error envelope must not carry data: %v", decoded["data"])
	}
	e := decoded["error"].(map[string]any)
	if e["code"] != "K8S_FORBIDDEN" || e["retryable"] != false {
		t.Errorf("error: got %v", e)
	}
}

func TestCoverageSerialization(t *testing.T) {
	env := NewEnvelope(Cluster{Name: "prod"}, map[string]any{}, Meta{Timestamp: time.Now()})
	env.Coverage = &Coverage{
		From:     time.Date(2026, 9, 9, 14, 58, 0, 0, time.UTC),
		To:       time.Date(2026, 9, 9, 15, 31, 0, 0, time.UTC),
		Complete: false,
		Reason:   "oldest available event is newer than requested window (likely --event-ttl)",
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	cov := decoded["coverage"].(map[string]any)
	if cov["complete"] != false {
		t.Errorf("coverage.complete: got %v", cov["complete"])
	}
	if cov["reason"] == "" {
		t.Error("coverage.reason must be present when complete=false")
	}
}
