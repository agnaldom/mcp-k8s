// Package doctor implements `mcp-k8s doctor` (spec §12): the checks to
// run first when something does not work. It validates the
// configuration, cluster discovery, API connectivity, discovery,
// metrics availability, list and pods/log permissions, and cache
// directory permissions — and reports each as ok, warn, or fail.
package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgo "k8s.io/client-go/kubernetes"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
)

// Status values for a Check.
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
)

// Check is one doctor check result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Result is the full doctor report.
type Result struct {
	Checks []Check `json:"checks"`
}

// Failed reports whether any check failed.
func (r *Result) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

// ClusterReport is the per-cluster probe outcome.
type ClusterReport struct {
	// Version is the Kubernetes server version, when reachable.
	Version string
	// APIGroups is the number of API groups served.
	APIGroups int
	// MetricsAvailable reports the metrics.k8s.io API.
	MetricsAvailable bool
	// CanListPods and CanGetPodLogs come from SelfSubjectAccessReview.
	CanListPods   bool
	CanGetPodLogs bool
}

// ClusterProber probes one cluster's connectivity, discovery, metrics,
// and effective permissions. Production wires it to the client factory;
// tests stub it.
type ClusterProber func(ctx context.Context, name string) (*ClusterReport, error)

// Deps carries everything doctor needs; the CLI assembles it from the
// config, provider, and factory.
type Deps struct {
	Cfg      *config.Config
	Provider kubernetes.ClusterProvider
	CacheDir string
	// Probe inspects one cluster; nil skips per-cluster checks.
	Probe ClusterProber
	// ClusterTimeout bounds each cluster probe.
	ClusterTimeout time.Duration
}

// Run executes every check and returns the report. Per-cluster checks
// run in parallel; a probe failure is a failed check, never a crashed
// doctor.
func Run(ctx context.Context, deps Deps) *Result {
	res := &Result{Checks: []Check{}}
	add := func(name, status, detail string) {
		res.Checks = append(res.Checks, Check{Name: name, Status: status, Detail: detail})
	}

	// 1. Configuration validity.
	if deps.Cfg == nil {
		add("config", StatusFail, "no configuration loaded")
	} else if err := deps.Cfg.Validate(); err != nil {
		add("config", StatusFail, err.Error())
	} else {
		add("config", StatusOK, fmt.Sprintf("valid (readonly=%v)", deps.Cfg.Security.Readonly))
	}

	// 2. Cluster discovery.
	clusters, err := deps.Provider.List(ctx)
	if err != nil {
		add("clusters", StatusFail, err.Error())
	} else if len(clusters) == 0 {
		add("clusters", StatusWarn, "no clusters discovered")
	} else {
		names := make([]string, 0, len(clusters))
		def := ""
		for _, c := range clusters {
			names = append(names, c.Name)
			if c.Default {
				def = c.Name
			}
		}
		detail := fmt.Sprintf("%d found: %v", len(clusters), names)
		if def != "" {
			detail += fmt.Sprintf(" (default: %s)", def)
		}
		add("clusters", StatusOK, detail)
	}

	// 8. Cache directory permissions.
	add(cacheCheck(deps.CacheDir))

	// 3-7. Per-cluster checks, in parallel.
	if deps.Probe != nil && len(clusters) > 0 {
		var wg sync.WaitGroup
		type outcome struct {
			name   string
			report *ClusterReport
			err    error
		}
		outcomes := make(chan outcome, len(clusters))
		for _, c := range clusters {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				probeCtx := ctx
				cancel := func() {}
				if deps.ClusterTimeout > 0 {
					probeCtx, cancel = context.WithTimeout(ctx, deps.ClusterTimeout)
				}
				defer cancel()
				report, err := deps.Probe(probeCtx, name)
				outcomes <- outcome{name: name, report: report, err: err}
			}(c.Name)
		}
		wg.Wait()
		close(outcomes)
		merged := map[string]outcome{}
		order := []string{}
		for o := range outcomes {
			merged[o.name] = o
			order = append(order, o.name)
		}
		sort.Strings(order)
		for _, name := range order {
			o := merged[name]
			if o.err != nil {
				add("cluster "+name, StatusFail, "connectivity: "+o.err.Error())
				continue
			}
			r := o.report
			add("cluster "+name, StatusOK, fmt.Sprintf("kubernetes %s", r.Version))
			if r.APIGroups > 0 {
				add("cluster "+name+" discovery", StatusOK, fmt.Sprintf("%d API groups", r.APIGroups))
			} else {
				add("cluster "+name+" discovery", StatusFail, "no API groups served")
			}
			if r.MetricsAvailable {
				add("cluster "+name+" metrics", StatusOK, "metrics.k8s.io available")
			} else {
				add("cluster "+name+" metrics", StatusWarn, "metrics.k8s.io not available (k8s_signals memory signals and workload metrics will be limited)")
			}
			if r.CanListPods {
				add("cluster "+name+" list permission", StatusOK, "list pods allowed")
			} else {
				add("cluster "+name+" list permission", StatusFail, "list pods denied by RBAC")
			}
			if r.CanGetPodLogs {
				add("cluster "+name+" pods/log permission", StatusOK, "get pods/log allowed")
			} else {
				add("cluster "+name+" pods/log permission", StatusWarn, "get pods/log denied by RBAC (k8s_pod_logs will fail)")
			}
		}
	}
	return res
}

// cacheCheck verifies the discovery cache directory exists and is
// writable; it creates the directory when missing.
func cacheCheck(dir string) (string, string, string) {
	if dir == "" {
		return "cache directory", StatusWarn, "no cache directory configured"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "cache directory", StatusFail, fmt.Sprintf("cannot create %s: %v", dir, err)
	}
	probe := filepath.Join(dir, ".doctor-write-test")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "cache directory", StatusFail, fmt.Sprintf("%s not writable: %v", dir, err)
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return "cache directory", StatusOK, dir
}

// ProbeWith builds a ClusterProber backed by the client factory.
func ProbeWith(provider kubernetes.ClusterProvider, factory *kubernetes.ClientFactory) ClusterProber {
	return func(ctx context.Context, name string) (*ClusterReport, error) {
		cfg, err := provider.Config(ctx, name)
		if err != nil {
			return nil, err
		}
		clients, err := factory.ForCluster(name, cfg)
		if err != nil {
			return nil, err
		}
		return ProbeClients(ctx, clients)
	}
}

// ProbeClients runs the per-cluster checks against a built client
// bundle.
func ProbeClients(ctx context.Context, clients *kubernetes.Clients) (*ClusterReport, error) {
	report := &ClusterReport{}

	version, err := clients.Discovery.ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("server version: %w", err)
	}
	report.Version = version.GitVersion

	groups, err := clients.Discovery.ServerGroups()
	if err != nil {
		return report, nil // connectivity confirmed; discovery detail stays zero
	}
	report.APIGroups = len(groups.Groups) + 1 // + the legacy core group
	for _, g := range groups.Groups {
		if g.Name == "metrics.k8s.io" {
			report.MetricsAvailable = true
		}
	}

	report.CanListPods = canI(ctx, clients.Typed, "list", "pods", "")
	report.CanGetPodLogs = canI(ctx, clients.Typed, "get", "pods", "log")
	return report, nil
}

// canI asks the API server what the current credentials may do
// (SelfSubjectAccessReview, spec §5.3).
func canI(ctx context.Context, typed clientgo.Interface, verb, resource, subresource string) bool {
	review := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Verb:        verb,
				Resource:    resource,
				Subresource: subresource,
			},
		},
	}
	res, err := typed.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false
	}
	return res.Status.Allowed
}
