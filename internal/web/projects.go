package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

type projectsPage struct {
	Projects []db.ListMyProjectsRow
}

func (w *Web) projects(rw http.ResponseWriter, r *http.Request) {
	projects, err := w.Store.ListMyProjects(r.Context(), currentUser(r.Context()).ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.render(rw, r, http.StatusOK, "projects", "Projects", projectsPage{Projects: projects})
}

func (w *Web) createProject(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(ctx)

	project, err := w.Store.CreateProject(ctx, user.ID, r.PostFormValue("key"), r.PostFormValue("name"))
	if errors.Is(err, store.ErrInvalidKey) {
		w.redirectError(rw, r, "/projects", capitalize(err.Error())+".")
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	membership, err := w.Store.GetMembership(ctx, db.GetMembershipParams{ProjectID: project.ID, PrincipalID: user.ID})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	note := ""
	if membership.Key != project.Key {
		note = " You already use " + project.Key + " for another project, so you see this one as " + membership.Key + "."
	}

	w.redirect(rw, r, "/projects/"+membership.Key, "Created "+project.Name+"."+note)
}

// membership finds the signed-in user's project by their key for it.
func (w *Web) membership(rw http.ResponseWriter, r *http.Request) (db.GetMembershipByKeyRow, bool) {
	row, err := w.Store.GetMembershipByKey(r.Context(), db.GetMembershipByKeyParams{
		PrincipalID: currentUser(r.Context()).ID,
		Key:         r.PathValue("key"),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		w.notFound(rw, r)
		return row, false
	}

	if err != nil {
		w.serverError(rw, r, err)
		return row, false
	}

	return row, true
}

// adminMembership is membership for actions only project administrators take.
func (w *Web) adminMembership(rw http.ResponseWriter, r *http.Request) (db.GetMembershipByKeyRow, bool) {
	row, ok := w.membership(rw, r)
	if ok && row.Membership.Role != store.RoleAdmin {
		w.notFound(rw, r)
		return row, false
	}

	return row, ok
}

type projectPage struct {
	Membership db.Membership
	Project    db.Project
	Issues     int64
	Members    []db.ListMembersRow

	// Admins counts the active administrators, so the page can keep the last
	// one from leaving or stepping down.
	Admins int
}

func (w *Web) project(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.membership(rw, r)
	if !ok {
		return
	}

	members, err := w.Store.ListMembers(r.Context(), row.Project.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	mine, err := w.Store.ListMyProjects(r.Context(), currentUser(r.Context()).ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	page := projectPage{Membership: row.Membership, Project: row.Project, Members: members}

	for _, m := range members {
		if m.Membership.Role == store.RoleAdmin && m.Principal.DisabledAt == nil {
			page.Admins++
		}
	}

	for _, p := range mine {
		if p.Project.ID == row.Project.ID {
			page.Issues = p.Issues
		}
	}

	w.render(rw, r, http.StatusOK, "project", row.Project.Name, page)
}

func (w *Web) changeKey(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.membership(rw, r)
	if !ok {
		return
	}

	err := w.Store.SetMemberKey(r.Context(), row.Project.ID, currentUser(r.Context()).ID, r.PostFormValue("key"))
	if errors.Is(err, store.ErrInvalidKey) || errors.Is(err, store.ErrKeyTaken) {
		w.redirectError(rw, r, "/projects", capitalize(err.Error())+".")
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, "/projects", "Changed your key for "+row.Project.Name+". Its issue keys changed with it.")
}

func (w *Web) renameProject(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.adminMembership(rw, r)
	if !ok {
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		w.redirectError(rw, r, "/projects/"+row.Membership.Key, "A project needs a name.")
		return
	}

	if err := w.Store.RenameProject(r.Context(), db.RenameProjectParams{ID: row.Project.ID, Name: name}); err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, "/projects/"+row.Membership.Key, "Renamed the project.")
}

func (w *Web) addMember(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.adminMembership(rw, r)
	if !ok {
		return
	}

	w.addMemberTo(rw, r, row.Project, "/projects/"+row.Membership.Key)
}

// addMemberTo adds a user by username, for project and site administrators.
func (w *Web) addMemberTo(rw http.ResponseWriter, r *http.Request, project db.Project, back string) {
	ctx := r.Context()
	username := strings.TrimSpace(r.PostFormValue("username"))

	user, err := w.Store.GetPrincipalByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && user.Kind != "user") {
		w.redirectError(rw, r, back, "There's no user named "+username+". Agents join with their owner.")
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	role := store.RoleMember
	if r.PostFormValue("role") == store.RoleAdmin {
		role = store.RoleAdmin
	}

	key, err := w.Store.AddMember(ctx, project.ID, user.ID, role)
	if errors.Is(err, store.ErrLastAdmin) {
		w.redirectError(rw, r, back, lastAdmin(user.Username, project.Name))
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, back, "Added "+user.Username+" as "+role+". They see the project as "+key+".")
}

func (w *Web) changeMember(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := currentUser(ctx)

	row, ok := w.membership(rw, r)
	if !ok {
		return
	}

	// Agents follow their owner's membership, so they aren't changed here.
	user, err := w.Store.GetPrincipalByUsername(ctx, r.PathValue("username"))
	if err != nil || user.Kind != "user" {
		w.notFound(rw, r)
		return
	}

	self := user.ID == me.ID
	back := "/projects/" + row.Membership.Key

	// Anyone can leave; only administrators change other people.
	leaving := self && r.PathValue("action") == "remove"
	if row.Membership.Role != store.RoleAdmin && !leaving {
		w.notFound(rw, r)
		return
	}

	switch r.PathValue("action") {
	case "remove":
		err = w.Store.RemoveMember(ctx, row.Project.ID, user.ID)
		if self {
			back = "/projects"
		}
	case "role":
		role := store.RoleMember
		if r.PostFormValue("role") == store.RoleAdmin {
			role = store.RoleAdmin
		}

		err = w.Store.SetMemberRole(ctx, row.Project.ID, user.ID, role)
	default:
		w.notFound(rw, r)
		return
	}

	if errors.Is(err, store.ErrLastAdmin) {
		w.redirectError(rw, r, "/projects/"+row.Membership.Key, lastAdmin(user.Username, row.Project.Name))
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, back, "Saved the change to "+user.Username+".")
}

func (w *Web) deleteProject(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.adminMembership(rw, r)
	if !ok {
		return
	}

	if !strings.EqualFold(strings.TrimSpace(r.PostFormValue("confirm")), row.Membership.Key) {
		w.redirectError(rw, r, "/projects/"+row.Membership.Key, "Type the project's key to delete it.")
		return
	}

	if err := w.Store.DeleteProject(r.Context(), row.Project.ID); err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, "/projects", "Deleted "+row.Project.Name+".")
}

// lastAdmin says why a project's last administrator can't leave or step down.
func lastAdmin(username, project string) string {
	return username + " is the only administrator of " + project +
		". Make someone else an administrator first, so the project isn't left with nobody to manage it."
}
