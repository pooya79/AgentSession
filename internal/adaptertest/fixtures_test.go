package adaptertest

import (
	"path/filepath"
	"testing"
)

func TestLoadSanitizedFixture(t *testing.T) {
	content := LoadSanitizedFixture(t, filepath.Join("testdata", "sanitized.jsonl"), "real-project-name")
	if len(content) == 0 {
		t.Fatal("LoadSanitizedFixture() returned empty content")
	}
}

func TestCheckSanitizedFixtureRejectsDefaultAndCustomMarkers(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		additional []string
	}{
		{name: "private key", content: "-----BEGIN PRIVATE KEY-----"},
		{name: "personal home", content: "/home/person/project"},
		{name: "custom identity", content: "private-project", additional: []string{"private-project"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckSanitizedFixture([]byte(tt.content), tt.additional...); err == nil {
				t.Fatal("CheckSanitizedFixture() error = nil, want forbidden marker error")
			}
		})
	}
}

func TestCheckSanitizedFixtureAllowsPlaceholdersAndMalformedData(t *testing.T) {
	content := []byte("{malformed <home> <token>}\n")
	if err := CheckSanitizedFixture(content); err != nil {
		t.Fatalf("CheckSanitizedFixture() error = %v", err)
	}
}
