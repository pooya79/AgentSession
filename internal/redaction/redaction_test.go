package redaction

import (
	"strings"
	"testing"
)

func TestTextRedactsSupportedSecretsDeterministically(t *testing.T) {
	input := "Authorization: Bearer abc\napi_key=secret-value\n" +
		"token sk-ant-abcdefghijklmnopqrstuvwxyz\n" +
		"-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----"
	got, count := Text(input)
	if count != 4 {
		t.Fatalf("Text() redactions = %d, want 4; output = %q", count, got)
	}
	if strings.Contains(got, "secret-value") || strings.Contains(got, "abcdefghijklmnopqrstuvwxyz") ||
		strings.Contains(got, "BEGIN PRIVATE") || !strings.Contains(got, "[REDACTED_4]") {
		t.Fatalf("Text() output = %q", got)
	}
	again, againCount := Text(input)
	if got != again || count != againCount {
		t.Fatalf("Text() is not deterministic: (%q, %d), (%q, %d)", got, count, again, againCount)
	}
}

func TestTextAvoidsUnrelatedWords(t *testing.T) {
	input := "tokenization is useful\nsecretary=value\npasswordless=true"
	got, count := Text(input)
	if got != input || count != 0 {
		t.Fatalf("Text() = (%q, %d), want unchanged", got, count)
	}
}

func TestTextRedactsUnterminatedPrivateKeyBlock(t *testing.T) {
	got, count := Text("prefix\n-----BEGIN OPENSSH PRIVATE KEY-----\npartial")
	if count != 1 || strings.Contains(got, "partial") {
		t.Fatalf("Text() = (%q, %d)", got, count)
	}
}

func TestTruncatedTextRedactsProviderTokenAcrossBoundary(t *testing.T) {
	got, count := TruncatedText("prefix sk-ant-secretpref")
	if count != 1 || strings.Contains(got, "secretpref") {
		t.Fatalf("TruncatedText() = (%q, %d)", got, count)
	}
	got, count = Text("prefix sk-short")
	if count != 0 || got != "prefix sk-short" {
		t.Fatalf("Text(short false positive) = (%q, %d)", got, count)
	}
}
