package services

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
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

var signalsNow = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

func signalsDeployment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "payments-api", "namespace": "payments"},
		"spec": map[string]any{
			"replicas": int64(3),
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "payments-api"}},
				"spec": map[string]any{
					"volumes": []any{
						map[string]any{"name": "cache", "persistentVolumeClaim": map[string]any{"claimName": "payments-api-cache"}},
					},
				},
			},
		},
		"status": map[string]any{"availableReplicas": int64(1)},
	}}
}

func signalsPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "payments-api-x1",
			Namespace: "payments",
			Labels:    map[string]string{"app": "payments-api"},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{{
				Name: "api",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.NewTime(signalsNow.Add(-6 * time.Minute)),
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "api",
				Ready:        false,
				RestartCount: 12,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "CrashLoopBackOff",
				}},
			}},
		},
	}
}

func toUnstructured(t *testing.T, obj runtime.Object) *unstructured.Unstructured {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: m}
}

func newSignalService(t *testing.T, objs map[string]runtime.Object, lists map[string]*unstructured.UnstructuredList) *SignalService {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{
		gvrPods:           "PodList",
		gvrServices:       "ServiceList",
		gvrEndpointSlices: "EndpointSliceList",
		gvrHPAs:           "HorizontalPodAutoscalerList",
		gvrPDBs:           "PodDisruptionBudgetList",
		metricsK8sPods:    "PodMetricsList",
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
	svc := NewSignalService(
		pol,
		dynamicClients,
		func(_ context.Context, _ string) (dynamic.Interface, error) { return dyn, nil },
		NewRelationshipService(pol, dynamicClients),
		signals.ThresholdsFrom(config.Signals{}),
		20,
	)
	svc.now = func() time.Time { return signalsNow }
	return svc
}

func TestSignalServiceEvaluate(t *testing.T) {
	objs := map[string]runtime.Object{
		"/deployments/payments-api": signalsDeployment(),
		"/nodes/node-1": toUnstructured(t, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
				Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady",
			}}},
		}),
		"/persistentvolumeclaims/payments-api-cache": &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "PersistentVolumeClaim",
			"metadata":   map[string]any{"name": "payments-api-cache", "namespace": "payments", "creationTimestamp": signalsNow.Add(-3 * time.Minute).Format(time.RFC3339)},
			"status":     map[string]any{"phase": "Pending"},
		}},
	}
	lists := map[string]*unstructured.UnstructuredList{
		"/pods":     newUnstructuredList(toUnstructured(t, signalsPod())),
		"/services": newUnstructuredList(relService("payments-api", map[string]any{"app": "payments-api"})),
		"discovery.k8s.io/endpointslices": newUnstructuredList(relEndpointSlice("payments-api",
			map[string]any{"conditions": map[string]any{"ready": false}})),
		"autoscaling/horizontalpodautoscalers": newUnstructuredList(),
		"policy/poddisruptionbudgets":          newUnstructuredList(),
		"metrics.k8s.io/pods": newUnstructuredList(&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "metrics.k8s.io/v1beta1",
			"kind":       "PodMetrics",
			"metadata":   map[string]any{"name": "payments-api-x1", "namespace": "payments"},
			"containers": []any{
				map[string]any{"name": "api", "usage": map[string]any{"memory": "950Mi"}},
			},
		}}),
	}
	svc := newSignalService(t, objs, lists)

	res, err := svc.Evaluate(context.Background(), SignalOptions{
		Cluster: "prod", Namespace: "payments", Kind: "Deployment", Name: "payments-api",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range res.Signals {
		got[s.Type] = true
	}
	for _, want := range []string{
		signals.WorkloadUnavailable,
		signals.ContainerCrashLoop,
		signals.ContainerNotReady,
		signals.HighRestartCount,
		signals.ServiceWithoutEndpoints,
		signals.PVCPending,
		signals.NodeNotReady,
		signals.MemoryNearLimit,
	} {
		if !got[want] {
			t.Errorf("signal %s not fired; signals: %v", want, res.Signals)
		}
	}
	if len(res.Unavailable) != 0 {
		t.Errorf("unexpected unavailable: %+v", res.Unavailable)
	}
}

func TestSignalServiceMetricsUnavailable(t *testing.T) {
	objs := map[string]runtime.Object{
		"/deployments/payments-api": signalsDeployment(),
	}
	lists := map[string]*unstructured.UnstructuredList{
		"/pods": newUnstructuredList(toUnstructured(t, signalsPod())),
	}
	svc := newSignalService(t, objs, lists)
	// The metrics list has no registered reactor entry... force failure by
	// swapping Metrics to a client whose list errors.
	svc.Metrics = func(_ context.Context, _ string) (dynamic.Interface, error) {
		return nil, context.DeadlineExceeded
	}

	res, err := svc.Evaluate(context.Background(), SignalOptions{
		Cluster: "prod", Namespace: "payments", Kind: "Deployment", Name: "payments-api",
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, u := range res.Unavailable {
		if strings.HasPrefix(u, "metrics:") {
			found = true
		}
	}
	if !found {
		t.Errorf("metrics failure must be recorded in unavailable: %+v", res.Unavailable)
	}
	// Workload and pod signals still fire.
	got := map[string]bool{}
	for _, s := range res.Signals {
		got[s.Type] = true
	}
	if !got[signals.WorkloadUnavailable] || !got[signals.ContainerCrashLoop] {
		t.Errorf("signals must survive metrics failure: %v", res.Signals)
	}
}
