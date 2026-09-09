// Package services sits between the MCP tools and the kubernetes layer:
// it orchestrates calls, applies limits, and shapes responses. It may
// import internal/kubernetes; it must not import internal/tools.
package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
)

// ClusterService answers cluster-level questions for the MCP tools.
// It holds only immutable wiring (provider, factory) — every call resolves
// its own clients from its own cluster name, so concurrent calls for
// different clusters can never share state (spec §4).
type ClusterService struct {
	Provider kubernetes.ClusterProvider
	Factory  *kubernetes.ClientFactory
}

// Summary is one entry of the k8s_cluster_list response (spec §4: every
// context is a cluster; exactly one carries default=true).
type Summary struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
	Source  string `json:"source"`
}

func (s *ClusterService) List(ctx context.Context) ([]Summary, error) {
	clusters, err := s.Provider.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}
	out := make([]Summary, 0, len(clusters))
	for _, c := range clusters {
		out = append(out, Summary{Name: c.Name, Default: c.Default, Source: c.Source})
	}
	return out, nil
}

// Version is the Kubernetes version block of k8s_cluster_info.
type Version struct {
	Major      string `json:"major"`
	Minor      string `json:"minor"`
	GitVersion string `json:"gitVersion"`
	Platform   string `json:"platform"`
	GoVersion  string `json:"goVersion"`
}

// Info is the k8s_cluster_info response.
type Info struct {
	Cluster string   `json:"cluster"`
	Version *Version `json:"version,omitempty"`
}

// Info resolves the named cluster and reports its Kubernetes version.
// Sentinel errors from the kubernetes layer (e.g. ErrClusterNotFound)
// propagate unchanged; the tools layer maps them to API error codes.
func (s *ClusterService) Info(ctx context.Context, cluster string) (*Info, error) {
	if cluster == "" {
		return nil, fmt.Errorf("%w: cluster argument is required", kubernetes.ErrClusterNotFound)
	}
	cfg, err := s.Provider.Config(ctx, cluster)
	if err != nil {
		return nil, err
	}
	clients, err := s.Factory.ForCluster(cluster, cfg)
	if err != nil {
		return nil, fmt.Errorf("build clients for %q: %w", cluster, err)
	}
	version, err := clients.Discovery.ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("server version for %q: %w", cluster, err)
	}
	return &Info{
		Cluster: cluster,
		Version: &Version{
			Major:      version.Major,
			Minor:      version.Minor,
			GitVersion: version.GitVersion,
			Platform:   version.Platform,
			GoVersion:  version.GoVersion,
		},
	}, nil
}

// IsClusterNotFound reports whether err is a missing-cluster condition,
// for callers that need to distinguish it without importing client-go.
func IsClusterNotFound(err error) bool {
	return errors.Is(err, kubernetes.ErrClusterNotFound)
}
