package buildinfo

import "fmt"

// Info describes the executable that is currently running.
type Info struct {
	Version string
	Commit  string
	Date    string
}

// String formats build metadata for CLI output.
func (i Info) String() string {
	return fmt.Sprintf("AgentSession %s\ncommit: %s\nbuilt: %s", i.Version, i.Commit, i.Date)
}
