package app

import "testing"

func TestResolveRuntimePaths(t *testing.T) {
	tests := []struct {
		name    string
		inputs  PathInputs
		options PathOptions
		want    RuntimePaths
	}{
		{
			name: "linux defaults", inputs: PathInputs{GOOS: "linux", HomeDir: "/home/test", WorkingDir: "/work"},
			want: RuntimePaths{DataDir: "/home/test/.local/share/agentsession", ConfigDir: "/home/test/.config/agentsession", DatabasePath: "/home/test/.local/share/agentsession/agentsession.db"},
		},
		{
			name: "linux xdg", inputs: PathInputs{GOOS: "linux", HomeDir: "/home/test", WorkingDir: "/work", LookupEnv: mapLookup(map[string]string{"XDG_DATA_HOME": "/data", "XDG_CONFIG_HOME": "/config"})},
			want: RuntimePaths{DataDir: "/data/agentsession", ConfigDir: "/config/agentsession", DatabasePath: "/data/agentsession/agentsession.db"},
		},
		{
			name: "macOS application support", inputs: PathInputs{GOOS: "darwin", HomeDir: "/Users/test", WorkingDir: "/work"},
			want: RuntimePaths{DataDir: "/Users/test/Library/Application Support/AgentSession", ConfigDir: "/Users/test/Library/Application Support/AgentSession/config", DatabasePath: "/Users/test/Library/Application Support/AgentSession/agentsession.db"},
		},
		{
			name: "windows appdata", inputs: PathInputs{GOOS: "windows", WorkingDir: `C:\work`, LookupEnv: mapLookup(map[string]string{"LOCALAPPDATA": `C:\Users\test\AppData\Local`, "APPDATA": `C:\Users\test\AppData\Roaming`})},
			want: RuntimePaths{DataDir: `C:\Users\test\AppData\Local\AgentSession`, ConfigDir: `C:\Users\test\AppData\Roaming\AgentSession`, DatabasePath: `C:\Users\test\AppData\Local\AgentSession\agentsession.db`},
		},
		{
			name: "windows UNC appdata", inputs: PathInputs{GOOS: "windows", WorkingDir: `\\server\work`, LookupEnv: mapLookup(map[string]string{"LOCALAPPDATA": `\\server\profiles\local`, "APPDATA": `\\server\profiles\roaming`})},
			want: RuntimePaths{DataDir: `\\server\profiles\local\AgentSession`, ConfigDir: `\\server\profiles\roaming\AgentSession`, DatabasePath: `\\server\profiles\local\AgentSession\agentsession.db`},
		},
		{
			name: "windows UNC overrides", inputs: PathInputs{GOOS: "windows", WorkingDir: `C:\work`}, options: PathOptions{DataDir: `\\storage\state\.\data`, ConfigDir: `\\storage\state\config\..\settings`},
			want: RuntimePaths{DataDir: `\\storage\state\data`, ConfigDir: `\\storage\state\settings`, DatabasePath: `\\storage\state\data\agentsession.db`},
		},
		{
			name: "relative overrides", inputs: PathInputs{GOOS: "linux", WorkingDir: "/work"}, options: PathOptions{DataDir: "state", ConfigDir: "settings"},
			want: RuntimePaths{DataDir: "/work/state", ConfigDir: "/work/settings", DatabasePath: "/work/state/agentsession.db"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveRuntimePaths(test.inputs, test.options)
			if err != nil {
				t.Fatalf("ResolveRuntimePaths() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("paths = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestResolveRuntimePathsRequiresPlatformInputs(t *testing.T) {
	for _, inputs := range []PathInputs{
		{GOOS: "linux", WorkingDir: "/work"},
		{GOOS: "darwin", WorkingDir: "/work"},
		{GOOS: "windows", WorkingDir: `C:\work`},
	} {
		if _, err := ResolveRuntimePaths(inputs, PathOptions{}); err == nil {
			t.Fatalf("ResolveRuntimePaths(%q) error = nil", inputs.GOOS)
		}
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }
}
