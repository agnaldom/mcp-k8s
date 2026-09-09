package services

import (
	"context"
	"errors"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"

	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
	"github.com/agnaldom/mcp-k8s/internal/policy"
)

// Well-known resources the resolver queries directly; no discovery
// round-trip needed for these.
var (
	gvrServices = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
	// EndpointSlice is discovery.k8s.io/v1 since Kubernetes 1.21; v0.1
	// targets 1.21+ (spec §2).
	gvrEndpointSlices = schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	gvrHPAs           = schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}
	gvrPDBs           = schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}
)

// maxOwnerDepth bounds the ownerReference walk; real chains are 2-3 hops,
// anything deeper is a cycle or corruption.
const maxOwnerDepth = 8

// OwnerStep is one hop of the ownerReference chain, outermost first.
type OwnerStep struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// ServiceRef is a Service that selects the workload, with its current
// ready endpoint count (spec §5.1 relationships).
type ServiceRef struct {
	Name           string `json:"name"`
	ReadyEndpoints int    `json:"readyEndpoints"`
}

// RelationshipResult is the relationships block of k8s_workload_context
// (spec §5.1): owner chain, Services, EndpointSlices, HPA, PDB, PVC.
type RelationshipResult struct {
	Owners   []OwnerStep  `json:"owners"`
	Services []ServiceRef `json:"services"`
	HPA      []string     `json:"hpa,omitempty"`
	PDB      []string     `json:"pdb,omitempty"`
	PVC      []string     `json:"pvc,omitempty"`
	// Unavailable lists relationship sections that could not be read
	// (e.g. RBAC denial); partial failure never fails the whole
	// resolution (spec §5.1).
	Unavailable []string `json:"unavailable,omitempty"`
}

// RelationshipOptions carries the workload to resolve around.
type RelationshipOptions struct {
	Cluster   string
	Namespace string
	Kind      string
	Name      string
}

// RelationshipService resolves what a workload is connected to: who owns
// it, what it is exposed through, what protects and scales it (spec §13
// step 15). Used by k8s_workload_context (#17) and the
// service_without_endpoints signal.
type RelationshipService struct {
	Policy  *policy.Policy
	Clients DynamicClients
}

func NewRelationshipService(pol *policy.Policy, clients DynamicClients) *RelationshipService {
	return &RelationshipService{Policy: pol, Clients: clients}
}

// Resolve walks every relationship kind. Lookups that fail are recorded in
// Unavailable and never abort the walk: during an incident a missing RBAC
// permission on PodDisruptionBudget must not hide the Services.
func (s *RelationshipService) Resolve(ctx context.Context, opts RelationshipOptions) (*RelationshipResult, error) {
	if err := s.Policy.ClusterAllowed(opts.Cluster); err != nil {
		return nil, err
	}
	if opts.Kind == "" || opts.Name == "" {
		return nil, fmt.Errorf("kind and name are required")
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

	out := &RelationshipResult{}
	out.Owners = s.ownerChain(ctx, clients, disc, mapper, workload, opts.Namespace)
	if !namespaced {
		// Services, EndpointSlices, HPA, PDB, PVC only exist around
		// namespaced workloads.
		return out, nil
	}

	labels := workloadLabels(workload)
	out.Services = s.services(ctx, clients, opts.Namespace, labels, out)
	out.HPA = s.hpas(ctx, clients, opts.Namespace, opts.Kind, opts.Name, out)
	out.PDB = s.pdbs(ctx, clients, opts.Namespace, labels, out)
	out.PVC = pvcNames(workload)
	return out, nil
}

// ownerChain follows ownerReferences from the workload up to the root,
// outermost owner first. A hop that cannot be read (deleted owner, missing
// RBAC) ends the chain; the workload itself is never part of it.
func (s *RelationshipService) ownerChain(
	ctx context.Context,
	clients dynamic.Interface,
	disc discovery.DiscoveryInterface,
	mapper meta.RESTMapper,
	workload *unstructured.Unstructured,
	namespace string,
) []OwnerStep {
	var chain []OwnerStep
	visited := map[string]bool{}
	current := workload
	for depth := 0; depth < maxOwnerDepth; depth++ {
		refs := current.GetOwnerReferences()
		if len(refs) == 0 {
			break
		}
		ref := controllerRef(refs)
		if ref == nil {
			ref = &refs[0]
		}
		key := ref.APIVersion + "/" + ref.Kind + "/" + namespace + "/" + ref.Name
		if visited[key] {
			break // cycle
		}
		visited[key] = true
		chain = append(chain, OwnerStep{Kind: ref.Kind, Name: ref.Name})

		mapping, err := kubernetes.ResolveKind(disc, mapper, ref.Kind)
		if err != nil {
			break // owner kind not served (e.g. CRD removed): chain ends here
		}
		owner, err := clients.Resource(mapping.Resource).Namespace(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			break
		}
		current = owner
	}
	return chain
}

// controllerRef picks the controlling ownerReference when one exists.
func controllerRef(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

// workloadLabels returns the labels the workload's pods carry: the pod
// template labels, falling back to selector matchLabels, and finally to
// the object's own labels (a Pod is selected directly, it has no
// template).
func workloadLabels(w *unstructured.Unstructured) map[string]string {
	if labels, found, _ := unstructured.NestedStringMap(w.Object, "spec", "template", "metadata", "labels"); found && len(labels) > 0 {
		return labels
	}
	if labels, found, _ := unstructured.NestedStringMap(w.Object, "spec", "selector", "matchLabels"); found && len(labels) > 0 {
		return labels
	}
	return w.GetLabels()
}

// selectorMatches reports whether want is a subset of have: a Service
// (or PDB) selects exactly the pods carrying every label in want.
func selectorMatches(want, have map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// services lists Services whose selector matches the workload labels, with
// ready endpoint counts summed from their EndpointSlices.
func (s *RelationshipService) services(ctx context.Context, clients dynamic.Interface, ns string, labels map[string]string, res *RelationshipResult) []ServiceRef {
	list, err := clients.Resource(gvrServices).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unavailable = append(res.Unavailable, fmt.Sprintf("services: %v", err))
		return nil
	}
	var out []ServiceRef
	endpointErrs := map[string]bool{}
	for i := range list.Items {
		svc := &list.Items[i]
		selector, found, _ := unstructured.NestedStringMap(svc.Object, "spec", "selector")
		if !found || len(selector) == 0 || !selectorMatches(selector, labels) {
			continue
		}
		ready, err := s.readyEndpoints(ctx, clients, ns, svc.GetName())
		if err != nil && !endpointErrs[err.Error()] {
			endpointErrs[err.Error()] = true
			res.Unavailable = append(res.Unavailable, fmt.Sprintf("endpointslices: %v", err))
		}
		out = append(out, ServiceRef{Name: svc.GetName(), ReadyEndpoints: ready})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// readyEndpoints sums endpoints with conditions.ready != false across all
// EndpointSlices carrying the kubernetes.io/service-name label.
func (s *RelationshipService) readyEndpoints(ctx context.Context, clients dynamic.Interface, ns, service string) (int, error) {
	slices, err := clients.Resource(gvrEndpointSlices).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "kubernetes.io/service-name=" + service,
	})
	if err != nil {
		return 0, err
	}
	ready := 0
	for i := range slices.Items {
		endpoints, found, _ := unstructured.NestedSlice(slices.Items[i].Object, "endpoints")
		if !found {
			continue
		}
		for _, e := range endpoints {
			ep, ok := e.(map[string]any)
			if !ok {
				continue
			}
			r, found, _ := unstructured.NestedBool(ep, "conditions", "ready")
			if !found || r {
				ready++
			}
		}
	}
	return ready, nil
}

// hpas lists HPAs in the namespace targeting this workload by
// scaleTargetRef.
func (s *RelationshipService) hpas(ctx context.Context, clients dynamic.Interface, ns, kind, name string, res *RelationshipResult) []string {
	list, err := clients.Resource(gvrHPAs).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unavailable = append(res.Unavailable, fmt.Sprintf("hpa: %v", err))
		return nil
	}
	var out []string
	for i := range list.Items {
		hpa := &list.Items[i]
		k, _, _ := unstructured.NestedString(hpa.Object, "spec", "scaleTargetRef", "kind")
		n, _, _ := unstructured.NestedString(hpa.Object, "spec", "scaleTargetRef", "name")
		if k == kind && n == name {
			out = append(out, hpa.GetName())
		}
	}
	sort.Strings(out)
	return out
}

// pdbs lists PDBs whose selector matches the workload labels. An empty
// selector matches every pod in the namespace (it is a subset of any
// label set).
func (s *RelationshipService) pdbs(ctx context.Context, clients dynamic.Interface, ns string, labels map[string]string, res *RelationshipResult) []string {
	list, err := clients.Resource(gvrPDBs).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unavailable = append(res.Unavailable, fmt.Sprintf("pdb: %v", err))
		return nil
	}
	var out []string
	for i := range list.Items {
		pdb := &list.Items[i]
		selector, found, _ := unstructured.NestedStringMap(pdb.Object, "spec", "selector", "matchLabels")
		if found && selectorMatches(selector, labels) {
			out = append(out, pdb.GetName())
		}
	}
	sort.Strings(out)
	return out
}

// pvcNames collects claims referenced by the workload: StatefulSet volume
// claim templates plus persistentVolumeClaim volumes in the pod spec.
func pvcNames(w *unstructured.Unstructured) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	templates, found, _ := unstructured.NestedSlice(w.Object, "spec", "volumeClaimTemplates")
	if found {
		for _, t := range templates {
			tpl, ok := t.(map[string]any)
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(tpl, "metadata", "name")
			add(name)
		}
	}
	volumes, found, _ := unstructured.NestedSlice(w.Object, "spec", "template", "spec", "volumes")
	if !found {
		volumes, _, _ = unstructured.NestedSlice(w.Object, "spec", "volumes")
	}
	for _, v := range volumes {
		vol, ok := v.(map[string]any)
		if !ok {
			continue
		}
		name, found, _ := unstructured.NestedString(vol, "persistentVolumeClaim", "claimName")
		if found {
			add(name)
		}
	}
	sort.Strings(out)
	return out
}
