package raglit

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/iodesystems/agentkit/llm"
)

// Transient errors (Carl 2026-11-10, ragcore.md): an ingest that failed for a
// reason outside the document — a model or fetch endpoint down, slow, or
// refusing for load — is retried after a backoff. Anything else is the
// document's own failure and is final until its bytes change
// (failedsources.go).

// httpStatusError carries the HTTP status of a model or fetch endpoint's
// non-2xx answer, so IsTransient can judge it without parsing text. Its text
// is the wrapped error's, unchanged.
type httpStatusError struct {
	status int
	err    error
}

func (e *httpStatusError) Error() string { return e.err.Error() }
func (e *httpStatusError) Unwrap() error { return e.err }

// withStatus tags err with the HTTP status it came from.
func withStatus(status int, err error) error {
	return &httpStatusError{status: status, err: err}
}

func transientStatus(code int) bool { return code == 429 || code >= 500 && code <= 599 }

// agentkit's llm client reports a status in text only: "llm: status 503",
// "llm: tokenize <url>: status 502", "llm: retry budget 5m exhausted after 5m
// (last status 503)".
var llmStatusRE = regexp.MustCompile(`llm: [^\n]*?status (\d{3})\b`)

// IsTransient reports whether err is a failure outside the document: the
// context cancelled or timed out, a dial or connection error, an HTTP 5xx or
// 429 from a model or fetch endpoint, or a spent model budget (402, which
// refills). A document error (extract or parse failure, unsupported or
// unreadable content) is not transient.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var opErr *net.OpError
	var netErr net.Error
	if errors.As(err, &opErr) || errors.As(err, &netErr) {
		return true
	}
	var se *httpStatusError
	if errors.As(err, &se) && transientStatus(se.status) {
		return true
	}
	var spent *llm.BudgetSpentError
	if errors.As(err, &spent) {
		return true
	}
	// Text, for an error flattened with %v on its way here, and for agentkit,
	// which has no typed status error.
	msg := err.Error()
	if m := llmStatusRE.FindStringSubmatch(msg); m != nil {
		if code, _ := strconv.Atoi(m[1]); transientStatus(code) {
			return true
		}
	}
	for _, s := range []string{
		"llm: retry budget", // agentkit gave up on 5xx or transport failures
		"connection refused",
		"connection reset by peer",
		"i/o timeout",
		"context deadline exceeded",
		"context canceled",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
