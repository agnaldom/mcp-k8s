package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/agnaldom/mcp-k8s/internal/doctor"
	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
)

// errConfigNotFound signals that config validate was pointed at a file
// that does not exist — a validation failure, not a defaults situation.
var errConfigNotFound = errors.New("config file not found (a missing file is not valid; create it or pass --config)")

// newDoctorCmd implements `mcp-k8s doctor` (spec §12): the checks to
// run first when something does not work. Exit code is non-zero when
// any check fails.
func newDoctorCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check config, connectivity, discovery, metrics, and permissions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := g.loadConfig()
			if err != nil {
				return err
			}
			provider, factory := buildKubernetesWiring(cfg)
			res := doctor.Run(cmd.Context(), doctor.Deps{
				Cfg:            cfg,
				Provider:       provider,
				CacheDir:       kubernetes.DefaultCacheDir(),
				Probe:          doctor.ProbeWith(provider, factory),
				ClusterTimeout: cfg.Limits.RequestTimeout.Duration,
			})
			out := cmd.OutOrStdout()
			for _, c := range res.Checks {
				fmt.Fprintf(out, "[%s] %s", strings.ToUpper(c.Status), c.Name)
				if c.Detail != "" {
					fmt.Fprintf(out, ": %s", c.Detail)
				}
				fmt.Fprintln(out)
			}
			if res.Failed() {
				return errDoctorFailed
			}
			return nil
		},
	}
}

// errDoctorFailed signals a non-zero exit for failing doctor checks;
// the report itself is already printed.
var errDoctorFailed = errors.New("doctor: one or more checks failed")

func newClusterCmd(g *globals) *cobra.Command {
	cluster := &cobra.Command{
		Use:   "cluster",
		Short: "Manage cluster provider information",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List available clusters/contexts",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if _, _, err := g.loadConfig(); err != nil {
				return err
			}
			return errors.New("cluster list: not implemented yet (spec §13 steps 04/07)")
		},
	}
	test := &cobra.Command{
		Use:   "test <name>",
		Short: "Test connectivity to a cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			if _, _, err := g.loadConfig(); err != nil {
				return err
			}
			return errors.New("cluster test: not implemented yet (spec §13 steps 04/07)")
		},
	}
	cluster.AddCommand(list, test)
	return cluster
}

func newConfigCmd(g *globals) *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration commands",
	}
	validate := &cobra.Command{
		Use:   "validate",
		Short: "Validate the config file without touching a cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, found, err := g.loadConfig()
			out := cmd.OutOrStdout()
			if err != nil {
				return err
			}
			if !found {
				// validate is explicit: the user asked about a file that
				// does not exist, which is a validation failure, not a
				// defaults situation.
				return errConfigNotFound
			}
			fmt.Fprintf(out, "config OK: %s\n", g.configPath)
			fmt.Fprintf(out, "  clusters allowed: %v (empty = all)\n", cfg.Security.Clusters.Allow)
			fmt.Fprintf(out, "  namespaces: allow=%v deny=%v\n", cfg.Security.Namespaces.Allow, cfg.Security.Namespaces.Deny)
			fmt.Fprintf(out, "  resources denied: %v\n", cfg.Security.Resources.Deny)
			fmt.Fprintf(out, "  allowExecPlugins: %v\n", cfg.Security.Kubeconfig.AllowExecPlugins)
			fmt.Fprintf(out, "  response.maxBytes: %d, list: %d/%d, logs: %d/%d/%d bytes\n",
				cfg.Limits.Response.MaxBytes,
				cfg.Limits.List.DefaultLimit, cfg.Limits.List.MaxLimit,
				cfg.Limits.Logs.DefaultTailLines, cfg.Limits.Logs.MaxTailLines, cfg.Limits.Logs.MaxBytes)
			return nil
		},
	}
	configCmd.AddCommand(validate)
	return configCmd
}
