package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

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

	w.redirect(rw, r, "/projects/"+membership.Key+"/tasks", "Created "+project.Name+"."+note)
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
	Members    []db.ListMembersRow

	// List is the issues to show, newest change first: the open ones, or all of
	// them when All is set. Open and Total count the open ones and all of them.
	List  []issueRow
	All   bool
	Open  int
	Total int

	// Admins counts the active administrators, so the page can keep the last
	// one from leaving or stepping down.
	Admins int
}

// Admin reports whether the signed-in user administers the project.
func (p projectPage) Admin() bool { return p.Membership.Role == store.RoleAdmin }

// project sends a project's address to its tasks.
func (w *Web) project(rw http.ResponseWriter, r *http.Request) {
	if row, ok := w.membership(rw, r); ok {
		http.Redirect(rw, r, "/projects/"+row.Membership.Key+"/tasks", http.StatusSeeOther)
	}
}

func (w *Web) projectTasks(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.membership(rw, r)
	if !ok {
		return
	}

	page := projectPage{Membership: row.Membership, Project: row.Project, All: r.URL.Query().Get("show") == "all"}

	issues, err := w.Store.VisibleIssues(r.Context(), db.VisibleIssuesParams{PrincipalID: currentUser(r.Context()).ID, Key: &row.Membership.Key})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	slices.SortFunc(issues, func(a, b db.VisibleIssuesRow) int { return b.Issue.UpdatedAt.Compare(a.Issue.UpdatedAt) })

	page.Total = len(issues)

	for _, i := range issues {
		item := w.issueRow(i)
		if item.Category != store.CategoryDone {
			page.Open++
		} else if !page.All {
			continue
		}

		page.List = append(page.List, item)
	}

	w.render(rw, r, http.StatusOK, "project_tasks", "Tasks", page)
}

func (w *Web) projectMembers(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.membership(rw, r)
	if !ok {
		return
	}

	members, err := w.Store.ListMembers(r.Context(), row.Project.ID)
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

	w.render(rw, r, http.StatusOK, "project_members", "Members", page)
}

func (w *Web) projectSettings(rw http.ResponseWriter, r *http.Request) {
	if row, ok := w.membership(rw, r); ok {
		w.render(rw, r, http.StatusOK, "project_settings", "Settings", projectPage{Membership: row.Membership, Project: row.Project})
	}
}

func (w *Web) changeKey(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.membership(rw, r)
	if !ok {
		return
	}

	back := "/projects/" + row.Membership.Key + "/settings"

	key, err := store.NormalizeKey(r.PostFormValue("key"))
	if err == nil {
		err = w.Store.SetMemberKey(r.Context(), row.Project.ID, currentUser(r.Context()).ID, key)
	}

	if errors.Is(err, store.ErrInvalidKey) || errors.Is(err, store.ErrKeyTaken) {
		w.redirectError(rw, r, back, capitalize(err.Error())+".")
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, "/projects/"+key+"/settings", "Changed your key to "+key+". Its issue keys changed with it, such as "+key+"-1.")
}

func (w *Web) renameProject(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.adminMembership(rw, r)
	if !ok {
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		w.redirectError(rw, r, "/projects/"+row.Membership.Key+"/settings", "A project needs a name.")
		return
	}

	if err := w.Store.RenameProject(r.Context(), db.RenameProjectParams{ID: row.Project.ID, Name: name}); err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, "/projects/"+row.Membership.Key+"/settings", "Renamed the project.")
}

func (w *Web) addMember(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.adminMembership(rw, r)
	if !ok {
		return
	}

	w.addMemberTo(rw, r, row.Project, "/projects/"+row.Membership.Key+"/members")
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
	back := "/projects/" + row.Membership.Key + "/members"

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
		w.redirectError(rw, r, "/projects/"+row.Membership.Key+"/members", lastAdmin(user.Username, row.Project.Name))
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
		w.redirectError(rw, r, "/projects/"+row.Membership.Key+"/settings", "Type the project's key to delete it.")
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

// issueRow is an issue as a list shows it.
type issueRow struct {
	issueLink

	Type     string
	Assignee string
	Parent   string
	Updated  time.Time
}

func (w *Web) issueRow(i db.VisibleIssuesRow) issueRow {
	status := w.Catalog.StatusByID(i.Issue.StatusID)

	item := issueRow{
		Key:      fmt.Sprintf("%s-%d", i.ProjectKey, i.Issue.Number),
		Summary:  i.Issue.Summary,
		Status:   status.Name,
		Category: status.Category,
		Type:     w.Catalog.IssueTypeByID(i.Issue.TypeID).Name,
		Updated:  i.Issue.UpdatedAt,
	}

	if i.AssigneeName != nil {
		item.Assignee = *i.AssigneeName
	}

	if i.ParentNumber != nil {
		item.Parent = fmt.Sprintf("%s-%d", i.ProjectKey, *i.ParentNumber)
	}

	return item
}
