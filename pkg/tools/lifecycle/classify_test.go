package lifecycle_test

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/docker/docker-agent/pkg/tools/lifecycle"
)

func TestClassify_Nil(t *testing.T) {
	t.Parallel()
	assert.Check(t, is.Nil(lifecycle.Classify(nil)))
}

func TestClassify_ExecNotFound(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("start command: %w", exec.ErrNotFound)
	got := lifecycle.Classify(wrapped)
	assert.Check(t, errors.Is(got, lifecycle.ErrServerUnavailable))
	assert.Check(t, errors.Is(got, exec.ErrNotFound))
}

func TestClassify_FileNotExist(t *testing.T) {
	t.Parallel()
	got := lifecycle.Classify(fmt.Errorf("%w", os.ErrNotExist))
	assert.Check(t, errors.Is(got, lifecycle.ErrServerUnavailable))
}

func TestClassify_EOF(t *testing.T) {
	t.Parallel()
	got := lifecycle.Classify(io.EOF)
	assert.Check(t, errors.Is(got, lifecycle.ErrServerUnavailable))
}

func TestClassify_NetError(t *testing.T) {
	t.Parallel()
	// *net.OpError satisfies net.Error.
	netErr := &net.OpError{Op: "dial", Err: errors.New("refused")}
	got := lifecycle.Classify(netErr)
	assert.Check(t, errors.Is(got, lifecycle.ErrTransport))
}

func TestClassify_InitNotification(t *testing.T) {
	t.Parallel()
	got := lifecycle.Classify(errors.New("failed to send initialized notification: bad"))
	assert.Check(t, errors.Is(got, lifecycle.ErrInitNotification))
}

func TestClassify_SessionMissingByMessage(t *testing.T) {
	t.Parallel()
	got := lifecycle.Classify(errors.New("session not found"))
	assert.Check(t, errors.Is(got, lifecycle.ErrSessionMissing))
}

func TestClassify_TransportByMessage(t *testing.T) {
	t.Parallel()
	cases := []string{
		"connection reset by peer",
		"connection refused",
		"write tcp ...: broken pipe",
		"unexpected EOF",
		// The MCP SDK's synthesized response error for a call whose SSE
		// stream ended without a response and without event IDs to resume
		// from. Must classify as transport so callTool's reconnect-and-retry
		// path picks it up; before this pattern it read as an application
		// error and every post-handshake stream termination failed the tool
		// call outright.
		`calling "tools/call": request terminated without response`,
		// A transient HTTP status from the server or an intermediary. The SDK
		// keeps the connection usable for exactly this reason, but surfaces the
		// status as bare http.StatusText, so without these patterns a gateway
		// that is cold-starting or shedding load permanently drops the toolset
		// for the session.
		`rejected by transport: sending "tools/list": Gateway Timeout`,
		`rejected by transport: sending "tools/call": Service Unavailable`,
		`rejected by transport: sending "tools/list": Bad Gateway`,
		`rejected by transport: sending "tools/call": Too Many Requests`,
		`rejected by transport: sending "initialize": Internal Server Error`,
	}
	for _, msg := range cases {
		got := lifecycle.Classify(errors.New(msg))
		assert.Check(t, errors.Is(got, lifecycle.ErrTransport), "msg=%q", msg)
	}
}

// TestClassify_NonTransientHTTPStatusUnchanged keeps the status matching from
// swallowing failures a retry cannot fix: a permanent 4xx must stay unclassified
// so the caller reports it instead of reconnecting into the same answer.
func TestClassify_NonTransientHTTPStatusUnchanged(t *testing.T) {
	t.Parallel()
	cases := []string{
		`rejected by transport: sending "tools/list": Forbidden`,
		`rejected by transport: sending "tools/list": Not Implemented`,
		`sending "tools/list": Bad Request`,
	}
	for _, msg := range cases {
		in := errors.New(msg)
		got := lifecycle.Classify(in)
		assert.Check(t, got == in, "msg=%q", msg)
		assert.Check(t, !lifecycle.IsTransient(got), "msg=%q", msg)
	}
}

func TestClassify_AlreadyClassifiedPasses(t *testing.T) {
	t.Parallel()
	in := fmt.Errorf("wrapped: %w", lifecycle.ErrAuthRequired)
	got := lifecycle.Classify(in)
	assert.Check(t, errors.Is(got, lifecycle.ErrAuthRequired))
}

func TestClassify_InvalidToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		msg  string
	}{
		{"rfc6750_error_code", `401 Unauthorized: {"error":"invalid_token","error_description":"Invalid access token"}`},
		{"space_variant", "server rejected token: invalid token"},
		{"upper_case", "INVALID_TOKEN: token expired"},
	}
	for _, tc := range cases {
		got := lifecycle.Classify(errors.New(tc.msg))
		assert.Check(t, errors.Is(got, lifecycle.ErrAuthRequired), "msg=%q", tc.msg)
		assert.Check(t, lifecycle.IsPermanent(got), "msg=%q: must be permanent", tc.msg)
	}
}

func TestClassify_BareUnauthorizedIsNotAuth(t *testing.T) {
	t.Parallel()
	// A bare "unauthorized" without "invalid_token" must NOT be classified as
	// ErrAuthRequired to avoid misreading application-level 401s as permanent
	// auth failures (see human decision Q3 in the implementation plan).
	got := lifecycle.Classify(errors.New("401 Unauthorized"))
	assert.Check(t, !errors.Is(got, lifecycle.ErrAuthRequired), "bare unauthorized must not map to ErrAuthRequired")
}

func TestClassify_InvalidToken_Idempotent(t *testing.T) {
	t.Parallel()
	// Classify must be idempotent: an already-wrapped ErrAuthRequired that
	// also contains "invalid_token" in its message must not be double-wrapped.
	inner := errors.New("invalid_token: expired")
	first := lifecycle.Classify(inner)
	second := lifecycle.Classify(first)
	assert.Check(t, errors.Is(second, lifecycle.ErrAuthRequired))
	assert.Check(t, errors.Is(second, inner))
}

func TestClassify_UnknownPassthrough(t *testing.T) {
	t.Parallel()
	in := errors.New("totally unrelated")
	got := lifecycle.Classify(in)
	assert.Check(t, is.Equal(got, in))
}

func TestClassified_ErrorMessageIncludesBoth(t *testing.T) {
	t.Parallel()
	got := lifecycle.Classify(io.EOF)
	assert.Check(t, is.Contains(got.Error(), "server unavailable"))
	assert.Check(t, is.Contains(got.Error(), "EOF"))
}

func TestIsTransient(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"transport", lifecycle.ErrTransport, true},
		{"unavailable", lifecycle.ErrServerUnavailable, true},
		{"crashed", lifecycle.ErrServerCrashed, true},
		{"init-timeout", lifecycle.ErrInitTimeout, true},
		{"init-notif", lifecycle.ErrInitNotification, true},
		{"session", lifecycle.ErrSessionMissing, true},
		{"capability", lifecycle.ErrCapabilityMissing, false},
		{"auth", lifecycle.ErrAuthRequired, false},
		{"nil", nil, false},
		{"unknown", errors.New("x"), false},
	}
	for _, tc := range cases {
		got := lifecycle.IsTransient(tc.err)
		assert.Check(t, is.Equal(got, tc.want), "case=%s", tc.name)
	}
}

func TestIsPermanent(t *testing.T) {
	t.Parallel()
	assert.Check(t, lifecycle.IsPermanent(lifecycle.ErrAuthRequired))
	assert.Check(t, lifecycle.IsPermanent(lifecycle.ErrCapabilityMissing))
	assert.Check(t, !lifecycle.IsPermanent(lifecycle.ErrTransport))
	assert.Check(t, !lifecycle.IsPermanent(nil))
}
