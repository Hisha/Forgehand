package state

import (
	"regexp"
	"strings"
)

const (
	FailureKindProvider          = "provider"
	FailureKindRepositoryContext = "repository_context"
	FailureKindValidation        = "validation"
	FailureKindWorker            = "worker"

	// MaxFailureReasonLength keeps events and execution rows bounded.
	MaxFailureReasonLength = 2000
)

var (
	sensitiveAssignment = regexp.MustCompile(`(?i)(["']?(?:authorization|proxy-authorization|api[-_ ]?key|access[-_ ]?token|refresh[-_ ]?token|session[-_ ]?token|client[-_ ]?secret|private[-_ ]?key|secret[-_ ]?key|password|passwd|secret|credentials?)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	bearerCredential    = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	urlCredential       = regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^/\s@]+@`)
	openAIStyleKey      = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`)
)

func normalizeFailure(kind, reason string) (string, string) {
	switch kind {
	case FailureKindProvider, FailureKindRepositoryContext, FailureKindValidation, FailureKindWorker:
	default:
		kind = FailureKindWorker
	}

	reason = strings.TrimSpace(strings.ReplaceAll(reason, "\x00", ""))
	reason = bearerCredential.ReplaceAllString(reason, "Bearer [REDACTED]")
	reason = sensitiveAssignment.ReplaceAllString(reason, `${1}[REDACTED]`)
	reason = urlCredential.ReplaceAllString(reason, `${1}[REDACTED]@`)
	reason = openAIStyleKey.ReplaceAllString(reason, "[REDACTED]")
	if reason == "" {
		reason = "worker failed without a more specific diagnostic"
	}
	if len(reason) > MaxFailureReasonLength {
		reason = reason[:MaxFailureReasonLength] + "…"
	}
	return kind, reason
}
