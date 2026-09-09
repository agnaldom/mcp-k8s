package kubernetes

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// ErrExecPluginBlocked is returned when a kubeconfig relies on an exec
// credential plugin while allowExecPlugins is false (spec §6.1): exec
// plugins run local processes, so they are off unless explicitly enabled.
var ErrExecPluginBlocked = errors.New("exec credential plugin blocked by policy (allowExecPlugins: false)")

// KubeconfigProvider exposes every context of a kubeconfig as a cluster —
// there is no separate "context" concept for the consumer (spec §4).
type KubeconfigProvider struct {
	// Path overrides the kubeconfig location. Empty means the default
	// loading rules (~/.kube/config plus KUBECONFIG).
	Path string
	// AllowExecPlugins gates exec credential plugins (spec §6.1).
	AllowExecPlugins bool
}

var _ ClusterProvider = (*KubeconfigProvider)(nil)

const kubeconfigSource = "kubeconfig"

func (p *KubeconfigProvider) load() (*clientcmdapi.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if p.Path != "" {
		rules.ExplicitPath = p.Path
	}
	cfg, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return cfg, nil
}

// List returns one cluster per context; the current context is the
// default. At most one cluster carries Default=true (spec §4).
func (p *KubeconfigProvider) List(_ context.Context) ([]ClusterInfo, error) {
	cfg, err := p.load()
	if err != nil {
		return nil, err
	}
	out := make([]ClusterInfo, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		out = append(out, ClusterInfo{
			Name:    name,
			Default: name == cfg.CurrentContext,
			Source:  kubeconfigSource,
		})
	}
	return out, nil
}

// Config resolves the named context into a fresh rest.Config. The config
// is rebuilt from disk on every call — nothing is shared between calls.
func (p *KubeconfigProvider) Config(_ context.Context, name string) (*rest.Config, error) {
	cfg, err := p.load()
	if err != nil {
		return nil, err
	}
	ctx, ok := cfg.Contexts[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrClusterNotFound, name)
	}
	if err := p.checkExecPlugins(cfg, ctx); err != nil {
		return nil, err
	}

	overrides := &clientcmd.ConfigOverrides{CurrentContext: name}
	restCfg, err := clientcmd.NewDefaultClientConfig(*cfg, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("resolve context %q: %w", name, err)
	}
	return restCfg, nil
}

// checkExecPlugins enforces the exec-plugin policy before any credential
// material is resolved: if the context's auth-info uses an exec plugin and
// the policy disallows it, fail deterministically instead of running the
// plugin lazily at first request (spec §6.1).
func (p *KubeconfigProvider) checkExecPlugins(cfg *clientcmdapi.Config, ctx *clientcmdapi.Context) error {
	if p.AllowExecPlugins {
		return nil
	}
	auth, ok := cfg.AuthInfos[ctx.AuthInfo]
	if !ok || auth.Exec == nil {
		return nil
	}
	return fmt.Errorf("%w: auth-info %q uses command %q", ErrExecPluginBlocked, ctx.AuthInfo, auth.Exec.Command)
}
