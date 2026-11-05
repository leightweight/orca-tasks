package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/leightweight/orca-tasks/internal/local"
	"github.com/leightweight/orca-tasks/internal/server"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/web"
)

type serveCmd struct {
	Local bool `help:"Run on this machine for one person, with an embedded Postgres, and let your agents use the CLI without a key."`

	DatabaseURL    string `name:"database-url" env:"ORCA_TASKS_DATABASE_URL" placeholder:"URL" help:"Postgres connection URL. Required without --local."`
	BaseURL        string `name:"base-url" env:"ORCA_TASKS_BASE_URL" placeholder:"URL" help:"The URL people and Orca reach the server at. Needed without --local."`
	SecretKey      string `env:"ORCA_TASKS_SECRET_KEY" placeholder:"BASE64" help:"32 random bytes in base64 that encrypt TOTP secrets. Required without --local."`
	DataDir        string `env:"ORCA_TASKS_DATA_DIR" type:"path" help:"Directory for attachment contents. Defaults to /data."`
	MaxUpload      string `env:"ORCA_TASKS_MAX_UPLOAD" default:"25MiB" help:"Largest attachment accepted."`
	Addr           string `env:"ORCA_TASKS_ADDR" help:"Address to listen on. Defaults to :8080, or 127.0.0.1:8080 with --local."`
	TrustedProxies string `env:"ORCA_TASKS_TRUSTED_PROXIES" placeholder:"LIST" help:"Proxies to trust X-Forwarded-For from: IPs and CIDRs, or \"private\"."`
	LogLevel       string `env:"ORCA_TASKS_LOG_LEVEL" default:"info" enum:"debug,info,warn,error" help:"Log level."`
	LogFormat      string `env:"ORCA_TASKS_LOG_FORMAT" help:"Log format, json or text. Defaults to json, or text with --local."`

	SuppressCreatedDetail time.Duration `default:"5s" help:"Fail Orca's detail read of a new issue for this long, to keep its Tasks pane closable. 0 is off."`
}

func (c *serveCmd) Run(ctx context.Context) error {
	format := cmpOr(c.LogFormat, "json")
	if c.Local {
		format = cmpOr(c.LogFormat, "text")
	}

	log, err := newLogger(c.LogLevel, format)
	if err != nil {
		return err
	}

	maxUpload, err := humanize.ParseBytes(c.MaxUpload)
	if err != nil {
		return usagef("reading the upload limit: %w", err)
	}

	cfg := server.Config{
		DatabaseURL:           c.DatabaseURL,
		BaseURL:               c.BaseURL,
		SecretKey:             c.SecretKey,
		DataDir:               cmpOr(c.DataDir, "/data"),
		MaxUpload:             int64(min(maxUpload, 1<<40)),
		Addr:                  cmpOr(c.Addr, ":8080"),
		TrustedProxies:        c.TrustedProxies,
		SuppressCreatedDetail: c.SuppressCreatedDetail,
	}

	if c.Local {
		stop, err := c.local(ctx, &cfg, log)
		if err != nil {
			return err
		}
		defer stop()
	}

	for flag, value := range map[string]string{"--database-url": cfg.DatabaseURL, "--base-url": cfg.BaseURL, "--secret-key": cfg.SecretKey} {
		if value == "" {
			return usagef("%s is required, unless you run with --local", flag)
		}
	}

	app, err := server.New(ctx, cfg, log)
	if err != nil {
		return err
	}

	if c.Local {
		log.Info("connect Orca in Settings, then Integrations, then Jira, with an API key from the API keys page",
			"site", cfg.BaseURL, "deployment", "Server / Data Center", "username", "(leave it empty)")
	}

	return app.Run(ctx, cfg.Addr)
}

// local fills in a local mode configuration: its own Postgres, attachments,
// secret key, and agent socket, in the XDG data, cache, and runtime directories.
func (c *serveCmd) local(ctx context.Context, cfg *server.Config, log *slog.Logger) (func(), error) {
	if c.DatabaseURL != "" {
		return nil, usagef("--local runs its own database, so leave out --database-url and ORCA_TASKS_DATABASE_URL")
	}

	cfg.Addr = cmpOr(c.Addr, "127.0.0.1:8080")

	// Local mode signs in with a password alone, which is only safe where no
	// other machine can reach the server.
	if !isLoopback(cfg.Addr) {
		return nil, usagef("--local only listens on this machine, so --addr must be a loopback address such as 127.0.0.1:8080, not %s", cfg.Addr)
	}

	db, err := local.OpenDatabase(ctx, log)
	if err != nil {
		return nil, err
	}

	stop := func() {
		if err := db.Close(); err != nil {
			log.Error("stopping Postgres", "error", err)
		}
	}

	if cfg.SecretKey == "" {
		if cfg.SecretKey, err = local.SecretKey(db.Paths.Data); err != nil {
			stop()
			return nil, err
		}
	}

	cfg.PasswordOnly = true
	cfg.BaseURL = cmpOr(c.BaseURL, "http://"+cfg.Addr)
	cfg.DataDir = cmpOr(c.DataDir, db.Paths.Attachments())
	cfg.Socket = db.Paths.Socket()

	if err := db.Paths.RecordSocket(); err != nil {
		stop()
		return nil, err
	}
	cfg.DatabaseURL = db.URL

	return stop, nil
}

// isLoopback reports whether an address listens only on this machine.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}

	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

func cmpOr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

type adminCmd struct {
	ResetLink resetLinkCmd `cmd:"" help:"Print a link that sets a user's password and authenticator, for an administrator who is locked out."`
	Import    importCmd    `cmd:"" help:"Import projects, issues, and comments from an export file."`
}

type resetLinkCmd struct {
	DatabaseURL string `name:"database-url" env:"ORCA_TASKS_DATABASE_URL" required:"" placeholder:"URL" help:"Postgres connection URL."`
	BaseURL     string `name:"base-url" env:"ORCA_TASKS_BASE_URL" required:"" placeholder:"URL" help:"The URL people reach the server at."`

	Username string `arg:"" help:"The user to reset."`
}

func (c *resetLinkCmd) Run(ctx context.Context) error {
	s, err := store.Open(ctx, c.DatabaseURL)
	if err != nil {
		return err
	}
	defer s.Close()

	user, err := s.GetPrincipalByUsername(ctx, c.Username)
	if err != nil || user.Kind != "user" {
		return usagef("there's no user named %q", c.Username)
	}

	link, err := web.NewResetLink(ctx, s, c.BaseURL, user.ID)
	if err != nil {
		return err
	}

	_, err = fmt.Println(link)

	return err
}
