package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/pooya79/AgentSession/internal/buildinfo"
	"github.com/pooya79/AgentSession/internal/sanitization"
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
			writeTerminalText(cmd.OutOrStdout(), fmt.Sprintf("AgentSession web listening on http://%s\n", addr))
			return webui.Serve(cmd.Context(), addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", addr, "listen address")
	return cmd
}

// WriteError writes a process-level terminal diagnostic through the mandatory
// sanitization boundary.
func WriteError(w io.Writer, err error) {
	writeTerminalText(w, fmt.Sprintf("error: %v\n", err))
}

func writeTerminalText(w io.Writer, text string) {
	_, _ = io.WriteString(w, sanitization.Terminal(text))
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
