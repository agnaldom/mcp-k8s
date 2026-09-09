package kubernetes

import (
	"context"

	"k8s.io/client-go/rest"
)

// InClusterName is the single cluster name exposed by InClusterProvider.
const InClusterName = "in-cluster"

// InClusterProvider serves the cluster the binary runs inside of, via the
// pod's service account. Used when mcp-k8s is deployed in-cluster.
type InClusterProvider struct{}

var _ ClusterProvider = (*InClusterProvider)(nil)

const inClusterSource = "in-cluster"

// List returns the single in-cluster entry, marked as default.
func (p *InClusterProvider) List(_ context.Context) ([]ClusterInfo, error) {
	return []ClusterInfo{{Name: InClusterName, Default: true, Source: inClusterSource}}, nil
}

// Config resolves the pod's service account into a fresh rest.Config.
func (p *InClusterProvider) Config(_ context.Context, name string) (*rest.Config, error) {
	if name != InClusterName {
		return nil, ErrClusterNotFound
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}
