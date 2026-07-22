package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/buildinfo"
	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/sanitization"
	"github.com/pooya79/AgentSession/internal/tui"
	webui "github.com/pooya79/AgentSession/internal/web"
)

const terminalDiagnosticLimit = 32

type rootOptions struct {
	dataDir   string
	configDir string
	codex     []string
	claude    []string
	opencode  []string
}

// Execute runs the AgentSession command with explicit I/O streams.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer, info buildinfo.Info) error {
	cmd := newRootCommand(info)
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd.ExecuteContext(ctx)
}

func newRootCommand(info buildinfo.Info) *cobra.Command {
	options := &rootOptions{}
	cmd := &cobra.Command{
		Use:           "agentsession",
		Short:         "Explore coding-agent sessions",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       info.String(),
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRuntime(cmd.Context(), *options, func(runtime *app.Runtime) error { return tui.Run(runtime) })
		},
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.PersistentFlags().StringVar(&options.dataDir, "data-dir", "", "application data directory")
	cmd.PersistentFlags().StringVar(&options.configDir, "config-dir", "", "application configuration directory")
	cmd.AddCommand(newImportCommand(options), newWebCommand(options), newVersionCommand(info))
	return cmd
}

func newWebCommand(options *rootOptions) *cobra.Command {
	addr := webui.DefaultAddress
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Start the local web interface",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRuntime(cmd.Context(), *options, func(runtime *app.Runtime) error {
				writeTerminalText(cmd.OutOrStdout(), fmt.Sprintf("AgentSession web listening on http://%s\n", addr))
				return webui.Serve(cmd.Context(), addr, runtime)
			})
		},
	}
	cmd.Flags().StringVar(&addr, "addr", addr, "listen address")
	return cmd
}

func newImportCommand(options *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Discover and import local coding-agent sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRuntime(cmd.Context(), *options, func(runtime *app.Runtime) error {
				result, err := runtime.DiscoverAndImport(cmd.Context())
				writeImportResult(cmd.OutOrStdout(), result)
				return err
			})
		},
	}
	cmd.Flags().StringArrayVar(&options.codex, "codex", nil, "Codex session file or directory (repeatable)")
	cmd.Flags().StringArrayVar(&options.claude, "claude", nil, "Claude session file or directory (repeatable)")
	cmd.Flags().StringArrayVar(&options.opencode, "opencode", nil, "OpenCode database file or directory (repeatable)")
	return cmd
}

func withRuntime(ctx context.Context, options rootOptions, run func(*app.Runtime) error) (err error) {
	runtime, err := app.OpenRuntime(ctx, app.RuntimeConfig{
		DataDir: options.dataDir, ConfigDir: options.configDir, ExplicitPaths: configuredPaths(options),
	})
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, runtime.Shutdown(shutdownCtx))
	}()
	return run(runtime)
}

func configuredPaths(options rootOptions) []discovery.ConfiguredPath {
	paths := make([]discovery.ConfiguredPath, 0, len(options.codex)+len(options.claude)+len(options.opencode))
	for _, value := range options.codex {
		paths = append(paths, discovery.ConfiguredPath{Kind: discovery.SourceCodex, Path: value})
	}
	for _, value := range options.claude {
		paths = append(paths, discovery.ConfiguredPath{Kind: discovery.SourceClaude, Path: value})
	}
	for _, value := range options.opencode {
		paths = append(paths, discovery.ConfiguredPath{Kind: discovery.SourceOpenCode, Path: value})
	}
	return paths
}

func writeImportResult(w io.Writer, result app.BatchImportResult) {
	writeTerminalText(w, fmt.Sprintf("Discovered %d source(s).\n", len(result.Discovery.Sources)))
	for i, diagnostic := range result.Discovery.Diagnostics {
		if i >= terminalDiagnosticLimit {
			break
		}
		writeTerminalText(w, fmt.Sprintf("Discovery %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message))
	}
	if omitted := len(result.Discovery.Diagnostics) - terminalDiagnosticLimit; omitted > 0 {
		writeTerminalText(w, fmt.Sprintf("%d additional discovery diagnostic(s) omitted.\n", omitted))
	}
	importedSessions, completedSources, failedSources, unchangedSessions := 0, 0, 0, 0
	for _, progress := range result.Imports {
		if progress.Failure != nil {
			failedSources++
			writeTerminalText(w, fmt.Sprintf("Source %s failed to import.\n", progress.SourceID))
			continue
		}
		completedSources++
		importedSessions += int(progress.ImportResultsObserved)
		for _, summary := range progress.ImportedSessions {
			status := string(summary.Change)
			if summary.Change == "" {
				status = "imported"
			}
			if summary.Change == "unchanged" {
				unchangedSessions++
			}
			warning := ""
			if summary.ProjectionWarning {
				warning = "; projection warning"
			}
			writeTerminalText(w, fmt.Sprintf("Session %s (source %s): %s, %d record(s), %d batch(es)%s.\n",
				summary.SessionID, summary.SourceID, status, summary.RecordsCommitted, summary.BatchesCommitted, warning))
		}
		if progress.ImportResultsOmitted > 0 {
			writeTerminalText(w, fmt.Sprintf("Source %s: %d session result(s) omitted.\n", progress.SourceID, progress.ImportResultsOmitted))
		}
		for _, diagnostic := range progress.RecentDiagnostics {
			writeTerminalText(w, fmt.Sprintf("Source %s %s %s: %s\n", progress.SourceID, diagnostic.Severity, diagnostic.Code, diagnostic.Message))
		}
		if progress.DiagnosticsOmitted > 0 {
			writeTerminalText(w, fmt.Sprintf("Source %s: %d earlier diagnostic(s) omitted.\n", progress.SourceID, progress.DiagnosticsOmitted))
		}
	}
	if len(result.Discovery.Sources) == 0 {
		writeTerminalText(w, "No session sources were found.\n")
	}
	writeTerminalText(w, fmt.Sprintf("Imported %d session(s) from %d source(s); %d unchanged; %d source failure(s).\n",
		importedSessions, completedSources, unchangedSessions, failedSources))
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
		Use: "version", Short: "Print build information", Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), info.String()) },
	}
}
