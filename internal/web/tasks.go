package web

import (
	"errors"
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

	// Delete links to the page that deletes the task being edited, when the
	// signed-in user may delete it.
	Delete string

	// Former is the task's assignee when they're no longer an active member,
	// so the form can keep them rather than unassign the task.
	Former string
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
// for the form to show. An assignee that isn't being changed isn't checked,
// so editing a task assigned to someone who has since left the project
// doesn't fail.
func (page taskForm) parse(issues []db.VisibleIssuesRow, checkAssignee bool) (taskFields, string) {
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

	if value := form.Get("assignee"); value != "" && checkAssignee {
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

	if was := form.Get("was_assignee"); was != "" &&
		!slices.ContainsFunc(page.Members, func(m db.ListMembersRow) bool { return m.Principal.Username == was }) {
		page.Former = was
	}

	page.Action = "/projects/" + projectKey + "/tasks"
	page.Cancel = page.Action
	page.Submit = "Create task"

	if key := form.Get("key"); key != "" {
		page.Action = "/browse/" + key + "/edit"
		page.Cancel = "/browse/" + key
		page.Submit = "Save changes"

		may, err := w.mayDeleteTask(r, editingID(form))
		if err != nil {
			w.serverError(rw, r, err)
			return
		}

		if may {
			page.Delete = "/browse/" + key + "/delete"
		}
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

	fields, problem := page.parse(issues, true)
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

// taskValues are a task's fields as the form sends them.
func (w *Web) taskValues(row db.VisibleIssuesRow) url.Values {
	i := row.Issue

	form := url.Values{
		"key":         {issueKey(row)},
		"id":          {strconv.FormatInt(i.ID, 10)},
		"summary":     {i.Summary},
		"description": {i.Description},
		"status":      {strconv.Itoa(int(i.StatusID))},
		"type":        {strconv.Itoa(int(i.TypeID))},
		"labels":      {strings.Join(i.Labels, ", ")},
	}

	if i.PriorityID != nil {
		form.Set("priority", strconv.Itoa(int(*i.PriorityID)))
	}

	if row.AssigneeUsername != nil {
		form.Set("assignee", *row.AssigneeUsername)
	}

	if row.ParentNumber != nil {
		form.Set("parent", fmt.Sprintf("%s-%d", row.ProjectKey, *row.ParentNumber))
	}

	return form
}

// editedFields are the fields an edit form can change.
var editedFields = []string{"summary", "description", "status", "type", "priority", "assignee", "parent", "labels"}

func (w *Web) editTaskForm(rw http.ResponseWriter, r *http.Request) {
	row, ok := w.visibleIssue(rw, r)
	if !ok {
		return
	}

	// The form carries each field as it was, so saving changes only the
	// fields this person changed, not ones someone else changed meanwhile.
	form := w.taskValues(row)
	for _, field := range editedFields {
		form.Set("was_"+field, form.Get(field))
	}

	w.renderTaskForm(rw, r, http.StatusOK, row.ProjectKey, "Edit "+issueKey(row), "", form)
}

func (w *Web) updateTask(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	row, ok := w.visibleIssue(rw, r)
	if !ok {
		return
	}

	key := issueKey(row)
	title := "Edit " + key

	if err := r.ParseForm(); err != nil {
		w.redirectError(rw, r, "/browse/"+key+"/edit", "The form didn't arrive whole. Try again.")
		return
	}

	form := r.PostForm
	form.Set("key", key)
	form.Set("id", strconv.FormatInt(row.Issue.ID, 10))

	changed := func(field string) bool {
		if field == "description" {
			return strings.ReplaceAll(form.Get(field), "\r\n", "\n") != strings.ReplaceAll(form.Get("was_"+field), "\r\n", "\n")
		}

		if field == "labels" {
			return !slices.Equal(splitLabels(form.Get(field)), splitLabels(form.Get("was_"+field)))
		}

		return form.Get(field) != form.Get("was_"+field)
	}

	page, err := w.loadTaskForm(r, row.ProjectKey, form, row.Issue.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	issues, err := w.Store.VisibleIssues(ctx, db.VisibleIssuesParams{PrincipalID: currentUser(ctx).ID, Key: &row.ProjectKey})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	fields, problem := page.parse(issues, changed("assignee"))
	if problem != "" {
		w.renderTaskForm(rw, r, http.StatusBadRequest, row.ProjectKey, title, problem, form)
		return
	}

	var change store.IssueChange

	if changed("summary") {
		change.Summary = &fields.Summary
	}

	if changed("description") {
		change.Description = &fields.Description
	}

	if changed("status") {
		change.StatusID = &fields.StatusID
	}

	if changed("type") {
		change.TypeID = &fields.TypeID
	}

	if changed("priority") {
		change.SetPriority, change.PriorityID = true, fields.PriorityID
	}

	if changed("assignee") {
		change.SetAssignee, change.Assignee = true, fields.Assignee
	}

	if changed("parent") {
		change.SetParent, change.Parent = true, fields.Parent
	}

	if changed("labels") {
		change.Labels = &fields.Labels
	}

	actor, err := w.Store.GetMembershipByKey(ctx, db.GetMembershipByKeyParams{PrincipalID: currentUser(ctx).ID, Key: row.ProjectKey})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	err = w.Store.UpdateIssue(ctx, w.Catalog, actor.Membership.ID, row.Issue.ID, change)
	if errors.Is(err, store.ErrParentCycle) {
		w.renderTaskForm(rw, r, http.StatusBadRequest, row.ProjectKey, title,
			form.Get("parent")+" is "+key+" or one of the tasks under it, so it can't be "+key+"'s parent.", form)

		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, "/browse/"+key, "Saved "+key+".")
}
