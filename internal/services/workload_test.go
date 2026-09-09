package services

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	"github.com/agnaldom/mcp-k8s/internal/signals"
)

var workloadNow = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

type stubEvents struct {
	res *EventsResult
	err error
}

func (s *stubEvents) Events(_ context.Context, _ EventsOptions) (*EventsResult, error) {
	return s.res, s.err
}

type stubPodMetrics struct {
	entries []MetricsEntry
	err     error
}

func (s *stubPodMetrics) Pods(_ context.Context, _, _ string) ([]MetricsEntry, error) {
	return s.entries, s.err
}

func workloadDeployment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "payments-api", "namespace": "payments"},
		"spec": map[string]any{
			"replicas": int64(3),
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "payments-api"}},
			},
		},
		"status": map[string]any{
			"availableReplicas": int64(1),
			"conditions": []any{
				map[string]any{
					"type":               "Available",
					"status":             "False",
					"reason":             "MinimumReplicasUnavailable",
					"message":            "Deployment does not have minimum availability",
					"lastTransitionTime": workloadNow.Add(-10 * time.Minute).Format(time.RFC3339),
				},
			},
		},
	}}
}

// t_runtime is a zero-size helper so fixture conversion stays a plain
// function without a *testing.T; conversion errors cannot happen for
// these fixtures.
type t_runtime struct{}

func toUnstructuredT(_ t_runtime, obj runtime.Object) *unstructured.Unstructured {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		panic(err)
	}
	return &unstructured.Unstructured{Object: m}
}

func newWorkloadService(t *testing.T, objs map[string]runtime.Object, lists map[string]*unstructured.UnstructuredList, events eventCollector, metrics metricsReader) *WorkloadService {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{
		gvrPods:           "PodList",
		gvrServices:       "ServiceList",
		gvrEndpointSlices: "EndpointSliceList",
		gvrHPAs:           "HorizontalPodAutoscalerList",
		gvrPDBs:           "PodDisruptionBudgetList",
		gvrPVC:            "PersistentVolumeClaimList",
		gvrNodes:          "NodeList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	dyn.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		gvr := action.GetResource()
		key := gvr.Group + "/" + gvr.Resource
		switch action.GetVerb() {
		case "get":
			if obj, ok := objs[key+"/"+action.(k8stesting.GetAction).GetName()]; ok {
				return true, obj, nil
			}
			return false, nil, nil
		case "list":
			if list, ok := lists[key]; ok {
				return true, list, nil
			}
			return true, &unstructured.UnstructuredList{}, nil
		default:
			return false, nil, nil
		}
	})
	pol := policy.New(config.Default().Security)
	dynamicClients := func(_ context.Context, _ string) (dynamic.Interface, discovery.DiscoveryInterface, meta.RESTMapper, error) {
		return dyn, (&fake.Clientset{}).Discovery(), relationshipMapper(), nil
	}
	svc := NewWorkloadService(
		pol,
		dynamicClients,
		metrics,
		NewRelationshipService(pol, dynamicClients),
		events,
		signals.ThresholdsFrom(config.Signals{}),
		20,
		8,
	)
	svc.now = func() time.Time { return workloadNow }
	return svc
}

func TestWorkloadContextFull(t *testing.T) {
	objs := map[string]runtime.Object{
		"/deployments/payments-api": workloadDeployment(),
		"/nodes/node-1": toUnstructuredT(t_runtime{}, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
				Type: corev1.NodeReady, Status: corev1.ConditionTrue,
			}}},
		}),
		"/persistentvolumeclaims/payments-api-cache": &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "PersistentVolumeClaim",
			"metadata":   map[string]any{"name": "payments-api-cache", "namespace": "payments"},
			"status":     map[string]any{"phase": "Bound"},
		}},
	}
	pending := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "payments-api-x2",
			Namespace: "payments",
			Labels:    map[string]string{"app": "payments-api"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	lists := map[string]*unstructured.UnstructuredList{
		"/pods": newUnstructuredList(
			toUnstructuredT(t_runtime{}, signalsPod()),
			toUnstructuredT(t_runtime{}, pending),
		),
		"/services": newUnstructuredList(relService("payments-api", map[string]any{"app": "payments-api"})),
		"discovery.k8s.io/endpointslices": newUnstructuredList(relEndpointSlice("payments-api",
			map[string]any{"conditions": map[string]any{"ready": true}})),
		"autoscaling/horizontalpodautoscalers": newUnstructuredList(relHPA("payments-api-hpa", "Deployment", "payments-api")),
		"policy/poddisruptionbudgets":          newUnstructuredList(),
	}
	events := &stubEvents{res: &EventsResult{
		Items: []NormalizedEvent{{
			Type:           "Warning",
			Reason:         "FailedScheduling",
			Message:        "0/3 nodes available",
			InvolvedKind:   "Pod",
			InvolvedName:   "payments-api-x2",
			Count:          4,
			LastTimestamp:  workloadNow.Add(-2 * time.Minute),
			FirstTimestamp: workloadNow.Add(-5 * time.Minute),
		}},
		Coverage: CoverageInfo{From: workloadNow.Add(-5 * time.Minute), To: workloadNow, Complete: true},
	}}
	metrics := &stubPodMetrics{entries: []MetricsEntry{{
		Name:      "payments-api-x1",
		Namespace: "payments",
		Containers: []ContainerUsage{{
			Name:  "api",
			Usage: map[string]string{"memory": "200Mi"},
		}},
	}}}
	svc := newWorkloadService(t, objs, lists, events, metrics)

	res, err := svc.Context(context.Background(), WorkloadOptions{
		Cluster: "prod", Namespace: "payments", Kind: "Deployment", Name: "payments-api",
		IncludeMetrics: true, EventLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Workload block.
	if res.Workload.Kind != "Deployment" || res.Workload.Name != "payments-api" {
		t.Errorf("workload identity: %+v", res.Workload)
	}
	if res.Workload.Replicas == nil || res.Workload.Replicas.Desired != 3 || res.Workload.Replicas.Available != 1 {
		t.Errorf("replicas: %+v", res.Workload.Replicas)
	}
	if len(res.Workload.Conditions) != 1 || res.Workload.Conditions[0].Reason != "MinimumReplicasUnavailable" {
		t.Errorf("conditions: %+v", res.Workload.Conditions)
	}

	// Pods block: 2 total, 1 detailed per phase counts.
	if res.Pods.Total != 2 || res.Pods.PhaseCounts["Pending"] != 1 || res.Pods.PhaseCounts["Running"] != 1 {
		t.Errorf("pods summary: %+v", res.Pods)
	}
	if len(res.Pods.Items) != 2 {
		t.Fatalf("pod details: %+v", res.Pods.Items)
	}
	detail := res.Pods.Items[0]
	if detail.Name != "payments-api-x1" || detail.Node != "node-1" {
		t.Errorf("pod detail: %+v", detail)
	}
	if len(detail.Containers) != 1 {
		t.Fatalf("containers: %+v", detail.Containers)
	}
	c := detail.Containers[0]
	if c.Name != "api" || c.Ready || c.RestartCount != 12 {
		t.Errorf("container: %+v", c)
	}
	if c.State.Waiting == nil || c.State.Waiting.Reason != "CrashLoopBackOff" {
		t.Errorf("container state: %+v", c.State)
	}
	if c.Limits.Memory().IsZero() {
		t.Errorf("limits must be carried: %+v", c.Limits)
	}

	// Relationships and events.
	if res.Relationships == nil || len(res.Relationships.Services) != 1 || res.Relationships.Services[0].ReadyEndpoints != 1 {
		t.Errorf("relationships: %+v", res.Relationships)
	}
	if len(res.Events) != 1 || res.Events[0].Reason != "FailedScheduling" {
		t.Errorf("events: %+v", res.Events)
	}
	if !res.Coverage.Events.Complete {
		t.Errorf("event coverage: %+v", res.Coverage.Events)
	}

	// Metrics.
	if len(res.Metrics) != 1 || !res.Coverage.Metrics.Complete {
		t.Errorf("metrics: %+v coverage: %+v", res.Metrics, res.Coverage.Metrics)
	}

	// Signals: the broken workload must light up.
	got := map[string]bool{}
	for _, s := range res.Signals {
		got[s.Type] = true
	}
	for _, want := range []string{
		signals.WorkloadUnavailable, signals.ContainerCrashLoop, signals.ContainerNotReady,
		signals.HighRestartCount,
	} {
		if !got[want] {
			t.Errorf("signal %s not fired: %v", want, res.Signals)
		}
	}

	if len(res.Unavailable) != 0 {
		t.Errorf("unexpected unavailable: %+v", res.Unavailable)
	}
}

func TestWorkloadContextPodKind(t *testing.T) {
	pod := signalsPod()
	pod.Name = "payments-api-x1"
	objs := map[string]runtime.Object{
		"/pods/payments-api-x1": toUnstructuredT(t_runtime{}, pod),
	}
	svc := newWorkloadService(t, objs, map[string]*unstructured.UnstructuredList{}, &stubEvents{}, nil)

	res, err := svc.Context(context.Background(), WorkloadOptions{
		Cluster: "prod", Namespace: "payments", Kind: "Pod", Name: "payments-api-x1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Workload.Phase != "Running" {
		t.Errorf("pod phase: %+v", res.Workload)
	}
	if res.Pods.Total != 1 || res.Pods.Items[0].Name != "payments-api-x1" {
		t.Errorf("pods: %+v", res.Pods)
	}
}

func TestWorkloadContextRejectsUnknownKind(t *testing.T) {
	svc := newWorkloadService(t, map[string]runtime.Object{}, nil, nil, nil)
	_, err := svc.Context(context.Background(), WorkloadOptions{
		Cluster: "prod", Namespace: "payments", Kind: "ConfigMap", Name: "x",
	})
	if !errors.Is(err, ErrResourceTypeNotFound) {
		t.Fatalf("want RESOURCE_TYPE_NOT_FOUND sentinel, got %v", err)
	}
}

func TestWorkloadContextPartialFailure(t *testing.T) {
	objs := map[string]runtime.Object{
		"/deployments/payments-api": workloadDeployment(),
	}
	lists := map[string]*unstructured.UnstructuredList{
		"/pods": newUnstructuredList(toUnstructuredT(t_runtime{}, signalsPod())),
	}
	events := &stubEvents{err: errors.New("events backend down")}
	metrics := &stubPodMetrics{err: errors.New("metrics api unavailable")}
	svc := newWorkloadService(t, objs, lists, events, metrics)

	res, err := svc.Context(context.Background(), WorkloadOptions{
		Cluster: "prod", Namespace: "payments", Kind: "Deployment", Name: "payments-api",
		IncludeMetrics: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, u := range res.Unavailable {
		joined += u + ";"
	}
	if joined == "" {
		t.Fatal("expected unavailable sections")
	}
	// The rest is still delivered.
	if res.Pods.Total != 1 {
		t.Errorf("pods must survive events/metrics failure: %+v", res.Pods)
	}
	if len(res.Signals) == 0 {
		t.Error("signals must still fire")
	}
	if res.Coverage.Events.Complete {
		t.Error("event coverage must be incomplete when events failed")
	}
}
