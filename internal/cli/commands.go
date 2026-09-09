package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// errConfigNotFound signals that config validate was pointed at a file
// that does not exist — a validation failure, not a defaults situation.
var errConfigNotFound = errors.New("config file not found (a missing file is not valid; create it or pass --config)")

// newDoctorCmd lands in step 19; the command shape exists from step 02 so
// the CLI contract is complete early.
func newDoctorCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check config, connectivity, discovery, metrics, and permissions",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if _, _, err := g.loadConfig(); err != nil {
				return err
			}
			return errors.New("doctor: not implemented yet (spec §13 step 19)")
		},
	}
}

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
