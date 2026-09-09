package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"

	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
	"github.com/agnaldom/mcp-k8s/internal/policy"
)

// ErrResourceTypeNotFound marks a Kind the cluster does not serve; the
// tools layer maps it to RESOURCE_TYPE_NOT_FOUND (spec §3.6).
var ErrResourceTypeNotFound = errors.New("resource type not found")

// Views (spec §3.4).
const (
	ViewSummary = "summary"
	ViewFull    = "full"
)

// DynamicClients resolves a dynamic client, discovery client, and
// RESTMapper for the named cluster. Wired in serve.go to
// provider+factory; tests wire fakes.
type DynamicClients func(ctx context.Context, cluster string) (dynamic.Interface, discovery.DiscoveryInterface, meta.RESTMapper, error)

// ResourceListOptions carries the validated tool arguments.
type ResourceListOptions struct {
	Cluster string
	Kind    string
	// Namespace empty means all namespaces.
	Namespace string
	View      string
	Limit     int
	// ContinueToken resumes a previous page (spec §3.5).
	ContinueToken string
}

// ListItem is one resource in the response.
type ListItem struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   map[string]any `json:"metadata"`
	// Status present only in summary view when the object carries it.
	Status map[string]any `json:"status,omitempty"`
	// Spec present only in full view.
	Spec map[string]any `json:"spec,omitempty"`
	// Object is the complete sanitized object (full view only).
	Object map[string]any `json:"object,omitempty"`
}

// ResourceListResult is the data block of k8s_resource_list.
type ResourceListResult struct {
	Items []ListItem `json:"items"`
	// ContinueToken is non-empty when the list was truncated by the
	// server and more pages exist (spec §3.5).
	ContinueToken string `json:"-"`
	HasMore       bool   `json:"-"`
}

// ResourceService lists any Kind through the dynamic client (spec §13
// step 09).
type ResourceService struct {
	Policy         *policy.Policy
	Clients        DynamicClients
	DefaultLimit   int
	MaxLimit       int
	MaxObjectBytes int64
}

// List resolves the Kind, lists it with pagination, sanitizes every item,
// and projects the requested view.
func (s *ResourceService) List(ctx context.Context, opts ResourceListOptions) (*ResourceListResult, error) {
	if err := s.Policy.ClusterAllowed(opts.Cluster); err != nil {
		return nil, err
	}
	if opts.Kind == "" {
		return nil, fmt.Errorf("kind is required")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = s.DefaultLimit
	}
	if limit > s.MaxLimit {
		limit = s.MaxLimit
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
	gvr := mapping.Resource

	var list *unstructured.UnstructuredList
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		list, err = clients.Resource(gvr).Namespace(opts.Namespace).List(ctx, metav1.ListOptions{
			Limit:    int64(limit),
			Continue: opts.ContinueToken,
		})
	} else {
		list, err = clients.Resource(gvr).List(ctx, metav1.ListOptions{
			Limit:    int64(limit),
			Continue: opts.ContinueToken,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", gvr.String(), err)
	}

	out := &ResourceListResult{
		Items:         make([]ListItem, 0, len(list.Items)),
		ContinueToken: list.GetContinue(),
		HasMore:       list.GetContinue() != "",
	}
	for i := range list.Items {
		item := list.Items[i].DeepCopy()
		kubernetes.SanitizeObject(item)
		projected, err := project(item, opts.View, s.MaxObjectBytes)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, *projected)
	}
	return out, nil
}

// project applies the view and enforces the per-object size cap
// (spec §3.4/§3.5).
func project(item *unstructured.Unstructured, view string, maxBytes int64) (*ListItem, error) {
	if view == ViewFull {
		raw, err := json.Marshal(item.Object)
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) > maxBytes {
			return nil, &kubernetes.SizeError{
				Kind: item.GetKind(), Name: item.GetName(), Size: int64(len(raw)), Cap: maxBytes,
			}
		}
		return &ListItem{
			APIVersion: item.GetAPIVersion(),
			Kind:       item.GetKind(),
			Metadata:   metadataBlock(item),
			Object:     item.Object,
		}, nil
	}

	// summary: identity + state (spec §3.4).
	out := &ListItem{
		APIVersion: item.GetAPIVersion(),
		Kind:       item.GetKind(),
		Metadata:   metadataBlock(item),
	}
	if status, found, _ := unstructured.NestedMap(item.Object, "status"); found {
		out.Status = status
	}
	return out, nil
}

// metadataBlock keeps the identity fields that matter during an incident.
func metadataBlock(item *unstructured.Unstructured) map[string]any {
	md := map[string]any{
		"name":              item.GetName(),
		"namespace":         item.GetNamespace(),
		"uid":               string(item.GetUID()),
		"resourceVersion":   item.GetResourceVersion(),
		"creationTimestamp": item.GetCreationTimestamp().Time,
		"generation":        item.GetGeneration(),
	}
	if labels := item.GetLabels(); len(labels) > 0 {
		md["labels"] = labels
	}
	if annotations := item.GetAnnotations(); len(annotations) > 0 {
		md["annotations"] = annotations
	}
	return md
}
