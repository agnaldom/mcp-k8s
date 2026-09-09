// Package cli implements the mcp-k8s administrative command line
// (spec §12): serve, doctor, config validate, cluster list, cluster test,
// version. Logging goes to stderr as structured JSON via log/slog — stdout
// is reserved for the stdio MCP transport.
package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/version"
)

type globals struct {
	configPath string
	logLevel   string
	logger     *slog.Logger
}

// Execute runs the command tree and returns the process exit code.
func Execute() int {
	g := &globals{}
	root := newRootCmd(g)

	if err := root.Execute(); err != nil {
		// Errors are already rendered by cobra; keep the exit code here.
		return 1
	}
	return 0
}

func newRootCmd(g *globals) *cobra.Command {
	root := &cobra.Command{
		Use:   "mcp-k8s",
		Short: "Read-only MCP server for structured Kubernetes facts",
		Long: "mcp-k8s exposes structured, sanitized, audited Kubernetes facts " +
			"over MCP stdio. It never mutates the cluster.",
		SilenceUsage:  true,
		SilenceErrors: false,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			logger, err := newLogger(g.logLevel, os.Stderr)
			if err != nil {
				return err
			}
			g.logger = logger
			return nil
		},
	}
	root.PersistentFlags().StringVar(&g.configPath, "config", config.DefaultPath(),
		"path to the config file")
	root.PersistentFlags().StringVar(&g.logLevel, "log-level", "info",
		"log level: debug, info, warn, error")

	root.AddCommand(
		newServeCmd(g),
		newDoctorCmd(g),
		newConfigCmd(g),
		newClusterCmd(g),
		newVersionCmd(),
	)
	return root
}

// loadConfig resolves the effective config path, loads it, and logs where
// configuration came from. found=false means the file does not exist and
// built-in defaults are in effect.
func (g *globals) loadConfig() (cfg *config.Config, found bool, err error) {
	path := g.configPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, found, err = config.Load(path)
	if err != nil {
		return nil, false, err
	}
	if found {
		g.logger.Info("config loaded", "path", path)
	} else {
		g.logger.Info("config file not found, using defaults", "path", path)
	}
	return cfg, found, nil
}

// newLogger builds the structured logger. Output is always stderr: on the
// stdio transport stdout carries MCP frames only (spec §10). Secrets,
// kubeconfig contents, tokens, and Authorization headers must never be
// passed as log values — the audit log (spec §10) defines what may be
// recorded.
func newLogger(level string, w io.Writer) (*slog.Logger, error) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "info":
		lv = slog.LevelInfo
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		return nil, fmt.Errorf("invalid --log-level %q: use debug, info, warn or error", level)
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lv})), nil
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build info",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "mcp-k8s %s (commit %s)\n", version.Version, version.Commit)
		},
	}
}
