package analysis

import (
	"strings"
	"unicode/utf8"
)

// VerificationKind limits what a recorded successful check establishes.
type VerificationKind string

const (
	VerificationTest        VerificationKind = "test"
	VerificationStaticCheck VerificationKind = "static_check"
	VerificationBuild       VerificationKind = "build"
)

// Verification describes declared scope, without claiming file participation.
type Verification struct {
	Kind   VerificationKind
	Target string
}

// RecognizeVerification accepts only the documented standalone Go grammar.
// It never executes commands or consults the filesystem. False means uncertain,
// not proof that verification did not occur. Directory must be established by
// canonical evidence; it is not inferred from the command or a live repository.
func RecognizeVerification(command, directory string) (Verification, bool) {
	if strings.TrimSpace(directory) == "" || len(directory) > MaxDirectoryBytes || !utf8.ValidString(directory) || strings.ContainsAny(directory, "\x00\r\n") || len(command) > MaxCommandBytes || !utf8.ValidString(command) {
		return Verification{}, false
	}
	command = strings.TrimSpace(command)
	if strings.ContainsAny(command, "\r\n") {
		return Verification{}, false
	}
	words := strings.FieldsFunc(command, func(r rune) bool { return r == ' ' || r == '\t' })
	if len(words) < 2 || words[0] != "go" {
		return Verification{}, false
	}
	v := Verification{Target: "."}
	switch words[1] {
	case "test":
		v.Kind = VerificationTest
	case "vet":
		v.Kind = VerificationStaticCheck
	case "build":
		v.Kind = VerificationBuild
	default:
		return Verification{}, false
	}
	haveTarget := false
	for _, word := range words[2:] {
		if strings.HasPrefix(word, "-") {
			if haveTarget && v.Kind != VerificationTest {
				return Verification{}, false
			}
			allowed := (v.Kind == VerificationTest && (word == "-v" || word == "-json" || word == "-race" || word == "-count=1")) ||
				(v.Kind == VerificationStaticCheck && word == "-json") ||
				(v.Kind == VerificationBuild && (word == "-v" || word == "-trimpath"))
			if !allowed {
				return Verification{}, false
			}
			continue
		}
		if haveTarget || !localVerificationTarget(word) {
			return Verification{}, false
		}
		v.Target, haveTarget = word, true
	}
	return v, true
}

func localVerificationTarget(target string) bool {
	if target == "." || target == "./..." {
		return true
	}
	if !strings.HasPrefix(target, "./") {
		return false
	}
	path := strings.TrimSuffix(target[2:], "/...")
	if strings.HasSuffix(path, ".go") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.Contains(part, "...") {
			return false
		}
		for _, r := range part {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
				return false
			}
		}
	}
	return true
}
