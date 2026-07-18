// Package adaptertest provides source-neutral helpers for adapter fixture
// tests. It must not contain source-format parsing or production behavior.
package adaptertest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var defaultForbiddenMarkers = [][]byte{
	[]byte("-----BEGIN PRIVATE KEY-----"),
	[]byte("-----BEGIN RSA PRIVATE KEY-----"),
	[]byte("-----BEGIN OPENSSH PRIVATE KEY-----"),
	[]byte("github_pat_"),
	[]byte("ghp_"),
	[]byte("sk-"),
	[]byte("/home/"),
	[]byte("/Users/"),
	[]byte(`C:\Users\`),
}

// LoadSanitizedFixture reads an explicit file under a testdata or fixtures
// directory and fails the test if it contains likely credentials, personal
// home paths, or an additional caller-provided marker.
func LoadSanitizedFixture(t testing.TB, path string, additionalForbidden ...string) []byte {
	t.Helper()
	cleaned := filepath.Clean(path)
	if !hasFixtureDirectory(cleaned) {
		t.Fatalf("fixture path %q must be under a testdata or fixtures directory", path)
	}
	info, err := os.Stat(cleaned)
	if err != nil {
		t.Fatalf("inspect sanitized fixture %q: %v", cleaned, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("sanitized fixture %q is not a regular file", cleaned)
	}
	content, err := os.ReadFile(cleaned)
	if err != nil {
		t.Fatalf("read sanitized fixture %q: %v", cleaned, err)
	}
	if err := CheckSanitizedFixture(content, additionalForbidden...); err != nil {
		t.Fatalf("sanitized fixture %q: %v", cleaned, err)
	}
	return content
}

// CheckSanitizedFixture checks fixture bytes without interpreting their source
// format. Additional markers let an adapter reject project-specific identities.
func CheckSanitizedFixture(content []byte, additionalForbidden ...string) error {
	markers := append([][]byte(nil), defaultForbiddenMarkers...)
	for _, marker := range additionalForbidden {
		if strings.TrimSpace(marker) != "" {
			markers = append(markers, []byte(marker))
		}
	}
	for _, marker := range markers {
		if bytes.Contains(content, marker) {
			return fmt.Errorf("contains forbidden marker %q", marker)
		}
	}
	return nil
}

func hasFixtureDirectory(path string) bool {
	for {
		directory, base := filepath.Split(path)
		base = strings.TrimSuffix(base, string(filepath.Separator))
		if base == "testdata" || base == "fixtures" {
			return true
		}
		next := filepath.Clean(strings.TrimSuffix(directory, string(filepath.Separator)))
		if next == "." || next == path || directory == "" {
			return false
		}
		path = next
	}
}
