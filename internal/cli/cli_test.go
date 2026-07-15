package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/buildinfo"
)

func TestHelpListsImplementedCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), []string{"--help"}, &stdout, &stderr, buildinfo.Info{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	output := stdout.String()
	for _, command := range []string{"web", "version"} {
		if !strings.Contains(output, command) {
			t.Errorf("help output does not contain %q", command)
		}
	}
	for _, command := range []string{"scan", "import", "doctor", "export"} {
		if strings.Contains(output, command) {
			t.Errorf("help output unexpectedly contains %q", command)
		}
	}
}

func TestVersionCommands(t *testing.T) {
	info := buildinfo.Info{Version: "v0.1.0", Commit: "abc123", Date: "2026-07-15"}
	want := info.String() + "\n"

	for _, args := range [][]string{{"version"}, {"--version"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := Execute(context.Background(), args, &stdout, &stderr, info)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if got := stdout.String(); got != want {
				t.Errorf("output = %q, want %q", got, want)
			}
		})
	}
}

func TestUnknownCommandReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), []string{"missing"}, &stdout, &stderr, buildinfo.Info{})
	if err == nil {
		t.Fatal("Execute() error = nil, want an unknown-command error")
	}
}
