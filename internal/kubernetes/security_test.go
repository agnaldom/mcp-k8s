package kubernetes

import (
	"context"
	"errors"
	"testing"
)

// Security regression suite (spec §6.1/§11). These tests block merge:
// each one pins a deterministic security control. Do not weaken one
// without a spec change.

func TestSecurityExecPluginBlocked(t *testing.T) {
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
