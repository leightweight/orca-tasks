// Package web is the web interface: first-run setup, sign-in with TOTP,
// account, API key, agent, and project management, the site administration
// pages, and the read-only issue page Orca links to.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/dustin/go-humanize"
	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/auth"
	"github.com/leightweight/orca-tasks/internal/jira"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

var (
	//go:embed templates/*.html
	templateFiles embed.FS

	//go:embed static
	staticFiles embed.FS
)

// Session keys.
const (
	sessionUser    = "user"
	sessionVersion = "version"
	sessionPending = "pending"

	sessionPendingVersion = "pending_version"
	sessionPendingUntil   = "pending_until"
	sessionEnroll         = "enroll"
	sessionFlash          = "flash"
	sessionFlashError     = "flash_error"
	sessionReveal         = "reveal"
	sessionNext           = "next"
)

// Limiter counts failed attempts; see server.Limiter.
type Limiter = jira.Limiter

// Web is the web interface.
type Web struct {
	Store     *store.Store
	Catalog   *store.Catalog
	Box       *auth.SecretBox
	BaseURL   string
	Limiter   Limiter
	ClientIP  func(*http.Request) string
	Log       *slog.Logger
	Now       func() time.Time
	Sessions  *scs.SessionManager
	SetupCode string

	// LocalAgent makes setup also create an agent named "agent", which the local
	// socket acts as.
	LocalAgent bool

	// PasswordOnly signs people in with a password alone, with no TOTP or
	// recovery codes. Only local mode sets it, and local mode only listens on
	// loopback, where a leaked password can't be used from another machine.
	PasswordOnly bool

	// Proxy describes how the server resolved a request's client address, for
	// the administration page that checks it.
	Proxy func(*http.Request) ProxyInfo

	pages map[string]*template.Template
}

// ProxyInfo is what /admin/proxy shows.
type ProxyInfo struct {
	Peer        string
	PeerTrusted bool
	Client      string
	Trusted     []string
	Headers     []Header
}

// Header is one forwarding header and its value.
type Header struct{ Name, Value string }

// NewSessions returns a session manager storing sessions in Postgres. secure
// marks the cookie Secure, for an HTTPS base URL.
func NewSessions(s *store.Store, secure bool) *scs.SessionManager {
	sessions := scs.New()
	sessions.Store = pgxstore.NewWithCleanupInterval(s.Pool, time.Hour)
	sessions.Lifetime = 30 * 24 * time.Hour
	sessions.IdleTimeout = 7 * 24 * time.Hour
	sessions.Cookie.Name = "orca_tasks_session"
	sessions.Cookie.HttpOnly = true
	sessions.Cookie.SameSite = http.SameSiteLaxMode
	sessions.Cookie.Secure = secure

	return sessions
}

// Register adds the web interface's routes to a mux.
func (w *Web) Register(mux *http.ServeMux) error {
	if err := w.parseTemplates(); err != nil {
		return err
	}

	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return err
	}

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	protect := http.NewCrossOriginProtection()

	// Browsers that send Sec-Fetch-Site pass on that alone. Older ones are
	// checked by Origin against Host, which a proxy may rewrite, so the base
	// URL's origin is trusted outright.
	if err := protect.AddTrustedOrigin(origin(w.BaseURL)); err != nil {
		return err
	}

	page := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, w.Sessions.LoadAndSave(protect.Handler(w.headers(w.loadUser(h)))))
	}

	page("GET /{$}", w.home)
	page("GET /setup", w.setupForm)
	page("POST /setup", w.setup)
	page("GET /enroll", w.enrollForm)
	page("POST /enroll", w.enroll)
	page("GET /reset/{token}", w.resetForm)
	page("POST /reset/{token}", w.reset)
	page("GET /login", w.loginForm)
	page("POST /login", w.login)
	page("GET /login/totp", w.loginTOTPForm)
	page("POST /login/totp", w.loginTOTP)
	page("POST /logout", w.logout)

	page("GET /account", w.signedIn(w.account))
	page("POST /account/profile", w.signedIn(w.updateProfile))
	page("POST /account/password", w.signedIn(w.changePassword))
	page("POST /account/recovery-codes", w.signedIn(w.newRecoveryCodes))
	page("GET /account/keys", w.signedIn(w.keys))
	page("POST /account/keys", w.signedIn(w.createKey))
	page("POST /account/keys/{id}/revoke", w.signedIn(w.revokeKey))
	page("GET /account/agents", w.signedIn(w.agents))
	page("POST /account/agents", w.signedIn(w.createAgent))
	page("POST /account/agents/{id}/{action}", w.signedIn(w.changeAgent))

	page("GET /projects", w.signedIn(w.projects))
	page("POST /projects", w.signedIn(w.createProject))
	page("GET /projects/{key}", w.signedIn(w.project))
	page("POST /projects/{key}/key", w.signedIn(w.changeKey))
	page("POST /projects/{key}/rename", w.signedIn(w.renameProject))
	page("POST /projects/{key}/members", w.signedIn(w.addMember))
	page("POST /projects/{key}/members/{username}/{action}", w.signedIn(w.changeMember))
	page("POST /projects/{key}/delete", w.signedIn(w.deleteProject))

	page("GET /admin/users", w.admin(w.users))
	page("POST /admin/users", w.admin(w.createUser))
	page("POST /admin/users/{id}/{action}", w.admin(w.changeUser))
	page("GET /admin/projects", w.admin(w.allProjects))
	page("POST /admin/projects/{id}/members", w.admin(w.adminAddMember))
	page("POST /admin/projects/{id}/delete", w.admin(w.adminDeleteProject))
	page("GET /admin/proxy", w.admin(w.proxy))

	page("GET /browse/{key}", w.signedIn(w.browse))

	return nil
}

// origin is a URL's scheme and host.
func origin(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}

	return u.Scheme + "://" + u.Host
}

func (w *Web) parseTemplates() error {
	funcs := template.FuncMap{
		"join": strings.Join,
		// current reports whether a page's path is one a navigation link covers.
		"current": func(path string, prefixes ...string) bool {
			for _, prefix := range prefixes {
				if path == prefix || strings.HasPrefix(path, prefix+"/") {
					return true
				}
			}

			return false
		},
		"bytes": func(n int64) string { return humanize.IBytes(uint64(max(n, 0))) },
		"ago": func(t any) string {
			switch t := t.(type) {
			case time.Time:
				return humanize.Time(t)
			case *time.Time:
				if t != nil {
					return humanize.Time(*t)
				}
			}

			return ""
		},
	}

	layout, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFiles, "templates/layout.html")
	if err != nil {
		return err
	}

	names, err := fs.Glob(templateFiles, "templates/*.html")
	if err != nil {
		return err
	}

	w.pages = map[string]*template.Template{}

	for _, name := range names {
		if name == "templates/layout.html" {
			continue
		}

		page, err := template.Must(layout.Clone()).ParseFS(templateFiles, name)
		if err != nil {
			return err
		}

		w.pages[strings.TrimSuffix(strings.TrimPrefix(name, "templates/"), ".html")] = page
	}

	return nil
}

// view is what every page template gets.
type view struct {
	Title string

	// Path is the page's URL path, for marking the current page in the
	// navigation.
	Path string

	// PasswordOnly hides the TOTP fields, which a password-only server doesn't
	// ask for.
	PasswordOnly bool
	User         *db.Principal
	BaseURL      string
	Flash        string
	Error        string
	Reveal       *reveal
	Data         any
}

// putSealed stores a value in the session encrypted with the secret key. The
// session store keeps its data in Postgres as it is, and these values are
// secrets: a TOTP secret during enrollment, recovery codes, a new API key, or
// a reset link waiting to be shown.
func (w *Web) putSealed(ctx context.Context, key string, value any) {
	// The values are plain structs, which always encode.
	data, _ := json.Marshal(value)
	w.Sessions.Put(ctx, key, w.Box.Seal(data))
}

// getSealed reads a value putSealed stored, reporting whether there was one.
func (w *Web) getSealed(ctx context.Context, key string, into any) bool {
	sealed, ok := w.Sessions.Get(ctx, key).([]byte)
	if !ok {
		return false
	}

	data, err := w.Box.Open(sealed)

	return err == nil && json.Unmarshal(data, into) == nil
}

// reveal is a secret shown once, after the redirect that follows creating it.
type reveal struct {
	Title string
	Note  string
	Lines []string
}

func (w *Web) render(rw http.ResponseWriter, r *http.Request, status int, name, title string, data any) {
	ctx := r.Context()

	v := view{
		Title:        title,
		Path:         r.URL.Path,
		PasswordOnly: w.PasswordOnly,
		User:         currentUser(ctx),
		BaseURL:      w.BaseURL,
		Flash:        w.Sessions.PopString(ctx, sessionFlash),
		Error:        w.Sessions.PopString(ctx, sessionFlashError),
		Data:         data,
	}

	var shown reveal
	if w.getSealed(ctx, sessionReveal, &shown) {
		w.Sessions.Remove(ctx, sessionReveal)
		v.Reveal = &shown
	}

	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.WriteHeader(status)

	if err := w.pages[name].ExecuteTemplate(rw, "layout.html", v); err != nil {
		w.Log.Error("rendering a page", "page", name, "error", err)
	}
}

// fail shows a page with an error and the form again.
func (w *Web) fail(rw http.ResponseWriter, r *http.Request, name, title, message string, data any) {
	ctx := r.Context()

	v := view{Title: title, Path: r.URL.Path, User: currentUser(ctx), BaseURL: w.BaseURL, Error: message, Data: data, PasswordOnly: w.PasswordOnly}

	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.WriteHeader(http.StatusBadRequest)

	if err := w.pages[name].ExecuteTemplate(rw, "layout.html", v); err != nil {
		w.Log.Error("rendering a page", "page", name, "error", err)
	}
}

// redirect sends the browser elsewhere after a form, with a message to show
// there.
func (w *Web) redirect(rw http.ResponseWriter, r *http.Request, to, flash string) {
	if flash != "" {
		w.Sessions.Put(r.Context(), sessionFlash, flash)
	}

	http.Redirect(rw, r, to, http.StatusSeeOther)
}

// redirectError sends the browser elsewhere after a form that failed, with
// the problem to show there as an error.
func (w *Web) redirectError(rw http.ResponseWriter, r *http.Request, to, problem string) {
	w.Sessions.Put(r.Context(), sessionFlashError, problem)
	http.Redirect(rw, r, to, http.StatusSeeOther)
}

// serverError logs an error and shows a generic page.
func (w *Web) serverError(rw http.ResponseWriter, r *http.Request, err error) {
	w.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	w.render(rw, r, http.StatusInternalServerError, "message", "Something went wrong", "The server failed to handle the request. The error is in its log.")
}

func (w *Web) notFound(rw http.ResponseWriter, r *http.Request) {
	w.render(rw, r, http.StatusNotFound, "message", "Not found", "That page doesn't exist, or you can't see it.")
}

// headers adds the security headers every page gets.
func (w *Web) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		h := rw.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; frame-ancestors 'none'; form-action 'self'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")

		next.ServeHTTP(rw, r)
	})
}

type userKey struct{}

func currentUser(ctx context.Context) *db.Principal {
	user, _ := ctx.Value(userKey{}).(*db.Principal)
	return user
}

// loadUser puts the signed-in user in the request's context. A session for a
// disabled user, or one from before a password change, ends here.
func (w *Web) loadUser(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		id := w.Sessions.GetInt64(ctx, sessionUser)
		if id == 0 {
			next(rw, r)
			return
		}

		user, err := w.Store.GetPrincipal(ctx, id)

		// A database error isn't a reason to sign someone out; only a missing
		// or disabled account, or a password change since, ends the session.
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			w.serverError(rw, r, err)
			return
		}

		if err != nil || user.DisabledAt != nil || int64(user.SessionVersion) != w.Sessions.GetInt64(ctx, sessionVersion) {
			if err := w.Sessions.Destroy(ctx); err != nil {
				w.serverError(rw, r, err)
				return
			}

			next(rw, r)

			return
		}

		next(rw, r.WithContext(context.WithValue(ctx, userKey{}, &user)))
	}
}

// signedIn sends a visitor who isn't signed in to the sign-in page, or to
// setup when there are no users yet.
func (w *Web) signedIn(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if currentUser(r.Context()) == nil {
			w.toSignIn(rw, r)
			return
		}

		next(rw, r)
	}
}

func (w *Web) toSignIn(rw http.ResponseWriter, r *http.Request) {
	hasUsers, err := w.Store.HasUsers(r.Context())
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	if !hasUsers {
		http.Redirect(rw, r, "/setup", http.StatusSeeOther)
		return
	}

	// The page to return to stays in the session, not the URL, so a link can't
	// send someone elsewhere after they sign in.
	if r.Method == http.MethodGet {
		w.Sessions.Put(r.Context(), sessionNext, r.URL.Path)
	}

	http.Redirect(rw, r, "/login", http.StatusSeeOther)
}

// admin lets only site administrators through.
func (w *Web) admin(next http.HandlerFunc) http.HandlerFunc {
	return w.signedIn(func(rw http.ResponseWriter, r *http.Request) {
		if !currentUser(r.Context()).SiteAdmin {
			w.notFound(rw, r)
			return
		}

		next(rw, r)
	})
}

func (w *Web) home(rw http.ResponseWriter, r *http.Request) {
	if currentUser(r.Context()) == nil {
		w.toSignIn(rw, r)
		return
	}

	http.Redirect(rw, r, "/projects", http.StatusSeeOther)
}

// SessionCaller is the signed-in user of a request, for attachment downloads.
// It runs outside the session middleware, so it loads the session itself.
func (w *Web) SessionCaller(r *http.Request) *jira.Caller {
	cookie, err := r.Cookie(w.Sessions.Cookie.Name)
	if err != nil {
		return nil
	}

	ctx, err := w.Sessions.Load(r.Context(), cookie.Value)
	if err != nil {
		return nil
	}

	id := w.Sessions.GetInt64(ctx, sessionUser)
	if id == 0 {
		return nil
	}

	user, err := w.Store.GetPrincipal(ctx, id)
	if err != nil || user.DisabledAt != nil || int64(user.SessionVersion) != w.Sessions.GetInt64(ctx, sessionVersion) {
		return nil
	}

	return &jira.Caller{PrincipalID: user.ID, Username: user.Username, DisplayName: user.DisplayName, Email: user.Email, User: user.Username}
}

// signIn starts a session for a user who has passed both factors.
func (w *Web) signIn(ctx context.Context, user db.Principal) error {
	if err := w.Sessions.RenewToken(ctx); err != nil {
		return err
	}

	w.Sessions.Remove(ctx, sessionPending)
	w.Sessions.Remove(ctx, sessionPendingVersion)
	w.Sessions.Remove(ctx, sessionPendingUntil)
	w.Sessions.Remove(ctx, sessionEnroll)
	w.Sessions.Put(ctx, sessionUser, user.ID)
	w.Sessions.Put(ctx, sessionVersion, int64(user.SessionVersion))

	return w.Store.RecordSignIn(ctx, user.ID)
}

var errTooManyAttempts = errors.New("too many failed attempts; wait a few minutes and try again")
