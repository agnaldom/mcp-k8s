//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/agnaldom/mcp-k8s/internal/signals"
)

// TestFixtures applies the spec §11 corpus once for the whole suite.
func TestFixtures(t *testing.T) {
	e := loadEnv(t)
	e.applyFixtures(t)
}

func TestHealthyDeployment(t *testing.T) {
	e := loadEnv(t)
	e.applyFixtures(t)
	res := e.workloadContext(t, "Deployment", "healthy", false)
	if res.Workload.Replicas == nil || res.Workload.Replicas.Available != 2 {
		t.Errorf("healthy replicas: %+v", res.Workload.Replicas)
	}
	if res.Pods.Total != 2 {
		t.Errorf("healthy pods: %+v", res.Pods)
	}
	if res.Relationships == nil || len(res.Relationships.Services) != 1 ||
		res.Relationships.Services[0].ReadyEndpoints < 1 {
		t.Errorf("healthy service endpoints: %+v", res.Relationships)
	}
	for _, s := range res.Signals {
		if s.Type == signals.WorkloadUnavailable || s.Type == signals.ServiceWithoutEndpoints {
			t.Errorf("healthy workload fired %s", s.Type)
		}
	}
}

func TestCrashLoopBackOff(t *testing.T) {
	e := loadEnv(t)
	e.applyFixtures(t)
	waitFor(t, "container_crash_loop signal", 4*time.Minute, func() bool {
		return e.evalSignals(t, "Deployment", "crashloop")[signals.ContainerCrashLoop]
	})
}

func TestOOMKilled(t *testing.T) {
	e := loadEnv(t)
	e.applyFixtures(t)
	waitFor(t, "container_oom_killed signal", 4*time.Minute, func() bool {
		return e.evalSignals(t, "Deployment", "oomkilled")[signals.ContainerOOMKilled]
	})
}

func TestImagePullBackOff(t *testing.T) {
	e := loadEnv(t)
	e.applyFixtures(t)
	waitFor(t, "container_image_pull_error signal", 4*time.Minute, func() bool {
		return e.evalSignals(t, "Deployment", "imagepull")[signals.ContainerImagePullError]
	})
}

func TestPendingForLackOfResources(t *testing.T) {
	e := loadEnv(t)
	e.applyFixtures(t)
	waitFor(t, "pod_unschedulable signal", 2*time.Minute, func() bool {
		return e.evalSignals(t, "Pod", "pending-resources")[signals.PodUnschedulable]
	})
}

func TestLivenessFailingRestarts(t *testing.T) {
	e := loadEnv(t)
	e.applyFixtures(t)
	waitFor(t, "liveness pod restart", 4*time.Minute, func() bool {
		res := e.workloadContext(t, "Deployment", "liveness-failing", false)
		for _, p := range res.Pods.Items {
			for _, c := range p.Containers {
				if c.RestartCount > 0 {
					return true
				}
			}
		}
		return false
	})
}

func TestServiceWithoutEndpoints(t *testing.T) {
	e := loadEnv(t)
	e.applyFixtures(t)
	waitFor(t, "service_without_endpoints signal", 2*time.Minute, func() bool {
		return e.evalSignals(t, "Pod", "pending-resources")[signals.ServiceWithoutEndpoints]
	})
}

func TestPVCPending(t *testing.T) {
	e := loadEnv(t)
	e.applyFixtures(t)
	waitFor(t, "pvc_pending signal", 4*time.Minute, func() bool {
		return e.evalSignals(t, "Pod", "pvc-consumer")[signals.PVCPending]
	})
}

func TestFailedJob(t *testing.T) {
	e := loadEnv(t)
	e.applyFixtures(t)
	waitFor(t, "failed job pod", 3*time.Minute, func() bool {
		res := e.workloadContext(t, "Job", "failed-job", false)
		return res.Pods.Total >= 1
	})
}

func TestMultiContainerPod(t *testing.T) {
	e := loadEnv(t)
	e.applyFixtures(t)
	waitFor(t, "multi-container pod running", 2*time.Minute, func() bool {
		res := e.workloadContext(t, "Pod", "multi-container", false)
		return len(res.Pods.Items) == 1 && len(res.Pods.Items[0].Containers) == 2
	})
}
