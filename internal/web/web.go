// Package web is the web interface: first-run setup, sign-in with TOTP,
// account, API key, agent, and project management, the site administration
// pages, and the read-only issue page Orca links to.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/leightweight/orca-tasks/internal/attachments"
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
	sessionProject        = "project"
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

	// Files holds attachment contents, and MaxUpload is the largest file a
	// task's page accepts.
	Files     *attachments.Dir
	MaxUpload int64

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
	page("GET /account/keys/new", w.signedIn(w.newKeyForm))
	page("POST /account/keys", w.signedIn(w.createKey))
	page("GET /account/keys/{id}/revoke", w.signedIn(w.confirmRevokeKey))
	page("POST /account/keys/{id}/revoke", w.signedIn(w.revokeKey))
	page("GET /account/agents", w.signedIn(w.agents))
	page("GET /account/agents/new", w.signedIn(w.newAgentForm))
	page("POST /account/agents", w.signedIn(w.createAgent))
	page("GET /account/agents/{id}/disable", w.signedIn(w.confirmDisableAgent))
	page("POST /account/agents/{id}/{action}", w.signedIn(w.changeAgent))

	page("GET /projects", w.signedIn(w.projects))
	page("GET /projects/new", w.signedIn(w.newProjectForm))
	page("POST /projects", w.signedIn(w.createProject))
	page("GET /projects/{key}", w.signedIn(w.project))
	page("GET /projects/{key}/tasks", w.signedIn(w.projectTasks))
	page("GET /projects/{key}/tasks/new", w.signedIn(w.newTaskForm))
	page("POST /projects/{key}/tasks", w.signedIn(w.createTask))
	page("GET /projects/{key}/members", w.signedIn(w.projectMembers))
	page("GET /projects/{key}/settings", w.signedIn(w.projectSettings))
	page("POST /projects/{key}/key", w.signedIn(w.changeKey))
	page("POST /projects/{key}/rename", w.signedIn(w.renameProject))
	page("GET /projects/{key}/members/new", w.signedIn(w.newMemberForm))
	page("POST /projects/{key}/members", w.signedIn(w.addMember))
	page("GET /projects/{key}/members/{username}/remove", w.signedIn(w.confirmRemoveMember))
	page("POST /projects/{key}/members/{username}/{action}", w.signedIn(w.changeMember))
	page("POST /projects/{key}/delete", w.signedIn(w.deleteProject))

	page("GET /admin/users", w.admin(w.users))
	page("GET /admin/users/new", w.admin(w.newUserForm))
	page("POST /admin/users", w.admin(w.createUser))
	page("GET /admin/users/{id}", w.admin(w.adminUser))
	page("GET /admin/users/{id}/{action}", w.admin(w.confirmUserChange))
	page("POST /admin/users/{id}/{action}", w.admin(w.changeUser))
	page("GET /admin/projects", w.admin(w.allProjects))
	page("GET /admin/projects/{id}", w.admin(w.adminProjectDetail))
	page("GET /admin/projects/{id}/members/new", w.admin(w.adminNewMemberForm))
	page("POST /admin/projects/{id}/members", w.admin(w.adminAddMember))
	page("POST /admin/projects/{id}/delete", w.admin(w.adminDeleteProject))
	page("GET /admin/proxy", w.admin(w.proxy))

	page("POST /preview", w.signedIn(w.preview))
	page("GET /browse/{key}", w.signedIn(w.browse))
	page("GET /browse/{key}/edit", w.signedIn(w.editTaskForm))
	page("POST /browse/{key}/edit", w.signedIn(w.updateTask))
	page("POST /browse/{key}/status", w.signedIn(w.moveTask))
	page("POST /browse/{key}/assignee", w.signedIn(w.assignTask))
	page("POST /browse/{key}/comments", w.signedIn(w.commentOnTask))
	page("POST /browse/{key}/attachments", w.signedIn(w.attachToTask))

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
		"join":      strings.Join,
		"hasPrefix": strings.HasPrefix,
		"formHas":   formHas,
		"list":      func(values ...string) []string { return values },
		// statusBadge gives a status what the "status" template reads.
		"statusBadge": func(s db.Status) issueLink { return issueLink{Status: s.Name, Category: s.Category} },
		// lastSeen is the later of a sign-in and an API key use, or nil for
		// neither. A key that was never used reports the epoch.
		"lastSeen": func(signIn *time.Time, keyUse time.Time) *time.Time {
			var last *time.Time
			if keyUse.Unix() != 0 {
				last = &keyUse
			}

			if signIn != nil && (last == nil || signIn.After(*last)) {
				last = signIn
			}

			return last
		},
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
		// ago shows a time relative to now, with the exact time on hover.
		"ago": func(t any) template.HTML {
			switch t := t.(type) {
			case time.Time:
				return timeElement(t)
			case *time.Time:
				if t != nil {
					return timeElement(*t)
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

// timeElement is a <time> that reads like "4 days ago" and shows the exact
// time in UTC as its tooltip. Pages have no JavaScript to find the viewer's
// time zone, so UTC is the one zone everyone can convert from.
func timeElement(t time.Time) template.HTML {
	t = t.UTC()

	return template.HTML(fmt.Sprintf(`<time datetime="%s" title="%s">%s</time>`,
		t.Format(time.RFC3339), t.Format("Mon, 2 Jan 2006 15:04 MST"), template.HTMLEscapeString(humanize.Time(t))))
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

	// Projects are the signed-in user's projects, for the sidebar's project
	// selector, and Project is the one the sidebar shows, if any.
	Projects []db.ListMyProjectsRow
	Project  *db.ListMyProjectsRow
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

	v := w.newView(r, title, data)
	v.Flash = w.Sessions.PopString(ctx, sessionFlash)
	v.Error = w.Sessions.PopString(ctx, sessionFlashError)

	var shown reveal
	if w.getSealed(ctx, sessionReveal, &shown) {
		w.Sessions.Remove(ctx, sessionReveal)
		v.Reveal = &shown
	}

	w.write(rw, status, name, v)
}

// fail shows a page with an error and the form again.
func (w *Web) fail(rw http.ResponseWriter, r *http.Request, name, title, message string, data any) {
	v := w.newView(r, title, data)
	v.Error = message

	w.write(rw, http.StatusBadRequest, name, v)
}

func (w *Web) newView(r *http.Request, title string, data any) view {
	v := view{
		Title:        title,
		Path:         r.URL.Path,
		PasswordOnly: w.PasswordOnly,
		User:         currentUser(r.Context()),
		BaseURL:      w.BaseURL,
		Data:         data,
	}

	if v.User != nil {
		w.loadNavigation(r, &v)
	}

	return v
}

// loadNavigation fills in the sidebar: the user's projects, and the one the
// page belongs to, or else the one they opened last.
func (w *Web) loadNavigation(r *http.Request, v *view) {
	ctx := r.Context()

	projects, err := w.Store.ListMyProjects(ctx, v.User.ID)
	if err != nil {
		// The page can still show without its sidebar.
		w.Log.Error("loading the sidebar's projects", "error", err)
		return
	}

	v.Projects = projects

	key := pageProjectKey(r.URL.Path)
	last := w.Sessions.GetInt64(ctx, sessionProject)

	for i, p := range projects {
		if key != "" && strings.EqualFold(p.Membership.Key, key) {
			v.Project = &projects[i]
			w.Sessions.Put(ctx, sessionProject, p.Project.ID)

			return
		}

		if key == "" && p.Project.ID == last {
			v.Project = &projects[i]
		}
	}
}

// pageProjectKey is the project key in a page's path, as in /projects/OPS/tasks
// or /browse/OPS-12, or empty for a page outside a project.
func pageProjectKey(path string) string {
	if rest, ok := strings.CutPrefix(path, "/projects/"); ok {
		// /projects/new is a page for no project yet.
		if key, _, _ := strings.Cut(rest, "/"); key != "new" {
			return key
		}

		return ""
	}

	if rest, ok := strings.CutPrefix(path, "/browse/"); ok {
		if i := strings.LastIndex(rest, "-"); i > 0 {
			return rest[:i]
		}
	}

	return ""
}

func (w *Web) write(rw http.ResponseWriter, status int, name string, v view) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.WriteHeader(status)

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

// home opens the project the user had open last, or their projects.
func (w *Web) home(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := currentUser(ctx)
	if user == nil {
		w.toSignIn(rw, r)
		return
	}

	if last := w.Sessions.GetInt64(ctx, sessionProject); last != 0 {
		membership, err := w.Store.GetMembership(ctx, db.GetMembershipParams{ProjectID: last, PrincipalID: user.ID})
		if err == nil {
			http.Redirect(rw, r, "/projects/"+membership.Key+"/tasks", http.StatusSeeOther)
			return
		}
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
