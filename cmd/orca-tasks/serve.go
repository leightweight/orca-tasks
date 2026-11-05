package main

import (
	"context"
	"fmt"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/leightweight/orca-tasks/internal/server"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/web"
)

type serveCmd struct {
	DatabaseURL    string `name:"database-url" env:"ORCA_TASKS_DATABASE_URL" required:"" placeholder:"URL" help:"Postgres connection URL."`
	BaseURL        string `name:"base-url" env:"ORCA_TASKS_BASE_URL" required:"" placeholder:"URL" help:"The URL people and Orca reach the server at."`
	SecretKey      string `env:"ORCA_TASKS_SECRET_KEY" required:"" placeholder:"BASE64" help:"32 random bytes in base64 that encrypt TOTP secrets. Keep a copy."`
	DataDir        string `env:"ORCA_TASKS_DATA_DIR" default:"/data" type:"path" help:"Directory for attachment contents."`
	MaxUpload      string `env:"ORCA_TASKS_MAX_UPLOAD" default:"25MiB" help:"Largest attachment accepted."`
	Addr           string `env:"ORCA_TASKS_ADDR" default:":8080" help:"Address to listen on."`
	TrustedProxies string `env:"ORCA_TASKS_TRUSTED_PROXIES" placeholder:"LIST" help:"Proxies to trust X-Forwarded-For from: IPs and CIDRs, or \"private\"."`
	LogLevel       string `env:"ORCA_TASKS_LOG_LEVEL" default:"info" enum:"debug,info,warn,error" help:"Log level."`
	LogFormat      string `env:"ORCA_TASKS_LOG_FORMAT" default:"json" enum:"json,text" help:"Log format."`

	SuppressCreatedDetail time.Duration `default:"5s" help:"Fail Orca's detail read of a new issue for this long, to keep its Tasks pane closable. 0 is off."`
}

func (c *serveCmd) Run(ctx context.Context) error {
	log, err := newLogger(c.LogLevel, c.LogFormat)
	if err != nil {
		return err
	}

	maxUpload, err := humanize.ParseBytes(c.MaxUpload)
	if err != nil {
		return usagef("reading the upload limit: %w", err)
	}

	app, err := server.New(ctx, server.Config{
		DatabaseURL:           c.DatabaseURL,
		BaseURL:               c.BaseURL,
		SecretKey:             c.SecretKey,
		DataDir:               c.DataDir,
		MaxUpload:             int64(min(maxUpload, 1<<40)),
		Addr:                  c.Addr,
		TrustedProxies:        c.TrustedProxies,
		SuppressCreatedDetail: c.SuppressCreatedDetail,
	}, log)
	if err != nil {
		return err
	}

	return app.Run(ctx, c.Addr)
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
