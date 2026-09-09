package tools

import (
	"fmt"
	"regexp"
)

// namePattern enforces the contract naming rule (spec §3.1): underscore
// separation, k8s_ prefix, lowercase — the intersection of what OpenAI
// function names and MCP tolerate.
var namePattern = regexp.MustCompile(`^k8s_[a-z0-9_]+$`)

// ValidName reports whether name satisfies the tool naming contract.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// mustValidName returns name or panics. Registration-time validation: an
// invalid tool name is a programming error, not a runtime condition.
func mustValidName(name string) string {
	if !ValidName(name) {
		panic(fmt.Sprintf("invalid tool name %q: must match %s (spec §3.1)", name, namePattern))
	}
	return name
}
