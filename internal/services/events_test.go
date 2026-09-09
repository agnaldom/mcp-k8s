package services

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/policy"
)

func event(name, ns string, last time.Time, reason string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: ns},
		Type:           "Warning",
		Reason:         reason,
		Message:        "back-off restarting failed container",
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-0", Namespace: ns},
		Count:          3,
		FirstTimestamp: metav1.NewTime(last.Add(-10 * time.Minute)),
		LastTimestamp:  metav1.NewTime(last),
		Source:         corev1.EventSource{Component: "kubelet"},
	}
}

func eventService(now time.Time, objs ...runtime.Object) *EventService {
	clientset := fake.NewSimpleClientset(objs...)
	return &EventService{
		Policy:   policy.New(config.Default().Security),
		Clients:  func(_ context.Context, _ string) (kubernetes.Interface, error) { return clientset, nil },
		MaxItems: 500,
		now:      func() time.Time { return now },
	}
}

func TestEventsCoverageComplete(t *testing.T) {
	now := time.Now()
	since := time.Hour
	// An event older than the window start proves the stream extends
	// before the window — that is what makes coverage complete.
	svc := eventService(now,
		event("e0", "payments", now.Add(-70*time.Minute), "Scheduled"),
		event("e1", "payments", now.Add(-50*time.Minute), "BackOff"),
		event("e2", "payments", now.Add(-30*time.Minute), "Unhealthy"),
	)
	res, err := svc.Events(context.Background(), EventsOptions{Cluster: "prod", Namespace: "payments", Since: &since})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("expected 2 events in window, got %d", len(res.Items))
	}
	if !res.Coverage.Complete {
		t.Errorf("window fully covered should be complete: %+v", res.Coverage)
	}
	if res.Coverage.From.After(res.Coverage.To) {
		t.Error("coverage window is inverted")
	}
	e := res.Items[0]
	if e.Reason == "" || e.InvolvedKind != "Pod" || e.Source != "kubelet" || e.Count != 3 {
		t.Errorf("normalization: %+v", e)
	}
}

func TestEventsCoverageIncompleteOldestNewerThanWindow(t *testing.T) {
	now := time.Now()
	since := 2 * time.Hour
	svc := eventService(now,
		event("e1", "payments", now.Add(-30*time.Minute), "BackOff"),
	)
	res, err := svc.Events(context.Background(), EventsOptions{Cluster: "prod", Since: &since})
	if err != nil {
		t.Fatal(err)
	}
	if res.Coverage.Complete {
		t.Error("oldest event newer than window must be complete=false (spec §3.3)")
	}
	if res.Coverage.Reason == "" {
		t.Error("reason is required when complete=false")
	}
}

func TestEventsEmptyIsUnknown(t *testing.T) {
	svc := eventService(time.Now())
	res, err := svc.Events(context.Background(), EventsOptions{Cluster: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || res.Coverage.Complete {
		t.Errorf("no events must be complete=false: %+v", res.Coverage)
	}
}

func TestEventsCapsToNewest(t *testing.T) {
	now := time.Now()
	var objs []runtime.Object
	for i := 0; i < 10; i++ {
		objs = append(objs, event(string(rune('a'+i)), "payments", now.Add(-time.Duration(10-i)*time.Minute), "BackOff"))
	}
	svc := eventService(now, objs...)
	svc.MaxItems = 5
	res, err := svc.Events(context.Background(), EventsOptions{Cluster: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 5 {
		t.Fatalf("expected 5 newest, got %d", len(res.Items))
	}
	if !res.Coverage.From.After(now.Add(-60 * time.Minute)) {
		t.Errorf("coverage.from must reflect the oldest KEPT event: %v", res.Coverage.From)
	}
}
