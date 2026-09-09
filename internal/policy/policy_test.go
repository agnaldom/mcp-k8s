package policy

import (
	"errors"
	"testing"

	"github.com/agnaldom/mcp-k8s/internal/config"
)

func TestClusterAllowedEmptyAllowList(t *testing.T) {
	p := New(config.Security{})
	for _, name := range []string{"prod", "development", "anything"} {
		if err := p.ClusterAllowed(name); err != nil {
			t.Errorf("%s should be allowed with empty allow-list", name)
		}
	}
}

func TestClusterAllowedWithAllowList(t *testing.T) {
	p := New(config.Security{Clusters: config.AllowList{Allow: []string{"prod"}}})
	if err := p.ClusterAllowed("prod"); err != nil {
		t.Errorf("prod should be allowed: %v", err)
	}
	if err := p.ClusterAllowed("development"); !errors.Is(err, ErrDenied) {
		t.Errorf("development should be denied with ErrDenied, got %v", err)
	}
}

func TestNamespaceAllowedDenyWins(t *testing.T) {
	p := New(config.Security{Namespaces: config.NamespaceList{
		Allow: []string{},
		Deny:  []string{"kube-system", "cattle-system"},
	}})
	if !p.NamespaceAllowed("payments") {
		t.Error("payments should be allowed")
	}
	for _, ns := range []string{"kube-system", "cattle-system"} {
		if p.NamespaceAllowed(ns) {
			t.Errorf("%s should be denied", ns)
		}
	}
}

func TestNamespaceAllowedWithAllowList(t *testing.T) {
	p := New(config.Security{Namespaces: config.NamespaceList{
		Allow: []string{"payments", "checkout"},
	}})
	if !p.NamespaceAllowed("payments") || !p.NamespaceAllowed("checkout") {
		t.Error("allowed namespaces must pass")
	}
	if p.NamespaceAllowed("default") {
		t.Error("default should not pass a non-empty allow-list")
	}
	// Deny still wins over allow.
	p2 := New(config.Security{Namespaces: config.NamespaceList{
		Allow: []string{"payments", "kube-system"},
		Deny:  []string{"kube-system"},
	}})
	if p2.NamespaceAllowed("kube-system") {
		t.Error("deny must win over allow")
	}
}

func TestFilterNamespacesPreservesOrder(t *testing.T) {
	p := New(config.Default().Security)
	in := []string{"payments", "kube-system", "default", "cattle-system", "checkout"}
	got := p.FilterNamespaces(in)
	want := []string{"payments", "default", "checkout"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order not preserved: got %v, want %v", got, want)
		}
	}
}
