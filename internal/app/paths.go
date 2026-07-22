package app

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

const databaseFilename = "agentsession.db"

// RuntimePaths are all application-owned filesystem locations. ConfigDir is
// resolved eagerly but is not created by opening the runtime.
type RuntimePaths struct {
	DataDir      string
	ConfigDir    string
	DatabasePath string
}

// PathOptions are optional command-line overrides.
type PathOptions struct {
	DataDir   string
	ConfigDir string
}

// PathInputs makes platform path resolution deterministic in tests.
type PathInputs struct {
	GOOS       string
	HomeDir    string
	WorkingDir string
	LookupEnv  func(string) (string, bool)
}

// ResolveRuntimePaths applies platform conventions and resolves relative
// overrides against WorkingDir.
func ResolveRuntimePaths(inputs PathInputs, options PathOptions) (RuntimePaths, error) {
	goos := strings.TrimSpace(inputs.GOOS)
	if goos == "" {
		return RuntimePaths{}, fmt.Errorf("resolve runtime paths: GOOS is required")
	}
	working := platformClean(goos, inputs.WorkingDir)
	if working == "" || !platformAbs(goos, working) {
		return RuntimePaths{}, fmt.Errorf("resolve runtime paths: working directory must be absolute")
	}
	lookup := inputs.LookupEnv
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	resolveOverride := func(value string) string {
		value = platformClean(goos, strings.TrimSpace(value))
		if value != "" && !platformAbs(goos, value) {
			value = platformJoin(goos, working, value)
		}
		return value
	}
	dataDir := resolveOverride(options.DataDir)
	configDir := resolveOverride(options.ConfigDir)
	home := platformClean(goos, inputs.HomeDir)
	env := func(name string) string {
		value, ok := lookup(name)
		if !ok {
			return ""
		}
		return platformClean(goos, strings.TrimSpace(value))
	}
	requireHome := func() error {
		if home == "" || !platformAbs(goos, home) {
			return fmt.Errorf("resolve runtime paths for %s: absolute home directory is required", goos)
		}
		return nil
	}

	switch goos {
	case "windows":
		if dataDir == "" {
			base := env("LOCALAPPDATA")
			if base == "" || !platformAbs(goos, base) {
				return RuntimePaths{}, fmt.Errorf("resolve runtime data path for windows: LOCALAPPDATA is required")
			}
			dataDir = platformJoin(goos, base, "AgentSession")
		}
		if configDir == "" {
			base := env("APPDATA")
			if base == "" || !platformAbs(goos, base) {
				return RuntimePaths{}, fmt.Errorf("resolve runtime config path for windows: APPDATA is required")
			}
			configDir = platformJoin(goos, base, "AgentSession")
		}
	case "darwin":
		if dataDir == "" || configDir == "" {
			if err := requireHome(); err != nil {
				return RuntimePaths{}, err
			}
			base := platformJoin(goos, home, "Library", "Application Support", "AgentSession")
			if dataDir == "" {
				dataDir = base
			}
			if configDir == "" {
				configDir = platformJoin(goos, base, "config")
			}
		}
	default:
		if dataDir == "" {
			base := env("XDG_DATA_HOME")
			if base == "" {
				if err := requireHome(); err != nil {
					return RuntimePaths{}, err
				}
				base = platformJoin(goos, home, ".local", "share")
			} else if !platformAbs(goos, base) {
				return RuntimePaths{}, fmt.Errorf("resolve runtime data path: XDG_DATA_HOME must be absolute")
			}
			dataDir = platformJoin(goos, base, "agentsession")
		}
		if configDir == "" {
			base := env("XDG_CONFIG_HOME")
			if base == "" {
				if err := requireHome(); err != nil {
					return RuntimePaths{}, err
				}
				base = platformJoin(goos, home, ".config")
			} else if !platformAbs(goos, base) {
				return RuntimePaths{}, fmt.Errorf("resolve runtime config path: XDG_CONFIG_HOME must be absolute")
			}
			configDir = platformJoin(goos, base, "agentsession")
		}
	}
	return RuntimePaths{DataDir: dataDir, ConfigDir: configDir, DatabasePath: platformJoin(goos, dataDir, databaseFilename)}, nil
}

// ResolvePaths is a concise alias for ResolveRuntimePaths.
func ResolvePaths(inputs PathInputs, options PathOptions) (RuntimePaths, error) {
	return ResolveRuntimePaths(inputs, options)
}

func platformClean(goos, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if goos != "windows" {
		return filepath.Clean(value)
	}
	value = strings.ReplaceAll(value, `\`, "/")
	cleaned := path.Clean(value)
	return strings.ReplaceAll(cleaned, "/", `\`)
}

func platformJoin(goos string, elements ...string) string {
	if goos != "windows" {
		return filepath.Join(elements...)
	}
	parts := make([]string, len(elements))
	for i, element := range elements {
		parts[i] = strings.ReplaceAll(element, `\`, "/")
	}
	return strings.ReplaceAll(path.Join(parts...), "/", `\`)
}

func platformAbs(goos, value string) bool {
	if goos != "windows" {
		return filepath.IsAbs(value)
	}
	v := strings.ReplaceAll(value, `\`, "/")
	return strings.HasPrefix(v, "//") || (len(v) >= 3 && ((v[0] >= 'A' && v[0] <= 'Z') || (v[0] >= 'a' && v[0] <= 'z')) && v[1] == ':' && v[2] == '/')
}
