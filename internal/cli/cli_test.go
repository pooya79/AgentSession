package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	for _, command := range []string{"import", "web", "version"} {
		if !strings.Contains(output, command) {
			t.Errorf("help output does not contain %q", command)
		}
	}
	for _, command := range []string{"scan", "doctor", "export"} {
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

func TestHelpAndVersionDoNotCreateDatabase(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "state")
	for _, args := range [][]string{{"--data-dir", dataDir, "--help"}, {"--data-dir", dataDir, "version"}} {
		var stdout, stderr bytes.Buffer
		if err := Execute(context.Background(), args, &stdout, &stderr, buildinfo.Info{}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%v created data directory: %v", args, err)
		}
	}
}

func TestImportWithNoSourcesSucceeds(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CODEX_HOME", filepath.Join(root, "missing-codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "missing-claude"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "missing-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config-home"))
	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), []string{"--data-dir", filepath.Join(root, "state"), "import"}, &stdout, &stderr, buildinfo.Info{})
	if err != nil {
		t.Fatalf("Execute(import) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "No session sources were found.") {
		t.Fatalf("output = %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(root, "state", "agentsession.db")); err != nil {
		t.Fatalf("database not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "config-home", "agentsession")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config directory was created: %v", err)
	}
}

func TestImportFailureDoesNotEchoRawFixtureText(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CODEX_HOME", filepath.Join(root, "missing-codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "missing-claude"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "missing-data"))
	fixture := filepath.Join(root, "malicious.jsonl")
	secret := "RAW_SECRET_SHOULD_NOT_BE_PRINTED"
	if err := os.WriteFile(fixture, []byte(secret+"\x1b]52;c;c2VjcmV0\x07\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), []string{"--data-dir", filepath.Join(root, "state"), "import", "--codex", fixture}, &stdout, &stderr, buildinfo.Info{})
	if err == nil {
		t.Fatal("Execute(import) error = nil, want unsupported source failure")
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("raw fixture text escaped boundary: output=%q error=%v", stdout.String(), err)
	}
	if !strings.Contains(stdout.String(), "failed to import") {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestUnknownCommandReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), []string{"missing"}, &stdout, &stderr, buildinfo.Info{})
	if err == nil {
		t.Fatal("Execute() error = nil, want an unknown-command error")
	}
}

func TestWriteErrorSanitizesDiagnostic(t *testing.T) {
	var output bytes.Buffer
	WriteError(&output, errors.New("source \x1b]52;c;c2VjcmV0\x07failed\u202e"))
	if got, want := output.String(), "error: source failed<U+202E>\n"; got != want {
		t.Fatalf("diagnostic = %q, want %q", got, want)
	}
}
