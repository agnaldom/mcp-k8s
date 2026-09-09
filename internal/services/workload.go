package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
	"github.com/agnaldom/mcp-k8s/internal/policy"
	"github.com/agnaldom/mcp-k8s/internal/signals"
)

// WorkloadKinds is the accepted kind set (spec §5.1).
var WorkloadKinds = map[string]bool{
	"Pod":         true,
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"Job":         true,
	"CronJob":     true,
}

// eventCollector and metricsReader are the narrow interfaces the
// workload context needs from the events and metrics services; tests
// stub them directly.
type eventCollector interface {
	Events(ctx context.Context, opts EventsOptions) (*EventsResult, error)
}

type metricsReader interface {
	Pods(ctx context.Context, cluster, namespace string) ([]MetricsEntry, error)
}

// WorkloadOptions carries the validated k8s_workload_context arguments
// (spec §5.1).
type WorkloadOptions struct {
	Cluster   string
	Namespace string
	Kind      string
	Name      string
	// IncludeMetrics adds per-container consumption from the metrics
	// API; when false the metrics block is omitted.
	IncludeMetrics bool
	// EventLimit caps the newest events returned; zero uses the service
	// maximum.
	EventLimit int
}

// ReplicaStatus is the workload replica counters when the shape has
// them (spec §5.1 workload block).
type ReplicaStatus struct {
	Desired   int64 `json:"desired"`
	Available int64 `json:"available"`
}

// WorkloadCondition is one status condition of the workload.
type WorkloadCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime,omitempty"`
}

// WorkloadBlock is the workload state: identity, conditions, replicas.
type WorkloadBlock struct {
	Kind       string              `json:"kind"`
	Name       string              `json:"name"`
	Namespace  string              `json:"namespace,omitempty"`
	Replicas   *ReplicaStatus      `json:"replicas,omitempty"`
	Conditions []WorkloadCondition `json:"conditions,omitempty"`
	Phase      string              `json:"phase,omitempty"` // set for Pod workloads
}

// ContainerDetail is one container of a detailed pod: current and
// previous state, restart count, and declared resources (spec §5.1).
type ContainerDetail struct {
	Name         string                `json:"name"`
	Ready        bool                  `json:"ready"`
	RestartCount int32                 `json:"restartCount"`
	State        corev1.ContainerState `json:"state"`
	LastState    corev1.ContainerState `json:"lastState,omitempty"`
	Requests     corev1.ResourceList   `json:"requests,omitempty"`
	Limits       corev1.ResourceList   `json:"limits,omitempty"`
}

// PodDetail is one fully described pod (spec §5.1 pods[]).
type PodDetail struct {
	Name       string            `json:"name"`
	Phase      string            `json:"phase"`
	Node       string            `json:"node,omitempty"`
	StartTime  *time.Time        `json:"startTime,omitempty"`
	Containers []ContainerDetail `json:"containers"`
}

// PodsBlock is the pod budget: every pod counts toward phaseCounts;
// only the first MaxPods are detailed (spec §5.1 orçamento).
type PodsBlock struct {
	Total       int            `json:"total"`
	PhaseCounts map[string]int `json:"phaseCounts"`
	Items       []PodDetail    `json:"items"`
	Truncated   int            `json:"truncated,omitempty"`
}

// WorkloadCoverage declares what the response provably covers: the
// event window and the metrics snapshot (spec §5.1 coverage).
type WorkloadCoverage struct {
	Events  CoverageInfo `json:"events"`
	Metrics CoverageInfo `json:"metrics,omitempty"`
}

// WorkloadResult is the full troubleshooting bundle of one workload.
type WorkloadResult struct {
	Workload      *WorkloadBlock      `json:"workload"`
	Pods          *PodsBlock          `json:"pods"`
	Relationships *RelationshipResult `json:"relationships,omitempty"`
	Events        []NormalizedEvent   `json:"events"`
	Metrics       []MetricsEntry      `json:"metrics,omitempty"`
	Signals       []signals.Signal    `json:"signals"`
	Coverage      WorkloadCoverage    `json:"coverage"`
	Unavailable   []string            `json:"unavailable,omitempty"`
}

// WorkloadService assembles k8s_workload_context: one call, the whole
// troubleshooting context of a workload (spec §13 step 17). Collection
// is parallel and bounded by MaxConcurrent Kubernetes requests per
// invocation; a failed section lands in Unavailable and never drops
// the response (spec §5.1).
type WorkloadService struct {
	Policy        *policy.Policy
	Clients       DynamicClients
	Metrics       metricsReader
	Relationships *RelationshipService
	Events        eventCollector
	Thresholds    signals.Thresholds
	MaxPods       int
	MaxConcurrent int
	// now is injectable for tests.
	now func() time.Time
}

func NewWorkloadService(
	pol *policy.Policy,
	clients DynamicClients,
	metrics metricsReader,
	relationships *RelationshipService,
	events eventCollector,
	thresholds signals.Thresholds,
	maxPods, maxConcurrent int,
) *WorkloadService {
	return &WorkloadService{
		Policy:        pol,
		Clients:       clients,
		Metrics:       metrics,
		Relationships: relationships,
		Events:        events,
		Thresholds:    thresholds,
		MaxPods:       maxPods,
		MaxConcurrent: maxConcurrent,
		now:           time.Now,
	}
}

// Context collects every block of the workload context.
func (s *WorkloadService) Context(ctx context.Context, opts WorkloadOptions) (*WorkloadResult, error) {
	if err := s.Policy.ClusterAllowed(opts.Cluster); err != nil {
		return nil, err
	}
	if !WorkloadKinds[opts.Kind] {
		return nil, fmt.Errorf("%w: kind %q (accepts Pod, Deployment, StatefulSet, DaemonSet, Job, CronJob)", ErrResourceTypeNotFound, opts.Kind)
	}
	if opts.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

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

	out := &WorkloadResult{
		Workload: workloadBlock(workload, opts.Kind, opts.Namespace),
		Events:   []NormalizedEvent{},
		Signals:  []signals.Signal{},
	}
	sem := newSemaphore(s.MaxConcurrent)
	unavailable := &lockedStrings{}

	var (
		wg          sync.WaitGroup
		pods        []corev1.Pod
		rel         *RelationshipResult
		events      *EventsResult
		metricsList []MetricsEntry
	)
	// Wave 1: pods, relationships, events, (optional) metrics.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if opts.Kind == "Pod" {
			var pod corev1.Pod
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(workload.Object, &pod); err != nil {
				unavailable.add(fmt.Sprintf("pods: %v", err))
				return
			}
			pods = []corev1.Pod{pod}
			return
		}
		labelMap := workloadLabels(workload)
		if len(labelMap) == 0 {
			unavailable.add("pods: workload carries no labels to select pods")
			return
		}
		if err := sem.acquire(ctx); err != nil {
			return
		}
		defer sem.release()
		got, err := listPodsByLabels(ctx, clients, opts.Namespace, labelMap)
		if err != nil {
			unavailable.add(fmt.Sprintf("pods: %v", err))
			return
		}
		pods = got
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if !namespaced || s.Relationships == nil {
			return
		}
		if err := sem.acquire(ctx); err != nil {
			return
		}
		defer sem.release()
		res, err := s.Relationships.Resolve(ctx, RelationshipOptions{
			Cluster: opts.Cluster, Namespace: opts.Namespace, Kind: opts.Kind, Name: opts.Name,
		})
		if err != nil {
			unavailable.add(fmt.Sprintf("relationships: %v", err))
			return
		}
		rel = res
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if s.Events == nil {
			return
		}
		if err := sem.acquire(ctx); err != nil {
			return
		}
		defer sem.release()
		res, err := s.Events.Events(ctx, EventsOptions{
			Cluster:   opts.Cluster,
			Namespace: opts.Namespace,
			Limit:     opts.EventLimit,
		})
		if err != nil {
			unavailable.add(fmt.Sprintf("events: %v", err))
			return
		}
		events = res
	}()
	if opts.IncludeMetrics && s.Metrics != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sem.acquire(ctx); err != nil {
				return
			}
			defer sem.release()
			res, err := s.Metrics.Pods(ctx, opts.Cluster, opts.Namespace)
			if err != nil {
				unavailable.add(fmt.Sprintf("metrics: %v", err))
				return
			}
			metricsList = res
		}()
	}
	wg.Wait()

	// Wave 2: per-claim PVC phases and per-node readiness, feeding the
	// signals block from facts already collected.
	if rel != nil {
		unavailable.append(rel.Unavailable)
		for _, claim := range rel.PVC {
			if err := sem.acquire(ctx); err != nil {
				break
			}
			out.Signals = append(out.Signals, checkClaimPending(ctx, clients, opts.Namespace, claim, s.now(), s.Thresholds)...)
			sem.release()
		}
	}
	if len(pods) > 0 {
		out.Signals = append(out.Signals, checkNodesReady(ctx, clients, pods)...)
	}

	// Assemble blocks.
	out.Pods = podsBlock(pods, s.MaxPods)
	out.Relationships = rel
	if events != nil {
		out.Events = events.Items
		out.Coverage.Events = events.Coverage
	} else {
		out.Coverage.Events = CoverageInfo{To: s.now(), Complete: false, Reason: "events unavailable"}
	}
	if opts.IncludeMetrics {
		out.Metrics = metricsList
		if metricsList != nil {
			out.Coverage.Metrics = CoverageInfo{From: s.now(), To: s.now(), Complete: true}
			usage := map[string]map[string]int64{}
			for _, entry := range metricsList {
				if entry.Namespace != opts.Namespace {
					continue
				}
				byContainer := map[string]int64{}
				for _, c := range entry.Containers {
					if q, err := resource.ParseQuantity(c.Usage["memory"]); err == nil {
						byContainer[c.Name] = q.Value()
					}
				}
				usage[entry.Name] = byContainer
			}
			out.Signals = append(out.Signals, memoryPressureSignals(pods, usage, s.Thresholds)...)
		} else {
			out.Coverage.Metrics = CoverageInfo{To: s.now(), Complete: false, Reason: "metrics unavailable"}
		}
	}

	out.Signals = append(out.Signals, signals.CheckWorkload(
		opts.Kind, opts.Namespace, opts.Name, desiredReplicas(workload), availableReplicas(workload))...)
	for i := range pods {
		out.Signals = append(out.Signals, signals.CheckPod(&pods[i], s.now(), s.Thresholds)...)
	}
	if rel != nil {
		for _, svc := range rel.Services {
			out.Signals = append(out.Signals, signals.CheckService(signals.ServiceRef{
				Name:             svc.Name,
				Namespace:        opts.Namespace,
				SelectorNonEmpty: true,
				ReadyEndpoints:   svc.ReadyEndpoints,
			})...)
		}
	}
	sort.Slice(out.Signals, func(a, b int) bool {
		if out.Signals[a].Type != out.Signals[b].Type {
			return out.Signals[a].Type < out.Signals[b].Type
		}
		return out.Signals[a].Source < out.Signals[b].Source
	})
	out.Unavailable = unavailable.snapshot()
	return out, nil
}

// workloadBlock projects the workload identity, conditions, replicas
// and (for Pods) phase.
func workloadBlock(w *unstructured.Unstructured, kind, namespace string) *WorkloadBlock {
	block := &WorkloadBlock{
		Kind:      kind,
		Name:      w.GetName(),
		Namespace: w.GetNamespace(),
	}
	if kind == "Pod" {
		phase, _, _ := unstructured.NestedString(w.Object, "status", "phase")
		block.Phase = phase
	}
	if desired := desiredReplicas(w); desired > 0 {
		block.Replicas = &ReplicaStatus{Desired: desired, Available: availableReplicas(w)}
	}
	conditions, found, _ := unstructured.NestedSlice(w.Object, "status", "conditions")
	if found {
		for _, c := range conditions {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			cond := WorkloadCondition{}
			cond.Type, _, _ = unstructured.NestedString(cm, "type")
			cond.Status, _, _ = unstructured.NestedString(cm, "status")
			cond.Reason, _, _ = unstructured.NestedString(cm, "reason")
			cond.Message, _, _ = unstructured.NestedString(cm, "message")
			if ts, found, _ := unstructured.NestedString(cm, "lastTransitionTime"); found {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					cond.LastTransitionTime = t
				}
			}
			block.Conditions = append(block.Conditions, cond)
		}
	}
	return block
}

// podsBlock builds the pod budget: total and per-phase counts for every
// pod; full detail only for the first maxPods (spec §5.1).
func podsBlock(pods []corev1.Pod, maxPods int) *PodsBlock {
	block := &PodsBlock{
		Total:       len(pods),
		PhaseCounts: map[string]int{},
		Items:       []PodDetail{},
	}
	limit := len(pods)
	if limit > maxPods {
		limit = maxPods
	}
	for i := range pods {
		phase := string(pods[i].Status.Phase)
		if phase == "" {
			phase = "Unknown"
		}
		block.PhaseCounts[phase]++
		if i >= limit {
			continue
		}
		detail := PodDetail{
			Name:  pods[i].Name,
			Phase: phase,
			Node:  pods[i].Spec.NodeName,
		}
		if st := pods[i].Status.StartTime; st != nil {
			t := st.Time
			detail.StartTime = &t
		}
		for _, c := range pods[i].Spec.Containers {
			cd := ContainerDetail{
				Name:     c.Name,
				Requests: c.Resources.Requests,
				Limits:   c.Resources.Limits,
			}
			for _, cs := range pods[i].Status.ContainerStatuses {
				if cs.Name != c.Name {
					continue
				}
				cd.Ready = cs.Ready
				cd.RestartCount = cs.RestartCount
				cd.State = cs.State
				cd.LastState = cs.LastTerminationState
			}
			detail.Containers = append(detail.Containers, cd)
		}
		block.Items = append(block.Items, detail)
	}
	block.Truncated = len(pods) - limit
	if block.Truncated <= 0 {
		block.Truncated = 0
	}
	return block
}

// semaphore bounds the number of in-flight Kubernetes requests.
type semaphore chan struct{}

func newSemaphore(n int) semaphore {
	if n <= 0 {
		n = 1
	}
	return make(semaphore, n)
}

func (s semaphore) acquire(ctx context.Context) error {
	select {
	case s <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s semaphore) release() { <-s }

// lockedStrings collects unavailable reasons from parallel sections.
type lockedStrings struct {
	mu    sync.Mutex
	items []string
}

func (l *lockedStrings) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, s)
}

func (l *lockedStrings) append(items []string) {
	for _, s := range items {
		l.add(s)
	}
}

func (l *lockedStrings) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.items) == 0 {
		return nil
	}
	out := make([]string, len(l.items))
	copy(out, l.items)
	sort.Strings(out)
	return out
}
