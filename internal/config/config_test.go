package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeConfig(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileYieldsDefaults(t *testing.T) {
	cfg, found, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing file")
	}
	def := Default()
	if !reflect.DeepEqual(cfg, def) {
		t.Fatalf("defaults mismatch:\n got %+v\nwant %+v", cfg, def)
	}
}

func TestLoadFullConfig(t *testing.T) {
	path := writeConfig(t, `
security:
  readonly: true
  clusters: { allow: [prod] }
  namespaces: { allow: [], deny: [kube-system] }
  resources: { deny: [Secret] }
  kubeconfig: { allowExecPlugins: true }
limits:
  requestTimeout: 45s
  response: { maxBytes: 2097152 }
  list: { defaultLimit: 50, maxLimit: 200 }
  logs: { defaultTailLines: 100, maxTailLines: 1000, maxBytes: 524288 }
  events: { maxItems: 250 }
  workload: { maxPods: 10, maxConcurrentK8sRequests: 4 }
kubernetes:
  qps: 10
  burst: 20
signals:
  container_not_ready: { notReadyFor: 10m }
  high_restart_count: { threshold: 5, window: 30m }
  pvc_pending: { pendingFor: 5m }
  memory_near_limit: { ratio: 0.80 }
`)
	cfg, found, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if cfg.Security.Clusters.Allow[0] != "prod" {
		t.Errorf("clusters.allow: got %v", cfg.Security.Clusters.Allow)
	}
	if !cfg.Security.Kubeconfig.AllowExecPlugins {
		t.Error("expected allowExecPlugins=true")
	}
	if cfg.Limits.RequestTimeout.Duration != 45*time.Second {
		t.Errorf("requestTimeout: got %v", cfg.Limits.RequestTimeout)
	}
	if cfg.Limits.List.DefaultLimit != 50 || cfg.Limits.List.MaxLimit != 200 {
		t.Errorf("list limits: got %+v", cfg.Limits.List)
	}
	if cfg.Kubernetes.QPS != 10 || cfg.Kubernetes.Burst != 20 {
		t.Errorf("kubernetes client: got %+v", cfg.Kubernetes)
	}
	if cfg.Signals.HighRestartCount.Threshold != 5 || cfg.Signals.HighRestartCount.Window.Duration != 30*time.Minute {
		t.Errorf("high_restart_count: got %+v", cfg.Signals.HighRestartCount)
	}
	if cfg.Signals.MemoryNearLimit.Ratio != 0.80 {
		t.Errorf("memory_near_limit: got %v", cfg.Signals.MemoryNearLimit.Ratio)
	}
}

func TestLoadRejectsReadonlyFalse(t *testing.T) {
	path := writeConfig(t, "security:\n  readonly: false\n")
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected error for readonly: false")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	path := writeConfig(t, "securty:\n  readonly: true\n")
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected strict-mode error for typo'd key")
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	path := writeConfig(t, "limits:\n  requestTimeout: notaduration\n")
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestLoadRejectsInvertedListLimits(t *testing.T) {
	path := writeConfig(t, "limits:\n  list: { defaultLimit: 500, maxLimit: 100 }\n")
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected error for defaultLimit > maxLimit")
	}
}

func TestLoadRejectsBadMemoryRatio(t *testing.T) {
	path := writeConfig(t, "signals:\n  memory_near_limit: { ratio: 1.5 }\n")
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected error for ratio > 1")
	}
}

func TestDefaultsMatchSpec(t *testing.T) {
	cfg := Default()
	if !cfg.Security.Readonly {
		t.Error("readonly default must be true")
	}
	if cfg.Limits.Response.MaxBytes != 4*1024*1024 {
		t.Errorf("response.maxBytes: got %d", cfg.Limits.Response.MaxBytes)
	}
	if cfg.Limits.Logs.MaxBytes != 1024*1024 {
		t.Errorf("logs.maxBytes: got %d", cfg.Limits.Logs.MaxBytes)
	}
	if cfg.Limits.Workload.MaxPods != 20 || cfg.Limits.Workload.MaxConcurrentK8sRequests != 8 {
		t.Errorf("workload limits: got %+v", cfg.Limits.Workload)
	}
	if cfg.Kubernetes.QPS != 20 || cfg.Kubernetes.Burst != 40 {
		t.Errorf("kubernetes client: got %+v", cfg.Kubernetes)
	}
	deny := map[string]bool{}
	for _, ns := range cfg.Security.Namespaces.Deny {
		deny[ns] = true
	}
	if !deny["kube-system"] || !deny["cattle-system"] {
		t.Errorf("namespace deny default: got %v", cfg.Security.Namespaces.Deny)
	}
}
