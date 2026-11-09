package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

// taskForm is the page that creates or edits a task, with the choices its
// fields offer.
type taskForm struct {
	Form   url.Values
	Action string
	Cancel string
	Submit string

	Catalog *store.Catalog
	Members []db.ListMembersRow
	Parents []issueRow
}

// loadTaskForm gathers a project's assignable members and the tasks that can
// be a parent, leaving out the task being edited.
func (w *Web) loadTaskForm(r *http.Request, key string, form url.Values, editing int64) (taskForm, error) {
	ctx := r.Context()
	page := taskForm{Form: form, Catalog: w.Catalog}

	row, err := w.Store.GetMembershipByKey(ctx, db.GetMembershipByKeyParams{PrincipalID: currentUser(ctx).ID, Key: key})
	if err != nil {
		return page, err
	}

	members, err := w.Store.ListMembers(ctx, row.Project.ID)
	if err != nil {
		return page, err
	}

	page.Members = slices.DeleteFunc(members, func(m db.ListMembersRow) bool { return m.Principal.DisabledAt != nil })

	issues, err := w.Store.VisibleIssues(ctx, db.VisibleIssuesParams{PrincipalID: currentUser(ctx).ID, Key: &key})
	if err != nil {
		return page, err
	}

	slices.SortFunc(issues, func(a, b db.VisibleIssuesRow) int { return int(a.Issue.Number - b.Issue.Number) })

	for _, i := range issues {
		if i.Issue.ID != editing {
			page.Parents = append(page.Parents, w.issueRow(i))
		}
	}

	return page, nil
}

// taskFields is what the task form says about a task, checked against the
// catalog and the project.
type taskFields struct {
	Summary     string
	Description string
	StatusID    int32
	TypeID      int32
	PriorityID  *int32
	Assignee    *store.Person
	Parent      *store.Parent
	Labels      []string
}

// parse checks the form's values, returning the first problem as a sentence
// for the form to show.
func (page taskForm) parse(issues []db.VisibleIssuesRow) (taskFields, string) {
	form := page.Form

	f := taskFields{
		Summary:     strings.TrimSpace(form.Get("summary")),
		Description: strings.ReplaceAll(form.Get("description"), "\r\n", "\n"),
		Labels:      splitLabels(form.Get("labels")),
	}

	if f.Summary == "" {
		return f, "A task needs a summary."
	}

	status, err := page.Catalog.Status(form.Get("status"))
	if err != nil {
		return f, "Choose a status."
	}

	f.StatusID = status.ID

	issueType, err := page.Catalog.IssueType(form.Get("type"))
	if err != nil {
		return f, "Choose a type."
	}

	f.TypeID = issueType.ID

	if value := form.Get("priority"); value != "" {
		p, err := page.Catalog.Priority(value)
		if err != nil {
			return f, "Choose a priority, or none."
		}

		f.PriorityID = &p.ID
	}

	if value := form.Get("assignee"); value != "" {
		i := slices.IndexFunc(page.Members, func(m db.ListMembersRow) bool { return m.Principal.Username == value })
		if i < 0 {
			return f, value + " isn't a member of the project, so the task can't be assigned to them."
		}

		f.Assignee = &store.Person{MembershipID: page.Members[i].Membership.ID, Username: value}
	}

	if value := form.Get("parent"); value != "" {
		i := slices.IndexFunc(issues, func(row db.VisibleIssuesRow) bool { return issueKey(row) == value })
		if i < 0 {
			return f, "The parent must be a task in this project."
		}

		f.Parent = &store.Parent{ID: issues[i].Issue.ID, Number: issues[i].Issue.Number}
	}

	return f, ""
}

// splitLabels reads labels separated by commas or spaces, since labels can't
// contain spaces.
func splitLabels(value string) []string {
	return store.CleanLabels(strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }))
}

func issueKey(row db.VisibleIssuesRow) string {
	return fmt.Sprintf("%s-%d", row.ProjectKey, row.Issue.Number)
}

func (w *Web) newTaskForm(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.membership(rw, r)
	if !ok {
		return
	}

	form := url.Values{
		"status": {strconv.Itoa(int(w.Catalog.Statuses[0].ID))},
		"type":   {strconv.Itoa(int(w.Catalog.IssueTypes[0].ID))},
	}

	// A New task link from a task's page offers to make the new one its child.
	if parent := r.URL.Query().Get("parent"); parent != "" {
		form.Set("parent", strings.ToUpper(parent))
	}

	w.renderTaskForm(rw, r, http.StatusOK, row.Membership.Key, "New task", "", form)
}

// renderTaskForm shows the task form for creating a task in a project, or,
// with a key, editing that task, with a problem if the last try had one.
func (w *Web) renderTaskForm(rw http.ResponseWriter, r *http.Request, status int, projectKey, title, problem string, form url.Values) {
	page, err := w.loadTaskForm(r, projectKey, form, editingID(form))
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	page.Action = "/projects/" + projectKey + "/tasks"
	page.Cancel = page.Action
	page.Submit = "Create task"

	if key := form.Get("key"); key != "" {
		page.Action = "/browse/" + key + "/edit"
		page.Cancel = "/browse/" + key
		page.Submit = "Save changes"
	}

	if problem != "" {
		w.fail(rw, r, "task_form", title, problem, page)
		return
	}

	w.render(rw, r, status, "task_form", title, page)
}

// editingID is the ID of the task an edit form is for, or 0 for a new task.
func editingID(form url.Values) int64 {
	id, _ := strconv.ParseInt(form.Get("id"), 10, 64)
	return id
}

func (w *Web) createTask(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	row, ok := w.membership(rw, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		w.renderTaskForm(rw, r, http.StatusBadRequest, row.Membership.Key, "New task", "The form didn't arrive whole. Try again.", url.Values{})
		return
	}

	form := r.PostForm
	form.Del("key")
	form.Del("id")

	page, err := w.loadTaskForm(r, row.Membership.Key, form, 0)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	issues, err := w.Store.VisibleIssues(ctx, db.VisibleIssuesParams{PrincipalID: currentUser(ctx).ID, Key: &row.Membership.Key})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	fields, problem := page.parse(issues)
	if problem != "" {
		w.renderTaskForm(rw, r, http.StatusBadRequest, row.Membership.Key, "New task", problem, form)
		return
	}

	created, err := w.Store.CreateIssue(ctx, store.NewIssue{
		ProjectID:   row.Project.ID,
		Summary:     fields.Summary,
		Description: fields.Description,
		StatusID:    fields.StatusID,
		TypeID:      fields.TypeID,
		PriorityID:  fields.PriorityID,
		Reporter:    row.Membership.ID,
		Assignee:    fields.Assignee,
		Parent:      fields.Parent,
		Labels:      fields.Labels,
	})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	key := fmt.Sprintf("%s-%d", row.Membership.Key, created.Number)
	w.redirect(rw, r, "/browse/"+key, "Created "+key+".")
}
