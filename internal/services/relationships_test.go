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
	k8stesting "k8s.io/client-go/testing"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/policy"
)

func relationshipMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "", Version: "v1"}})
	for kind, resource := range map[string]string{
		"Pod":        "pods",
		"ReplicaSet": "replicasets",
		"Deployment": "deployments",
	} {
		gvk := schema.GroupVersionKind{Kind: kind, Version: "v1"}
		m.AddSpecific(gvk,
			schema.GroupVersionResource{Version: "v1", Resource: resource},
			schema.GroupVersionResource{Version: "v1", Resource: strings.ToLower(kind)},
			meta.RESTScopeNamespace)
	}
	return m
}

func relDeployment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "payments-api", "namespace": "payments"},
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "payments-api"}},
				"spec": map[string]any{
					"volumes": []any{
						map[string]any{"name": "cache", "persistentVolumeClaim": map[string]any{"claimName": "payments-api-cache"}},
						map[string]any{"name": "config", "configMap": map[string]any{"name": "payments-api-config"}},
					},
				},
			},
		},
	}}
}

func relService(name string, selector map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": name, "namespace": "payments"},
		"spec":       map[string]any{"selector": selector},
	}}
}

func relEndpointSlice(service string, endpoints ...map[string]any) *unstructured.Unstructured {
	eps := make([]any, 0, len(endpoints))
	for _, e := range endpoints {
		eps = append(eps, e)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "discovery.k8s.io/v1",
		"kind":       "EndpointSlice",
		"metadata": map[string]any{
			"name":      service + "-abc",
			"namespace": "payments",
			"labels":    map[string]any{"kubernetes.io/service-name": service},
		},
		"endpoints": eps,
	}}
}

func relHPA(name, kind, target string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"metadata":   map[string]any{"name": name, "namespace": "payments"},
		"spec": map[string]any{
			"scaleTargetRef": map[string]any{"kind": kind, "name": target},
		},
	}}
}

func relPDB(name string, matchLabels map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "policy/v1",
		"kind":       "PodDisruptionBudget",
		"metadata":   map[string]any{"name": name, "namespace": "payments"},
		"spec":       map[string]any{"selector": map[string]any{"matchLabels": matchLabels}},
	}}
}

// newRelationshipService wires a dynamic fake driven entirely by reactors,
// so each test declares exactly what the cluster returns per GVR. The
// dynamic fake requires every listed kind to be registered up front.
func newRelationshipService(t *testing.T, reactor k8stesting.ReactionFunc) *RelationshipService {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{
		gvrServices:       "ServiceList",
		gvrEndpointSlices: "EndpointSliceList",
		gvrHPAs:           "HorizontalPodAutoscalerList",
		gvrPDBs:           "PodDisruptionBudgetList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	dyn.PrependReactor("*", "*", reactor)
	return &RelationshipService{
		Policy: policy.New(config.Default().Security),
		Clients: func(_ context.Context, _ string) (dynamic.Interface, discovery.DiscoveryInterface, meta.RESTMapper, error) {
			return dyn, (&fake.Clientset{}).Discovery(), relationshipMapper(), nil
		},
	}
}

func relationshipReactor(t *testing.T, objects map[string]runtime.Object, lists map[string]*unstructured.UnstructuredList, errs map[string]error) k8stesting.ReactionFunc {
	t.Helper()
	return func(action k8stesting.Action) (bool, runtime.Object, error) {
		gvr := action.GetResource()
		key := gvr.Resource + "/" + action.GetVerb()
		if err, ok := errs[key]; ok {
			return true, nil, err
		}
		switch action.GetVerb() {
		case "get":
			obj, ok := objects[gvr.Resource+"/"+action.(k8stesting.GetAction).GetName()]
			if !ok {
				return false, nil, nil // let the tracker answer with NotFound
			}
			return true, obj, nil
		case "list":
			if list, ok := lists[gvr.Resource]; ok {
				return true, list, nil
			}
			return true, &unstructured.UnstructuredList{}, nil
		default:
			return false, nil, nil
		}
	}
}

func TestRelationshipResolveDeployment(t *testing.T) {
	objects := map[string]runtime.Object{
		"deployments/payments-api": relDeployment(),
	}
	lists := map[string]*unstructured.UnstructuredList{
		"services": newUnstructuredList(relService("payments-api", map[string]any{"app": "payments-api"}),
			relService("payments-internal", map[string]any{"app": "payments-api", "track": "canary"}), // not a subset of pod labels
			relService("payments-manual", nil), // no selector: does not select pods
			relService("other-svc", map[string]any{"app": "other"})),
		"endpointslices": newUnstructuredList(relEndpointSlice("payments-api",
			map[string]any{"conditions": map[string]any{"ready": true}},
			map[string]any{"conditions": map[string]any{"ready": true}},
			map[string]any{"conditions": map[string]any{"ready": false}},
			map[string]any{})), // no conditions: counts as ready
		"horizontalpodautoscalers": newUnstructuredList(relHPA("payments-api-hpa", "Deployment", "payments-api"),
			relHPA("other-hpa", "Deployment", "other")),
		"poddisruptionbudgets": newUnstructuredList(relPDB("payments-api-pdb", map[string]any{"app": "payments-api"}),
			relPDB("other-pdb", map[string]any{"app": "other"})),
	}
	svc := newRelationshipService(t, relationshipReactor(t, objects, lists, nil))

	res, err := svc.Resolve(context.Background(), RelationshipOptions{
		Cluster: "prod", Namespace: "payments", Kind: "Deployment", Name: "payments-api",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Owners) != 0 {
		t.Errorf("deployment has no owners, got %+v", res.Owners)
	}
	if len(res.Services) != 1 || res.Services[0].Name != "payments-api" {
		t.Fatalf("services: %+v", res.Services)
	}
	if res.Services[0].ReadyEndpoints != 3 {
		t.Errorf("readyEndpoints: want 3 (2 ready + 1 without conditions), got %d", res.Services[0].ReadyEndpoints)
	}
	if len(res.HPA) != 1 || res.HPA[0] != "payments-api-hpa" {
		t.Errorf("hpa: %+v", res.HPA)
	}
	if len(res.PDB) != 1 || res.PDB[0] != "payments-api-pdb" {
		t.Errorf("pdb: %+v", res.PDB)
	}
	if len(res.PVC) != 1 || res.PVC[0] != "payments-api-cache" {
		t.Errorf("pvc: %+v", res.PVC)
	}
	if len(res.Unavailable) != 0 {
		t.Errorf("unexpected unavailable: %+v", res.Unavailable)
	}
}

func TestRelationshipOwnerChain(t *testing.T) {
	rs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata": map[string]any{
			"name":      "payments-api-89abc",
			"namespace": "payments",
			"ownerReferences": []any{
				map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "payments-api", "controller": true},
			},
		},
	}}
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      "payments-api-89abc-x1",
			"namespace": "payments",
			"labels":    map[string]any{"app": "payments-api"},
			"ownerReferences": []any{
				map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "payments-api-89abc", "controller": true},
				// non-controller owner must be ignored
				map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "name": "noise", "controller": false},
			},
		},
		"spec": map[string]any{"nodeName": "node-1"},
	}}
	objects := map[string]runtime.Object{
		"pods/payments-api-89abc-x1":     pod,
		"replicasets/payments-api-89abc": rs,
		"deployments/payments-api":       relDeployment(),
	}
	svc := newRelationshipService(t, relationshipReactor(t, objects, map[string]*unstructured.UnstructuredList{
		"services": newUnstructuredList(relService("payments-api", map[string]any{"app": "payments-api"})),
	}, nil))

	res, err := svc.Resolve(context.Background(), RelationshipOptions{
		Cluster: "prod", Namespace: "payments", Kind: "Pod", Name: "payments-api-89abc-x1",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []OwnerStep{{Kind: "ReplicaSet", Name: "payments-api-89abc"}, {Kind: "Deployment", Name: "payments-api"}}
	if len(res.Owners) != len(want) {
		t.Fatalf("owners: want %+v, got %+v", want, res.Owners)
	}
	for i := range want {
		if res.Owners[i] != want[i] {
			t.Fatalf("owners: want %+v, got %+v", want, res.Owners)
		}
	}
	// The pod's own labels drive Service selection.
	if len(res.Services) != 1 || res.Services[0].Name != "payments-api" {
		t.Errorf("services: %+v", res.Services)
	}
}

func TestRelationshipPartialFailure(t *testing.T) {
	objects := map[string]runtime.Object{
		"deployments/payments-api": relDeployment(),
	}
	lists := map[string]*unstructured.UnstructuredList{
		"services": newUnstructuredList(relService("payments-api", map[string]any{"app": "payments-api"})),
	}
	errs := map[string]error{
		"poddisruptionbudgets/list":     errors.New("forbidden"),
		"horizontalpodautoscalers/list": errors.New("forbidden"),
	}
	svc := newRelationshipService(t, relationshipReactor(t, objects, lists, errs))

	res, err := svc.Resolve(context.Background(), RelationshipOptions{
		Cluster: "prod", Namespace: "payments", Kind: "Deployment", Name: "payments-api",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Services) != 1 {
		t.Errorf("services must survive unrelated failures: %+v", res.Services)
	}
	if len(res.Unavailable) != 2 {
		t.Fatalf("unavailable: %+v", res.Unavailable)
	}
	joined := strings.Join(res.Unavailable, ";")
	if !strings.Contains(joined, "pdb:") || !strings.Contains(joined, "hpa:") {
		t.Errorf("unavailable reasons: %s", joined)
	}
}

func TestRelationshipRequiresNamespace(t *testing.T) {
	svc := newRelationshipService(t, func(action k8stesting.Action) (bool, runtime.Object, error) {
		return false, nil, nil
	})
	_, err := svc.Resolve(context.Background(), RelationshipOptions{
		Cluster: "prod", Kind: "Deployment", Name: "payments-api",
	})
	if err == nil || !strings.Contains(err.Error(), "namespace is required") {
		t.Fatalf("want namespace error, got %v", err)
	}
}

func TestPVCNamesStatefulSet(t *testing.T) {
	sts := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "StatefulSet",
		"metadata":   map[string]any{"name": "payments-db", "namespace": "payments"},
		"spec": map[string]any{
			"volumeClaimTemplates": []any{
				map[string]any{"metadata": map[string]any{"name": "data"}},
				map[string]any{"metadata": map[string]any{"name": "data"}}, // duplicate: reported once
				map[string]any{"metadata": map[string]any{"name": "wal"}},
			},
		},
	}}
	got := pvcNames(sts)
	want := []string{"data", "wal"}
	if len(got) != len(want) {
		t.Fatalf("pvcNames: want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pvcNames: want %v, got %v", want, got)
		}
	}
}

func newUnstructuredList(items ...*unstructured.Unstructured) *unstructured.UnstructuredList {
	list := &unstructured.UnstructuredList{Object: map[string]any{}}
	for i := range items {
		list.Items = append(list.Items, *items[i])
	}
	return list
}
