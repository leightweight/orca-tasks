package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLogsLeaveOutTokens(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /reset/{token}", func(http.ResponseWriter, *http.Request) {})

	var logs bytes.Buffer

	handler := logRequests(slog.New(slog.NewTextHandler(&logs, nil)), func(*http.Request) string { return "192.0.2.1" }, mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), "GET", "/reset/abcdefsecret", nil))

	if strings.Contains(logs.String(), "abcdefsecret") || !strings.Contains(logs.String(), "/reset/{token}") {
		t.Errorf("log = %q, want the route without the token", logs.String())
	}
}
