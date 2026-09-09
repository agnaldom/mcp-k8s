package services

import (
	"context"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/policy"
)

func fakeAPIResourcesClientset(t *testing.T, allowedVerbs map[string]bool) *k8sfake.Clientset {
	t.Helper()
	clientset := &k8sfake.Clientset{}
	clientset.AddReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateAction)
		review := create.GetObject().(*authorizationv1.SelfSubjectAccessReview)
		verb := review.Spec.ResourceAttributes.Verb
		resp := review.DeepCopy()
		resp.Status.Allowed = allowedVerbs[verb]
		return true, resp, nil
	})
	return clientset
}

// stubDiscovery embeds the interface and overrides only the method under
// test: FakeDiscovery.ServerPreferredResources returns nil in this
// client-go version.
type stubDiscovery struct {
	discovery.DiscoveryInterface
	lists []*metav1.APIResourceList
}

func (s stubDiscovery) ServerPreferredResources() ([]*metav1.APIResourceList, error) {
	return s.lists, nil
}

func fakeDiscoveryWith(resources ...*metav1.APIResourceList) discovery.DiscoveryInterface {
	return stubDiscovery{lists: resources}
}

func deploymentResourceList() *metav1.APIResourceList {
	return &metav1.APIResourceList{
		GroupVersion: "apps/v1",
		APIResources: []metav1.APIResource{
			{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		},
	}
}

func secretResourceList() *metav1.APIResourceList {
	return &metav1.APIResourceList{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		},
	}
}

func newAPIResourceService(t *testing.T, allowedVerbs map[string]bool, disc discovery.DiscoveryInterface) *APIResourceService {
	t.Helper()
	typed := fakeAPIResourcesClientset(t, allowedVerbs)
	return NewAPIResourceService(policy.New(config.Default().Security),
		func(_ context.Context, _ string) (kubernetes.Interface, discovery.DiscoveryInterface, error) {
			return typed, disc, nil
		})
}

func TestAPIResourcesEffectivePermissions(t *testing.T) {
	svc := newAPIResourceService(t,
		map[string]bool{"get": true, "list": true, "watch": false},
		fakeDiscoveryWith(deploymentResourceList()))

	entries, err := svc.List(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Group != "apps" || e.Version != "v1" || e.Resource != "deployments" || e.Kind != "Deployment" {
		t.Errorf("identity: %+v", e)
	}
	if !e.Allowed["get"] || !e.Allowed["list"] || e.Allowed["watch"] {
		t.Errorf("allowed must reflect SSAR results: %+v", e.Allowed)
	}
	if e.PolicyBlocked {
		t.Error("Deployment must not be policyBlocked")
	}
}

func TestAPIResourcesPolicyBlockedSkipsSSAR(t *testing.T) {
	svc := newAPIResourceService(t,
		map[string]bool{},
		fakeDiscoveryWith(secretResourceList()))

	entries, err := svc.List(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !entries[0].PolicyBlocked {
		t.Error("Secret must be policyBlocked (spec §6.1)")
	}
	if len(entries[0].Allowed) != 0 {
		t.Errorf("policy-blocked resource must not issue SSARs: %+v", entries[0].Allowed)
	}
}

func TestAPIResourcesCacheTTL(t *testing.T) {
	svc := newAPIResourceService(t,
		map[string]bool{"get": true},
		fakeDiscoveryWith(deploymentResourceList()))
	now := time.Now()
	svc.now = func() time.Time { return now }

	if _, err := svc.List(context.Background(), "prod"); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	cached, ok := svc.cache["prod"]
	svc.mu.Unlock()
	if !ok {
		t.Fatal("expected cache entry")
	}

	// Within TTL: same slice, no refetch.
	if got := cached.fetchedAt; !got.Equal(now) {
		t.Errorf("fetchedAt: got %v", got)
	}

	// After TTL: refetch updates the timestamp.
	now = now.Add(ssarCacheTTL + time.Second)
	if _, err := svc.List(context.Background(), "prod"); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	cached = svc.cache["prod"]
	svc.mu.Unlock()
	if !cached.fetchedAt.Equal(now) {
		t.Error("cache must refresh after TTL")
	}
}
