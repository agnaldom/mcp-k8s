// Package config loads and validates the mcp-k8s YAML configuration
// (spec §7 policy, §8 limits, §5.2 signal overrides). It is a leaf layer:
// it imports nothing from internal/kubernetes, internal/services, or
// internal/tools.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration so YAML values like "30s" or "5m" decode
// directly (time.Duration does not implement yaml unmarshalling).
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("expected duration string: %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

func (d Duration) String() string { return d.Duration.String() }

// Config is the root of the configuration file.
type Config struct {
	Security   Security         `yaml:"security"`
	Limits     Limits           `yaml:"limits"`
	Kubernetes KubernetesClient `yaml:"kubernetes"`
	Signals    Signals          `yaml:"signals"`
}

// Security is the local policy layer (spec §7). It is evaluated before
// cluster resolution and before Kubernetes RBAC, and it is never skipped
// because "RBAC covers it".
type Security struct {
	Readonly   bool           `yaml:"readonly"`
	Clusters   AllowList      `yaml:"clusters"`
	Namespaces NamespaceList  `yaml:"namespaces"`
	Resources  DenyList       `yaml:"resources"`
	Kubeconfig KubeconfigRule `yaml:"kubeconfig"`
}

type AllowList struct {
	Allow []string `yaml:"allow"` // empty = all allowed
}

type DenyList struct {
	Deny []string `yaml:"deny"`
}

type NamespaceList struct {
	Allow []string `yaml:"allow"` // empty = all
	Deny  []string `yaml:"deny"`
}

type KubeconfigRule struct {
	// AllowExecPlugins gates exec credential plugins (spec §6.1): they run
	// local processes, so they are disabled by default.
	AllowExecPlugins bool `yaml:"allowExecPlugins"`
}

// Limits caps every response shape (spec §8).
type Limits struct {
	RequestTimeout Duration       `yaml:"requestTimeout"`
	Response       ResponseLimits `yaml:"response"`
	List           ListLimits     `yaml:"list"`
	Logs           LogsLimits     `yaml:"logs"`
	Events         EventsLimits   `yaml:"events"`
	Workload       WorkloadLimits `yaml:"workload"`
}

type ResponseLimits struct {
	MaxBytes int64 `yaml:"maxBytes"`
}

type ListLimits struct {
	DefaultLimit int `yaml:"defaultLimit"`
	MaxLimit     int `yaml:"maxLimit"`
}

type LogsLimits struct {
	DefaultTailLines int   `yaml:"defaultTailLines"`
	MaxTailLines     int   `yaml:"maxTailLines"`
	MaxBytes         int64 `yaml:"maxBytes"`
}

type EventsLimits struct {
	MaxItems int `yaml:"maxItems"`
}

type WorkloadLimits struct {
	MaxPods                  int `yaml:"maxPods"`
	MaxConcurrentK8sRequests int `yaml:"maxConcurrentK8sRequests"`
}

// KubernetesClient tunes the client-go rate limiter (spec §8).
type KubernetesClient struct {
	QPS   float32 `yaml:"qps"`
	Burst int     `yaml:"burst"`
}

// Signals holds overrides for the configurable signal thresholds
// (spec §5.2). Nil means "use the built-in default".
type Signals struct {
	ContainerNotReady *NotReadyRule    `yaml:"container_not_ready"`
	HighRestartCount  *RestartRule     `yaml:"high_restart_count"`
	PVCPending        *PendingRule     `yaml:"pvc_pending"`
	MemoryNearLimit   *MemoryRatioRule `yaml:"memory_near_limit"`
}

type NotReadyRule struct {
	NotReadyFor Duration `yaml:"notReadyFor"`
}

type RestartRule struct {
	Threshold int      `yaml:"threshold"`
	Window    Duration `yaml:"window"`
}

type PendingRule struct {
	PendingFor Duration `yaml:"pendingFor"`
}

type MemoryRatioRule struct {
	Ratio float64 `yaml:"ratio"`
}

// DefaultPath returns the configuration path used when --config is not
// given: ~/.config/mcp-k8s/config.yaml.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "mcp-k8s", "config.yaml")
}

// Default returns the built-in configuration: the values from spec §7/§8.
func Default() *Config {
	return &Config{
		Security: Security{
			Readonly:   true,
			Namespaces: NamespaceList{Deny: []string{"kube-system", "cattle-system"}},
			Resources:  DenyList{Deny: []string{"Secret", "TokenRequest"}},
		},
		Limits: Limits{
			RequestTimeout: Duration{30 * time.Second},
			Response:       ResponseLimits{MaxBytes: 4 * 1024 * 1024},
			List:           ListLimits{DefaultLimit: 100, MaxLimit: 500},
			Logs:           LogsLimits{DefaultTailLines: 500, MaxTailLines: 5000, MaxBytes: 1024 * 1024},
			Events:         EventsLimits{MaxItems: 500},
			Workload:       WorkloadLimits{MaxPods: 20, MaxConcurrentK8sRequests: 8},
		},
		Kubernetes: KubernetesClient{QPS: 20, Burst: 40},
	}
}

// Load reads the YAML file at path, applies the built-in defaults for
// anything the file leaves unset, and validates the result. A missing file
// yields the defaults (found reports whether the file existed).
func Load(path string) (cfg *Config, found bool, err error) {
	cfg = Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, false, nil
		}
		return nil, false, fmt.Errorf("read config: %w", err)
	}
	found = true

	// Strict mode: a typo'd key must fail loudly, not be ignored.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, true, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, true, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, true, nil
}

// Validate enforces the invariants the server relies on.
func (c *Config) Validate() error {
	// There is no write mode in this binary (spec §2): a config asking for
	// writes can only be a mistake, and it must be rejected, not honoured.
	if !c.Security.Readonly {
		return fmt.Errorf("security.readonly must be true: this binary has no write mode (spec §2)")
	}
	if c.Limits.Response.MaxBytes <= 0 {
		return fmt.Errorf("limits.response.maxBytes must be > 0")
	}
	if c.Limits.List.DefaultLimit <= 0 || c.Limits.List.MaxLimit <= 0 {
		return fmt.Errorf("limits.list limits must be > 0")
	}
	if c.Limits.List.DefaultLimit > c.Limits.List.MaxLimit {
		return fmt.Errorf("limits.list.defaultLimit (%d) exceeds maxLimit (%d)", c.Limits.List.DefaultLimit, c.Limits.List.MaxLimit)
	}
	if c.Limits.Logs.DefaultTailLines <= 0 || c.Limits.Logs.MaxTailLines <= 0 || c.Limits.Logs.MaxBytes <= 0 {
		return fmt.Errorf("limits.logs limits must be > 0")
	}
	if c.Limits.Logs.DefaultTailLines > c.Limits.Logs.MaxTailLines {
		return fmt.Errorf("limits.logs.defaultTailLines (%d) exceeds maxTailLines (%d)", c.Limits.Logs.DefaultTailLines, c.Limits.Logs.MaxTailLines)
	}
	if c.Limits.Events.MaxItems <= 0 {
		return fmt.Errorf("limits.events.maxItems must be > 0")
	}
	if c.Limits.Workload.MaxPods <= 0 || c.Limits.Workload.MaxConcurrentK8sRequests <= 0 {
		return fmt.Errorf("limits.workload limits must be > 0")
	}
	if c.Limits.RequestTimeout.Duration <= 0 {
		return fmt.Errorf("limits.requestTimeout must be > 0")
	}
	if c.Kubernetes.QPS <= 0 || c.Kubernetes.Burst <= 0 {
		return fmt.Errorf("kubernetes.qps and kubernetes.burst must be > 0")
	}
	if c.Kubernetes.Burst < int(c.Kubernetes.QPS) {
		return fmt.Errorf("kubernetes.burst (%d) must be >= qps (%v)", c.Kubernetes.Burst, c.Kubernetes.QPS)
	}
	if s := c.Signals.MemoryNearLimit; s != nil && (s.Ratio <= 0 || s.Ratio > 1) {
		return fmt.Errorf("signals.memory_near_limit.ratio must be in (0, 1]")
	}
	return nil
}
