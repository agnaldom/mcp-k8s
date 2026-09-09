//go:build integration

package integration

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	clientgo "k8s.io/client-go/kubernetes"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/policy"
	"github.com/agnaldom/mcp-k8s/internal/services"
	"github.com/agnaldom/mcp-k8s/internal/signals"
)

// Wiring from the real kind cluster into the service layer — the same
// shape serve.go uses, minus the provider.

func (e *env) dynamicClients() services.DynamicClients {
	return func(_ context.Context, _ string) (dynamic.Interface, discovery.DiscoveryInterface, meta.RESTMapper, error) {
		return e.dynamic, e.discover, e.mapper, nil
	}
}

func (e *env) metricsClients() services.MetricsClients {
	return func(_ context.Context, _ string) (dynamic.Interface, error) {
		return e.dynamic, nil
	}
}

func newSignalService(e *env) *services.SignalService {
	return services.NewSignalService(
		policy.New(config.Default().Security),
		e.dynamicClients(),
		e.metricsClients(),
		services.NewRelationshipService(policy.New(config.Default().Security), e.dynamicClients()),
		signals.ThresholdsFrom(config.Signals{}),
		20,
	)
}

func newWorkloadService(e *env, includeMetrics bool) *services.WorkloadService {
	return services.NewWorkloadService(
		policy.New(config.Default().Security),
		e.dynamicClients(),
		services.NewMetricsService(policy.New(config.Default().Security), e.metricsClients()),
		services.NewRelationshipService(policy.New(config.Default().Security), e.dynamicClients()),
		services.NewEventService(policy.New(config.Default().Security), func(_ context.Context, _ string) (clientgo.Interface, error) {
			return e.typed, nil
		}, 500),
		signals.ThresholdsFrom(config.Signals{}),
		20,
		8,
	)
}

func signalOptions(cluster, ns, kind, name string) services.SignalOptions {
	return services.SignalOptions{Cluster: cluster, Namespace: ns, Kind: kind, Name: name}
}

func workloadOptions(cluster, ns, kind, name string, includeMetrics bool) services.WorkloadOptions {
	return services.WorkloadOptions{
		Cluster: cluster, Namespace: ns, Kind: kind, Name: name, IncludeMetrics: includeMetrics,
	}
}

type workloadResultAlias = services.WorkloadResult
