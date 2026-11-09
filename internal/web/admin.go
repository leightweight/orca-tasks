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

type adminUserPage struct {
	db.ListUsersRow

	Memberships []db.ListMyProjectsRow
}

// adminUser shows one user's counts, projects, and the actions a site
// administrator can take on the account.
func (w *Web) adminUser(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		w.notFound(rw, r)
		return
	}

	users, err := w.Store.ListUsers(ctx)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	for _, u := range users {
		if u.Principal.ID != id {
			continue
		}

		memberships, err := w.Store.ListMyProjects(ctx, id)
		if err != nil {
			w.serverError(rw, r, err)
			return
		}

		w.render(rw, r, http.StatusOK, "admin_user", u.Principal.Username, adminUserPage{ListUsersRow: u, Memberships: memberships})

		return
	}

	w.notFound(rw, r)
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
	w.putSealed(r.Context(), sessionReveal, reveal{
		Title: "Link for " + username,
		Note:  "Give this to " + username + ". It sets their password and authenticator, works once, and expires in 24 hours.",
		Lines: []string{link},
	})
}

func (w *Web) createUser(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	again := func(problem string) {
		w.fail(rw, r, "user_new", "New user", problem, formPage{Form: r.PostForm})
	}

	username, err := store.NormalizeUsername(r.PostFormValue("username"))
	if err != nil {
		again(capitalize(err.Error()) + ".")
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
			again("That username is taken.")
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
	http.Redirect(rw, r, "/admin/users/"+strconv.FormatInt(user.ID, 10), http.StatusSeeOther)
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
	back := "/admin/users/" + strconv.FormatInt(user.ID, 10)

	// Administrators can't lock themselves out.
	if user.ID == me.ID && action != "reset" {
		w.redirectError(rw, r, back, "You can't disable yourself or remove your own administration.")
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
		http.Redirect(rw, r, back, http.StatusSeeOther)

		return
	default:
		w.notFound(rw, r)
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, back, "Saved the change to "+user.Username+".")
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

type adminProjectPage struct {
	Project db.Project
	Issues  int64
	Members []db.ListMembersRow
}

// adminProjectDetail shows a project's members and counts to a site
// administrator, with forms to add a member and delete the project. It shows
// no issues: administrators read those only as members.
func (w *Web) adminProjectDetail(rw http.ResponseWriter, r *http.Request) {
	project, ok := w.adminProject(rw, r)
	if !ok {
		return
	}

	members, err := w.Store.ListMembers(r.Context(), project.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	issues, err := w.Store.CountProjectIssues(r.Context(), project.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.render(rw, r, http.StatusOK, "admin_project", project.Name, adminProjectPage{Project: project, Issues: issues, Members: members})
}

func (w *Web) adminAddMember(rw http.ResponseWriter, r *http.Request) {
	if project, ok := w.adminProject(rw, r); ok {
		back := adminProjectPath(project)
		w.addMemberTo(rw, r, formPage{Action: back + "/members", Cancel: back, Project: project})
	}
}

func adminProjectPath(project db.Project) string {
	return "/admin/projects/" + strconv.FormatInt(project.ID, 10)
}

func (w *Web) adminDeleteProject(rw http.ResponseWriter, r *http.Request) {
	project, ok := w.adminProject(rw, r)
	if !ok {
		return
	}

	if !strings.EqualFold(strings.TrimSpace(r.PostFormValue("confirm")), project.Key) {
		w.redirectError(rw, r, "/admin/projects/"+strconv.FormatInt(project.ID, 10), "Type the project's key to delete it.")
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
