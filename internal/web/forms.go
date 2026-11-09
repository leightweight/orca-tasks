package web

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/leightweight/orca-tasks/internal/store/db"
)

// formPage is a form on a page of its own. Form holds what was typed, so a
// form that failed shows it again rather than starting over.
type formPage struct {
	Form url.Values

	// Action is where the form posts and Cancel where its Cancel link goes,
	// for forms that more than one page uses.
	Action string
	Cancel string

	// The choices some forms offer.
	Agents   []db.Principal
	Projects []db.ListMyProjectsRow
	Project  db.Project
}

// formHas reports whether a form value, such as a checked box, was sent.
func formHas(form url.Values, key, value string) bool {
	return slices.Contains(form[key], value)
}

// keyForm is the API key form with the user's agents and projects to choose
// from.
func (w *Web) keyForm(r *http.Request, form url.Values) (formPage, error) {
	ctx := r.Context()
	user := currentUser(ctx)

	agents, err := w.Store.ListAgents(ctx, &user.ID)
	if err != nil {
		return formPage{}, err
	}

	projects, err := w.Store.ListMyProjects(ctx, user.ID)
	if err != nil {
		return formPage{}, err
	}

	return formPage{Form: form, Agents: agents, Projects: projects}, nil
}

func (w *Web) newProjectForm(rw http.ResponseWriter, r *http.Request) {
	w.render(rw, r, http.StatusOK, "project_new", "New project", formPage{})
}

func (w *Web) newKeyForm(rw http.ResponseWriter, r *http.Request) {
	page, err := w.keyForm(r, url.Values{"scope": {"all"}})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.render(rw, r, http.StatusOK, "key_new", "New API key", page)
}

func (w *Web) newAgentForm(rw http.ResponseWriter, r *http.Request) {
	w.render(rw, r, http.StatusOK, "agent_new", "New agent", formPage{})
}

func (w *Web) newUserForm(rw http.ResponseWriter, r *http.Request) {
	w.render(rw, r, http.StatusOK, "user_new", "New user", formPage{})
}

func (w *Web) newMemberForm(rw http.ResponseWriter, r *http.Request) {
	if row, ok := w.adminMembership(rw, r); ok {
		back := "/projects/" + row.Membership.Key + "/members"
		w.render(rw, r, http.StatusOK, "member_new", "Add a member", formPage{Action: back, Cancel: back, Project: row.Project})
	}
}

func (w *Web) adminNewMemberForm(rw http.ResponseWriter, r *http.Request) {
	if project, ok := w.adminProject(rw, r); ok {
		back := adminProjectPath(project)
		w.render(rw, r, http.StatusOK, "member_new", "Add a member", formPage{Action: back + "/members", Cancel: back, Project: project})
	}
}
