// Package server runs orca-tasks: the Jira-compatible API, the web interface,
// health checks, and the attachment cleanup, behind one HTTP server.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/realclientip/realclientip-go"

	"github.com/leightweight/orca-tasks/internal/attachments"
	"github.com/leightweight/orca-tasks/internal/auth"
	"github.com/leightweight/orca-tasks/internal/jira"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/web"
)

// Config is everything the server reads from its flags and environment.
type Config struct {
	DatabaseURL           string
	BaseURL               string
	SecretKey             string
	DataDir               string
	MaxUpload             int64
	Addr                  string
	TrustedProxies        string
	SuppressCreatedDetail time.Duration

	// Socket, when set, is local mode: setup also creates an agent named
	// "agent", and Run serves the API on this Unix socket too, acting as that
	// agent without an API key.
	Socket string

	// PasswordOnly signs people in with a password alone; see
	// web.Web.PasswordOnly. Only local mode sets it.
	PasswordOnly bool

	// Now is the clock, for tests. Nil means time.Now.
	Now func() time.Time
}

// App is a configured server, before it listens.
type App struct {
	Handler http.Handler

	// SetupCode is the code first-run setup asks for, while there are no users.
	SetupCode string

	Store *store.Store
	Files *attachments.Dir
	Log   *slog.Logger

	// Socket is the local mode socket, or empty.
	Socket string

	limiter *Limiter
}

// New connects to the database and builds the server.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	s, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}

	app, err := Build(ctx, s, cfg, log)
	if err != nil {
		s.Close()
		return nil, err
	}

	return app, nil
}

// Build migrates a database and builds the server on it.
func Build(ctx context.Context, s *store.Store, cfg Config, log *slog.Logger) (*App, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("the base URL %q must be an http or https URL", cfg.BaseURL)
	}

	baseURL := strings.TrimRight(cfg.BaseURL, "/")

	box, err := auth.ParseSecretKey(cfg.SecretKey)
	if err != nil {
		return nil, err
	}

	clientIPs, err := NewClientIPs(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}

	files, err := attachments.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	if err := s.Migrate(ctx); err != nil {
		return nil, err
	}

	catalog, err := s.LoadCatalog(ctx)
	if err != nil {
		return nil, err
	}

	limiter, err := NewLimiter()
	if err != nil {
		return nil, err
	}

	sessions := web.NewSessions(s, base.Scheme == "https")

	w := &web.Web{
		Store:        s,
		Catalog:      catalog,
		Box:          box,
		BaseURL:      baseURL,
		Limiter:      limiter,
		ClientIP:     clientIPs.Of,
		Log:          log,
		Now:          now,
		Sessions:     sessions,
		Proxy:        clientIPs.describe,
		LocalAgent:   cfg.Socket != "",
		PasswordOnly: cfg.PasswordOnly,
	}

	hasUsers, err := s.HasUsers(ctx)
	if err != nil {
		return nil, err
	}

	if !hasUsers {
		token, _ := auth.NewToken("")
		w.SetupCode = token[:12]

		log.Warn("there are no users yet; open the setup page and enter the setup code", "url", baseURL+"/setup", "setup_code", w.SetupCode)
	}

	api := &jira.Server{
		Store:         s,
		Catalog:       catalog,
		Files:         files,
		BaseURL:       baseURL,
		MaxUpload:     cfg.MaxUpload,
		Log:           log,
		Limiter:       limiter,
		ClientIP:      clientIPs.Of,
		SessionCaller: w.SessionCaller,
		Now:           now,
	}
	api.SuppressCreatedDetail(cfg.SuppressCreatedDetail)

	if cfg.Socket != "" {
		api.LocalCaller = localCaller(s, baseURL)
	}

	mux := http.NewServeMux()
	api.Register(mux)

	if err := w.Register(mux); err != nil {
		return nil, err
	}

	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte("ok\n"))
	})

	mux.HandleFunc("GET /readyz", func(rw http.ResponseWriter, r *http.Request) {
		if err := s.Pool.Ping(r.Context()); err != nil {
			http.Error(rw, "database: "+err.Error(), http.StatusServiceUnavailable)
			return
		}

		if err := files.Writable(); err != nil {
			http.Error(rw, "data directory: "+err.Error(), http.StatusServiceUnavailable)
			return
		}

		_, _ = rw.Write([]byte("ok\n"))
	})

	return &App{
		Handler:   logRequests(log, clientIPs.Of, mux),
		SetupCode: w.SetupCode,
		Socket:    cfg.Socket,
		Store:     s,
		Files:     files,
		Log:       log,
		limiter:   limiter,
	}, nil
}

// Run serves until the context ends, then shuts down gracefully.
func (a *App) Run(ctx context.Context, addr string) error {
	defer a.Store.Close()

	go a.sweep(ctx)

	server := &http.Server{
		Addr:              addr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	failed := make(chan error, 2)

	go func() {
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()

	var socket *http.Server

	if a.Socket != "" {
		listener, s, err := a.ListenSocket(ctx, a.Socket)
		if err != nil {
			return err
		}

		socket = s

		go func() {
			if err := socket.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
				failed <- err
			}
		}()

		a.Log.Info("agents on this machine can use the CLI without an API key", "socket", a.Socket)
	}

	a.Log.Info("serving", "addr", addr)

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}

	// The signal context is already canceled, so shutdown gets a fresh one.
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	_ = a.limiter.Close(shutdown)

	if socket != nil {
		_ = socket.Shutdown(shutdown)
		_ = os.Remove(a.Socket)
	}

	return server.Shutdown(shutdown)
}

// sweep removes orphaned attachment files at startup and then daily.
func (a *App) sweep(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		if err := a.Files.Sweep(ctx, a.Store.ExistingAttachments, a.Log); err != nil && ctx.Err() == nil {
			a.Log.Error("sweeping attachment files", "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// logRequests logs each request with its client address and how long it took.
func logRequests(log *slog.Logger, clientIP func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			return
		}

		// The matched route rather than the path, which can hold a secret: a
		// reset link's token takes over the account. ServeMux sets the
		// pattern on this request as it routes it.
		route := r.Pattern
		if route == "" {
			route = r.URL.Path
		}

		log.Info("request",
			"method", r.Method,
			"route", route,
			"status", recorder.status,
			"client", clientIP(r),
			"duration", time.Since(started).Round(time.Millisecond),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController reach the real writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// describe explains how a request's client address was resolved.
func (c *ClientIPs) describe(r *http.Request) web.ProxyInfo {
	peer := realclientip.RemoteAddrStrategy{}.ClientIP(nil, r.RemoteAddr)

	info := web.ProxyInfo{Peer: peer, PeerTrusted: c.IsTrusted(peer), Client: c.Of(r)}

	for _, n := range c.Trusted {
		info.Trusted = append(info.Trusted, n.String())
	}

	for _, name := range []string{"X-Forwarded-For", "X-Real-Ip", "Forwarded", "X-Forwarded-Proto", "X-Forwarded-Host"} {
		if value := r.Header.Get(name); value != "" {
			info.Headers = append(info.Headers, web.Header{Name: name, Value: value})
		}
	}

	return info
}
