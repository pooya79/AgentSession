package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/pooya79/AgentSession/internal/buildinfo"
	"github.com/pooya79/AgentSession/internal/cli"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	info := buildinfo.Info{Version: version, Commit: commit, Date: date}
	if err := cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr, info); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
