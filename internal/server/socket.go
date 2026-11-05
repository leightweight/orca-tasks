package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/jira"
	"github.com/leightweight/orca-tasks/internal/store"
)

// localCaller is who requests through the local socket act as: the first
// user's agent named "agent".
func localCaller(s *store.Store, baseURL string) func(ctx context.Context) (*jira.Caller, error) {
	return func(ctx context.Context) (*jira.Caller, error) {
		row, err := s.LocalAgent(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, jira.Forbidden("There's no agent named \"agent\" yet. Finish setup at %s/setup, or create one on the Agents page.", baseURL)
		}

		if err != nil {
			return nil, err
		}

		a := row.Principal

		return &jira.Caller{PrincipalID: a.ID, Username: a.Username, DisplayName: a.DisplayName, Email: a.Email, User: row.OwnerUsername}, nil
	}
}

// ListenSocket listens on the local socket and returns a server for it whose
// connections the API trusts as the local agent. The socket is readable and
// writable only by its owner.
//
// A socket left by a server that's gone is removed; one that still answers
// belongs to another running server, so this fails instead of taking it.
func (a *App) ListenSocket(ctx context.Context, path string) (net.Listener, *http.Server, error) {
	if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path); err == nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("another orca-tasks is already serving %s", path)
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return nil, nil, err
	}

	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, nil, err
	}

	server := &http.Server{
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return jira.WithLocal(ctx)
		},
	}

	return listener, server, nil
}
