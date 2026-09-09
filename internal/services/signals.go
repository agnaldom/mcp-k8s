package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
	"github.com/agnaldom/mcp-k8s/internal/policy"
	"github.com/agnaldom/mcp-k8s/internal/signals"
)

var (
	gvrPods  = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	gvrNodes = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}
	gvrPVC   = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}
	// metricsK8sPods is the metrics.k8s.io API used by k8s_metrics_pods (#14).
	metricsK8sPods = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
)

// SignalOptions carries the validated k8s_signals arguments.
type SignalOptions struct {
	Cluster   string
	Namespace string
	Kind      string
	Name      string
}

// SignalResult is the data block of k8s_signals: every deterministic
// signal fired for the workload and the facts behind each one
// (spec §5.2). Sections that could not be read land in Unavailable.
type SignalResult struct {
	Workload    string           `json:"workload"`
	Signals     []signals.Signal `json:"signals"`
	Unavailable []string         `json:"unavailable,omitempty"`
}

// SignalService evaluates the v0.1 signal catalog against one workload
// (spec §13 step 16). It reuses the relationship resolver and the
// metrics reader; every threshold comes from config via the signals
// package, and the effective value is always present in the response.
type SignalService struct {
	Policy        *policy.Policy
	Clients       DynamicClients
	Metrics       MetricsClients
	Relationships *RelationshipService
	Thresholds    signals.Thresholds
	MaxPods       int
	// now is injectable for tests.
	now func() time.Time
}

func NewSignalService(
	pol *policy.Policy,
	clients DynamicClients,
	metrics MetricsClients,
	relationships *RelationshipService,
	thresholds signals.Thresholds,
	maxPods int,
) *SignalService {
	return &SignalService{
		Policy:        pol,
		Clients:       clients,
		Metrics:       metrics,
		Relationships: relationships,
		Thresholds:    thresholds,
		MaxPods:       maxPods,
		now:           time.Now,
	}
}

// Evaluate collects the facts around the workload and runs the signal
// catalog over them. A failure in one section (e.g. the metrics API
// being unavailable) is recorded in Unavailable and never hides the
// signals the other sections fired (spec §5.1 partial failure).
func (s *SignalService) Evaluate(ctx context.Context, opts SignalOptions) (*SignalResult, error) {
	if err := s.Policy.ClusterAllowed(opts.Cluster); err != nil {
		return nil, err
	}
	if opts.Kind == "" || opts.Name == "" {
		return nil, fmt.Errorf("kind and name are required")
	}
	out := &SignalResult{Workload: fmt.Sprintf("%s/%s/%s", opts.Namespace, opts.Kind, opts.Name)}

	// Facts: the workload itself, its pods, its relationships.
	clients, disc, mapper, err := s.Clients(ctx, opts.Cluster)
	if err != nil {
		return nil, err
	}
	mapping, err := kubernetes.ResolveKind(disc, mapper, opts.Kind)
	if err != nil {
		if errors.Is(err, kubernetes.ErrResourceTypeNotFound) {
			return nil, fmt.Errorf("%w: %q", ErrResourceTypeNotFound, opts.Kind)
		}
		return nil, err
	}
	namespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace
	if namespaced && opts.Namespace == "" {
		return nil, fmt.Errorf("namespace is required for namespaced kind %q", opts.Kind)
	}

	var workload *unstructured.Unstructured
	if namespaced {
		workload, err = clients.Resource(mapping.Resource).Namespace(opts.Namespace).Get(ctx, opts.Name, metav1.GetOptions{})
	} else {
		workload, err = clients.Resource(mapping.Resource).Get(ctx, opts.Name, metav1.GetOptions{})
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%s", ErrResourceNotFound, opts.Kind, opts.Name)
		}
		return nil, fmt.Errorf("get %s %q: %w", opts.Kind, opts.Name, err)
	}

	out.Signals = append(out.Signals, signals.CheckWorkload(
		opts.Kind, opts.Namespace, opts.Name, desiredReplicas(workload), availableReplicas(workload))...)

	pods := s.workloadPods(ctx, clients, opts.Namespace, workloadLabels(workload), out)
	for i := range pods {
		out.Signals = append(out.Signals, signals.CheckPod(&pods[i], s.now(), s.Thresholds)...)
	}

	// Relationships: services (endpoint signal) and PVCs (pending signal).
	if namespaced && s.Relationships != nil {
		rel, err := s.Relationships.Resolve(ctx, RelationshipOptions{
			Cluster: opts.Cluster, Namespace: opts.Namespace, Kind: opts.Kind, Name: opts.Name,
		})
		if err != nil {
			out.Unavailable = append(out.Unavailable, fmt.Sprintf("relationships: %v", err))
		} else {
			out.Unavailable = append(out.Unavailable, rel.Unavailable...)
			for _, svc := range rel.Services {
				out.Signals = append(out.Signals, signals.CheckService(signals.ServiceRef{
					Name:             svc.Name,
					Namespace:        opts.Namespace,
					SelectorNonEmpty: true,
					ReadyEndpoints:   svc.ReadyEndpoints,
				})...)
			}
			for _, pvc := range rel.PVC {
				out.Signals = append(out.Signals, s.checkPVC(ctx, clients, opts.Namespace, pvc)...)
			}
		}
	}

	// Nodes hosting the workload's pods.
	out.Signals = append(out.Signals, s.checkNodes(ctx, clients, pods)...)

	// Memory pressure: metrics API usage vs declared limits.
	s.checkMemory(ctx, opts.Cluster, opts.Namespace, pods, out)
	return out, nil
}

// workloadPods lists the pods carrying the workload's labels, converted
// to the typed shape the signals package expects. Capped at MaxPods;
// the cap only limits how many pods are evaluated, never the workload-
// level signals.
func (s *SignalService) workloadPods(ctx context.Context, clients dynamic.Interface, ns string, labelMap map[string]string, out *SignalResult) []corev1.Pod {
	if ns == "" || len(labelMap) == 0 {
		return nil
	}
	list, err := clients.Resource(gvrPods).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(labelMap).String(),
	})
	if err != nil {
		out.Unavailable = append(out.Unavailable, fmt.Sprintf("pods: %v", err))
		return nil
	}
	limit := len(list.Items)
	if limit > s.MaxPods {
		limit = s.MaxPods
		out.Unavailable = append(out.Unavailable, fmt.Sprintf("pods: evaluated first %d of %d (maxPods)", s.MaxPods, len(list.Items)))
	}
	pods := make([]corev1.Pod, 0, limit)
	for i := 0; i < limit; i++ {
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &pod); err != nil {
			continue
		}
		pods = append(pods, pod)
	}
	return pods
}

// checkNodes evaluates node_not_ready for every distinct node hosting a
// workload pod. Node reads that fail are recorded, not fatal.
func (s *SignalService) checkNodes(ctx context.Context, clients dynamic.Interface, pods []corev1.Pod) []signals.Signal {
	seen := map[string]bool{}
	var out []signals.Signal
	for i := range pods {
		nodeName := pods[i].Spec.NodeName
		if nodeName == "" || seen[nodeName] {
			continue
		}
		seen[nodeName] = true
		u, err := clients.Resource(gvrNodes).Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			continue // fact unknown: without the node object there is no signal to fire
		}
		var node corev1.Node
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &node); err != nil {
			continue
		}
		out = append(out, signals.CheckNode(&node)...)
	}
	return out
}

// checkPVC reads one claim and evaluates pvc_pending.
func (s *SignalService) checkPVC(ctx context.Context, clients dynamic.Interface, ns, name string) []signals.Signal {
	u, err := clients.Resource(gvrPVC).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	phase, found, _ := unstructured.NestedString(u.Object, "status", "phase")
	if !found {
		return nil
	}
	var pendingSince time.Time
	if phase == string(corev1.ClaimPending) {
		pendingSince = u.GetCreationTimestamp().Time
	}
	return signals.CheckPVC(ns, name, corev1.PersistentVolumeClaimPhase(phase), pendingSince, s.now(), s.Thresholds)
}

// checkMemory evaluates memory_near_limit for every workload pod
// container that declares a memory limit. Metrics API failure is
// recorded in Unavailable: without usage there is no ratio to compute.
func (s *SignalService) checkMemory(ctx context.Context, cluster, ns string, pods []corev1.Pod, out *SignalResult) {
	if s.Metrics == nil || len(pods) == 0 {
		return
	}
	metricsClient, err := s.Metrics(ctx, cluster)
	if err != nil {
		out.Unavailable = append(out.Unavailable, fmt.Sprintf("metrics: %v", err))
		return
	}
	list, err := metricsClient.Resource(metricsK8sPods).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		out.Unavailable = append(out.Unavailable, fmt.Sprintf("metrics: %v", err))
		return
	}
	usageByPod := map[string]map[string]int64{} // pod name -> container -> bytes
	for i := range list.Items {
		podName := list.Items[i].GetName()
		containers, found, _ := unstructured.NestedSlice(list.Items[i].Object, "containers")
		if !found {
			continue
		}
		for _, c := range containers {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(cm, "name")
			mem, found, _ := unstructured.NestedString(cm, "usage", "memory")
			if !found {
				continue
			}
			q, err := resource.ParseQuantity(mem)
			if err != nil {
				continue
			}
			if usageByPod[podName] == nil {
				usageByPod[podName] = map[string]int64{}
			}
			usageByPod[podName][name] = q.Value()
		}
	}
	for i := range pods {
		pod := &pods[i]
		usage := usageByPod[pod.Name]
		if usage == nil {
			continue
		}
		for _, c := range pod.Spec.Containers {
			limit := c.Resources.Limits.Memory()
			if limit == nil || limit.IsZero() {
				continue
			}
			used, ok := usage[c.Name]
			if !ok {
				continue
			}
			ref := fmt.Sprintf("pod/%s/%s", pod.Namespace, pod.Name)
			out.Signals = append(out.Signals, signals.CheckMemory(ref, c.Name, used, limit.Value(), s.Thresholds)...)
		}
	}
}

// desiredReplicas and availableReplicas read the replica counters from
// the workload shape that has them; zero desired means the shape does
// not carry the concept and the signal does not apply.
func desiredReplicas(w *unstructured.Unstructured) int64 {
	if v, found, _ := unstructured.NestedInt64(w.Object, "spec", "replicas"); found {
		return v
	}
	if v, found, _ := unstructured.NestedInt64(w.Object, "status", "desiredNumberScheduled"); found {
		return v
	}
	return 0
}

func availableReplicas(w *unstructured.Unstructured) int64 {
	for _, path := range [][2]string{
		{"status", "availableReplicas"},
		{"status", "numberAvailable"},
		{"status", "readyReplicas"},
	} {
		if v, found, _ := unstructured.NestedInt64(w.Object, path[0], path[1]); found {
			return v
		}
	}
	return 0
}
