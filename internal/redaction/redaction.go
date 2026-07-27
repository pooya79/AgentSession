// Package redaction removes common secrets from explicitly inspected retained
// evidence. It is deterministic and does not depend on external services.
package redaction

import (
	"fmt"
	"regexp"
)

// patterns is deliberately small and deterministic: it covers the secret
// forms promised by the retained-evidence inspection contract without trying
// to classify arbitrary session content.
var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)-----BEGIN [^-]*PRIVATE KEY-----.*?(?:-----END [^-]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`(?im)(authorization\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\r\n,}]+)`),
	regexp.MustCompile(`(?im)(["']?(?:api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|password|passwd|secret|token)["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,\r\n}]+)`),
	regexp.MustCompile(`\b(?:sk-ant-[A-Za-z0-9_-]{12,}|sk-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{20,})\b`),
}

// truncatedProviderToken catches a recognized token whose suffix may fall
// beyond the bounded raw-record prefix.
var truncatedProviderToken = regexp.MustCompile(`(?:sk-(?:ant-)?|ghp_|github_pat_|xox[baprs]-|AKIA|AIza)[A-Za-z0-9_-]{4,}$`)

// Text replaces recognized secret forms with numbered placeholders.
func Text(value string) (string, int) {
	count := 0
	for _, pattern := range patterns {
		value = pattern.ReplaceAllStringFunc(value, func(match string) string {
			count++
			prefix := ""
			if groups := pattern.FindStringSubmatch(match); len(groups) > 1 {
				prefix = groups[1]
			}
			return prefix + fmt.Sprintf("[REDACTED_%d]", count)
		})
	}
	return value, count
}

// TruncatedText additionally redacts a provider-token prefix cut by a bounded
// read. This prevents the visible prefix of a secret crossing the cap from
// being returned.
func TruncatedText(value string) (string, int) {
	value, count := Text(value)
	if truncatedProviderToken.MatchString(value) {
		value = truncatedProviderToken.ReplaceAllStringFunc(value, func(string) string {
			count++
			return fmt.Sprintf("[REDACTED_%d]", count)
		})
	}
	return value, count
}
