package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/agnaldom/mcp-k8s/internal/policy"
)

// ErrMetricsUnavailable marks a cluster without the metrics API
// (no metrics-server). It is a normal cluster state, not a server
// failure — the consumer should continue without it (spec §3.6).
var ErrMetricsUnavailable = errors.New("metrics unavailable")

// metricsTimeout is the cascading timeout for metrics reads (spec §8).
const metricsTimeout = 10 * time.Second

var (
	podsGVR  = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	nodesGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
)

// MetricsClients resolves a dynamic client for the named cluster.
type MetricsClients func(ctx context.Context, cluster string) (dynamic.Interface, error)

// MetricsService reads pod/node consumption from the metrics API
// (spec §13 step 14).
type MetricsService struct {
	Policy  *policy.Policy
	Clients MetricsClients
	Timeout time.Duration
}

func NewMetricsService(pol *policy.Policy, clients MetricsClients) *MetricsService {
	return &MetricsService{Policy: pol, Clients: clients, Timeout: metricsTimeout}
}

// MetricsEntry is one pod or node with its usage. Pods carry per-
// container usage; nodes carry top-level usage.
type MetricsEntry struct {
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace,omitempty"`
	Usage      map[string]string `json:"usage,omitempty"`
	Containers []ContainerUsage  `json:"containers,omitempty"`
}

// ContainerUsage is one container's consumption within a pod.
type ContainerUsage struct {
	Name  string            `json:"name"`
	Usage map[string]string `json:"usage"`
}

// Pods returns per-pod consumption for one namespace (empty = all).
func (s *MetricsService) Pods(ctx context.Context, cluster, namespace string) ([]MetricsEntry, error) {
	return s.list(ctx, cluster, podsGVR, namespace, true)
}

// Nodes returns per-node consumption.
func (s *MetricsService) Nodes(ctx context.Context, cluster string) ([]MetricsEntry, error) {
	return s.list(ctx, cluster, nodesGVR, "", false)
}

func (s *MetricsService) list(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace string, namespaced bool) ([]MetricsEntry, error) {
	if err := s.Policy.ClusterAllowed(cluster); err != nil {
		return nil, err
	}
	clients, err := s.Clients(ctx, cluster)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()

	var ul *unstructured.UnstructuredList
	if namespaced && namespace != "" {
		ul, err = clients.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		ul, err = clients.Resource(gvr).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: metrics-server not installed", ErrMetricsUnavailable)
		}
		return nil, fmt.Errorf("list %s: %w", gvr.String(), err)
	}
	out := make([]MetricsEntry, 0, len(ul.Items))
	for i := range ul.Items {
		item := &ul.Items[i]
		entry := MetricsEntry{Name: item.GetName()}
		if namespaced {
			entry.Namespace = item.GetNamespace()
			containers, _, _ := unstructured.NestedSlice(item.Object, "containers")
			for _, c := range containers {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				entry.Containers = append(entry.Containers, ContainerUsage{
					Name:  stringField(cm, "name"),
					Usage: stringMapField(cm, "usage"),
				})
			}
		} else {
			entry.Usage = stringMapField(map[string]any{"usage": nestedRaw(item.Object, "usage")}, "usage")
		}
		out = append(out, entry)
	}
	return out, nil
}

func nestedRaw(object map[string]any, path ...string) any {
	v, _, _ := unstructured.NestedFieldNoCopy(object, path...)
	return v
}

func stringField(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func stringMapField(m map[string]any, key string) map[string]string {
	raw, ok := m[key].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}
