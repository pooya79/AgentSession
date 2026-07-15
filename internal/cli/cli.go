package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/pooya79/AgentSession/internal/buildinfo"
	"github.com/pooya79/AgentSession/internal/tui"
	webui "github.com/pooya79/AgentSession/internal/web"
)

// Execute runs the AgentSession command with explicit I/O streams.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer, info buildinfo.Info) error {
	cmd := newRootCommand(info)
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd.ExecuteContext(ctx)
}

func newRootCommand(info buildinfo.Info) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "agentsession",
		Short:         "Explore coding-agent sessions",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       info.String(),
		RunE: func(*cobra.Command, []string) error {
			return tui.Run()
		},
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.CompletionOptions.DisableDefaultCmd = true

	cmd.AddCommand(newWebCommand(), newVersionCommand(info))
	return cmd
}

func newWebCommand() *cobra.Command {
	addr := webui.DefaultAddress
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Start the local web interface",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Printf("AgentSession web listening on http://%s\n", addr)
			return webui.Serve(cmd.Context(), addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", addr, "listen address")
	return cmd
}

func newVersionCommand(info buildinfo.Info) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), info.String())
		},
	}
}
