package doctor

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
)

type stubProvider struct {
	clusters []kubernetes.ClusterInfo
	err      error
}

func (s *stubProvider) List(_ context.Context) ([]kubernetes.ClusterInfo, error) {
	return s.clusters, s.err
}

func (s *stubProvider) Config(_ context.Context, _ string) (*rest.Config, error) {
	return nil, nil
}

func statuses(res *Result) map[string]string {
	out := map[string]string{}
	for _, c := range res.Checks {
		out[c.Name] = c.Status
	}
	return out
}

func TestRunAllHealthy(t *testing.T) {
	provider := &stubProvider{clusters: []kubernetes.ClusterInfo{{Name: "prod", Default: true}}}
	probe := func(_ context.Context, name string) (*ClusterReport, error) {
		if name != "prod" {
			t.Errorf("unexpected cluster %q", name)
		}
		return &ClusterReport{
			Version: "v1.30.0", APIGroups: 3, MetricsAvailable: true,
			CanListPods: true, CanGetPodLogs: true,
		}, nil
	}
	res := Run(context.Background(), Deps{
		Cfg: config.Default(), Provider: provider, CacheDir: t.TempDir(),
		Probe: probe, ClusterTimeout: time.Second,
	})
	if res.Failed() {
		t.Fatalf("healthy environment must not fail: %+v", res.Checks)
	}
	got := statuses(res)
	for _, name := range []string{
		"config", "clusters", "cache directory",
		"cluster prod", "cluster prod discovery", "cluster prod metrics",
		"cluster prod list permission", "cluster prod pods/log permission",
	} {
		if got[name] != StatusOK {
			t.Errorf("%s: %s", name, got[name])
		}
	}
}

func TestRunProbeFailure(t *testing.T) {
	provider := &stubProvider{clusters: []kubernetes.ClusterInfo{{Name: "staging"}}}
	probe := func(_ context.Context, _ string) (*ClusterReport, error) {
		return nil, errors.New("connection refused")
	}
	res := Run(context.Background(), Deps{
		Cfg: config.Default(), Provider: provider, CacheDir: t.TempDir(),
		Probe: probe, ClusterTimeout: time.Second,
	})
	if !res.Failed() {
		t.Fatal("probe failure must fail the report")
	}
	got := statuses(res)
	if got["cluster staging"] != StatusFail {
		t.Errorf("cluster check: %v", got)
	}
}

func TestRunInvalidConfig(t *testing.T) {
	bad := config.Default()
	bad.Security.Readonly = false // rejected by Validate
	res := Run(context.Background(), Deps{
		Cfg: bad, Provider: &stubProvider{}, CacheDir: t.TempDir(),
	})
	if !res.Failed() {
		t.Fatal("invalid config must fail")
	}
	if statuses(res)["config"] != StatusFail {
		t.Errorf("config check: %+v", res.Checks)
	}
}

func TestRunNoClustersWarns(t *testing.T) {
	res := Run(context.Background(), Deps{
		Cfg: config.Default(), Provider: &stubProvider{}, CacheDir: t.TempDir(),
		Probe: func(_ context.Context, _ string) (*ClusterReport, error) {
			return &ClusterReport{}, nil
		},
	})
	if statuses(res)["clusters"] != StatusWarn {
		t.Errorf("clusters: %+v", res.Checks)
	}
}

func TestRunCacheDirCreated(t *testing.T) {
	dir := t.TempDir() + "/nested/cache"
	res := Run(context.Background(), Deps{
		Cfg: config.Default(), Provider: &stubProvider{}, CacheDir: dir,
	})
	if statuses(res)["cache directory"] != StatusOK {
		t.Fatalf("cache check: %+v", res.Checks)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("cache dir not created: %v", err)
	}
}

func TestRunMetricsMissingWarns(t *testing.T) {
	provider := &stubProvider{clusters: []kubernetes.ClusterInfo{{Name: "prod"}}}
	probe := func(_ context.Context, _ string) (*ClusterReport, error) {
		return &ClusterReport{
			Version: "v1.30.0", APIGroups: 3, MetricsAvailable: false,
			CanListPods: true, CanGetPodLogs: false,
		}, nil
	}
	res := Run(context.Background(), Deps{
		Cfg: config.Default(), Provider: provider, CacheDir: t.TempDir(),
		Probe: probe, ClusterTimeout: time.Second,
	})
	if res.Failed() {
		t.Fatalf("warn-only findings must not fail: %+v", res.Checks)
	}
	got := statuses(res)
	if got["cluster prod metrics"] != StatusWarn {
		t.Errorf("metrics: %v", got)
	}
	if got["cluster prod pods/log permission"] != StatusWarn {
		t.Errorf("pods/log: %v", got)
	}
}

func TestProbeFailureDetailMentionsReason(t *testing.T) {
	provider := &stubProvider{clusters: []kubernetes.ClusterInfo{{Name: "down"}}}
	res := Run(context.Background(), Deps{
		Cfg: config.Default(), Provider: provider, CacheDir: t.TempDir(),
		Probe: func(_ context.Context, _ string) (*ClusterReport, error) {
			return nil, errors.New("i/o timeout")
		},
		ClusterTimeout: time.Second,
	})
	for _, c := range res.Checks {
		if c.Name == "cluster down" {
			if !strings.Contains(c.Detail, "i/o timeout") {
				t.Errorf("detail must carry the reason: %q", c.Detail)
			}
			return
		}
	}
	t.Fatalf("cluster check missing: %+v", res.Checks)
}
