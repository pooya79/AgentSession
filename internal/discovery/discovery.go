// Package discovery locates candidate coding-agent session sources without
// interpreting their source-specific records.
package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

// SourceKind is a location hint for adapter selection. It does not assert
// that a candidate's contents conform to that source format.
type SourceKind string

const (
	SourceCodex    SourceKind = "codex"
	SourceClaude   SourceKind = "claude"
	SourceOpenCode SourceKind = "opencode"
)

// SourceOrigin records how a candidate location entered discovery.
type SourceOrigin string

const (
	OriginDefault  SourceOrigin = "default"
	OriginExplicit SourceOrigin = "explicit"
)

// ConfiguredPath is a user-supplied file or search root associated with one
// source kind. Adapters, not discovery, confirm the source format.
type ConfiguredPath struct {
	Kind SourceKind
	Path string
}

// Source is one read-only candidate file found by discovery.
type Source struct {
	ID     model.SourceID
	Kind   SourceKind
	Path   string
	Origin SourceOrigin
}

// DiagnosticCode is a stable machine-readable discovery problem category.
type DiagnosticCode string

const (
	DiagnosticInaccessible DiagnosticCode = "discovery.inaccessible"
	DiagnosticMalformed    DiagnosticCode = "discovery.malformed"
)

// Diagnostic describes one unusable candidate without invalidating other
// discovery results. Cause is retained for errors.Is and errors.As.
type Diagnostic struct {
	Code     DiagnosticCode
	Severity model.Severity
	Kind     SourceKind
	Path     string
	Message  string
	Cause    error
}

// Error returns the human-readable diagnostic explanation.
func (d Diagnostic) Error() string { return d.Message }

// Unwrap exposes the underlying filesystem or validation failure.
func (d Diagnostic) Unwrap() error { return d.Cause }

// Result distinguishes successfully located candidates from independent
// source-level diagnostics.
type Result struct {
	Sources     []Source
	Diagnostics []Diagnostic
}

// FileSystem is the read-only, context-aware filesystem boundary consumed by
// discovery. Implementations must not open paths with write access.
type FileSystem interface {
	Lstat(context.Context, string) (fs.FileInfo, error)
	Stat(context.Context, string) (fs.FileInfo, error)
	ReadDir(context.Context, string) ([]fs.DirEntry, error)
	Open(context.Context, string) (io.ReadCloser, error)
}

// Inputs makes platform defaults and explicit locations deterministic in
// tests. HomeDir and WorkingDir must be absolute in the selected path style.
type Inputs struct {
	FileSystem    FileSystem
	HomeDir       string
	WorkingDir    string
	GOOS          string
	LookupEnv     func(string) (string, bool)
	ExplicitPaths []ConfiguredPath
}

// Discoverer locates candidate source files from immutable inputs.
type Discoverer struct {
	fs       FileSystem
	home     string
	working  string
	goos     string
	lookup   func(string) (string, bool)
	explicit []ConfiguredPath
}

// New constructs a discoverer from injected filesystem and environment
// inputs. Per-path configuration problems are reported by Discover.
func New(inputs Inputs) (*Discoverer, error) {
	if inputs.FileSystem == nil {
		return nil, fmt.Errorf("discovery filesystem is required")
	}
	goos := strings.TrimSpace(inputs.GOOS)
	if goos == "" {
		return nil, fmt.Errorf("discovery GOOS is required")
	}
	home := cleanPath(goos, inputs.HomeDir)
	if home == "" || !isAbsPath(goos, home) {
		return nil, fmt.Errorf("discovery home directory must be absolute")
	}
	working := cleanPath(goos, inputs.WorkingDir)
	if working == "" || !isAbsPath(goos, working) {
		return nil, fmt.Errorf("discovery working directory must be absolute")
	}
	lookup := inputs.LookupEnv
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	return &Discoverer{
		fs:       inputs.FileSystem,
		home:     home,
		working:  working,
		goos:     goos,
		lookup:   lookup,
		explicit: append([]ConfiguredPath(nil), inputs.ExplicitPaths...),
	}, nil
}

// NewOS constructs a discoverer using the current process environment and a
// filesystem implementation that opens all paths read-only.
func NewOS(explicit []ConfiguredPath) (*Discoverer, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home directory: %w", err)
	}
	working, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	return New(Inputs{
		FileSystem:    OSFileSystem{},
		HomeDir:       home,
		WorkingDir:    working,
		GOOS:          runtime.GOOS,
		LookupEnv:     os.LookupEnv,
		ExplicitPaths: explicit,
	})
}

type defaultLocation struct {
	kind   SourceKind
	path   string
	direct bool
}

// Discover locates all usable candidates. Cancellation stops traversal and is
// returned as the top-level error; any results completed before cancellation
// remain available in Result.
func (d *Discoverer) Discover(ctx context.Context) (Result, error) {
	result := Result{}
	sources := make(map[model.SourceID]Source)

	for _, location := range d.defaultLocations() {
		if err := contextCause(ctx); err != nil {
			return finishResult(result, sources), err
		}
		if location.direct {
			d.discoverDefaultFile(ctx, location, sources, &result.Diagnostics)
		} else {
			d.discoverDefaultRoot(ctx, location, sources, &result.Diagnostics)
		}
		if err := contextCause(ctx); err != nil {
			return finishResult(result, sources), err
		}
	}

	for _, configured := range d.explicit {
		if err := contextCause(ctx); err != nil {
			return finishResult(result, sources), err
		}
		d.discoverExplicit(ctx, configured, sources, &result.Diagnostics)
		if err := contextCause(ctx); err != nil {
			return finishResult(result, sources), err
		}
	}

	return finishResult(result, sources), nil
}

func (d *Discoverer) defaultLocations() []defaultLocation {
	codexHome := d.envPath("CODEX_HOME", joinPath(d.goos, d.home, ".codex"))
	claudeHome := d.envPath("CLAUDE_CONFIG_DIR", joinPath(d.goos, d.home, ".claude"))
	dataHome := d.envPath("XDG_DATA_HOME", joinPath(d.goos, d.home, ".local", "share"))
	return []defaultLocation{
		{kind: SourceCodex, path: joinPath(d.goos, codexHome, "sessions")},
		{kind: SourceClaude, path: joinPath(d.goos, claudeHome, "projects")},
		{kind: SourceOpenCode, path: joinPath(d.goos, dataHome, "opencode", "opencode.db"), direct: true},
	}
}

func (d *Discoverer) envPath(name, fallback string) string {
	value, ok := d.lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return d.absolutePath(value)
}

func (d *Discoverer) discoverDefaultRoot(ctx context.Context, location defaultLocation, sources map[model.SourceID]Source, diagnostics *[]Diagnostic) {
	info, err := d.fs.Lstat(ctx, location.path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && contextCause(ctx) == nil {
			*diagnostics = append(*diagnostics, inaccessible(location.kind, location.path, "inspect default source root", err))
		}
		return
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		info, err = d.fs.Stat(ctx, location.path)
		if err != nil {
			if contextCause(ctx) == nil {
				*diagnostics = append(*diagnostics, inaccessible(location.kind, location.path, "inspect default source root symlink", err))
			}
			return
		}
	}
	if !info.IsDir() {
		*diagnostics = append(*diagnostics, malformed(location.kind, location.path, "default source root is not a directory", nil))
		return
	}
	d.walk(ctx, location.kind, location.path, OriginDefault, sources, diagnostics)
}

func (d *Discoverer) discoverDefaultFile(ctx context.Context, location defaultLocation, sources map[model.SourceID]Source, diagnostics *[]Diagnostic) {
	info, err := d.fs.Lstat(ctx, location.path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && contextCause(ctx) == nil {
			*diagnostics = append(*diagnostics, inaccessible(location.kind, location.path, "inspect default source file", err))
		}
		return
	}
	if info.IsDir() || (!info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0) {
		*diagnostics = append(*diagnostics, malformed(location.kind, location.path, "default source is not a regular file", nil))
		return
	}
	d.addCandidate(ctx, location.kind, location.path, OriginDefault, sources, diagnostics)
}

func (d *Discoverer) discoverExplicit(ctx context.Context, configured ConfiguredPath, sources map[model.SourceID]Source, diagnostics *[]Diagnostic) {
	if !validKind(configured.Kind) {
		*diagnostics = append(*diagnostics, malformed(configured.Kind, configured.Path, "explicit source has an unsupported kind", nil))
		return
	}
	if strings.TrimSpace(configured.Path) == "" {
		*diagnostics = append(*diagnostics, malformed(configured.Kind, configured.Path, "explicit source path is empty", nil))
		return
	}
	candidatePath := d.absolutePath(configured.Path)
	info, err := d.fs.Lstat(ctx, candidatePath)
	if err != nil {
		if contextCause(ctx) == nil {
			*diagnostics = append(*diagnostics, inaccessible(configured.Kind, candidatePath, "inspect explicit source", err))
		}
		return
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := d.fs.Stat(ctx, candidatePath)
		if err != nil {
			if contextCause(ctx) == nil {
				*diagnostics = append(*diagnostics, inaccessible(configured.Kind, candidatePath, "inspect explicit source symlink", err))
			}
			return
		}
		if target.IsDir() {
			d.walk(ctx, configured.Kind, candidatePath, OriginExplicit, sources, diagnostics)
			return
		}
		if !target.Mode().IsRegular() {
			*diagnostics = append(*diagnostics, malformed(configured.Kind, candidatePath, "explicit source is not a regular file or directory", nil))
			return
		}
		d.addCandidate(ctx, configured.Kind, candidatePath, OriginExplicit, sources, diagnostics)
		return
	}
	if info.IsDir() {
		d.walk(ctx, configured.Kind, candidatePath, OriginExplicit, sources, diagnostics)
		return
	}
	if !info.Mode().IsRegular() {
		*diagnostics = append(*diagnostics, malformed(configured.Kind, candidatePath, "explicit source is not a regular file or directory", nil))
		return
	}
	d.addCandidate(ctx, configured.Kind, candidatePath, OriginExplicit, sources, diagnostics)
}

func (d *Discoverer) walk(ctx context.Context, kind SourceKind, root string, origin SourceOrigin, sources map[model.SourceID]Source, diagnostics *[]Diagnostic) {
	if contextCause(ctx) != nil {
		return
	}
	entries, err := d.fs.ReadDir(ctx, root)
	if err != nil {
		if contextCause(ctx) == nil {
			*diagnostics = append(*diagnostics, inaccessible(kind, root, "read source directory", err))
		}
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if contextCause(ctx) != nil {
			return
		}
		candidatePath := joinPath(d.goos, root, entry.Name())
		if entry.Type()&fs.ModeSymlink != 0 {
			if matches(kind, entry.Name()) {
				d.addCandidate(ctx, kind, candidatePath, origin, sources, diagnostics)
			}
			continue
		}
		if entry.IsDir() {
			d.walk(ctx, kind, candidatePath, origin, sources, diagnostics)
			continue
		}
		if matches(kind, entry.Name()) {
			d.addCandidate(ctx, kind, candidatePath, origin, sources, diagnostics)
		}
	}
}

func (d *Discoverer) addCandidate(ctx context.Context, kind SourceKind, candidatePath string, origin SourceOrigin, sources map[model.SourceID]Source, diagnostics *[]Diagnostic) {
	if contextCause(ctx) != nil {
		return
	}
	info, err := d.fs.Stat(ctx, candidatePath)
	if err != nil {
		if contextCause(ctx) == nil {
			*diagnostics = append(*diagnostics, inaccessible(kind, candidatePath, "inspect source candidate", err))
		}
		return
	}
	if !info.Mode().IsRegular() {
		*diagnostics = append(*diagnostics, malformed(kind, candidatePath, "source candidate is not a regular file", nil))
		return
	}
	file, err := d.fs.Open(ctx, candidatePath)
	if err != nil {
		if contextCause(ctx) == nil {
			*diagnostics = append(*diagnostics, inaccessible(kind, candidatePath, "open source candidate read-only", err))
		}
		return
	}
	if err := file.Close(); err != nil {
		*diagnostics = append(*diagnostics, inaccessible(kind, candidatePath, "close source candidate", err))
		return
	}
	cleaned := cleanPath(d.goos, candidatePath)
	id := newSourceID(kind, cleaned, d.goos)
	existing, found := sources[id]
	if !found || (existing.Origin == OriginDefault && origin == OriginExplicit) {
		sources[id] = Source{ID: id, Kind: kind, Path: cleaned, Origin: origin}
	}
}

func (d *Discoverer) absolutePath(value string) string {
	cleaned := cleanPath(d.goos, strings.TrimSpace(value))
	if isAbsPath(d.goos, cleaned) {
		return cleaned
	}
	return joinPath(d.goos, d.working, cleaned)
}

func finishResult(result Result, sources map[model.SourceID]Source) Result {
	result.Sources = make([]Source, 0, len(sources))
	for _, source := range sources {
		result.Sources = append(result.Sources, source)
	}
	sort.Slice(result.Sources, func(i, j int) bool {
		if result.Sources[i].Kind != result.Sources[j].Kind {
			return result.Sources[i].Kind < result.Sources[j].Kind
		}
		return result.Sources[i].Path < result.Sources[j].Path
	})
	sort.SliceStable(result.Diagnostics, func(i, j int) bool {
		if result.Diagnostics[i].Kind != result.Diagnostics[j].Kind {
			return result.Diagnostics[i].Kind < result.Diagnostics[j].Kind
		}
		if result.Diagnostics[i].Path != result.Diagnostics[j].Path {
			return result.Diagnostics[i].Path < result.Diagnostics[j].Path
		}
		return result.Diagnostics[i].Code < result.Diagnostics[j].Code
	})
	return result
}

func matches(kind SourceKind, name string) bool {
	switch kind {
	case SourceCodex:
		return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
	case SourceClaude:
		return strings.HasSuffix(name, ".jsonl")
	case SourceOpenCode:
		return name == "opencode.db"
	default:
		return false
	}
}

func validKind(kind SourceKind) bool {
	return kind == SourceCodex || kind == SourceClaude || kind == SourceOpenCode
}

func newSourceID(kind SourceKind, candidatePath, goos string) model.SourceID {
	identityPath := candidatePath
	if goos == "windows" {
		identityPath = strings.ToLower(identityPath)
	}
	digest := sha256.Sum256([]byte("agentsession:source-id:v1\x00" + string(kind) + "\x00" + identityPath))
	return model.SourceID("src_" + hex.EncodeToString(digest[:]))
}

func inaccessible(kind SourceKind, candidatePath, operation string, cause error) Diagnostic {
	return Diagnostic{
		Code:     DiagnosticInaccessible,
		Severity: model.SeverityWarning,
		Kind:     kind,
		Path:     candidatePath,
		Message:  fmt.Sprintf("%s %q: %v", operation, candidatePath, cause),
		Cause:    cause,
	}
}

func malformed(kind SourceKind, candidatePath, message string, cause error) Diagnostic {
	if strings.TrimSpace(candidatePath) != "" {
		message = fmt.Sprintf("%s %q", message, candidatePath)
	}
	return Diagnostic{Code: DiagnosticMalformed, Severity: model.SeverityWarning, Kind: kind, Path: candidatePath, Message: message, Cause: cause}
}

func contextCause(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func joinPath(goos string, elements ...string) string {
	if goos != "windows" {
		return filepath.Join(elements...)
	}
	parts := make([]string, len(elements))
	for i, element := range elements {
		parts[i] = strings.ReplaceAll(element, `\`, "/")
	}
	joined := path.Join(parts...)
	if len(parts) > 0 && strings.HasPrefix(parts[0], "//") {
		joined = "/" + joined
	}
	return strings.ReplaceAll(joined, "/", `\`)
}

func cleanPath(goos, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if goos != "windows" {
		return filepath.Clean(value)
	}
	value = strings.ReplaceAll(value, `\`, "/")
	cleaned := path.Clean(value)
	if strings.HasPrefix(value, "//") {
		cleaned = "/" + cleaned
	}
	return strings.ReplaceAll(cleaned, "/", `\`)
}

func isAbsPath(goos, value string) bool {
	if goos != "windows" {
		return filepath.IsAbs(value)
	}
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/') || strings.HasPrefix(value, `\\`)
}

// OSFileSystem implements FileSystem using only read-only os package calls.
type OSFileSystem struct{}

func (OSFileSystem) Lstat(ctx context.Context, name string) (fs.FileInfo, error) {
	if err := contextCause(ctx); err != nil {
		return nil, err
	}
	info, err := os.Lstat(name)
	if err == nil {
		err = contextCause(ctx)
	}
	return info, err
}

func (OSFileSystem) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	if err := contextCause(ctx); err != nil {
		return nil, err
	}
	info, err := os.Stat(name)
	if err == nil {
		err = contextCause(ctx)
	}
	return info, err
}

func (OSFileSystem) ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	if err := contextCause(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(name)
	if err == nil {
		err = contextCause(ctx)
	}
	return entries, err
}

func (OSFileSystem) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := contextCause(ctx); err != nil {
		return nil, err
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	if err := contextCause(ctx); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
