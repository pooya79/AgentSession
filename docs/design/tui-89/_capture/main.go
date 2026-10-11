// This review-only entry point runs the production TUI and application services
// with discovery isolated to invented fixtures. It never uses the real home.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	root, err := os.MkdirTemp("", "agentsession-tui-89-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	fixtures, err := filepath.Abs("docs/design/tui-89/fixtures")
	if err != nil {
		return err
	}
	inputs := discovery.Inputs{FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux",
		ExplicitPaths: []discovery.ConfiguredPath{{Kind: discovery.SourceCodex, Path: fixtures}}}
	paths := app.PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	runtime, err := app.OpenRuntime(context.Background(), app.RuntimeConfig{
		DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), PathInputs: &paths, DiscoveryInputs: &inputs,
	})
	if err != nil {
		return err
	}
	defer runtime.Shutdown(context.Background())
	if _, err := runtime.DiscoverAndImport(context.Background()); err != nil {
		return err
	}
	return tui.Run(context.Background(), runtime)
}
