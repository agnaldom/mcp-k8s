package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"

	"github.com/agnaldom/mcp-k8s/internal/policy"
)

// ssarCacheTTL mirrors the discovery cache freshness (spec §5.3):
// effective permissions are cached for 5 minutes.
const ssarCacheTTL = 5 * time.Minute

// ssarConcurrency bounds parallel SelfSubjectAccessReview calls.
const ssarConcurrency = 8

// AuthzClients resolves the typed client (for SelfSubjectAccessReview)
// and the discovery client for the named cluster.
type AuthzClients func(ctx context.Context, cluster string) (kubernetes.Interface, discovery.DiscoveryInterface, error)

// APIResourceEntry describes one resource with its effective permissions
// (spec §5.3): verbs are what the cluster supports; allowed is what this
// identity can actually do; policyBlocked marks denial by local policy.
type APIResourceEntry struct {
	Group         string          `json:"group"`
	Version       string          `json:"version"`
	Resource      string          `json:"resource"`
	Kind          string          `json:"kind"`
	Namespaced    bool            `json:"namespaced"`
	Verbs         []string        `json:"verbs"`
	Allowed       map[string]bool `json:"allowed"`
	PolicyBlocked bool            `json:"policyBlocked"`
}

type apiResourceCacheEntry struct {
	items     []APIResourceEntry
	fetchedAt time.Time
}

// APIResourceService lists API resources with effective permissions via
// SelfSubjectAccessReview, batched and cached (spec §5.3).
type APIResourceService struct {
	Policy  *policy.Policy
	Clients AuthzClients

	mu    sync.Mutex
	cache map[string]apiResourceCacheEntry

	// now is injectable for tests.
	now func() time.Time
}

func NewAPIResourceService(pol *policy.Policy, clients AuthzClients) *APIResourceService {
	return &APIResourceService{
		Policy:  pol,
		Clients: clients,
		cache:   map[string]apiResourceCacheEntry{},
		now:     time.Now,
	}
}

// List returns the resource catalog with effective permissions. Results
// are cached per cluster for ssarCacheTTL.
func (s *APIResourceService) List(ctx context.Context, cluster string) ([]APIResourceEntry, error) {
	if err := s.Policy.ClusterAllowed(cluster); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if entry, ok := s.cache[cluster]; ok && s.now().Sub(entry.fetchedAt) < ssarCacheTTL {
		s.mu.Unlock()
		return entry.items, nil
	}
	s.mu.Unlock()

	clients, disc, err := s.Clients(ctx, cluster)
	if err != nil {
		return nil, err
	}
	resourceLists, err := disc.ServerPreferredResources()
	if err != nil && len(resourceLists) == 0 {
		return nil, fmt.Errorf("discovery: %w", err)
	}

	// Flatten discovery into entries.
	var entries []APIResourceEntry
	for _, rl := range resourceLists {
		gv, err := schema.ParseGroupVersion(rl.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range rl.APIResources {
			if strings.Contains(r.Name, "/") {
				continue // subresources are out of v0.1 scope
			}
			verbs := append([]string(nil), r.Verbs...)
			sort.Strings(verbs)
			entries = append(entries, APIResourceEntry{
				Group:      gv.Group,
				Version:    gv.Version,
				Resource:   r.Name,
				Kind:       r.Kind,
				Namespaced: r.Namespaced,
				Verbs:      verbs,
				Allowed:    make(map[string]bool, len(r.Verbs)),
			})
		}
	}

	// Effective permissions: one SSAR per (resource, verb), bounded
	// concurrency, and local policy applied first.
	var wg sync.WaitGroup
	var allowedMu sync.Mutex
	sem := make(chan struct{}, ssarConcurrency)
	for i := range entries {
		if s.Policy.ResourceAllowed(entries[i].Kind) != nil {
			entries[i].PolicyBlocked = true
			continue
		}
		for _, verb := range entries[i].Verbs {
			wg.Add(1)
			go func(i int, verb string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				review := &authorizationv1.SelfSubjectAccessReview{
					Spec: authorizationv1.SelfSubjectAccessReviewSpec{
						ResourceAttributes: &authorizationv1.ResourceAttributes{
							Group:    entries[i].Group,
							Version:  entries[i].Version,
							Resource: entries[i].Resource,
							Verb:     verb,
						},
					},
				}
				resp, err := clients.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
				allowed := err == nil && resp.Status.Allowed
				allowedMu.Lock()
				entries[i].Allowed[verb] = allowed
				allowedMu.Unlock()
			}(i, verb)
		}
	}
	wg.Wait()

	sort.Slice(entries, func(a, b int) bool {
		if entries[a].Group != entries[b].Group {
			return entries[a].Group < entries[b].Group
		}
		if entries[a].Version != entries[b].Version {
			return entries[a].Version < entries[b].Version
		}
		return entries[a].Resource < entries[b].Resource
	})

	s.mu.Lock()
	s.cache[cluster] = apiResourceCacheEntry{items: entries, fetchedAt: s.now()}
	s.mu.Unlock()
	return entries, nil
}
