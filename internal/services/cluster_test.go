package services

import (
	"context"
	"errors"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
)

type fakeProvider struct {
	clusters []kubernetes.ClusterInfo
	configs  map[string]*rest.Config
}

func (f *fakeProvider) List(_ context.Context) ([]kubernetes.ClusterInfo, error) {
	return f.clusters, nil
}

func (f *fakeProvider) Config(_ context.Context, name string) (*rest.Config, error) {
	cfg, ok := f.configs[name]
	if !ok {
		return nil, kubernetes.ErrClusterNotFound
	}
	return cfg, nil
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		clusters: []kubernetes.ClusterInfo{
			{Name: "prod", Default: true, Source: "kubeconfig"},
			{Name: "development", Source: "kubeconfig"},
		},
		configs: map[string]*rest.Config{
			"prod":        {Host: "https://prod.example.com:6443"},
			"development": {Host: "https://dev.example.com:6443"},
		},
	}
}

func TestListReturnsClustersWithSingleDefault(t *testing.T) {
	svc := &ClusterService{Provider: newFakeProvider()}
	clusters, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}
	defaults := 0
	for _, c := range clusters {
		if c.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("exactly one default required, got %d", defaults)
	}
}

func TestInfoRequiresClusterName(t *testing.T) {
	svc := &ClusterService{Provider: newFakeProvider(), Factory: kubernetes.NewClientFactory(20, 40)}
	_, err := svc.Info(context.Background(), "")
	if !errors.Is(err, kubernetes.ErrClusterNotFound) {
		t.Fatalf("expected ErrClusterNotFound for empty cluster, got %v", err)
	}
}

func TestInfoUnknownCluster(t *testing.T) {
	svc := &ClusterService{Provider: newFakeProvider(), Factory: kubernetes.NewClientFactory(20, 40)}
	_, err := svc.Info(context.Background(), "staging")
	if !errors.Is(err, kubernetes.ErrClusterNotFound) {
		t.Fatalf("expected ErrClusterNotFound, got %v", err)
	}
	if !IsClusterNotFound(err) {
		t.Error("IsClusterNotFound should recognize the wrapped sentinel")
	}
}
