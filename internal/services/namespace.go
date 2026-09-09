package services

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/agnaldom/mcp-k8s/internal/policy"
)

// TypedClients resolves a typed clientset for the named cluster. Wired
// in serve.go to provider+factory; tests wire it to a fake clientset.
type TypedClients func(ctx context.Context, cluster string) (kubernetes.Interface, error)

// NamespaceService lists namespaces after local policy (spec §7, §13
// step 08). Policy is evaluated before cluster resolution.
type NamespaceService struct {
	Policy  *policy.Policy
	Clients TypedClients
}

// List returns the namespace names visible after the allow/deny policy.
func (s *NamespaceService) List(ctx context.Context, cluster string) ([]string, error) {
	if err := s.Policy.ClusterAllowed(cluster); err != nil {
		return nil, err
	}
	clients, err := s.Clients(ctx, cluster)
	if err != nil {
		return nil, err
	}
	list, err := clients.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		names = append(names, ns.Name)
	}
	return s.Policy.FilterNamespaces(names), nil
}
