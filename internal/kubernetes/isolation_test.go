package kubernetes

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"k8s.io/client-go/rest"
)

// isolationKubeconfig has two contexts with distinguishable credentials:
// production and development tokens and API servers must never cross.
const isolationKubeconfig = `
apiVersion: v1
kind: Config
clusters:
- name: production
  cluster:
    server: https://production.example.com:6443
- name: development
  cluster:
    server: https://development.example.com:6443
contexts:
- name: production
  context:
    cluster: production
    user: production
- name: development
  context:
    cluster: development
    user: development
current-context: production
users:
- name: production
  user:
    token: production-token-7f3a
- name: development
  user:
    token: development-token-9c1e
`

// TestConcurrentClusterIsolation is the most important test in the
// repository (spec §11): goroutine A resolves cluster=production 100
// times while goroutine B resolves cluster=development 100 times.
// A must never see development credentials and B must never see
// production credentials. This is the bug MCP Kubernetes servers with
// mutable global context have; this test proves the no-global-state
// decision (spec §4) holds under concurrency.
func TestConcurrentClusterIsolation(t *testing.T) {
	p := &KubeconfigProvider{Path: writeKubeconfig(t, isolationKubeconfig)}
	ctx := context.Background()

	const calls = 100
	results := make(chan *resolvedConfig, 2*calls)
	errs := make(chan error, 2*calls)

	var wg sync.WaitGroup
	worker := func(cluster string) {
		defer wg.Done()
		for i := 0; i < calls; i++ {
			cfg, err := p.Config(ctx, cluster)
			if err != nil {
				errs <- fmt.Errorf("%s call %d: %w", cluster, i, err)
				continue
			}
			results <- &resolvedConfig{cluster: cluster, host: cfg.Host, token: cfg.BearerToken, ptr: cfg}
		}
	}

	wg.Add(2)
	go worker("production")
	go worker("development")
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}

	seen := map[*rest.Config]bool{}
	count := map[string]int{}
	for r := range results {
		count[r.cluster]++
		if seen[r.ptr] {
			t.Fatalf("Config returned a shared *rest.Config pointer: %p — state leaks between calls", r.ptr)
		}
		seen[r.ptr] = true

		switch r.cluster {
		case "production":
			if r.host != "https://production.example.com:6443" || r.token != "production-token-7f3a" {
				t.Fatalf("production call resolved foreign credentials: host=%q token=%q", r.host, r.token)
			}
		case "development":
			if r.host != "https://development.example.com:6443" || r.token != "development-token-9c1e" {
				t.Fatalf("development call resolved foreign credentials: host=%q token=%q", r.host, r.token)
			}
		}
	}
	if count["production"] != calls || count["development"] != calls {
		t.Fatalf("expected %d calls per cluster, got %v", calls, count)
	}
}

// TestConcurrentClusterIsolationMutationSafety: a caller that mutates a
// returned config must not poison any other call — every resolution is
// independent state.
func TestConcurrentClusterIsolationMutationSafety(t *testing.T) {
	p := &KubeconfigProvider{Path: writeKubeconfig(t, isolationKubeconfig)}
	ctx := context.Background()

	var wg sync.WaitGroup
	worker := func(cluster string) {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			cfg, err := p.Config(ctx, cluster)
			if err != nil {
				t.Error(err)
				return
			}
			// Tamper with the returned config: this must be a private copy.
			cfg.BearerToken = "tampered"
			cfg.Host = "https://tampered.example.com"
		}
	}

	wg.Add(2)
	go worker("production")
	go worker("development")
	wg.Wait()

	// After all the tampering, fresh resolutions must be pristine.
	prod, err := p.Config(ctx, "production")
	if err != nil {
		t.Fatal(err)
	}
	if prod.Host != "https://production.example.com:6443" || prod.BearerToken != "production-token-7f3a" {
		t.Fatalf("production credentials poisoned by another caller: host=%q token=%q", prod.Host, prod.BearerToken)
	}
	dev, err := p.Config(ctx, "development")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Host != "https://development.example.com:6443" || dev.BearerToken != "development-token-9c1e" {
		t.Fatalf("development credentials poisoned by another caller: host=%q token=%q", dev.Host, dev.BearerToken)
	}
}

type resolvedConfig struct {
	cluster string
	host    string
	token   string
	ptr     *rest.Config
}
