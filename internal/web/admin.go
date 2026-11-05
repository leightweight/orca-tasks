package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/auth"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

// ResetLinkLifetime is how long a reset or invitation link works.
const ResetLinkLifetime = 24 * time.Hour

type usersPage struct {
	Users []db.ListUsersRow
}

func (w *Web) users(rw http.ResponseWriter, r *http.Request) {
	users, err := w.Store.ListUsers(r.Context())
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.render(rw, r, http.StatusOK, "admin_users", "Users", usersPage{Users: users})
}

// NewResetLink makes a link that sets a user's password and authenticator.
// The admin reset-link command uses it too.
func NewResetLink(ctx context.Context, s *store.Store, baseURL string, userID int64) (string, error) {
	token, hash := auth.NewToken("")

	err := s.CreateResetLink(ctx, db.CreateResetLinkParams{TokenHash: hash, PrincipalID: userID, ExpiresAt: time.Now().Add(ResetLinkLifetime)})
	if err != nil {
		return "", err
	}

	return strings.TrimRight(baseURL, "/") + "/reset/" + token, nil
}

func (w *Web) revealLink(r *http.Request, username, link string) {
	w.Sessions.Put(r.Context(), sessionReveal, reveal{
		Title: "Link for " + username,
		Note:  "Give this to " + username + ". It sets their password and authenticator, works once, and expires in 24 hours.",
		Lines: []string{link},
	})
}

func (w *Web) createUser(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	username, err := store.NormalizeUsername(r.PostFormValue("username"))
	if err != nil {
		w.redirect(rw, r, "/admin/users", capitalize(err.Error())+".")
		return
	}

	user, err := w.Store.CreateUser(ctx, db.CreateUserParams{
		Username:    username,
		DisplayName: cmpOr(strings.TrimSpace(r.PostFormValue("display_name")), username),
		Email:       strings.TrimSpace(r.PostFormValue("email")),
		SiteAdmin:   r.PostFormValue("admin") == "1",
	})
	if err != nil {
		if _, taken := w.Store.GetPrincipalByUsername(ctx, username); taken == nil {
			w.redirect(rw, r, "/admin/users", "That username is taken.")
			return
		}

		w.serverError(rw, r, err)

		return
	}

	link, err := NewResetLink(ctx, w.Store, w.BaseURL, user.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.revealLink(r, user.Username, link)
	http.Redirect(rw, r, "/admin/users", http.StatusSeeOther)
}

func (w *Web) changeUser(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := currentUser(ctx)

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		w.notFound(rw, r)
		return
	}

	user, err := w.Store.GetPrincipal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && user.Kind != "user") {
		w.notFound(rw, r)
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	action := r.PathValue("action")

	// Administrators can't lock themselves out.
	if user.ID == me.ID && action != "reset" {
		w.redirect(rw, r, "/admin/users", "You can't disable yourself or remove your own administration.")
		return
	}

	switch action {
	case "disable", "enable":
		err = w.Store.SetDisabled(ctx, db.SetDisabledParams{ID: user.ID, Disabled: action == "disable"})
	case "admin", "unadmin":
		err = w.Store.SetSiteAdmin(ctx, db.SetSiteAdminParams{ID: user.ID, SiteAdmin: action == "admin"})
	case "reset":
		link, err := NewResetLink(ctx, w.Store, w.BaseURL, user.ID)
		if err != nil {
			w.serverError(rw, r, err)
			return
		}

		w.revealLink(r, user.Username, link)
		http.Redirect(rw, r, "/admin/users", http.StatusSeeOther)

		return
	default:
		w.notFound(rw, r)
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, "/admin/users", "Saved the change to "+user.Username+".")
}

type allProjectsPage struct {
	Projects []db.ListAllProjectsRow
}

func (w *Web) allProjects(rw http.ResponseWriter, r *http.Request) {
	projects, err := w.Store.ListAllProjects(r.Context())
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.render(rw, r, http.StatusOK, "admin_projects", "All projects", allProjectsPage{Projects: projects})
}

func (w *Web) adminProject(rw http.ResponseWriter, r *http.Request) (db.Project, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		w.notFound(rw, r)
		return db.Project{}, false
	}

	project, err := w.Store.GetProject(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		w.notFound(rw, r)
		return project, false
	}

	if err != nil {
		w.serverError(rw, r, err)
		return project, false
	}

	return project, true
}

func (w *Web) adminAddMember(rw http.ResponseWriter, r *http.Request) {
	if project, ok := w.adminProject(rw, r); ok {
		w.addMemberTo(rw, r, project.ID, "/admin/projects")
	}
}

func (w *Web) adminDeleteProject(rw http.ResponseWriter, r *http.Request) {
	project, ok := w.adminProject(rw, r)
	if !ok {
		return
	}

	if !strings.EqualFold(strings.TrimSpace(r.PostFormValue("confirm")), project.Key) {
		w.redirect(rw, r, "/admin/projects", "Type the project's key to delete it.")
		return
	}

	if err := w.Store.DeleteProject(r.Context(), project.ID); err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, "/admin/projects", "Deleted "+project.Name+".")
}

func (w *Web) proxy(rw http.ResponseWriter, r *http.Request) {
	w.render(rw, r, http.StatusOK, "admin_proxy", "Reverse proxy check", w.Proxy(r))
}
