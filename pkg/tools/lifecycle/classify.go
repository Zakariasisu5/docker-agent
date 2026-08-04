package lifecycle

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// Classify maps a transport-level error (stdio MCP, remote MCP, LSP) to one
// of the typed sentinels in this package, wrapping it so errors.Is matches
// both the sentinel and the original error.
//
// Already-classified errors (any wrapping a sentinel via errors.Is) are
// returned unchanged. Unknown errors are returned as-is so callers can
// decide their own policy.
//
// Substring matching is used as a last resort because some upstream SDKs
// wrap their errors with %v (which drops the chain).
func Classify(err error) error {
	if err == nil {
		return nil
	}
	if isClassified(err) {
		return err
	}

	switch {
	case errors.Is(err, exec.ErrNotFound),
		errors.Is(err, os.ErrNotExist),
		errors.Is(err, io.EOF):
		return wrap(ErrServerUnavailable, err)
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return wrap(ErrTransport, err)
	}

	return classifyByMessage(err)
}

// isClassified reports whether err already wraps one of the package
// sentinels. Used to make Classify idempotent.
func isClassified(err error) bool {
	for _, s := range []error{
		ErrServerUnavailable, ErrServerCrashed, ErrInitTimeout,
		ErrInitNotification, ErrCapabilityMissing, ErrAuthRequired,
		ErrSessionMissing, ErrTransport,
	} {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

// classifyByMessage matches well-known substrings emitted by upstream SDKs
// that wrap errors with %v (dropping the chain). Returns err unchanged
// when no pattern matches.
func classifyByMessage(err error) error {
	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "failed to send initialized notification"):
		return wrap(ErrInitNotification, err)
	case strings.Contains(lower, "session missing"),
		strings.Contains(lower, "session not found"):
		return wrap(ErrSessionMissing, err)
	case strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "broken pipe"),
		// The MCP SDK's streamable client synthesizes this JSON-RPC response
		// error when the server (or an intermediary) ends a call's SSE stream
		// without delivering the response and the stream carries no event IDs
		// to resume from. It reaches the caller as a plain response error —
		// not ErrSessionMissing, not io.EOF — so without this pattern the
		// call is treated as an application failure and never retried, even
		// though a reconnect gives it a fresh stream that typically succeeds.
		strings.Contains(lower, "request terminated without response"),
		strings.Contains(msg, "EOF"),
		hasTransientHTTPStatus(msg):
		return wrap(ErrTransport, err)
	// Map server-side OAuth token rejection to ErrAuthRequired. We match
	// "invalid_token" (RFC 6750 §3.1 canonical error code) and its space-
	// separated variant. We deliberately do NOT match bare "unauthorized"
	// here to avoid classifying application-level 401s (unrelated to OAuth)
	// as permanent auth failures; the token-was-attached gating in
	// oauthTransport.roundTrip is the correct place for that check.
	case strings.Contains(lower, "invalid_token"),
		strings.Contains(lower, "invalid token"):
		return wrap(ErrAuthRequired, err)
	}
	return err
}

// transientHTTPStatusTexts are the status lines the MCP SDK's streamable
// client reports for the HTTP statuses it itself defines as transient. It
// wraps them as "rejected by transport" precisely so the connection stays
// usable and the request can be retried, but the status reaches the caller as
// nothing but http.StatusText in the message — so this is the only way to tell
// a busy or cold-starting server from an application error.
//
// Matched case-sensitively: these are canonical http.StatusText values, and
// folding case would classify ordinary prose containing the same words.
var transientHTTPStatusTexts = []string{
	http.StatusText(http.StatusInternalServerError),
	http.StatusText(http.StatusBadGateway),
	http.StatusText(http.StatusServiceUnavailable),
	http.StatusText(http.StatusGatewayTimeout),
	http.StatusText(http.StatusTooManyRequests),
}

// hasTransientHTTPStatus reports whether msg carries one of the transient HTTP
// status lines. A remote MCP server behind a gateway answers this way while it
// is cold-starting or shedding load; treating it as permanent costs the caller
// the whole toolset for the rest of the session over a condition that clears
// on its own.
func hasTransientHTTPStatus(msg string) bool {
	for _, status := range transientHTTPStatusTexts {
		if strings.Contains(msg, status) {
			return true
		}
	}
	return false
}

// IsTransient reports whether err wraps a sentinel that warrants a retry.
func IsTransient(err error) bool {
	for _, s := range []error{
		ErrTransport, ErrServerUnavailable, ErrServerCrashed,
		ErrInitTimeout, ErrInitNotification, ErrSessionMissing,
	} {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

// IsPermanent reports whether err wraps a sentinel that must NOT be retried
// (currently ErrCapabilityMissing and ErrAuthRequired).
func IsPermanent(err error) bool {
	return errors.Is(err, ErrCapabilityMissing) || errors.Is(err, ErrAuthRequired)
}

// wrap returns an error satisfying errors.Is for both sentinel and
// underlying, using Go 1.20+ multi-%w support.
func wrap(sentinel, underlying error) error {
	return fmt.Errorf("%w: %w", sentinel, underlying)
}
