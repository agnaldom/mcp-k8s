package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/policy"
)

func podWithContainers(name, ns string, containers ...string) *corev1.Pod {
	spec := make([]corev1.Container, 0, len(containers))
	for _, c := range containers {
		spec = append(spec, corev1.Container{Name: c})
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: corev1.PodSpec{Containers: spec}}
}

func logServiceWith(pod *corev1.Pod) *LogService {
	clientset := fake.NewSimpleClientset(pod)
	return &LogService{
		Policy:       policy.New(config.Default().Security),
		Clients:      func(_ context.Context, _ string) (kubernetes.Interface, error) { return clientset, nil },
		MaxTailLines: 5000,
		MaxBytes:     1024,
		Timeout:      logsTimeout,
	}
}

func TestLogsRequiresContainerChoice(t *testing.T) {
	svc := logServiceWith(podWithContainers("api", "payments", "api", "sidecar"))
	_, err := svc.Logs(context.Background(), PodLogsOptions{Cluster: "prod", Namespace: "payments", Pod: "api"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for ambiguous container, got %v", err)
	}
}

func TestLogsUnknownContainer(t *testing.T) {
	svc := logServiceWith(podWithContainers("api", "payments", "api"))
	_, err := svc.Logs(context.Background(), PodLogsOptions{
		Cluster: "prod", Namespace: "payments", Pod: "api", Container: "ghost",
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for unknown container, got %v", err)
	}
}

func TestLogsAutoSelectsSingleContainer(t *testing.T) {
	svc := logServiceWith(podWithContainers("api", "payments", "api"))
	res, err := svc.Logs(context.Background(), PodLogsOptions{Cluster: "prod", Namespace: "payments", Pod: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pod != "api" || res.Truncated {
		t.Errorf("result: %+v", res)
	}
}

func TestLogsPodNotFound(t *testing.T) {
	svc := logServiceWith(podWithContainers("api", "payments", "api"))
	_, err := svc.Logs(context.Background(), PodLogsOptions{Cluster: "prod", Namespace: "payments", Pod: "ghost"})
	if !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("expected ErrResourceNotFound, got %v", err)
	}
}

func TestTruncateFromStart(t *testing.T) {
	content := []byte(strings.Repeat("line\n", 1000))
	kept, truncated, dropped := truncateFromStart(content, 100)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if int64(len(kept)) > 100 {
		t.Errorf("kept %d bytes, cap is 100", len(kept))
	}
	if dropped == 0 {
		t.Error("droppedLines must be reported (spec §3.5)")
	}
	if !strings.HasSuffix(string(kept), "line\n") || strings.Count(string(kept), "\n") != 1 && !strings.HasPrefix(string(kept), "line") {
		// kept content must start on a line boundary
	}
	if !strings.HasPrefix(string(kept), "line\n") && len(kept) > 0 && kept[0] != 'l' {
		t.Error("kept content should start on a line boundary")
	}
}

func TestTruncateFromStartUnderCap(t *testing.T) {
	content := []byte("small")
	kept, truncated, dropped := truncateFromStart(content, 100)
	if truncated || dropped != 0 || string(kept) != "small" {
		t.Errorf("under-cap content must pass through: %q", kept)
	}
}
