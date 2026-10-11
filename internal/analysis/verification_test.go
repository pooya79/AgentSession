package analysis_test

import (
	"testing"

	"github.com/pooya79/AgentSession/internal/analysis"
)

func TestRecognizeGoVerification(t *testing.T) {
	got, ok := analysis.RecognizeVerification("go test -race -count=1 ./internal/...", "/workspace")
	if !ok || got.Kind != analysis.VerificationTest || got.Target != "./internal/..." {
		t.Fatalf("recognition = %#v, %t", got, ok)
	}
}

func TestGoVerificationGrammar(t *testing.T) {
	if _, ok := analysis.RecognizeVerification("go test ./... -race -count=1", "/workspace"); !ok {
		t.Fatal("Go test accepts supported flags after its package target")
	}
	for _, check := range []struct {
		verb  string
		kind  analysis.VerificationKind
		flags []string
	}{
		{"test", analysis.VerificationTest, []string{"-v", "-json", "-race", "-count=1", "-v -json -race -count=1"}},
		{"vet", analysis.VerificationStaticCheck, []string{"-json"}},
		{"build", analysis.VerificationBuild, []string{"-v", "-trimpath", "-v -trimpath"}},
	} {
		for _, target := range []string{"", ".", "./...", "./internal", "./internal/sub-package", "./internal/...", "./v1.2/pkg_3/..."} {
			for _, flags := range append([]string{""}, check.flags...) {
				command := "go " + check.verb + " " + flags + " " + target
				t.Run(command, func(t *testing.T) {
					got, ok := analysis.RecognizeVerification(command, "/workspace")
					wantTarget := target
					if target == "" {
						wantTarget = "."
					}
					if !ok || got.Kind != check.kind || got.Target != wantTarget {
						t.Fatalf("recognition = %#v, %t", got, ok)
					}
				})
			}
		}
	}
	for _, command := range []string{
		"", "go", "go run .", "npm test", "pytest", "make test", "go test -c", "go test -list .", "go test -run TestFoo", "go test -bench .", "go test -count=2", "go test -count 1", "go test -cover", "go test -unknown", "go test -trimpath", "go vet -v", "go vet -race", "go build -json", "go build -race", "go build ./... -v", "go vet ./... -json", "go build -o app", "go test -- ./...",
		"go test . ./...", "go test ./a ./b", "go test all", "go test std", "go test example.org/pkg", "go test /workspace", "go test ../pkg", "go test ./../pkg", "go test ./a/../pkg", "go test ./a//pkg", "go test ./a/", "go test ./.../pkg", "go test ./file.go", "go test ./a/*.go", "go test ./pkg*", "go test './pkg'", "go test ./[ab]", "go test ${TARGET}", "go test $(pwd)", "go test `pwd`", "go test ./a;pwd", "go test ./a#comment",
		"go test ./... && go vet ./...", "go test ./...; go vet ./...", "go test ./... | tee log", "go test ./... > log", "go test ./... &", "env go test ./...", "GOFLAGS=-race go test ./...", "sh -c 'go test ./...'", "timeout 5 go test ./...", "cd /workspace && go test ./...", "go test\n./...", "go\x00 test", "go\vtest", "go test ./... # comment",
	} {
		t.Run(command, func(t *testing.T) {
			if got, ok := analysis.RecognizeVerification(command, "/workspace"); ok {
				t.Fatalf("unsupported command recognized: %#v", got)
			}
		})
	}
	for _, directory := range []string{"", " ", "/workspace\x00", "/workspace\nother"} {
		if _, ok := analysis.RecognizeVerification("go test ./...", directory); ok {
			t.Fatalf("unavailable directory %q recognized", directory)
		}
	}
}
