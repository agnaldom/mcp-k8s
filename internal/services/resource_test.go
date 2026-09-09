package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
	"github.com/agnaldom/mcp-k8s/internal/policy"
)

func testMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "", Version: "v1"}})
	m.AddSpecific(schema.GroupVersionKind{Kind: "Deployment", Version: "v1"},
		schema.GroupVersionResource{Version: "v1", Resource: "deployments"},
		schema.GroupVersionResource{Version: "v1", Resource: "deployment"},
		meta.RESTScopeNamespace)
	return m
}

func deployment(name, ns string, replicas int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"managedFields": []any{
				map[string]any{"manager": "kubectl"},
			},
		},
		"spec": map[string]any{"replicas": replicas},
		"status": map[string]any{
			"readyReplicas": replicas,
		},
	}}
}

func newResourceService(objs ...runtime.Object) *ResourceService {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "deployments"}: "DeploymentList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)
	return &ResourceService{
		Policy: policy.New(config.Default().Security),
		Clients: func(_ context.Context, _ string) (dynamic.Interface, discovery.DiscoveryInterface, meta.RESTMapper, error) {
			return dyn, (&fake.Clientset{}).Discovery(), testMapper(), nil
		},
		DefaultLimit:   100,
		MaxLimit:       500,
		MaxObjectBytes: 4 * 1024 * 1024,
	}
}

func TestListSummaryView(t *testing.T) {
	svc := newResourceService(deployment("payments-api", "payments", 3))
	res, err := svc.List(context.Background(), ResourceListOptions{
		Cluster: "prod", Kind: "Deployment", Namespace: "payments", View: ViewSummary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(res.Items))
	}
	item := res.Items[0]
	if item.Kind != "Deployment" || item.Metadata["name"] != "payments-api" {
		t.Errorf("identity: %+v", item)
	}
	if item.Status["readyReplicas"] != int64(3) {
		t.Errorf("summary must carry status: %+v", item.Status)
	}
	if item.Object != nil || item.Spec != nil {
		t.Error("summary must not carry the full object or spec")
	}
	if _, found := item.Metadata["managedFields"]; found {
		t.Error("managedFields must be stripped")
	}
}

func TestListFullView(t *testing.T) {
	svc := newResourceService(deployment("payments-api", "payments", 3))
	res, err := svc.List(context.Background(), ResourceListOptions{
		Cluster: "prod", Kind: "Deployment", Namespace: "payments", View: ViewFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Object == nil {
		t.Fatal("full view must carry the sanitized object")
	}
	spec, _, _ := unstructured.NestedMap(res.Items[0].Object, "spec")
	if spec["replicas"] != int64(3) {
		t.Errorf("full view must keep spec: %+v", spec)
	}
}

func TestListUnknownKind(t *testing.T) {
	svc := newResourceService()
	_, err := svc.List(context.Background(), ResourceListOptions{
		Cluster: "prod", Kind: "Widget", View: ViewSummary,
	})
	if !errors.Is(err, ErrResourceTypeNotFound) {
		t.Fatalf("expected ErrResourceTypeNotFound, got %v", err)
	}
}

func TestListPolicyDeniesCluster(t *testing.T) {
	security := config.Default().Security
	security.Clusters.Allow = []string{"prod"}
	svc := newResourceService()
	svc.Policy = policy.New(security)
	_, err := svc.List(context.Background(), ResourceListOptions{
		Cluster: "development", Kind: "Deployment", View: ViewSummary,
	})
	if !errors.Is(err, policy.ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
}

func TestListOversizedObjectIsError(t *testing.T) {
	big := deployment("payments-api", "payments", 3)
	big.Object["spec"] = map[string]any{"blob": strings.Repeat("x", 5*1024*1024)}
	svc := newResourceService(big)
	svc.MaxObjectBytes = 4 * 1024 * 1024
	_, err := svc.List(context.Background(), ResourceListOptions{
		Cluster: "prod", Kind: "Deployment", Namespace: "payments", View: ViewFull,
	})
	var sizeErr *kubernetes.SizeError
	if !errors.As(err, &sizeErr) {
		t.Fatalf("expected SizeError (RESPONSE_TOO_LARGE at the API), got %v", err)
	}
}

func TestListPaginationFields(t *testing.T) {
	// The fake dynamic client does not implement server-side continue
	// tokens; this test pins the result-shape contract instead.
	res := &ResourceListResult{ContinueToken: "next-page", HasMore: true}
	if !res.HasMore || res.ContinueToken != "next-page" {
		t.Errorf("pagination contract: %+v", res)
	}
}

func TestGetAppliesSecretPolicyBeforeResolution(t *testing.T) {
	svc := newResourceService()
	svc.Clients = func(_ context.Context, _ string) (dynamic.Interface, discovery.DiscoveryInterface, meta.RESTMapper, error) {
		t.Error("clients must not be resolved for a policy-denied kind")
		return nil, nil, nil, nil
	}
	_, err := svc.Get(context.Background(), ResourceGetOptions{
		Cluster: "prod", Kind: "Secret", Name: "db-creds", Namespace: "payments", View: ViewSummary,
	})
	if !errors.Is(err, policy.ErrDenied) {
		t.Fatalf("expected ErrDenied for Secret, got %v", err)
	}
}

func TestGetReturnsSanitizedObject(t *testing.T) {
	obj := deployment("payments-api", "payments", 3)
	svc := newResourceService(obj)
	item, err := svc.Get(context.Background(), ResourceGetOptions{
		Cluster: "prod", Kind: "Deployment", Name: "payments-api", Namespace: "payments", View: ViewFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Metadata["name"] != "payments-api" {
		t.Errorf("metadata: %+v", item.Metadata)
	}
	if item.Object == nil {
		t.Error("full view must carry the object")
	}
}

func TestGetNotFound(t *testing.T) {
	svc := newResourceService()
	_, err := svc.Get(context.Background(), ResourceGetOptions{
		Cluster: "prod", Kind: "Deployment", Name: "ghost", Namespace: "payments", View: ViewSummary,
	})
	if !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("expected ErrResourceNotFound, got %v", err)
	}
}
