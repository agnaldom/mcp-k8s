package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	clientgo "k8s.io/client-go/kubernetes"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
	"github.com/agnaldom/mcp-k8s/internal/policy"
	"github.com/agnaldom/mcp-k8s/internal/services"
	"github.com/agnaldom/mcp-k8s/internal/tools"
	"github.com/agnaldom/mcp-k8s/internal/version"
)

// newServeCmd runs the MCP server. v0.1 speaks stdio only — there is no
// HTTP transport (spec §2). Cancellation of the MCP session propagates
// through ctx down to client-go (spec §8).
func newServeCmd(g *globals) *cobra.Command {
	var transport string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server over stdio",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if transport != "stdio" {
				return fmt.Errorf("unsupported --transport %q: v0.1 speaks stdio only (spec §2)", transport)
			}
			cfg, _, err := g.loadConfig()
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			provider, factory := buildKubernetesWiring(cfg)
			clusterSvc := &services.ClusterService{Provider: provider, Factory: factory}
			namespaceSvc := &services.NamespaceService{
				Policy:  policy.New(cfg.Security),
				Clients: typedClients(provider, factory),
			}

			mcpServer := server.NewMCPServer(
				"mcp-k8s",
				version.Version,
				server.WithToolCapabilities(true),
				server.WithLogging(),
			)
			tools.RegisterClusterTools(mcpServer, clusterSvc)
			tools.RegisterNamespaceTools(mcpServer, namespaceSvc)
			apiResourcesSvc := services.NewAPIResourceService(policy.New(cfg.Security), authzClients(provider, factory))
			resourceSvc := &services.ResourceService{
				Policy:         policy.New(cfg.Security),
				Clients:        dynamicClients(provider, factory),
				DefaultLimit:   cfg.Limits.List.DefaultLimit,
				MaxLimit:       cfg.Limits.List.MaxLimit,
				MaxObjectBytes: cfg.Limits.Response.MaxBytes,
			}
			tools.RegisterResourceListTool(mcpServer, resourceSvc)
			tools.RegisterResourceGetTool(mcpServer, resourceSvc)
			tools.RegisterAPIResourcesTool(mcpServer, apiResourcesSvc)
			tools.RegisterLogsTool(mcpServer, services.NewLogService(policy.New(cfg.Security), typedClients(provider, factory), int64(cfg.Limits.Logs.MaxTailLines), cfg.Limits.Logs.MaxBytes))
			tools.RegisterEventsTool(mcpServer, services.NewEventService(policy.New(cfg.Security), typedClients(provider, factory), cfg.Limits.Events.MaxItems))
			tools.RegisterMetricsTools(mcpServer, services.NewMetricsService(policy.New(cfg.Security), dynamicOnly(provider, factory)))
			stdio := server.NewStdioServer(mcpServer)
			g.logger.Info("mcp-k8s serving", "transport", "stdio", "version", version.Version)

			// StdioServer owns stdout for MCP frames; all diagnostics go
			// to stderr via g.logger.
			if err := stdio.Listen(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
				return fmt.Errorf("serve: %w", err)
			}
			g.logger.Info("mcp-k8s stopped")
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "stdio", "transport: stdio (the only option in v0.1)")
	return cmd
}

// buildKubernetesWiring assembles the kubernetes-layer objects for the
// MCP server: which provider exposes clusters, and the client factory
// with rate limits and the persistent discovery cache (spec §8/§9).
func buildKubernetesWiring(cfg *config.Config) (kubernetes.ClusterProvider, *kubernetes.ClientFactory) {
	var provider kubernetes.ClusterProvider
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		provider = &kubernetes.InClusterProvider{}
	} else {
		provider = &kubernetes.KubeconfigProvider{
			AllowExecPlugins: cfg.Security.Kubeconfig.AllowExecPlugins,
		}
	}
	factory := kubernetes.NewCachedClientFactory(
		cfg.Kubernetes.QPS, cfg.Kubernetes.Burst, kubernetes.DefaultCacheDir(),
	)
	return provider, factory
}

// typedClients wires a services.TypedClients to the provider+factory
// chain: resolve the cluster's rest.Config, build the per-cluster bundle,
// hand over the typed clientset. Never cached across calls.
func typedClients(provider kubernetes.ClusterProvider, factory *kubernetes.ClientFactory) services.TypedClients {
	return func(ctx context.Context, cluster string) (clientgo.Interface, error) {
		cfg, err := provider.Config(ctx, cluster)
		if err != nil {
			return nil, err
		}
		clients, err := factory.ForCluster(cluster, cfg)
		if err != nil {
			return nil, err
		}
		return clients.Typed, nil
	}
}

// dynamicClients wires a services.DynamicClients to provider+factory:
// resolve the cluster, build the bundle, hand over the dynamic client,
// discovery, and RESTMapper.
func dynamicClients(provider kubernetes.ClusterProvider, factory *kubernetes.ClientFactory) services.DynamicClients {
	return func(ctx context.Context, cluster string) (dynamic.Interface, discovery.DiscoveryInterface, meta.RESTMapper, error) {
		cfg, err := provider.Config(ctx, cluster)
		if err != nil {
			return nil, nil, nil, err
		}
		clients, err := factory.ForCluster(cluster, cfg)
		if err != nil {
			return nil, nil, nil, err
		}
		return clients.Dynamic, clients.Discovery, clients.Mapper, nil
	}
}

// authzClients wires services.AuthzClients to provider+factory.
func authzClients(provider kubernetes.ClusterProvider, factory *kubernetes.ClientFactory) services.AuthzClients {
	return func(ctx context.Context, cluster string) (clientgo.Interface, discovery.DiscoveryInterface, error) {
		cfg, err := provider.Config(ctx, cluster)
		if err != nil {
			return nil, nil, err
		}
		clients, err := factory.ForCluster(cluster, cfg)
		if err != nil {
			return nil, nil, err
		}
		return clients.Typed, clients.Discovery, nil
	}
}

// dynamicOnly wires services.MetricsClients to provider+factory.
func dynamicOnly(provider kubernetes.ClusterProvider, factory *kubernetes.ClientFactory) services.MetricsClients {
	return func(ctx context.Context, cluster string) (dynamic.Interface, error) {
		cfg, err := provider.Config(ctx, cluster)
		if err != nil {
			return nil, err
		}
		clients, err := factory.ForCluster(cluster, cfg)
		if err != nil {
			return nil, err
		}
		return clients.Dynamic, nil
	}
}
