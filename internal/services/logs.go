package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
	"github.com/agnaldom/mcp-k8s/internal/policy"
)

// logsTimeout is the cascading timeout for log reads (spec §8).
const logsTimeout = 20 * time.Second

// PodLogsOptions carries the validated k8s_pod_logs arguments (spec §5.4).
type PodLogsOptions struct {
	Cluster       string
	Namespace     string
	Pod           string
	Container     string
	Previous      bool
	AllContainers bool
	// SinceSeconds narrows the window; nil means everything the tail covers.
	SinceSeconds *int64
	TailLines    int64
	Timestamps   bool
}

// PodLogsResult is the data block of k8s_pod_logs.
type PodLogsResult struct {
	Pod          string `json:"pod"`
	Container    string `json:"container,omitempty"`
	Logs         string `json:"logs"`
	Truncated    bool   `json:"truncated"`
	DroppedLines int    `json:"droppedLines"`
}

// LogService reads pod logs (spec §13 step 12).
type LogService struct {
	Policy       *policy.Policy
	Clients      TypedClients
	MaxTailLines int64
	MaxBytes     int64
	// Timeout overridable in tests.
	Timeout time.Duration
}

func NewLogService(pol *policy.Policy, clients TypedClients, maxTail int64, maxBytes int64) *LogService {
	return &LogService{Policy: pol, Clients: clients, MaxTailLines: maxTail, MaxBytes: maxBytes, Timeout: logsTimeout}
}

// Logs reads logs for one pod. Container selection is deterministic:
// one container → automatic; several → container or allContainers is
// required, never an arbitrary choice (spec §5.4).
func (s *LogService) Logs(ctx context.Context, opts PodLogsOptions) (*PodLogsResult, error) {
	if err := s.Policy.ClusterAllowed(opts.Cluster); err != nil {
		return nil, err
	}
	clients, err := s.Clients(ctx, opts.Cluster)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()

	pod, err := clients.CoreV1().Pods(opts.Namespace).Get(ctx, opts.Pod, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: pod %s/%s", ErrResourceNotFound, opts.Namespace, opts.Pod)
		}
		return nil, fmt.Errorf("get pod %s/%s: %w", opts.Namespace, opts.Pod, err)
	}

	containers, err := selectContainers(pod, opts.Container, opts.AllContainers)
	if err != nil {
		return nil, err
	}

	tail := opts.TailLines
	if tail <= 0 {
		tail = 500
	}
	if tail > s.MaxTailLines {
		tail = s.MaxTailLines
	}

	var out bytes.Buffer
	for _, c := range containers {
		raw, err := clients.CoreV1().Pods(opts.Namespace).GetLogs(opts.Pod, &corev1.PodLogOptions{
			Container:    c,
			Previous:     opts.Previous,
			Timestamps:   opts.Timestamps,
			TailLines:    &tail,
			SinceSeconds: opts.SinceSeconds,
		}).DoRaw(ctx)
		if err != nil {
			if apierrors.IsBadRequest(err) || apierrors.IsNotFound(err) {
				// kubelet has no logs for this container (common after
				// restarts): a normal cluster state, not a server failure.
				continue
			}
			return nil, fmt.Errorf("logs %s/%s[%s]: %w", opts.Namespace, opts.Pod, c, err)
		}
		if len(containers) > 1 {
			fmt.Fprintf(&out, "==> %s <==\n", c)
		}
		out.Write(kubernetes.RedactLogContent(raw))
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			out.WriteByte('\n')
		}
	}

	content, truncated, dropped := truncateFromStart(out.Bytes(), s.MaxBytes)
	return &PodLogsResult{
		Pod:          opts.Pod,
		Container:    opts.Container,
		Logs:         string(content),
		Truncated:    truncated,
		DroppedLines: dropped,
	}, nil
}

// selectContainers enforces the container-selection contract (spec §5.4).
func selectContainers(pod *corev1.Pod, container string, all bool) ([]string, error) {
	names := make([]string, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	if container != "" {
		for _, n := range names {
			if n == container {
				return []string{container}, nil
			}
		}
		return nil, fmt.Errorf("%w: pod %s has no container %q (has: %v)", ErrInvalidArgument, pod.Name, container, names)
	}
	if all {
		return names, nil
	}
	if len(names) == 1 {
		return names, nil
	}
	return nil, fmt.Errorf("%w: pod %s has multiple containers %v: pass container or allContainers (spec §5.4)",
		ErrInvalidArgument, pod.Name, names)
}

// truncateFromStart enforces the log byte cap by cutting the BEGINNING
// (spec §3.5/§5.4) and counting how many whole lines were discarded.
func truncateFromStart(content []byte, maxBytes int64) (kept []byte, truncated bool, droppedLines int) {
	if int64(len(content)) <= maxBytes {
		return content, false, 0
	}
	cut := content[int64(len(content))-maxBytes:]
	// Drop the partial first line so the output starts on a line boundary.
	if idx := bytes.IndexByte(cut, '\n'); idx >= 0 && idx < len(cut)-1 {
		droppedLines = 1 + bytes.Count(content[:int64(len(content))-maxBytes+int64(idx)+1], []byte{'\n'})
		cut = cut[idx+1:]
	} else {
		droppedLines = bytes.Count(content[:int64(len(content))-maxBytes], []byte{'\n'})
	}
	return cut, true, droppedLines
}

// ErrInvalidArgument marks a caller-side argument problem.
var ErrInvalidArgument = errors.New("invalid argument")
