package services

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/policy"
)

func TestNamespaceListAppliesPolicy(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cattle-system"}},
	)
	svc := &NamespaceService{
		Policy: policy.New(config.Default().Security),
		Clients: func(_ context.Context, _ string) (kubernetes.Interface, error) {
			return clientset, nil
		},
	}
	names, err := svc.List(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"payments": true, "default": true}
	if len(names) != len(want) {
		t.Fatalf("got %v, want only %v", names, want)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("%s should have been filtered by policy", n)
		}
	}
}

func TestNamespaceListDeniesClusterOutsideAllowList(t *testing.T) {
	security := config.Default().Security
	security.Clusters.Allow = []string{"prod"}
	svc := &NamespaceService{
		Policy: policy.New(security),
		Clients: func(_ context.Context, _ string) (kubernetes.Interface, error) {
			t.Error("clients must not be resolved when policy denies the cluster")
			return nil, nil
		},
	}
	_, err := svc.List(context.Background(), "development")
	if !errors.Is(err, policy.ErrDenied) {
		t.Fatalf("expected ErrDenied before any cluster resolution, got %v", err)
	}
}
