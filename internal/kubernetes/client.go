package kubernetes

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// Clients bundles every client flavor a tool may need for one cluster.
// Built fresh per call — a Clients value is never shared across clusters
// or across tool invocations (spec §4).
type Clients struct {
	Typed     kubernetes.Interface
	Dynamic   dynamic.Interface
	Discovery discovery.DiscoveryInterface
	// Mapper resolves Kind/GVK to resources. Deferred: it talks to the
	// API server lazily on first use. The memory cache in front of it is
	// per-Clients; the persistent disk cache lands in step 06 (spec §9).
	Mapper meta.RESTMapper
}

// ClientFactory builds the client bundle for a resolved rest.Config,
// applying the rate limits from config (spec §8: kubernetes.qps/burst).
// The factory itself is stateless and safe for concurrent use.
type ClientFactory struct {
	QPS   float32
	Burst int
}

func NewClientFactory(qps float32, burst int) *ClientFactory {
	return &ClientFactory{QPS: qps, Burst: burst}
}

// ForConfig builds a fresh Clients for cfg. The input config is copied
// before mutation, so callers keep ownership of theirs.
func (f *ClientFactory) ForConfig(cfg *rest.Config) (*Clients, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil rest.Config")
	}
	limited := rest.CopyConfig(cfg)
	limited.QPS = f.QPS
	limited.Burst = f.Burst

	typed, err := kubernetes.NewForConfig(limited)
	if err != nil {
		return nil, fmt.Errorf("typed client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(limited)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(limited)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
	}

	return &Clients{
		Typed:     typed,
		Dynamic:   dyn,
		Discovery: disc,
		Mapper:    restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disc)),
	}, nil
}
