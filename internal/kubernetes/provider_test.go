package kubernetes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const testKubeconfig = `
apiVersion: v1
kind: Config
clusters:
- name: prod
  cluster:
    server: https://prod.example.com:6443
- name: development
  cluster:
    server: https://dev.example.com:6443
contexts:
- name: prod
  context:
    cluster: prod
    user: prod
- name: development
  context:
    cluster: development
    user: development
current-context: prod
users:
- name: prod
  user:
    token: test-token
- name: development
  user:
    token: test-token
`

func writeKubeconfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKubeconfigListMarksCurrentContextDefault(t *testing.T) {
	p := &KubeconfigProvider{Path: writeKubeconfig(t, testKubeconfig)}
	clusters, err := p.List(context.Background())
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
			if c.Name != "prod" {
				t.Errorf("default should be prod, got %q", c.Name)
			}
		}
		if c.Source != kubeconfigSource {
			t.Errorf("source: got %q", c.Source)
		}
	}
	if defaults != 1 {
		t.Fatalf("exactly one cluster must be default, got %d", defaults)
	}
}

func TestKubeconfigConfigResolvesPerCall(t *testing.T) {
	p := &KubeconfigProvider{Path: writeKubeconfig(t, testKubeconfig)}
	ctx := context.Background()

	prod, err := p.Config(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	dev, err := p.Config(ctx, "development")
	if err != nil {
		t.Fatal(err)
	}
	if prod.Host != "https://prod.example.com:6443" {
		t.Errorf("prod host: got %q", prod.Host)
	}
	if dev.Host != "https://dev.example.com:6443" {
		t.Errorf("dev host: got %q", dev.Host)
	}
	if prod.BearerToken != dev.BearerToken {
		t.Error("tokens should match in fixture")
	}

	// Each call returns an independent config: mutating one must not
	// affect the next resolution (no shared mutable state, spec §4).
	prod.Host = "https://tampered.example.com:6443"
	again, err := p.Config(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if again.Host != "https://prod.example.com:6443" {
		t.Errorf("config leaked between calls: got %q", again.Host)
	}
}

func TestKubeconfigConfigUnknownCluster(t *testing.T) {
	p := &KubeconfigProvider{Path: writeKubeconfig(t, testKubeconfig)}
	_, err := p.Config(context.Background(), "staging")
	if !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("expected ErrClusterNotFound, got %v", err)
	}
}

func TestKubeconfigExecPluginBlockedByDefault(t *testing.T) {
	const withExec = `
apiVersion: v1
kind: Config
clusters:
- name: prod
  cluster:
    server: https://prod.example.com:6443
contexts:
- name: prod
  context:
    cluster: prod
    user: prod
current-context: prod
users:
- name: prod
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: /usr/local/bin/credential-helper
      interactiveMode: Never
`
	p := &KubeconfigProvider{Path: writeKubeconfig(t, withExec)}
	_, err := p.Config(context.Background(), "prod")
	if !errors.Is(err, ErrExecPluginBlocked) {
		t.Fatalf("expected ErrExecPluginBlocked, got %v", err)
	}

	allowed := &KubeconfigProvider{Path: p.Path, AllowExecPlugins: true}
	cfg, err := allowed.Config(context.Background(), "prod")
	if err != nil {
		t.Fatalf("exec plugin should be permitted when allowExecPlugins=true: %v", err)
	}
	if cfg.ExecProvider == nil || cfg.ExecProvider.Command != "/usr/local/bin/credential-helper" {
		t.Errorf("exec provider not wired: %+v", cfg.ExecProvider)
	}
}

func TestInClusterProviderList(t *testing.T) {
	p := &InClusterProvider{}
	clusters, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 || clusters[0].Name != InClusterName || !clusters[0].Default {
		t.Errorf("unexpected in-cluster listing: %+v", clusters)
	}
}

func TestInClusterProviderUnknownName(t *testing.T) {
	p := &InClusterProvider{}
	_, err := p.Config(context.Background(), "prod")
	if !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("expected ErrClusterNotFound, got %v", err)
	}
}

func TestInClusterProviderOutsideCluster(t *testing.T) {
	// Outside a pod, InClusterConfig fails because the service account
	// files are absent. The provider must surface that, not invent config.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	p := &InClusterProvider{}
	if _, err := p.Config(context.Background(), InClusterName); err == nil {
		t.Fatal("expected error outside a cluster")
	}
}
