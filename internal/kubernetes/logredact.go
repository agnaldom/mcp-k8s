package kubernetes

import "regexp"

// Log redaction patterns (spec §6.2). This is DEFENSE IN DEPTH, not a
// control: application log text is arbitrary, the false-negative rate is
// high and unmeasurable, and the real control is RBAC on pods/log. These
// patterns reduce damage when a log was already read — they do not
// authorize reading it.
var logRedactors = []struct {
	pattern *regexp.Regexp
	repl    string
}{
	// Authorization: Bearer <token>
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`), "${1}[REDACTED]"},
	// JWTs (three base64url segments)
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`), "[REDACTED:jwt]"},
	// PEM private keys
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[^-]*-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED:private-key]"},
	// Connection strings with passwords
	{regexp.MustCompile(`(?i)(password|pwd)=([^;\s]+)`), "${1}=[REDACTED]"},
	// AWS access key ids
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), "[REDACTED:aws-access-key]"},
}

// RedactLogContent applies best-effort secret redaction to log text
// (spec §6.2). See the package-level warning: this is not a control.
func RedactLogContent(content []byte) []byte {
	out := content
	for _, r := range logRedactors {
		out = r.pattern.ReplaceAll(out, []byte(r.repl))
	}
	return out
}
