package signals

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agnaldom/mcp-k8s/internal/config"
)

var testNow = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

func defaultThresholds() Thresholds {
	return ThresholdsFrom(config.Signals{})
}

func find(t *testing.T, signals []Signal, typ string) Signal {
	t.Helper()
	for _, s := range signals {
		if s.Type == typ {
			return s
		}
	}
	t.Fatalf("signal %s not found in %+v", typ, signals)
	return Signal{}
}

func mustNotHave(t *testing.T, signals []Signal, typ string) {
	t.Helper()
	for _, s := range signals {
		if s.Type == typ {
			t.Fatalf("signal %s fired unexpectedly: %+v", typ, s)
		}
	}
}

func TestPodCrashLoopAndImagePull(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-x1", Namespace: "payments"}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "api",
		Ready:        false,
		RestartCount: 17,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "CrashLoopBackOff",
		}},
	}}
	got := CheckPod(pod, testNow, defaultThresholds())
	find(t, got, ContainerCrashLoop)
	find(t, got, HighRestartCount)
	mustNotHave(t, got, ContainerImagePullError)
	mustNotHave(t, got, ContainerNotReady) // no Ready condition: duration unknown
}

func TestPodOOMKilled(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-x1", Namespace: "payments"}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "api",
		Ready: true,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason:   "OOMKilled",
			ExitCode: 137,
		}},
	}}
	got := CheckPod(pod, testNow, defaultThresholds())
	s := find(t, got, ContainerOOMKilled)
	if s.Severity != SeverityCritical {
		t.Errorf("severity: %s", s.Severity)
	}
	if s.Observed["exitCode"] != int32(137) {
		t.Errorf("observed: %+v", s.Observed)
	}
}

func TestPodNotReadyWindow(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-x1", Namespace: "payments"}}
	pod.Status.Conditions = []corev1.PodCondition{{
		Type:               corev1.PodReady,
		Status:             corev1.ConditionFalse,
		LastTransitionTime: metav1.NewTime(testNow.Add(-6 * time.Minute)),
	}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "api", Ready: false}}

	got := CheckPod(pod, testNow, defaultThresholds())
	s := find(t, got, ContainerNotReady)
	if s.Rule.Expression != "ready == false for >= 5m0s" {
		t.Errorf("rule must carry the effective threshold, got %q", s.Rule.Expression)
	}
	if !s.Rule.Configurable {
		t.Error("container_not_ready must be configurable")
	}

	// Below the window: not a fact yet.
	pod.Status.Conditions[0].LastTransitionTime = metav1.NewTime(testNow.Add(-30 * time.Second))
	mustNotHave(t, CheckPod(pod, testNow, defaultThresholds()), ContainerNotReady)
}

func TestPodUnschedulableAndEvicted(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-x2", Namespace: "payments"}}
	pod.Status.Reason = "Evicted"
	pod.Status.Conditions = []corev1.PodCondition{{
		Type:    corev1.PodScheduled,
		Status:  corev1.ConditionFalse,
		Message: "0/3 nodes available",
	}}
	got := CheckPod(pod, testNow, defaultThresholds())
	find(t, got, PodUnschedulable)
	find(t, got, PodEvicted)
}

func TestHighRestartCountConfigOverride(t *testing.T) {
	th := ThresholdsFrom(config.Signals{
		HighRestartCount: &config.RestartRule{Threshold: 3, Window: config.Duration{Duration: 10 * time.Minute}},
	})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-x1", Namespace: "payments"}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "api", Ready: true, RestartCount: 4}}
	got := CheckPod(pod, testNow, th)
	s := find(t, got, HighRestartCount)
	if s.Rule.Expression != "restartCount >= 3" || s.Rule.Window != "10m0s" {
		t.Errorf("effective rule: %+v", s.Rule)
	}

	// Below the custom threshold: silent.
	pod.Status.ContainerStatuses[0].RestartCount = 2
	mustNotHave(t, CheckPod(pod, testNow, th), HighRestartCount)
}

func TestWorkloadUnavailable(t *testing.T) {
	got := CheckWorkload("Deployment", "payments", "api", 3, 1)
	s := find(t, got, WorkloadUnavailable)
	if s.Observed["available"] != int64(1) || s.Observed["desired"] != int64(3) {
		t.Errorf("observed: %+v", s.Observed)
	}
	if got := CheckWorkload("Deployment", "payments", "api", 3, 3); len(got) != 0 {
		t.Errorf("healthy workload fired: %+v", got)
	}
}

func TestServiceWithoutEndpoints(t *testing.T) {
	got := CheckService(ServiceRef{Name: "api", Namespace: "payments", SelectorNonEmpty: true, ReadyEndpoints: 0})
	find(t, got, ServiceWithoutEndpoints)
	if got := CheckService(ServiceRef{Name: "api", SelectorNonEmpty: true, ReadyEndpoints: 2}); len(got) != 0 {
		t.Errorf("service with endpoints fired: %+v", got)
	}
	if got := CheckService(ServiceRef{Name: "manual", SelectorNonEmpty: false, ReadyEndpoints: 0}); len(got) != 0 {
		t.Errorf("selector-less service fired: %+v", got)
	}
}

func TestPVCPending(t *testing.T) {
	th := defaultThresholds()
	pendingSince := testNow.Add(-3 * time.Minute)
	got := CheckPVC("payments", "data", corev1.ClaimPending, pendingSince, testNow, th)
	s := find(t, got, PVCPending)
	if s.Rule.Expression != "phase == Pending for >= 2m0s" {
		t.Errorf("rule: %+v", s.Rule)
	}
	// Recent pending: below the window.
	if got := CheckPVC("payments", "data", corev1.ClaimPending, testNow.Add(-30*time.Second), testNow, th); len(got) != 0 {
		t.Errorf("recent pending fired: %+v", got)
	}
	// Bound: silent.
	if got := CheckPVC("payments", "data", corev1.ClaimBound, pendingSince, testNow, th); len(got) != 0 {
		t.Errorf("bound pvc fired: %+v", got)
	}
}

func TestNodeNotReady(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady"}}
	find(t, CheckNode(node), NodeNotReady)

	node.Status.Conditions[0].Status = corev1.ConditionTrue
	if got := CheckNode(node); len(got) != 0 {
		t.Errorf("ready node fired: %+v", got)
	}

	if got := CheckNode(&corev1.Node{}); len(got) != 1 {
		t.Errorf("node without Ready condition must fire, got %+v", got)
	}
}

func TestMemoryNearLimit(t *testing.T) {
	th := defaultThresholds() // 0.90
	got := CheckMemory("pod/payments/api-x1", "api", 950, 1000, th)
	s := find(t, got, MemoryNearLimit)
	if s.Rule.Expression != "usage / limit >= 0.90" {
		t.Errorf("rule: %+v", s.Rule)
	}
	// Below ratio: silent.
	if got := CheckMemory("pod/payments/api-x1", "api", 800, 1000, th); len(got) != 0 {
		t.Errorf("below ratio fired: %+v", got)
	}
	// No limit: no denominator, silent.
	if got := CheckMemory("pod/payments/api-x1", "api", 950, 0, th); len(got) != 0 {
		t.Errorf("unlimited container fired: %+v", got)
	}
}

func TestSignalCarriesSource(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-x1", Namespace: "payments"}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "api",
		Ready: true,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
	}}
	s := find(t, CheckPod(pod, testNow, defaultThresholds()), ContainerImagePullError)
	wantSource := "pod/payments/api-x1 .status.containerStatuses[api].state.waiting.reason"
	if s.Source != wantSource {
		t.Errorf("source: want %q, got %q", wantSource, s.Source)
	}
}
