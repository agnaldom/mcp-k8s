package services

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/policy"
)

func metricsPod(name, ns, cpu, mem string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "metrics.k8s.io/v1beta1",
		"kind":       "PodMetrics",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"containers": []any{
			map[string]any{"name": "api", "usage": map[string]any{"cpu": cpu, "memory": mem}},
		},
	}}
}

func metricsNode(name, cpu string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "metrics.k8s.io/v1beta1",
		"kind":       "NodeMetrics",
		"metadata":   map[string]any{"name": name},
		"usage":      map[string]any{"cpu": cpu},
	}}
}

func metricsServiceWith(items []unstructured.Unstructured, err error) *MetricsService {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			podsGVR:  "PodMetricsList",
			nodesGVR: "NodeMetricsList",
		})
	dyn.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if err != nil {
			return true, nil, err
		}
		kind := "PodMetricsList"
		if action.GetResource() == nodesGVR {
			kind = "NodeMetricsList"
		}
		list := &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "metrics.k8s.io/v1beta1", "kind": kind}}
		for i := range items {
			list.Items = append(list.Items, items[i])
		}

		return true, list, nil
	})
	return NewMetricsService(policy.New(config.Default().Security),
		func(_ context.Context, _ string) (dynamic.Interface, error) { return dyn, nil })
}

func TestMetricsPods(t *testing.T) {
	svc := metricsServiceWith([]unstructured.Unstructured{*metricsPod("api-0", "payments", "12m", "128Mi")}, nil)
	entries, err := svc.Pods(context.Background(), "prod", "payments")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "api-0" || entries[0].Namespace != "payments" {
		t.Fatalf("entries: %+v", entries)
	}
	if entries[0].Containers[0].Usage["cpu"] != "12m" || entries[0].Containers[0].Usage["memory"] != "128Mi" {
		t.Errorf("usage: %+v", entries[0].Containers)
	}
}

func TestMetricsNodes(t *testing.T) {
	svc := metricsServiceWith([]unstructured.Unstructured{*metricsNode("node-1", "350m")}, nil)
	entries, err := svc.Nodes(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Usage["cpu"] != "350m" {
		t.Errorf("entries: %+v", entries)
	}
}

func TestMetricsUnavailable(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}, "")
	svc := metricsServiceWith(nil, notFound)
	_, err := svc.Pods(context.Background(), "prod", "")
	if !errors.Is(err, ErrMetricsUnavailable) {
		t.Fatalf("expected ErrMetricsUnavailable, got %v", err)
	}
}
