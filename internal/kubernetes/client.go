package kubernetes

import (
	"fmt"
	"path/filepath"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/disk"
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
	// CacheDir enables the persistent discovery cache (spec §9). Empty
	// means memory-only.
	CacheDir string
}

func NewClientFactory(qps float32, burst int) *ClientFactory {
	return &ClientFactory{QPS: qps, Burst: burst}
}

// NewCachedClientFactory returns a factory that persists discovery per
// cluster under baseDir with CacheTTL freshness.
func NewCachedClientFactory(qps float32, burst int, baseDir string) *ClientFactory {
	return &ClientFactory{QPS: qps, Burst: burst, CacheDir: baseDir}
}

// ForCluster builds a fresh Clients for one named cluster. The cluster
// name scopes the disk cache and never appears in logs or responses. The
// input config is copied before mutation, so callers keep ownership.
func (f *ClientFactory) ForCluster(cluster string, cfg *rest.Config) (*Clients, error) {
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
	var disc discovery.DiscoveryInterface
	rawDisc, err := discovery.NewDiscoveryClientForConfig(limited)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
	}
	disc = rawDisc
	if f.CacheDir != "" {
		dir := clusterCacheDir(f.CacheDir, cluster)
		httpDir := filepath.Join(dir, "http")
		if err := ensurePrivateDir(dir); err != nil {
			return nil, fmt.Errorf("discovery cache dir: %w", err)
		}
		if err := ensurePrivateDir(httpDir); err != nil {
			return nil, fmt.Errorf("discovery http cache dir: %w", err)
		}
		cached, err := disk.NewCachedDiscoveryClientForConfig(limited, dir, httpDir, CacheTTL)
		if err != nil {
			return nil, fmt.Errorf("cached discovery client: %w", err)
		}
		disc = cached
	}

	return &Clients{
		Typed:     typed,
		Dynamic:   dyn,
		Discovery: disc,
		Mapper:    restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disc)),
	}, nil
}
