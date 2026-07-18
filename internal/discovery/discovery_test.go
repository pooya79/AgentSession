package discovery

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestDiscoverPlatformDefaultsAndEnvironmentOverrides(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	codexHome := filepath.Join(home, "custom-codex")
	claudeHome := filepath.Join(home, "custom-claude")
	dataHome := filepath.Join(home, "custom-data")
	codexFile := filepath.Join(codexHome, "sessions", "2026", "07", "18", "rollout-one.jsonl")
	claudeFile := filepath.Join(claudeHome, "projects", "project-a", "session.jsonl")
	openCodeFile := filepath.Join(dataHome, "opencode", "opencode.db")
	writeTestFile(t, codexFile, []byte("not parsed codex"))
	writeTestFile(t, claudeFile, []byte("not parsed claude"))
	writeTestFile(t, openCodeFile, []byte("not sqlite"))
	writeTestFile(t, filepath.Join(codexHome, "sessions", "ignored.txt"), []byte("ignored"))

	discoverer := newTestDiscoverer(t, Inputs{
		HomeDir:    home,
		WorkingDir: home,
		LookupEnv: envLookup(map[string]string{
			"CODEX_HOME":        codexHome,
			"CLAUDE_CONFIG_DIR": claudeHome,
			"XDG_DATA_HOME":     dataHome,
		}),
	})
	result, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("Discover() diagnostics = %v, want none", result.Diagnostics)
	}
	want := []Source{
		{ID: newSourceID(SourceClaude, claudeFile, runtime.GOOS), Kind: SourceClaude, Path: claudeFile, Origin: OriginDefault},
		{ID: newSourceID(SourceCodex, codexFile, runtime.GOOS), Kind: SourceCodex, Path: codexFile, Origin: OriginDefault},
		{ID: newSourceID(SourceOpenCode, openCodeFile, runtime.GOOS), Kind: SourceOpenCode, Path: openCodeFile, Origin: OriginDefault},
	}
	if !slices.Equal(result.Sources, want) {
		t.Fatalf("Discover() sources = %#v, want %#v", result.Sources, want)
	}
}

func TestDiscoverExplicitFilesDirectoriesAndDeduplication(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	defaultFile := filepath.Join(home, ".codex", "sessions", "2026", "07", "18", "rollout-default.jsonl")
	claudeRoot := filepath.Join(home, "other-claude")
	claudeFile := filepath.Join(claudeRoot, "nested", "session.jsonl")
	nonstandardOpenCode := filepath.Join(home, "backup", "sessions.sqlite")
	writeTestFile(t, defaultFile, nil)
	writeTestFile(t, claudeFile, nil)
	writeTestFile(t, nonstandardOpenCode, nil)

	discoverer := newTestDiscoverer(t, Inputs{
		HomeDir:    home,
		WorkingDir: home,
		ExplicitPaths: []ConfiguredPath{
			{Kind: SourceCodex, Path: defaultFile},
			{Kind: SourceClaude, Path: claudeRoot},
			{Kind: SourceOpenCode, Path: filepath.Join("backup", "sessions.sqlite")},
		},
	})
	result, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("Discover() diagnostics = %v, want none", result.Diagnostics)
	}
	if len(result.Sources) != 3 {
		t.Fatalf("Discover() source count = %d, want 3: %#v", len(result.Sources), result.Sources)
	}
	for _, source := range result.Sources {
		if source.Origin != OriginExplicit {
			t.Errorf("source %q origin = %q, want explicit", source.Path, source.Origin)
		}
	}
}

func TestDiscoverFollowsConfiguredRootSymlinks(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	realCodexRoot := filepath.Join(home, "stored-codex-sessions")
	codexFile := filepath.Join(realCodexRoot, "rollout-symlinked.jsonl")
	realClaudeRoot := filepath.Join(home, "stored-claude-sessions")
	claudeFile := filepath.Join(realClaudeRoot, "session.jsonl")
	writeTestFile(t, codexFile, nil)
	writeTestFile(t, claudeFile, nil)

	defaultRoot := filepath.Join(home, ".codex", "sessions")
	if err := os.MkdirAll(filepath.Dir(defaultRoot), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(defaultRoot), err)
	}
	if err := os.Symlink(realCodexRoot, defaultRoot); err != nil {
		t.Skipf("create default source root symlink: %v", err)
	}
	explicitRoot := filepath.Join(home, "claude-sessions-link")
	if err := os.Symlink(realClaudeRoot, explicitRoot); err != nil {
		t.Skipf("create explicit source root symlink: %v", err)
	}

	discoverer := newTestDiscoverer(t, Inputs{
		HomeDir:       home,
		WorkingDir:    home,
		ExplicitPaths: []ConfiguredPath{{Kind: SourceClaude, Path: explicitRoot}},
	})
	result, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("Discover() diagnostics = %v, want none", result.Diagnostics)
	}
	want := []Source{
		{ID: newSourceID(SourceClaude, filepath.Join(explicitRoot, "session.jsonl"), runtime.GOOS), Kind: SourceClaude, Path: filepath.Join(explicitRoot, "session.jsonl"), Origin: OriginExplicit},
		{ID: newSourceID(SourceCodex, filepath.Join(defaultRoot, "rollout-symlinked.jsonl"), runtime.GOOS), Kind: SourceCodex, Path: filepath.Join(defaultRoot, "rollout-symlinked.jsonl"), Origin: OriginDefault},
	}
	if !slices.Equal(result.Sources, want) {
		t.Fatalf("Discover() sources = %#v, want %#v", result.Sources, want)
	}
}

func TestDiscoverContinuesAfterInaccessibleAndMalformedCandidates(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	blocked := filepath.Join(home, ".codex", "sessions", "rollout-blocked.jsonl")
	valid := filepath.Join(home, ".claude", "projects", "project", "valid.jsonl")
	malformedDefault := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	writeTestFile(t, blocked, nil)
	writeTestFile(t, valid, nil)
	if err := os.MkdirAll(malformedDefault, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", malformedDefault, err)
	}
	permissionErr := fs.ErrPermission
	tracking := &trackingFS{FileSystem: OSFileSystem{}, openErrors: map[string]error{blocked: permissionErr}}
	discoverer := newTestDiscoverer(t, Inputs{FileSystem: tracking, HomeDir: home, WorkingDir: home})

	result, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Sources) != 1 || result.Sources[0].Path != valid {
		t.Fatalf("Discover() sources = %#v, want only %q", result.Sources, valid)
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("Discover() diagnostic count = %d, want 2: %v", len(result.Diagnostics), result.Diagnostics)
	}
	if result.Diagnostics[0].Code != DiagnosticInaccessible || !errors.Is(result.Diagnostics[0], permissionErr) {
		t.Errorf("first diagnostic = %#v, want wrapped inaccessible permission error", result.Diagnostics[0])
	}
	if result.Diagnostics[1].Code != DiagnosticMalformed {
		t.Errorf("second diagnostic code = %q, want malformed", result.Diagnostics[1].Code)
	}
}

func TestDiscoverInaccessibleDefaultRootDoesNotSuppressValidRoot(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	blockedRoot := filepath.Join(home, ".codex", "sessions")
	valid := filepath.Join(home, ".claude", "projects", "project", "valid.jsonl")
	if err := os.MkdirAll(blockedRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", blockedRoot, err)
	}
	writeTestFile(t, valid, nil)
	tracking := &trackingFS{FileSystem: OSFileSystem{}, lstatErrors: map[string]error{blockedRoot: fs.ErrPermission}}
	discoverer := newTestDiscoverer(t, Inputs{FileSystem: tracking, HomeDir: home, WorkingDir: home})

	result, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Sources) != 1 || result.Sources[0].Path != valid {
		t.Fatalf("Discover() sources = %#v, want only %q", result.Sources, valid)
	}
	if len(result.Diagnostics) != 1 || !errors.Is(result.Diagnostics[0], fs.ErrPermission) {
		t.Fatalf("Discover() diagnostics = %#v, want inaccessible default root", result.Diagnostics)
	}
}

func TestDiscoverMissingDefaultsAreSilentButMissingExplicitPathIsDiagnostic(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	missing := filepath.Join(home, "missing.jsonl")
	discoverer := newTestDiscoverer(t, Inputs{
		HomeDir:       home,
		WorkingDir:    home,
		ExplicitPaths: []ConfiguredPath{{Kind: SourceClaude, Path: missing}},
	})
	result, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Sources) != 0 {
		t.Fatalf("Discover() sources = %#v, want none", result.Sources)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != DiagnosticInaccessible || !errors.Is(result.Diagnostics[0], fs.ErrNotExist) {
		t.Fatalf("Discover() diagnostics = %#v, want one wrapped missing explicit path", result.Diagnostics)
	}
}

func TestDiscoverReportsMalformedExplicitConfiguration(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	discoverer := newTestDiscoverer(t, Inputs{
		HomeDir:    home,
		WorkingDir: home,
		ExplicitPaths: []ConfiguredPath{
			{Kind: "other", Path: "/somewhere"},
			{Kind: SourceCodex, Path: ""},
		},
	})
	result, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("Discover() diagnostic count = %d, want 2", len(result.Diagnostics))
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code != DiagnosticMalformed || diagnostic.Severity != "warning" {
			t.Errorf("diagnostic = %#v, want malformed warning", diagnostic)
		}
	}
}

func TestDiscoverHonorsCancellationAndReturnsCompletedResults(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	codexFile := filepath.Join(home, ".codex", "sessions", "rollout-first.jsonl")
	claudeRoot := filepath.Join(home, ".claude", "projects")
	writeTestFile(t, codexFile, nil)
	if err := os.MkdirAll(claudeRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", claudeRoot, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	tracking := &trackingFS{FileSystem: OSFileSystem{}, cancelOnReadDir: claudeRoot, cancel: cancel}
	discoverer := newTestDiscoverer(t, Inputs{FileSystem: tracking, HomeDir: home, WorkingDir: home})

	result, err := discoverer.Discover(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover() error = %v, want context canceled", err)
	}
	if len(result.Sources) != 1 || result.Sources[0].Path != codexFile {
		t.Fatalf("Discover() partial sources = %#v, want %q", result.Sources, codexFile)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("Discover() diagnostics = %#v, want cancellation not converted to diagnostic", result.Diagnostics)
	}

	canceled, stop := context.WithCancel(context.Background())
	stop()
	result, err = discoverer.Discover(canceled)
	if !errors.Is(err, context.Canceled) || len(result.Sources) != 0 {
		t.Fatalf("Discover(pre-canceled) = (%#v, %v), want empty and canceled", result, err)
	}
}

func TestDiscoverDoesNotReadCandidateContentsAndUsesOnlyReadOnlyBoundary(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	file := filepath.Join(home, ".claude", "projects", "repo", "invalid.jsonl")
	writeTestFile(t, file, []byte("{ definitely not valid JSON"))
	tracking := &trackingFS{FileSystem: OSFileSystem{}}
	discoverer := newTestDiscoverer(t, Inputs{FileSystem: tracking, HomeDir: home, WorkingDir: home})

	result, err := discoverer.Discover(context.Background())
	if err != nil || len(result.Sources) != 1 {
		t.Fatalf("Discover() = (%#v, %v), want invalid-content candidate located", result, err)
	}
	if !slices.Contains(tracking.opened, file) {
		t.Fatalf("opened paths = %#v, want candidate read-only accessibility check", tracking.opened)
	}
	for _, opened := range tracking.opened {
		if strings.Contains(opened, "repo/.git") || strings.Contains(opened, `repo\.git`) {
			t.Fatalf("discovery opened repository metadata %q", opened)
		}
	}
}

func TestDefaultLocationsUseInjectedWindowsPathInputs(t *testing.T) {
	t.Parallel()

	recording := &trackingFS{FileSystem: missingFS{}}
	discoverer, err := New(Inputs{
		FileSystem: recording,
		HomeDir:    `C:\Users\tester`,
		WorkingDir: `C:\workspace`,
		GOOS:       "windows",
		LookupEnv: envLookup(map[string]string{
			"CODEX_HOME":        `D:\codex`,
			"CLAUDE_CONFIG_DIR": `D:\claude`,
			"XDG_DATA_HOME":     `D:\data`,
		}),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := discoverer.Discover(context.Background())
	if err != nil || len(result.Diagnostics) != 0 {
		t.Fatalf("Discover() = (%#v, %v), want missing defaults to be silent", result, err)
	}
	want := []string{`D:\codex\sessions`, `D:\claude\projects`, `D:\data\opencode\opencode.db`}
	if !slices.Equal(recording.lstatPaths, want) {
		t.Fatalf("default Lstat paths = %#v, want %#v", recording.lstatPaths, want)
	}
}

func TestWindowsUNCPathsRemainAbsolute(t *testing.T) {
	t.Parallel()

	recording := &trackingFS{FileSystem: missingFS{}}
	discoverer, err := New(Inputs{
		FileSystem: recording,
		HomeDir:    `\\server\users\tester`,
		WorkingDir: `\\server\work`,
		GOOS:       "windows",
		LookupEnv: envLookup(map[string]string{
			"CODEX_HOME": `\\storage\agent-data\codex`,
		}),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := discoverer.Discover(context.Background())
	if err != nil || len(result.Diagnostics) != 0 {
		t.Fatalf("Discover() = (%#v, %v), want missing defaults to be silent", result, err)
	}
	want := []string{
		`\\storage\agent-data\codex\sessions`,
		`\\server\users\tester\.claude\projects`,
		`\\server\users\tester\.local\share\opencode\opencode.db`,
	}
	if !slices.Equal(recording.lstatPaths, want) {
		t.Fatalf("default Lstat paths = %#v, want %#v", recording.lstatPaths, want)
	}
}

func TestSourceIDCompatibility(t *testing.T) {
	t.Parallel()

	got := newSourceID(SourceCodex, "/home/test/.codex/sessions/2026/07/18/rollout-a.jsonl", "linux")
	const want = "src_0ae011f262e18ec534e8db1189f9ead3b6d1abe9c5da4d8fec7d756bf70be302"
	if got != want {
		t.Fatalf("newSourceID() = %q, want compatibility value %q", got, want)
	}
}

func TestNewValidatesRequiredGlobalInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		inputs Inputs
	}{
		{name: "filesystem", inputs: Inputs{HomeDir: "/home", WorkingDir: "/work", GOOS: "linux"}},
		{name: "GOOS", inputs: Inputs{FileSystem: OSFileSystem{}, HomeDir: "/home", WorkingDir: "/work"}},
		{name: "home", inputs: Inputs{FileSystem: OSFileSystem{}, HomeDir: "relative", WorkingDir: "/work", GOOS: "linux"}},
		{name: "working directory", inputs: Inputs{FileSystem: OSFileSystem{}, HomeDir: "/home", WorkingDir: "relative", GOOS: "linux"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(tt.inputs); err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
		})
	}
}

func newTestDiscoverer(t *testing.T, inputs Inputs) *Discoverer {
	t.Helper()
	if inputs.FileSystem == nil {
		inputs.FileSystem = OSFileSystem{}
	}
	if inputs.GOOS == "" {
		inputs.GOOS = runtime.GOOS
	}
	if inputs.LookupEnv == nil {
		inputs.LookupEnv = envLookup(nil)
	}
	discoverer, err := New(inputs)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return discoverer
}

func writeTestFile(t *testing.T, name string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", name, err)
	}
}

func envLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

type trackingFS struct {
	FileSystem
	openErrors      map[string]error
	lstatErrors     map[string]error
	opened          []string
	lstatPaths      []string
	cancelOnReadDir string
	cancel          context.CancelFunc
}

func (f *trackingFS) Lstat(ctx context.Context, name string) (fs.FileInfo, error) {
	f.lstatPaths = append(f.lstatPaths, name)
	if err := f.lstatErrors[name]; err != nil {
		return nil, err
	}
	return f.FileSystem.Lstat(ctx, name)
}

func (f *trackingFS) ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	if name == f.cancelOnReadDir {
		f.cancel()
		return nil, ctx.Err()
	}
	return f.FileSystem.ReadDir(ctx, name)
}

func (f *trackingFS) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	f.opened = append(f.opened, name)
	if err := f.openErrors[name]; err != nil {
		return nil, err
	}
	return f.FileSystem.Open(ctx, name)
}

type missingFS struct{}

func (missingFS) Lstat(context.Context, string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
func (missingFS) Stat(context.Context, string) (fs.FileInfo, error)  { return nil, fs.ErrNotExist }
func (missingFS) ReadDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, fs.ErrNotExist
}
func (missingFS) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, fs.ErrNotExist
}
