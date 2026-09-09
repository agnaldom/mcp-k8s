package kubernetes

import (
	"context"
	"errors"

	"k8s.io/client-go/rest"
)

// ErrClusterNotFound is returned when a provider has no cluster with the
// requested name. API error codes (spec §3.6) are assigned by the tools
// layer; this layer keeps transport-neutral sentinel errors.
var ErrClusterNotFound = errors.New("cluster not found")

// ClusterInfo describes one cluster a provider can resolve.
type ClusterInfo struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
	// Source identifies the provider: "kubeconfig" or "in-cluster".
	Source string `json:"source"`
}

// ClusterProvider lists the clusters it can reach and resolves a fresh
// client configuration per call. Implementations must hold no mutable
// state that could leak one call's cluster into another — every call
// resolves its own credentials (spec §4). This is what makes the
// concurrent-isolation test (spec §11) possible.
type ClusterProvider interface {
	// List returns the available clusters, marking the default.
	List(ctx context.Context) ([]ClusterInfo, error)
	// Config resolves the client configuration for the named cluster.
	// A new configuration is built on every call; nothing is cached
	// between calls.
	Config(ctx context.Context, name string) (*rest.Config, error)
}
