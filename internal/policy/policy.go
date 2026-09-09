// Package policy is the local policy layer (spec §7): a leaf package
// that depends only on the config types. It is evaluated before cluster
// resolution and before Kubernetes RBAC, and it is never skipped because
// "RBAC covers it" — the two are independent layers.
package policy

import (
	"errors"
	"fmt"

	"github.com/agnaldom/mcp-k8s/internal/config"
)

// ErrDenied marks a denial by local policy. The tools layer maps it to
// the POLICY_DENIED API error code (spec §3.6).
var ErrDenied = errors.New("denied by local policy")

// Policy evaluates the security block of the configuration.
type Policy struct {
	security config.Security
}

func New(security config.Security) *Policy {
	return &Policy{security: security}
}

// ClusterAllowed reports whether the named cluster may be used at all.
// Empty allow-list means every cluster (spec §7). Denial happens before
// any cluster resolution — the evaluation order is input schema → local
// policy → cluster resolution → RBAC → API.
func (p *Policy) ClusterAllowed(name string) error {
	allow := p.security.Clusters.Allow
	if len(allow) == 0 {
		return nil
	}
	for _, a := range allow {
		if a == name {
			return nil
		}
	}
	return fmt.Errorf("%w: cluster %q not in security.clusters.allow", ErrDenied, name)
}

// NamespaceAllowed reports whether a namespace passes the allow/deny
// lists. Empty allow means all namespaces pass the allow check (spec §7).
func (p *Policy) NamespaceAllowed(name string) bool {
	allow := p.security.Namespaces.Allow
	if len(allow) > 0 {
		found := false
		for _, a := range allow {
			if a == name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, d := range p.security.Namespaces.Deny {
		if d == name {
			return false
		}
	}
	return true
}

// ResourceAllowed reports whether a Kind may be read at all. The deny
// list exists because some types must stay blocked even when RBAC would
// allow them — Secret foremost (spec §6.1): Secret.data/stringData are
// never serialized, no matter the view.
func (p *Policy) ResourceAllowed(kind string) error {
	for _, d := range p.security.Resources.Deny {
		if d == kind {
			return fmt.Errorf("%w: resource %q is blocked by security.resources.deny (spec §6.1)", ErrDenied, kind)
		}
	}
	return nil
}

// FilterNamespaces returns only the namespaces the policy permits,
// preserving the input order.
func (p *Policy) FilterNamespaces(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if p.NamespaceAllowed(n) {
			out = append(out, n)
		}
	}
	return out
}
