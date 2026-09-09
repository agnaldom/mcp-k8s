package kubernetes

import (
	"errors"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// ErrResourceTypeNotFound marks a Kind the cluster does not serve.
var ErrResourceTypeNotFound = errors.New("resource type not found")

// ResolveKind maps a user-supplied Kind (e.g. "Deployment") to a REST
// mapping. Kinds are unique in practice but not guaranteed across API
// groups, so an ambiguous Kind is an error, never a guess (spec §4: no
// arbitrary choices).
func ResolveKind(disc discovery.DiscoveryInterface, mapper meta.RESTMapper, kind string) (*meta.RESTMapping, error) {
	gk := schema.GroupKind{Kind: kind}
	mapping, err := mapper.RESTMapping(gk)
	if err == nil {
		return mapping, nil
	}
	if !meta.IsNoMatchError(err) {
		return nil, fmt.Errorf("resolve kind %q: %w", kind, err)
	}

	// The mapper missed: fall back to discovery to distinguish
	// "unknown kind" from "unknown group placement" and to detect
	// ambiguity deterministically.
	resources, discErr := disc.ServerPreferredResources()
	if discErr != nil && len(resources) == 0 {
		return nil, fmt.Errorf("resolve kind %q: discovery failed: %w", kind, discErr)
	}
	var matches []schema.GroupVersionKind
	for _, list := range resources {
		for _, r := range list.APIResources {
			if r.Kind == kind {
				gv, _ := schema.ParseGroupVersion(list.GroupVersion)
				matches = append(matches, gv.WithKind(kind))
			}
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%w: %q", ErrResourceTypeNotFound, kind)
	case 1:
		return mapper.RESTMapping(matches[0].GroupKind(), matches[0].Version)
	default:
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m.String())
		}
		sort.Strings(names)
		return nil, fmt.Errorf("kind %q is ambiguous across %v: pass a more specific kind", kind, names)
	}
}
