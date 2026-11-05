// Package testutil holds the helpers every test package shares: short
// per-step deadlines, an HTTP client with a timeout, and a fresh migrated
// database per test.
package testutil

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// StepTimeout is the deadline for one step of a test: a query, a request, or
// a command. Steps take milliseconds, so anything near this is a hang.
const StepTimeout = 5 * time.Second

// Context returns a context for one step of a test, canceled when the test
// ends.
func Context(t testing.TB) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), StepTimeout)
	t.Cleanup(cancel)

	return ctx
}

// Client is an HTTP client for tests. It doesn't follow redirects, so tests
// can check where a response sends the browser.
var Client = &http.Client{
	Timeout: StepTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}
