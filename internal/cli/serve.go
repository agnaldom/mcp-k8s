package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

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
			if _, _, err := g.loadConfig(); err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			mcpServer := server.NewMCPServer(
				"mcp-k8s",
				version.Version,
				// v0.1 ships no tools until step 07; capabilities are
				// re-enabled as the catalog lands.
				server.WithToolCapabilities(false),
				server.WithLogging(),
			)
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
