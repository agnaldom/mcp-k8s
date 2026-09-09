// Package signals turns already-collected facts into deterministic
// signals (spec §5.2). A signal is never a bare judgment: it always
// carries the rule that fired, the effective threshold (including
// config overrides), the observed value, and the exact field the
// value was read from. The consumer can disagree with the threshold
// because the threshold is in the response.
//
// The package is pure: it makes no Kubernetes calls and imports only
// internal/config, so services and tools can depend on it freely.
package signals

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/agnaldom/mcp-k8s/internal/config"
)

// Built-in defaults for the configurable thresholds (spec §5.2).
const (
	DefaultNotReadyFor     = 5 * time.Minute
	DefaultRestartCount    = 10
	DefaultRestartWindow   = time.Hour
	DefaultPVCPendingFor   = 2 * time.Minute
	DefaultMemoryNearRatio = 0.90
)

// Signal is one fired signal.
type Signal struct {
	Type     string         `json:"type"`
	Severity string         `json:"severity"`
	Rule     Rule           `json:"rule"`
	Observed map[string]any `json:"observed"`
	Source   string         `json:"source"`
}

// Rule declares what fired. The expression always contains the
// effective value: defaults, or the config override when one is set.
type Rule struct {
	Expression   string `json:"expression"`
	Window       string `json:"window,omitempty"`
	Configurable bool   `json:"configurable"`
}

const (
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// v0.1 catalog types (spec §5.2).
const (
	ContainerOOMKilled      = "container_oom_killed"
	ContainerCrashLoop      = "container_crash_loop"
	ContainerImagePullError = "container_image_pull_error"
	ContainerNotReady       = "container_not_ready"
	HighRestartCount        = "high_restart_count"
	PodUnschedulable        = "pod_unschedulable"
	PodEvicted              = "pod_evicted"
	WorkloadUnavailable     = "workload_unavailable"
	ServiceWithoutEndpoints = "service_without_endpoints"
	PVCPending              = "pvc_pending"
	NodeNotReady            = "node_not_ready"
	MemoryNearLimit         = "memory_near_limit"
)

// Thresholds are the effective signal thresholds: built-in defaults
// merged with the config overrides.
type Thresholds struct {
	NotReadyFor     time.Duration
	RestartCount    int
	RestartWindow   time.Duration
	PVCPendingFor   time.Duration
	MemoryNearRatio float64
}

// ThresholdsFrom resolves the effective thresholds from the config
// overrides; nil overrides keep the built-in default.
func ThresholdsFrom(cfg config.Signals) Thresholds {
	t := Thresholds{
		NotReadyFor:     DefaultNotReadyFor,
		RestartCount:    DefaultRestartCount,
		RestartWindow:   DefaultRestartWindow,
		PVCPendingFor:   DefaultPVCPendingFor,
		MemoryNearRatio: DefaultMemoryNearRatio,
	}
	if cfg.ContainerNotReady != nil {
		t.NotReadyFor = cfg.ContainerNotReady.NotReadyFor.Duration
	}
	if cfg.HighRestartCount != nil {
		t.RestartCount = cfg.HighRestartCount.Threshold
		t.RestartWindow = cfg.HighRestartCount.Window.Duration
	}
	if cfg.PVCPending != nil {
		t.PVCPendingFor = cfg.PVCPending.PendingFor.Duration
	}
	if cfg.MemoryNearLimit != nil {
		t.MemoryNearRatio = cfg.MemoryNearLimit.Ratio
	}
	return t
}

// CheckPod evaluates every pod-level signal against one pod. now is
// injectable so tests are deterministic.
func CheckPod(pod *corev1.Pod, now time.Time, t Thresholds) []Signal {
	ref := fmt.Sprintf("pod/%s/%s", pod.Namespace, pod.Name)
	var out []Signal

	if pod.Status.Reason == "Evicted" {
		out = append(out, Signal{
			Type:     PodEvicted,
			Severity: SeverityWarning,
			Rule:     Rule{Expression: `status.reason == "Evicted"`, Configurable: false},
			Observed: map[string]any{"reason": pod.Status.Reason},
			Source:   ref + " .status.reason",
		})
	}

	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			out = append(out, Signal{
				Type:     PodUnschedulable,
				Severity: SeverityCritical,
				Rule:     Rule{Expression: `condition PodScheduled == False`, Configurable: false},
				Observed: map[string]any{"message": c.Message},
				Source:   ref + " .status.conditions[PodScheduled]",
			})
		}
	}

	for _, cs := range pod.Status.ContainerStatuses {
		src := fmt.Sprintf("%s .status.containerStatuses[%s]", ref, cs.Name)
		observedContainer := map[string]any{"container": cs.Name}

		if terminated := cs.LastTerminationState.Terminated; terminated != nil && terminated.Reason == "OOMKilled" {
			out = append(out, Signal{
				Type:     ContainerOOMKilled,
				Severity: SeverityCritical,
				Rule:     Rule{Expression: `lastState.terminated.reason == "OOMKilled"`, Configurable: false},
				Observed: merge(observedContainer, map[string]any{"reason": terminated.Reason, "exitCode": terminated.ExitCode}),
				Source:   src + ".lastState.terminated.reason",
			})
		}
		if waiting := cs.State.Waiting; waiting != nil {
			switch waiting.Reason {
			case "CrashLoopBackOff":
				out = append(out, Signal{
					Type:     ContainerCrashLoop,
					Severity: SeverityCritical,
					Rule:     Rule{Expression: `state.waiting.reason == "CrashLoopBackOff"`, Configurable: false},
					Observed: merge(observedContainer, map[string]any{"reason": waiting.Reason, "restartCount": cs.RestartCount}),
					Source:   src + ".state.waiting.reason",
				})
			case "ImagePullBackOff", "ErrImagePull":
				out = append(out, Signal{
					Type:     ContainerImagePullError,
					Severity: SeverityCritical,
					Rule:     Rule{Expression: `state.waiting.reason in {"ImagePullBackOff", "ErrImagePull"}`, Configurable: false},
					Observed: merge(observedContainer, map[string]any{"reason": waiting.Reason, "message": waiting.Message}),
					Source:   src + ".state.waiting.reason",
				})
			}
		}

		if !cs.Ready && notReadyLongEnough(pod, cs, now, t.NotReadyFor) {
			out = append(out, Signal{
				Type:     ContainerNotReady,
				Severity: SeverityWarning,
				Rule: Rule{
					Expression:   fmt.Sprintf("ready == false for >= %s", t.NotReadyFor),
					Configurable: true,
				},
				Observed: merge(observedContainer, map[string]any{"ready": cs.Ready}),
				Source:   src + ".ready",
			})
		}

		if cs.RestartCount >= int32(t.RestartCount) {
			out = append(out, Signal{
				Type:     HighRestartCount,
				Severity: SeverityWarning,
				Rule: Rule{
					Expression:   fmt.Sprintf("restartCount >= %d", t.RestartCount),
					Window:       t.RestartWindow.String(),
					Configurable: true,
				},
				Observed: merge(observedContainer, map[string]any{"restartCount": cs.RestartCount}),
				Source:   src + ".restartCount",
			})
		}
	}
	return out
}

// notReadyLongEnough decides whether a not-ready container has been so
// for at least threshold. The pod Ready condition's lastTransitionTime
// is the timestamp; without it the duration is unknown and the signal
// must not fire (absence of data is not a fact).
func notReadyLongEnough(pod *corev1.Pod, cs corev1.ContainerStatus, now time.Time, threshold time.Duration) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status != corev1.ConditionTrue && !c.LastTransitionTime.IsZero() {
			return now.Sub(c.LastTransitionTime.Time) >= threshold
		}
	}
	return false
}

// CheckWorkload fires workload_unavailable when available replicas are
// below the desired count.
func CheckWorkload(kind, namespace, name string, desired, available int64) []Signal {
	if desired <= 0 || available >= desired {
		return nil
	}
	return []Signal{{
		Type:     WorkloadUnavailable,
		Severity: SeverityCritical,
		Rule:     Rule{Expression: "available < desired", Configurable: false},
		Observed: map[string]any{"desired": desired, "available": available},
		Source:   fmt.Sprintf("%s/%s/%s .status (replicas)", strings.ToLower(kind), namespace, name),
	}}
}

// ServiceRef is the slice of service state the signal needs; defined
// here so the signals package stays independent of the services layer.
type ServiceRef struct {
	Name             string
	Namespace        string
	SelectorNonEmpty bool
	ReadyEndpoints   int
}

// CheckService fires service_without_endpoints when a Service with a
// non-empty selector has zero ready endpoints — nothing can receive
// traffic through it.
func CheckService(svc ServiceRef) []Signal {
	if !svc.SelectorNonEmpty || svc.ReadyEndpoints > 0 {
		return nil
	}
	return []Signal{{
		Type:     ServiceWithoutEndpoints,
		Severity: SeverityCritical,
		Rule:     Rule{Expression: "readyEndpoints == 0 with selector != {}", Configurable: false},
		Observed: map[string]any{"readyEndpoints": svc.ReadyEndpoints},
		Source:   fmt.Sprintf("service/%s/%s .status (endpoints)", svc.Namespace, svc.Name),
	}}
}

// CheckPVC fires pvc_pending when a claim has been Pending for at
// least the configured duration (creation time counts as the start).
func CheckPVC(namespace, name string, phase corev1.PersistentVolumeClaimPhase, pendingSince time.Time, now time.Time, t Thresholds) []Signal {
	if phase != corev1.ClaimPending {
		return nil
	}
	if pendingSince.IsZero() || now.Sub(pendingSince) < t.PVCPendingFor {
		return nil
	}
	return []Signal{{
		Type:     PVCPending,
		Severity: SeverityWarning,
		Rule:     Rule{Expression: fmt.Sprintf("phase == Pending for >= %s", t.PVCPendingFor), Configurable: true},
		Observed: map[string]any{"phase": string(phase), "pendingSince": pendingSince},
		Source:   fmt.Sprintf("persistentvolumeclaim/%s/%s .status.phase", namespace, name),
	}}
}

// CheckNode fires node_not_ready when the node's Ready condition is
// not True.
func CheckNode(node *corev1.Node) []Signal {
	for _, c := range node.Status.Conditions {
		if c.Type != corev1.NodeReady {
			continue
		}
		if c.Status == corev1.ConditionTrue {
			return nil
		}
		return []Signal{{
			Type:     NodeNotReady,
			Severity: SeverityCritical,
			Rule:     Rule{Expression: "condition Ready != True", Configurable: false},
			Observed: map[string]any{"status": string(c.Status), "reason": c.Reason},
			Source:   fmt.Sprintf("node/%s .status.conditions[Ready]", node.Name),
		}}
	}
	// No Ready condition at all: the node never reported health.
	return []Signal{{
		Type:     NodeNotReady,
		Severity: SeverityCritical,
		Rule:     Rule{Expression: "condition Ready missing", Configurable: false},
		Observed: map[string]any{"status": "missing"},
		Source:   fmt.Sprintf("node/%s .status.conditions[Ready]", node.Name),
	}}
}

// CheckMemory fires memory_near_limit when a container's usage has
// reached the configured ratio of its limit. A container without a
// limit cannot fire: there is no denominator.
func CheckMemory(podRef, container string, usageBytes, limitBytes int64, t Thresholds) []Signal {
	if limitBytes <= 0 {
		return nil
	}
	ratio := float64(usageBytes) / float64(limitBytes)
	if ratio < t.MemoryNearRatio {
		return nil
	}
	return []Signal{{
		Type:     MemoryNearLimit,
		Severity: SeverityWarning,
		Rule:     Rule{Expression: fmt.Sprintf("usage / limit >= %.2f", t.MemoryNearRatio), Configurable: true},
		Observed: map[string]any{"container": container, "usageBytes": usageBytes, "limitBytes": limitBytes, "ratio": ratio},
		Source:   fmt.Sprintf("%s .containers[%s].usage.memory vs spec.resources.limits.memory", podRef, container),
	}}
}

func merge(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
