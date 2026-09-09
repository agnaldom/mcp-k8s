package kubernetes

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
)

func TestForClusterBuildsAllClients(t *testing.T) {
	f := NewClientFactory(20, 40)
	clients, err := f.ForCluster("prod", &rest.Config{Host: "https://prod.example.com:6443"})
	if err != nil {
		t.Fatal(err)
	}
	if clients.Typed == nil || clients.Dynamic == nil || clients.Discovery == nil || clients.Mapper == nil {
		t.Fatalf("all client flavors must be built: %+v", clients)
	}
}

func TestForClusterDoesNotMutateInput(t *testing.T) {
	f := NewClientFactory(20, 40)
	cfg := &rest.Config{Host: "https://prod.example.com:6443"}
	if _, err := f.ForCluster("prod", cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.QPS != 0 || cfg.Burst != 0 {
		t.Errorf("input config must not be mutated: qps=%v burst=%v", cfg.QPS, cfg.Burst)
	}
}

func TestForClusterIsolatesBundles(t *testing.T) {
	// Two bundles from different clusters must share nothing: this is the
	// client-level half of the no-global-state rule (spec §4).
	f := NewClientFactory(20, 40)
	prod, err := f.ForCluster("prod", &rest.Config{Host: "https://prod.example.com:6443"})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := f.ForCluster("development", &rest.Config{Host: "https://dev.example.com:6443"})
	if err != nil {
		t.Fatal(err)
	}
	prodHost := prod.Typed.Discovery().RESTClient().Get().URL().Host
	devHost := dev.Typed.Discovery().RESTClient().Get().URL().Host
	if prodHost == devHost {
		t.Errorf("bundles share a host: %q", prodHost)
	}
}

func TestForClusterNil(t *testing.T) {
	f := NewClientFactory(20, 40)
	if _, err := f.ForCluster("prod", nil); err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestForClusterCreatesPrivateCacheDir(t *testing.T) {
	base := t.TempDir()
	f := NewCachedClientFactory(20, 40, base)
	if _, err := f.ForCluster("prod", &rest.Config{Host: "https://prod.example.com:6443"}); err != nil {
		t.Fatal(err)
	}
	dir := clusterCacheDir(base, "prod")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("cache dir not created: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("cache dir permissions: got %o, want 0700 (spec §9)", info.Mode().Perm())
	}
}

func TestForClusterCacheDirsArePerCluster(t *testing.T) {
	base := t.TempDir()
	f := NewCachedClientFactory(20, 40, base)
	for _, name := range []string{"prod", "development"} {
		if _, err := f.ForCluster(name, &rest.Config{Host: "https://prod.example.com:6443"}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(base, "discovery"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 per-cluster cache dirs, got %d", len(entries))
	}
}

func TestClusterCacheDirSanitizesNames(t *testing.T) {
	base := "/tmp/cache"
	dir := clusterCacheDir(base, `prod/../../etc`)
	if filepath.Dir(filepath.Dir(dir)) != base {
		t.Errorf("sanitized dir escapes base: %q", dir)
	}
	if filepath.Base(dir) == ".." || filepath.Base(dir) == "." || filepath.Base(dir) == "" {
		t.Errorf("unsafe base name: %q", dir)
	}
	// The sanitized name must be stable for the same cluster.
	if clusterCacheDir(base, "prod") != clusterCacheDir(base, "prod") {
		t.Error("sanitization must be deterministic")
	}
}

func TestEnsurePrivateDirTightensPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("permissions not tightened: got %o", info.Mode().Perm())
	}
}
